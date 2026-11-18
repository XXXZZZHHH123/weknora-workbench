"""Batch review jobs preserve source isolation and never publish unchecked AI output."""
from copy import deepcopy
from datetime import datetime
from io import BytesIO
import json
import time

from openpyxl import Workbook
import pytest

from structured_data import ai_review, review_worker
from structured_data.imports import ImportManager
from test_ai_review import mock_model
from test_review_api import FakeAuthClient, Reply, client, make_workbook, poll


def upload(client):
    return poll(client, client.post('/api/import', files={'file': ('review.xlsx', make_workbook())}))


def test_batch_job_imports_explicit_pending_sheet_and_returns_block_status(client):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    sheet = client.get(url + '/sheet', params={'name': 'parallel'}).json()
    result = poll(client, client.post(url + '/confirm-all', json={'mappings': [sheet['mapping']]}))
    assert 'parallel' in result['sheets_imported']
    assert not result['sheets_failed']
    assert result['dataset_id'] != uploaded['dataset_id']
    summary = client.get(url, params={'summary_only': True}).json()
    confirmed = next(row for row in summary['sheets'] if row['name'] == 'parallel')
    assert confirmed['import_state'] == 'imported'
    assert all(block['status'] == 'confirmed' for block in confirmed['review_state']['blocks'])


def test_batch_does_not_approve_unsubmitted_pending_sheet(client):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    result = poll(client, client.post(url + '/confirm-all', json={}))
    assert result['dataset_id'] == uploaded['dataset_id']
    assert 'parallel' in result['sheets_pending']
    assert client.get(url + '/sheet', params={'name': 'parallel'}).json()['status'] == 'needs_review'


def test_large_batch_request_rejected_before_creating_job_and_does_not_hold_lock(client):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    response = client.post(url + '/confirm-all', json={'mappings': [{'sheet': 'parallel', 'note': 'x' * (260 * 1024)}]})
    assert response.status_code == 413
    response = client.post(url + '/assist-confirm', json={'sheet': 'parallel', 'mapping': {'note': 'x' * (260 * 1024)}})
    assert response.status_code == 413
    assert poll(client, client.post(url + '/confirm-all', json={}))['dataset_id'] == uploaded['dataset_id']


def test_ai_assist_checks_model_mapping_then_automatically_imports(client, monkeypatch):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    mapping = client.get(url + '/sheet', params={'name': 'parallel'}).json()['mapping']
    seen = []

    def assist(manager, import_id, sheet, *, mapping=None, budget=None, cancelled=None,
               progress=None, review=None):
        budget.consume()
        seen.append((sheet, mapping))
        state = review(mapping)
        progress({'block_index': 0, 'total_blocks': 2, 'completed_blocks': 2, 'stage': '本地测试核验完成'})
        return {'mapping': mapping, 'review_state': state, 'model_requests': 1, 'notice': ''}

    monkeypatch.setattr('structured_data.ai_review.assist_mapping', assist)
    result = poll(client, client.post(url + '/assist-confirm', json={'sheet': 'parallel', 'mapping': mapping}))
    assert result['dataset_id'] != uploaded['dataset_id']
    assert result['imported'] is True and result['model_requests'] == 1
    assert seen == [('parallel', mapping)]
    assert client.get(url + '/sheet', params={'name': 'parallel'}).json()['status'] == 'confirmed'


def test_ai_unresolved_mapping_remains_pending_without_publishing(client, monkeypatch):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    mapping = client.get(url + '/sheet', params={'name': 'parallel'}).json()['mapping']
    pending = {'status': 'needs_review', 'blocks': [{'block_index': 0, 'status': 'needs_review',
        'diagnostics': [{'code': 'AI_UNRESOLVED', 'message': '来源未注明单位', 'field': 'unit'}]}], 'diagnostics': []}

    def assist(*args, **kwargs):
        return {'mapping': mapping, 'review_state': pending, 'model_requests': 1, 'notice': '需要人工补充'}

    monkeypatch.setattr('structured_data.ai_review.assist_mapping', assist)
    result = poll(client, client.post(url + '/assist-confirm', json={'sheet': 'parallel'}))
    assert result['imported'] is False and result['dataset_id'] == uploaded['dataset_id']
    assert result['review_state'] == pending and result['mapping'] == mapping
    assert client.get(url + '/sheet', params={'name': 'parallel'}).json()['status'] == 'needs_review'


