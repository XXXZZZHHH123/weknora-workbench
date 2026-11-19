"""Review header notes on published workbooks without calling a real model."""
from copy import deepcopy
from datetime import datetime
from io import BytesIO
import json
from threading import Event
from time import monotonic, sleep

import httpx
from openpyxl import Workbook
import pytest

from structured_data import header_cleanup, model_gateway
from test_review_api import client, poll


NOTE = '月底核对各地记录后统一修订。'
OLD_METRIC = f'产量 / 周 / {NOTE}'
SHEET_NAMES = ('本期数据', '历史数据', '已发布单位表', '已发布单行表', '待确认表')


@pytest.fixture
def workbook_import(client, tmp_path):
    book = Workbook()
    for index, name in enumerate(SHEET_NAMES):
        sheet = book.active if index == 0 else book.create_sheet()
        sheet.title = name
        sheet.append(['日期', '产量', '价格'])
        if name in ('本期数据', '历史数据', '待确认表'):
            sheet.append([None, '周'])
            sheet.append([None, NOTE])
        if name == '已发布单位表':
            sheet.append([None, '吨'])
        sheet.append([datetime(2024, 1, 1), 10, 40])
        sheet.append([datetime(2024, 2, 1), 11, 41])
    raw = BytesIO()
    book.save(raw)
    uploaded = poll(client, client.post('/api/import', files={'file': ('header-notes.xlsx', raw.getvalue())}))
    import_url = '/api/imports/' + uploaded['import_id']
    for name in ('本期数据', '历史数据'):
        sheet = read_sheet(client, import_url, name)
        assert sheet['status'] == 'needs_review'
        mapping = sheet['mapping']
        amount, price = mapping['blocks'][0]['columns']
        assert amount['metric'] == OLD_METRIC
        amount.update(frequency='周', frequency_cells=['B2'])
        price.update(metric='价格', header_cells=['C1'])
        poll(client, client.post(import_url + '/confirm', json={'mapping': mapping, 'save_template': False}))
    review_path = tmp_path / 'data' / 'imports' / uploaded['import_id'] / 'review.json'
    return import_url, review_path


def read_sheet(client, import_url, name):
    response = client.get(import_url + '/sheet', params={'name': name})
    assert response.status_code == 200, response.text
    return response.json()


def read_series(client, dataset_id):
    response = client.get(f'/api/datasets/{dataset_id}/series')
    assert response.status_code == 200, response.text
    return {(row['sheet'], row['metric']): row for row in response.json()['series']}


def suggest_roles(rows, evidence, *, unresolved=()):
    return {'block_updates': [{'block_index': 0, 'header_roles': rows}],
            'evidence': [{'block_index': 0, 'field': role, 'cells': cells} for role, cells in evidence.items()],
            'unresolved': list(unresolved)}


def suggest_notes(*, frequency_role='frequency'):
    evidence = {'name': ['A1', 'B1', 'C1'], 'note': ['B3']}
    evidence.setdefault(frequency_role, []).append('B2')
    return suggest_roles([{'row': 1, 'role': 'name'}, {'row': 2, 'role': frequency_role},
                          {'row': 3, 'role': 'note'}], evidence)


def mock_header_model(monkeypatch, propose):
    calls = []

    def complete(config, messages, **kwargs):
        payload = json.loads(messages[1]['content'])
        calls.append(payload)
        assert payload['mode'] == 'header_only'
        assert 'source_cells' not in payload
        suggestion = propose(payload['sheet'])
        content = suggestion if isinstance(suggestion, str) else json.dumps(suggestion)
        return {'choices': [{'finish_reason': 'stop', 'message': {'content': content}}]}

    monkeypatch.setattr(header_cleanup, 'read_model', lambda _: {'native_model_id': 'test-model'})
    monkeypatch.setattr(model_gateway, 'complete', complete)
    return calls


def start_cleanup(client, import_url, **settings):
    return client.post(import_url + '/header-cleanup', json=settings)


def apply_cleanup(client, import_url, job_id, *sheet_names):
    response = client.post(import_url + '/header-cleanup/apply', json={
        'job_id': job_id, 'sheet_names': list(sheet_names),
    })
    assert response.status_code == 200, response.text
    return response.json()


