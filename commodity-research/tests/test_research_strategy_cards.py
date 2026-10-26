"""Actual personal-artifact storage, review-card protocol and scoped read boundaries."""
from dataclasses import replace
from datetime import datetime
import json

from fastapi import FastAPI, HTTPException
from fastapi.testclient import TestClient
import httpx
import pytest
from sqlalchemy import select

from tin.config import ROOT, settings
from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, current_workbench, artifact_session
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision
from tin.web import research_strategy

WB = "fbc4430b-cb50-49c2-9479-289f2f52971f"
OTHER = "272c1307-3f64-4306-953e-9789bf2b53c8"
URL = "/api/sn/research-strategies"


@pytest.fixture
def cards(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, "data_dir", tmp_path)
    monkeypatch.setattr(settings, "weknora_api_url", "http://native.test")
    monkeypatch.setattr(settings, "structured_data_url", "http://structured.test")
    key = tmp_path / "service-key"
    key.write_bytes(b"c" * 48)
    monkeypatch.setattr(settings, "service_token_file", key)
    state = {"source_reads": [], "fact_reads": [], "scope": None}
    app = FastAPI()
    app.include_router(research_strategy.router)

    @app.middleware("http")
    async def scoped(request, call_next):
        identity = Identity(request.headers.get("x-actor", "first"), "fixture@example.test", "Fixture", 7,
                            request.headers.get("x-role", "owner"))
        workbench = request.headers.get("x-workbench", WB)
        scope = WorkbenchScope(identity, workbench, "library-a", 7,
            {"id": workbench, "workspace_mode": "isolated", "can_edit": request.headers.get("x-can-edit", "true") == "true",
             "can_read_source": request.headers.get("x-restricted") != "true"}, "Bearer fixture")
        if request.headers.get("x-no-module") == "true":
            scope = replace(scope, view_modules=("home",))
        state["scope"] = scope
        marker = current_workbench.set(None if request.headers.get("x-no-workbench") == "true" else scope)
        user = current_identity.set(identity)
        try:
            return await call_next(request)
        finally:
            current_identity.reset(user)
            current_workbench.reset(marker)

    async def native(scope, path, **kwargs):
        state["source_reads"].append(path)
        if path == "/api/v1/knowledge-bases/library-a/knowledge":
            return {"success": True, "data": [{"id": "source-a", "title": "A real source", "file_name": "source.md",
                     "knowledge_base_id": "library-a", "file_type": "md", "secret": "not projected"}], "total": 1}
        identifier = path.rsplit("/", 1)[-1]
        if identifier == "missing":
            raise HTTPException(404, "missing")
        return {"success": True, "data": {"id": identifier,
                "knowledge_base_id": "library-b" if identifier == "foreign" else "library-a",
                "type": "manual", "title": "Actual authorized source", "file_hash": "", "file_size": 0}}

    monkeypatch.setattr("tin.web.workbench_artifacts.go_request", native)
    monkeypatch.setattr("tin.web.workbenches.go_request", native)

    def facts(method, address, **kwargs):
        state["fact_reads"].append(address)
        if address.startswith("http://native.test/api/v1/workspaces/research/"):
            return httpx.Response(200, json={"success": True, "data": {"id": address.rsplit("/", 1)[-1],
                    "scope_tenant_id": 7, "knowledge_base_id": "library-a", "workspace_mode": "isolated"}},
                    request=httpx.Request(method, address))
        if address == "http://native.test/api/v1/knowledge-bases/library-a/knowledge":
            return httpx.Response(200, json={"success": True, "data": [], "total": 0}, request=httpx.Request(method, address))
        assert address == "http://structured.test/api/research/facts"
        data = json.loads(kwargs["content"])
        if data.get("metadata_only"):
            return httpx.Response(200, json={"knowledge_base_id": "library-a", "knowledge_base_tenant_id": 7,
                    "version": "a" * 64, "metadata_only": True}, request=httpx.Request(method, address))
        return httpx.Response(200, json={"knowledge_base_id": "library-a", "knowledge_base_tenant_id": 7,
                "version": "a" * 64, "datasets": [], "indicators": [], "rows": [], "total": 0,
                "offset": 0, "next_offset": None, "truncated": False}, request=httpx.Request(method, address))

    monkeypatch.setattr(httpx, "request", facts)
    with TestClient(app) as client:
        yield client, state


def entry(**changes):
    value = {"card": {"format": research_strategy.FORMAT, "title": "A testable research hypothesis",
             "direction": "observe", "thesis": "Demand may change; verify before drawing a conclusion.",
             "watch_for": "Compare actual load and inventory in one dated source.",
             "trigger": "Revisit after the next authorized observation.",
             "invalidation": "Reject if the underlying evidence contradicts the hypothesis.",
             "review_date": "2024-04-05", "original_text": "# Original Markdown\n\nA human statement, not a fact."},
             "source_ids": ["source-a"]}
    value.update(changes)
    return value


