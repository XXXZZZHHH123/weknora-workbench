"""Authorized upload/review/publication and signed read workflow (parser mocked)."""
import base64
from copy import deepcopy
import hashlib
import hmac
import json
import time

import pytest

from test_native_knowledge_import import native, headers
from test_pdf_tables import bundle


@pytest.fixture
def pdf_service(native, monkeypatch):
    client, app, state, root = native
    content = b'%PDF-1.7\n isolated API fixture, not an OCR acceptance sample'
    state['contents'] = content
    state['document'].update(file_name='source.pdf', file_type='pdf', file_size=len(content),
                             file_hash=hashlib.md5(content).hexdigest())
    raw = bundle([['Item', 'Value'], ['A', '-1.20'], ['B', '']], [['C', '2.30']])
    async def parse(path, source=None, **kwargs):
        result = deepcopy(raw)
        result['source'] = source
        return result
    monkeypatch.setattr('structured_data.pdf_parser_bridge.parse_pdf', parse)
    monkeypatch.setattr('structured_data.pdf_parser_bridge.parser_availability', lambda: {'configured': True, 'engine': 'fixture'})
    key = b'pdf-read-fixture-key-012345678901234567890'
    key_path = root / 'read-key'
    key_path.write_bytes(key)
    monkeypatch.setenv('STRUCTURED_SERVICE_TOKEN_FILE', str(key_path))
    return client, app, state, root, key


def imported(service, native=False):
    client, _, state, _, _ = service
    if native:
        response = client.post('/api/pdf/import-knowledge', json={'source_knowledge_id': 'source-a'}, headers=headers())
    else:
        response = client.post('/api/pdf/import', files={'file': ('source.pdf', state['contents'], 'application/pdf')}, headers=headers())
    assert response.status_code == 200, response.text
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        job = client.get('/api/pdf/jobs/' + response.json()['id'], headers=headers()).json()
        if job['status'] != 'processing':
            assert job['status'] == 'completed', job
            return client.get('/api/pdf/drafts/' + job['draft_id'], headers=headers()).json()
        time.sleep(.01)
    raise AssertionError('mocked PDF import did not finish')


def finish_pdf_job(client, job_id):
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        job = client.get('/api/pdf/jobs/' + job_id, headers=headers()).json()
        if job['status'] != 'processing':
            assert job['status'] == 'completed', job
            return job
        time.sleep(.01)
    raise AssertionError('mocked PDF parse job did not finish')


def plan(draft):
    return dict(bundle_sha256=draft['bundle_sha256'], fragment_ids=['table-1', 'table-2'],
                header_rows={'table-1': [{'row': 0, 'reason': 'original header'}], 'table-2': []},
                columns=[dict(name='item', type='text', source_columns={'table-1': 0, 'table-2': 0}),
                         dict(name='amount', type='decimal', source_columns={'table-1': 1, 'table-2': 1})])


def read(service, operation, arguments, kb='kb-a'):
    client, _, _, _, key = service
    authorization = 'Bearer owner'
    identity = {'user_id': 'user-a', 'email': 'a@example.test', 'display_name': 'A', 'tenant_id': 10000,
                'role': 'viewer', 'knowledge_base_id': kb, 'knowledge_base_tenant_id': 10000,
                'authorization_sha256': hashlib.sha256(authorization.encode()).hexdigest()}
    encoded = base64.urlsafe_b64encode(json.dumps(identity, separators=(',', ':')).encode()).decode().rstrip('=')
    body = json.dumps({'operation': operation, 'arguments': arguments}, separators=(',', ':')).encode()
    stamp = str(int(time.time()))
    signature = hmac.new(key, '\n'.join([stamp, 'POST', '/api/tools/read', encoded, hashlib.sha256(body).hexdigest()]).encode(), hashlib.sha256).hexdigest()
    return client.post('/api/tools/read', content=body, headers={'Content-Type': 'application/json',
        'Authorization': authorization,
        'X-Research-Identity': encoded, 'X-Research-Timestamp': stamp, 'X-Research-Signature': signature})


