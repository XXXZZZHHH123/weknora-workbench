"""Actual immutable file storage and revision history; only external Go HTTP is mocked."""
import hashlib
import json
from pathlib import Path

from alembic import command
from alembic.config import Config
from fastapi import FastAPI, HTTPException, Request
from fastapi.testclient import TestClient
import httpx
import pytest
from sqlalchemy import select, update

from tin.config import ROOT, settings
from tin.db import make_engine
from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope, verified_workbench, workbench_session
from tin.models import SourceArtifact
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision
from tin.provenance.service import artifact_path
from tin.web.workbench_artifacts import router
from tin.web.workbenches import router as workbench_router

WB = "7a5adc48-f489-41a1-a880-556be375a993"
OTHER = "d56a2037-68dc-4a01-bbd9-607dc52d0d14"


def url(module="notes", workbench=WB):
    return f"/api/workbenches/{workbench}/{module}"


def entry(**changes):
    return {"title":"Research from a real original", "body":"A human-authored test conclusion",
            "source_ids":["source-a"], "status":"confirmed", "confirmed":True, **changes}


@pytest.fixture
def original_client(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, "data_dir", tmp_path)
    monkeypatch.setattr(settings, "weknora_api_url", "http://go.test")
    monkeypatch.setattr(settings, 'structured_data_url', 'http://structured-data:19082')
    service_key = tmp_path / 'service-key'; service_key.write_bytes(b'e' * 48)
    monkeypatch.setattr(settings, 'service_token_file', service_key)
    state = {"raw":b"first immutable original\x00\xff", "status":200, "moved":False,
             "deleted":False, "wrong_hash":False, "downloads":[], "scope":None, "metadata":{}, "stream":False}
    app = FastAPI()
    app.include_router(router)
    app.include_router(workbench_router)

    def scope(request: Request, workbench_id: str):
        actor = request.headers.get("x-actor", "first")
        value = WorkbenchScope(Identity(actor, "fixture@example.test", "Fixture", 7,
            request.headers.get("x-role", "owner")), workbench_id, "library-a", 7,
            {"id":workbench_id, "workspace_mode":"isolated", "can_edit": True}, "Bearer " + actor)
        state["scope"] = value
        return value

    def native_document(path):
        if path in {'/api/v1/workspaces/research/' + WB, '/api/v1/workspaces/research/' + OTHER}:
            return {"success":True, "data":{"id":path.rsplit("/", 1)[-1],
                    "scope_tenant_id":7, "knowledge_base_id":"library-a", "workspace_mode":"isolated"}}
        assert path.startswith('/api/v1/knowledge/')
        identifier = path.rsplit("/", 1)[-1]
        if identifier == "missing":
            raise HTTPException(404, "not found")
        doc = {"id":identifier, "knowledge_base_id":"library-b" if state["moved"] else "library-a",
               "type":"file", "title":"Actual title", "file_name":"original.bin", "file_type":"bin",
               "file_size":len(state["raw"]), "file_hash":hashlib.md5(state["raw"]).hexdigest(),
               "parse_status":"deleted" if state["deleted"] else "stored"}
        if identifier == "manual":
            doc.update(type="manual", title="Online source", file_hash="", file_size=0)
        doc.update(state["metadata"])
        if state["wrong_hash"]:
            doc["file_hash"] = "0" * 32
        return {"success":True, "data":doc}

    async def native(scope, path, **kwargs):
        return native_document(path)

    def transport(request):
        assert request.url.host == "go.test"
        assert request.url.path.startswith("/api/v1/knowledge/")
        assert request.url.path.endswith("/download")
        assert request.headers["x-tenant-id"] == "7"
        state["downloads"].append({"path":request.url.path, "authorization":request.headers["authorization"]})
        if state["stream"]:
            class Chunks(httpx.AsyncByteStream):
                async def __aiter__(self):
                    for offset in range(0,len(state["raw"]),3):
                        yield state["raw"][offset:offset+3]
            return httpx.Response(state["status"],stream=Chunks())
        return httpx.Response(state["status"], content=state["raw"])

    original_async = httpx.AsyncClient
    def client(**kwargs):
        assert kwargs["follow_redirects"] is False
        assert kwargs["trust_env"] is False
        return original_async(transport=httpx.MockTransport(transport), **kwargs)

    app.dependency_overrides[verified_workbench] = scope
    monkeypatch.setattr("tin.web.workbench_artifacts.go_request", native)
    monkeypatch.setattr("tin.web.workbench_artifact_evidence.go_request", native)
    monkeypatch.setattr(httpx, "AsyncClient", client)
    def fact_request(method, address, **kwargs):
        if address in {'http://go.test/api/v1/workspaces/research/' + WB,
                       'http://go.test/api/v1/workspaces/research/' + OTHER}:
            assert method == 'GET'
            assert kwargs['headers'] == {'Authorization': state['scope'].authorization, 'X-Tenant-ID': '7'}
            return httpx.Response(200, json=native_document(address.removeprefix('http://go.test')),
                                  request=httpx.Request(method, address))
        if address == 'http://go.test/api/v1/knowledge-bases/library-a/knowledge':
            return httpx.Response(200, json={'success': True, 'data': [], 'total': 0},
                                  request=httpx.Request(method, address))
        assert address == 'http://structured-data:19082/api/research/facts'
        if json.loads(kwargs['content']).get('metadata_only'):
            return httpx.Response(200,json={'knowledge_base_id':'library-a','knowledge_base_tenant_id':7,
                'version':'e'*64,'metadata_only':True},request=httpx.Request(method,address))
        return httpx.Response(200, json={'knowledge_base_id': 'library-a', 'knowledge_base_tenant_id': 7,
            'version': 'e' * 64, 'datasets': [], 'indicators': [], 'rows': [], 'total': 0,
            'offset': 0, 'next_offset': None, 'truncated': False}, request=httpx.Request(method, address))
    monkeypatch.setattr(httpx, 'request', fact_request)
    with TestClient(app) as result:
        yield result, state, tmp_path


