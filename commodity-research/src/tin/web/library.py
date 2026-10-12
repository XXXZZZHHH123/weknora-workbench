"""One immutable upload, independently recoverable knowledge/data/research uses."""
from dataclasses import asdict
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path, PurePosixPath
import threading
from typing import Literal
from urllib.parse import quote, urlsplit
import uuid

import httpx
from fastapi import APIRouter, BackgroundTasks, File, HTTPException, Query, Request, UploadFile
from fastapi.responses import Response
from pydantic import BaseModel, ConfigDict, Field
from sqlalchemy import or_, select
from starlette.concurrency import run_in_threadpool

from tin.config import settings
from tin.db import SessionLocal
from tin.integration.identity import current_identity
from tin.models import ImportPreview, Indicator, LibraryOperation, SourceArtifact
from tin.ownership import verified_actor
from tin.provenance.service import MAX_SOURCE_BYTES, preserve_blob, research_visible, stable_hash, verified_artifact_bytes

router = APIRouter(prefix="/api/library", tags=["library"])
TABLES = {".xlsx", ".xlsm", ".xls", ".csv", ".tsv"}
DOCUMENTS = {".pdf", ".docx", ".doc", ".txt", ".md", ".html", ".htm", ".rtf", ".odt",
             ".ppt", ".pptx", ".json", ".xml", ".epub", ".png", ".jpg", ".jpeg", ".webp",
             ".gif", ".bmp", ".tiff", ".tif", ".mp3", ".wav", ".m4a", ".flac", ".ogg",
             ".markdown", ".mhtml", ".xmind"}
_active = set()
_active_lock = threading.Lock()


def now():
    return datetime.now(timezone.utc)


def actor():
    return verified_actor_from_identity()


def verified_actor_from_identity():
    identity = current_identity.get()
    if identity is None:
        raise HTTPException(401, "请从 WeKnora 打开资料中心")
    return identity.user_id


def _filename(artifact):
    receipts = artifact.details.get("library_receipts", [])
    return next((row["filename"] for row in reversed(receipts) if row.get("actor") == actor()), artifact.filename)


def _capabilities(filename):
    suffix = Path(filename).suffix.lower()
    result = {"knowledge": suffix in DOCUMENTS | TABLES, "structured": suffix == ".xlsx", "research": suffix in TABLES | DOCUMENTS}
    if suffix in TABLES and suffix != ".xlsx":
        result["structured_reason"] = "结构化目前仅支持原生 .xlsx；此原件可直接接入研究指标，无需重新上传或转换格式。"
    return result


def _info(artifact, operations=()):
    filename = _filename(artifact)
    return {"id": artifact.id, "sha256": artifact.id, "filename": filename, "size": artifact.size,
            "kind": artifact.kind, "created_at": artifact.created_at,
            "capabilities": _capabilities(filename), "shared_research": research_visible(artifact),
            "shared_structured": artifact.details.get("shared_structured") is True,
            "operations": [_operation_info(row, summary=True) for row in operations]}


def _operation_info(row, *, summary=False):
    result = {"id": row.id, "asset_id": row.asset_id, "purpose": row.purpose, "status": row.status,
              "error": row.error, "created_at": row.created_at, "updated_at": row.updated_at,
              "attempts": row.attempts, "remote_ids": row.remote_ids,
              "parse_status": row.result.get("parse_status"), "status_checked": row.result.get("status_checked", True),
              "status_refresh_error": row.result.get("status_refresh_error")}
    if not summary:
        result.update(result=row.result, request=row.request)
    return result


def _token(request):
    # This value lives only in the authenticated request/background invocation.
    # Neither request headers nor credentials are persisted in operation JSON.
    supplied = request.headers.get("authorization", "")
    if supplied.startswith("Bearer "):
        return supplied
    from tin.web.materials import headers
    return headers(request)["Authorization"]


