"""Review header notes on published workbooks without calling a real model."""
from copy import deepcopy
from datetime import datetime
from io import BytesIO
import json
from threading import Event
from time import monotonic, sleep

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


def suggest_notes(*, name_rows=(1,)):
    evidence = [{'block_index': 0, 'field': 'header_note_cells', 'cells': ['B3']}]
    if name_rows:
        evidence.append({'block_index': 0, 'field': 'metric', 'cells': ['B1']})
    return {'block_updates': [{'block_index': 0, 'header_note_cells': ['B3'],
                              'metric_header_rows': list(name_rows)}], 'evidence': evidence}


def mock_header_model(monkeypatch, propose):
    calls = []

    def complete(config, messages, **kwargs):
        payload = json.loads(messages[1]['content'])
        calls.append(payload)
        assert payload['mode'] == 'header_only'
        assert 'source_cells' not in payload
        suggestion = propose(payload['sheet'])
        return {'choices': [{'finish_reason': 'stop', 'message': {'content': json.dumps(suggestion)}}]}

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
    assert (change['old_name'], change['new_name'], change['header_notes']) == (OLD_METRIC, '产量', [NOTE])
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
        return suggest_notes(name_rows=())

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