def test_cleanup_preview_is_read_only_and_applies_only_selected_renames(client, monkeypatch, workbook_import):
    import_url, review_path = workbook_import
    review_before = review_path.read_bytes()
    dataset_before = json.loads(review_before)['dataset_id']
    catalogue_before = read_series(client, dataset_before)
    datasets_before = client.get('/api/datasets').json()
    templates_before = client.get('/api/templates').json()
    entered, release = Event(), Event()

    def propose(sheet):
        if sheet == '本期数据':
            entered.set()
            assert release.wait(8)
        return suggest_notes() if sheet != '已发布单位表' else {'block_updates': [], 'evidence': []}

    calls = mock_header_model(monkeypatch, propose)
    response = start_cleanup(client, import_url)
    assert response.status_code == 200, response.text
    job_id = response.json()['id']
    try:
        assert entered.wait(5)
        job = client.get('/api/jobs/' + job_id).json()
        assert job['status'] == 'processing' and job['cancellable']
        assert '1/3' in job['stage']
        assert review_path.read_bytes() == review_before
        assert read_series(client, dataset_before) == catalogue_before
        assert client.get('/api/datasets').json() == datasets_before
        premature = client.post(import_url + '/header-cleanup/apply', json={
            'job_id': job_id, 'sheet_names': ['本期数据'],
        })
        assert premature.status_code == 409
    finally:
        release.set()
    result = poll(client, response)
    assert review_path.read_bytes() == review_before
    assert client.get('/api/templates').json() == templates_before
    assert client.get('/api/datasets').json() == datasets_before
    assert read_series(client, dataset_before) == catalogue_before
    assert len(calls) == result['requests_used'] == 3
    suggestions = {row['sheet_name']: row for row in result['sheets']}
    current = suggestions['本期数据']
    assert current['status'] == suggestions['历史数据']['status'] == 'rename'
    assert suggestions['已发布单位表']['status'] == 'unchanged'
    change, = current['changes']
    assert change['column_index'] == 2
    assert (change['old_name'], change['new_name']) == (OLD_METRIC, '产量')
    assert change['header_notes'] == [{'cell': 'B3', 'text': NOTE}]
    assert change['frequency'] == [{'cell': 'B2', 'text': '周'}]
    sheet_before = read_sheet(client, import_url, '本期数据')
    assert current['fingerprint'] == sheet_before['fingerprint']
    assert current['mapping_digest']
    proposed_block, = current['mapping']['blocks']
    assert proposed_block['header_note_cells'] == ['B3']
    assert proposed_block['columns'][0]['metric'] == '产量'
    assert proposed_block['columns'][0]['frequency_cells'] == ['B2']

    applied = apply_cleanup(client, import_url, job_id, '本期数据')
    applied_sheet, = applied['sheets']
    assert applied_sheet['sheet_name'] == '本期数据'
    assert applied_sheet['status'] == 'applied'
    assert applied_sheet['renamed_fields'] == applied['renamed_fields'] == 1
    for phrase in ('1 个字段', 'ID', '指标对应确认', '改写引用'):
        assert phrase in applied['notice']

    saved = read_sheet(client, import_url, '本期数据')
    assert saved['status'] == 'confirmed' and saved['published_in_dataset']
    assert saved['review_state']['status'] == 'confirmed'
    assert saved['published_mapping']['blocks'][0]['header_note_cells'] == ['B3']
    catalogue_after = read_series(client, applied['dataset_id'])
    expected_keys = catalogue_before.keys() - {('本期数据', OLD_METRIC)}
    expected_keys.add(('本期数据', '产量'))
    assert catalogue_after.keys() == expected_keys
    renamed = catalogue_after['本期数据', '产量']
    assert renamed['series_id'] != catalogue_before['本期数据', OLD_METRIC]['series_id']
    assert renamed['field_metadata']['header_notes'] == [{'cell': 'B3', 'text': NOTE}]
    assert renamed['field_metadata']['frequency_evidence'] == [{'sheet': '本期数据', 'cell': 'B2', 'text': '周'}]
    for key in catalogue_before.keys() - {('本期数据', OLD_METRIC)}:
        assert catalogue_after[key]['series_id'] == catalogue_before[key]['series_id']
    assert read_sheet(client, import_url, '历史数据')['mapping'] == next(
        sheet['mapping'] for sheet in json.loads(review_before)['sheets'] if sheet['name'] == '历史数据')
    assert client.get('/api/templates').json() == templates_before