async def _remote(request, provider, method, path, **kwargs):
    base = (settings.weknora_api_url if provider == "knowledge" else settings.structured_data_url) or ""
    parsed = urlsplit(base)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc or parsed.query or parsed.fragment or parsed.username:
        raise HTTPException(503, "资料服务地址未正确配置")
    if provider == "structured" and parsed.path not in {"", "/"}:
        raise HTTPException(503, "结构化服务地址必须为固定服务地址")
    identity = current_identity.get()
    headers = {"Authorization": _token(request), "X-Tenant-ID": str(identity.tenant_id)}
    if method not in {"GET", "HEAD"}:
        # The browser write already passed ResearchAuthMiddleware's strict
        # same-origin check. The internal structured request has a different
        # Host and must use its configured service origin, never a caller's
        # arbitrary Origin header.
        headers["Origin"] = f"{parsed.scheme}://{parsed.netloc}" if provider == "structured" else request.headers.get("origin", "")
    prefix = "/api/v1" if provider == "knowledge" else ""
    try:
        async with httpx.AsyncClient(timeout=120, follow_redirects=False, trust_env=False) as client:
            response = await client.request(method, base.rstrip("/") + prefix + path, headers=headers, **kwargs)
    except httpx.HTTPError:
        raise HTTPException(503, "资料服务暂时无法连接；原件已保留，可以重试") from None
    return response


def _json_response(response, *, envelope=False):
    if response.status_code >= 300:
        message = "目标服务拒绝操作；请检查目标知识库权限、格式或服务状态，原件已保留"
        raise HTTPException(response.status_code if 400 <= response.status_code < 500 else 503, message)
    doc = response.json()
    return doc if envelope else doc.get("data", doc)


async def _knowledge(request, method, path, **kwargs):
    return _json_response(await _remote(request, "knowledge", method, path, **kwargs))


async def _structured(request, method, path, **kwargs):
    return _json_response(await _remote(request, "structured", method, path, **kwargs), envelope=True)


async def _can_read(request, artifact, cache=None):
    if not artifact.details.get("library_restricted") or artifact.details.get("shared_research") or artifact.details.get("shared_structured"):
        return True
    if actor() in artifact.details.get("library_uploaders", []):
        return True
    # Only actual KB-read access can expose a KB-only original to another actor.
    cache = {} if cache is None else cache
    for binding in artifact.details.get("knowledge_bindings", []):
        knowledge_id = binding.get("knowledge_id")
        if not knowledge_id:
            continue
        if knowledge_id not in cache:
            try:
                cache[knowledge_id] = await _knowledge(request, "GET", "/knowledge/" + quote(knowledge_id, safe=""))
            except HTTPException as exc:
                if exc.status_code not in {403, 404}:
                    raise
                cache[knowledge_id] = None
        if cache[knowledge_id] is not None:
            return True
    return False


async def _asset(request, sha):
    with SessionLocal() as session:
        artifact = session.get(SourceArtifact, sha)
        if artifact is None:
            raise HTTPException(404, "当前空间没有此文件")
    if not await _can_read(request, artifact):
        raise HTTPException(404, "当前空间没有可访问的此文件")
    return artifact


async def _require_original_access(request, artifact):
    if (not artifact.details.get("library_restricted") or artifact.details.get("shared_research")
            or artifact.details.get("shared_structured") or actor() in artifact.details.get("library_uploaders", [])):
        return
    for binding in artifact.details.get("knowledge_bindings", []):
        response = await _remote(request, "knowledge", "GET", "/knowledge/" + quote(binding["knowledge_id"], safe="") + "/download")
        if response.status_code == 200 and hashlib.sha256(response.content).hexdigest() == artifact.id:
            return
    raise HTTPException(403, "原知识库未授予原件下载权限，不能把该原件接入其他用途")


def _own_operation(operation_id):
    with SessionLocal() as session:
        row = session.get(LibraryOperation, operation_id)
        if row is None or row.actor != actor():
            raise HTTPException(404, "当前账号没有此处理任务")
        return row


def _save_operation(operation_id, **changes):
    with SessionLocal() as session:
        row = session.get(LibraryOperation, operation_id)
        if row is None or row.actor != actor():
            raise HTTPException(404, "当前账号没有此处理任务")
        for name, value in changes.items():
            setattr(row, name, value)
        row.updated_at = now()
        session.commit()
        return row


def _shared(sha, purpose):
    with SessionLocal() as session:
        artifact = session.get(SourceArtifact, sha)
        artifact.details = {**artifact.details, "shared_" + purpose: True}
        session.commit()