def test_upload_review_crosspage_query_and_original_integrity(pdf_service):
    client, _, state, root, _ = pdf_service
    draft = imported(pdf_service)
    base = '/api/pdf/drafts/' + draft['draft_id']
    assert read(pdf_service, 'list_pdf_tables', {}).json()['tables'] == []
    assert client.get(base + '/source', headers=headers()).content == state['contents']
    assert client.get('/api/pdf/drafts', headers=headers()).json()['drafts'][0]['draft_id'] == draft['draft_id']
    mapping = plan(draft)
    preview = client.post(base + '/preview', json={'plan': mapping}, headers=headers())
    assert preview.status_code == 200, preview.text
    assert len(preview.json()['rows']) == 3
    assert client.post(base + '/publish', json={'plan': mapping}, headers=headers()).status_code == 400
    assert client.post(base + '/publish', json={'plan': mapping, 'reviewed': 'true'}, headers=headers()).status_code == 422
    published = client.post(base + '/publish', json={'plan': mapping, 'reviewed': True}, headers=headers())
    assert published.status_code == 200, published.text
    table_id = published.json()['table_id']
    assert read(pdf_service, 'list_datasets', {}).json()['pdf_tables'][0]['table_id'] == table_id
    result = read(pdf_service, 'query_pdf_table', {'table_id': table_id, 'sql': 'SELECT sum(amount) AS total, count(amount) AS n FROM data', 'limit': 1}).json()
    assert result['status'] == 'ok' and result['source_kind'] == 'pdf_table'
    assert float(result['rows'][0][0]) == 1.1 and result['rows'][0][1] == 2
    assert result['input_count'] == 3 and result['input_complete']
    row = read(pdf_service, 'read_pdf_table', {'table_id': table_id, 'offset': 2, 'limit': 1}).json()['rows'][0]
    assert row['values']['item'] == 'C' and row['source_cells']['amount'][0]['page'] == 2
    assert read(pdf_service, 'list_pdf_tables', {}, kb='kb-other').json()['tables'] == []
    denied = read(pdf_service, 'query_pdf_table', {'table_id': table_id, 'sql': 'SELECT * FROM data'}, kb='kb-other')
    assert denied.status_code == 400 or denied.json().get('status') == 'error'
    original = next(root.rglob('originals/*.pdf'))
    original.write_bytes(b'changed')
    assert read(pdf_service, 'query_pdf_table', {'table_id': table_id, 'sql': 'SELECT * FROM data'}).json()['status'] == 'error'
    assert client.get(base + '/source', headers=headers()).status_code == 400


def test_native_pdf_uses_authorized_original_and_default_excel_stays_restricted(pdf_service):
    client, _, state, _, _ = pdf_service
    draft = imported(pdf_service, native=True)
    assert draft['bundle']['source']['knowledge_id'] == 'source-a'
    assert draft['bundle']['source']['knowledge_base_id'] == 'kb-a'
    assert client.post('/api/import-knowledge', json={'source_knowledge_id': 'source-a'}, headers=headers()).status_code == 400
    assert client.post('/api/pdf/import-knowledge', json={'source_knowledge_id': 'source-a'}, headers=headers(kb='kb-other')).status_code == 403
    state['changed_after_download'] = True
    assert client.post('/api/pdf/import-knowledge', json={'source_knowledge_id': 'source-a'}, headers=headers()).status_code == 409


