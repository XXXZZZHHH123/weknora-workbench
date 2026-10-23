from copy import deepcopy
from datetime import datetime

from openpyxl import Workbook
import pytest

from structured_data.imports import ImportManager


def workbook(tmp_path):
    source = tmp_path / 'review.xlsx'
    book = Workbook()
    sheet = book.active
    sheet.title = 'ready'
    sheet.append(['日期', '数量'])
    sheet.append([datetime(2024, 1, 1), 10])
    sheet.append([datetime(2024, 1, 2), 12])
    other = book.create_sheet('bad')
    other.append(['日期', '价格'])
    for day in range(1, 42):
        other.append([datetime(2024, 2, 1), day])
    book.save(source)
    return source


def test_auto_import_isolates_bad_sheet_and_persists_exact_block_diagnostics(tmp_path):
    manager = ImportManager(tmp_path / 'data')
    result = manager.ingest(workbook(tmp_path))
    good = manager.sheet(result['import_id'], 'ready')
    bad = manager.sheet(result['import_id'], 'bad')
    assert good['import_state'] == 'imported'
    assert bad['status'] == 'needs_review' and bad['import_state'] == 'failed'
    assert bad['review_state']['blocks'][0]['status'] == 'needs_review'
    diagnostic = bad['review_state']['blocks'][0]['diagnostics'][0]
    assert diagnostic['sheet'] == 'bad' and diagnostic['block_index'] == 0
    assert diagnostic['block_id'] == 'block-0' and diagnostic['field'] == 'date_column'
    assert diagnostic['cells'] == ['A3']
    assert diagnostic['code'] == 'mapping_validation_failed'
    assert [item['sheet'] for item in manager.store.list_series(result['dataset_id'])['series']] == ['ready']
    assert not list(manager.store.datasets.glob('.import-*'))
    repeat = manager.ingest(manager.source_path(manager.get(result['import_id'])))
    assert repeat['dataset_id'] == result['dataset_id']


def test_invalid_ready_mapping_does_not_prevent_other_sheets_from_importing(tmp_path, monkeypatch):
    from structured_data import scanner
    original = scanner.scan_workbook
    def scan(*args, **kwargs):
        result = original(*args, **kwargs)
        bad = result['sheets'][1]
        bad['mapping']['blocks'][0]['columns'][0]['metric'] = ''
        bad['status'] = 'auto_ready'
        return result
    monkeypatch.setattr(scanner, 'scan_workbook', scan)
    manager = ImportManager(tmp_path / 'data')
    result = manager.ingest(workbook(tmp_path))
    assert result['dataset_id']
    bad = manager.sheet(result['import_id'], 'bad')
    assert bad['status'] == 'needs_review'
    assert bad['review_state']['blocks'][0]['diagnostics'][0]['field'] == 'metric'
    assert manager.store.manifest(result['dataset_id'])['observation_count'] == 2


def test_review_mapping_marks_good_blocks_confirmed_and_all_bad_blocks_pending(tmp_path):
    manager = ImportManager(tmp_path / 'data')
    result = manager.ingest(workbook(tmp_path))
    candidate = manager.sheet(result['import_id'], 'ready')['mapping']
    candidate['blocks'][0]['data_end_row'] = 2
    bad = deepcopy(candidate['blocks'][0])
    bad.update(data_start_row=3, data_end_row=3)
    bad['columns'][0]['metric'] = ''
    another = deepcopy(candidate['blocks'][0])
    another['date_column'] = 'Z'
    candidate['blocks'] += [bad, another]
    before = manager.get(result['import_id'])
    review = manager.review_mapping(result['import_id'], candidate)
    assert [item['status'] for item in review['blocks']] == ['confirmed', 'needs_review', 'needs_review']
    assert review['blocks'][1]['diagnostics'][0]['field'] == 'metric'
    assert review['blocks'][2]['diagnostics'][0]['field'] == 'date_column'
    assert manager.get(result['import_id']) == before


def test_sheet_level_error_keeps_independently_validated_blocks_confirmed(tmp_path, monkeypatch):
    from structured_data import generic_parser
    manager = ImportManager(tmp_path / 'data')
    result = manager.ingest(workbook(tmp_path))
    sheet = manager.sheet(result['import_id'], 'ready')
    mapping = deepcopy(sheet['mapping'])
    good = mapping['blocks'][0]
    bad = deepcopy(good)
    good['name'], bad['name'] = 'good', 'bad'
    mapping['blocks'] = [good, bad]

    def parse_one(_path, mappings, *, observations, issues, scan):
        block = mappings[0]['blocks'][0]
        if block['name'] == 'bad':
            raise ValueError('bad block without an original block target')
        observations.append({'block_index': 0, 'value': 1, 'value_status': 'valid'})
        return {'series': [{'block_index': 0}]}

    monkeypatch.setattr(generic_parser, 'parse_mapped_workbook', parse_one)
    review = manager._review_failed(manager.get(result['import_id']), sheet, mapping,
                                    ValueError('sheet-level failure'))
    assert [item['status'] for item in review['blocks']] == ['confirmed', 'needs_review']
    assert review['blocks'][1]['diagnostics'][0]['block_index'] == 1


