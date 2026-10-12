import hashlib
import io
import json
from datetime import date, datetime

from openpyxl import Workbook
import pytest
from sqlalchemy import select, func

from tin.config import settings, SHANGHAI
from tin.models import Observation, ObservationSource, SourceArtifact, SourceBatch
from tin.provenance import bridge
from tin.provenance.service import (preserve_blob, snapshot_workbook, source_cells,
    observation_sources, record_with_evidence, artifact_path)
from tin.ingest.excel_importer import build_preview, commit_preview
from tin.schemas.caliber import Caliber
from tin.schemas.observation import ObservationIn


@pytest.fixture(autouse=True)
def isolated_source_files(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, "data_dir", tmp_path)


def workbook(sheets):
    wb = Workbook(); wb.remove(wb.active)
    for name, rows in sheets.items():
        sheet=wb.create_sheet(name)
        for row in rows:sheet.append(row)
    stream=io.BytesIO();wb.save(stream)
    return stream.getvalue()


def observation(value=100):
    return ObservationIn(series_id="SMM.SN.spot.1", value=value,
        as_of=datetime(2026,9,18,11,30,tzinfo=SHANGHAI),
        caliber=Caliber(time_type="发布时点",price_type="均价",spot_source="SMM"),
        source="人工",entered_by="test",note="original source retained")


def test_sources_are_immutable_and_equal_values_keep_two_sources(session):
    first=preserve_blob(session,b"date,value\n2026-09-18,100\n","a.csv")
    second=preserve_blob(session,b"date,value,note\n2026-09-18,100,independent\n","b.csv")
    row, written=record_with_evidence(session,observation(),artifact=first,locator={"sheet":"CSV","value_cell":"B2"})
    same, added=record_with_evidence(session,observation(),artifact=second,locator={"sheet":"CSV","value_cell":"B2"})
    assert written and not added and row.id==same.id
    assert len(observation_sources(session,row.id))==2
    record_with_evidence(session,observation(),artifact=second,locator={"sheet":"CSV","value_cell":"B2"})
    assert len(observation_sources(session,row.id))==2
    assert all(s["url"].startswith('/platform/commodity-research?source=') for s in observation_sources(session,row.id))
    artifact_path(session,first).write_bytes(b"tampered")
    with pytest.raises(ValueError,match="不可变"):
        preserve_blob(session,b"date,value\n2026-09-18,100\n","a.csv")


def test_all_sheets_blank_cells_and_uncached_formula_are_preserved(session):
    blob=workbook({"A":[["date","value"],["2026-09-18",100]],
                   "B":[["date","formula","blank"],["2026-09-18","=1+2",None]]})
    artifact=preserve_blob(session,blob,"two.xlsx")
    snapshot_workbook(session,artifact)
    assert set(artifact.details["sheets"])=={"A","B"}
    cells=source_cells(session,artifact,sheet="B")["cells"]
    formula=next(c for c in cells if c["address"]=="B2")
    assert formula["formula"]=="=1+2" and formula["value"] is None and formula["status"]=="formula_uncached"
    assert next(c for c in cells if c["address"]=="C2")["status"]=="empty"
    assert artifact_path(session,artifact).read_bytes()==blob


def test_generic_all_sheets_import_and_repeated_upload_keep_provenance(session):
    blob=workbook({"one":[["日期","[SMM.SN.spot.1]"],["2026-09-18",100]],
                   "two":[["日期","[SMM.SN.spot.1]"],["2026-09-19",110]]})
    preview=build_preview(session,blob,"source.xlsx","test","SN")
    assert set(preview["sheets"])=={"one","two"}
    result=commit_preview(session,preview["preview_id"],[r["row_key"] for r in preview["rows"]],{},"test","test source")
    assert result["committed"]==2
    locators=session.scalars(select(ObservationSource)).all()
    assert {e.locator["sheet"] for e in locators}=={"one","two"}
    again=build_preview(session,blob,"source.xlsx","test","SN")
    assert again["known_mapping"]
    result=commit_preview(session,again["preview_id"],[r["row_key"] for r in again["rows"]],{},"test","test source")
    assert result["committed"]==0
    assert session.scalar(select(func.count()).select_from(ObservationSource))==2


