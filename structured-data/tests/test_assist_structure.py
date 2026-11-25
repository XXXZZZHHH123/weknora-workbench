"""Replay representative workbook layouts through the real parser and review API."""
from datetime import datetime
from io import BytesIO

from openpyxl import Workbook
from openpyxl.chart import LineChart, Reference
import pytest

from structured_data.ai_review import _validate_shape
from structured_data.assist_structure import prepare_assist
from structured_data.generic_parser import parse_mapped_workbook
from structured_data.imports import ImportManager

from test_review_api import client, poll


def _save(book):
    raw = BytesIO()
    book.save(raw)
    return raw.getvalue()


def _ingest(tmp_path, book):
    path = tmp_path / 'shape.xlsx'
    path.write_bytes(_save(book))
    manager = ImportManager(tmp_path / 'state')
    result = manager.ingest(path, use_reviewed_mappings=False)
    return manager, result['import_id']


def _add_chart(on, source, columns, first, last, axis=1):
    chart = LineChart()
    for col in columns:
        chart.add_data(Reference(source, min_col=col, min_row=first, max_row=last))
    chart.set_categories(Reference(source, min_col=axis, min_row=first, max_row=last))
    on.add_chart(chart, 'F30')


def _upload(client, book):
    result = poll(client, client.post('/api/import', files={'file': ('shape.xlsx', _save(book))}), timeout=30)
    return '/api/imports/' + result['import_id']


def _assist(client, url, sheet):
    return poll(client, client.post(url + '/assist-confirm', json={'sheet': sheet}), timeout=45)


def test_redundant_index_requires_exact_json_integer_and_no_unknown_keys():
    response = {'mapping': {'sheet': '换算页', 'fingerprint': 'same', 'blocks': [
        {'block_index': 0, 'header_rows': [], 'columns': [{'column': 'B'}]},
        {'block_index': 1, 'header_rows': [], 'columns': [{'column': 'B'}]},
    ]}}
    _validate_shape(response)
    assert [set(b) for b in response['mapping']['blocks']] == [{'header_rows', 'columns'}] * 2
    for invalid in (False, 1, '0', 0.0, None):
        response['mapping']['blocks'][0]['block_index'] = invalid
        with pytest.raises(ValueError, match='编号与实际位置不符'):
            _validate_shape(response)
    response['mapping']['blocks'][0].update(block_index=0, data=[1625])
    with pytest.raises(ValueError, match='第 1 个区块包含不支持'):
        _validate_shape(response)


@pytest.mark.parametrize('layered', [False, True])
def test_repeated_header_creates_independent_dated_candidate(tmp_path, layered):
    book = Workbook()
    ws = book.active
    ws.title = '汇总'
    axis_start = 3 if layered else 2
    if layered:
        ws.append(['整体', '2010/11年度', None])
        ws.merge_cells('B1:C1')
        ws.append([None, '期初库存', '产量'])
    else:
        ws.append(['合计', '周数', '15/16'])
    ws.append([datetime(2020, 1, 1), 110, 200])
    ws.append([datetime(2020, 2, 1), 111, 201])
    if layered:
        ws.append(['某地区', '2010/11年度', None])
        ws.merge_cells('B5:C5')
        ws.append([None, '期初库存', '产量'])
    else:
        ws.append(['某地区', '周数', '15/16'])
    ws.append([datetime(2020, 1, 1), 330, 400])
    ws.append([datetime(2020, 2, 1), 331, 401])
    manager, import_id = _ingest(tmp_path, book)
    sheet = manager.sheet(import_id, ws.title)
    result = prepare_assist(manager, import_id, sheet['mapping'])
    first, second = result['mapping']['blocks']
    assert first['data_start_row'] == axis_start
    assert first['data_end_row'] == axis_start + 1
    assert second['header_rows'] == ([5, 6] if layered else [4])
    assert second['data_start_row'] == (7 if layered else 5)
    assert second['name'] == '某地区'
    parsed = parse_mapped_workbook(manager.source_path(manager.get(import_id)), [result['mapping']],
                                  scan=manager.get(import_id))
    assert len(parsed['series']) == 4
    assert {row['source_cell']: row['value'] for row in parsed['observations']}[f'B{second["data_start_row"]}'] == 330