def test_research_card_does_not_require_prices_or_fabricate_execution(cards):
    client, state = cards
    created = client.post(URL, json=entry())
    assert created.status_code == 201, created.text
    row = created.json()
    assert row["status"] == "draft" and row["revision"] == 1
    assert row["card"] == entry()["card"]
    assert "尚未" in row["evidence_notice"] and "关联原件" in row["evidence_notice"]
    assert row["source_snapshots"][0]["capture_status"] == "metadata_only"
    scope = state["scope"]
    with artifact_session(scope) as session:
        actual = session.get(WorkbenchArtifact, row["id"])
        assert actual.actor == "first" and actual.module == "strategies" and actual.status == "draft"
        assert json.loads(actual.body) == entry()["card"]
        revisions = session.scalars(select(WorkbenchArtifactRevision).where(WorkbenchArtifactRevision.artifact_id == row["id"])).all()
        assert len(revisions) == 1 and revisions[0].edited_by == "first"


def test_owner_workbench_and_module_boundaries_use_real_store(cards):
    client, state = cards
    created = client.post(URL, json=entry()).json()
    assert client.get(URL).json()["total"] == 1
    assert client.get(URL, headers={"x-actor": "second"}).json()["total"] == 0
    assert client.get(URL, headers={"x-workbench": OTHER}).json()["total"] == 0
    for headers in ({"x-restricted": "true"}, {"x-no-module": "true"}):
        assert client.get(URL, headers=headers).status_code == 403
        assert client.post(URL, headers=headers, json=entry()).status_code == 403
    assert client.get(URL, headers={"x-no-workbench": "true"}).status_code == 409
    assert client.patch(URL + "/" + created["id"], headers={"x-actor": "second"}, json=entry(revision=1)).status_code == 404


def test_revision_compare_and_swap_retains_source_history(cards):
    client, state = cards
    created = client.post(URL, json=entry()).json()
    target = URL + "/" + created["id"]
    updated = entry(revision=1)
    updated["card"]["thesis"] = "An explicitly revised hypothesis"
    assert client.patch(target, json=entry()).status_code == 400
    response = client.patch(target, json=updated)
    assert response.status_code == 200, response.text
    assert response.json()["revision"] == 2
    assert client.patch(target, json=updated).status_code == 409
    with artifact_session(state["scope"]) as session:
        old = session.get(WorkbenchArtifactRevision, (created["id"], 1))
        new = session.get(WorkbenchArtifactRevision, (created["id"], 2))
        assert json.loads(old.body)["thesis"] == entry()["card"]["thesis"]
        assert json.loads(new.body)["thesis"] == updated["card"]["thesis"]


@pytest.mark.parametrize("source,status", [(["foreign"], 403), (["missing"], 404), (["../escape"], 400)])
def test_source_ids_are_native_permissions_not_asserted_indicator_evidence(cards, source, status):
    client, state = cards
    assert client.post(URL, json=entry(source_ids=source)).status_code == status
    assert client.get(URL).json()["total"] == 0


def test_space_role_and_workbench_edit_both_bound_card_writes(cards):
    client, state = cards
    assert client.post(URL, json=entry(), headers={"x-role": "viewer"}).status_code == 403
    assert client.post(URL, json=entry(), headers={"x-role": "contributor", "x-can-edit": "false"}).status_code == 403
    assert client.post(URL, json=entry(), headers={"x-role": "owner", "x-can-edit": "false"}).status_code == 403
    response = client.post(URL, json=entry(), headers={"x-role": "contributor", "x-can-edit": "true"})
    assert response.status_code == 201, response.text
    assert response.json()["status"] == "draft"


def test_list_does_not_load_fact_catalog_or_recapture_sources(cards):
    client, state = cards
    assert client.post(URL, json=entry()).status_code == 201
    state["fact_reads"].clear()
    state["source_reads"].clear()
    assert client.get(URL).json()["total"] == 1
    assert state["fact_reads"] == [] and state["source_reads"] == []


def test_bounded_input_rejects_duplicate_properties_nonfinite_and_fake_confirmation(cards):
    client, state = cards
    assert client.post(URL, content=b" " * (research_strategy.MAX_REQUEST_BYTES + 1)).status_code == 413
    assert client.post(URL, content=b'{"card":{},"card":{}}').status_code == 422
    assert client.post(URL, content=b'{"card":NaN}').status_code == 422
    assert client.post(URL, json=entry(confirmed=True, status="confirmed")).status_code == 422
    bad = entry()
    bad["card"]["thesis"] = " \n "
    assert client.post(URL, json=bad).status_code == 422
    bad = entry()
    bad["card"]["review_date"] = 0
    assert client.post(URL, json=bad).status_code == 422
    assert state["source_reads"] == []