def test_cleanup_applied_mapping_is_unchanged_on_repeat(client, monkeypatch, workbook_import):
    url, _ = workbook_import
    calls = mock_header_model(monkeypatch, lambda sheet: suggest_notes() if sheet == '本期数据'
                              else {'block_updates': []})
    started = start_cleanup(client, url)
    first = next(row for row in poll(client, started)['sheets'] if row['sheet_name'] == '本期数据')
    assert first['status'] == 'rename'
    assert apply_cleanup(client, url, started.json()['id'], '本期数据')['renamed_fields'] == 1
    saved = read_sheet(client, url, '本期数据')['published_mapping']
    assert all(len(column['header_cells']) == 1 for column in saved['blocks'][0]['columns'])

    repeated = next(row for row in poll(client, start_cleanup(client, url))['sheets']
                    if row['sheet_name'] == '本期数据')
    assert [payload['sheet'] for payload in calls].count('本期数据') == 2
    assert repeated['status'] == 'unchanged'
    assert repeated['changes'] == []
    assert repeated['mapping'] == saved


@pytest.mark.parametrize('changed', ['fingerprint', 'mapping', 'published_snapshot'])
def test_cleanup_apply_skips_changed_sheet_and_continues(client, monkeypatch, workbook_import, changed):
    import_url, review_path = workbook_import
    mock_header_model(monkeypatch, lambda sheet: suggest_notes() if sheet != '已发布单位表'
                      else {'block_updates': [], 'evidence': []})
    response = start_cleanup(client, import_url)
    poll(client, response)
    previous_review = review_path.read_bytes()

    if changed == 'fingerprint':
        doc = json.loads(previous_review)
        sheet = next(row for row in doc['sheets'] if row['name'] == '本期数据')
        sheet['fingerprint'] = 'f' * 64
        review_path.write_text(json.dumps(doc), encoding='utf-8')
    else:
        mapping = deepcopy(read_sheet(client, import_url, '本期数据')['mapping'])
        mapping['blocks'][0]['columns'][0]['metric'] = '人工核对后的名称'
        poll(client, client.post(import_url + '/confirm', json={'mapping': mapping, 'save_template': False}))
    current = read_sheet(client, import_url, '本期数据')
    if changed == 'published_snapshot':
        # Older tools can leave the review pointer behind the newest publication.
        review_path.write_bytes(previous_review)

    result = apply_cleanup(client, import_url, response.json()['id'], '本期数据', '历史数据')
    by_sheet = {row['sheet_name']: row for row in result['sheets']}
    assert by_sheet['本期数据']['status'] == 'failed'
    assert by_sheet['本期数据']['error'].rstrip('。') == '工作表已变化，请重新整理'
    assert by_sheet['历史数据']['status'] == 'applied'
    assert result['renamed_fields'] == 1
    saved = read_sheet(client, import_url, '本期数据')
    assert saved['fingerprint'] == current['fingerprint']
    assert saved['mapping'] == current['mapping']
    assert saved['published_mapping'] == current['published_mapping']