@router.post("/upload")
async def upload(file: UploadFile = File(...)):
    filename = Path((file.filename or "").replace("\\", "/")).name
    if not filename or Path(filename).suffix.lower() not in TABLES | DOCUMENTS:
        raise HTTPException(400, "支持 Excel、CSV/TSV、PDF、Word、文本和图片")
    chunks, size = [], 0
    while chunk := await file.read(1024 * 1024):
        size += len(chunk)
        if size > MAX_SOURCE_BYTES:
            raise HTTPException(413, "文件超过 100 MiB")
        chunks.append(chunk)
    if not size:
        raise HTTPException(400, "文件为空")
    data = b"".join(chunks)
    sha = hashlib.sha256(data).hexdigest()
    with SessionLocal() as session:
        existing = session.get(SourceArtifact, sha)
        # A repeated upload proves this caller possesses these exact bytes;
        # reading another caller's metadata by SHA alone never grants access.
        artifact = preserve_blob(session, data, filename, kind="library")
        details = dict(artifact.details)
        if existing is None:
            details.update(library_restricted=True, shared_research=False, shared_structured=False)
        uploaders = list(details.get("library_uploaders", []))
        if actor() not in uploaders:
            uploaders.append(actor())
        receipts = list(details.get("library_receipts", []))
        receipt = {"actor": actor(), "filename": filename}
        if receipt not in receipts:
            receipts.append(receipt)
        artifact.details = {**details, "library_uploaders": uploaders, "library_receipts": receipts}
        session.commit()
        return {"asset": _info(artifact), "already_present": existing is not None}


@router.get("")
async def list_assets(request: Request, source: Literal["manual", "automatic", "all"] = "manual",
                      offset: int = Query(0, ge=0), limit: int = Query(25, ge=1, le=100),
                      search: str = Query("", max_length=200), kind: Literal["all", "table", "document"] = "all"):
    with SessionLocal() as session:
        statement = select(SourceArtifact).where(SourceArtifact.kind != "structured_snapshot")
        if source == "manual":
            statement = statement.where(or_(SourceArtifact.kind != "automatic",
                SourceArtifact.details["library_receipts"].as_string().is_not(None)))
        elif source == "automatic":
            statement = statement.where(SourceArtifact.kind == "automatic")
        artifacts = session.scalars(statement.order_by(SourceArtifact.created_at.desc(), SourceArtifact.id)).all()
        operations = session.scalars(select(LibraryOperation).where(LibraryOperation.actor == actor())
            .order_by(LibraryOperation.updated_at.desc())).all()
    visible, permissions = [], {}
    for artifact in artifacts:
        if await _can_read(request, artifact, permissions):
            visible.append(artifact)
    needle = search.casefold()
    visible = [artifact for artifact in visible
               if (not needle or needle in _filename(artifact).casefold())
               and (kind == "all" or (Path(_filename(artifact)).suffix.lower() in TABLES) == (kind == "table"))]
    page = visible[offset:offset + limit]
    by_asset = {}
    for operation in operations:
        by_asset.setdefault(operation.asset_id, []).append(operation)
    for artifact in page:
        recent = by_asset.get(artifact.id, [])[:10]
        by_asset[artifact.id] = [await _status(request, row) if row.status in {"pending", "running", "needs_mapping"} else row
                                for row in recent]
    return {"assets": [_info(a, by_asset.get(a.id, [])) for a in page], "total": len(visible),
            "offset": offset, "next_offset": offset + limit if offset + limit < len(visible) else None}


@router.get("/legacy")
async def legacy(request: Request, kb_id: str | None = None, page: int = Query(1, ge=1), page_size: int = Query(25, ge=1, le=100)):
    # Do not hide documents imported before the center existed. The original
    # endpoints remain the authority for per-KB and cross-space shared access.
    libraries, documents, total, datasets, errors = [], [], 0, [], {}
    try:
        libraries = await _knowledge(request, "GET", "/knowledge-bases")
        libraries = libraries if isinstance(libraries, list) else libraries.get("items", libraries.get("knowledge_bases", []))
        if kb_id:
            response = await _remote(request, "knowledge", "GET", "/knowledge-bases/" + quote(kb_id, safe="") + "/knowledge",
                params={"page": page, "page_size": page_size})
            doc = _json_response(response, envelope=True)
            documents, total = doc.get("data", []), doc.get("total", 0)
    except HTTPException as exc:
        errors["knowledge"] = str(exc.detail)
    try:
        datasets = (await _structured(request, "GET", "/api/datasets")).get("datasets", [])
    except HTTPException as exc:
        errors["structured"] = str(exc.detail)
    return {"libraries": libraries, "documents": documents, "total": total, "page": page, "page_size": page_size,
            "datasets": datasets, "errors": errors}