def test_confirm_all_does_not_approve_pending_candidates_or_create_duplicate_versions(tmp_path):
    manager = ImportManager(tmp_path / 'data')
    result = manager.ingest(workbook(tmp_path))
    dataset = result['dataset_id']
    repeated = manager.confirm_all(result['import_id'])
    assert repeated['dataset_id'] == dataset
    assert repeated['sheets_pending'] == ['bad']
    assert repeated['sheets_imported'] == []
    assert len(manager.store.list_datasets()) == 1
    assert manager.sheet(result['import_id'], 'bad')['status'] == 'needs_review'


def test_failed_manual_confirmation_preserves_published_state_and_returns_review(tmp_path):
    manager = ImportManager(tmp_path / 'data')
    result = manager.ingest(workbook(tmp_path))
    mapping = manager.sheet(result['import_id'], 'ready')['mapping']
    mapping['blocks'][0]['columns'][0]['metric'] = ''
    before = manager.get(result['import_id'])
    with pytest.raises(ValueError) as error:
        manager.confirm(result['import_id'], mapping)
    assert error.value.review_state['blocks'][0]['diagnostics'][0]['field'] == 'metric'
    assert manager.get(result['import_id']) == before


def test_batch_failed_replacement_keeps_previous_values_and_imports_other_confirmed_sheet(tmp_path):
    manager = ImportManager(tmp_path / 'data')
    result = manager.ingest(workbook(tmp_path))
    old_id = result['dataset_id']
    old_series = manager.store.list_series(old_id)['series'][0]['series_id']
    old_rows = manager.store.query_series(old_id, old_series)['rows']
    doc = manager.get(result['import_id'])
    doc['sheets'][0]['mapping']['blocks'][0]['columns'][0]['metric'] = ''
    pending = doc['sheets'][1]
    pending['mapping']['blocks'][0].update(date_mode='none', data_start_row=2, data_end_row=42)
    pending['status'] = 'confirmed'
    manager._save(doc)
    imported = manager.confirm_all(result['import_id'])
    assert imported['sheets_imported'] == ['bad']
    assert [item['sheet'] for item in imported['sheets_failed']] == ['ready']
    assert manager.store.query_series(imported['dataset_id'], old_series)['rows'] == old_rows
    assert manager.store.query_series(old_id, old_series)['rows'] == old_rows
    assert manager.sheet(result['import_id'], 'ready')['import_state'] == 'imported'
    assert manager.store.manifest(imported['dataset_id'])['changed_sheets'] == ['bad']
    assert manager.store.manifest(imported['dataset_id'])['observation_count'] == 43
    assert not list(manager.store.datasets.glob('.import-*'))


def test_unmodified_batch_reuses_snapshot_without_copying_database(tmp_path, monkeypatch):
    from structured_data import snapshots
    manager = ImportManager(tmp_path / 'data')
    result = manager.ingest(workbook(tmp_path))
    def forbid_republication(*args, **kwargs):
        raise AssertionError('unchanged data must not copy a published database')
    monkeypatch.setattr(snapshots, 'publish_parts', forbid_republication)
    assert manager.confirm_all(result['import_id'])['dataset_id'] == result['dataset_id']


def test_batch_uses_only_explicitly_confirmed_drafts_and_continues_after_failed_sheet(tmp_path):
    manager = ImportManager(tmp_path / 'data')
    result = manager.ingest(workbook(tmp_path))
    old_id = result['dataset_id']
    bad = manager.sheet(result['import_id'], 'bad')['mapping']
    bad['blocks'][0]['columns'][0]['metric'] = ''
    good = manager.sheet(result['import_id'], 'ready')['mapping']
    good['blocks'][0]['columns'][0]['metric'] = '核对后数量'
    imported = manager.confirm_all(result['import_id'], mappings=[bad, good], save_template=False)
    assert imported['dataset_id'] != old_id
    assert imported['sheets_imported'] == ['ready']
    assert imported['sheets_pending'] == ['bad']
    assert [item['sheet'] for item in imported['sheets_failed']] == ['bad']
    problem = imported['sheets_failed'][0]['review_state']['blocks'][0]['diagnostics'][0]
    assert problem['field'] == 'metric'
    assert manager.store.list_series(imported['dataset_id'])['series'][0]['metric'] == '核对后数量'
    assert manager.store.list_series(old_id)['series'][0]['metric'] == '数量'
    assert not manager.list_templates()


def test_batch_duplicate_or_invalid_sheet_drafts_are_rejected_before_publishing(tmp_path):
    manager = ImportManager(tmp_path / 'data')
    result = manager.ingest(workbook(tmp_path))
    mapping = manager.sheet(result['import_id'], 'ready')['mapping']
    before = manager.get(result['import_id'])
    for drafts in ([mapping, mapping], [None], 'not-a-list', [{'sheet': 'missing'}]):
        with pytest.raises(ValueError):
            manager.confirm_all(result['import_id'], mappings=drafts)
        assert manager.get(result['import_id']) == before