def test_cleanup_is_structural_and_contains_one_model_failure(client, monkeypatch, workbook_import):
    import_url, _ = workbook_import
    summary = client.get(import_url, params={'summary_only': True}).json()
    assert summary['header_cleanup_count'] == 3
    by_name = {sheet['name']: sheet for sheet in summary['sheets']}
    assert by_name['已发布单位表']['published_in_dataset'] and by_name['已发布单位表']['status'] == 'auto_ready'
    assert by_name['待确认表']['status'] == 'needs_review' and not by_name['待确认表']['published_in_dataset']

    def propose(sheet):
        if sheet == '本期数据':
            raise RuntimeError('internal trace marker /srv/config/private.json')
        if sheet == '已发布单位表':
            # The source has two header cells but no description to move.
            return {'block_updates': [], 'evidence': []}
        return suggest_notes(frequency_role='name')

    calls = mock_header_model(monkeypatch, propose)
    result = poll(client, start_cleanup(client, import_url))
    assert len(calls) == result['requests_used'] == 3
    assert {payload['sheet'] for payload in calls} == {'本期数据', '历史数据', '已发布单位表'}
    rows = {row['sheet_name']: row for row in result['sheets']}
    failure = rows['本期数据']
    assert failure['status'] == 'failed'
    assert any('\u4e00' <= char <= '\u9fff' for char in failure['error'])
    assert 'internal trace' not in failure['error'] and '/srv/config' not in failure['error']
    assert rows['已发布单位表']['status'] == 'unchanged'
    assert rows['历史数据']['status'] == 'rename'
    change, = rows['历史数据']['changes']
    assert change['new_name'] == '产量 / 周'
    assert rows['历史数据']['mapping']['blocks'][0]['columns'][0]['header_cells'] == ['B1', 'B2']


def test_cleanup_respects_shared_request_budget(client, monkeypatch, workbook_import):
    import_url, _ = workbook_import
    calls = mock_header_model(monkeypatch, lambda _: {'block_updates': [], 'evidence': []})
    result = poll(client, start_cleanup(client, import_url, total_max_requests=1))
    assert len(calls) == result['requests_used'] == 1
    statuses = [row['status'] for row in result['sheets']]
    assert statuses.count('unchanged') == 1
    assert statuses.count('failed') == 2


def header_model_failure(kind):
    details = 'internal trace marker /srv/model/private.json'
    request = httpx.Request('POST', 'https://model.fixture/chat/completions')
    if kind == 'timeout':
        raise httpx.ReadTimeout(details, request=request)
    if kind == 'invalid_json':
        return '{'
    status = {'server_error': 503, 'too_large': 413}[kind]
    response = httpx.Response(status, request=request)
    raise httpx.HTTPStatusError(details, request=request, response=response)


@pytest.mark.parametrize(('failure', 'category'), [
    ('timeout', '超时'), ('server_error', '模型服务暂时不可用'), ('invalid_json', '模型返回格式无效'),
])
@pytest.mark.parametrize('recover', [True, False], ids=['recover', 'exhausted'])
def test_cleanup_retries_each_transient_model_failure(client, monkeypatch, workbook_import, failure, category,
                                                      recover):
    url, _ = workbook_import

    def propose(sheet):
        if sheet != '本期数据':
            return {'block_updates': []}
        if not recover or [payload['sheet'] for payload in calls].count(sheet) == 1:
            return header_model_failure(failure)
        return suggest_notes()

    calls = mock_header_model(monkeypatch, propose)
    result = poll(client, start_cleanup(client, url))
    names = [payload['sheet'] for payload in calls]
    assert len(names) == result['requests_used'] == 4
    assert names[:2] == ['本期数据', '本期数据']
    rows = {row['sheet_name']: row for row in result['sheets']}
    row = rows['本期数据']
    if recover:
        assert row['status'] == 'rename'
        assert [change['new_name'] for change in row['changes']] == ['产量']
    else:
        assert row['status'] == 'failed'
        assert category in row['error']
        assert all(detail not in row['error'] for detail in ('internal trace', '/srv/model', 'model.fixture'))
    assert rows['历史数据']['status'] == rows['已发布单位表']['status'] == 'unchanged'


@pytest.mark.parametrize(('failure', 'budget', 'category'), [
    ('too_large', 2, '请求过大'), ('timeout', 1, '超时'),
])
def test_cleanup_forgoes_retry_on_oversize_or_exhausted_budget(client, monkeypatch, workbook_import, failure,
                                                              budget, category):
    url, _ = workbook_import
    calls = mock_header_model(monkeypatch, lambda sheet: header_model_failure(failure) if sheet == '本期数据'
                              else {'block_updates': []})
    result = poll(client, start_cleanup(client, url, total_max_requests=budget))
    names = [payload['sheet'] for payload in calls]
    assert names.count('本期数据') == 1
    assert len(names) == result['requests_used'] == budget
    row = next(row for row in result['sheets'] if row['sheet_name'] == '本期数据')
    assert row['status'] == 'failed'
    assert category in row['error']
    assert '/srv/model' not in row['error']