@router.get("/indicators")
def indicators():
    with SessionLocal() as session:
        return {"indicators": [{"series_id": row.series_id, "name": row.name, "unit": row.unit,
                "caliber": row.caliber, "frequency": row.frequency, "fetch_mode": row.fetch_mode,
                "default_entry_time": row.default_entry_time.isoformat() if row.default_entry_time else None}
                for row in session.scalars(select(Indicator).where(Indicator.status == "可用").order_by(Indicator.series_id))]}


class CatalogRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")
    operation: Literal["list_datasets", "list_series"]
    arguments: dict = Field(default_factory=dict)


@router.post("/catalog")
async def catalog(body: CatalogRequest):
    from tin.provenance.bridge import structured_request
    try:
        return await run_in_threadpool(lambda: structured_request("POST", "/api/tools/read", body.model_dump()).json())
    except (ValueError, httpx.HTTPError) as exc:
        raise HTTPException(400, str(exc)) from None


class ProcessRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")
    purpose: Literal["knowledge", "structured", "research"]
    operation_id: str | None = None
    kb_id: str | None = Field(default=None, max_length=128)
    folder: str = Field(default="", max_length=1024)
    tag_ids: list[str] = Field(default_factory=list, max_length=100)
    process_config: dict | None = None
    enable_multimodel: bool | None = None
    dataset_id: str | None = Field(default=None, max_length=128)
    series_id: str | None = Field(default=None, max_length=128)
    mapping: dict | None = None
    allow_conflicts: bool = False
    selected_row_keys: list[str] | None = None
    column_overrides: dict = Field(default_factory=dict)
    register_new: bool = False
    confirm: bool = False
    note: str = Field(default="统一资料中心导入；原件及单元格已留存", max_length=1024)


def _validate_purpose(body, artifact):
    identity = current_identity.get()
    if body.purpose != "knowledge" and not identity.can_write:
        raise HTTPException(403, "当前空间角色不能发布结构化数据或研究来源")
    if not _capabilities(_filename(artifact))[body.purpose]:
        raise HTTPException(400, _capabilities(_filename(artifact)).get("structured_reason", "此文件不支持指定用途"))
    if body.purpose == "knowledge" and not body.kb_id:
        raise HTTPException(400, "请选择本次文件要进入的知识库")
    if body.folder and (body.folder.startswith(("/", "\\")) or ".." in PurePosixPath(body.folder.replace("\\", "/")).parts):
        raise HTTPException(400, "知识库文件夹路径无效")
    if bool(body.dataset_id) != bool(body.series_id):
        raise HTTPException(400, "桥接需要同时提供真实 dataset_id 和 series_id")


def _schedule(operation, request, background):
    key = (current_identity.get().tenant_id, operation.id)
    with _active_lock:
        if key in _active:
            return
        _active.add(key)
    background.add_task(_execute, operation.id, request, current_identity.get(), key)


@router.post("/{sha}/process")
async def process(sha: str, body: ProcessRequest, request: Request, background: BackgroundTasks):
    artifact = await _asset(request, sha)
    _validate_purpose(body, artifact)
    await _require_original_access(request, artifact)
    if body.operation_id:
        operation = _own_operation(body.operation_id)
        if operation.asset_id != sha or operation.purpose != body.purpose:
            raise HTTPException(400, "确认任务与文件用途不符")
        for field in ("kb_id", "dataset_id", "series_id"):
            if operation.request.get(field) != getattr(body, field):
                raise HTTPException(400, "确认时不能切换知识库或源字段；请为新用途建立独立任务")
    else:
        config = body.model_dump(exclude={"operation_id"})
        key = stable_hash({"asset": sha, "actor": actor(), "request": config})
        with SessionLocal() as session:
            operation = session.scalar(select(LibraryOperation).where(LibraryOperation.idempotency_key == key))
            if operation is None:
                values = dict(id=uuid.uuid4().hex, asset_id=sha, actor=actor(), purpose=body.purpose,
                    status="uploaded", idempotency_key=key, request=config, remote_ids={}, result={}, attempts=0,
                    created_at=now(), updated_at=now())
                dialect = session.get_bind().dialect.name
                if dialect in {"sqlite", "postgresql"}:
                    if dialect == "sqlite":
                        from sqlalchemy.dialects.sqlite import insert
                    else:
                        from sqlalchemy.dialects.postgresql import insert
                    session.execute(insert(LibraryOperation).values(**values).on_conflict_do_nothing(index_elements=[LibraryOperation.idempotency_key]))
                    operation = session.scalar(select(LibraryOperation).where(LibraryOperation.idempotency_key == key))
                else:
                    operation = LibraryOperation(**values)
                    session.add(operation)
                session.commit()
    operation = await _refresh(request, operation)
    if operation.status in {"uploaded", "needs_mapping", "failed", "interrupted"}:
        config = body.model_dump(exclude={"operation_id"})
        operation = _save_operation(operation.id, request=config, status="running", error=None, attempts=operation.attempts + 1)
        _schedule(operation, request, background)
    return {"operation": _operation_info(operation), "asset": _info(artifact)}


