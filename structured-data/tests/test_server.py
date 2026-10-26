from fastapi.testclient import TestClient
import httpx
import pytest

from structured_data.server import create_app
from test_store import snapshot


def test_workspace_assets_load_before_login_without_opening_data(tmp_path):
    with TestClient(create_app(tmp_path)) as client:
        page = client.get('/')
        assert './workspace.css' in page.text
        stylesheet = client.get('/workspace.css')
        assert stylesheet.status_code == 200
        assert stylesheet.headers['content-type'].startswith('text/css')
        assert stylesheet.headers['cache-control'] == 'no-cache'
        assert '.series-choice' in stylesheet.text
        assert client.get('/workspace.css', headers={'Host': 'malicious.invalid'}).status_code == 403
        pdf_script = client.get('/pdf-review.js')
        assert pdf_script.status_code == 200
        assert pdf_script.headers['content-type'].startswith('text/javascript')
        assert 'openKnowledgeSource' in pdf_script.text
        assert client.get('/api/pdf/drafts').status_code == 401
        assert client.get('/api/datasets').status_code == 401


def test_authentication_and_mcp_read_only_surface(tmp_path):
    app = create_app(tmp_path)
    token = (tmp_path / "service-token").read_text()
    with TestClient(app) as client:
        assert client.get("/health").status_code == 200
        page = client.get("/")
        assert page.status_code == 200
        assert '<title>中哲物产 · 表格分析</title>' in page.text
        assert '<h1 id="appTitle">表格分析</h1>' in page.text
        assert "棉花结构化数据" not in page.text
        assert client.get("/api/datasets").status_code == 401
        request = {"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}}
        headers = {"Host": "localhost:18082", "Accept": "application/json, text/event-stream"}
        assert client.post("/mcp", json=request, headers=headers).status_code == 401
        headers["Authorization"] = "Bearer " + token
        result = client.post("/mcp", json=request, headers=headers)
        assert result.status_code == 200
        names = {t["name"] for t in result.json()["result"]["tools"]}
        assert names == {"list_datasets", "list_series", "list_charts", "query_series", "compare_series", "convert_price", "resolve_fields", "confirm_fields", "execute_field_plan", "get_result_evidence", "analyze_table", "inspect_source", "list_pdf_tables", "read_pdf_table", "query_pdf_table"}
        headers["Host"] = "malicious.invalid"
        assert client.post("/mcp", json=request, headers=headers).status_code == 403


def test_public_origin_keeps_host_authentication_and_origin_checks(tmp_path, monkeypatch):
    origin = 'https://workbench.example:8443'
    host = 'workbench.example:8443'
    monkeypatch.setenv('STRUCTURED_PUBLIC_ORIGIN', origin)
    monkeypatch.setenv('STRUCTURED_TENANT_ID', '10000')

    class AuthClient:
        def __init__(self, **kwargs): pass
        async def __aenter__(self): return self
        async def __aexit__(self, *args): pass
        async def get(self, url, headers):
            return httpx.Response(200, json={'data': {'user': {'id':'user-a', 'tenant_id':10000},
                'memberships':[{'tenant_id':10000,'role':'owner'}]}})

    monkeypatch.setattr('httpx.AsyncClient', AuthClient)
    with TestClient(create_app(tmp_path)) as client:
        assert client.get('/health', headers={'Host': host}).status_code == 200
        assert client.get('/health', headers={'Host': 'workbench.example:9999'}).status_code == 403
        assert client.get('/health', headers={'Host': 'malicious.invalid'}).status_code == 403
        assert client.get('/api/datasets', headers={'Host': host}).status_code == 401
        headers = {'Host': host, 'Origin': origin, 'Authorization': 'Bearer valid'}
        assert client.get('/api/datasets', headers=headers).status_code == 200
        assert client.post('/api/not-found', json={}, headers=headers).status_code == 404
        assert client.post('/api/not-found', json={}, headers=headers | {'Origin': 'https://evil.invalid'}).status_code == 403
        assert client.post('/login', json={}, headers={'Host': host, 'Origin': origin}).status_code == 401
        assert client.post('/login', json={}, headers={'Host': host, 'Origin': 'https://evil.invalid'}).status_code == 403


def test_public_mcp_origin_still_requires_service_token(tmp_path, monkeypatch):
    monkeypatch.setenv('STRUCTURED_PUBLIC_ORIGIN', 'http://10.11.60.130:18090')
    with TestClient(create_app(tmp_path)) as client:
        request = {'jsonrpc': '2.0', 'id': 1, 'method': 'tools/list', 'params': {}}
        headers = {'Host': '10.11.60.130:18090', 'Origin': 'http://10.11.60.130:18090',
                   'Accept': 'application/json, text/event-stream'}
        assert client.post('/mcp', json=request, headers=headers).status_code == 401
        headers['Authorization'] = 'Bearer ' + (tmp_path / 'service-token').read_text()
        assert client.post('/mcp', json=request, headers=headers).status_code == 200
        assert client.post('/mcp', json=request, headers=headers | {'Origin': 'http://evil.invalid'}).status_code == 403


@pytest.mark.parametrize('origin', ['ftp://example.com', 'https://*', 'http://u:p@example.com',
                                  'https://example.com/path', 'https://example.com?x=1',
                                  'https://example.com/#x', 'https://example.com:invalid',
                                  'https://exa mple.com'])
def test_invalid_public_origins_refuse_startup(tmp_path, monkeypatch, origin):
    monkeypatch.setenv('STRUCTURED_PUBLIC_ORIGIN', origin)
    with pytest.raises(ValueError):
        create_app(tmp_path)


def test_model_call_does_not_block_health_requests(tmp_path,monkeypatch):
    import threading
    from concurrent.futures import ThreadPoolExecutor
    from structured_data.field_resolver import FieldResolver
    started=threading.Event();release=threading.Event()
    def slow_model(*args,**kwargs):
        started.set();release.wait(3)
        return {'status':'test_complete'}
    monkeypatch.setattr(FieldResolver,'tool_answer',slow_model)
    with TestClient(create_app(tmp_path)) as client,ThreadPoolExecutor(max_workers=2) as pool:
        headers={'Host':'localhost:18082','Accept':'application/json, text/event-stream','Authorization':'Bearer '+(tmp_path/'service-token').read_text()}
        pending=pool.submit(client.post,'/mcp',headers=headers,json={'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':'resolve_fields','arguments':{'question':'测试'}}})
        try:
            assert started.wait(1)
            health=pool.submit(client.get,'/health')
            assert health.result(timeout=1).status_code==200
            assert not pending.done()
        finally:release.set()
        assert pending.result(timeout=2).status_code==200