def test_reparse_accepts_only_server_configured_mineru_profiles_and_keeps_old_draft(pdf_service, monkeypatch):
    from structured_data.pdf_parser_bridge import ParserConfig
    client, _, state, _, _ = pdf_service
    original = imported(pdf_service)
    base = f"/api/pdf/drafts/{original['draft_id']}/reparse"
    monkeypatch.setattr(ParserConfig, 'from_env', classmethod(lambda cls: ParserConfig(
        'mineru_v1', 'http://127.0.0.1:18733', api_key='server-only-key',
        tier='standard', ocr_mode='auto')))
    calls = []
    async def parse(path, source=None, **kwargs):
        calls.append((path.read_bytes(), source, kwargs))
        result = deepcopy(bundle([['Item', 'Value'], ['A', '1.20']]))
        result['source'] = source
        result['parser'] = {'engine': 'mineru', 'version': 'fixture'}
        return result
    monkeypatch.setattr('structured_data.pdf_parser_bridge.parse_pdf', parse)

    assert client.post(base, json={'tier': 'basic', 'ocr_mode': 'bad'}, headers=headers()).status_code == 422
    assert client.post(base, json={'tier': 'basic', 'ocr_mode': 'ocr', 'url': 'http://evil.test'}, headers=headers()).status_code == 422
    assert client.post(base, json={'tier': 'flash', 'ocr_mode': 'auto'}, headers=headers()).status_code == 422
    assert not calls

    started = client.post(base, json={'tier': 'basic', 'ocr_mode': 'ocr'}, headers=headers())
    assert started.status_code == 200, started.text
    completed = finish_pdf_job(client, started.json()['id'])
    second = client.get('/api/pdf/drafts/' + completed['draft_id'], headers=headers()).json()
    assert second['draft_id'] != original['draft_id']
    assert second['bundle']['source']['sha256'] == original['bundle']['source']['sha256']
    assert second['bundle']['parser']['requested_tier'] == 'basic'
    assert second['bundle']['parser']['requested_ocr_mode'] == 'ocr'
    assert not ({'url', 'api_key'} & set(second['bundle']['parser']))
    assert calls[0][0] == state['contents']
    assert calls[0][2]['config'].tier == 'basic'
    assert calls[0][2]['config'].ocr_mode == 'ocr'
    assert client.get('/api/pdf/drafts/' + original['draft_id'], headers=headers()).status_code == 200
    assert read(pdf_service, 'list_pdf_tables', {}).json()['tables'] == []

    monkeypatch.setattr(ParserConfig, 'from_env', classmethod(lambda cls: ParserConfig(
        'paddleocr_vl', 'http://127.0.0.1:8080')))
    unsupported = client.post(base, json={'tier': 'standard', 'ocr_mode': 'auto'}, headers=headers())
    assert unsupported.status_code == 409
    assert len(calls) == 1


def test_native_pdf_reparse_revalidates_current_authorized_source(pdf_service, monkeypatch):
    from structured_data.pdf_parser_bridge import ParserConfig
    client, _, state, _, _ = pdf_service
    original = imported(pdf_service, native=True)
    source = original['bundle']['source']
    base = f"/api/pdf/drafts/{original['draft_id']}/reparse"
    monkeypatch.setattr(ParserConfig, 'from_env', classmethod(lambda cls: ParserConfig(
        'mineru_v1', 'http://127.0.0.1:18733', tier='standard')))
    calls = []
    async def parse(path, source=None, **kwargs):
        calls.append((path.read_bytes(), source, kwargs))
        result = deepcopy(bundle([['Item', 'Value'], ['A', '1.20']]))
        result['source'] = source
        result['parser'] = {'engine': 'mineru', 'version': 'fixture'}
        return result
    monkeypatch.setattr('structured_data.pdf_parser_bridge.parse_pdf', parse)

    started = client.post(base, json={'tier': 'standard', 'ocr_mode': 'auto'}, headers=headers())
    assert started.status_code == 200, started.text
    second = client.get('/api/pdf/drafts/' + finish_pdf_job(client, started.json()['id'])['draft_id'],
                        headers=headers()).json()
    assert second['bundle']['source']['knowledge_id'] == 'source-a'
    assert second['bundle']['source']['knowledge_base_id'] == 'kb-a'
    assert second['bundle']['source']['sha256'] == source['sha256']
    assert calls[0][0] == state['contents']
    assert calls[0][1]['knowledge_id'] == 'source-a'
    assert len([path for path, _ in state['calls'] if path.endswith('/download')]) == 2

    state['changed_after_download'] = True
    denied = client.post(base, json={'tier': 'basic', 'ocr_mode': 'ocr'}, headers=headers())
    assert denied.status_code == 409
    assert len(calls) == 1


