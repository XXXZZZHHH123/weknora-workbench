"""Platform model selection and scoped inference, without external model calls."""
import asyncio
from contextlib import contextmanager
import json
import shutil

import httpx
import pytest

from structured_data import model_gateway as gateway
from structured_data.model_settings import read_model, save_model
from structured_data.workspaces import Identity, current_identity
from test_source_context import duplicate_source  # noqa: F401 (pytest fixture)


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

    def list_models(client, url, headers):
        assert url.endswith('/api/v1/models')
        calls.append((url, headers))
        return httpx.Response(200, json={'success': True, 'data': rows})

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
    monkeypatch.setattr(httpx.Client, 'get', list_models)
    return calls


def test_choices_filter_real_enabled_chat_models_and_redact_provider_metadata(monkeypatch):
    calls = async_metadata(monkeypatch, [native(parameters={'api_key': 'never-expose', 'base_url': 'https://private.invalid'}),
        native('embedding', type='Embedding'), native('disabled', status='download_failed'),
        native('foreign', tenant_id=10001), native('builtin', tenant_id=10001, is_builtin=True)])
    with context():
        result = gateway.available_models()
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
    models = [native(), native('native-b', is_default=True)]

    def list_models(client, url, headers):
        assert url.endswith('/api/v1/models')
        calls.append((url, dict(headers)))
        return httpx.Response(200, json={'success': True, 'data': models})

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
                    'summary_model_id': 'native-a',
                    'my_permission': 'viewer' if headers['Authorization'] == 'Bearer readonly' else 'editor'}}
            else:
                row = models if url.endswith('/models') else next((m for m in models if m['id'] == url.rsplit('/', 1)[-1]), None)
                if row is None: return httpx.Response(404, json={})
                document = {'success': True, 'data': row}
            return httpx.Response(200, json=document)
    monkeypatch.setattr(httpx, 'AsyncClient', Client)
    monkeypatch.setattr(httpx.Client, 'get', list_models)
    monkeypatch.setenv('STRUCTURED_TENANT_ID', '10000')
    monkeypatch.setenv('STRUCTURED_MODEL_CONFIG', str(tmp_path / 'missing'))
    headers = {'Authorization': 'Bearer editor', 'X-Tenant-ID': '10000', 'X-Knowledge-Base-ID': 'kb-a', 'Origin': 'http://testserver'}
    with TestClient(create_app(tmp_path)) as client:
        response = client.get('/api/model-settings', headers=headers)
        assert response.status_code == 200
        assert response.json()['models'] == [{'id': name, 'name': 'Configured chat'} for name in ['native-a', 'native-b']]
        assert response.json()['effective_model'] == {'id': 'native-a', 'name': 'Configured chat'}
        assert response.json()['model_selection'] == 'automatic'
        assert response.json()['native_model_id'] == '' and response.json()['can_edit'] is True
        first = tmp_path / 'knowledge-bases' / 'kb-a' / 'model-settings.json'
        assert not first.exists()
        assert client.post('/api/model-settings', headers=headers, json={'native_model_id': 'native-b'}).status_code == 200
        assert json.loads(first.read_text()) == {'chat': {'native_model_id': 'native-b'}}
        explicit = client.get('/api/model-settings', headers=headers).json()
        assert explicit['model_selection'] == 'explicit' and explicit['effective_model']['id'] == 'native-b'
        other = client.get('/api/model-settings', headers=headers | {'X-Knowledge-Base-ID': 'kb-b'}).json()
        assert other['native_model_id'] == '' and other['effective_model']['id'] == 'native-a'
        readonly = headers | {'Authorization': 'Bearer readonly'}
        assert client.get('/api/model-settings', headers=readonly).json()['can_edit'] is False
        assert client.post('/api/model-settings', headers=readonly, json={'native_model_id': 'native-a'}).status_code == 403
        assert client.post('/api/model-settings', headers=headers, json={'native_model_id': 'gone'}).status_code == 400
        assert client.post('/api/model-settings', headers=headers, json={'native_model_id': 'native-a', 'api_key': 'never'}).status_code == 400
        assert json.loads(first.read_text()) == {'chat': {'native_model_id': 'native-b'}}
        models[:] = [native()]
        stale = client.get('/api/model-settings', headers=headers).json()
        assert stale['native_model_id'] == 'native-b' and stale['effective_model'] is None
        assert stale['model_selection'] == 'unavailable' and '重新选择' in stale['model_error']
    assert all(h['Authorization'] in {'Bearer editor', 'Bearer readonly'} for _, h in calls)
    assert gateway.current_authorization.get() == '' and current_identity.get() is None


