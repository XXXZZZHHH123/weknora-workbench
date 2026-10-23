"""Real HTTP/storage isolation with local auth fixtures and no model calls."""
import base64
import hashlib
import hmac
import json
import time

import httpx
import pytest
from fastapi.testclient import TestClient

from structured_data.server import create_app
from structured_data.workspaces import Identity, WorkspaceRegistry, current_identity
from structured_data.model_settings import read_model, save_model
from test_review_api import make_workbook


@pytest.fixture
def scoped(tmp_path, monkeypatch):
    grants = {'kb-a': 10000, 'kb-b': 10000, 'kb-other': 10001}
    requests = []
    originals = {}

    class Auth:
        def __init__(self, **kwargs): pass
        async def __aenter__(self): return self
        async def __aexit__(self, *args): pass
        async def get(self, url, headers):
            requests.append((url, dict(headers)))
            tenant = int(headers.get('X-Tenant-ID', '10000'))
            role = headers['Authorization'].removeprefix('Bearer ')
            if url.endswith('/auth/me'):
                return httpx.Response(200, json={'data': {'user': {'id': 'test-user', 'tenant_id': tenant},
                    'memberships': [{'tenant_id': tenant, 'role': role}]}})
            if '/api/v1/knowledge/' in url:
                identifier = url.rsplit('/', 1)[-1]
                return httpx.Response(200 if identifier in originals else 404,
                                     json={'success': True, 'data': originals.get(identifier)})
            kb_id = url.rsplit('/', 1)[-1]
            if kb_id not in grants:
                return httpx.Response(404, json={})
            if kb_id == 'kb-other' and tenant != 10001 and role != 'service-reader':
                return httpx.Response(403, json={})
            if isinstance(grants[kb_id], dict):
                data = grants[kb_id]
            else:
                data = {'id': kb_id, 'tenant_id': grants[kb_id], 'my_permission': 'editor'}
                if grants[kb_id] != tenant: data['my_permission'] = 'viewer'
            return httpx.Response(200, json={'success': True, 'data': data}, request=httpx.Request('GET', url))

        async def post(self, url, headers, files, data):
            requests.append((url, dict(headers)))
            kb_id = url.removesuffix('/knowledge/file').rsplit('/', 1)[-1]
            assert kb_id in grants
            assert data == {'process_config': '{"source_only":true}'}
            name, stream, _ = files['file']
            contents = stream.read()
            digest = hashlib.md5(contents).hexdigest()
            identifier = 'source-' + kb_id + '-' + digest[:12]
            owner = grants[kb_id]['tenant_id'] if isinstance(grants[kb_id], dict) else grants[kb_id]
            document = {'id': identifier, 'tenant_id': owner, 'knowledge_base_id': kb_id,
                        'type': 'file', 'file_type': 'xlsx', 'file_name': name, 'file_size': len(contents),
                        'file_hash': digest, 'parse_status': 'stored'}
            originals[identifier] = document
            return httpx.Response(200, json={'success': True, 'data': document})

    monkeypatch.setattr(httpx, 'AsyncClient', Auth)
    monkeypatch.setenv('STRUCTURED_TENANT_ID', '10000')
    monkeypatch.setenv('STRUCTURED_MODEL_CONFIG', str(tmp_path / 'absent-model.json'))
    key = b'isolated-knowledge-base-test-service-key-1234567890'
    key_file = tmp_path / 'signed-key'
    key_file.write_bytes(key)
    monkeypatch.setenv('STRUCTURED_SERVICE_TOKEN_FILE', str(key_file))
    app = create_app(tmp_path / 'data')
    with TestClient(app) as client:
        yield client, app, grants, requests, key


def headers(kb='', tenant=10000, role='owner'):
    result = {'Authorization': 'Bearer ' + role, 'X-Tenant-ID': str(tenant), 'Origin': 'http://testserver'}
    if kb: result['X-Knowledge-Base-ID'] = kb
    return result


def upload(client, kb='', tenant=10000, role='owner'):
    auth = headers(kb, tenant, role)
    response = client.post('/api/import', headers=auth, files={'file': ('isolated.xlsx', make_workbook())})
    assert response.status_code == 200, response.text
    job_id = response.json()['id']
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        job = client.get('/api/jobs/' + job_id, headers=auth).json()
        if job['status'] != 'processing':
            assert job['status'] == 'completed', job
            return job_id, job['result']
        time.sleep(.01)
    raise AssertionError('local import worker did not finish')


