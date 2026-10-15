"""New research workbenches must never fall back to a tenant's legacy facts."""
import base64
import hashlib
import hmac
import io
import json
import time

import httpx
import pytest
from fastapi.testclient import TestClient
from openpyxl import Workbook

from tin.config import settings
from tin.web.app import app


WB = "10000000-0000-4000-8000-000000000001"
OTHER = "10000000-0000-4000-8000-000000000002"
LEGACY = "10000000-0000-4000-8000-000000000003"
KB = "20000000-0000-4000-8000-000000000001"
DATASET = "abcdef0123456789abcdef01"


@pytest.fixture
def isolated(monkeypatch, tmp_path):
    monkeypatch.setattr(settings, "weknora_api_url", "http://weknora.test")
    monkeypatch.setattr(settings, "structured_data_url", "http://structured-data:19082")
    monkeypatch.setattr(settings, "data_dir", tmp_path)
    key = tmp_path / "service-key"
    key.write_bytes(b"a" * 48)
    monkeypatch.setattr(settings, "service_token_file", key)
    wb = Workbook(); ws = wb.active; ws.title = "Measurements"
    ws.append(["date", "quantity"]); ws.append(["2026-01-01", 7.5]); ws.append(["2026-01-02", "#N/A"])
    stream = io.BytesIO(); wb.save(stream); blob = stream.getvalue()
    sha = hashlib.sha256(blob).hexdigest()
    manifest = {"dataset_id": DATASET, "sha256": sha, "source_file": "measurements.xlsx",
                "use_reviewed_mappings": False, "import_id": "fresh-import"}
    series = {"series_id": "field-1", "metric": "Quantity", "sheet": "Measurements", "unit": "吨",
              "frequency": "日", "date_mode": "date", "header_cells": ["B1"]}
    state = {"calls": [], "revoked_kb": False, "go_bad_id": False, "go_tenant": 17,
             "wrong_series": False, "future": False, "signed": [], "structured_down": False,
             "knowledge_base_id": KB, "imports": [], "datasets": None, "file_hash_override": None,
             "legacy_snapshot": False, "parse_status": "stored", "canonical_indicators": [], "canonical_rows": []}
    state['imports'] = [{'import_id': 'fresh-import', 'dataset_id': DATASET, 'use_reviewed_mappings': False,
        'native_source_bindings': [{'source_knowledge_id': 'source-1', 'file_hash': hashlib.md5(blob).hexdigest(),
                                  'knowledge_base_id': KB, 'tenant_id': 23}],
        'summary': {'needs_review': 1}, 'updated_at': '2026-01-01'}]

    def transport(request):
        state["calls"].append((request.method, request.url.path))
        if request.url.host == "weknora.test":
            token = request.headers.get("authorization", "").removeprefix("Bearer ")
            if token not in {"writer", "viewer"}:
                return httpx.Response(401, json={"success": False})
            if request.url.path == "/api/v1/auth/me":
                role = "viewer" if token == "viewer" else "contributor"
                return httpx.Response(200, json={"success": True, "data": {
                    "user": {"id": token, "email": token + "@test", "username": token},
                    "tenant": {"id": 17}, "memberships": [{"tenant_id": 17, "role": role}]}})
            if request.url.path.startswith("/api/v1/workspaces/research/"):
                target = request.url.path.rsplit("/", 1)[1]
                if target not in {WB, OTHER, LEGACY}:
                    return httpx.Response(404, json={"success": False})
                return httpx.Response(200, json={"success": True, "data": {
                    "id": OTHER if state["go_bad_id"] else target, "name": "New workbench",
                    "scope_tenant_id": state["go_tenant"], "knowledge_base_id": state["knowledge_base_id"],
                    "workspace_mode": "legacy" if target == LEGACY else "isolated",
                    "template_id": "data-tracking", "template_version": 1}})
            if request.url.path == f"/api/v1/knowledge-bases/{KB}":
                return httpx.Response(403 if state["revoked_kb"] else 200, json={"success": True,
                    "data": {"id": KB, "tenant_id": 23, "my_permission": "viewer"}})
            if request.url.path == f"/api/v1/knowledge-bases/{KB}/knowledge":
                return httpx.Response(200, json={"success": True, "data": [{"id": "source-1",
                    "knowledge_base_id": KB, "file_name": "measurements.xlsx", "file_type": "xlsx",
                    "file_hash": state["file_hash_override"] or hashlib.md5(blob).hexdigest(), "parse_status": state['parse_status'], "folder_path": ""}], "total": 1,
                    "page": 1, "page_size": 100})
            if request.url.path == "/api/v1/knowledge/source-1":
                return httpx.Response(200, json={"success": True, "data": {"id": "source-1", "knowledge_base_id": KB,
                    "file_hash": hashlib.md5(blob).hexdigest(), "parse_status": "stored"}})
            if request.url.path == "/api/v1/knowledge/source-1/download":
                return httpx.Response(200, content=blob)
        if request.url.host == "structured-data":
            if state["structured_down"]:
                return httpx.Response(503, json={"detail": "temporarily down"})
            if request.headers.get("x-research-identity"):
                encoded = request.headers["x-research-identity"]
                identity = json.loads(base64.urlsafe_b64decode(encoded + "=" * (-len(encoded) % 4)))
                state["signed"].append(identity)
                assert identity["knowledge_base_id"] == KB
                assert identity["knowledge_base_tenant_id"] == 23
            if request.url.path == '/api/research/facts':
                body = json.loads(request.content)
                active = any(source.get('knowledge_id') == 'source-1'
                             and source.get('file_hash') == hashlib.md5(blob).hexdigest()
                             for source in body['sources'])
                indicators = state['canonical_indicators'] if active else []
                rows = state['canonical_rows'] if active else []
                version = hashlib.sha256(json.dumps([indicators, rows], sort_keys=True).encode()).hexdigest()
                offset, limit = body.get('offset', 0), body.get('limit', 10000)
                selected = rows[offset:offset + limit]
                next_offset = offset + len(selected) if offset + len(selected) < len(rows) else None
                return httpx.Response(200, json={'knowledge_base_id': KB, 'knowledge_base_tenant_id': 23,
                    'version': version, 'datasets': [{'dataset_id': DATASET, 'import_id': 'fresh-import',
                        'source_knowledge_id': 'source-1', 'source_file': 'measurements.xlsx'}] if indicators else [],
                    'indicators': indicators, 'rows': selected,
                    'total': len(rows), 'offset': offset, 'next_offset': next_offset,
                    'truncated': next_offset is not None})
            if request.url.path == "/api/tools/read":
                body = json.loads(request.content)
                if body["operation"] == "list_datasets":
                    fallback = [{**manifest, 'use_reviewed_mappings': True}] if state['legacy_snapshot'] else [manifest]
                    return httpx.Response(200, json={"datasets": state['datasets'] if state['datasets'] is not None else fallback})
                return httpx.Response(200, json={"dataset_id": DATASET, "series": [series], "total": 1})
            if request.url.path == "/api/imports":
                return httpx.Response(200, json={"imports": state['imports']})
            if request.url.path == "/api/research/export":
                observed = "2099-01-01" if state["future"] else "2026-01-01"
                spec = {**series, "series_id": "wrong-field"} if state["wrong_series"] else series
                actual_manifest = {**manifest, 'use_reviewed_mappings': True} if state['legacy_snapshot'] else manifest
                return httpx.Response(200, json={"manifest": actual_manifest, "series": spec,
                    "rows": [{"date": observed, "value": 7.5, "source_cell": "B2", "date_cell": "A2",
                              "raw_value": 7.5, "value_status": "valid"},
                             {"date": "2026-01-02", "value": None, "source_cell": "B3", "date_cell": "A3",
                              "raw_value": "#N/A", "value_status": "error"}],
                    "total": 2, "offset": 0, "next_offset": None, "truncated": False})
            if request.url.path == f"/api/research/source/{DATASET}":
                return httpx.Response(200, content=blob)
            if request.url.path == "/api/import-knowledge":
                assert request.headers["x-knowledge-base-id"] == KB
                assert request.headers["authorization"] == "Bearer writer"
                assert request.headers["origin"] == "http://structured-data:19082"
                assert json.loads(request.content) == {"source_knowledge_id": "source-1", "restore_reviewed_state": False}
                return httpx.Response(200, json={"id": "scan-job", "status": "processing"})
            if request.url.path == "/api/jobs/scan-job":
                return httpx.Response(200, json={"id": "scan-job", "status": "completed", "result": {"import_id": "new-import"}})
            if request.url.path == "/api/imports/new-import/sheet":
                assert request.url.params["name"] == "Measurements"
                return httpx.Response(200, json={"name": "Measurements", "mapping": {"sheet": "Measurements", "data_start_row": 2}})
            if request.url.path == "/api/imports/new-import/sample-preview":
                assert request.headers["x-knowledge-base-id"] == KB
                assert "source_role" not in json.loads(request.content)
                return httpx.Response(200, json={"samples": [{"source_cell": "B2", "value": 7.5}]})
            if request.url.path == "/api/imports/new-import/preview":
                assert "source_role" not in json.loads(request.content)
                return httpx.Response(200, json={"observation_count": 2, "sample": [{"source_cell": "B2", "value": 7.5}]})
        return httpx.Response(404, json={"detail": "fixture endpoint absent"})

    mock = httpx.MockTransport(transport)
    original_async = httpx.AsyncClient
    original_client = httpx.Client
    monkeypatch.setattr(httpx, "AsyncClient", lambda **kwargs: original_async(transport=mock, **kwargs))

    def sync_request(method, url, **kwargs):
        kwargs.pop("trust_env", None)
        with original_client(transport=mock) as client:
            return client.request(method, url, **kwargs)
    monkeypatch.setattr(httpx, "request", sync_request)
    headers = {"Authorization": "Bearer writer", "X-Tenant-ID": "17", "X-Research-User": "writer",
               "Origin": "http://testserver"}
    return TestClient(app), headers, state, tmp_path