@pytest.mark.parametrize('batch', [False, True], ids=['single', 'batch'])
@pytest.mark.parametrize('unsupported_header', [False, True], ids=['verified', 'borrowed-header'])
def test_wide_assist_job_checks_each_header_before_publishing(client, monkeypatch, batch, unsupported_header):
    width = 150
    note = '初步统计以公斤记录。修订前请先核对原表的统计覆盖范围以及填报规则。' * 4
    book = Workbook()
    existing = book.active
    existing.title = 'existing'
    existing.append(['日期', '价格'])
    existing.append([datetime(2024, 1, 1), 10])
    existing.append([datetime(2024, 2, 1), 11])
    wide = book.create_sheet('wide')
    wide.append([note])
    wide.append(['统计日期', *(f'产量 {column:03}' for column in range(2, width + 1))])
    wide.append([datetime(2024, 1, 1), *([10] * (width - 1))])
    wide.append([datetime(2024, 2, 1), *([11] * (width - 1))])
    raw = BytesIO()
    book.save(raw)
    uploaded = poll(client, client.post('/api/import', files={'file': ('wide.xlsx', raw.getvalue())}))
    url = '/api/imports/' + uploaded['import_id']
    sheet_url = url + '/sheet'
    sheet = client.get(sheet_url, params={'name': 'wide'}).json()
    assert sheet['status'] == 'needs_review' and not sheet['published_in_dataset']
    original_mapping = sheet['mapping']
    mapping = deepcopy(original_mapping)
    block, = mapping['blocks']
    for column in block['columns']:
        column['metric'] = f'{note} / {column["metric"]}'
        column['header_cells'].insert(0, 'A1')
    # Initially review tries to infer the declared unit from A1. The note is
    # outside B and does not constitute true unit evidence for that column.
    block['columns'][0]['unit'] = '公斤'
    last_column = block['columns'][-1]
    if unsupported_header:
        last_column['header_cells'] = ['A1', 'C2']
    assert len(json.dumps(mapping, ensure_ascii=False).encode('utf-8')) > ai_review.MAX_INPUT_BYTES
    preview = client.post(url + '/preview', json={'mapping': mapping})
    assert preview.status_code == 400 and '元数据来源未覆盖' in preview.json()['detail']

    suggestion = {
        'block_updates': [{'block_index': 0, 'header_note_cells': ['A1'],
                           'metric_header_rows': [] if batch else [2]}],
        'evidence': [{'block_index': 0, 'field': 'header_note_cells', 'cells': ['A1']}],
    }
    if not batch:
        suggestion['evidence'].append({'block_index': 0, 'field': 'metric', 'cells': ['B2']})
    calls = []
    monkeypatch.setattr(ai_review, 'read_model', lambda _: {
        'model': 'fixture', 'base_url': 'https://example.invalid/v1', 'api_key': 'fixture-only',
    })
    mock_model(monkeypatch, [suggestion], calls)
    if batch:
        result = poll(client, client.post(url + '/assist-confirm-all', json={
            'acknowledged_risk': True, 'mappings': [mapping], 'save_template': False,
            'total_max_requests': 1, 'per_sheet_max_requests': 1,
        }), timeout=30)
        detail = next(row for row in result['details'] if row['sheet'] == 'wide')
        assert result['sheets_existing'] == ['existing'] and not result['sheets_failed']
        assert result['sheets_pending'] == (['wide'] if unsupported_header else [])
        assert result['sheets_imported'] == ([] if unsupported_header else ['wide'])
        assert detail['requests_used'] == result['requests_used'] == 1
    else:
        result = poll(client, client.post(url + '/assist-confirm', json={
            'sheet': 'wide', 'mapping': mapping, 'save_template': False, 'max_requests': 1,
        }), timeout=30)
        detail = result
        assert result['imported'] is not unsupported_header
        assert result['model_requests'] == 1
    body, = calls
    payload = json.loads(body['messages'][1]['content'])
    assert payload['mode'] == detail['scope'] == 'header_only'
    assert 'source_cells' not in payload

    saved = client.get(sheet_url, params={'name': 'wide'}).json()
    if unsupported_header:
        assert result['dataset_id'] == uploaded['dataset_id']
        assert saved['status'] == 'needs_review' and not saved['published_in_dataset']
        assert saved['mapping'] == original_mapping
        if not batch:
            assert result['mapping'] == mapping
        state = detail['review_state']
        assert state['status'] == 'needs_review'
        assert any(item.get('field') == 'header_cells' and item.get('column') == last_column['column']
                   and 'C2' in item.get('cells', []) for item in state['diagnostics'])
    else:
        assert result['dataset_id'] != uploaded['dataset_id']
        assert saved['status'] == 'confirmed' and saved['published_in_dataset']
        published_block, = saved['published_mapping']['blocks']
        assert published_block['header_note_cells'] == ['A1']
        assert [column['metric'] for column in published_block['columns']] == [
            f'产量 {column:03}' for column in range(2, width + 1)]
        assert detail['renamed_fields'] == width - 1
        assert all(word in detail['header_notice'] for word in (
            '仅表头建议', str(width - 1), '字段 ID', '指标对应确认'))


