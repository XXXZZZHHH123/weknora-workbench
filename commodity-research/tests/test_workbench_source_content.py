import hashlib

from fastapi import FastAPI
from fastapi.testclient import TestClient
import httpx
import pytest

from tin.config import settings
from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope, verified_workbench
from tin.web.workbench_sources import router


@pytest.fixture
def content(monkeypatch):
    raw = "# 方法资料\n这是原始观点，需核对。".encode()
    state = {"raw": raw, "downloads": 0, "source": {"id": "source-a", "knowledge_base_id": "library-a",
             "file_name": "method.md", "file_type": "md", "file_hash": hashlib.md5(raw).hexdigest()}}
    app = FastAPI()
    app.include_router(router)
    app.dependency_overrides[verified_workbench] = lambda: WorkbenchScope(
        Identity("first", "fixture@test", "Fixture", 7, "viewer"), "67175bd2-32c2-42df-8ba6-d8c2e2f1fe67",
        "library-a", 7, {}, "Bearer test")
    async def document(*args, **kwargs): return {"success": True, "data": state["source"]}
    monkeypatch.setattr("tin.web.workbench_sources.go_request", document)
    monkeypatch.setattr(settings, "weknora_api_url", "http://native.test")
    real = httpx.AsyncClient
    def upstream(request):
        state["downloads"] += 1
        assert request.url == "http://native.test/api/v1/knowledge/source-a/download"
        assert request.headers["authorization"] == "Bearer test"
        return httpx.Response(200, content=state["raw"])
    def client(*args, **kwargs): return real(*args, **{**kwargs, "transport": httpx.MockTransport(upstream)})
    monkeypatch.setattr(httpx, "AsyncClient", client)
    with TestClient(app) as result:
        yield result, state


URL = "/api/workbenches/67175bd2-32c2-42df-8ba6-d8c2e2f1fe67/sources/source-a/content"


def test_source_text_matches_real_original_and_never_executes_it(content):
    client, state = content
    response = client.get(URL)
    assert response.status_code == 200
    assert response.json()["text"] == state["raw"].decode()
    assert response.json()["sha256"] == hashlib.sha256(state["raw"]).hexdigest()
    assert response.json()["truncated"] is False


@pytest.mark.parametrize("changes,status", [
    ({"knowledge_base_id": "other"}, 403), ({"id": "other"}, 403),
    ({"file_size": 1024 * 1024 + 1}, 413),
])
def test_source_denied_before_downloading(content, changes, status):
    client, state = content
    state["source"].update(changes)
    assert client.get(URL).status_code == status
    assert state["downloads"] == 0


def test_changed_original_is_rejected(content):
    client, state = content
    state["raw"] += b"changed"
    assert client.get(URL).status_code == 409


def test_non_text_formats_do_not_claim_a_parsed_preview(content):
    client, state = content
    state["source"]["file_type"] = "zip"
    response = client.get(URL)
    assert response.json()["supported"] is False
    assert response.json()["text"] == ""
    assert state["downloads"] == 0


def test_raster_original_preview_does_not_require_vision_or_execute_markup(content):
    client, state = content
    state["source"]["file_type"] = "png"
    response = client.get(URL)
    assert response.status_code == 200
    result = response.json()
    assert result["image_data_url"].startswith("data:image/png;base64,")
    import base64
    assert base64.b64decode(result["image_data_url"].split(",", 1)[1]) == state["raw"]
    assert result["text"] == ""
    assert result["supported"] is True
