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


def test_manual_weekend_series_is_readable_without_exchange_calendar(session):
    from tin.ingest.record import record
    from tin.schemas.observation import ObservationIn
    from tin.schemas.caliber import Caliber
    row=record(session,ObservationIn(series_id='SMM.SN.spot.1',value=123456,
        as_of=datetime(2026,10,11,0,tzinfo=SHANGHAI),caliber=Caliber(time_type='发布时点',price_type='均价',spot_source='SMM'),
        source='Excel',entered_by='test-user',note='original workbook'))
    session.commit()
    result=read_research(session,ReadRequest(operation='series',series_id='SMM.SN.spot.1'))
    assert result['latest_trade_date'] is None
    assert result['effective_date'].isoformat()=='2026-10-11'
    assert result['data']['observations'][0]['value']==123456
    assert result['data']['observations'][0]['sources']==[]


def test_summary_returns_exact_original_evidence_for_fact_and_derived_inputs(loaded,tmp_path,monkeypatch):
    from tin.provenance.service import preserve_blob, record_with_evidence
    from tin.schemas.caliber import Caliber
    from tin.schemas.observation import ObservationIn
    from tin.compute.engine import compute_day
    monkeypatch.setattr(settings,'data_dir',tmp_path)
    artifact=preserve_blob(loaded,b'date,value\n2026-09-18,410000\n','spot.csv')
    record_with_evidence(loaded,ObservationIn(series_id='SMM.SN.spot.1',value=410000,
        as_of=datetime(2026,9,18,11,30,tzinfo=SHANGHAI),caliber=Caliber(time_type='发布时点',price_type='均价',spot_source='SMM'),
        source='Excel',entered_by='tester',note='fixture'),artifact=artifact,
        locator={'sheet':'CSV','value_cell':'B2','date_cell':'A2'},mapping_version='confirmed-v1')
    loaded.commit();compute_day(loaded,datetime(2026,9,18).date())
    result=read_research(loaded,ReadRequest(operation='summary'))['data']
    point=next(p for p in result['observations'] if p['series_id']=='SMM.SN.spot.1')
    assert point['sources'][0]['artifact_id']==artifact.id
    assert point['sources'][0]['locator']['value_cell']=='B2'
    basis=next(d for d in result['derived'] if d['formula_id']=='BASIS')
    spot=next(p for p in basis['inputs'] if p['series_id']=='SMM.SN.spot.1')
    assert spot['sources'][0]['locator']['date_cell']=='A2'


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


def test_latest_brief_is_found_when_market_data_has_advanced(loaded):
    from tin.models import TradingDay
    me=seed_researcher(loaded)
    loaded.add(DailyBrief(researcher_id=me.id,variety="SN",trade_date="2026-09-18",
                         generated_at=datetime.now(SHANGHAI),status="草稿"))
    loaded.add(TradingDay(trade_date="2026-09-21",year=2026,year_seq=175))
    loaded.commit()
    result=read_research(loaded,ReadRequest(operation="brief"))
    assert result["latest_trade_date"].isoformat() == "2026-09-21"
    assert result["effective_date"].isoformat() == "2026-09-18"
    assert result["data"]["status"] == "草稿"
    missing=read_research(loaded,ReadRequest(operation="brief",date="2026-09-19"))
    assert missing["data"] is None and missing["effective_date"].isoformat() == "2026-09-19"


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