def test_reopening_native_pdf_reuses_latest_verified_matching_profile(pdf_service, monkeypatch):
    from structured_data.pdf_parser_bridge import ParserConfig
    client, _, state, _, _ = pdf_service
    config = ParserConfig('mineru_v1', 'http://127.0.0.1:18733', api_key='secret-fixture-key',
                          tier='standard', ocr_mode='auto')
    monkeypatch.setattr(ParserConfig, 'from_env', classmethod(lambda cls: config))
    calls = []
    async def parse(path, source=None, **kwargs):
        calls.append(path.read_bytes())
        result = deepcopy(bundle([['Item', 'Value'], ['A', '1.20']]))
        result['source'] = source
        result['parser'] = {'engine': 'mineru', 'version': 'fixture'}
        return result
    monkeypatch.setattr('structured_data.pdf_parser_bridge.parse_pdf', parse)
    first = imported(pdf_service, native=True)
    assert first['bundle']['parser']['requested_tier'] == 'standard'
    first_profile = first['bundle']['parser']['profile_sha256']
    assert len(first_profile) == 64
    assert '127.0.0.1:18733' not in json.dumps(first['bundle']['parser'])
    assert 'secret-fixture-key' not in json.dumps(first['bundle']['parser'])

    reused = client.post('/api/pdf/import-knowledge', json={'source_knowledge_id': 'source-a'}, headers=headers())
    assert reused.status_code == 200, reused.text
    assert reused.json()['reused'] is True
    assert reused.json()['draft_id'] == first['draft_id']
    assert len(calls) == 1
    # Reuse still downloads through the authorized path and checks the content SHA.
    assert len([path for path, _ in state['calls'] if path.endswith('/download')]) == 2

    config.tier = 'basic'
    new_profile = client.post('/api/pdf/import-knowledge', json={'source_knowledge_id': 'source-a'}, headers=headers())
    assert new_profile.status_code == 200, new_profile.text
    assert 'reused' not in new_profile.json()
    second = client.get('/api/pdf/drafts/' + finish_pdf_job(client, new_profile.json()['id'])['draft_id'],
                        headers=headers()).json()
    assert second['bundle']['parser']['requested_tier'] == 'basic'
    assert len(calls) == 2

    config.tier = 'standard'
    config.url = 'http://127.0.0.1:18734/'
    changed_endpoint = client.post('/api/pdf/import-knowledge', json={'source_knowledge_id': 'source-a'}, headers=headers())
    assert changed_endpoint.status_code == 200, changed_endpoint.text
    assert 'reused' not in changed_endpoint.json()
    third = client.get('/api/pdf/drafts/' + finish_pdf_job(client, changed_endpoint.json()['id'])['draft_id'],
                       headers=headers()).json()
    assert third['bundle']['parser']['profile_sha256'] != first_profile
    parser_json = json.dumps(third['bundle']['parser'])
    assert '127.0.0.1:18734' not in parser_json and 'secret-fixture-key' not in parser_json
    assert len(calls) == 3


def test_parser_profile_fingerprint_excludes_api_key():
    from structured_data.pdf_api import _parser_profile_sha256
    from structured_data.pdf_parser_bridge import ParserConfig
    first = ParserConfig('mineru_v1', 'https://parser.internal/api', api_key='one', tier='basic', ocr_mode='ocr')
    second = ParserConfig('mineru_v1', 'https://parser.internal/api/', api_key='two', tier='basic', ocr_mode='ocr')
    assert _parser_profile_sha256(first) == _parser_profile_sha256(second)


def test_auth_and_mutation_boundaries(pdf_service, monkeypatch):
    client, _, _, _, _ = pdf_service
    assert client.get('/api/pdf/drafts').status_code == 401
    assert client.post('/api/pdf/import', files={'file': ('a.pdf', b'%PDF-x')}, headers=headers(role='viewer')).status_code == 403
    assert client.post('/api/pdf/import', files={'file': ('a.pdf', b'%PDF-x')}, headers=headers() | {'Origin': 'https://evil.test'}).status_code == 403
    assert client.post('/api/pdf/import', files={'file': ('a.pdf', b'not pdf')}, headers=headers()).status_code == 400
    monkeypatch.setattr('structured_data.pdf_parser_bridge.parser_availability', lambda: {'configured': False, 'engine': None})
    assert client.post('/api/pdf/import', files={'file': ('a.pdf', b'%PDF-x')}, headers=headers()).status_code == 503
    assert read(pdf_service, 'publish_pdf_table', {}).status_code == 400
    schema = read(pdf_service, 'describe_tools', {'operations': ['query_pdf_table']}).json()['tools'][0]
    assert set(schema['inputSchema']['properties']) == {'table_id', 'sql', 'limit'}
    assert read(pdf_service, 'query_pdf_table', {'table_id': 'x', 'sql': 'SELECT 1', 'url': 'http://evil.test'}).status_code == 400