def test_worker_review_reports_each_block_and_does_not_write(tmp_path):
    root = tmp_path / 'data'
    root.mkdir()
    source = root / 'review.xlsx'
    source.write_bytes(make_workbook())
    manager = ImportManager(root)
    uploaded = manager.ingest(source)
    mapping = deepcopy(manager.sheet(uploaded['import_id'], 'parallel')['mapping'])
    mapping['blocks'][1]['columns'][0]['metric'] = ''
    before = manager.path(uploaded['import_id']).joinpath('review.json').read_bytes()
    state = review_worker.run_review_task(root, 'review', import_id=uploaded['import_id'], mapping=mapping)
    assert [block['status'] for block in state['blocks']] == ['confirmed', 'needs_review']
    assert state['blocks'][1]['diagnostics'][0]['field'] == 'metric'
    assert manager.path(uploaded['import_id']).joinpath('review.json').read_bytes() == before


def test_ai_job_can_be_cancelled_before_import_and_cannot_be_cancelled_by_others(client, monkeypatch):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    import threading
    entered, release = threading.Event(), threading.Event()

    def assist(*args, **kwargs):
        entered.set()
        release.wait(4)
        return {'mapping': {}, 'review_state': {'status': 'needs_review'}, 'model_requests': 0, 'notice': ''}

    monkeypatch.setattr('structured_data.ai_review.assist_mapping', assist)
    original_get = FakeAuthClient.get

    async def scoped_get(self, url, headers):
        if headers.get('Authorization') == 'Bearer other-user':
            return Reply({'data': {'user': {'id': 'other-user', 'tenant_id': 10000},
                'memberships': [{'tenant_id': 10000, 'role': 'owner'}]}})
        if headers.get('Authorization') == 'Bearer other-space':
            return Reply({'data': {'user': {'id': 'test-user', 'tenant_id': 10001},
                'memberships': [{'tenant_id': 10001, 'role': 'owner'}]}})
        return await original_get(self, url, headers)

    monkeypatch.setattr(FakeAuthClient, 'get', scoped_get)
    try:
        response = client.post(url + '/assist-confirm', json={'sheet': 'parallel'})
        assert response.status_code == 200, response.text
        assert entered.wait(2)
        target = '/api/jobs/' + response.json()['id'] + '/cancel'
        assert client.post(target, headers={'Authorization': 'Bearer other-user'}).status_code == 404
        assert client.post(target, headers={'Authorization': 'Bearer other-space'}).status_code == 404
        cancel = client.post(target)
        assert cancel.status_code == 200, cancel.text
    finally:
        release.set()
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        job = client.get('/api/jobs/' + response.json()['id']).json()
        if job['status'] != 'processing':
            break
        time.sleep(.01)
    assert job['status'] == 'cancelled'
    assert client.get(url).json()['dataset_id'] == uploaded['dataset_id']


def test_ai_cancellation_can_arrive_before_create_response_without_starting_work(client, monkeypatch):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    identifier = 'e' * 32

    def forbidden(*args, **kwargs):
        raise AssertionError('An early-cancelled task must never call AI assistance')

    monkeypatch.setattr('structured_data.ai_review.assist_mapping', forbidden)
    cancelled = client.post('/api/jobs/' + identifier + '/cancel')
    assert cancelled.status_code == 200 and cancelled.json()['cancel_requested'] is True
    assert client.get('/api/jobs/' + identifier).status_code == 404
    created = client.post(url + '/assist-confirm', json={'sheet': 'parallel', 'request_id': identifier})
    assert created.status_code == 200 and created.json() == {'id': identifier, 'status': 'cancelled'}
    assert client.get('/api/jobs/' + identifier).json()['status'] == 'cancelled'
    assert client.post('/api/jobs/' + identifier + '/cancel').status_code == 200
    assert client.get(url).json()['dataset_id'] == uploaded['dataset_id']
    # The cancelled create must not hold the import lock.
    assert poll(client, client.post(url + '/confirm-all', json={}))['dataset_id'] == uploaded['dataset_id']


