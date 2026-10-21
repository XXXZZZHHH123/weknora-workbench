"""Native originals enter the existing local importer through real scope checks."""
import asyncio
import hashlib
from io import BytesIO
import threading
import time
from types import SimpleNamespace

from fastapi.testclient import TestClient
from fastapi import HTTPException
import httpx
from openpyxl import Workbook
import pytest

from structured_data.server import create_app
from structured_data.native_import import load_source, native_limit


def workbook():
    stream = BytesIO()
    book = Workbook()
    sheet = book.active
    sheet.title = "records"
    sheet.append(["date", "measure"])
    sheet.append(["2024-03-01", 7])
    sheet.append(["2024-03-02", 11])
    book.save(stream)
    return stream.getvalue()


def headers(kb="kb-a", role="owner"):
    return {"Authorization": "Bearer " + role, "X-Tenant-ID": "10000",
            "X-Knowledge-Base-ID": kb, "Origin": "http://testserver"}


@pytest.fixture
def native(tmp_path, monkeypatch):
    contents = workbook()
    document = dict(id="source-a", tenant_id=10000, knowledge_base_id="kb-a", type="file",
                    file_name="source.xlsx", file_type="xlsx", file_size=len(contents),
                    file_hash=hashlib.md5(contents).hexdigest(), parse_status="stored")
    state = {"document": document, "contents": contents, "download_status": 200, "calls": []}
    real_client = httpx.AsyncClient

    def upstream(request):
        state["calls"].append((request.url.path, dict(request.headers)))
        assert request.url.host == "native.test"
        if request.url.path == "/api/v1/auth/login":
            return httpx.Response(200, json={"data": {"token": "owner"}})
        tenant = int(request.headers.get("x-tenant-id", "10000"))
        role = request.headers["authorization"].removeprefix("Bearer ")
        if request.url.path == "/api/v1/auth/me":
            return httpx.Response(200, json={"data": {"user": {"id": "user-a", "tenant_id": tenant},
                                   "memberships": [{"tenant_id": tenant, "role": role}]}})
        if request.url.path.startswith("/api/v1/knowledge-bases/"):
            identifier = request.url.path.rsplit("/", 1)[-1]
            return httpx.Response(200, json={"success": True, "data": {
                "id": identifier, "tenant_id": 10000, "creator_id": "user-a",
                "my_permission": state.get('permission', 'admin' if role in {'owner', 'admin'} else 'editor')}})
        if request.url.path == "/api/v1/knowledge/source-a":
            if "metadata_payload" in state:
                return httpx.Response(200, json=state["metadata_payload"])
            state['metadata_reads'] = state.get('metadata_reads', 0) + 1
            if state.get('changed_after_download') and state['metadata_reads'] > 1:
                return httpx.Response(200, json={'data': state['document'] | {'file_hash': 'b' * 32}})
            return httpx.Response(200, json={"success": True, "data": state["document"]})
        if request.url.path == "/api/v1/knowledge/source-a/download":
            return httpx.Response(state["download_status"], content=state["contents"])
        return httpx.Response(404, json={})

    def client_factory(*args, **kwargs):
        kwargs["transport"] = httpx.MockTransport(upstream)
        return real_client(*args, **kwargs)

    monkeypatch.setattr(httpx, "AsyncClient", client_factory)
    monkeypatch.setenv("WEKNORA_API_URL", "http://native.test")
    monkeypatch.setenv("STRUCTURED_TENANT_ID", "10000")
    monkeypatch.setenv("STRUCTURED_MODEL_CONFIG", str(tmp_path / "absent-model.json"))
    root = tmp_path / "data"
    app = create_app(root)
    with TestClient(app) as client:
        yield client, app, state, root


def finish(client, job):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        current = client.get("/api/jobs/" + job, headers=headers()).json()
        if current["status"] != "processing":
            assert current["status"] == "completed", current
            return current
        time.sleep(.01)
    raise AssertionError("native original import did not finish")


