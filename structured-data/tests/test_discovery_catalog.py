"""Discovery stays bounded, source-scoped, and independent of workbook parsing."""
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
from pathlib import Path
import threading

from openpyxl import Workbook
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
    assert len(item['unmapped_sheets']) == 80
    assert item['unmapped_sheet_count'] == 300 and item['unmapped_sheets_truncated']
    assert len(json.dumps(result, ensure_ascii=False)) < 32000
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


@pytest.mark.parametrize('columns,filename,dates', [
    (('combined', 'north'), 'synthetic-stock.xlsx', ['2024-01-01', '2024-02-01', '2024-03-01', '2024-04-01']),
    (('north', 'combined'), 'reordered-ledger.xlsx', ['2025-07-01', '2025-08-01', '2025-09-01', '2025-10-01']),
])
def test_inventory_definitions_and_last_nonempty_observation_survive_discovery(
        tmp_path, monkeypatch, columns, filename, dates):
    from structured_data.source_inspection import inspect_source

    store = DataStore(tmp_path / 'data')
    definitions = {
        'combined': ('两处仓库合计库存', '北仓和南仓合计', '吨', [12, 15, None, None]),
        'north': ('北仓库存', '仅包含北仓', None, [4, None, None, None]),
    }
    workbook = Workbook()
    sheet = workbook.active
    sheet.title = '库存表'
    sheet.append(['日期', *(definitions[column][0] for column in columns)])
    for index, date in enumerate(dates):
        sheet.append([date, *(definitions[column][3][index] for column in columns)])
    source = tmp_path / filename
    workbook.save(source)

    series, observations, header_cells = [], [], {}
    for column, identifier in enumerate(columns, start=2):
        source_metric, description, unit, _ = definitions[identifier]
        header = sheet.cell(1, column).coordinate
        header_cells[identifier] = [header]
        series.append({'series_id': identifier, 'sheet': sheet.title, 'metric': '库存',
                       'source_metric': source_metric, 'description': description,
                       'dimensions': {'scope': identifier}, 'header_cells': [header],
                       'unit': unit, 'unit_status': 'explicit' if unit else 'needs_review'})
        for row, date in enumerate(dates, start=2):
            cell = sheet.cell(row, column)
            observations.append({'series_id': identifier, 'date': date, 'value': cell.value,
                                 'source_cell': cell.coordinate,
                                 'value_status': 'valid' if cell.value is not None else 'blank'})
    workbook.close()
    with monkeypatch.context() as old_version:
        old_version.setattr(discovery, 'VERSION', 'discovery-headers-v2')
        dataset = store.publish({
            'source_file': str(source), 'sha256': hashlib.sha256(source.read_bytes()).hexdigest(),
            'rule_version': 'synthetic-v1', 'dataset_kind': 'business',
            'sheets': [{'name': sheet.title, 'series_count': len(series), 'observations': len(observations)}],
            'series': series, 'observations': observations, 'excluded_regions': [],
        })['dataset_id']
    path = store._path(dataset) / 'discovery.sqlite'
    old_index = path.stat().st_ino
    result = discovery.list_datasets(store, search='库存')
    assert path.stat().st_ino != old_index  # A previous index is rebuilt for the current projection.
    assert result['source_data_read'] is False
    candidates = {field['series_id']: field for field in result['field_candidates']}
    listed = {field['series_id']: field for field in store.list_series(dataset)['series']}
    for identifier, expected_last in [('combined', dates[1]), ('north', dates[0])]:
        field = candidates[identifier]
        for key in ('description', 'source_metric', 'dimensions', 'header_cells', 'unit', 'unit_status',
                    'last_date', 'last_valid_date'):
            assert field[key] == listed[identifier][key]
        assert field['source_file'] == filename
        assert field['last_date'] == dates[-1]
        assert field['last_valid_date'] == expected_last
        assert field['header_cells'] == header_cells[identifier]
        inspected = inspect_source(store, dataset, sheet=field['sheet'], ranges=field['header_cells'])
        assert inspected['ranges'][0]['cells'][0]['value'] == field['source_metric']
    assert candidates['north']['unit'] is None
    assert candidates['north']['unit_status'] == 'needs_review'
    assert candidates['combined']['unit'] == '吨'


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


