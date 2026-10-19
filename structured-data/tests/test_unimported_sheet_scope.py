"""A sheet present in the original but not imported is not "not found".

Sheet names and values are synthetic fixtures, not acceptance facts.
"""
import json

import pytest

from test_source_context import duplicate_source  # noqa: F401  (pytest fixture)


def _with_coverage(store, dataset, coverage):
    path = store._path(dataset) / 'manifest.json'
    manifest = json.loads(path.read_text())
    manifest['coverage'] = coverage
    manifest['sheets'] = [{'name': '原料', 'observations': 8, 'series_count': 8, 'parsed_range': 'A1:I3'}]
    path.write_text(json.dumps(manifest, ensure_ascii=False))


COVERAGE = [
    {'name': '原料', 'status': 'confirmed', 'reasons': []},
    {'name': '下游负荷', 'status': 'auto_ready', 'reasons': ['样本结构可自动解析']},
    {'name': '复杂表', 'status': 'needs_review', 'reasons': ['复杂表头']},
    {'name': '空表', 'status': 'empty', 'reasons': []},
]


def test_list_series_reports_unimported_original_sheet(duplicate_source):
    store, dataset = duplicate_source
    _with_coverage(store, dataset, COVERAGE)
    result = store.list_series(dataset, '负荷', sheet='下游负荷')
    assert result['match_mode'] == 'unimported_sheet'
    assert result['series'] == [] and result['total'] == 0
    assert result['unimported_sheet_states'] == [{'name': '下游负荷', 'status': 'auto_ready', 'reasons': ['样本结构可自动解析'], 'imported_in_this_version': False}]
    assert 'inspect_source' in result['notice'] and '不表示原件无数据' in result['notice']


def test_list_series_keeps_pending_and_truly_missing_modes(duplicate_source):
    store, dataset = duplicate_source
    _with_coverage(store, dataset, COVERAGE)
    assert store.list_series(dataset, '', sheet='复杂表')['match_mode'] == 'pending_scope'
    missing = store.list_series(dataset, '', sheet='原件中不存在的表')
    assert missing['match_mode'] == 'sheet_not_found' and 'unimported_sheet_states' not in missing
    # An imported sheet is never listed as unimported.
    assert store.list_series(dataset, '', sheet='原料')['total'] > 0


def test_list_datasets_lists_unimported_states_without_changing_pending(duplicate_source):
    store, dataset = duplicate_source
    _with_coverage(store, dataset, COVERAGE)
    item = next(d for d in store.list_datasets() if d['dataset_id'] == dataset)
    assert item['pending_sheets'] == ['复杂表']
    assert [s['name'] for s in item['unimported_sheet_states']] == ['下游负荷']


def test_invalid_query_operation_explains_parameter_meaning(duplicate_source):
    store, dataset = duplicate_source
    with pytest.raises(ValueError) as error:
        store.query_series(dataset, 'B', operation='query_series')
    message = str(error.value)
    assert 'rows/latest' in message and 'query_series' in message and '未查询数值' in message
