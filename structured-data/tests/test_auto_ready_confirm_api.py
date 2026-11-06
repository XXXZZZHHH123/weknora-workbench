"""Strict auto-ready selection reuses real single-sheet validation and ACL guards."""
from io import BytesIO
from pathlib import Path
import threading

from openpyxl import load_workbook
import pytest

from structured_data import server
from structured_data.imports import ImportManager
from test_review_api import FakeAuthClient, Reply, client, make_workbook, poll


def upload(client, **kwargs):
    book = load_workbook(BytesIO(make_workbook()))
    for name in ('second', 'third'):
        book.copy_worksheet(book['simple']).title = name
    content = BytesIO()
    book.save(content)
    book.close()
    return poll(client, client.post('/api/import', files={'file': ('synthetic.xlsx', content.getvalue())}, **kwargs))


def review_manager(tmp_path):
    return ImportManager(tmp_path / 'data')


def test_completed_import_allows_immediate_auto_ready_confirmation(client, monkeypatch):
    entered, release = threading.Event(), threading.Event()
    original = Path.unlink

    def delayed_cleanup(path, *args, **kwargs):
        if path.name == 'completion-race.xlsx' and path.parent.parent.name == 'uploads':
            entered.set()
            assert release.wait(5), 'test did not release staging cleanup'
        return original(path, *args, **kwargs)

    monkeypatch.setattr(Path, 'unlink', delayed_cleanup)
    response = client.post('/api/import', files={'file': ('completion-race.xlsx', make_workbook())})
    assert response.status_code == 200
    try:
        assert entered.wait(3)
        # The import still owns the confirmation lock while cleanup is blocked.
        assert client.get('/api/jobs/' + response.json()['id']).json()['status'] == 'processing'
    finally:
        release.set()
    uploaded = poll(client, response)
    url = '/api/imports/' + uploaded['import_id'] + '/confirm-auto-ready'
    assert poll(client, client.post(url, json={'sheets': ['simple']}))['sheets_imported'] == ['simple']


def test_auto_ready_batch_includes_already_imported_auto_ready_and_excludes_other_states(client, tmp_path):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    manager = review_manager(tmp_path)
    doc = manager.get(uploaded['import_id'])
    for sheet in doc['sheets']:
        if sheet['name'] == 'second':
            sheet['status'] = 'template_ready'
        elif sheet['name'] == 'third':
            sheet['status'] = 'confirmed'
    manager._save(doc)
    before = client.get(url).json()
    assert before['sheets'][0]['status'] == 'auto_ready' and before['sheets'][0]['import_state'] == 'imported'
    names = [sheet['name'] for sheet in before['sheets']]
    result = poll(client, client.post(url + '/confirm-auto-ready', json={'sheets': names}))
    assert result['sheets_imported'] == ['simple']
    assert {item['sheet'] for item in result['sheets_failed']} == set(names) - {'simple'}
    after = client.get(url).json()
    assert {sheet['name']: sheet['status'] for sheet in after['sheets']} == {
        'simple': 'confirmed', 'parallel': 'needs_review', 'empty': 'empty',
        'second': 'template_ready', 'third': 'confirmed'}
    assert next(sheet for sheet in before['sheets'] if sheet['name'] == 'parallel') == (
        next(sheet for sheet in after['sheets'] if sheet['name'] == 'parallel'))
    # An explicit subset does not silently add any other auto-ready sheet.
    assert client.get(url + '/sheet', params={'name': 'simple'}).json()['published_in_dataset'] is True


def test_auto_ready_batch_confirms_only_previewed_names(client):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    summary = client.get(url, params={'summary_only': True}).json()
    assert next(sheet for sheet in summary['sheets'] if sheet['name'] == 'simple')['field_count'] == 1
    result = poll(client, client.post(url + '/confirm-auto-ready', json={'sheets': ['simple']}))
    assert result['sheets_imported'] == ['simple']
    assert client.get(url + '/sheet', params={'name': 'second'}).json()['status'] == 'auto_ready'