def test_cleanup_job_cancels_without_saving_and_skips_remaining_requests(client, monkeypatch, workbook_import):
    import_url, review_path = workbook_import
    review_before = review_path.read_bytes()
    entered, release = Event(), Event()

    def propose(_):
        entered.set()
        assert release.wait(8)
        return suggest_notes()

    calls = mock_header_model(monkeypatch, propose)
    response = start_cleanup(client, import_url)
    assert response.status_code == 200, response.text
    job_url = '/api/jobs/' + response.json()['id']
    try:
        assert entered.wait(5)
        cancelled = client.post(job_url + '/cancel')
        assert cancelled.status_code == 200, cancelled.text
        assert cancelled.json()['cancel_requested']
    finally:
        release.set()
    deadline = monotonic() + 5
    while monotonic() < deadline:
        job = client.get(job_url).json()
        if job['status'] != 'processing':
            break
        sleep(.01)
    assert job['status'] == 'cancelled'
    assert len(calls) == 1
    assert all(row['status'] != 'rename' for row in job['result']['sheets'])
    assert review_path.read_bytes() == review_before


def publish_fixture(client, book, blocks):
    raw = BytesIO()
    book.save(raw)
    uploaded = poll(client, client.post('/api/import', files={'file': ('header-roles.xlsx', raw.getvalue())}))
    url = '/api/imports/' + uploaded['import_id']
    name = book.active.title
    sheet = read_sheet(client, url, name)
    mapping = {'sheet': name, 'fingerprint': sheet['fingerprint'], 'blocks': blocks}
    poll(client, client.post(url + '/confirm', json={'mapping': mapping, 'save_template': False}))
    return url, read_sheet(client, url, name)['published_mapping']


def fixture_block(rows, columns, **extra):
    return {'header_rows': rows, 'data_start_row': rows[-1] + 1, 'data_end_row': rows[-1] + 2,
            'date_mode': 'none', 'columns': columns, **extra}


def test_cleanup_keeps_steel_note_suggestion_and_unresolved_other_block(client, monkeypatch):
    book = Workbook()
    sheet = book.active
    sheet.title = '钢联下游情况'
    names = ['开机率', '成品库存', '产量', '订单']
    descriptions = [f'{name} 的统计口径补充说明' for name in names]
    sheet.append(['钢联数据', None, None, None, None, '其他来源', '另一分区'])
    sheet.append(['日期', *names, '日期', '辅助指标'])
    sheet.append([None, '周', '周', '周', '周'])
    sheet.append(['指标描述', *descriptions])
    for day in (1, 2):
        date = datetime(2024, 1, day)
        sheet.append([date, 5, 6, 7, 8, date, 9])
    first = fixture_block([1, 2, 3, 4], [
        {'column': col, 'metric': f'{name} / 周 / {desc}', 'header_cells': [f'{col}{row}' for row in (2, 3, 4)]}
        for col, name, desc in zip('BCDE', names, descriptions)
    ], date_mode='date', date_column='A')
    second = fixture_block([1, 2, 3, 4], [
        {'column': 'G', 'metric': '另一分区 / 辅助指标', 'header_cells': ['G1', 'G2']}
    ], date_mode='date', date_column='F')
    url, previous = publish_fixture(client, book, [first, second])
    unresolved = {'block_index': 1, 'field': 'header_note_cells', 'cells': ['G1'],
                  'message': '需要核对 G1 在另一区块的用途。'}
    suggestion = suggest_roles(
        [{'row': row, 'role': role} for row, role in enumerate(('note', 'name', 'frequency', 'note'), 1)],
        {'name': [f'{col}2' for col in 'ABCDE'], 'frequency': [f'{col}3' for col in 'BCDE'],
         'note': ['A1', *(f'{col}4' for col in 'ABCDE')]}, unresolved=[unresolved],
    )
    mock_header_model(monkeypatch, lambda _: suggestion)
    response = start_cleanup(client, url)
    row, = poll(client, response)['sheets']
    assert row['status'] == 'rename'
    assert [change['new_name'] for change in row['changes']] == names
    assert row['mapping']['blocks'][1] == previous['blocks'][1]
    diagnostic, = row['diagnostics']
    assert (diagnostic['block_index'], diagnostic['message']) == (1, unresolved['message'])
    assert row['changes'][0]['header_notes'] == [
        {'cell': 'A1', 'text': '钢联数据'}, {'cell': 'A4', 'text': '指标描述'},
        {'cell': 'B4', 'text': descriptions[0]},
    ]
    result = apply_cleanup(client, url, response.json()['id'], sheet.title)
    assert result['renamed_fields'] == 4
    catalog = read_series(client, result['dataset_id'])
    assert catalog[sheet.title, names[0]]['field_metadata']['header_notes'] == row['changes'][0]['header_notes']
    assert (sheet.title, '另一分区 / 辅助指标') in catalog

    # A response containing only unresolved reasons stays visible as a review item.
    message = 'B4-E4 建议作为表头说明格，请核对。'
    suggestion.clear()
    suggestion.update(block_updates=[], evidence=[], unresolved=[
        {'block_index': 0, 'field': 'header_note_cells', 'cells': ['B4', 'E4'], 'message': message},
    ])
    # The second block still has multiple header cells, so the sheet remains in scope.
    row, = poll(client, start_cleanup(client, url))['sheets']
    assert row['status'] == 'needs_review' and row['changes'] == []
    assert row['diagnostics'][0]['message'] == message