@pytest.mark.parametrize('role', ['owner', 'admin', 'contributor'])
def test_resource_view_permission_does_not_inherit_space_write_or_old_creator(native, role):
    client, _, state, _ = native
    state['permission'] = 'viewer'
    response = client.post('/api/import-knowledge', json={'source_knowledge_id': 'source-a'}, headers=headers(role=role))
    assert response.status_code == 403, response.text
    assert not any(path.endswith('/download') for path, _ in state['calls'])


def begin(client, **kwargs):
    return client.post("/api/import-knowledge", json={"source_knowledge_id": "source-a"},
                       headers=kwargs.pop("headers", headers()), **kwargs)


def test_fresh_workbench_import_never_consults_legacy_review(native, monkeypatch):
    from structured_data.imports import ImportManager
    import structured_data.server as server
    client, _, _, _ = native
    def forbidden(*args, **kwargs):
        raise AssertionError("A fresh workbench must not consult an existing workspace review")
    monkeypatch.setattr("structured_data.legacy_import.candidate", forbidden)
    monkeypatch.setattr("structured_data.profiles.reviewed.reviewed_mapping", forbidden)
    original_task = server.run_review_task
    def direct_task(root, action, **kwargs):
        if action != "import":
            return original_task(root, action, **kwargs)
        manager = ImportManager(root)
        if kwargs.get("use_reviewed_mappings", True):
            return manager.ingest(kwargs["path"])
        return manager.ingest(kwargs["path"], use_reviewed_mappings=False)
    monkeypatch.setattr(server, "run_review_task", direct_task)
    response = client.post("/api/import-knowledge", json={"source_knowledge_id": "source-a",
        "restore_reviewed_state": False}, headers=headers())
    assert response.status_code == 200, response.text
    result = finish(client, response.json()["id"])["result"]
    assert result.get("recovered_legacy") is not True
    assert result["source_knowledge_id"] == "source-a"
    review = client.get("/api/imports/" + result["import_id"], headers=headers()).json()
    assert review["use_reviewed_mappings"] is False
    repeated = client.post("/api/import-knowledge", json={"source_knowledge_id": "source-a",
        "restore_reviewed_state": False}, headers=headers()).json()
    assert repeated["status"] == "completed" and repeated["result"]["import_id"] == result["import_id"]


def test_native_original_import_preserves_real_source_and_reuses_after_restart(native):
    client, _, state, root = native
    started = begin(client)
    assert started.status_code == 200, started.text
    assert started.json()["source_knowledge_id"] == "source-a"
    job = finish(client, started.json()["id"])
    result = job["result"]
    assert result["source_knowledge_id"] == job["source_knowledge_id"] == "source-a"
    review = client.get("/api/imports/" + result["import_id"], headers=headers()).json()
    assert review["native_source_bindings"][0]["file_hash"] == state["document"]["file_hash"]
    series = client.get("/api/datasets/" + result["dataset_id"] + "/series", headers=headers()).json()["series"]
    rows = client.get("/api/query", params={"dataset_id": result["dataset_id"],
                       "series_id": series[0]["series_id"]}, headers=headers()).json()["rows"]
    assert [(r["value"], r["source_cell"]) for r in rows] == [(7, "B2"), (11, "B3")]
    source = client.get("/api/datasets/" + result["dataset_id"] + "/source", headers=headers())
    assert source.content == state["contents"]
    downloads = [path for path, _ in state["calls"] if path.endswith("/download")]
    with TestClient(create_app(root)) as restarted:
        reused = begin(restarted)
        assert reused.status_code == 200, reused.text
        assert reused.json()["reused"] is True
        assert reused.json()["result"]["import_id"] == result["import_id"]
    assert [path for path, _ in state["calls"] if path.endswith("/download")] == downloads


@pytest.mark.parametrize("patch,status", [
    ({"knowledge_base_id": "kb-b"}, 403), ({"tenant_id": 10001}, 403), ({"id": "other"}, 403),
    ({"file_type": "xls", "file_name": "source.xls"}, 400), ({"type": "url"}, 400),
    ({"file_name": "../source.xlsx"}, 400), ({"file_size": 101 * 1024 * 1024}, 413),
    ({"file_hash": "invalid"}, 400), ({"parse_status": "deleting"}, 409),
])
def test_source_metadata_refuses_cross_scope_and_invalid_files_before_download(native, patch, status):
    client, _, state, root = native
    state["document"].update(patch)
    response = begin(client)
    assert response.status_code == status, response.text
    assert not any(path.endswith("/download") for path, _ in state["calls"])
    assert not list(root.glob("**/review.json"))