def endpoint(workbench=WB, suffix="overview"):
    return f"/research/api/workbenches/{workbench}/{suffix}"


def preview(client, headers):
    result = client.post(endpoint(suffix="imports/preview"), headers=headers,
                         json={"dataset_id": DATASET, "series_id": "field-1"})
    assert result.status_code == 200, result.text
    return result.json()


def confirmation(batch):
    return {"batch_id": batch["batch_id"], "source_unit": "吨", "target_unit": "吨", "factor": 1,
            "caliber": {"time_type": "发布时点"}, "default_entry_time": "15:00",
            "note": "Verified source field and unit", "confirmed": True}


def confirm_kb_source(state, rows=None):
    """Remote KB confirmation fixture; research itself never registers facts."""
    identifier = 'kb-' + hashlib.sha256(json.dumps([KB, 'source-1', 'field-1']).encode()).hexdigest()[:48]
    state['canonical_indicators'] = [{'series_id': identifier, 'source_series_id': 'field-1',
        'source_knowledge_id': 'source-1', 'dataset_id': DATASET, 'metric': 'Quantity', 'unit': '吨',
        'frequency': '日', 'sheet': 'Measurements', 'date_mode': 'date', 'header_cells': ['B1']}]
    state['canonical_rows'] = [{**raw, 'series_id': identifier, 'source_series_id': 'field-1',
        'source_knowledge_id': 'source-1', 'dataset_id': DATASET} for raw in (rows if rows is not None else [
            {'date': '2026-01-01', 'value': 7.5, 'source_cell': 'B2', 'date_cell': 'A2',
             'raw_value': 7.5, 'value_status': 'valid'},
            {'date': '2026-01-02', 'value': None, 'source_cell': 'B3', 'date_cell': 'A3',
             'raw_value': '#N/A', 'value_status': 'error'}])]
    return identifier