def test_bad_numeric_cell_cannot_be_rebound_into_zero(session):
    blob=workbook({"S":[["日期","unmapped"],["2026-09-18","missing"]]})
    preview=build_preview(session,blob,"missing.xlsx","test","SN")
    result=commit_preview(session,preview["preview_id"],[r["row_key"] for r in preview["rows"]],
        {"c1":{"series_id":"SMM.SN.spot.1"}},"test","test source")
    assert result["committed"]==0 and result["stale_rows"]
    assert session.scalar(select(func.count()).select_from(Observation))==0


def frozen_export(blob, *, dataset="version-a", value=100, value_status="valid", date_value="2026-09-18", unit="元/吨"):
    return {"manifest":{"dataset_id":dataset,"sha256":hashlib.sha256(blob).hexdigest(),"source_file":"provider.xlsx"},
            "series":{"series_id":"external-price","sheet":"S","metric":"spot","unit":unit,"header_cells":["B1"]},
            "rows":[{"date":date_value,"value":value,"source_cell":"B2","date_cell":"A2","raw_value":value,
                     "formula":None,"value_status":value_status}],"total":1}


def fake_bridge(monkeypatch, exported, blob):
    monkeypatch.setattr(bridge,"full_export",lambda *_:exported)
    class Response:
        content=blob
    monkeypatch.setattr(bridge,"structured_request",lambda *_args,**_kw:Response())


MAPPING={"research_series_id":"SMM.SN.spot.1","source_unit":"元/吨","target_unit":"元/吨","factor":1,"note":"同一现货价格、单位和口径"}


def test_bridge_versions_share_original_but_not_snapshot_or_import_batch(session,monkeypatch):
    blob=workbook({"S":[["date","value"],["2026-09-18",100]]})
    first=frozen_export(blob);fake_bridge(monkeypatch,first,blob)
    a=bridge.preview_bridge(session,"version-a","external-price")
    assert bridge.apply_bridge(session,a["batch_id"],MAPPING)["written"]==1
    changed=frozen_export(blob,dataset="version-b",value=101);fake_bridge(monkeypatch,changed,blob)
    b=bridge.preview_bridge(session,"version-b","external-price")
    assert a["artifact_id"]==b["artifact_id"] and a["batch_id"]!=b["batch_id"]
    assert b["known_mapping"]
    result=bridge.apply_bridge(session,b["batch_id"])
    assert result["status"]=="committed" and result["written"]==1
    assert session.scalar(select(func.count()).select_from(Observation))==2
    latest=session.scalar(select(Observation).order_by(Observation.revision.desc()).limit(1))
    provenance=observation_sources(session,latest.id)[0]
    assert provenance["locator"]["dataset_id"]=="version-b"
    assert "bridge-v1" in provenance["mapping_version"]


def test_bridge_foreign_source_conflict_needs_confirmation_and_missing_not_zero(session,monkeypatch):
    old=preserve_blob(session,b"manual", "old.txt")
    record_with_evidence(session,observation(99),artifact=old,locator={"origin":"independent"})
    session.commit()
    blob=workbook({"S":[["date","value"],["2026-09-18",100]]})
    data=frozen_export(blob);fake_bridge(monkeypatch,data,blob)
    preview=bridge.preview_bridge(session,"version-a","external-price")
    result=bridge.apply_bridge(session,preview["batch_id"],MAPPING)
    assert result["status"]=="needs_confirmation" and result["written"]==0
    assert bridge.apply_bridge(session,preview["batch_id"],MAPPING,allow_conflicts=True)["written"]==1
    missing=frozen_export(blob,dataset="version-blank",value=None,value_status="formula_no_cache")
    fake_bridge(monkeypatch,missing,blob)
    preview=bridge.preview_bridge(session,"version-blank","external-price")
    result=bridge.apply_bridge(session,preview["batch_id"])
    assert result["written"]==0 and result["skipped"]
    assert session.scalar(select(func.count()).select_from(Observation))==2


