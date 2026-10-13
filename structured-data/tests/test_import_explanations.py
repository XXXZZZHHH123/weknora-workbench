from copy import deepcopy
from datetime import datetime
import json

import httpx
from openpyxl import Workbook
import pytest

from structured_data.dataset_history import describe_history
from structured_data.generic_parser import validate_mapping
from structured_data.imports import ImportManager
from structured_data.mapping_suggestions import apply_suggestion


def duplicate_workbook(tmp_path):
    path = tmp_path / '业务.xlsx'
    wb = Workbook(); ws = wb.active; ws.title = '原料价格'
    ws.append(['日期', '20天原料', '20天原料'])
    ws.append(['', '元/吨', '元/吨'])
    ws.append([datetime(2023, 1, 1), 10, 20])
    ws.append([datetime(2023, 1, 2), 11, 22]); wb.save(path)
    manager = ImportManager(tmp_path / 'data')
    result = manager.ingest(path)
    sheet = manager.sheet(result['import_id'], '原料价格')
    return manager, result, sheet


def test_equal_headers_keep_distinct_values_units_and_source_cells(tmp_path):
    manager, uploaded, sheet = duplicate_workbook(tmp_path)
    assert sheet['status'] == 'needs_review'
    preview = manager.preview(uploaded['import_id'], sheet['mapping'])
    assert preview['series_count'] == 2 and preview['observation_count'] == 4
    assert '按原表列分别保存' in preview['notice']
    result = manager.confirm(uploaded['import_id'], sheet['mapping'])
    series = manager.store.list_series(result['dataset_id'])['series']
    assert {s['metric'] for s in series} == {'20天原料'}
    assert {s['unit'] for s in series} == {'元/吨'}
    sums = {}
    for item in series:
        column = item['dimensions']['原表列']
        query = manager.store.query_series(result['dataset_id'], item['series_id'], operation='sum')
        sums[column] = query['aggregate']
        assert all(row['source_cell'].startswith(column) for row in query['rows'])
    assert sums == {'B': 21, 'C': 42}
    assert manager.store.manifest(result['dataset_id'])['observation_count'] == 4


def test_column_identity_does_not_allow_overlap_or_forged_provenance(tmp_path):
    _, _, sheet = duplicate_workbook(tmp_path)
    mapping = deepcopy(sheet['mapping'])
    mapping['blocks'][0]['columns'][0]['dimensions'] = {'原表列': 'Q'}
    with pytest.raises(ValueError, match='真实原表列'):
        validate_mapping(mapping, sheet)
    mapping = deepcopy(sheet['mapping'])
    mapping['blocks'][0]['columns'][1]['column'] = 'B'
    with pytest.raises(ValueError, match='重叠'):
        validate_mapping(mapping, sheet)


def configure(manager):
    (manager.root / 'model-settings.json').write_text(json.dumps({'chat': {
        'model': 'fixture', 'base_url': 'https://example.invalid/v1',
        'api_key': 'private-fixture-key', 'enable_thinking': False,
    }}))


def test_suggestion_honors_thinking_setting_and_retains_column_identity(tmp_path, monkeypatch):
    manager, uploaded, sheet = duplicate_workbook(tmp_path)
    configure(manager)
    def post(self, url, **kwargs):
        body = kwargs['json']
        assert body['enable_thinking'] is False and body['max_tokens'] >= 6000
        candidate = json.loads(body['messages'][1]['content'])['candidate_mapping']
        assert candidate['blocks'][0]['columns'][0]['dimensions'] == {'原表列': 'B'}
        return httpx.Response(200, request=httpx.Request('POST', url), json={'choices': [{
            'finish_reason': 'stop', 'message': {'content': json.dumps({'mapping': candidate})}}]})
    monkeypatch.setattr(httpx.Client, 'post', post)
    result = manager.suggest(uploaded['import_id'], sheet['name'])
    assert result['mapping']['blocks'][0]['columns'][1]['dimensions'] == {'原表列': 'C'}
    assert manager.get(uploaded['import_id'])['dataset_id'] is None


@pytest.mark.parametrize('reason,content,notice', [('length', '', '输出长度'), ('stop', '', '空响应')])
def test_suggestion_reports_truncation_or_empty_mapping_without_provider_secrets(tmp_path, monkeypatch, reason, content, notice):
    manager, uploaded, sheet = duplicate_workbook(tmp_path); configure(manager)
    def post(self, url, **kwargs):
        return httpx.Response(200, request=httpx.Request('POST', url), json={'choices': [{
            'finish_reason': reason, 'message': {'content': content, 'reasoning_content': 'private-fixture-key'}}]})
    monkeypatch.setattr(httpx.Client, 'post', post)
    with pytest.raises(ValueError, match=notice) as error:
        manager.suggest(uploaded['import_id'], sheet['name'])
    assert 'private-fixture-key' not in str(error.value)


def test_history_explains_coverage_changes_and_separates_source_updates():
    def version(index, sha, names, changed=()):
        return {'dataset_id': str(index), 'source_file': '业务.xlsx', 'sha256': sha,
                'imported_at': f'2026-10-{index:02}', 'sheets': [{'name': n} for n in names],
                'changed_sheets': list(changed)}
    items = [version(1, 'same', ['期货']), version(2, 'same', ['期货', '仓单']),
             version(3, 'same', ['期货', '仓单'], ['仓单']), version(4, 'new', ['期货', '仓单'])]
    describe_history(items)
    assert [v['import_event']['kind'] for v in items] == ['first_parse', 'expand_scope', 'mapping_update', 'source_update']
    assert items[1]['import_event']['summary'] == '新增可查询工作表：仓单'
    assert '原文件内容更新' in items[3]['import_event']['summary']


def test_compact_ai_edits_and_incomplete_full_responses_keep_other_source_columns(tmp_path):
    _, _, sheet = duplicate_workbook(tmp_path)
    candidate = validate_mapping(sheet['mapping'], sheet)
    patched, restored = apply_suggestion(candidate, {'column_updates': [
        {'block_index': 0, 'column': 'B', 'metric': '原料价格'}]})
    assert restored == 0
    assert patched['blocks'][0]['columns'][0]['metric'] == '原料价格'
    assert patched['blocks'][0]['columns'][1] == candidate['blocks'][0]['columns'][1]
    partial = deepcopy(candidate); partial['blocks'][0]['columns'].pop()
    merged, restored = apply_suggestion(candidate, {'mapping': partial})
    assert restored == 1 and merged == candidate
    assert len(candidate['blocks'][0]['columns']) == 2
    with pytest.raises(ValueError, match='不存在'):
        apply_suggestion(candidate, {'column_updates': [{'block_index': 0, 'column': 'Q'}]})