def test_new_workbench_starts_empty_and_never_creates_legacy_database(isolated):
    client, headers, state, tmp = isolated
    result = client.get(endpoint(), headers=headers)
    assert result.status_code == 200, result.text
    doc = result.json()
    assert doc["counts"]["indicators"] == doc["counts"]["observations"] == 0
    assert doc["workbench"]["workspace_mode"] == "isolated"
    assert not (tmp / "workspaces" / "17" / "tin.db").exists()
    assert list(tmp.glob(f"workbenches/17/{WB}/*.db"))
    assert ("GET", f"/api/v1/workspaces/research/{WB}") in state["calls"]


@pytest.mark.parametrize("change,status", [("go_bad_id", 403), ("revoked_kb", 403), ("go_tenant", 403)])
def test_every_request_revalidates_workbench_and_kb_before_touching_data(isolated, change, status):
    client, headers, state, tmp = isolated
    assert client.get(endpoint(), headers=headers).status_code == 200
    state[change] = 999 if change == "go_tenant" else True
    assert client.get(endpoint(), headers=headers).status_code == status
    assert client.get(endpoint(suffix="sources"), headers=headers).status_code == status


def test_unknown_legacy_malformed_or_wrong_actor_requests_are_rejected(isolated):
    client, headers, _, tmp = isolated
    assert client.get(endpoint(), headers={}).status_code == 401
    legacy = client.get(endpoint(LEGACY), headers=headers)
    assert legacy.status_code == 200 and legacy.json()['counts']['indicators'] == 0
    assert client.get(endpoint('not-a-uuid'), headers=headers).status_code == 400
    assert client.get(endpoint('10000000-0000-4000-8000-000000000099'), headers=headers).status_code == 404
    assert client.get(endpoint(), headers={**headers, 'X-Tenant-ID': '99'}).status_code == 403
    assert client.get(endpoint(), headers={**headers, 'X-Research-User': 'forged'}).status_code == 403