def test_text_axis_with_numerics_stays_a_real_date_error(tmp_path):
    book = Workbook()
    ws = book.active
    for row in (['时间', '库存', '产量'], [datetime(2020, 1, 1), 1, 2],
                [datetime(2020, 2, 1), 3, 4], ['最终', 500, 600],
                [datetime(2020, 3, 1), 7, 8]):
        ws.append(row)
    manager, import_id = _ingest(tmp_path, book)
    mapping = manager.sheet(import_id, ws.title)['mapping']
    assert prepare_assist(manager, import_id, mapping)['mapping'] == mapping
    assert manager.review_mapping(import_id, mapping)['diagnostics'][0]['cells'] == ['A4', 'B4']


def test_partial_publication_keeps_a_mislabeled_named_column_pending(tmp_path):
    book = Workbook()
    ws = book.active
    ws.append(['日期', '库存', None])
    ws.append([datetime(2020, 1, 1), 5, 8])
    ws.append([datetime(2020, 2, 1), 6, 9])
    manager, import_id = _ingest(tmp_path, book)
    mapping = manager.sheet(import_id, ws.title)['mapping']
    mapping['blocks'][0]['columns'][0]['metric'] = '没有原格依据的指标'
    preparation = prepare_assist(manager, import_id, mapping)
    assert preparation['diagnostics'][0]['columns'] == ['C']
    assert preparation['blocked'][0]['cells'] == ['B1']


@pytest.mark.parametrize('batch', [False, True])
def test_side_chart_and_unnamed_columns_are_persisted_as_partial_publication(client, batch):
    book = Workbook()
    ws = book.active
    ws.title = '主表'
    ws.append(['日期', '装运', '到港', None, None, None, None])
    ws.append([datetime(2020, 1, 1), 50, 60])
    ws.append([datetime(2020, 2, 1), 51, 61, '8月', 4.32, 0.83, '=B2'])
    ws.append([datetime(2020, 3, 1), 52, 62, '9月', 3.52, 0.67])
    _add_chart(ws, ws, [5, 6], 3, 4, axis=4)
    url = _upload(client, book)
    if batch:
        result = poll(client, client.post(url + '/assist-confirm-all', json={'acknowledged_risk': True}), timeout=45)
        assert result['sheets_imported'] == result['sheets_pending'] == ['主表']
        assert result['requests_used'] == 0
        assert result['details'][0]['status'] == 'partially_imported'
        notice = result['details'][0]['message']
    else:
        result = _assist(client, url, ws.title)
        assert result['imported'] and result['partially_imported'] and result['model_requests'] == 0
        assert result['review_state']['status'] == 'needs_review'
        notice = result['notice']
    assert '其余 4 列缺表头，尚未选作指标' in notice
    assert 'D3:F4 是旁边的小表' in notice
    saved = client.get(url + '/sheet', params={'name': ws.title}).json()
    assert saved['partially_imported'] and saved['published_in_dataset']
    assert saved['published_quality']['series_count'] == 2
    assert [c['column'] for c in saved['published_mapping']['blocks'][0]['columns']] == ['B', 'C']
    assert saved['partial_review']['source_diagnostics'][0]['columns'] == ['D', 'E', 'F', 'G']
    preview = poll(client, client.post(url + '/preview?background=true', json={'mapping': saved['published_mapping']}))
    assert preview['valid_observation_count'] == 6
    assert any(row['source_cell'] == 'B3' and row['value'] == 51 for row in preview['sample'])
    retried = _assist(client, url, ws.title)
    assert retried['dataset_id'] == result['dataset_id']
    assert retried['partially_imported'] and '其余 4 列缺表头' in retried['notice']


