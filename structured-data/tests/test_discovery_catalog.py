"""Discovery stays bounded, source-scoped, and independent of workbook parsing."""
from concurrent.futures import ThreadPoolExecutor
import json
from pathlib import Path
import threading

import pytest

from structured_data import discovery_catalog as discovery
from structured_data.store import DataStore


def publish(store, number=0, *, name=None, count=1, sheet_count=1):
    return store.publish({
        'source_file': name or f'source-{number}.xlsx', 'sha256': f'{number:064x}',
        'rule_version': 'synthetic-v1', 'dataset_kind': 'business',
        'sheets': [{'name': f'Sheet-{i}', 'series_count': count, 'observations': count}
                   for i in range(sheet_count)],
        'series': [{'series_id': f'field-{i}', 'sheet': f'Sheet-{i % sheet_count}',
                    'metric': f'Measure-{i}', 'unit': None, 'unit_status': 'needs_review',
                    'dimensions': {'column': f'C{i}'}, 'header_cells': ['B1']}
                   for i in range(count)],
        'observations': [{'series_id': f'field-{i}', 'date': '2026-01-01', 'value': i + 1,
                          'source_cell': 'B2', 'value_status': 'valid'} for i in range(count)],
        'excluded_regions': [], 'issues': [],
        'coverage': [{'name': f'Pending-{i}', 'status': 'needs_review', 'reasons': ['header review']}
                     for i in range(sheet_count)]})['dataset_id']


def test_wide_workbook_recalls_late_sheet_without_reading_original_or_observations(tmp_path, monkeypatch):
    store = DataStore(tmp_path)
    dataset = publish(store, count=2100, sheet_count=300)
    def forbidden(*args, **kwargs):
        pytest.fail('discovery must not build/enrich full catalog or scan observations')
    monkeypatch.setattr(store, 'catalog', forbidden)
    monkeypatch.setattr(store, '_connect', forbidden)
    result = discovery.list_datasets(store, 'Measure-2099 on Sheet-299')
    assert any(row['series_id'] == 'field-2099' for row in result['field_candidates'])
    assert result['field_candidate_stage'] == 'published_headers'
    assert len(result['field_candidates']) <= discovery.FIELD_LIMIT
    item = result['datasets'][0]
    assert item['dataset_id'] == dataset
    assert item['sheets'][0] == 'Sheet-299'
    assert len(item['sheets']) == len(item['pending_sheet_states']) == 8
    assert item['sheet_count'] == item['pending_sheet_states_count'] == 300
    assert item['sheets_truncated'] and item['pending_sheet_states_truncated']
    assert len(json.dumps(result, ensure_ascii=False)) < 24000
    assert all('source_context' not in field for field in result['field_candidates'])


def test_file_pagination_keeps_all_identities_and_explicit_history(tmp_path):
    store = DataStore(tmp_path)
    old = publish(store, 0, name='revised.xlsx')
    latest = publish(store, 1, name='revised.xlsx')
    others = {publish(store, i) for i in range(2, 12)}
    pages = [discovery.list_datasets(store, limit=3, offset=i) for i in range(0, 12, 3)]
    assert {item['dataset_id'] for page in pages for item in page['datasets']} == others | {latest}
    assert [page['next_offset'] for page in pages] == [3, 6, 9, None]
    assert all(page['dataset_count'] == 11 and page['total_dataset_count'] == 12 for page in pages)
    assert old in {item['dataset_id'] for item in discovery.list_datasets(store, latest_only=False,
                                                                        limit=20)['datasets']}
    assert len(store.list_datasets()) == 12  # Management API retains its complete report.
    assert discovery.list_datasets(store, offset=100)['datasets'] == []


def test_explicit_search_only_recalls_real_metadata_and_does_not_hide_files(tmp_path):
    store = DataStore(tmp_path)
    dataset = publish(store, count=20, sheet_count=2)
    result = discovery.list_datasets(store, 'full question retained', search='Sheet-1 Measure-19')
    assert [(field['dataset_id'], field['series_id']) for field in result['field_candidates']] == [(dataset, 'field-19')]
    assert result['field_candidate_count'] == 1
    assert not result['field_candidates_truncated']
    missing = discovery.list_datasets(store, 'an unrelated question', search='no-such-header')
    assert missing['datasets'][0]['dataset_id'] == dataset
    assert missing['field_candidates'] == []
    assert '不表示没有数据' in missing['directory_notice']


def test_empty_catalog_scope_is_not_document_absence_or_another_search_retry(tmp_path):
    store = DataStore(tmp_path)
    for arguments in ({}, {'question': 'A different request', 'search': 'arbitrary field'},
                      {'latest_only': False}):
        result = discovery.list_datasets(store, **arguments)
        assert result['datasets'] == []
        assert result['dataset_count'] == result['total_dataset_count'] == 0
        assert result['next_offset'] is None and not result['datasets_truncated']
        assert result['source_data_read'] is False
        assert result['catalog_source_types'] == ['registered_spreadsheet_versions']
        assert set(result['catalog_excludes']) == {'document_body', 'presentation_slides', 'original_figures'}
        assert '不必重复同一空目录' in result['empty_catalog_notice']
        assert '本轮实际可用工具' in result['document_source_next_step']
        assert '已授权范围' in result['document_source_next_step']
    dataset = publish(store)
    page = discovery.list_datasets(store, offset=100)
    assert page['datasets'] == [] and page['total_dataset_count'] == 1
    assert 'empty_catalog_notice' not in page  # Empty page is not an empty inventory.
    assert discovery.list_datasets(store)['datasets'][0]['dataset_id'] == dataset