def test_native_sources_and_structured_catalog_use_bound_shared_kb_only(isolated):
    client, headers, state, _ = isolated
    confirm_kb_source(state)
    result = client.get(endpoint(suffix="sources"), headers=headers)
    assert result.status_code == 200, result.text
    assert result.json()["knowledge_base_id"] == KB
    assert result.json()["sources"][0]["id"] == "source-1"
    result = client.get(endpoint(suffix=f"datasets/{DATASET}/series"), headers=headers)
    assert result.status_code == 200 and result.json()["series"][0]["source_series_id"] == "field-1"
    assert state["signed"] and all(i["knowledge_base_tenant_id"] == 23 for i in state["signed"])


def test_confirmed_real_snapshot_registers_facts_and_keeps_cell_provenance(isolated):
    client, headers, state, tmp = isolated
    identifier = confirm_kb_source(state)
    rows = client.get(endpoint(suffix=f'indicators/{identifier}/observations'), headers=headers).json()
    assert len(rows['observations']) == 1 and rows['observations'][0]['value'] == 7.5
    locator = rows['observations'][0]['sources'][0]['locator']
    assert locator['value_cell'] == 'B2' and locator['date_cell'] == 'A2'
    assert '#N/A' not in str(rows['observations'][0]['value'])
    assert client.get(endpoint(OTHER, 'indicators'), headers=headers).json()['total'] == 1
    assert not list(tmp.glob('sources/**/*.bin'))
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import Indicator, Observation
    from sqlalchemy import func, select
    with isolated_session_factory(17, WB)() as session:
        assert session.scalar(select(func.count()).select_from(Indicator)) == 0
        assert session.scalar(select(func.count()).select_from(Observation)) == 0
    again = client.get(endpoint(suffix=f'indicators/{identifier}/observations'), headers=headers).json()
    assert again == rows


@pytest.mark.parametrize("change", [{"confirmed": False}, {"source_unit": "kg"}, {"factor": 0},
                                  {"factor": 2}, {"research_series_id": "invented"}])
def test_unconfirmed_or_forged_mapping_cannot_write_observations(isolated, change):
    client, headers, _, _ = isolated
    result = client.post(endpoint(suffix='imports/confirm'), headers=headers,
                         json={**confirmation({'batch_id': 'retired-candidate'}), **change})
    assert result.status_code == 409, result.text
    assert '知识库' in result.text
    assert client.get(endpoint(suffix='indicators'), headers=headers).json()['total'] == 0


def test_future_dates_require_explicit_review_and_cross_workbench_batch_is_denied(isolated):
    client, headers, state, _ = isolated
    state['future'] = True
    payload = {**confirmation({'batch_id': 'retired-candidate'}), 'future_dates_confirmed': True}
    for workbench in (WB, OTHER):
        assert client.post(endpoint(workbench, 'imports/confirm'), headers=headers, json=payload).status_code == 409
        assert client.get(endpoint(workbench, 'indicators'), headers=headers).json()['total'] == 0
    assert not any(path == '/api/research/export' for method, path in state['calls'])


def test_viewer_and_cross_origin_cannot_preview_or_commit(isolated):
    client, headers, _, tmp = isolated
    viewer = {**headers, "Authorization": "Bearer viewer", "X-Research-User": "viewer"}
    assert client.get(endpoint(), headers=viewer).status_code == 200
    for actor in (viewer, {**headers, "Origin": "https://foreign.test"}):
        result = client.post(endpoint(suffix="imports/preview"), headers=actor,
                             json={"dataset_id": DATASET, "series_id": "field-1"})
        assert result.status_code == 403, result.text