@pytest.mark.parametrize("role,origin", [("viewer", "http://testserver"), ("owner", "http://untrusted.test")])
def test_read_only_or_wrong_origin_cannot_fetch_an_original(native, role, origin):
    client, _, state, _ = native
    auth = headers(role=role) | {"Origin": origin}
    assert begin(client, headers=auth).status_code == 403
    assert not any(path.startswith("/api/v1/knowledge/source-a") for path, _ in state["calls"])


def test_corrupt_download_keeps_raw_source_unbound_and_does_not_publish(native):
    client, _, state, root = native
    state["contents"] = state["contents"][:-1] + bytes([state["contents"][-1] ^ 1])
    response = begin(client)
    assert response.status_code == 409, response.text
    assert not list(root.glob("**/review.json"))
    assert not list(root.glob("**/manifest.json"))
    assert not list(root.glob("**/uploads/*"))


def test_repeated_source_while_scanning_reuses_its_inflight_job(native, monkeypatch):
    client, _, state, _ = native
    import structured_data.server as server
    original = server.run_review_task
    released = threading.Event()

    def paused(*args, **kwargs):
        assert released.wait(10)
        return original(*args, **kwargs)

    monkeypatch.setattr(server, "run_review_task", paused)
    try:
        first = begin(client)
        assert first.status_code == 200, first.text
        second = begin(client)
        assert second.status_code == 200, second.text
        assert second.json()["id"] == first.json()["id"]
        assert sum(path.endswith("/download") for path, _ in state["calls"]) == 1
    finally:
        released.set()
    finish(client, first.json()["id"])


def test_identifier_is_data_not_an_arbitrary_download_url(native):
    client, _, state, _ = native
    response = client.post("/api/import-knowledge", headers=headers(),
                           json={"source_knowledge_id": "http://untrusted.test/file.xlsx", "url": "http://untrusted.test"})
    assert response.status_code == 422
    assert not any(path.startswith("/api/v1/knowledge/source-a") for path, _ in state["calls"])


@pytest.mark.parametrize("payload", [[], None, "invalid"])
def test_malformed_native_metadata_is_a_safe_upstream_error(native, payload):
    client, _, state, _ = native
    state["metadata_payload"] = payload
    response = begin(client)
    assert response.status_code == 502, response.text
    assert not any(path.endswith("/download") for path, _ in state["calls"])


def test_existing_cookie_login_is_forwarded_as_its_verified_native_authorization(native):
    client, _, state, _ = native
    logged_in = client.post("/login", headers={"Origin": "http://testserver"},
                            json={"email": "isolated@example.test", "password": "fixture-password", "tenant_id": 10000})
    assert logged_in.status_code == 200
    cookie_headers = {key: value for key, value in headers().items() if key != "Authorization"}
    started = begin(client, headers=cookie_headers)
    assert started.status_code == 200, started.text
    finish(client, started.json()["id"])
    native_requests = [request for path, request in state["calls"] if path.endswith("/download")]
    assert native_requests[0]["authorization"] == "Bearer owner"


def test_source_replacement_during_download_cannot_bind_stale_bytes(native):
    client, _, state, root = native
    state['changed_after_download'] = True
    response = begin(client)
    assert response.status_code == 409, response.text
    assert not list(root.glob('**/review.json'))
    assert not list(root.glob('**/manifest.json'))


@pytest.mark.parametrize('status', [403, 404, 302, 500])
def test_download_denials_and_redirects_do_not_publish_or_leave_the_import_locked(native, status):
    client, _, state, root = native
    state['download_status'] = status
    response = begin(client)
    assert response.status_code == (status if status in {403, 404} else 502), response.text
    assert not list(root.glob('**/review.json'))
    state['download_status'] = 200
    retry = begin(client)
    assert retry.status_code == 200, retry.text
    finish(client, retry.json()['id'])