def test_model_plan_validation_never_publishes(pdf_service, monkeypatch):
    client, _, _, _, _ = pdf_service
    draft = imported(pdf_service)
    base = '/api/pdf/drafts/' + draft['draft_id']
    mapping = plan(draft)
    monkeypatch.setattr('structured_data.pdf_api.suggest_plan', lambda *args: {'status': 'ready', 'message': 'fixture', 'plan': mapping})
    result = client.post(base + '/suggest', json={'fragment_ids': ['table-1', 'table-2']}, headers=headers())
    assert result.status_code == 200 and len(result.json()['preview']['rows']) == 3
    assert read(pdf_service, 'list_pdf_tables', {}).json()['tables'] == []
    mapping['header_rows']['table-2'] = [{'row': 12345, 'reason': 'fabricated'}]
    assert client.post(base + '/suggest', json={'fragment_ids': ['table-1', 'table-2']}, headers=headers()).status_code == 400


def test_model_suggestion_binds_complete_fragments_and_handles_truncated_output(tmp_path, monkeypatch):
    import httpx
    from structured_data.pdf_api import suggest_plan, PDFSuggestRequest
    from structured_data.pdf_tables import PDFTableStore
    raw = bundle([['Item', 'Value'], ['A', '-1.20']], [['C', '2.30']])
    raw['page_context'] = [{'page': 1, 'block_id': 'p1-context-1', 'bbox': [0.1, 0.2, 0.8, 0.3],
                            'text': 'Amounts in kg', 'text_parts': ['Amounts in kg']}]
    raw['page_context_coverage'] = 'complete'
    draft = PDFTableStore(tmp_path).create_draft(raw)
    mapping = plan(draft)
    reply = {'status': 'ready', 'message': 'fixture evidence', 'plan': mapping}
    completion = {'choices': [{'finish_reason': 'stop', 'message': {'content': json.dumps(reply)}}]}
    requests = []
    class Model:
        def __init__(self, **kwargs):
            assert kwargs['follow_redirects'] is False and kwargs['trust_env'] is False
        def __enter__(self): return self
        def __exit__(self, *args): pass
        def post(self, url, **kwargs):
            requests.append(kwargs['json'])
            return httpx.Response(200, json=completion)
    monkeypatch.setattr(httpx, 'Client', Model)
    monkeypatch.setattr('structured_data.model_settings.read_model', lambda root: {'base_url': 'http://model.test/v1', 'api_key': 'fixture', 'model': 'fixture'})
    body = PDFSuggestRequest(fragment_ids=['table-1', 'table-2'])
    result = suggest_plan(draft, body, tmp_path)
    assert result['status'] == 'ready'
    evidence = json.loads(requests[0]['messages'][1]['content'])
    assert evidence['evidence']['fragments'] == raw['fragments']
    assert evidence['evidence']['page_context'][0]['text'] == 'Amounts in kg'
    prompt = requests[0]['messages'][0]['content']
    assert 'unit:可选字符串' in prompt
    assert '绝不能推测或标准化单位' in prompt
    assert PDFTableStore(tmp_path).validate_plan(draft['draft_id'], result['plan'])['coverage']['logical_row_count'] == 2
    completion['choices'][0]['finish_reason'] = 'length'
    with pytest.raises(ValueError, match='未正常完成'):
        suggest_plan(draft, body, tmp_path)
    completion['choices'][0].update(finish_reason='stop', message={'content': 'not JSON'})
    with pytest.raises(ValueError, match='格式无效'):
        suggest_plan(draft, body, tmp_path)