def test_report_projection_is_personal_bounded_and_omits_frozen_sources(cards):
    from sqlalchemy import event
    client, state = cards
    created = client.post(URL, json=entry()).json()
    scope = state["scope"]
    with artifact_session(scope) as session:
        statements = []
        def observe(connection, cursor, statement, parameters, context, many):
            statements.append(statement)
        event.listen(session.bind, "before_cursor_execute", observe)
        try:
            result = research_strategy.report_projection(session, scope, limit=1)
        finally:
            event.remove(session.bind, "before_cursor_execute", observe)
        assert result["total"] == 1 and len(result["items"]) == 1
        row = result["items"][0]
        assert row["id"] == created["id"] and row["review_date"] == "2024-04-05"
        assert not {"source_snapshots", "source_ids", "original_text", "body", "card"} & row.keys()
        assert not any("source_snapshots" in statement for statement in statements)
        other = replace(scope, identity=replace(scope.identity, user_id="second"))
        assert research_strategy.report_projection(session, other)["items"] == []
        assert research_strategy.report_projection(session, replace(scope, metadata={**scope.metadata, "can_read_source": False}))["items"] == []


def test_sources_only_project_current_native_metadata(cards):
    client, state = cards
    response = client.get(URL + "/sources")
    assert response.status_code == 200, response.text
    assert response.json() == {"items": [{"id": "source-a", "title": "A real source", "file_name": "source.md", "file_type": "md"}],
                               "page": 1, "has_more": False}
    assert state["source_reads"] == ["/api/v1/knowledge-bases/library-a/knowledge"]


def test_partial_displays_readable_fields_and_escapes_untrusted_markdown(cards):
    from jinja2 import Environment, FileSystemLoader, select_autoescape
    client, state = cards
    value = entry()
    value["card"]["original_text"] = "# Example\n<script>window.bad=true</script>"
    assert client.post(URL, json=value).status_code == 201
    context = client.get(URL).json()
    environment = Environment(loader=FileSystemLoader(ROOT / "src/tin/web/templates"), autoescape=select_autoescape())
    html = environment.get_template("_research_strategy_cards.html").render(research_cards=context, restricted_view=False,
        research_static_prefix=lambda: "/research", static_version=lambda: "fixture")
    assert "观察依据" in html and "触发条件" in html and "失效条件" in html and "2024-04-05" in html
    assert "<script>window.bad=true</script>" not in html and "&lt;script&gt;" in html
    assert '"format":' not in html.split('<script id="research-card-data"')[0]
    assert environment.get_template("_research_strategy_cards.html").render(research_cards=context, restricted_view=True).strip() == ""


def test_partial_renders_saved_database_dates_without_response_encoding(cards):
    """The page consumes build_context directly, without FastAPI's JSON response encoder."""
    from jinja2 import Environment, FileSystemLoader, select_autoescape
    client, state = cards
    response = client.post(URL, json=entry(source_ids=[]))
    assert response.status_code == 201, response.text
    identifier = response.json()["id"]
    with artifact_session(state["scope"]) as session:
        stored = session.get(WorkbenchArtifact, identifier)
        assert isinstance(stored.created_at, datetime) and isinstance(stored.updated_at, datetime)
        expected_dates = {field: getattr(stored, field).isoformat()
                          for field in ("created_at", "updated_at")}
        context = research_strategy.build_context(session, state["scope"])
    environment = Environment(loader=FileSystemLoader(ROOT / "src/tin/web/templates"), autoescape=select_autoescape())
    html = environment.get_template("_research_strategy_cards.html").render(research_cards=context, restricted_view=False,
        research_static_prefix=lambda: "/research", static_version=lambda: "fixture")
    embedded = html.split('<script id="research-card-data" type="application/json">', 1)[1].split("</script>", 1)[0]
    rendered = json.loads(embedded)
    assert rendered == context
    row = rendered["items"][0]
    assert row["id"] == identifier
    assert {field: row[field] for field in expected_dates} == expected_dates
    assert row["card"]["review_date"] == entry()["card"]["review_date"]
    # Serialization is a view transformation, never a persisted date replacement.
    with artifact_session(state["scope"]) as session:
        stored = session.get(WorkbenchArtifact, identifier)
        assert {field: getattr(stored, field).isoformat() for field in expected_dates} == expected_dates
