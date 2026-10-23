"""Platform model selection and scoped inference, without external model calls."""
import asyncio
from contextlib import contextmanager
import json

import httpx
import pytest

from structured_data import model_gateway as gateway
from structured_data.model_settings import read_model, save_model
from structured_data.workspaces import Identity, current_identity


@contextmanager
def context(*, user='user-a', tenant=10000, kb='kb-a', writable=True):
    identity = Identity(user, '', '', tenant, 'contributor', kb, tenant, writable)
    marker = current_identity.set(identity)
    authorization = gateway.current_authorization.set('Bearer fixture-login')
    try:
        yield identity
    finally:
        gateway.current_authorization.reset(authorization)
        current_identity.reset(marker)


def native(identifier='native-a', **extra):
    return {'id': identifier, 'tenant_id': 10000, 'name': 'provider-internal-name',
            'display_name': 'Configured chat', 'type': 'KnowledgeQA', 'status': 'active', **extra}


def async_metadata(monkeypatch, rows):
    calls = []
    class Client:
        def __init__(self, **options):
            assert options['trust_env'] is False and options['follow_redirects'] is False
        async def __aenter__(self): return self
        async def __aexit__(self, *args): pass
        async def get(self, url, headers):
            calls.append((url, headers))
            row = rows if url.endswith('/models') else next((r for r in rows if r['id'] == url.rsplit('/', 1)[-1]), None)
            return httpx.Response(200 if row is not None else 404, json={'success': row is not None, 'data': row})
    monkeypatch.setattr(httpx, 'AsyncClient', Client)
    return calls


def test_choices_filter_real_enabled_chat_models_and_redact_provider_metadata(monkeypatch):
    calls = async_metadata(monkeypatch, [native(parameters={'api_key': 'never-expose', 'base_url': 'https://private.invalid'}),
        native('embedding', type='Embedding'), native('disabled', status='download_failed'),
        native('foreign', tenant_id=10001), native('builtin', tenant_id=10001, is_builtin=True)])
    with context():
        result = asyncio.run(gateway.available_models())
    assert [r['id'] for r in result] == ['native-a', 'builtin']
    assert 'never-expose' not in json.dumps(result) and 'private.invalid' not in json.dumps(result)
    assert calls[0][1] == {'Authorization': 'Bearer fixture-login', 'X-Tenant-ID': '10000'}


def test_native_selection_removes_provider_secret_and_keeps_only_real_id(tmp_path, monkeypatch):
    async_metadata(monkeypatch, [native()])
    save_model(tmp_path, {'model': 'legacy', 'base_url': 'https://legacy.invalid', 'api_key': 'old-secret'})
    with context():
        metadata = asyncio.run(gateway.validate_model('native-a'))
        saved = save_model(tmp_path, {'native_model_id': metadata['id']})
    assert saved['native_model_id'] == 'native-a'
    document = (tmp_path / 'model-settings.json').read_text()
    assert 'old-secret' not in document and 'legacy.invalid' not in document
    assert read_model(tmp_path)['native_model_id'] == 'native-a'
    assert (tmp_path / 'model-settings.json').stat().st_mode & 0o777 == 0o600


@pytest.mark.parametrize('row', [native(type='Embedding'), native(status='download_failed'), native(tenant_id=10001)])
def test_unavailable_native_model_is_rejected_without_fallback(monkeypatch, row):
    async_metadata(monkeypatch, [row])
    with context(), pytest.raises(ValueError, match='模型'):
        asyncio.run(gateway.validate_model('native-a'))


def test_native_completion_uses_current_kb_login_and_does_not_send_provider_credentials(monkeypatch):
    calls = []
    def post(client, url, **options):
        calls.append((url, options))
        return httpx.Response(200, json={'success': True, 'data': {
            'model_id': 'native-a', 'content': '{"fields": []}', 'finish_reason': 'stop'}})
    monkeypatch.setattr(httpx.Client, 'post', post)
    with context():
        result = gateway.complete({'native_model_id': 'native-a', 'api_key': 'do-not-forward'},
            [{'role': 'user', 'content': 'fixture'}], max_tokens=3500, temperature=0, enable_thinking=False)
    assert result['choices'][0]['message']['content'] == '{"fields": []}'
    url, options = calls[0]
    assert url.endswith('/api/v1/knowledge-bases/kb-a/structured-model/chat')
    assert options['headers']['Authorization'] == 'Bearer fixture-login'
    assert options['json'] == {'model_id': 'native-a', 'messages': [{'role': 'user', 'content': 'fixture'}],
                               'max_tokens': 3500, 'temperature': 0, 'enable_thinking': False}
    assert 'do-not-forward' not in json.dumps(calls)