def test_auto_ready_batch_isolates_real_validation_failure_and_keeps_both_successes(client, tmp_path):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    manager = review_manager(tmp_path)
    doc = manager.get(uploaded['import_id'])
    failed = next(sheet for sheet in doc['sheets'] if sheet['name'] == 'second')
    failed['mapping']['blocks'][0]['columns'][0]['metric'] = ''
    manager._save(doc)
    result = poll(client, client.post(url + '/confirm-auto-ready',
        json={'sheets': ['simple', 'second', 'third']}))
    assert result['sheets_imported'] == ['simple', 'third']
    assert [item['sheet'] for item in result['sheets_failed']] == ['second']
    failure = result['sheets_failed'][0]
    assert '指标名称' in failure['message'] and failure['review_target']['field'] == 'metric'
    assert failure['review_state']['status'] == 'needs_review'
    sheets = {sheet['name']: sheet for sheet in client.get(url).json()['sheets']}
    assert sheets['simple']['status'] == sheets['third']['status'] == 'confirmed'
    assert sheets['second']['status'] == 'auto_ready'
    assert sheets['second']['published_in_dataset'] is True  # prior successful snapshot survives
    assert sheets['parallel']['status'] == 'needs_review'


def test_auto_ready_batch_isolates_worker_error_and_unknown_sheet(client, monkeypatch):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    original = server.run_review_task

    def failing(directory, operation, **kwargs):
        if operation == 'confirm' and kwargs['mapping']['sheet'] == 'second':
            raise RuntimeError('synthetic worker failure')
        return original(directory, operation, **kwargs)
    monkeypatch.setattr(server, 'run_review_task', failing)
    result = poll(client, client.post(url + '/confirm-auto-ready',
        json={'sheets': ['missing', 'simple', 'second', 'third']}))
    assert result['sheets_imported'] == ['simple', 'third']
    assert {item['sheet'] for item in result['sheets_failed']} == {'missing', 'second'}
    assert any(item['message'] == 'synthetic worker failure' for item in result['sheets_failed'])


@pytest.mark.parametrize('body', [{}, {'sheets': []}, {'sheets': ['simple'], 'mappings': []},
    {'sheets': ['simple'], 'save_template': 'yes'}, {'sheets': [None]}, {'sheets': ['x' * 255]},
    {'sheets': ['simple'] * 257}])
def test_auto_ready_batch_rejects_invalid_requests(client, body):
    assert client.post('/api/imports/' + 'a' * 24 + '/confirm-auto-ready', json=body).status_code == 422


def test_auto_ready_batch_rejects_duplicate_or_oversized_request_without_holding_lock(client):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id'] + '/confirm-auto-ready'
    assert client.post(url, json={'sheets': ['simple', 'simple']}).status_code == 400
    assert client.post(url, json={'sheets': ['simple'], 'note': 'x' * (260 * 1024)}).status_code == 413
    assert poll(client, client.post(url, json={'sheets': ['simple']}))['sheets_imported'] == ['simple']


def test_auto_ready_batch_requires_login_write_permission_and_same_origin(client, monkeypatch):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    before = client.get(url).json()
    original = FakeAuthClient.get

    async def readonly(self, url, headers):
        reply = await original(self, url, headers)
        if headers.get('Authorization') == 'Bearer readonly':
            reply.value['data']['memberships'][0]['role'] = 'viewer'
        return reply
    monkeypatch.setattr(FakeAuthClient, 'get', readonly)
    assert client.post(url + '/confirm-auto-ready', json={'sheets': ['simple']},
        headers={'Authorization': 'Bearer readonly'}).status_code == 403
    assert client.post(url + '/confirm-auto-ready', json={'sheets': ['simple']},
        headers={'Origin': 'http://another-site'}).status_code == 403
    client.cookies.clear()
    assert client.post(url + '/confirm-auto-ready', json={'sheets': ['simple']}).status_code == 401
    assert client.get(url, headers={'Authorization': 'Bearer test-token'}).json() == before