@pytest.mark.parametrize('arguments', [{'question': '请查看Pending-299中的字段'},
                                       {'search': 'Pending-299'}])
def test_unmapped_sheet_literal_recall_ranks_late_candidate_without_hiding_others(tmp_path, arguments):
    store = DataStore(tmp_path)
    dataset = publish(store, count=1, sheet_count=300)
    result = discovery.list_datasets(store, **arguments)
    item = result['datasets'][0]
    assert item['dataset_id'] == dataset
    assert item['unmapped_sheets'][0]['name'] == 'Pending-299'
    assert item['unmapped_sheet_count'] == 300 and item['unmapped_sheets_truncated']
    assert len(item['unmapped_sheets']) == item['unmapped_sheet_limit'] == 80
    assert all(state['status'] == 'needs_review'
               and state['reasons'] == ['header review']
               and state['structured_query_available'] is False
               and '不能用结构化查询取数' in state['next_step']
               and 'inspect_source' in state['next_step'] for state in item['unmapped_sheets'])
    # Management-page pending fields retain their separate meaning and order.
    assert store.list_datasets()[0]['pending_sheets'][0] == 'Pending-0'


def test_unmapped_sheets_preserve_known_bounds_and_legacy_entire_sheet_exclusions(tmp_path):
    store = DataStore(tmp_path)
    dataset = publish(store)
    path = store._path(dataset) / 'manifest.json'
    manifest = json.loads(path.read_text())
    manifest['coverage'] = [
        {'name': '待核对表', 'status': 'needs_review', 'reasons': ['组合表头'],
         'rows': 12, 'columns': 5, 'source_range': 'A1:E12'},
        {'name': '尚未导入表', 'status': 'auto_ready', 'reasons': ['未发布字段']},
        {'name': 'Intentionally empty', 'status': 'empty', 'reasons': []}]
    manifest['excluded_regions'] = [
        {'sheet': '旧版未映射表', 'range': 'entire_sheet', 'reason': '需要核对映射'},
        {'sheet': 'Intentionally empty', 'range': 'entire_sheet', 'reason': ''},
        {'sheet': 'Sheet-0', 'range': 'entire_sheet', 'reason': '已有已发布字段'}]
    path.write_text(json.dumps(manifest, ensure_ascii=False))
    item = discovery.list_datasets(store)['datasets'][0]
    states = {state['name']: state for state in item['unmapped_sheets']}
    assert set(states) == {'待核对表', '尚未导入表', '旧版未映射表'}
    assert (states['待核对表']['rows'], states['待核对表']['columns'],
            states['待核对表']['source_range']) == (12, 5, 'A1:E12')
    assert states['旧版未映射表']['status'] == 'unmapped'
    assert states['旧版未映射表']['reasons'] == ['需要核对映射']
    assert 'rows' not in states['尚未导入表']  # Unknown dimensions are not invented.
    assert '尚未导入' in states['尚未导入表']['next_step']
    assert '字段映射待确认' not in states['尚未导入表']['next_step']
    result = store.list_series(dataset, sheet='旧版未映射表')
    assert result['match_mode'] == 'pending_scope' and result['series'] == []
    assert result['catalog_summary']['unmapped_sheets'][0]['name'] == '旧版未映射表'


