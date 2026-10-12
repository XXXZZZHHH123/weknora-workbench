"""Integrated requests must prove workspace membership before touching any data."""
import time
from pathlib import Path

import pytest
from fastapi import HTTPException
from fastapi.testclient import TestClient
from sqlalchemy import select

from tin.config import settings
from tin.db import SessionLocal, tenant_database_path
from tin.integration import auth
from tin.integration.identity import Identity, current_identity
from tin.models import AuditLog, Judgment, Researcher
from tin.web.app import app


@pytest.fixture
def embedded(monkeypatch, tmp_path):
    monkeypatch.setattr(settings, "weknora_api_url", "http://weknora.internal")
    monkeypatch.setattr(settings, "data_dir", tmp_path)
    monkeypatch.setattr(settings, "auth_cache_seconds", 0)
    auth._sessions.clear()
    identities = {
        "alice": Identity("alice-id", "alice@example.test", "Alice", 101, "owner"),
        "bob": Identity("bob-id", "bob@example.test", "Bob", 101, "contributor"),
        "viewer": Identity("viewer-id", "viewer@example.test", "Viewer", 102, "viewer"),
    }

    async def verify(token, tenant=None):
        identity = identities.get(token)
        if not identity:
            raise HTTPException(401, "revoked")
        if tenant is not None and int(tenant) != identity.tenant_id:
            raise HTTPException(403, "foreign workspace")
        return identity

    monkeypatch.setattr(auth, "verify_bearer", verify)
    return TestClient(app), identities, tmp_path


def login(client, token="alice", tenant=101):
    return client.post("/research/auth/session", headers={
        "Authorization": f"Bearer {token}", "X-Tenant-ID": str(tenant), "Origin": "http://testserver"})


def test_sensitive_pages_api_and_download_require_identity(embedded):
    client, _, tmp = embedded
    for path in ("/research/", "/research/sn", "/research/sn/judgment", "/research/sn/reports/2026-09-18.json",
                 "/research/sn/contracts.csv", "/research/api/sn/import/template", "/research/api/sn/strategies",
                 "/research/openapi.json", "/research/docs"):
        assert client.get(path).status_code == 401, path
    assert not (tmp / "workspaces").exists()
    assert client.get("/research/healthz").status_code == 200
    bootstrap = client.get("/research/auth/bootstrap")
    assert bootstrap.status_code == 200
    assert "event.origin !== location.origin" in bootstrap.text
    assert "Authorization" in bootstrap.text
    assert not (tmp / "workspaces").exists()


def test_exchange_origin_membership_cookie_and_revocation(embedded):
    client, identities, tmp = embedded
    assert client.post("/research/auth/session", headers={"Authorization": "Bearer alice"}).status_code == 403
    assert login(client, tenant=999).status_code == 403
    response = login(client)
    assert response.status_code == 200
    cookie = response.headers["set-cookie"]
    assert "HttpOnly" in cookie and "Path=/research/" in cookie and "SameSite=strict" in cookie
    assert "alice" not in cookie
    assert client.get("/research/sn/judgment", headers={"X-Tenant-ID": "999"}).status_code == 403
    assert not (tmp / "workspaces" / "999").exists()
    assert client.get("/research/api/sn/strategies", headers={"X-Research-User": "different"}).status_code == 403
    assert client.post("/research/sn/entry?_research_tenant=102&_research_user=alice-id", data={},
                       headers={"Origin": "http://testserver"}).status_code == 403
    identities.pop("alice")
    assert client.get("/research/sn/judgment").status_code == 401