def download_path(snapshot):
    return snapshot["download_url"].removeprefix("/research")


def test_real_bytes_are_pinned_and_later_native_replacement_does_not_rewrite_history(original_client):
    client, state, _ = original_client
    old = state["raw"]
    created = client.post(url(), json=entry())
    assert created.status_code == 201, created.text
    first = created.json(); snapshot = first["source_snapshots"][0]
    assert snapshot["capture_status"] == "original_frozen"
    assert snapshot["sha256"] == snapshot["artifact_id"] == hashlib.sha256(old).hexdigest()
    assert snapshot["file_hash"] == hashlib.md5(old).hexdigest()
    assert snapshot["title"] == "Actual title"
    assert "Bearer" not in str(first)
    with workbench_session(state["scope"]) as session:
        stored = session.get(SourceArtifact, snapshot["artifact_id"])
        assert artifact_path(session, stored).read_bytes() == old
    assert client.get(download_path(snapshot)).content == old

    state["raw"] = b"second and different original"
    changed = client.patch(url()+"/"+first["id"], json=entry(body="Reconsidered human conclusion",revision=1))
    assert changed.status_code == 200, changed.text
    second = changed.json(); frozen = second["source_snapshots"][0]
    assert frozen["sha256"] == hashlib.sha256(state["raw"]).hexdigest()
    assert client.get(download_path(snapshot)).content == old
    assert client.get(download_path(frozen)).content == state["raw"]
    history = client.get(url()+"/"+first["id"]+"/revisions").json()
    assert history["total"] == 2
    assert [item["revision"] for item in history["items"]] == [2,1]
    assert history["items"][1]["body"] == entry()["body"]
    assert history["items"][1]["source_snapshots"] == [snapshot]
    assert history["items"][0]["body"] == "Reconsidered human conclusion"


