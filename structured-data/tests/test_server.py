from fastapi.testclient import TestClient
import httpx
import pytest

from structured_data.server import create_app


def test_authentication_and_mcp_read_only_surface(tmp_path):
    app = create_app(tmp_path)
    token = (tmp_path / "service-token").read_text()
    with TestClient(app) as client:
        assert client.get("/health").status_code == 200
        assert "结构化数据工作台" in client.get("/").text
        assert "棉花结构化数据" not in client.get("/").text
        assert client.get("/api/datasets").status_code == 401
        request = {"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}}
        headers = {"Host": "localhost:18082", "Accept": "application/json, text/event-stream"}
        assert client.post("/mcp", json=request, headers=headers).status_code == 401
        headers["Authorization"] = "Bearer " + token
        result = client.post("/mcp", json=request, headers=headers)
        assert result.status_code == 200
        names = {t["name"] for t in result.json()["result"]["tools"]}
        assert names == {"list_datasets", "list_series", "list_charts", "query_series", "compare_series", "convert_price", "resolve_fields", "confirm_fields", "execute_field_plan"}
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
            return httpx.Response(200, json={'data': {'user': {'tenant_id': 10000}}})

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