def mock_native_pdf_gateway(monkeypatch, data, status=200):
    import httpx
    from types import SimpleNamespace
    from structured_data import model_gateway
    calls = []

    def upstream(request):
        assert request.method == 'POST'
        assert str(request.url) == 'http://native.test/api/v1/knowledge-bases/kb-a/structured-model/chat'
        calls.append(request)
        return httpx.Response(status, json={'success': True, 'data': data})

    def client_factory(**options):
        assert options['trust_env'] is False and options['follow_redirects'] is False
        return httpx.Client(transport=httpx.MockTransport(upstream), **options)

    # Keep Starlette's own httpx client intact, including versions whose
    # TestClient.post delegates directly to httpx.Client.post.
    monkeypatch.setattr(model_gateway, 'httpx', SimpleNamespace(
        Client=client_factory, HTTPError=httpx.HTTPError))
    return calls


def test_native_model_pdf_suggestion_keeps_verified_context_and_requires_publication(pdf_service, monkeypatch):
    from structured_data.model_settings import save_model
    client, _, _, root, _ = pdf_service
    draft = imported(pdf_service)
    save_model(root / 'knowledge-bases' / 'kb-a', {'native_model_id': 'configured-chat'})
    mapping = plan(draft)
    calls = mock_native_pdf_gateway(monkeypatch, {
        'model_id': 'configured-chat', 'finish_reason': 'stop',
        'content': json.dumps({'status': 'ready', 'message': 'fixture evidence', 'plan': mapping})})
    path = '/api/pdf/drafts/' + draft['draft_id'] + '/suggest'
    response = client.post(path,
                           json={'fragment_ids': ['table-1', 'table-2']}, headers=headers())
    assert str(response.request.url) == 'http://testserver' + path
    assert response.status_code == 200, response.text
    assert len(response.json()['preview']['rows']) == 3
    assert read(pdf_service, 'list_pdf_tables', {}).json()['tables'] == []
    assert len(calls) == 1
    request = calls[0]
    assert request.headers['authorization'] == 'Bearer owner'
    assert request.headers['x-tenant-id'] == '10000'
    body = json.loads(request.content)
    assert {k: v for k, v in body.items() if k != 'messages'} == {
        'model_id': 'configured-chat', 'max_tokens': 6000, 'temperature': 0, 'enable_thinking': False}
    evidence = json.loads(body['messages'][1]['content'])
    assert evidence['bundle_sha256'] == draft['bundle_sha256']
    assert evidence['evidence']['fragments'] == draft['bundle']['fragments']


@pytest.mark.parametrize('failure', ['permission', 'wrong_model', 'truncated', 'invalid_json', 'wrong_source'])
def test_native_pdf_model_failures_do_not_fall_back_or_publish(pdf_service, monkeypatch, failure):
    client, _, _, _, _ = pdf_service
    draft = imported(pdf_service)
    mapping = plan(draft)
    if failure == 'wrong_source':
        mapping['bundle_sha256'] = '0' * 64
    data = {'model_id': 'other-chat' if failure == 'wrong_model' else 'configured-chat',
            'finish_reason': 'length' if failure == 'truncated' else 'stop',
            'content': 'not JSON' if failure == 'invalid_json' else json.dumps({
                'status': 'ready', 'message': 'fixture evidence', 'plan': mapping})}
    calls = mock_native_pdf_gateway(monkeypatch, data, 403 if failure == 'permission' else 200)
    monkeypatch.setattr('structured_data.model_settings.read_model', lambda root: {
        'native_model_id': 'configured-chat', 'base_url': 'http://legacy.invalid/v1',
        'api_key': 'legacy-provider-secret', 'model': 'legacy-chat'})
    path = '/api/pdf/drafts/' + draft['draft_id'] + '/suggest'
    response = client.post(path,
                           json={'fragment_ids': ['table-1', 'table-2']}, headers=headers())
    assert str(response.request.url) == 'http://testserver' + path
    assert response.status_code == 400, response.text
    assert 'detail' in response.json()
    assert len(calls) == 1
    assert b'legacy-provider-secret' not in calls[0].content
    assert 'legacy-provider-secret' not in response.text
    assert read(pdf_service, 'list_pdf_tables', {}).json()['tables'] == []