def test_full_export_rejects_truncation_and_snapshot_version_mix(monkeypatch):
    class Response:
        def __init__(self,data):self.data=data
        def json(self):return self.data
    page={"manifest":{"dataset_id":"x"},"series":{"series_id":"s"},"rows":[{"value":1}],
          "total":2,"offset":0,"next_offset":None,"truncated":True}
    monkeypatch.setattr(bridge,"structured_request",lambda *_:Response(page))
    with pytest.raises(ValueError,match="未完整"):
        bridge.full_export("x","s")


def test_history_without_original_stays_without_evidence(session):
    from tin.ingest.record import record
    row=record(session,observation())
    assert observation_sources(session,row.id)==[]


def test_bridge_snapshot_tampering_is_rejected_without_writes(session,monkeypatch):
    blob=workbook({"S":[["date","value"],["2026-09-18",100]]})
    fake_bridge(monkeypatch,frozen_export(blob),blob)
    preview=bridge.preview_bridge(session,"version-a","external-price")
    batch=session.get(SourceBatch,preview["batch_id"])
    artifact=session.get(SourceArtifact,batch.mapping["external_snapshot_id"])
    original=artifact_path(session,artifact).read_bytes()
    artifact_path(session,artifact).write_bytes(original.replace(b'100',b'999'))
    with pytest.raises(ValueError,match="哈希"):
        bridge.apply_bridge(session,batch.id,MAPPING)
    assert session.scalar(select(func.count()).select_from(Observation))==0


def test_table_commit_rejects_tampered_original_and_snapshot_cells(session):
    blob=workbook({"S":[["date","[SMM.SN.spot.1]"],["2026-09-18",100]]})
    preview=build_preview(session,blob,"source.xlsx","test","SN")
    artifact=session.get(SourceArtifact,preview["artifact_id"])
    artifact_path(session,artifact).write_bytes(b"wrong")
    with pytest.raises(ValueError,match="哈希"):
        commit_preview(session,preview["preview_id"],[r["row_key"] for r in preview["rows"]],{},"test","test")
    with pytest.raises(ValueError,match="哈希"):
        source_cells(session,artifact)
    assert session.scalar(select(func.count()).select_from(Observation))==0


def test_source_http_upload_known_mapping_updates_without_second_review(session,monkeypatch):
    from contextlib import nullcontext
    from fastapi.testclient import TestClient
    import tin.provenance.router as source_router
    from tin.web.app import app
    monkeypatch.setattr(settings,"weknora_api_url",None)
    monkeypatch.setattr(settings,"refresh_scheduler_enabled",False)
    monkeypatch.setattr(source_router,"SessionLocal",lambda:nullcontext(session))
    monkeypatch.setattr("tin.web.app.SessionLocal",lambda:nullcontext(session))
    blob=workbook({"S":[["date","[SMM.SN.spot.1]"],["2026-09-18",100]]})
    updated=workbook({"S":[["date","[SMM.SN.spot.1]"],["2026-09-18",110]]})
    with TestClient(app) as client:
        result=client.post('/api/sources/upload',files={'file':('provider.xlsx',blob)}).json()
        assert result['mode']=='table' and not result['known_mapping']
        committed=client.post('/api/sources/table/commit',json={'preview_id':result['preview_id'],
            'selected_row_keys':[r['row_key'] for r in result['rows']],'note':'source verified'})
        assert committed.status_code==200 and committed.json()['committed']==1
        second=client.post('/api/sources/upload',files={'file':('provider.xlsx',updated)})
        assert second.status_code==200,second.text
        assert second.json()['automatically_imported'] and second.json()['import_result']['revisions']==1
        page=client.get('/files',params={'source':result['artifact_id']})
        assert page.status_code==200 and 'researchFetch(sourcePrefix+path' in page.text
        assert 'const selectedSource' in page.text
        details=client.get('/api/sources/'+result['artifact_id']).json()
        assert details['details']['sheets']['S']['rows']==2
        original=client.get('/api/sources/'+result['artifact_id']+'/download')
        assert original.status_code==200 and original.content==blob
        assert original.headers['X-Source-Sha256']==hashlib.sha256(blob).hexdigest()