def test_mismatched_export_identity_cannot_create_candidate(isolated):
    client, headers, state, _ = isolated
    identifier = confirm_kb_source(state)
    state['canonical_rows'][0]['source_series_id'] = 'wrong-field'
    result = client.get(endpoint(suffix=f'indicators/{identifier}/observations'), headers=headers)
    assert result.status_code == 503
    assert '7.5' not in result.text


def test_batch_requests_preserve_independent_errors_and_explicit_confirmations(isolated):
    client, headers, _, _ = isolated
    for suffix, items in [('imports/preview-batch', [
            {'dataset_id': DATASET, 'series_id': 'field-1'}, {'dataset_id': DATASET, 'series_id': 'wrong'}]),
            ('imports/confirm-batch', [confirmation({'batch_id': 'old-one'}),
                {**confirmation({'batch_id': 'old-two'}), 'confirmed': False}])]:
        response = client.post(endpoint(suffix=suffix), headers=headers, json={'items': items})
        assert response.status_code == 409 and '知识库' in response.text
    assert client.get(endpoint(suffix='indicators'), headers=headers).json()['total'] == 0


def test_structured_failure_keeps_document_and_guide_available_with_explicit_error(isolated):
    client, headers, state, _ = isolated
    confirm_kb_source(state)
    assert client.get(endpoint(), headers=headers).status_code == 200
    state['structured_down'] = True
    response = client.get(endpoint(), headers=headers)
    assert response.status_code == 503
    assert '知识库' in response.text and '7.5' not in response.text
    state['structured_down'] = False
    assert client.get(endpoint(), headers=headers).json()['counts']['indicators'] == 1


def test_one_published_field_does_not_mark_all_source_sheets_reviewed(isolated):
    client, headers, state, _ = isolated
    confirm_kb_source(state)
    result = client.get(endpoint(), headers=headers)
    assert result.status_code == 200
    counts = result.json()['counts']
    assert counts['indicators'] == 1 and counts['excel_pending'] == 1
    assert result.json()['readiness']['tasks'][1]['status'] == 'partially_ready'


def test_explicit_acquisition_plan_is_not_counted_as_pending_observation_import(isolated):
    client, headers, _, _ = isolated
    result = client.put(endpoint(suffix='sources/source-1/role'), headers=headers,
                        json={'source_role': 'acquisition-plan'})
    assert result.status_code == 200
    result = client.get(endpoint(), headers=headers).json()
    assert result['counts']['excel_pending'] == 0 and result['counts']['observation_source_count'] == 0
    assert next(task for task in result['readiness']['tasks'] if task['id'] == 'review')['optional'] is True


def test_explicit_plan_role_is_persistent_and_cannot_become_observations(isolated):
    client, headers, _, _ = isolated
    batch = {'batch_id': 'retired-candidate'}
    role = client.put(endpoint(suffix="sources/source-1/role"), headers=headers, json={"source_role": "acquisition-plan"})
    assert role.status_code == 200, role.text
    assert client.get(endpoint(suffix="sources"), headers=headers).json()["sources"][0]["source_role"] == "acquisition-plan"
    result = client.post(endpoint(suffix="imports/confirm"), headers=headers, json=confirmation(batch))
    assert result.status_code == 409
    result = client.post(endpoint(suffix="imports/preview"), headers=headers,
                         json={"dataset_id": DATASET, "series_id": "field-1", "source_role": "observations"})
    assert result.status_code == 409
    assert client.get(endpoint(suffix="indicators"), headers=headers).json()["total"] == 0