@router.get("/operations/{operation_id}")
async def operation_status(operation_id: str, request: Request):
    operation = _own_operation(operation_id)
    await _asset(request, operation.asset_id)
    return {"operation": _operation_info(await _status(request, operation))}


@router.post("/operations/{operation_id}/retry")
async def retry(operation_id: str, request: Request, background: BackgroundTasks):
    operation = _own_operation(operation_id)
    artifact = await _asset(request, operation.asset_id)
    body = ProcessRequest.model_validate(operation.request)
    _validate_purpose(body, artifact)
    await _require_original_access(request, artifact)
    operation = await _refresh(request, operation)
    if operation.status in {"failed", "interrupted", "needs_mapping", "uploaded"}:
        changes = dict(status="running", error=None, attempts=operation.attempts + 1)
        if operation.purpose == "research" and operation.result.get("mode") == "table":
            # Repeating old confirmation is unsafe after expiry or a concurrent
            # write. Regenerate from retained bytes, then require a fresh review.
            config = {**operation.request, "confirm": False, "selected_row_keys": None}
            changes.update(request=config, result={**operation.result, "refresh_preview": True})
        operation = _save_operation(operation.id, **changes)
        _schedule(operation, request, background)
    return {"operation": _operation_info(operation)}


def _structured_result(operation, result, import_doc=None):
    remote = {**operation.remote_ids, **{key: result[key] for key in ("import_id", "dataset_id") if result.get(key)}}
    summary = result.get("summary", (import_doc or {}).get("summary", {}))
    status = "needs_mapping" if summary.get("needs_review", 0) else "completed"
    if not remote.get("dataset_id") and not summary.get("needs_review", 0):
        status = "failed"
    payload = {**result, "mode": "structured", "summary": summary}
    if import_doc:
        payload["review"] = {key: value for key, value in import_doc.items() if key != "source_path"}
    return _save_operation(operation.id, status=status, remote_ids=remote, result=payload,
        error="没有可发布的数据，请检查工作表范围和格式" if status == "failed" else None)


async def _recover_structured(request, operation):
    listing = await _structured(request, "GET", "/api/imports")
    matches = []
    for summary in listing.get("imports", []):
        doc = await _structured(request, "GET", "/api/imports/" + quote(summary["import_id"], safe=""))
        if doc.get("sha256") == operation.asset_id:
            matches.append(doc)
    if matches:
        doc = max(matches, key=lambda row: row.get("updated_at", ""))
        if doc.get("publish_status") != "completed":
            return _save_operation(operation.id, status="interrupted", remote_ids={**operation.remote_ids, "import_id": doc["import_id"]},
                error="原件导入记录已恢复，但发布未完成；可重试继续处理")
        return _structured_result(operation, {key: doc.get(key) for key in ("import_id", "dataset_id", "summary")}, doc)
    return _save_operation(operation.id, status="interrupted", error="后台任务不存在且没有找到持久导入记录；原件仍在，可重试")