def test_history_and_originals_are_private_but_structure_is_shared(original_client):
    client, state, _ = original_client
    private = client.post(url(), json=entry()).json()
    history = url()+"/"+private["id"]+"/revisions"
    assert client.get(history, headers={"x-actor":"second"}).status_code == 404
    assert client.get(history.replace(WB, OTHER)).status_code == 404
    snapshot = private["source_snapshots"][0]
    assert client.get(download_path(snapshot), headers={"x-actor":"second"}).status_code == 403
    assert client.get(download_path(snapshot).replace(WB, OTHER)).status_code == 404
    shared = client.post(url("structure"), json=entry()).json()
    assert client.get(url("structure")+"/"+shared["id"]+"/revisions", headers={"x-actor":"second"}).status_code == 200
    assert client.get(download_path(snapshot), headers={"x-actor":"second"}).status_code == 200


@pytest.mark.parametrize("field,value",[("status",403),("status",404),("moved",True),("deleted",True)])
def test_frozen_download_rechecks_current_native_original_permissions(original_client,field,value):
    client, state, _ = original_client
    original = client.post(url(), json=entry()).json()
    state[field] = value
    assert client.get(download_path(original["source_snapshots"][0])).status_code == 403
    assert client.get(url()+"/"+original["id"]+"/revisions").status_code == 200


@pytest.mark.parametrize("source_type",["manual","url"])
def test_manual_sources_are_honestly_metadata_only_and_do_not_fetch_external_websites(original_client,source_type):
    client, state, _ = original_client
    state["metadata"]={"type":source_type,"source":"https://untrusted.example.test/instructions"}
    response = client.post(url(), json=entry(source_ids=["manual"]))
    assert response.status_code == 201, response.text
    snapshot = response.json()["source_snapshots"][0]
    assert snapshot["capture_status"] == "metadata_only"
    assert snapshot["title"] == "Online source"
    assert "artifact_id" not in snapshot and "sha256" not in snapshot and "download_url" not in snapshot
    assert state["downloads"] == []


@pytest.mark.parametrize("change,value,status",[("wrong_hash",True,409),("status",403,403),("status",302,503)])
def test_invalid_original_never_publishes_record_or_history(original_client,change,value,status):
    client, state, _ = original_client
    state[change] = value
    assert client.post(url(), json=entry()).status_code == status
    assert client.get(url()).json()["total"] == 0
    with workbench_session(state["scope"]) as session:
        assert session.scalars(select(WorkbenchArtifactRevision)).all() == []
        assert session.scalars(select(SourceArtifact)).all() == []


def test_per_file_and_whole_request_limits_are_checked_before_download(original_client,monkeypatch):
    client, state, _ = original_client
    import tin.web.workbench_artifact_evidence as evidence
    monkeypatch.setattr(evidence,"MAX_SOURCE_BYTES",10)
    assert client.post(url(), json=entry()).status_code == 413
    assert state["downloads"] == []
    monkeypatch.setattr(evidence,"MAX_SOURCE_BYTES",100)
    monkeypatch.setattr(evidence,"MAX_REQUEST_SOURCE_BYTES",len(state["raw"])*2-1)
    assert client.post(url(), json=entry(source_ids=["source-a","source-b"])).status_code == 413
    assert state["downloads"] == []
    assert client.get(url()).json()["total"] == 0


@pytest.mark.parametrize("metadata",[{"file_hash":""},{"file_hash":"x"*32},{"file_size":-1},{"file_size":True}])
def test_missing_or_invalid_native_version_metadata_is_not_called_frozen(original_client,metadata):
    client,state,_=original_client
    state["metadata"]=metadata
    assert client.post(url(),json=entry()).status_code==409
    assert state["downloads"]==[]
    assert client.get(url()).json()["total"]==0


def test_streaming_without_content_length_rejects_bytes_beyond_registered_size(original_client):
    client,state,_=original_client
    state["stream"]=True
    state["metadata"]={"file_size":len(state["raw"])-1}
    assert client.post(url(),json=entry()).status_code==409
    assert len(state["downloads"])==1
    assert client.get(url()).json()["total"]==0