@pytest.mark.parametrize('status', [401, 403, 404, 429, 502])
def test_native_http_errors_do_not_fall_back_to_legacy(monkeypatch, status):
    calls = []
    def post(client, url, **options):
        calls.append(url)
        return httpx.Response(status, json={'detail': 'provider-secret-not-for-errors'})
    monkeypatch.setattr(httpx.Client, 'post', post)
    with context(), pytest.raises(ValueError) as error:
        gateway.complete({'native_model_id': 'native-a', 'base_url': 'https://legacy.invalid', 'api_key': 'secret'}, [])
    assert 'provider-secret' not in str(error.value)
    assert len(calls) == 1 and 'legacy.invalid' not in calls[0]


def test_native_scope_required_and_cache_identity_separates_models_and_principals():
    with pytest.raises(ValueError, match='登录'):
        gateway.complete({'native_model_id': 'native-a'}, [])
    with context(): first = gateway.cache_identity({'native_model_id': 'native-a'})
    with context(): second = gateway.cache_identity({'native_model_id': 'native-b'})
    with context(user='user-b'): third = gateway.cache_identity({'native_model_id': 'native-a'})
    with context(kb='kb-b'): fourth = gateway.cache_identity({'native_model_id': 'native-a'})
    assert len({first, second, third, fourth}) == 4
    assert 'fixture-login' not in first


def test_catalogue_threads_inherit_verified_context():
    from structured_data.field_resolver import narrow_catalogue
    columns = [{'id': 'id-' + str(i), 'name': 'Field ' + str(i)} for i in range(600)]
    seen = []
    def select(payload, root):
        seen.append((current_identity.get().user_id, gateway.current_authorization.get()))
        return {'fields': [{'candidate_ids': []}]}
    with context(): narrow_catalogue({'question': 'fixture', 'catalog': [{'sheet': 'Data', 'columns': columns}]}, None, select)
    assert len(seen) > 1 and all(item == ('user-a', 'Bearer fixture-login') for item in seen)


def test_field_understanding_and_ai_review_use_native_gateway(tmp_path, monkeypatch):
    from structured_data.ai_review import AIRequestBudget, _request
    from structured_data.field_resolver import ask_model
    save_model(tmp_path, {'native_model_id': 'native-a'})
    calls = []
    replies = [{'fields': [{'candidate_ids': []}]}, {'kind': 'mapping', 'mapping': {'blocks': []}}]
    def post(client, url, **options):
        calls.append((url, options))
        return httpx.Response(200, json={'success': True, 'data': {
            'model_id': 'native-a', 'content': json.dumps(replies.pop(0)), 'finish_reason': 'stop'}})
    monkeypatch.setattr(httpx.Client, 'post', post)
    with context():
        assert ask_model({'question': 'fixture', '_catalog_selection': True}, tmp_path)['fields'] == [{'candidate_ids': []}]
        # Mapping shape validation is owned by ai_review and still runs after transport.
        with pytest.raises(ValueError, match='AI'):
            _request(read_model(tmp_path), {'fixture': True}, AIRequestBudget(), None)
    assert len(calls) == 2
    assert calls[0][0].endswith('/kb-a/structured-model/plan')
    assert calls[1][0].endswith('/kb-a/structured-model/chat')
    assert all(options['json']['model_id'] == 'native-a' for _, options in calls)


def test_viewer_field_planning_uses_read_only_proxy_without_elevating_identity(monkeypatch):
    calls = []
    def post(client, url, **options):
        calls.append((url, options))
        assert current_identity.get().knowledge_base_can_write is False
        return httpx.Response(200, json={'success': True, 'data': {
            'model_id': 'native-a', 'content': '{}', 'finish_reason': 'stop'}})
    monkeypatch.setattr(httpx.Client, 'post', post)
    marker = current_identity.set(Identity('reader', '', '', 10000, 'viewer', 'kb-a', 10000, False))
    auth = gateway.current_authorization.set('Bearer fixture-reader')
    try:
        assert gateway.complete({'native_model_id': 'native-a'}, [], purpose='fields')['choices'][0]['message']['content'] == '{}'
        with pytest.raises(ValueError, match='任务'):
            gateway.complete({'native_model_id': 'native-a'}, [], purpose='https://arbitrary.invalid')
    finally:
        gateway.current_authorization.reset(auth)
        current_identity.reset(marker)
    assert len(calls) == 1 and calls[0][0].endswith('/kb-a/structured-model/plan')
    assert calls[0][1]['headers']['Authorization'] == 'Bearer fixture-reader'


def test_unscoped_legacy_settings_cannot_save_native_selection(tmp_path, monkeypatch):
    from fastapi.testclient import TestClient
    from structured_data.server import create_app
    class Client:
        def __init__(self, **options): pass
        async def __aenter__(self): return self
        async def __aexit__(self, *args): pass
        async def get(self, url, headers):
            assert url.endswith('/auth/me')
            return httpx.Response(200, json={'data': {'user': {'id': 'owner', 'tenant_id': 10000},
                'memberships': [{'tenant_id': 10000, 'role': 'owner'}]}})
    monkeypatch.setattr(httpx, 'AsyncClient', Client)
    monkeypatch.setenv('STRUCTURED_TENANT_ID', '10000')
    with TestClient(create_app(tmp_path)) as client:
        response = client.post('/api/model-settings', headers={'Authorization': 'Bearer fixture-owner',
            'X-Tenant-ID': '10000', 'Origin': 'http://testserver'}, json={'native_model_id': 'native-a'})
        assert response.status_code == 400 and '具体知识库' in response.json()['detail']
        assert not (tmp_path / 'model-settings.json').exists()