@pytest.mark.parametrize('summary,models,explicit,expected,error_hint', [
    ('summary', [native('default', is_default=True), native('summary')], '', 'summary', ''),
    ('gone', [native('tenant', is_default=True),
              native('workspace', tenant_id=10001, is_builtin=True, default_for=['KnowledgeQA'])],
     '', 'workspace', ''),
    ('', [native('other'), native('default', is_default=True)], '', 'default', ''),
    ('inactive', [native('inactive', status='download_failed'), native('sole')], '', 'sole', ''),
    ('summary', [native('summary'), native('explicit')], 'explicit', 'explicit', ''),
    ('inactive', [native('inactive', status='download_failed'), native('embedding', type='Embedding'),
                  native('foreign', tenant_id=10001)], '', '', '模型管理'),
    ('', [native('first'), native('global', tenant_id=10001, is_builtin=True, is_default=True)],
     '', '', '未确定唯一默认模型'),
])
def test_signed_source_binding_uses_effective_kb_model(duplicate_source, tmp_path, monkeypatch,
                                                       summary, models, explicit, expected, error_hint):
    from fastapi.testclient import TestClient
    from structured_data.server import create_app
    from test_knowledge_base_scope import sign

    store, dataset = duplicate_source
    root = tmp_path / 'workbench'
    kb_root = root / 'knowledge-bases' / 'kb-a'
    shutil.copytree(store.root, kb_root)
    if explicit:
        save_model(kb_root, {'native_model_id': explicit})
    # The scoped runtime and GET must never open the legacy provider file.
    legacy = tmp_path / 'legacy-model.json'
    legacy.write_text('this legacy configuration must not be parsed')
    monkeypatch.setenv('STRUCTURED_MODEL_CONFIG', str(legacy))
    key = b'synthetic-model-fallback-service-signing-key'
    key_path = tmp_path / 'fixture-signing-key'
    key_path.write_bytes(key)
    monkeypatch.setenv('STRUCTURED_SERVICE_TOKEN_FILE', str(key_path))
    monkeypatch.setenv('STRUCTURED_TENANT_ID', '10000')

    async def get_identity(client, url, headers):
        assert headers['X-Tenant-ID'] == '10000'
        if url.endswith('/auth/me'):
            data = {'user': {'id': 'tool-user', 'tenant_id': 10000},
                    'memberships': [{'tenant_id': 10000, 'role': 'viewer'}]}
        else:
            assert url.endswith('/api/v1/knowledge-bases/kb-a')
            data = {'id': 'kb-a', 'tenant_id': 10000, 'my_permission': 'viewer', 'summary_model_id': summary}
        return httpx.Response(200, json={'success': True, 'data': data}, request=httpx.Request('GET', url))

    metadata_calls, completion_calls = [], []

    def list_models(client, url, headers):
        assert url.endswith('/api/v1/models') and headers == {
            'Authorization': 'Bearer service-reader', 'X-Tenant-ID': '10000'}
        metadata_calls.append(url)
        return httpx.Response(200, json={'success': True, 'data': models})

    def complete(client, url, headers, **options):
        assert url.endswith('/kb-a/structured-model/plan') and headers['Authorization'] == 'Bearer service-reader'
        body = options['json']
        assert body['model_id'] == expected
        payload = json.loads(body['messages'][-1]['content'])
        assert payload['requested_series_ids'] == ['G']
        assert {field['series_id'] for field in payload['source_groups'][0]} == {'C', 'E', 'G', 'I'}
        completion_calls.append(payload['question'])
        binding = {'decision': 'resolved', 'bindings': [{'considered_ids': ['C', 'E', 'G', 'I'],
                   'selected_ids': ['G'], 'reason': 'The fixture request identifies this source.'}]}
        return httpx.Response(200, json={'success': True, 'data': {
            'model_id': expected, 'content': json.dumps(binding), 'finish_reason': 'stop'}})

    monkeypatch.setattr(httpx.AsyncClient, 'get', get_identity)
    monkeypatch.setattr(httpx.Client, 'get', list_models)
    monkeypatch.setattr(httpx.Client, 'post', complete)

    with TestClient(create_app(root)) as client:
        settings = client.get('/api/model-settings', headers={'Authorization': 'Bearer service-reader',
            'X-Tenant-ID': '10000', 'X-Knowledge-Base-ID': 'kb-a'}).json()
        assert settings['native_model_id'] == explicit and not settings['can_edit']
        assert settings['model_selection'] == ('explicit' if explicit else 'automatic' if expected else 'unavailable')
        if expected:
            assert settings['effective_model']['id'] == expected and not settings['model_error']
        else:
            assert settings['effective_model'] is None and error_hint in settings['model_error']

        operations = {'query_series': {'series_id': 'G'}, 'analyze_table': {
            'series_ids': ['G'], 'sql': 'SELECT value FROM data'}}
        for operation, arguments in operations.items():
            raw, headers = sign(key, {'operation': operation, 'arguments': arguments | {
                'dataset_id': dataset, 'source_question': f'Full fixture request for {operation}'}}, kb='kb-a')
            response = client.post('/api/tools/read', content=raw, headers=headers)
            assert response.status_code == 200
            result = response.json()
            if not expected:
                assert result['code'] == 'source_binding_unavailable' and 'rows' not in result
                assert result['error'] == settings['model_error']
            elif operation == 'query_series':
                assert result['rows'][0]['value'] == 200 and result['rows'][0]['source_cell'] == 'G3'
            else:
                assert result['rows'] == [[200]]

    assert len(metadata_calls) == (1 if explicit else 3)
    assert len(completion_calls) == (2 if expected else 0)
    assert (kb_root / 'model-settings.json').exists() == bool(explicit)


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