def test_scan_and_sheet_review_remain_scoped_and_disable_legacy_restoration(isolated):
    client, headers, _, _ = isolated
    scan = client.post(endpoint(suffix="sources/source-1/scan"), headers=headers)
    assert scan.status_code == 200 and scan.json()["id"] == "scan-job"
    job = client.get(endpoint(suffix="scan-jobs/scan-job"), headers=headers)
    assert job.status_code == 200 and job.json()["result"]["import_id"] == "new-import"
    sheet = client.get(endpoint(suffix="source-imports/new-import/sheet"), params={"name": "Measurements"}, headers=headers)
    assert sheet.status_code == 200 and sheet.json()["mapping"]["data_start_row"] == 2
    sample = client.post(endpoint(suffix="source-imports/new-import/sample-preview"), headers=headers,
                         json={"mapping": sheet.json()["mapping"]})
    assert sample.status_code == 200 and sample.json()["samples"][0]["source_cell"] == "B2"
    checked = client.post(endpoint(suffix="source-imports/new-import/preview"), headers=headers,
                          json={"mapping": sheet.json()["mapping"], "save_template": False, "source_role": "observations"})
    assert checked.status_code == 200 and checked.json()["observation_count"] == 2
    for suffix in ("preview", "sample-preview", "confirm"):
        denied = client.post(endpoint(suffix=f"source-imports/new-import/{suffix}"), headers=headers,
                             json={"mapping": sheet.json()["mapping"], "source_role": "acquisition-plan"})
        assert denied.status_code == 409


def test_signed_workbench_reads_use_no_seed_and_keep_scope_fields_out_of_model_arguments(isolated):
    def signed_headers(body, key, identity):
        stamp = str(int(time.time()))
        identity = {**identity, "authorization_sha256": hashlib.sha256(b"Bearer writer").hexdigest()}
        encoded = base64.urlsafe_b64encode(json.dumps(identity).encode()).decode().rstrip("=")
        message = "\n".join((stamp, "POST", "/api/tools/read", encoded, hashlib.sha256(body).hexdigest()))
        return {"Content-Type": "application/json", "X-Research-Authorization": "Bearer writer", "X-Research-Identity": encoded,
                "X-Research-Timestamp": stamp,
                "X-Research-Signature": hmac.new(key.encode(), message.encode(), hashlib.sha256).hexdigest()}
    client, _, state, tmp = isolated
    identity = {"user_id": "writer", "email": "writer@test", "display_name": "Writer", "tenant_id": 17,
                "role": "contributor", "workbench_id": WB, "knowledge_base_id": KB, "knowledge_base_tenant_id": 23}
    body = b'{"operation":"indicators"}'
    result = client.post("/research/api/tools/read", content=body, headers=signed_headers(body, "a" * 48, identity))
    assert result.status_code == 200 and result.json()["total"] == 0
    assert result.json()["workbench_id"] == WB and not (tmp / "workspaces" / "17" / "tin.db").exists()
    headers = {"Authorization": "Bearer writer", "X-Tenant-ID": "17", "X-Research-User": "writer", "Origin": "http://testserver"}
    identifier = confirm_kb_source(state)
    read = json.dumps({"operation": "series", "series_id": identifier}).encode()
    result = client.post("/research/api/tools/read", content=read, headers=signed_headers(read, "a" * 48, identity))
    assert result.status_code == 200
    assert result.json()["data"]["observations"][0]["sources"][0]["locator"]["value_cell"] == "B2"
    assert "/platform/commodity-research" not in result.text
    other = client.post("/research/api/tools/read", content=body,
                        headers=signed_headers(body, "a" * 48, {**identity, "workbench_id": OTHER}))
    assert other.status_code == 200 and other.json()["total"] == 1
    for fields in ({"workbench_id": WB}, {"workbench_id": "../bad", "knowledge_base_id": KB,
                   "knowledge_base_tenant_id": 23}):
        forged = {key: value for key, value in identity.items() if key not in {
            "workbench_id", "knowledge_base_id", "knowledge_base_tenant_id"}}
        forged.update(fields)
        denied = client.post("/research/api/tools/read", content=body, headers=signed_headers(body, "a" * 48, forged))
        assert denied.status_code == 401
    body = json.dumps({"operation": "indicators", "workbench_id": OTHER}).encode()
    assert client.post("/research/api/tools/read", content=body,
                       headers=signed_headers(body, "a" * 48, identity)).status_code == 422