def test_native_http_configuration_is_kb_scoped_and_permission_checked(tmp_path, monkeypatch):
    from fastapi.testclient import TestClient
    from structured_data.server import create_app
    calls = []
    models = [native()]
    class Client:
        def __init__(self, **options): pass
        async def __aenter__(self): return self
        async def __aexit__(self, *args): pass
        async def get(self, url, headers):
            calls.append((url, dict(headers)))
            if url.endswith('/auth/me'):
                document = {'data': {'user': {'id': 'test-user', 'tenant_id': 10000},
                                    'memberships': [{'tenant_id': 10000, 'role': 'contributor'}]}}
            elif '/knowledge-bases/' in url:
                kb = url.rsplit('/', 1)[-1]
                document = {'success': True, 'data': {'id': kb, 'tenant_id': 10000,
                    'my_permission': 'viewer' if headers['Authorization'] == 'Bearer readonly' else 'editor'}}
            else:
                row = models if url.endswith('/models') else next((m for m in models if m['id'] == url.rsplit('/', 1)[-1]), None)
                if row is None: return httpx.Response(404, json={})
                document = {'success': True, 'data': row}
            return httpx.Response(200, json=document)
    monkeypatch.setattr(httpx, 'AsyncClient', Client)
    monkeypatch.setenv('STRUCTURED_TENANT_ID', '10000')
    monkeypatch.setenv('STRUCTURED_MODEL_CONFIG', str(tmp_path / 'missing'))
    headers = {'Authorization': 'Bearer editor', 'X-Tenant-ID': '10000', 'X-Knowledge-Base-ID': 'kb-a', 'Origin': 'http://testserver'}
    with TestClient(create_app(tmp_path)) as client:
        response = client.get('/api/model-settings', headers=headers)
        assert response.status_code == 200 and response.json()['models'] == [{'id': 'native-a', 'name': 'Configured chat'}]
        assert response.json()['can_edit'] is True
        assert client.post('/api/model-settings', headers=headers, json={'native_model_id': 'native-a'}).status_code == 200
        first = tmp_path / 'knowledge-bases' / 'kb-a' / 'model-settings.json'
        assert json.loads(first.read_text()) == {'chat': {'native_model_id': 'native-a'}}
        assert client.get('/api/model-settings', headers=headers | {'X-Knowledge-Base-ID': 'kb-b'}).json()['native_model_id'] == ''
        readonly = headers | {'Authorization': 'Bearer readonly'}
        assert client.get('/api/model-settings', headers=readonly).json()['can_edit'] is False
        assert client.post('/api/model-settings', headers=readonly, json={'native_model_id': 'native-a'}).status_code == 403
        assert client.post('/api/model-settings', headers=headers, json={'native_model_id': 'gone'}).status_code == 400
        assert client.post('/api/model-settings', headers=headers, json={'native_model_id': 'native-a', 'api_key': 'never'}).status_code == 400
        assert json.loads(first.read_text()) == {'chat': {'native_model_id': 'native-a'}}
    assert all(h['Authorization'] in {'Bearer editor', 'Bearer readonly'} for _, h in calls)
    assert gateway.current_authorization.get() == '' and current_identity.get() is None


def test_native_configuration_rechecks_write_after_model_verification(tmp_path, monkeypatch):
    from fastapi.testclient import TestClient
    from structured_data.server import create_app
    revoked = False
    class Client:
        def __init__(self, **options): pass
        async def __aenter__(self): return self
        async def __aexit__(self, *args): pass
        async def get(self, url, headers):
            nonlocal revoked
            if url.endswith('/auth/me'):
                data = {'data': {'user': {'id': 'test-user', 'tenant_id': 10000},
                                'memberships': [{'tenant_id': 10000, 'role': 'contributor'}]}}
            elif '/models/' in url:
                revoked = True
                data = {'success': True, 'data': native()}
            else:
                data = {'success': True, 'data': {'id': 'kb-a', 'tenant_id': 10000,
                        'my_permission': 'viewer' if revoked else 'editor'}}
            return httpx.Response(200, json=data)
    monkeypatch.setattr(httpx, 'AsyncClient', Client)
    monkeypatch.setenv('STRUCTURED_TENANT_ID', '10000')
    headers = {'Authorization': 'Bearer editor', 'X-Tenant-ID': '10000', 'X-Knowledge-Base-ID': 'kb-a', 'Origin': 'http://testserver'}
    with TestClient(create_app(tmp_path)) as client:
        response = client.post('/api/model-settings', headers=headers, json={'native_model_id': 'native-a'})
        assert response.status_code == 400 and '权限' in response.json()['detail']
        assert not (tmp_path / 'knowledge-bases' / 'kb-a' / 'model-settings.json').exists()