async def _refresh(request, operation):
    identity = current_identity.get()
    with _active_lock:
        active = (identity.tenant_id, operation.id) in _active
    if operation.purpose == "knowledge" and operation.remote_ids.get("knowledge_id"):
        response = await _remote(request, "knowledge", "GET", "/knowledge/" + quote(operation.remote_ids["knowledge_id"], safe=""))
        if response.status_code in {403, 404}:
            return _save_operation(operation.id, status="failed", result={**operation.result, "remote_missing": response.status_code == 404},
                error="知识库条目已不存在，原件仍在，可重试创建" if response.status_code == 404 else "当前账号已无该知识库权限，原件仍在")
        knowledge = _json_response(response)
        parse_status = knowledge.get("parse_status", "pending")
        status = "completed" if parse_status == "completed" else "failed" if parse_status in {"failed", "cancelled", "deleting"} else "pending"
        message = {"failed": "知识库解析失败；原件仍在，可重试解析", "cancelled": "知识库解析已取消；原件仍在，可重试解析",
                   "deleting": "知识库条目正在删除；原件仍在"}.get(parse_status)
        return _save_operation(operation.id, status=status, result={**operation.result, "knowledge": knowledge, "parse_status": parse_status,
            "remote_missing": False}, error=message)
    if operation.purpose == "structured" and operation.status in {"needs_mapping", "completed"} and operation.remote_ids.get("import_id") and not active:
        doc = await _structured(request, "GET", "/api/imports/" + quote(operation.remote_ids["import_id"], safe=""))
        if doc.get("sha256") != operation.asset_id:
            raise HTTPException(409, "结构化导入记录的SHA256与原件不符")
        if doc.get("publish_status") != "completed":
            return _save_operation(operation.id, status="interrupted", error="结构化映射尚未发布完成，可重试恢复")
        return _structured_result(operation, {key: doc.get(key) for key in ("import_id", "dataset_id", "summary")}, doc)
    if operation.purpose == "structured" and operation.status == "running" and not active:
        job = operation.remote_ids.get("job_id")
        if job:
            response = await _remote(request, "structured", "GET", "/api/jobs/" + quote(job, safe=""))
            if response.status_code == 404:
                return await _recover_structured(request, operation)
            doc = _json_response(response, envelope=True)
            if doc.get("status") == "completed":
                return _structured_result(operation, doc.get("result", {}))
            if doc.get("status") == "failed":
                return _save_operation(operation.id, status="failed", error=doc.get("error", "结构化导入失败"))
            return operation
        return await _recover_structured(request, operation)
    if operation.status == "running" and not active:
        return _save_operation(operation.id, status="interrupted", error="处理服务已重启，原件仍在；请重试恢复")
    return operation


async def _status(request, operation):
    try:
        return await _refresh(request, operation)
    except (HTTPException, ValueError, httpx.HTTPError) as exc:
        # A temporary provider outage does not erase the file list, disable its
        # retained original, or masquerade as a successfully reconciled state.
        operation.result = {**operation.result, "status_checked": False,
            "status_refresh_error": str(exc.detail) if isinstance(exc, HTTPException) else "暂无法核对目标服务状态，展示最近已知结果"}
        return operation


async def _execute(operation_id, request, identity, key):
    marker = current_identity.set(identity)
    try:
        operation = _own_operation(operation_id)
        body = ProcessRequest.model_validate(operation.request)
        artifact = await _asset(request, operation.asset_id)
        _validate_purpose(body, artifact)
        with SessionLocal() as session:
            data = verified_artifact_bytes(session, session.get(SourceArtifact, artifact.id))
        if body.purpose == "knowledge":
            await _process_knowledge(request, operation, artifact, data, body)
        elif body.purpose == "structured":
            await _process_structured(request, operation, artifact, data, body)
        else:
            _shared(artifact.id, "research")  # Explicit purpose selection grants workspace sharing.
            result = await run_in_threadpool(_process_research, operation, artifact, data, body)
            status = "failed" if result.get("requires_repreview") else "needs_mapping" if result.get("needs_mapping") else "completed"
            _save_operation(operation.id, status=status, result=result,
                error="原预览已无法安全应用；原件仍在，请恢复处理生成新预览后重新核对" if result.get("requires_repreview") else None,
                remote_ids={**operation.remote_ids, **{key: result[key] for key in ("preview_id", "batch_id", "dataset_id") if result.get(key)}})
    except Exception as exc:
        # Upstream response bodies and bearer headers never become error logs.
        message = str(exc.detail) if isinstance(exc, HTTPException) else str(exc)
        _save_operation(operation_id, status="failed", error=message[:2000])
    finally:
        current_identity.reset(marker)
        with _active_lock:
            _active.discard(key)