def test_isolated_summary_returns_latest_normal_revisions_and_real_pagination(isolated):
    from datetime import date
    from tin.integration.identity import Identity
    from tin.integration.workbench_scope import service_scope, workbench_session
    from tin.integration.read_api import ReadRequest, read_isolated_research
    client, headers, state, _ = isolated
    confirm_kb_source(state, [
        {'date': '2024-01-01', 'value': 15, 'source_cell': 'B2', 'value_status': 'valid'},
        {'date': '2024-01-02', 'value': 20, 'source_cell': 'B3', 'value_status': 'error'},
        {'date': '2024-01-03', 'value': 19, 'source_cell': 'B4', 'value_status': 'valid'}])
    identity = Identity('writer', 'writer@test', 'Writer', 17, 'contributor', WB, KB, 23, authorization='Bearer writer')
    with workbench_session(service_scope(identity)) as session:
        first = read_isolated_research(session, ReadRequest(operation='summary', limit=1), WB)
        assert first['total'] == 2 and first['truncated'] and first['next_offset'] == 1
        assert [row['value'] for row in first['observations']] == [19]
        assert first['counts']['current_observations'] == 2 and first['counts']['all_observation_revisions'] == 2
        second = read_isolated_research(session, ReadRequest(operation='summary', limit=1, offset=1), WB)
        assert [row['value'] for row in second['observations']] == [15] and not second['truncated']
        cutoff = read_isolated_research(session, ReadRequest(operation='summary', date=date(2024, 1, 1)), WB)
        assert cutoff['total'] == 1 and cutoff['counts']['all_observation_revisions'] == 1


def test_catalog_defaults_to_native_current_fresh_snapshot_and_keeps_history_separate(isolated):
    client, headers, state, _ = isolated
    confirm_kb_source(state)
    source = client.get(endpoint(suffix='sources'), headers=headers).json()['sources'][0]
    binding = {'source_knowledge_id': source['id'], 'file_hash': source['file_hash'],
               'knowledge_base_id': KB, 'tenant_id': 23}
    state['imports'] = [
        {'import_id': 'fresh-import', 'dataset_id': DATASET, 'use_reviewed_mappings': False,
         'native_source_bindings': [binding], 'updated_at': '2026-01-02', 'summary': {'confirmed': 2, 'empty': 1}},
        {'import_id': 'wrong-hash-import', 'dataset_id': 'wrong-hash', 'use_reviewed_mappings': False,
         'native_source_bindings': [{**binding, 'file_hash': 'changed-original'}], 'updated_at': '2026-01-03'},
        {'import_id': 'legacy-import', 'dataset_id': 'old-profile', 'use_reviewed_mappings': True,
         'native_source_bindings': [binding], 'updated_at': '2025-01-04'}]
    state['datasets'] = [
        {'dataset_id': DATASET, 'import_id': 'fresh-import', 'source_file': 'same-name.xlsx'},
        {'dataset_id': 'prior-sheet-snapshot', 'import_id': 'fresh-import', 'source_file': 'same-name.xlsx'},
        {'dataset_id': 'wrong-hash', 'import_id': 'wrong-hash-import', 'source_file': 'same-name.xlsx'},
        {'dataset_id': 'old-profile', 'import_id': 'legacy-import', 'source_file': 'same-name.xlsx'},
        {'dataset_id': 'unbound-same-name', 'source_file': 'same-name.xlsx', 'is_latest_for_source': True}]
    result = client.get(endpoint(suffix='sources'), headers=headers)
    assert result.status_code == 200, result.text
    body = result.json()
    assert [row['dataset_id'] for row in body['datasets']] == [DATASET]
    assert body['datasets'][0]['series_count'] == 1
    assert body['datasets'][0]['import_id'] == 'fresh-import'
    assert [row['dataset_id'] for row in body['dataset_versions']] == ['prior-sheet-snapshot']


def test_preview_freezes_native_binding_by_original_hash_and_rejects_unbound_original(isolated):
    client, headers, state, tmp = isolated
    identifier = confirm_kb_source(state)
    result = client.get(endpoint(suffix=f'indicators/{identifier}/observations'), headers=headers)
    assert result.status_code == 200
    locator = result.json()['observations'][0]['sources'][0]['locator']
    assert locator['knowledge_id'] == 'source-1' and locator['knowledge_base_id'] == KB
    state['file_hash_override'] = '0' * 32
    assert client.get(endpoint(suffix='indicators'), headers=headers).json()['total'] == 0
    assert not list(tmp.glob('sources/**/*.bin'))