def test_identical_original_with_distinct_filenames_keeps_both_receipts(session):
    blob=workbook({"S":[["date","[SMM.SN.spot.1]"],["2026-09-18",100]]})
    for filename in ('supplier-a.xlsx','supplier-b.xlsx'):
        preview=build_preview(session,blob,filename,'test','SN')
        commit_preview(session,preview['preview_id'],[r['row_key'] for r in preview['rows']],{},'test','test')
    assert session.scalar(select(func.count()).select_from(Observation))==1
    assert {r.locator['filename'] for r in session.scalars(select(ObservationSource))}=={'supplier-a.xlsx','supplier-b.xlsx'}


def test_bridge_dimension_or_date_semantics_change_needs_new_mapping(session,monkeypatch):
    blob=workbook({"S":[["date","value"],["2026-09-18",100]]})
    data=frozen_export(blob);data["series"].update(date_mode="date",dimensions={"region":"China"})
    fake_bridge(monkeypatch,data,blob)
    first=bridge.preview_bridge(session,"version-a","external-price")
    bridge.apply_bridge(session,first["batch_id"],MAPPING)
    changed=frozen_export(blob,dataset="version-world");changed["series"].update(date_mode="date",dimensions={"region":"World"})
    fake_bridge(monkeypatch,changed,blob)
    preview=bridge.preview_bridge(session,"version-world","external-price")
    assert preview["known_mapping"] is None
    with pytest.raises(ValueError,match="尚未确认"):
        bridge.apply_bridge(session,preview["batch_id"])
    undated=frozen_export(blob,dataset="undated",date_value=None);undated["series"]["date_mode"]="none"
    fake_bridge(monkeypatch,undated,blob)
    preview=bridge.preview_bridge(session,"undated","external-price")
    with pytest.raises(ValueError,match="没有明确日期"):
        bridge.apply_bridge(session,preview["batch_id"],MAPPING)


def test_equal_file_batches_are_per_actor_while_observation_evidence_is_shared(session,monkeypatch):
    blob=workbook({"S":[["date","value"],["2026-09-18",100]]})
    fake_bridge(monkeypatch,frozen_export(blob),blob)
    session.info.update(weknora_user_id="account-a",tenant_id=7)
    first=bridge.preview_bridge(session,"version-a","external-price")
    bridge.apply_bridge(session,first["batch_id"],MAPPING)
    session.info.update(weknora_user_id="account-b",tenant_id=7)
    second=bridge.preview_bridge(session,"version-a","external-price")
    assert first["artifact_id"]==second["artifact_id"] and first["batch_id"]!=second["batch_id"]
    from tin.provenance.router import _own_batch
    assert _own_batch(session,second["batch_id"]).actor=="account-b"
    with pytest.raises(PermissionError):
        _own_batch(session,first["batch_id"])
    assert bridge.apply_bridge(session,second["batch_id"])["written"]==0
    assert session.scalar(select(func.count()).select_from(Observation))==1
    assert session.scalar(select(func.count()).select_from(ObservationSource))==1


def test_generic_unit_conversion_is_saved_and_caliber_change_needs_review(session):
    blob=workbook({"S":[["date","[SMM.SN.spot.1]"],["2026-09-18",40]]})
    first=build_preview(session,blob,"source.xlsx","test","SN")
    result=commit_preview(session,first["preview_id"],[r["row_key"] for r in first["rows"]],
        {"c1":{"series_id":"SMM.SN.spot.1","source_unit":"万元/吨","factor":10000}},"test","单位已核对")
    assert result["committed"]==1
    assert session.scalar(select(Observation.value))==400000
    again=build_preview(session,blob,"source.xlsx","test","SN")
    assert again["known_mapping"] and again["rows"][0]["value"]==400000
    from tin.models import Indicator
    indicator=session.get(Indicator,"SMM.SN.spot.1")
    indicator.caliber={**indicator.caliber,"note":"a changed description"}
    session.commit()
    changed=build_preview(session,blob,"source.xlsx","test","SN")
    assert not changed["known_mapping"]
