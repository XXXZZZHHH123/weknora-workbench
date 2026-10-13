import hashlib
import json
from datetime import datetime

import pytest
from openpyxl import Workbook

from structured_data.imports import ImportManager
from structured_data.streaming import DiskRows, read_json_array, write_json_array


def source_file(tmp_path, name='业务.xlsx'):
    path = tmp_path / name
    wb = Workbook()
    ws = wb.active
    ws.title = '已导入'
    ws.append(['日期', '库存'])
    ws.append([datetime(2023, 1, 1), 100])
    ws.append([datetime(2023, 1, 2), '=NA()'])
    ws = wb.create_sheet('新增表')
    ws.append(['日期', '价格'])
    ws.append([None, '品种甲'])
    ws.append([datetime(2023, 1, 1), 10])
    ws.append([datetime(2023, 1, 2), 12])
    wb.save(path)
    return path


def test_confirmation_only_parses_changed_sheet_and_preserves_all_sources(tmp_path, monkeypatch):
    from structured_data import generic_parser
    source = source_file(tmp_path)
    manager = ImportManager(tmp_path / 'data')
    first = manager.ingest(source)
    old = first['dataset_id']
    stock = manager.store.list_series(old)['series'][0]['series_id']
    old_rows = manager.store.query_series(old, stock)['rows']
    old_hash = hashlib.sha256((manager.store._path(old) / 'data.duckdb').read_bytes()).hexdigest()
    calls = []
    original = generic_parser.parse_mapped_workbook

    def tracked(path, mappings, **kwargs):
        calls.extend(m['sheet'] for m in mappings)
        assert isinstance(kwargs['observations'], DiskRows)
        return original(path, mappings, **kwargs)

    monkeypatch.setattr(generic_parser, 'parse_mapped_workbook', tracked)
    mapping = manager.sheet(first['import_id'], '新增表')['mapping']
    result = manager.confirm(first['import_id'], mapping)
    new = result['dataset_id']
    assert set(calls) == {'新增表'}
    assert manager.store.manifest(new)['changed_sheets'] == ['新增表']
    assert manager.store.manifest(new)['parent_dataset_id'] == old
    assert manager.store.manifest(new)['observation_count'] == 4
    assert manager.store.query_series(new, stock)['rows'] == old_rows
    assert old_rows[1]['formula'] == '=NA()' and old_rows[1]['value'] is None
    assert old_rows[1]['source_cell'] == 'B3' and old_rows[1]['date_cell'] == 'A3'
    assert hashlib.sha256((manager.store._path(old) / 'data.duckdb').read_bytes()).hexdigest() == old_hash
    assert (manager.store._path(new) / 'source.xlsx').read_bytes() == source.read_bytes()
    assert not list(manager.store.datasets.glob('.import-*'))
    # Rechecking identical mappings (including browser block IDs) creates no new version.
    mapping['blocks'][0]['_review_id'] = 'another-browser-session'
    repeat = manager.confirm(first['import_id'], mapping)
    assert repeat['dataset_id'] == new
    assert len(manager.store.list_datasets()) == 2


def test_changed_mapping_replaces_rows_instead_of_appending_and_failure_is_atomic(tmp_path, monkeypatch):
    from structured_data import generic_parser
    manager = ImportManager(tmp_path / 'data')
    first = manager.ingest(source_file(tmp_path))
    mapping = manager.sheet(first['import_id'], '新增表')['mapping']
    second = manager.confirm(first['import_id'], mapping)
    mapping['blocks'][0]['columns'][0]['metric'] = '更正指标'
    third = manager.confirm(first['import_id'], mapping)
    assert manager.store.manifest(third['dataset_id'])['observation_count'] == 4
    assert manager.store.manifest(second['dataset_id'])['observation_count'] == 4
    before = len(manager.store.list_datasets())
    original = generic_parser.parse_mapped_workbook
    calls = 0

    def fail_after_preview(*args, **kwargs):
        nonlocal calls
        calls += 1
        if calls == 2:
            raise ValueError('模拟写入前失败')
        return original(*args, **kwargs)

    monkeypatch.setattr(generic_parser, 'parse_mapped_workbook', fail_after_preview)
    with pytest.raises(ValueError, match='模拟'):
        manager.confirm(first['import_id'], mapping)
    assert manager.get(first['import_id'])['dataset_id'] == third['dataset_id']
    assert len(manager.store.list_datasets()) == before
    assert not list(manager.store.datasets.glob('.import-*'))


def test_stale_review_pointer_preserves_newer_published_tables(tmp_path):
    from structured_data.imports import atomic_json
    path = source_file(tmp_path)
    from openpyxl import load_workbook
    wb = load_workbook(path)
    ws = wb.create_sheet('另一张待核对表')
    ws.append(['日期', '进口']); ws.append([None, '另一口径'])
    ws.append([datetime(2023, 1, 1), 30]); ws.append([datetime(2023, 1, 2), 40]); wb.save(path)
    manager = ImportManager(tmp_path / 'data')
    first = manager.ingest(path)
    stale_doc = manager.get(first['import_id'])
    mapping = manager.sheet(first['import_id'], '新增表')['mapping']
    published = manager.confirm(first['import_id'], mapping)
    atomic_json(manager.path(first['import_id']) / 'review.json', stale_doc)
    other = manager.sheet(first['import_id'], '另一张待核对表')['mapping']
    result = manager.confirm(first['import_id'], other)
    manifest = manager.store.manifest(result['dataset_id'])
    assert manifest['parent_dataset_id'] == published['dataset_id']
    assert manifest['observation_count'] == 6
    assert manifest['changed_sheets'] == ['另一张待核对表']
    assert manager.sheet(first['import_id'], '新增表')['status'] == 'confirmed'
    assert {s['sheet'] for s in manager.store.list_series(result['dataset_id'])['series']} == {
        '已导入', '新增表', '另一张待核对表'}


def test_explicit_acceptance_sources_are_excluded_from_default_query_and_review_lists(tmp_path):
    manager = ImportManager(tmp_path / 'data')
    business = manager.ingest(source_file(tmp_path, '设备验收业务.xlsx'))
    path = tmp_path / '中哲_范围核对验收_非行情.xlsx'
    wb = Workbook(); ws = wb.active
    ws.append(['日期', '验收值']); ws.append([datetime(2024, 1, 1), 1]); ws.append([datetime(2024, 1, 2), 2]); wb.save(path)
    sample = manager.ingest(path)
    import json, hashlib
    (manager.root / 'source-classifications.json').write_text(json.dumps({
        hashlib.sha256(path.read_bytes()).hexdigest(): {'dataset_kind':'sample'}}))
    assert manager.store.select_dataset() == business['dataset_id']
    assert [d['source_file'] for d in manager.store.list_datasets()] == ['设备验收业务.xlsx']
    assert len(manager.store.list_datasets(include_samples=True)) == 2
    assert [d['source_file'] for d in manager.list()] == ['设备验收业务.xlsx']
    assert len(manager.list(include_samples=True)) == 2
    assert manager.store.manifest(sample['dataset_id'])['observation_count'] == 2


def test_large_legacy_issue_array_can_be_replayed_without_loading_whole_file(tmp_path):
    path = tmp_path / 'issues.json'
    values = [{'sheet': '原表', 'message': '值' * 70000}, {'sheet': '新表', 'cell': 'B3'}]
    assert write_json_array(path, iter(values)) == 2
    assert list(read_json_array(path)) == values
    path.write_text('[{"sheet":"原表"}', encoding='utf-8')
    with pytest.raises(ValueError, match='Incomplete'):
        list(read_json_array(path))