def test_auto_ready_batch_enforces_knowledge_base_acl_and_isolation(client, tmp_path, monkeypatch):
    original = FakeAuthClient.get
    permissions = {'library-a': 'admin', 'library-b': 'admin'}

    async def knowledge(self, url, headers):
        if '/api/v1/knowledge-bases/' in url:
            kb = url.rsplit('/', 1)[-1]
            return Reply({'success': True, 'data': {'id': kb, 'tenant_id': 10000, 'my_permission': permissions[kb]}})
        return await original(self, url, headers)
    monkeypatch.setattr(FakeAuthClient, 'get', knowledge)
    scope = {'X-Knowledge-Base-ID': 'library-a'}
    source = tmp_path / 'synthetic.xlsx'
    source.write_bytes(make_workbook())
    uploaded = ImportManager(tmp_path / 'data' / 'knowledge-bases' / 'library-a').ingest(source)
    url = '/api/imports/' + uploaded['import_id']
    permissions['library-a'] = 'viewer'
    assert client.post(url + '/confirm-auto-ready', json={'sheets': ['simple']}, headers=scope).status_code == 403
    permissions['library-a'] = 'admin'
    assert client.post(url + '/confirm-auto-ready', json={'sheets': ['simple']},
        headers={'X-Knowledge-Base-ID': 'library-b'}).status_code == 400
    assert client.get(url + '/sheet', params={'name': 'simple'}, headers=scope).json()['status'] == 'auto_ready'
    assert poll_scoped(client, client.post(url + '/confirm-auto-ready', json={'sheets': ['simple']}, headers=scope), scope)['sheets_imported'] == ['simple']


def poll_scoped(client, response, headers):
    original_headers = dict(client.headers)
    client.headers.update(headers)
    try:
        return poll(client, response)
    finally:
        client.headers.clear()
        client.headers.update(original_headers)


@pytest.mark.parametrize('revoke_after_first', [False, True])
def test_auto_ready_batch_rechecks_permissions_at_publication_and_retains_partial_success(client, monkeypatch, revoke_after_first):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    original_auth, original_worker = FakeAuthClient.get, server.run_review_task
    revoked = False

    async def auth(self, url, headers):
        result = await original_auth(self, url, headers)
        if revoked:
            result.value['data']['memberships'][0]['role'] = 'viewer'
        return result

    def worker(directory, operation, **kwargs):
        nonlocal revoked
        if operation != 'confirm':
            return original_worker(directory, operation, **kwargs)
        if not revoke_after_first:
            revoked = True
        result = original_worker(directory, operation, **kwargs)
        revoked = True
        return result
    monkeypatch.setattr(FakeAuthClient, 'get', auth)
    monkeypatch.setattr(server, 'run_review_task', worker)
    result = poll(client, client.post(url + '/confirm-auto-ready', json={'sheets': ['simple', 'second', 'third']}))
    assert result['sheets_imported'] == (['simple'] if revoke_after_first else [])
    assert {item['sheet'] for item in result['sheets_failed']} == ({'second', 'third'} if revoke_after_first else {'simple', 'second', 'third'})
    assert all('权限' in item['message'] for item in result['sheets_failed'])
    assert client.get(url + '/sheet', params={'name': 'simple'}).json()['status'] == ('confirmed' if revoke_after_first else 'auto_ready')
    if not revoke_after_first:
        assert client.get(url).json()['dataset_id'] == uploaded['dataset_id']


def test_auto_ready_batch_rechecks_raw_status_at_publication(client, tmp_path, monkeypatch):
    uploaded = upload(client)
    url = '/api/imports/' + uploaded['import_id']
    original = server.run_review_task

    def worker(directory, operation, **kwargs):
        manager = review_manager(tmp_path)
        doc = manager.get(uploaded['import_id'])
        next(sheet for sheet in doc['sheets'] if sheet['name'] == 'simple')['status'] = 'needs_review'
        manager._save(doc)
        return original(directory, operation, **kwargs)
    monkeypatch.setattr(server, 'run_review_task', worker)
    result = poll(client, client.post(url + '/confirm-auto-ready', json={'sheets': ['simple']}))
    assert result['sheets_imported'] == []
    assert '状态已变化' in result['sheets_failed'][0]['message']
    assert client.get(url).json()['dataset_id'] == uploaded['dataset_id']
    assert client.get(url + '/sheet', params={'name': 'simple'}).json()['status'] == 'needs_review'
