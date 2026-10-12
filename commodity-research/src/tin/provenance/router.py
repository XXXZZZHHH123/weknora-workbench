"""Workspace scoped source files, sheet inspection and confirmed imports."""
from dataclasses import asdict
from datetime import datetime, timezone
from pathlib import Path

from fastapi import APIRouter, File, HTTPException, Request, UploadFile, Query
from fastapi.responses import FileResponse
from pydantic import BaseModel, ConfigDict, Field
from sqlalchemy import select

from tin.db import SessionLocal
from tin.models import SourceArtifact, SourceBatch, Indicator, ImportPreview, ObservationSource
from tin.ownership import verified_actor
from tin.provenance.service import artifact_path, source_cells, preserve_blob, snapshot_workbook, start_batch, MAX_SOURCE_BYTES, verified_artifact_bytes
from tin.provenance import bridge

router = APIRouter()


def _artifact(session, source):
    row = session.get(SourceArtifact, source)
    if row is None:
        raise HTTPException(404, "当前空间没有此来源文件")
    return row


def _info(artifact):
    return {"id": artifact.id, "sha256": artifact.id, "filename": artifact.filename,
            "kind": artifact.kind, "size": artifact.size, "created_at": artifact.created_at,
            "source_url": artifact.source_url, "details": artifact.details}


@router.get("/files")
def files_page(request: Request, source: str | None = None):
    from tin.web.app import templates, _shell
    from tin.compute.engine import latest_trade_date
    with SessionLocal() as session:
        files = session.scalars(select(SourceArtifact).order_by(SourceArtifact.created_at.desc()).limit(100)).all()
        indicators = session.scalars(select(Indicator).where(Indicator.status == "可用").order_by(Indicator.series_id)).all()
        return templates.TemplateResponse(request, "sources.html", _shell(session, "files", latest_trade_date(session),
            sources=[_info(a) for a in files], selected_source=source,
            source_indicators=[{"series_id": i.series_id, "name": i.name, "unit": i.unit} for i in indicators]))


@router.get("/api/sources")
def list_sources(offset: int = Query(0, ge=0), limit: int = Query(100, ge=1, le=200)):
    with SessionLocal() as session:
        return {"sources": [_info(a) for a in session.scalars(select(SourceArtifact)
            .order_by(SourceArtifact.created_at.desc()).offset(offset).limit(limit))]}


@router.get("/api/sources/{source}/download")
def download_source(source: str):
    with SessionLocal() as session:
        artifact = _artifact(session, source)
        path = artifact_path(session, artifact)
        import hashlib
        if not path.is_file():
            raise HTTPException(409, "原件缺失，未提供替代文件")
        with path.open("rb") as stream:
            actual_hash = hashlib.file_digest(stream, "sha256").hexdigest()
        if actual_hash != artifact.id:
            raise HTTPException(409, "原件缺失或哈希不一致，未提供替代文件")
        return FileResponse(path, filename=artifact.filename, media_type=artifact.media_type,
                            headers={"ETag": artifact.id, "X-Source-Sha256": artifact.id})


@router.get("/api/sources/{source}")
def inspect_source(source: str, sheet: str | None = None, offset: int = Query(0, ge=0), limit: int = Query(200, ge=1, le=1000)):
    with SessionLocal() as session:
        artifact = _artifact(session, source)
        evidence = session.scalars(select(ObservationSource).where(ObservationSource.artifact_id == source).limit(200)).all()
        try:
            cells = source_cells(session, artifact, sheet=sheet, offset=offset, limit=limit)
        except (ValueError, OSError) as exc:
            raise HTTPException(409, str(exc)) from None
        return {**_info(artifact), **cells,
                "observations": [{"observation_id": r.observation_id, "locator": r.locator,
                                  "mapping_version": r.mapping_version, "transformation": r.transformation} for r in evidence]}