async def _process_knowledge(request, operation, artifact, data, body):
    # Creation/reparse uses the caller's JWT and the existing authoritative KB
    # write guards, including org-shared KB grants. No guessed role allow-list.
    filename = _filename(artifact)
    kb_id = quote(body.kb_id, safe="")
    library = await _knowledge(request, "GET", "/knowledge-bases/" + kb_id)
    existing_id = operation.remote_ids.get("knowledge_id")
    if operation.result.get("remote_missing"):
        existing_id = None
    if existing_id:
        current = await _knowledge(request, "GET", "/knowledge/" + quote(existing_id, safe=""))
        if current.get("parse_status") in {"failed", "cancelled"}:
            await _knowledge(request, "POST", "/knowledge/" + quote(existing_id, safe="") + "/reparse")
            current = await _knowledge(request, "GET", "/knowledge/" + quote(existing_id, safe=""))
        knowledge = current
    else:
        form = {"metadata": json.dumps({"library_sha256": artifact.id, "source_sha256": artifact.id}),
                "fileName": str(PurePosixPath(body.folder.replace("\\", "/")) / filename) if body.folder else filename,
                "tag_ids": ",".join(body.tag_ids)}
        if body.process_config is not None:
            form["process_config"] = json.dumps(body.process_config, ensure_ascii=False)
        if body.enable_multimodel is not None:
            form["enable_multimodel"] = str(body.enable_multimodel).lower()
        response = await _remote(request, "knowledge", "POST", "/knowledge-bases/" + kb_id + "/knowledge/file",
            files={"file": (filename, data, artifact.media_type)}, data=form)
        if response.status_code == 409:
            doc = response.json()
            knowledge = doc.get("data", {})
            if doc.get("code") != "duplicate_file" or not knowledge.get("id"):
                raise HTTPException(409, "知识库重复响应无法核对，请在原知识库检查；原件已保留")
            # Existing KB items can lack center metadata. Compare actual original
            # bytes rather than pretending their historical MD5 is our SHA256.
            original = await _remote(request, "knowledge", "GET", "/knowledge/" + quote(knowledge["id"], safe="") + "/download")
            if original.status_code != 200 or hashlib.sha256(original.content).hexdigest() != artifact.id:
                raise HTTPException(409, "重复文件原件无法核验为本次SHA256版本；未关联错误文件")
        else:
            knowledge = _json_response(response)
    if not knowledge.get("id"):
        raise HTTPException(503, "知识库未返回文件身份，原件已保留，可重试")
    binding = {"knowledge_id": knowledge["id"], "kb_id": body.kb_id, "tenant_id": library.get("tenant_id")}
    with SessionLocal() as session:
        stored = session.get(SourceArtifact, artifact.id)
        bindings = list(stored.details.get("knowledge_bindings", []))
        if binding not in bindings:
            bindings.append(binding)
        stored.details = {**stored.details, "knowledge_bindings": bindings}
        session.commit()
    _save_operation(operation.id, remote_ids={"knowledge_id": knowledge["id"], "kb_id": body.kb_id},
                    result={"mode": "knowledge", "knowledge": knowledge, "parse_status": knowledge.get("parse_status", "pending")})
    await _refresh(request, _own_operation(operation.id))


async def _process_structured(request, operation, artifact, data, body):
    import_id = operation.remote_ids.get("import_id")
    if import_id and body.confirm:
        if not body.mapping:
            raise HTTPException(400, "请提供已核对的工作表映射")
        result = await _structured(request, "POST", "/api/imports/" + quote(import_id, safe="") + "/confirm",
            json={"mapping": body.mapping, "save_template": True})
    elif import_id:
        result = await _structured(request, "POST", "/api/imports/" + quote(import_id, safe="") + "/rescan")
        _shared(artifact.id, "structured")
        _structured_result(operation, result)
        return
    else:
        result = await _structured(request, "POST", "/api/import", files={"file": (_filename(artifact), data, artifact.media_type)})
    if not result.get("id"):
        raise HTTPException(503, "结构化服务未返回后台任务号；原件已保留，可重试")
    _shared(artifact.id, "structured")
    _save_operation(operation.id, status="running", remote_ids={**operation.remote_ids, "job_id": result["id"]},
                    result={"mode": "structured", "job_id": result["id"]})