def test_mcp_analysis_and_persisted_evidence_without_dataset_guessing(snapshot):
    import json
    store, dataset_id, _ = snapshot
    with TestClient(create_app(store.root)) as client:
        headers = {'Host': 'localhost:18082', 'Accept': 'application/json, text/event-stream',
                   'Authorization': 'Bearer ' + (store.root / 'service-token').read_text()}

        def call(name, arguments):
            response = client.post('/mcp', headers=headers, json={
                'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call',
                'params': {'name': name, 'arguments': arguments}})
            assert response.status_code == 200
            result = response.json()['result']
            assert not result.get('isError'), result
            return result.get('structuredContent') or json.loads(result['content'][0]['text'])

        result = call('analyze_table', {'series_ids': ['price'],
            'sql': 'SELECT sum(value) AS total,min(date) AS used_first_date,max(date) AS used_last_date,count(value) AS used_count FROM data'})
        assert result['dataset_id'] == dataset_id
        assert result['rows'] == [[30, '2023-09-21', '2023-09-23', 2]]
        evidence = call('get_result_evidence', {'resolution_id': result['result_id']})
        assert evidence == result
        failure = call('analyze_table', {'series_ids': ['price'], 'sql': 'DELETE FROM data'})
        assert failure['status'] == 'error' and failure['retryable']
        unchanged = call('query_series', {'series_id': 'price', 'operation': 'sum'})
        assert unchanged['aggregate'] == 30