@pytest.mark.parametrize('unit,existing', [(None, None), ('百万包', None), ('百万包', '万吨')])
def test_cleanup_publishes_wind_codes_and_units(client, monkeypatch, unit, existing):
    book = Workbook()
    sheet = book.active
    sheet.title = 'USDA' if unit else '国内外棉花现货价格指数'
    name, code = ('期初库存', 'S0112531') if unit else ('国家棉花价格B指数:CNCotton B', 'S5329490')
    parts = [('note', 'EDBclose'), ('name', name), ('code', code)]
    if unit:
        parts = [('note', '数据来源：Wind（USDA）'), *parts, ('unit', unit), ('frequency', ' 周 ')]
    evidence = {}
    rules, headers = [], []
    for row, (role, value) in enumerate(parts, 1):
        sheet.append([None, value])
        rules.append({'row': row, 'role': role})
        headers.append(f'B{row}')
        evidence.setdefault(role, []).append(headers[-1])
    sheet.append([None, 10])
    sheet.append([None, 11])
    old = ' / '.join(text.strip() for _, text in parts)
    block = fixture_block(list(range(1, len(parts) + 1)), [
        {'column': 'B', 'metric': old, 'header_cells': headers, 'unit': existing}])
    url, _ = publish_fixture(client, book, [block])
    mock_header_model(monkeypatch, lambda _: suggest_roles(rules, evidence))
    response = start_cleanup(client, url)
    row, = poll(client, response)['sheets']
    change, = row['changes']
    assert row['status'] == 'rename'
    assert change['new_name'] == name
    assert change['source_codes'] == [{'cell': evidence['code'][0], 'text': code}]
    if existing:
        assert change['unit_conflict']
        diagnostic, = row['diagnostics']
        assert (diagnostic['column'], diagnostic['field']) == ('B', 'unit')
    else:
        assert not row['diagnostics']
    result = apply_cleanup(client, url, response.json()['id'], sheet.title)
    assert result['renamed_fields'] == 1
    field = read_series(client, result['dataset_id'])[sheet.title, name]
    metadata = field['field_metadata']
    assert metadata['source_codes'] == change['source_codes']
    assert 'EDBclose' in [item['text'] for item in metadata['header_notes']]
    assert field['unit'] == metadata['unit'] == (existing or unit)
    if unit and not existing:
        assert metadata['unit_source'] == 'source'
        assert metadata['unit_evidence'] == [{'sheet': sheet.title, 'cell': evidence['unit'][0], 'text': unit}]
    elif existing:
        assert metadata['unit_source'] == 'declared' and not metadata['unit_evidence']
    if unit:
        assert metadata['frequency'] == ' 周 '
        assert metadata['frequency_evidence'] == [{'sheet': sheet.title, 'cell': evidence['frequency'][0], 'text': ' 周 '}]


