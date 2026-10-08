from fastapi.testclient import TestClient

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