def sign(key, body, kb='', owner=10000):
    data = {'user_id': 'tool-user', 'email': '', 'display_name': '', 'tenant_id': 10000, 'role': 'viewer'}
    if kb: data.update(knowledge_base_id=kb, knowledge_base_tenant_id=owner,
                       authorization_sha256=hashlib.sha256(b'Bearer service-reader').hexdigest())
    encoded = base64.urlsafe_b64encode(json.dumps(data, separators=(',', ':')).encode()).decode().rstrip('=')
    raw = json.dumps(body, separators=(',', ':')).encode()
    stamp = str(int(time.time()))
    signature = hmac.new(key, '\n'.join((stamp, 'POST', '/api/tools/read', encoded,
        hashlib.sha256(raw).hexdigest())).encode(), hashlib.sha256).hexdigest()
    return raw, {'Content-Type': 'application/json', 'Authorization': 'Bearer service-reader', 'X-Research-Identity': encoded,
        'X-Research-Timestamp': stamp, 'X-Research-Signature': signature}


def test_registry_keeps_legacy_and_each_real_kb_separate(tmp_path):
    registry = WorkspaceRegistry(tmp_path, 10000)
    assert registry.directory(10000) == tmp_path
    assert registry.directory(10001) == tmp_path / 'workspaces' / '10001'
    marker = current_identity.set(Identity('u', '', '', 10000, 'owner', 'kb-a', 10000, True))
    try:
        assert registry.directory() == tmp_path / 'knowledge-bases' / 'kb-a'
        own = registry.objects()['import_lock']
        other = current_identity.set(Identity('u', '', '', 10000, 'owner', 'kb-b', 10000, True))
        try: assert registry.objects()['import_lock'] is not own
        finally: current_identity.reset(other)
    finally: current_identity.reset(marker)
    invalid = current_identity.set(Identity('u', '', '', 10000, 'owner', '../escape', 10000, True))
    try:
        with pytest.raises(ValueError): registry.directory()
    finally: current_identity.reset(invalid)


def test_kb_http_reads_originals_imports_queries_and_jobs_cannot_cross_scope(scoped):
    client, app, _, requests, _ = scoped
    job_id, result = upload(client, 'kb-a')
    dataset, import_id = result['dataset_id'], result['import_id']
    a, b = headers('kb-a'), headers('kb-b')
    assert client.get('/api/datasets', headers=a).json()['datasets'][0]['dataset_id'] == dataset
    assert client.get('/api/datasets', headers=b).json()['datasets'] == []
    assert client.get('/api/imports', headers=b).json()['imports'] == []
    series = client.get(f'/api/datasets/{dataset}/series', headers=a).json()['series'][0]['series_id']
    for path in (f'/api/datasets/{dataset}/report', f'/api/datasets/{dataset}/source',
                 f'/api/datasets/{dataset}/series', f'/api/imports/{import_id}',
                 f'/api/imports/{import_id}/sheet?name=simple',
                 f'/api/imports/{import_id}/sheet-view?name=simple',
                 f'/api/query?dataset_id={dataset}&series_id={series}'):
        assert client.get(path, headers=a).status_code == 200, path
        assert client.get(path, headers=b).status_code in {400, 404}, path
    assert client.get('/api/jobs/' + job_id, headers=b).status_code == 404
    assert client.get('/api/jobs/' + job_id, headers=headers()).status_code == 404
    assert client.get('/api/datasets', headers=headers()).json()['datasets'] == []
    assert any(url.endswith('/knowledge-bases/kb-a') for url, _ in requests)
    assert (app.state.workspaces.directory(10000) / 'knowledge-bases' / 'kb-a').is_dir()


def test_legacy_import_is_not_implicitly_bound_or_migrated(scoped):
    client, app, _, _, _ = scoped
    _, result = upload(client)
    legacy = app.state.workspaces.directory(10000) / 'datasets' / result['dataset_id']
    assert legacy.is_dir()
    assert client.get('/api/datasets', headers=headers('kb-a')).json()['datasets'] == []
    assert legacy.is_dir()
    assert client.get('/api/datasets', headers=headers()).json()['datasets'][0]['dataset_id'] == result['dataset_id']


def test_scope_is_verified_on_every_request_and_shared_readers_cannot_write(scoped):
    client, _, grants, _, _ = scoped
    job_id, result = upload(client, 'kb-a')
    shared = headers('kb-a', 10001)
    assert client.get('/api/datasets', headers=shared).json()['datasets'][0]['dataset_id'] == result['dataset_id']
    assert client.post('/api/import', headers=shared, files={'file': ('x.xlsx', b'no')}).status_code == 403
    assert client.post('/api/import', headers=headers('kb-a', role='viewer'), files={'file': ('x.xlsx', b'no')}).status_code == 403
    assert client.get('/api/jobs/' + job_id, headers=shared).status_code == 404
    assert client.get('/api/datasets', headers=headers('kb-other')).status_code == 403
    assert client.get('/api/datasets', headers=headers('../escape')).status_code == 400
    del grants['kb-a']
    assert client.get('/api/datasets', headers=headers('kb-a')).status_code == 403