def test_cleanup_removes_merged_daily_title_and_respects_cell_override(client, monkeypatch):
    book = Workbook()
    sheet = book.active
    sheet.title = '日报'
    title = '棉花日报 2023-07-10'
    sheet.append(['日期', '价格', '产地'])
    sheet.append([None, title])
    sheet.merge_cells('B2:C2')
    sheet.append([None, 'S123', '按登记产地统计'])
    for day in (1, 2):
        sheet.append([datetime(2024, 1, day), 10, 11])
    block = fixture_block([1, 2, 3], [
        {'column': 'B', 'metric': f'价格 / {title} / S123', 'header_cells': ['B1', 'B2', 'B3']},
        {'column': 'C', 'metric': f'产地 / {title} / 按登记产地统计', 'header_cells': ['C1', 'B2', 'C3']},
    ], date_mode='date', date_column='A')
    url, _ = publish_fixture(client, book, [block])
    suggestion = suggest_roles([{'row': 1, 'role': 'name'}, {'row': 2, 'role': 'note'},
                                {'row': 3, 'role': 'code', 'overrides': {'C3': 'note'}}],
                               {'name': ['A1', 'B1', 'C1'], 'note': ['B2', 'C3'], 'code': ['B3']})
    mock_header_model(monkeypatch, lambda _: suggestion)
    row, = poll(client, start_cleanup(client, url))['sheets']
    assert row['status'] == 'rename'
    price, origin = row['changes']
    assert (price['new_name'], origin['new_name']) == ('价格', '产地')
    assert price['source_codes'] == [{'cell': 'B3', 'text': 'S123'}]
    assert not origin['source_codes']
    assert [item['text'] for item in price['header_notes']] == [title]
    assert [item['text'] for item in origin['header_notes']] == [title, '按登记产地统计']


def test_cleanup_preserves_undetermined_cells_and_applies_verified_rename(client, monkeypatch):
    book = Workbook()
    sheet = book.active
    sheet.append(['日期', '产量', '价格'])
    sheet.append([None, '分地区', '预留'])
    sheet.append([None, '计算口径', 'S123'])
    sheet.append([None, '已有来源说明'])
    sheet.append([None, '吨'])
    for day in (1, 2):
        sheet.append([datetime(2024, 1, day), 10, 11])
    block = fixture_block([1, 2, 3, 4, 5], [
        {'column': 'B', 'metric': '产量 / 分地区 / 计算口径', 'header_cells': ['B1', 'B2', 'B3', 'B5'],
         'header_note_cells': ['B4'], 'unit': '吨', 'unit_cells': ['B5']},
        {'column': 'C', 'metric': '价格', 'header_cells': ['C1'], 'source_code_cells': ['C3']},
    ], date_mode='date', date_column='A', header_note_cells=['B4'])
    url, _ = publish_fixture(client, book, [block])
    suggestion = suggest_roles([{'row': 1, 'role': 'name'}, {'row': 3, 'role': 'note'}],
                               {'name': ['A1', 'B1'], 'note': ['B3']})
    mock_header_model(monkeypatch, lambda _: suggestion)

    started = start_cleanup(client, url)
    row, = poll(client, started)['sheets']
    assert row['status'] == 'rename' and not row['diagnostics']
    change, = row['changes']
    assert (change['column'], change['new_name']) == ('B', '产量 / 分地区')
    updated, = row['mapping']['blocks']
    assert set(updated['header_note_cells']) == {'B3', 'B4'}
    amount, price = updated['columns']
    assert amount['header_cells'] == ['B1', 'B2', 'B5']
    assert amount['unit'] == '吨' and amount['unit_cells'] == ['B5']
    assert price['header_cells'] == ['C1'] and price['source_code_cells'] == ['C3']
    undetermined = [item for item in row['notices'] if item['code'] == 'header_role_undetermined']
    assert undetermined and all(item['message'] == '未判定，保持原样' for item in undetermined)
    assert {cell for item in undetermined for cell in item['cells']} == {'C1', 'B2', 'C2', 'C3', 'B4', 'B5'}
    result = apply_cleanup(client, url, started.json()['id'], sheet.title)
    assert result['renamed_fields'] == 1
    catalogue = read_series(client, result['dataset_id'])
    assert (sheet.title, '产量 / 分地区') in catalogue
    assert catalogue[sheet.title, '价格']['field_metadata']['source_codes'] == [{'cell': 'C3', 'text': 'S123'}]