@router.post("/api/sources/upload")
async def upload_source(file: UploadFile = File(...)):
    data = await file.read(MAX_SOURCE_BYTES+1)
    if len(data) > MAX_SOURCE_BYTES:
        raise HTTPException(413, "文件超过 100 MiB，请分批导入")
    filename = Path(file.filename or "upload.xlsx").name
    if Path(filename).suffix.lower() not in {".xlsx", ".xls", ".xlsm", ".csv", ".tsv"}:
        raise HTTPException(400, "表格请上传Excel或CSV；文档请使用研报资料入口")
    with SessionLocal() as session:
        artifact = preserve_blob(session, data, filename)
        session.commit()  # Parse failure must not erase the original uploaded bytes.
        try:
            snapshot_workbook(session, artifact, filename=filename)
            session.commit()
            from tin.ingest import vendor_terminal as vendor
            detected = vendor.detect(data) if Path(filename).suffix.lower() in {".xlsx", ".xlsm"} else None
            if detected:
                summary = vendor.summarize(session, data)
                batch = start_batch(session, artifact, kind="vendor_preview", mapping={"variety": "SN"})
                session.commit()
                recognized = vendor.read_workbook(data)
                ids, conflicts = vendor.resolve_ids(session, recognized)
                registered = [session.get(Indicator, ids[s.code]) for s in recognized if not vendor.discontinued(s)]
                if not conflicts and registered and all(r is not None for r in registered) and not summary.get("frequency_mismatches"):
                    report = vendor.load(session, data, filename=filename, entered_by=verified_actor(session,"研究员"), register_new=False)
                    return {"mode": "vendor", "batch_id": batch.id, "artifact_id": artifact.id,
                            "summary": summary, "automatically_imported": True, "import_result": asdict(report)}
                return {"mode": "vendor", "batch_id": batch.id, "artifact_id": artifact.id, "summary": summary}
            from tin.ingest.excel_importer import build_preview
            preview = build_preview(session, data, filename, verified_actor(session, "研究员"), "SN")
            if preview["known_mapping"] and not preview["error_count"]:
                from tin.ingest.excel_importer import commit_preview
                result = commit_preview(session, preview["preview_id"], [r["row_key"] for r in preview["rows"]], {},
                    verified_actor(session,"研究员"), "已确认格式自动导入；原件及单元格已留存")
                return {"mode": "table", **preview, "automatically_imported": True, "import_result": result}
            return {"mode": "table", **preview}
        except Exception as exc:
            if isinstance(exc, (HTTPException, PermissionError)):
                raise
            raise HTTPException(400, f"原件已留存，但未写入观测：{exc}") from None


class TableCommit(BaseModel):
    model_config = ConfigDict(extra="forbid")
    preview_id: str
    selected_row_keys: list[str] = Field(default_factory=list)
    column_overrides: dict = Field(default_factory=dict)
    note: str = "文件导入"


@router.post("/api/sources/table/commit")
def commit_table(body: TableCommit):
    from tin.ingest.excel_importer import commit_preview
    with SessionLocal() as session:
        try:
            return commit_preview(session, body.preview_id, body.selected_row_keys, body.column_overrides,
                                  verified_actor(session, "研究员"), body.note)
        except ValueError as exc:
            raise HTTPException(400, str(exc)) from None


class VendorCommit(BaseModel):
    model_config = ConfigDict(extra="forbid")
    batch_id: str
    register_new: bool = False


def _own_batch(session, batch_id):
    row = session.get(SourceBatch, batch_id)
    if row is None:
        raise HTTPException(404, "批次不存在")
    if session.info.get("weknora_user_id") and row.actor != verified_actor(session, ""):
        raise PermissionError("导入批次不属于当前账号")
    return row


@router.post("/api/sources/vendor/commit")
def commit_vendor(body: VendorCommit):
    from tin.ingest.vendor_terminal import load
    with SessionLocal() as session:
        batch = _own_batch(session, body.batch_id)
        if batch.kind != "vendor_preview":
            raise HTTPException(400, "不是数据商工作簿批次")
        artifact = _artifact(session, batch.artifact_id)
        try:
            report = load(session, verified_artifact_bytes(session, artifact), filename=artifact.filename,
                          entered_by=verified_actor(session, "研究员"), register_new=body.register_new)
            batch.status, batch.result, batch.committed_at = "committed", asdict(report), datetime.now(timezone.utc)
            session.commit()
            return asdict(report)
        except ValueError as exc:
            raise HTTPException(400, str(exc)) from None


class BridgePreview(BaseModel):
    model_config = ConfigDict(extra="forbid")
    dataset_id: str = Field(min_length=1, max_length=128)
    series_id: str = Field(min_length=1, max_length=128)


@router.post("/api/sources/bridge/preview")
def bridge_preview(body: BridgePreview):
    with SessionLocal() as session:
        try:
            preview = bridge.preview_bridge(session, body.dataset_id, body.series_id)
            if preview["known_mapping"]:
                result = bridge.apply_bridge(session, preview["batch_id"])
                preview.update(import_result=result, automatically_imported=result.get("status")=="committed")
            return preview
        except (ValueError, httpx.HTTPError) as exc:
            raise HTTPException(400, str(exc)) from None


import httpx


class BridgeApply(BaseModel):
    model_config = ConfigDict(extra="forbid")
    batch_id: str
    mapping: dict | None = None
    allow_conflicts: bool = False


@router.post("/api/sources/bridge/commit")
def bridge_commit(body: BridgeApply):
    with SessionLocal() as session:
        _own_batch(session, body.batch_id)
        try:
            return bridge.apply_bridge(session, body.batch_id, body.mapping, allow_conflicts=body.allow_conflicts)
        except ValueError as exc:
            raise HTTPException(400, str(exc)) from None


@router.post("/api/sources/bridge/catalog")
def bridge_catalog(body: dict):
    if set(body) - {"operation", "arguments"} or body.get("operation") not in {"list_datasets", "list_series"}:
        raise HTTPException(400, "仅允许检索当前空间的结构化数据目录")
    try:
        return bridge.structured_request("POST", "/api/tools/read", body).json()
    except (ValueError, httpx.HTTPError) as exc:
        raise HTTPException(400, str(exc)) from None
