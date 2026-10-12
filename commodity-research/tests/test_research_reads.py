import base64
from datetime import datetime
import hashlib
import hmac
import json
from pathlib import Path
import sys
import time

from fastapi.testclient import TestClient
import pytest

from tin.config import settings, SHANGHAI
from tin.integration.read_api import ReadRequest, read_research
from tin.models import DailyBrief, Researcher
from tin.jobs.seed import seed_researcher
from tin.web.app import app


def test_series_concrete_contract_has_observation_evidence_and_actual_cutoff(loaded):
    result = read_research(loaded, ReadRequest(operation="series", series_id="SHFE.SN.2610.close",
                                              date="2026-10-12", limit=1))
    assert result["effective_date"].isoformat() == "2026-09-18"
    row = result["data"]["observations"][0]
    assert row["observation_id"] and row["source"] == "SHFE"
    assert row["as_of"].astimezone(SHANGHAI).date().isoformat() == "2026-09-18"
    assert result["data"]["unit"] == "元/吨"


def test_briefs_never_use_other_person_even_if_same_day(loaded):
    me = seed_researcher(loaded)
    loaded.add(DailyBrief(researcher_id=me.id, variety="SN", trade_date="2026-09-18",
                          generated_at=datetime.now(SHANGHAI), status="已采纳", summary="secret personal thesis"))
    loaded.commit()
    loaded.info.update(weknora_user_id="another-user", weknora_email="other@example.com", tenant_id=7)
    result = read_research(loaded, ReadRequest(operation="brief"))
    assert result["data"] is None and "尚无简报" in result["gap"]
    assert "secret personal thesis" not in json.dumps(result, default=str)


def test_read_operation_rejects_model_identity_and_mutation_parameters():
    for data in ({"operation": "brief", "tenant_id": 8}, {"operation": "brief", "user_id": "other"},
                 {"operation": "publish"}, {"operation": "series", "limit": 201}):
        with pytest.raises(ValueError):
            ReadRequest.model_validate(data)


def test_judgment_remains_readable_without_market_data(session):
    from tin.jobs.seed import seed_judgment
    row=seed_judgment(session)
    session.commit()
    result=read_research(session,ReadRequest(operation="judgment"))
    assert result["latest_trade_date"] is None
    assert result["data"]["id"] == row.id


def test_series_pagination_has_distinct_older_observations(loaded):
    first=read_research(loaded,ReadRequest(operation="series",series_id="SHFE.SN.warrant",limit=1))
    second=read_research(loaded,ReadRequest(operation="series",series_id="SHFE.SN.warrant",limit=1,offset=1))
    assert first["data"]["observations"][0]["observation_id"] != second["data"]["observations"][0]["observation_id"]
    assert second["data"]["offset"] == 1 and second["data"]["truncated"] is False


def signed_headers(body, key, identity, stamp=None):
    stamp = str(int(time.time()) if stamp is None else stamp)
    encoded = base64.urlsafe_b64encode(json.dumps(identity).encode()).decode().rstrip("=")
    message = "\n".join((stamp, "POST", "/api/tools/read", encoded, hashlib.sha256(body).hexdigest()))
    return {"Content-Type": "application/json", "X-Research-Identity": encoded,
            "X-Research-Timestamp": stamp,
            "X-Research-Signature": hmac.new(key.encode(), message.encode(), hashlib.sha256).hexdigest()}


def test_signed_reads_isolate_workspaces_and_reject_tampering(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, "weknora_api_url", "http://weknora.test")
    monkeypatch.setattr(settings, "data_dir", tmp_path / "data")
    monkeypatch.setattr(settings, "service_token_file", tmp_path / "token")
    key = "private-test-key-" * 4
    settings.service_token_file.write_text(key)
    identity = {"user_id": "reader-a", "email": "a@example.com", "display_name": "A",
                "tenant_id": 7, "role": "viewer"}
    body = b'{"operation":"indicators","limit":1}'
    with TestClient(app) as client:
        response = client.post("/api/tools/read", content=body, headers=signed_headers(body, key, identity))
        assert response.status_code == 200
        assert response.json()["total"] > 0
        assert (tmp_path / "data/workspaces/7/tin.db").exists()
        headers = signed_headers(body, key, identity)
        forged = dict(identity, tenant_id=8)
        headers["X-Research-Identity"] = base64.urlsafe_b64encode(json.dumps(forged).encode()).decode().rstrip("=")
        assert client.post("/api/tools/read", content=body, headers=headers).status_code == 401
        assert not (tmp_path / "data/workspaces/8/tin.db").exists()
        assert client.post("/api/tools/read", content=body,
                           headers=signed_headers(body, key, identity, int(time.time())-120)).status_code == 401
        assert client.post("/api/tools/read", content=b'{"operation":"brief"}',
                           headers=signed_headers(body, key, identity)).status_code == 401
        oversized=b"x" * 16385
        assert client.post("/api/tools/read",content=oversized,
                           headers=signed_headers(oversized,key,identity)).status_code == 413