def test_persistent_index_reused_without_parsing_series_or_scanning_data(tmp_path, monkeypatch):
    store = DataStore(tmp_path)
    dataset = publish(store, count=40)
    original = Path.read_text
    def guarded(path, *args, **kwargs):
        if path.name == 'series.json':
            pytest.fail('already indexed metadata must not be reread on each question')
        return original(path, *args, **kwargs)
    monkeypatch.setattr(Path, 'read_text', guarded)
    for number in range(4):
        # New DataStore instances simulate worker/process reuse of disk state.
        fields, total = discovery.search_fields(DataStore(tmp_path), dataset, '', search=f'Measure-{number}')
        assert total and any(field['series_id'] == f'field-{number}' for field in fields)


def test_metadata_revision_invalidates_index_and_new_snapshot_keeps_old_searchable(tmp_path):
    store = DataStore(tmp_path)
    old = publish(store, 0, name='source.xlsx')
    folder = store._path(old)
    fields = json.loads((folder / 'series.json').read_text())
    fields[0]['metric'] = 'Corrected header'
    (folder / 'series.json').write_text(json.dumps(fields))
    assert discovery.search_fields(store, old, '', search='Corrected header')[1] == 1
    assert discovery.search_fields(store, old, '', search='Measure-0')[1] == 0
    latest = publish(store, 1, name='source.xlsx')
    assert discovery.list_datasets(store)['datasets'][0]['dataset_id'] == latest
    assert discovery.search_fields(store, old, '', search='Corrected header')[1] == 1
    assert discovery.search_fields(store, latest, '', search='Corrected header')[1] == 0


@pytest.mark.parametrize('question', ['?', '2026-01-01'])
def test_question_without_searchable_header_tokens_keeps_dataset_available(tmp_path, question):
    store = DataStore(tmp_path)
    dataset = publish(store)
    result = discovery.list_datasets(store, question)
    assert result['field_candidates'] == []
    assert result['datasets'][0]['dataset_id'] == dataset


def test_source_revision_invalidates_derived_index_without_opening_workbook(tmp_path, monkeypatch):
    store = DataStore(tmp_path)
    dataset = publish(store)
    folder = store._path(dataset)
    builds = []
    build = discovery._build
    def counted(*args):
        builds.append(1)
        return build(*args)
    monkeypatch.setattr(discovery, '_build', counted)
    # Discovery is metadata only, even when the original cannot be parsed. It
    # does not attest original integrity or authorize a query/source binding.
    (folder / 'source.xlsx').write_bytes(b'changed original')
    assert discovery.search_fields(store, dataset, '', search='Measure-0')[1] == 1
    assert builds == [1]
    discovery.search_fields(store, dataset, '', search='Measure-0')
    assert builds == [1]


def test_existing_snapshots_build_once_across_concurrent_requests_and_repair_corrupt_index(tmp_path, monkeypatch):
    store = DataStore(tmp_path)
    dataset = publish(store, count=8)
    path = store._path(dataset) / 'discovery.sqlite'
    path.unlink()
    real_build = discovery._build
    calls = []
    lock = threading.Lock()
    def counted(*args):
        with lock:
            calls.append(1)
        return real_build(*args)
    monkeypatch.setattr(discovery, '_build', counted)
    with ThreadPoolExecutor(max_workers=4) as pool:
        results = list(pool.map(lambda _: discovery.search_fields(store, dataset, 'Measure-7'), range(4)))
    assert len(calls) == 1
    assert all(any(field['series_id'] == 'field-7' for field in fields) for fields, _ in results)
    path.write_bytes(b'broken derived index')
    assert discovery.search_fields(store, dataset, '', search='Measure-7')[1] == 1
    assert len(calls) == 2


def test_same_dataset_id_in_different_workspace_does_not_reuse_other_headers(tmp_path):
    left, right = DataStore(tmp_path / 'left'), DataStore(tmp_path / 'right')
    dataset = publish(left)
    assert publish(right) == dataset
    path = right._path(dataset) / 'series.json'
    fields = json.loads(path.read_text())
    fields[0]['metric'] = 'Other workspace'
    path.write_text(json.dumps(fields))
    assert discovery.search_fields(right, dataset, '', search='Other workspace')[1] == 1
    assert discovery.search_fields(left, dataset, '', search='Other workspace')[1] == 0


@pytest.mark.parametrize('arguments', [{'limit': 0}, {'limit': 21}, {'limit': True},
    {'offset': -1}, {'offset': True}, {'latest_only': 'false'}])
def test_discovery_validates_page_inputs(tmp_path, arguments):
    with pytest.raises(ValueError):
        discovery.list_datasets(DataStore(tmp_path), **arguments)