def test_early_ai_cancel_is_scoped_to_user_and_ids_are_not_reusable(client, monkeypatch):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    identifier = 'f' * 32
    original_get = FakeAuthClient.get

    async def other_user(self, url, headers):
        if headers.get('Authorization') == 'Bearer other-user':
            return Reply({'data': {'user': {'id': 'other-user', 'tenant_id': 10000},
                'memberships': [{'tenant_id': 10000, 'role': 'owner'}]}})
        return await original_get(self, url, headers)

    monkeypatch.setattr(FakeAuthClient, 'get', other_user)
    seen = []

    def assist(*args, **kwargs):
        seen.append('called')
        return {'mapping': {}, 'review_state': {'status': 'needs_review'}, 'model_requests': 0}

    monkeypatch.setattr('structured_data.ai_review.assist_mapping', assist)
    assert client.post('/api/jobs/' + identifier + '/cancel',
                       headers={'Authorization': 'Bearer other-user'}).status_code == 200
    result = poll(client, client.post(url + '/assist-confirm',
                  json={'sheet': 'parallel', 'request_id': identifier}))
    assert result['imported'] is False and seen == ['called']
    duplicate = client.post(url + '/assist-confirm', json={'sheet': 'parallel', 'request_id': identifier})
    assert duplicate.status_code == 409
    assert client.post('/api/jobs/' + identifier + '/cancel',
                       headers={'Authorization': 'Bearer other-user'}).status_code == 404


def test_pending_ai_cancel_intents_are_bounded_and_reject_invalid_ids(client):
    for number in range(32):
        response = client.post('/api/jobs/' + format(number, '032x') + '/cancel')
        assert response.status_code == 200
    assert client.post('/api/jobs/' + 'a' * 32 + '/cancel').status_code == 429
    assert client.post('/api/jobs/' + '0' * 32 + '/cancel').status_code == 200
    assert client.post('/api/jobs/not-a-valid-request-id/cancel').status_code == 404


def test_ai_cannot_publish_after_edit_permission_revoked(client, monkeypatch):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    mapping = client.get(url + '/sheet', params={'name': 'parallel'}).json()['mapping']
    original_get = FakeAuthClient.get

    async def readonly_get(self, url, headers):
        return Reply({'data': {'user': {'id': 'test-user', 'tenant_id': 10000},
            'memberships': [{'tenant_id': 10000, 'role': 'viewer'}]}})

    def assist(*args, **kwargs):
        state = kwargs['review'](mapping)
        monkeypatch.setattr(FakeAuthClient, 'get', readonly_get)
        return {'mapping': mapping, 'review_state': state, 'model_requests': 0}

    monkeypatch.setattr('structured_data.ai_review.assist_mapping', assist)
    response = client.post(url + '/assist-confirm', json={'sheet': 'parallel'})
    assert response.status_code == 200
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        job = client.get('/api/jobs/' + response.json()['id']).json()
        if job['status'] != 'processing':
            break
        time.sleep(.01)
    assert job['status'] == 'failed' and '权限' in job['error']
    assert client.get(url).json()['dataset_id'] == uploaded['dataset_id']
    monkeypatch.setattr(FakeAuthClient, 'get', original_get)


def test_ai_claiming_confirmation_cannot_bypass_real_worker_validation(client, monkeypatch):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    mapping = client.get(url + '/sheet', params={'name': 'parallel'}).json()['mapping']
    mapping['blocks'][1]['columns'][0]['metric'] = ''

    def assist(*args, **kwargs):
        return {'mapping': mapping, 'review_state': {'status': 'confirmed'}, 'model_requests': 0}

    monkeypatch.setattr('structured_data.ai_review.assist_mapping', assist)
    response = client.post(url + '/assist-confirm', json={'sheet': 'parallel'})
    assert response.status_code == 200
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        job = client.get('/api/jobs/' + response.json()['id']).json()
        if job['status'] != 'processing':
            break
        time.sleep(.01)
    assert job['status'] == 'failed'
    assert [b['status'] for b in job['review_state']['blocks']] == ['confirmed', 'needs_review']
    assert job['review_target']['field'] == 'metric'
    assert client.get(url).json()['dataset_id'] == uploaded['dataset_id']