def test_cleanup_keeps_original_name_when_a_column_loses_every_name_cell(client, monkeypatch):
    book = Workbook()
    sheet = book.active
    sheet.append(['日期', '原字段', '价格'])
    sheet.append([None, '说明甲', '说明乙'])
    for day in (1, 2):
        sheet.append([datetime(2024, 1, day), 10, 11])
    block = fixture_block([1, 2], [
        {'column': 'B', 'metric': '原字段 / 说明甲', 'header_cells': ['B1', 'B2']},
        {'column': 'C', 'metric': '价格 / 说明乙', 'header_cells': ['C1', 'C2']},
    ], date_mode='date', date_column='A')
    url, _ = publish_fixture(client, book, [block])
    suggestion = suggest_roles([{'row': 1, 'role': 'name', 'overrides': {'B1': 'note'}},
                                {'row': 2, 'role': 'note'}],
                               {'name': ['A1', 'C1'], 'note': ['B1', 'B2', 'C2']})
    mock_header_model(monkeypatch, lambda _: suggestion)

    started = start_cleanup(client, url)
    row, = poll(client, started)['sheets']
    assert row['status'] == 'rename' and not row['diagnostics']
    assert [(change['column'], change['new_name']) for change in row['changes']] == [('C', '价格')]
    assert row['mapping']['blocks'][0]['columns'][0]['metric'] == '原字段 / 说明甲'
    notice, = row['notices']
    assert (notice['code'], notice['column'], notice['message']) == (
        'header_name_unchanged', 'B', '该列没有名称表头格，保持原名。',
    )
    result = apply_cleanup(client, url, started.json()['id'], sheet.title)
    assert result['renamed_fields'] == 1
    assert (sheet.title, '原字段 / 说明甲') in read_series(client, result['dataset_id'])


@pytest.mark.parametrize('invalid', ['row', 'out_of_scope', 'blank', 'overlap', 'override_missing_evidence',
                                     'mismatched_role', 'period'])
def test_cleanup_rejects_invalid_role_evidence(client, monkeypatch, workbook_import, invalid):
    url, _ = workbook_import
    suggestion = suggest_notes()
    rules = suggestion['block_updates'][0]['header_roles']
    if invalid == 'row':
        rules[-1]['row'] = 4
    elif invalid == 'out_of_scope':
        suggestion['evidence'][1]['cells'].append('B4')
    elif invalid == 'blank':
        suggestion['evidence'][1]['cells'].append('C3')
    elif invalid == 'overlap':
        suggestion['evidence'][1]['cells'].append('B1')
    elif invalid == 'override_missing_evidence':
        rules[1]['overrides'] = {'B2': 'note'}
        suggestion['evidence'].pop()
    elif invalid == 'mismatched_role':
        rules[0]['role'] = 'note'
    elif invalid == 'period':
        rules[0]['role'] = 'period'
    mock_header_model(monkeypatch, lambda sheet: suggestion if sheet == '本期数据' else {'block_updates': []})
    previous = read_sheet(client, url, '本期数据')['published_mapping']
    row = next(row for row in poll(client, start_cleanup(client, url))['sheets'] if row['sheet_name'] == '本期数据')
    assert row['status'] == 'needs_review' and not row['changes']
    assert row['mapping'] == previous
    assert row['diagnostics'][0]['block_index'] == 0