def test_tenant_database_prefix_actor_and_new_user_no_legacy_seed(embedded):
    client, identities, tmp = embedded
    assert login(client).status_code == 200
    response = client.get("/research/")
    assert response.status_code == 200, response.text
    assert 'href="/research/sn/judgment"' in response.text
    assert 'src="/research/static/app.js' in response.text
    assert 'action="/research/sn/entry?_research_tenant=101' in response.text
    assert 'window.RESEARCH_PREFIX = "/research"' in response.text
    assert client.get("/research/sn/macro", follow_redirects=False).headers["location"].startswith("/research/sn/indicators?")
    marker = current_identity.set(identities["alice"])
    try:
        with SessionLocal() as session:
            assert session.info["weknora_user_id"] == "alice-id"
            assert session.scalar(select(Judgment)) is None
            assert session.scalar(select(Researcher).where(Researcher.username == "me")) is None
    finally:
        current_identity.reset(marker)
    assert tenant_database_path(101).exists()
    assert not (tmp / "tin.db").exists()
    # Submitted entered_by cannot forge the audit identity. The shared metric itself
    # is a workspace fact, so contributors can write it without claiming others.
    from datetime import datetime
    from tin.config import SHANGHAI
    response = client.post("/research/sn/entry", data={"series_id": "SMM.SN.spot.1", "value": "200000",
        "as_of": datetime.now(SHANGHAI).strftime("%Y-%m-%dT%H:%M"), "entered_by": "forged",
        "note": "test", "return_to": "/research/sn/entry"}, headers={"Origin": "http://testserver"}, follow_redirects=False)
    assert response.status_code == 303, response.text
    assert response.headers["location"].startswith("/research/sn/entry")
    marker = current_identity.set(identities["alice"])
    try:
        with SessionLocal() as session:
            assert session.scalar(select(AuditLog).where(AuditLog.action == "人工录入")).actor == "alice-id"
    finally:
        current_identity.reset(marker)


def test_viewer_and_cookie_csrf_cannot_mutate(embedded):
    client, _, tmp = embedded
    assert login(client, "viewer", 102).status_code == 200
    assert client.post("/research/api/sn/strategies", json={}, headers={"Origin": "http://testserver"}).status_code == 403
    assert login(client).status_code == 200
    assert client.post("/research/api/sn/strategies", json={}, headers={"Origin": "http://evil.test"}).status_code == 403
    assert client.post("/research/api/sn/strategies", json={}).status_code == 403
    assert not (tmp / "workspaces").exists()


@pytest.mark.parametrize("tenant", ["../x", 0, -1, True])
def test_database_path_rejects_unverified_components(tenant):
    with pytest.raises(ValueError):
        tenant_database_path(tenant)


def test_bearer_uses_active_membership_not_home_tenant(monkeypatch):
    import asyncio
    import httpx
    monkeypatch.setattr(settings, "weknora_api_url", "http://internal")
    reply = {"success": True, "data": {"user": {"id": "user", "email": "u@test", "tenant_id": 1},
        "tenant": {"id": 22}, "memberships": [{"tenant_id": 22, "role": "contributor"}]}}
    calls = []
    class Client:
        async def __aenter__(self): return self
        async def __aexit__(self, *args): pass
        async def get(self, url, headers):
            calls.append((url, headers))
            return httpx.Response(200, json=reply, request=httpx.Request("GET", url))
    monkeypatch.setattr(auth.httpx, "AsyncClient", lambda **kwargs: Client())
    identity = asyncio.run(auth.verify_bearer("token", 22))
    assert identity.tenant_id == 22 and identity.can_write
    assert calls[0][1]["X-Tenant-ID"] == "22"
    reply["data"]["memberships"] = []
    with pytest.raises(HTTPException) as error:
        asyncio.run(auth.verify_bearer("token", 22))
    assert error.value.status_code == 403



@pytest.mark.parametrize("upstream_prefix", ["/research", ""])
@pytest.mark.parametrize("asset,content_type", [("app.css", "text/css"), ("app.js", "javascript"),
                                                ("diagram.js", "javascript"), ("strategy.css", "text/css")])
def test_static_mount_resolves_public_and_proxy_stripped_paths(embedded, upstream_prefix, asset, content_type):
    client, _, tmp = embedded
    response = client.get(f"{upstream_prefix}/static/{asset}")
    assert response.status_code == 200, response.text
    assert content_type in response.headers["content-type"]
    assert len(response.content) > 100
    assert not (tmp / "workspaces").exists()


def test_prefixed_page_does_not_duplicate_root_in_form_or_navigation(embedded):
    client, _, _ = embedded
    assert login(client).status_code == 200
    html = client.get("/research/").text
    assert "/research/research" not in html
    assert 'href="/research/static/app.css' in html
    assert 'src="/research/static/app.js' in html
    assert 'value="/research/' in html