def test_native_kb_write_uses_resource_grant_instead_of_creator_or_space_administrator(scoped):
    client, _, grants, _, _ = scoped
    grants['kb-a'] = {'id': 'kb-a', 'tenant_id': 10000, 'creator_id': 'other-user', 'my_permission': 'viewer'}
    contributor = headers('kb-a', role='contributor')
    assert client.get('/api/datasets', headers=contributor).status_code == 200
    assert client.post('/api/import', headers=contributor,
        files={'file': ('isolated.xlsx', make_workbook())}).status_code == 403
    assert client.post('/api/model-settings', headers=contributor, json={}).status_code == 403
    assert client.post('/api/import', headers=headers('kb-a', role='admin'),
        files={'file': ('isolated.xlsx', make_workbook())}).status_code == 403
    grants['kb-a']['my_permission'] = 'editor'
    upload(client, 'kb-a', role='contributor')
    grants['kb-a'].pop('creator_id')
    grants['kb-a']['my_permission'] = 'viewer'
    assert client.post('/api/import', headers=contributor,
        files={'file': ('isolated.xlsx', make_workbook())}).status_code == 403


def test_signed_reads_bind_real_kb_and_unsigned_headers_cannot_change_it(scoped):
    client, _, _, _, key = scoped
    _, result = upload(client, 'kb-a')
    body = {'operation': 'list_datasets', 'arguments': {}}
    raw, auth = sign(key, body, 'kb-a')
    response = client.post('/api/tools/read', content=raw, headers=auth)
    assert response.status_code == 200, response.text
    assert response.json()['datasets'][0]['dataset_id'] == result['dataset_id']
    assert client.post('/api/tools/read', content=raw, headers=auth | {'X-Knowledge-Base-ID': 'kb-b'}).status_code == 401
    for kb in ('kb-b', ''):
        raw, auth = sign(key, body, kb)
        assert client.post('/api/tools/read', content=raw, headers=auth).json()['datasets'] == []
    raw, auth = sign(key, body, '../escape')
    assert client.post('/api/tools/read', content=raw, headers=auth).status_code == 401


def test_shared_signed_reads_use_the_verified_owner_storage(scoped):
    client, _, _, _, key = scoped
    _, result = upload(client, 'kb-other', tenant=10001)
    body = {'operation': 'list_datasets', 'arguments': {}}
    raw, auth = sign(key, body, 'kb-other', owner=10001)
    response = client.post('/api/tools/read', content=raw, headers=auth)
    assert response.status_code == 200, response.text
    assert response.json()['datasets'][0]['dataset_id'] == result['dataset_id']
    raw, auth = sign(key, body, 'kb-other', owner=10000)
    assert client.post('/api/tools/read', content=raw, headers=auth).status_code == 403


@pytest.mark.parametrize('grant', [
    {'id': 'kb-b', 'tenant_id': 10000},
    {'id': 'kb-a', 'tenant_id': '10000'},
    {'id': 'kb-a', 'tenant_id': True},
    {'id': 'kb-a', 'tenant_id': 0},
    {'id': 'kb-a', 'tenant_id': 10001},
    {'id': 'kb-a', 'tenant_id': 10001, 'my_permission': 'owner'},
])
def test_invalid_knowledge_base_grants_never_select_a_store(scoped, grant):
    client, app, grants, _, _ = scoped
    grants['kb-a'] = grant
    assert client.get('/api/datasets', headers=headers('kb-a')).status_code == 403
    assert not (app.state.workspaces.directory(10000) / 'knowledge-bases' / 'kb-a').exists()


def test_explicit_read_only_grant_wins_over_tenant_owner_role(scoped):
    client, _, grants, _, _ = scoped
    grants['kb-a'] = {'id': 'kb-a', 'tenant_id': 10000, 'my_permission': 'viewer'}
    assert client.get('/api/datasets', headers=headers('kb-a')).status_code == 200
    assert client.post('/api/import', headers=headers('kb-a'), files={'file': ('x.xlsx', b'no')}).status_code == 403


def test_kb_model_configuration_inherits_only_its_owner_tenant(tmp_path, monkeypatch):
    pilot = tmp_path / 'pilot'
    pilot.mkdir()
    config = tmp_path / 'global.json'
    config.write_text(json.dumps({'chat': {'base_url': 'https://pilot.example/v1', 'model': 'fixture', 'api_key': 'pilot-only'}}))
    monkeypatch.setenv('STRUCTURED_MODEL_CONFIG', str(config))
    assert read_model(pilot / 'knowledge-bases' / 'kb-a')['api_key'] == 'pilot-only'
    other = pilot / 'workspaces' / '10001'
    other.mkdir(parents=True)
    assert read_model(other / 'knowledge-bases' / 'kb-b') == {}
    save_model(other, {'base_url': 'https://own.example/v1', 'model': 'own', 'api_key': 'own-only'})
    assert read_model(other / 'knowledge-bases' / 'kb-b')['api_key'] == 'own-only'