def test_original_source_date_confirmation_preserves_date_without_claiming_publication_time(isolated):
    from datetime import datetime
    from tin.config import SHANGHAI
    client, headers, state, _ = isolated
    identifier = confirm_kb_source(state)
    rows = client.get(endpoint(suffix=f'indicators/{identifier}/observations'), headers=headers).json()
    indicator, observation = rows['indicator'], rows['observations'][0]
    assert indicator['caliber']['time_type'] == '原表日期'
    assert indicator['default_entry_time'] is None
    assert datetime.fromisoformat(observation['as_of']).astimezone(SHANGHAI).date().isoformat() == '2026-01-01'
    assert observation['caliber']['time_type'] == '原表日期'
    assert observation['sources'][0]['locator']['date'] == '2026-01-01'
    assert observation['sources'][0]['locator']['date_cell'] == 'A2'


def test_signed_manual_research_honors_pagination_without_claiming_historical_replay(isolated):
    from datetime import date, datetime, timezone
    from tin.integration.identity import Identity
    from tin.integration.workbench_scope import service_scope, workbench_session
    from tin.integration.read_api import ReadRequest, read_isolated_research
    from tin.models.workbench import WorkbenchArtifact
    identity = Identity('writer', 'writer@test', 'Writer', 17, 'contributor', WB, KB, 23, authorization='Bearer writer')
    with workbench_session(service_scope(identity)) as session:
        for index, status in [(1, 'draft'), (2, 'confirmed')]:
            stamp = datetime(2026, 1, index, tzinfo=timezone.utc)
            session.add(WorkbenchArtifact(id=f'fixture-{index}', module='judgments', actor='writer',
                title=f'Fixture {index}', body='Human record', status=status, source_ids=[], source_snapshots=[],
                revision=1, created_at=stamp, updated_at=stamp))
        session.commit()
        result = read_isolated_research(session, ReadRequest(operation='judgment', limit=1, offset=1,
                                         date=date(2020, 1, 1)), WB)
        assert result['data']['total'] == 2 and result['data']['offset'] == 1
        assert [row['status'] for row in result['data']['items']] == ['draft']
        assert result['temporal_scope'] == 'current_manual_records' and not result['requested_date_applied']
        assert '不代表该日历史版本' in result['notice']


def test_old_reviewed_snapshot_id_cannot_bypass_fresh_workbench_entry(isolated):
    client, headers, state, _ = isolated
    state['legacy_snapshot'] = True
    result = client.get(endpoint(suffix=f'datasets/{DATASET}/series'), headers=headers)
    assert result.status_code == 409
    result = client.post(endpoint(suffix='imports/preview'), headers=headers,
                         json={'dataset_id': DATASET, 'series_id': 'field-1'})
    assert result.status_code == 409
    assert client.get(endpoint(suffix='indicators'), headers=headers).json()['total'] == 0


def test_confirm_rejects_legacy_frozen_manifest_and_missing_current_native_evidence(isolated):
    client, headers, state, _ = isolated
    for altered in (False, True):
        state['legacy_snapshot'] = altered
        state['file_hash_override'] = '0' * 32 if altered else None
        result = client.post(endpoint(suffix='imports/confirm'), headers=headers,
                             json=confirmation({'batch_id': 'historical-frozen-candidate'}))
        assert result.status_code == 409 and '知识库' in result.text
    assert client.get(endpoint(suffix='indicators'), headers=headers).json()['total'] == 0


def test_fresh_series_without_current_native_import_binding_is_not_exposed(isolated):
    client, headers, state, _ = isolated
    state['imports'][0]['native_source_bindings'][0]['file_hash'] = 'different-original'
    response = client.get(endpoint(suffix=f'datasets/{DATASET}/series'), headers=headers)
    assert response.status_code == 409


@pytest.mark.parametrize('parse_status', ['deleting', 'deleted'])
def test_source_deletion_after_preview_blocks_new_reads_and_fact_confirmation(isolated, parse_status):
    client, headers, state, _ = isolated
    confirm_kb_source(state)
    assert client.get(endpoint(suffix='indicators'), headers=headers).json()['total'] == 1
    state['parse_status'] = parse_status
    assert client.get(endpoint(suffix=f'datasets/{DATASET}/series'), headers=headers).status_code == 409
    assert client.post(endpoint(suffix='imports/preview'), headers=headers,
        json={'dataset_id': DATASET, 'series_id': 'field-1'}).status_code == 409
    assert client.post(endpoint(suffix='imports/confirm'), headers=headers,
        json=confirmation({'batch_id': 'retired-candidate'})).status_code == 409
    assert client.get(endpoint(suffix='indicators'), headers=headers).json()['total'] == 0