def test_a_corrupted_bound_original_is_rejected_instead_of_reused(native):
    client, _, state, root = native
    started = begin(client)
    completed = finish(client, started.json()['id'])
    original = root / 'knowledge-bases' / 'kb-a' / 'imports' / completed['result']['import_id'] / 'source.xlsx'
    content = original.read_bytes()
    original.write_bytes(content[:-1] + bytes([content[-1] ^ 1]))
    downloads = sum(path.endswith('/download') for path, _ in state['calls'])
    response = begin(client)
    assert response.status_code == 400, response.text
    assert '校验' in response.json()['detail']
    assert sum(path.endswith('/download') for path, _ in state['calls']) == downloads


@pytest.mark.parametrize('native_mb,expected_mb', [('100', 100), ('50', 50), ('200', 100), ('0', 50), ('invalid', 50)])
def test_native_limit_preserves_both_service_and_upstream_limits(monkeypatch, native_mb, expected_mb):
    monkeypatch.setenv('MAX_FILE_SIZE_MB', native_mb)
    assert native_limit(100 * 1024 * 1024) == expected_mb * 1024 * 1024


def test_deployment_sized_native_original_is_accepted_without_relaxing_the_limit(native, monkeypatch):
    _, _, state, _ = native
    monkeypatch.setenv('MAX_FILE_SIZE_MB', '100')
    identity = SimpleNamespace(tenant_id=10000, knowledge_base_tenant_id=10000, knowledge_base_id='kb-a')
    state['document']['file_size'] = 75 * 1024 * 1024
    source = asyncio.run(load_source('source-a', 'Bearer owner', identity, 100 * 1024 * 1024))
    assert source['file_size'] == 75 * 1024 * 1024
    monkeypatch.setenv('MAX_FILE_SIZE_MB', '50')
    with pytest.raises(HTTPException) as rejected:
        asyncio.run(load_source('source-a', 'Bearer owner', identity, 100 * 1024 * 1024))
    assert rejected.value.status_code == 413
    assert not any(path.endswith('/download') for path, _ in state['calls'])


def test_native_open_recovers_same_tenant_legacy_review_without_rescanning(native, monkeypatch):
    from structured_data.imports import ImportManager
    client, app, state, root = native
    legacy = ImportManager(app.state.workspaces.directory(10000))
    original = root / 'fixture-original.xlsx'
    original.write_bytes(state['contents'])
    seed = legacy.ingest(original)
    mapping = legacy.sheet(seed['import_id'], 'records')['mapping']
    saved = legacy.confirm(seed['import_id'], mapping, True)
    before = (legacy.directory / saved['import_id'] / 'review.json').read_bytes()
    # Reproduce the regression: a new KB-local automatic scan already exists.
    target = ImportManager(root / 'knowledge-bases' / 'kb-a')
    automatic = target.ingest(original)
    native_source = {key: state['document'][key] for key in
                     ('file_name', 'file_size', 'file_hash', 'knowledge_base_id', 'tenant_id')}
    native_source['source_knowledge_id'] = 'source-a'
    target.bind_native_source(automatic['import_id'], native_source)

    def never_scan(*args, **kwargs):
        raise AssertionError('An unchanged reviewed original must not be rescanned')
    monkeypatch.setattr('structured_data.server.run_review_task', never_scan)
    started = begin(client)
    assert started.status_code == 200, started.text
    job = finish(client, started.json()['id'])
    assert job['result']['recovered_legacy'] is True
    assert job['result']['dataset_id'] == saved['dataset_id']
    restored = client.get('/api/imports/' + saved['import_id'], headers=headers()).json()
    assert restored['sheets'][0]['status'] == 'confirmed'
    assert restored['sheets'][0]['mapping'] == legacy.sheet(saved['import_id'], 'records')['mapping']
    assert (legacy.directory / saved['import_id'] / 'review.json').read_bytes() == before
    assert client.get('/api/imports/' + saved['import_id'], headers=headers('kb-b')).status_code in {400, 404}
    downloads = sum(path.endswith('/download') for path, _ in state['calls'])
    reopened = begin(client).json()
    assert reopened['status'] == 'completed'
    assert reopened['result']['dataset_id'] == saved['dataset_id']
    assert sum(path.endswith('/download') for path, _ in state['calls']) == downloads