def test_even_visible_frozen_sources_need_a_native_original_permission_binding(original_client):
    client,state,_=original_client
    created=client.post(url("structure"),json=entry()).json()
    snapshot=created["source_snapshots"][0]
    with workbench_session(state["scope"]) as session:
        stored=session.get(SourceArtifact,snapshot["artifact_id"])
        stored.details={}
        session.commit()
    assert client.get(download_path(snapshot)).status_code==403


def test_stale_or_foreign_edit_does_not_download_or_overwrite_history(original_client):
    client, state, _ = original_client
    initial = client.post(url(), json=entry()).json()
    target = url()+"/"+initial["id"]
    assert client.patch(target,json=entry(revision=1,body="New text")).status_code == 200
    before = len(state["downloads"])
    assert client.patch(target,json=entry(revision=1,body="Stale overwrite")).status_code == 409
    assert client.patch(target,json=entry(revision=2),headers={"x-actor":"other"}).status_code == 404
    assert len(state["downloads"]) == before
    assert client.get(target+"/revisions").json()["total"] == 2
    assert client.get(url()).json()["items"][0]["body"] == "New text"


def test_revision_cas_still_rejects_edit_that_races_after_original_capture(original_client,monkeypatch):
    client, state, _ = original_client
    initial = client.post(url(),json=entry()).json()
    import tin.web.workbench_artifacts as artifacts
    actual = artifacts.capture_sources

    async def racing_capture(scope,sources):
        captured = await actual(scope,sources)
        with workbench_session(scope) as session:
            current = session.get(WorkbenchArtifact,initial["id"])
            current.body, current.revision = "Concurrent winner", 2
            artifacts.record_revision(session,current,"first")
            session.commit()
        return captured
    monkeypatch.setattr(artifacts,"capture_sources",racing_capture)
    response = client.patch(url()+"/"+initial["id"],json=entry(revision=1,body="Lost race"))
    assert response.status_code == 409,response.text
    current = client.get(url()).json()["items"][0]
    assert current["body"] == "Concurrent winner" and current["revision"] == 2
    history = client.get(url()+"/"+initial["id"]+"/revisions").json()
    assert history["total"] == 2
    assert all(item["body"] != "Lost race" for item in history["items"])


def test_upgrade_preserves_real_legacy_current_version_without_fabricating_evidence(tmp_path):
    path = tmp_path / "legacy-research.db"
    config = Config(); config.set_main_option("script_location",str(ROOT/"alembic"))
    config.set_main_option("sqlalchemy.url",f"sqlite:///{path}")
    command.upgrade(config,"9f17b80c4a32")
    engine = make_engine(f"sqlite:///{path}")
    with engine.begin() as connection:
        connection.exec_driver_sql("INSERT INTO workbench_artifacts "
            "(id,module,actor,title,body,status,source_ids,revision,created_at,updated_at) VALUES "
            "('old','notes','first','Old title','Only surviving old text','confirmed','[\"old-source\"]',3,"
            "'2026-01-01 00:00:00','2026-01-03 00:00:00')")
    command.upgrade(config,"a618c3e72f45")
    with engine.connect() as connection:
        row = connection.exec_driver_sql("SELECT body,revision,source_snapshots FROM workbench_artifacts").first()
        assert row[:2] == ("Only surviving old text",3)
        import json
        snapshot = json.loads(row[2])[0]
        assert snapshot["capture_status"] == "unfrozen" and "sha256" not in snapshot
        history = connection.exec_driver_sql("SELECT body,revision,edited_by FROM workbench_artifact_revisions").all()
        assert history == [("Only surviving old text",3,"")]
    command.downgrade(config,"9f17b80c4a32")
    with engine.connect() as connection:
        assert connection.exec_driver_sql("SELECT body,revision FROM workbench_artifacts").first() == ("Only surviving old text",3)
    command.upgrade(config,"a618c3e72f45")
    engine.dispose()