def test_annual_rows_keep_original_label_and_source_unit(tmp_path):
    book = Workbook()
    ws = book.active
    ws.title = '年度价格'
    ws.append(['目录', 'Medium Staple', 'Long Staple', 'S-6', '备注'])
    ws.append(['单位', '卢比/公担', '卢比/公担', '卢比/公担'])
    for offset in range(20):
        ws.append([f'{2000 + offset}/{offset + 1:02}', 1625 + offset, 1825 + offset,
                   5500 if offset == 19 else None])
    ws['F10'] = 5515
    manager, import_id = _ingest(tmp_path, book)
    source = manager.get(import_id)
    result = prepare_assist(manager, import_id, source['sheets'][0]['mapping'])
    block, = result['mapping']['blocks']
    assert (block['date_column'], block['data_start_row'], block['data_end_row']) == ('A', 3, 22)
    assert block['header_rows'] == [1, 2]
    assert result['diagnostics'][0]['cells'] == ['F10']
    parsed = parse_mapped_workbook(manager.source_path(source), [result['mapping']], scan=source)
    assert len(parsed['series']) == 3
    assert sum(o['value_status'] == 'valid' for o in parsed['observations']) == 41
    value = next(o for o in parsed['observations'] if o['source_cell'] == 'B3')
    assert (value['date'], value['period'], value['period_cell'], value['value']) == (None, '2000/01', 'A3', 1625)
    assert parsed['series'][0]['unit'] == '卢比/公担'
    assert parsed['series'][0]['field_metadata']['unit_evidence'][0]['cell'] == 'B2'


def test_chart_reference_skip_uses_published_source_and_preserves_independent_values(client):
    book = Workbook()
    source = book.active
    source.title = '已核对库存'
    source.append(['日期', '库存'])
    source.append([datetime(2020, 1, 1), 12])
    source.append([datetime(2020, 2, 1), 13])
    pure = book.create_sheet('曲线汇总')
    pure['A1'] = '目录'
    _add_chart(pure, source, [2], 2, 3)
    independent = book.create_sheet('曲线和自有数值')
    independent.append(['日期', '本页库存'])
    independent.append([datetime(2020, 1, 1), 42])
    independent.append([datetime(2020, 2, 1), 43])
    _add_chart(independent, source, [2], 2, 3)
    unreviewed = book.create_sheet('待核对库存')
    unreviewed.append(['类别', '库存'])
    unreviewed.append(['甲', 27])
    unreviewed.append(['乙', 28])
    waiting = book.create_sheet('依赖待核对源表')
    _add_chart(waiting, unreviewed, [2], 2, 3)
    url = _upload(client, book)
    result = _assist(client, url, pure.title)
    assert result['skipped']['source_sheets'] == [source.title]
    assert result['notice'] == '本页数据来自 「已核对库存」 表，已发布，无需重复导入。'
    assert result['model_requests'] == 0
    saved = client.get(url + '/sheet', params={'name': pure.title}).json()
    assert saved['automatic_skip']['code'] == 'published_chart_source'
    assert saved['import_state'] == 'skipped' and not saved['published_in_dataset']
    result = _assist(client, url, independent.title)
    assert result['imported'] and not result.get('skipped')
    saved = client.get(url + '/sheet', params={'name': independent.title}).json()
    assert saved['published_quality']['series_count'] == 1
    preview = poll(client, client.post(url + '/preview?background=true', json={'mapping': saved['published_mapping']}))
    assert any(row['source_cell'] == 'B2' and row['value'] == 42 for row in preview['sample'])
    result = _assist(client, url, waiting.title)
    assert result['review_state']['status'] == 'needs_review'
    assert '请先检查曲线来源' in result['notice'] and not result.get('skipped')


def test_chart_reference_cannot_skip_when_only_part_of_its_source_range_was_published(tmp_path):
    book = Workbook()
    source = book.active
    source.title = '库存'
    source.append(['日期', '库存'])
    source.append([datetime(2020, 1, 1), 12])
    source.append([datetime(2020, 2, 1), 13])
    chart_page = book.create_sheet('曲线')
    _add_chart(chart_page, source, [2], 2, 3)
    manager, import_id = _ingest(tmp_path, book)
    mapping = manager.sheet(import_id, source.title)['mapping']
    mapping['blocks'][0]['data_end_row'] = 2
    manager.confirm(import_id, mapping, save_template=False)
    page_mapping = manager.sheet(import_id, chart_page.title)['mapping']
    result = prepare_assist(manager, import_id, page_mapping)
    assert '曲线的原表和范围' in result['chart_pending']
    assert not result.get('reference')