def _process_research(operation, artifact, data, body):
    filename = _filename(artifact)
    if Path(filename).suffix.lower() in DOCUMENTS:
        return {"mode": "document_source", "artifact_id": artifact.id, "needs_mapping": False,
                "notice": "已明确选择作为空间共享研究来源；正文仍由指定知识库解析，不从文档猜测指标。"}
    with SessionLocal() as session:
        if body.dataset_id:
            from tin.provenance import bridge
            preview = bridge.preview_bridge(session, body.dataset_id, body.series_id)
            if preview["sha256"] != artifact.id:
                raise ValueError("所选结构化版本与本原件SHA256不符，拒绝混合来源")
            if body.confirm or preview.get("known_mapping"):
                result = bridge.apply_bridge(session, preview["batch_id"], body.mapping,
                    allow_conflicts=body.allow_conflicts)
                return {"mode": "bridge", **preview, "import_result": result,
                        "needs_mapping": result.get("status") != "committed"}
            return {"mode": "bridge", **preview, "needs_mapping": True}
        from tin.ingest import vendor_terminal as vendor
        if Path(filename).suffix.lower() in {".xlsx", ".xlsm"} and vendor.detect(data):
            summary = vendor.summarize(session, data)
            recognized = vendor.read_workbook(data)
            ids, conflicts = vendor.resolve_ids(session, recognized)
            registered = [session.get(Indicator, ids[item.code]) for item in recognized if not vendor.discontinued(item)]
            known = not conflicts and registered and all(row is not None for row in registered) and not summary.get("frequency_mismatches")
            if body.confirm or known:
                report = vendor.load(session, data, filename=filename, entered_by=verified_actor(session, "研究员"), register_new=body.register_new)
                return {"mode": "vendor", "summary": summary, "import_result": asdict(report),
                        "needs_mapping": bool(report.unmapped_columns or report.rejected)}
            return {"mode": "vendor", "summary": summary, "needs_mapping": True}
        from tin.ingest.excel_importer import build_preview, commit_preview, summarize
        if body.confirm:
            stored = session.get(ImportPreview, operation.result.get("preview_id")) if operation.result.get("preview_id") else None
            if stored is None or stored.expires_at <= now():
                raise ValueError("原预览不存在或已过期；原件仍在，请重试生成新预览后重新核对")
            if stored.actor != actor() or stored.payload.get("artifact_id") != artifact.id:
                raise PermissionError("预览不属于当前账号或原件")
            preview = {"preview_id": stored.id, "artifact_id": artifact.id, "sheets": stored.payload.get("sheets", []),
                "known_mapping": operation.result.get("known_mapping"), **summarize(stored.payload)}
        else:
            preview = build_preview(session, data, filename, verified_actor(session, "研究员"), "SN")
        automatic = (not operation.result.get("refresh_preview") and preview.get("known_mapping")
                     and not preview.get("error_count") and not preview.get("revision_count"))
        if body.confirm or automatic:
            selection = body.selected_row_keys if body.selected_row_keys is not None else [row["row_key"] for row in preview["rows"] if row["category"] != "revision"]
            result = commit_preview(session, preview["preview_id"], selection, body.column_overrides,
                verified_actor(session, "研究员"), body.note)
            return {"mode": "table", **preview, "import_result": result, "needs_mapping": bool(result.get("stale_rows")),
                    "requires_repreview": bool(result.get("stale_rows"))}
        return {"mode": "table", **preview, "needs_mapping": True}


@router.get("/{sha}/download")
async def download(sha: str, request: Request):
    artifact = await _asset(request, sha)
    # KB raw downloads are stricter than parsed-document reads. A second actor
    # uses the same original download permission checks as the existing KB API.
    private = artifact.details.get("library_restricted") and not artifact.details.get("shared_research") and not artifact.details.get("shared_structured")
    if private and actor() not in artifact.details.get("library_uploaders", []):
        allowed = False
        for binding in artifact.details.get("knowledge_bindings", []):
            response = await _remote(request, "knowledge", "GET", "/knowledge/" + quote(binding["knowledge_id"], safe="") + "/download")
            if response.status_code == 200 and hashlib.sha256(response.content).hexdigest() == sha:
                allowed = True
                break
        if not allowed:
            raise HTTPException(403, "知识库原件下载权限不足")
    with SessionLocal() as session:
        try:
            data = verified_artifact_bytes(session, session.get(SourceArtifact, sha))
        except (ValueError, OSError):
            raise HTTPException(409, "原件缺失或SHA256不符，未提供替代文件") from None
    return Response(data, media_type=artifact.media_type, headers={"ETag": sha, "X-Source-Sha256": sha,
        "Content-Disposition": "attachment; filename*=UTF-8''" + quote(_filename(artifact), safe="")})


@router.get("/{sha}")
async def detail(sha: str, request: Request):
    artifact = await _asset(request, sha)
    with SessionLocal() as session:
        operations = session.scalars(select(LibraryOperation).where(LibraryOperation.asset_id == sha,
            LibraryOperation.actor == actor()).order_by(LibraryOperation.updated_at.desc())).all()
    refreshed = [await _status(request, operation) for operation in operations]
    return {"asset": _info(artifact, refreshed), "operations": [_operation_info(row) for row in refreshed]}
