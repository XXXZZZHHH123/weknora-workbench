"""Real isolated stores, human publication and native-source authorization."""
from dataclasses import replace
import json

from fastapi import FastAPI, HTTPException, Request
from fastapi.testclient import TestClient
import httpx
import pytest

from tin.config import settings
from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope, verified_workbench
from tin.web.workbench_artifacts import router

FIRST = "67175bd2-32c2-42df-8ba6-d8c2e2f1fe67"
SECOND = "0b3eb224-d10c-49f4-bca1-295a5349e7c2"


@pytest.fixture
def client(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, "data_dir", tmp_path)
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://native.test')
    monkeypatch.setattr(settings, 'structured_data_url', 'http://structured-data:19082')
    service_key = tmp_path / 'service-key'; service_key.write_bytes(b'a' * 48)
    monkeypatch.setattr(settings, 'service_token_file', service_key)
    app = FastAPI()
    app.include_router(router)

    def scope(request: Request, workbench_id: str):
        identity = Identity(request.headers.get("x-actor", "first"), "fixture@example.test", "Fixture", 7,
                            request.headers.get("x-role", "owner"))
        return WorkbenchScope(identity, workbench_id, "library-a", 7, {"id": workbench_id}, "Bearer fixture")

    async def native(scope, path, **kwargs):
        identifier = path.rsplit("/", 1)[-1]
        if identifier == "missing":
            raise HTTPException(404, "not found")
        return {"success": True, "data": {"id": identifier,
                "knowledge_base_id": "library-b" if identifier == "foreign" else "library-a"}}

    app.dependency_overrides[verified_workbench] = scope
    monkeypatch.setattr("tin.web.workbench_artifacts.go_request", native)
    def fact_request(method, address, **kwargs):
        if address == 'http://native.test/api/v1/knowledge-bases/library-a/knowledge':
            return httpx.Response(200, json={'success': True, 'data': [], 'total': 0},
                                  request=httpx.Request(method, address))
        assert address == 'http://structured-data:19082/api/research/facts'
        if json.loads(kwargs['content']).get('metadata_only'):
            return httpx.Response(200,json={'knowledge_base_id':'library-a','knowledge_base_tenant_id':7,
                'version':'a'*64,'metadata_only':True},request=httpx.Request(method,address))
        return httpx.Response(200, json={'knowledge_base_id': 'library-a', 'knowledge_base_tenant_id': 7,
            'version': 'a' * 64, 'datasets': [], 'indicators': [], 'rows': [], 'total': 0,
            'offset': 0, 'next_offset': None, 'truncated': False}, request=httpx.Request(method, address))
    monkeypatch.setattr(httpx, 'request', fact_request)
    with TestClient(app) as result:
        yield result


def entry(**changes):
    return {"title": "A source-linked note", "body": "Human research content", "source_ids": ["source-a"], **changes}


def path(module="notes", workbench=FIRST):
    return f"/api/workbenches/{workbench}/{module}"


def test_empty_start_and_tenant_workbench_actor_boundaries(client):
    assert client.get(path()).json()["total"] == 0
    created = client.post(path(), json=entry())
    assert created.status_code == 201, created.text
    assert created.json()["revision"] == 1
    assert client.get(path()).json()["total"] == 1
    assert client.get(path(), headers={"x-actor": "second"}).json()["total"] == 0
    assert client.get(path(workbench=SECOND)).json()["total"] == 0
    assert client.get(path("judgments")).json()["total"] == 0


def test_structure_shared_only_within_actual_workbench(client):
    assert client.post(path("structure"), json=entry()).status_code == 201
    assert client.get(path("structure"), headers={"x-actor": "second"}).json()["total"] == 1
    assert client.get(path("structure", SECOND)).json()["total"] == 0


@pytest.mark.parametrize("changes,status", [
    ({"source_ids": ["foreign"]}, 403), ({"source_ids": ["missing"]}, 404),
    ({"source_ids": ["../source"]}, 400), ({"status": "confirmed"}, 400),
    ({"status": "confirmed", "confirmed": True, "source_ids": []}, 400),
    ({"title": "  "}, 400), ({"body": "  "}, 400), ({"status": "executed"}, 422),
])
def test_invalid_research_is_not_published(client, changes, status):
    response = client.post(path(), json=entry(**changes))
    assert response.status_code == status, response.text
    assert client.get(path()).json()["total"] == 0


def test_explicit_confirmation_and_optimistic_revision(client):
    created = client.post(path(), json=entry(status="confirmed", confirmed=True)).json()
    assert created["status"] == "confirmed"
    target = path() + "/" + created["id"]
    assert client.patch(target, json=entry()).status_code == 400
    changed = client.patch(target, json=entry(title="New version", revision=1))
    assert changed.status_code == 200, changed.text
    assert changed.json()["revision"] == 2
    assert client.patch(target, json=entry(revision=1)).status_code == 409
    assert client.patch(target, json=entry(revision=2), headers={"x-actor": "second"}).status_code == 404
    assert client.patch(target.replace(FIRST, SECOND), json=entry(revision=2)).status_code == 404
    assert client.get(path()).json()["items"][0]["title"] == "New version"


def test_viewer_cannot_create_or_edit(client):
    assert client.post(path(), json=entry(), headers={"x-role": "viewer"}).status_code == 403
    assert client.get(path()).json()["total"] == 0