def test_unmapped_catalog_summary_keeps_scope_and_raw_inspection_only(tmp_path):
    from structured_data.source_inspection import SourceVerificationError, inspect_source
    store = DataStore(tmp_path / 'data')
    workbook = Workbook()
    imported = workbook.active
    imported.title = 'Mapped'
    imported.append(['日期', '观察值'])
    imported.append(['2026-01-01', 7])
    pending = workbook.create_sheet('待确认组合表')
    pending.append(['组合标签', '第一项', '第二项'])
    pending.append(['甲', 3, 4])
    source = tmp_path / 'synthetic.xlsx'
    workbook.save(source)
    workbook.close()
    dataset = store.publish({
        'source_file': str(source), 'sha256': hashlib.sha256(source.read_bytes()).hexdigest(),
        'rule_version': 'synthetic-v1', 'dataset_kind': 'business',
        'sheets': [{'name': 'Mapped', 'series_count': 1, 'observations': 1,
                    'parsed_range': 'A1:B2'}],
        'series': [{'series_id': 'observed', 'sheet': 'Mapped', 'metric': '观察值',
                    'unit': None, 'dimensions': {}, 'header_cells': ['B1']}],
        'observations': [{'series_id': 'observed', 'date': '2026-01-01', 'value': 7,
                          'source_cell': 'B2', 'value_status': 'valid'}],
        'excluded_regions': [{'sheet': pending.title, 'range': 'entire_sheet',
                              'reason': '组合表头待确认'}], 'issues': [],
        'coverage': [{'name': pending.title, 'status': 'needs_review',
                      'reasons': ['组合表头待确认'], 'rows': 2, 'columns': 3}]
    })['dataset_id']
    directory = store.list_series(dataset)
    summary = directory['catalog_summary']
    assert directory['total'] == summary['series_count'] == 1
    assert summary['unmapped_sheet_count'] == 1 and not summary['unmapped_sheets_truncated']
    assert summary['unmapped_sheets'][0]['name'] == pending.title
    assert summary['groups'] == [{'sheet': 'Mapped', 'metric': '观察值', 'series_count': 1}]
    target = store.list_series(dataset, sheet=pending.title)
    assert target['series'] == [] and target['total'] == 0
    assert target['catalog_summary']['unmapped_sheets'][0]['structured_query_available'] is False
    with pytest.raises(ValueError, match='指标不存在'):
        store.query_series(dataset, pending.title)
    # Discovery creates no field IDs or rows for the pending sheet. The same
    # preserved original remains readable only through verified source access.
    raw = inspect_source(store, dataset, sheet=pending.title, ranges=['B2:C2'])
    assert [cell['value'] for cell in raw['ranges'][0]['cells']] == [3, 4]
    assert raw['hash_verified'] is True and raw['sha256'] == store.manifest(dataset)['sha256']
    original = store._path(dataset) / 'source.xlsx'
    original.write_bytes(original.read_bytes() + b'changed')
    with pytest.raises(SourceVerificationError):
        inspect_source(store, dataset, sheet=pending.title, ranges=['B2:C2'])


def test_group_summary_bounds_unmapped_candidates_and_ranks_search_without_changing_fields(tmp_path):
    store = DataStore(tmp_path)
    dataset = publish(store, count=2, sheet_count=100)
    path = store._path(dataset) / 'series.json'
    fields = json.loads(path.read_text())
    fields[1]['metric'] = 'Pending-99 Measure-1'
    path.write_text(json.dumps(fields))
    result = store.list_series(dataset, search='Pending-99 Measure-1')
    summary = result['catalog_summary']
    assert summary['unmapped_sheets'][0]['name'] == 'Pending-99'
    assert summary['unmapped_sheet_count'] == 100 and summary['unmapped_sheets_truncated']
    assert len(summary['unmapped_sheets']) == 80
    assert [(row['series_id'], row['sheet']) for row in result['series']] == [('field-1', 'Sheet-1')]
    assert summary['series_count'] == 1  # Pending label recall does not replace published matches.


@pytest.mark.parametrize('arguments', [{'limit': 0}, {'limit': 21}, {'limit': True},
    {'offset': -1}, {'offset': True}, {'latest_only': 'false'}])
def test_discovery_validates_page_inputs(tmp_path, arguments):
    with pytest.raises(ValueError):
        discovery.list_datasets(DataStore(tmp_path), **arguments)
