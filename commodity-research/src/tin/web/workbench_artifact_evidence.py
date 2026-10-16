"""Bounded captures of actual authorized originals; uploaded text is never code."""
from datetime import datetime, timezone
import hashlib
from urllib.parse import quote, urlsplit

import httpx
from fastapi import HTTPException
from sqlalchemy import select

from tin.config import settings
from tin.integration.workbench_scope import go_request
from tin.models import ObservationSource, SourceBatch
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision
from tin.provenance.service import MAX_SOURCE_BYTES, preserve_blob

MAX_REQUEST_SOURCE_BYTES = MAX_SOURCE_BYTES


def source_metadata(source):
    result = {"knowledge_id": source["id"], "knowledge_base_id": source["knowledge_base_id"],
              "captured_at": datetime.now(timezone.utc).isoformat()}
    for key in ("title", "file_name", "file_type", "file_size", "file_hash", "type", "source", "updated_at"):
        value = source.get(key)
        if isinstance(value, str):
            result[key] = value[:2048]
        elif key == "file_size" and type(value) is int:
            result[key] = value
    return result


def original_metadata(source):
    size, digest = source.get("file_size"), source.get("file_hash")
    if type(size) is not int or size < 0:
        raise HTTPException(409, "原件登记大小缺失或无效，不能确认冻结版本")
    if size > MAX_SOURCE_BYTES:
        raise HTTPException(413, "单个来源原件超过 100 MiB")
    if not isinstance(digest, str) or len(digest) not in {32, 64} or any(c not in "0123456789abcdefABCDEF" for c in digest):
        raise HTTPException(409, "原件登记哈希缺失或无效，不能确认冻结版本")
    return size, digest.lower()


async def capture_sources(scope, sources):
    """Read before taking a DB write lock; bound the whole request, not each item only."""
    declared = {}
    for source in sources:
        if source.get("type") in {"file", "file_url"}:
            declared[source["id"]] = original_metadata(source)
    if sum(size for size, _ in declared.values()) > MAX_REQUEST_SOURCE_BYTES:
        raise HTTPException(413, "本次引用原件合计超过 100 MiB，请分开保存研究记录")
    if not declared:
        return [({**source_metadata(source), "capture_status": "metadata_only",
                  "notice": "仅冻结当前来源元数据，未保存网页或在线编辑正文原件"}, None) for source in sources]
    base = (settings.weknora_api_url or "").rstrip("/")
    parsed = urlsplit(base)
    if declared and (not scope.authorization.startswith("Bearer ") or parsed.scheme not in {"http", "https"}
                     or not parsed.netloc or parsed.username or parsed.path or parsed.query or parsed.fragment):
        raise HTTPException(503, "主平台原件读取地址或授权无效")
    captures, total = [], 0
    async with httpx.AsyncClient(timeout=60, follow_redirects=False, trust_env=False) as client:
        for source in sources:
            snapshot = source_metadata(source)
            raw = None
            if source["id"] not in declared:
                snapshot.update(capture_status="metadata_only", notice="仅冻结当前来源元数据，未保存网页或在线编辑正文原件")
            else:
                expected_size, expected_hash = declared[source["id"]]
                url = base + "/api/v1/knowledge/" + quote(source["id"], safe="") + "/download"
                try:
                    async with client.stream("GET", url, headers={"Authorization": scope.authorization,
                        "X-Tenant-ID": str(scope.identity.tenant_id)}) as response:
                        if response.status_code != 200:
                            raise HTTPException(response.status_code if response.status_code in {401, 403, 404} else 503,
                                "原件无法按当前授权读取，研究记录未保存；未用其他文件替代")
                        length = response.headers.get("content-length")
                        if length and length.isdecimal() and int(length) > expected_size:
                            raise HTTPException(409, "原件响应大小与登记版本不符，未冻结替代内容")
                        raw = bytearray()
                        async for chunk in response.aiter_bytes(chunk_size=64 * 1024):
                            total += len(chunk)
                            if total > MAX_REQUEST_SOURCE_BYTES or len(raw) + len(chunk) > MAX_SOURCE_BYTES:
                                raise HTTPException(413, "本次引用原件超过 100 MiB，研究记录未保存")
                            if len(raw) + len(chunk) > expected_size:
                                raise HTTPException(409, "原件实际大小与登记版本不符，未冻结替代内容")
                            raw.extend(chunk)
                except httpx.HTTPError:
                    raise HTTPException(503, "来源原件读取未完成，研究记录未保存") from None
                actual_hash = (hashlib.md5(raw).hexdigest() if len(expected_hash) == 32 else hashlib.sha256(raw).hexdigest())
                if len(raw) != expected_size or actual_hash != expected_hash:
                    raise HTTPException(409, "原件与登记大小或哈希不一致，未冻结替代内容")
                snapshot.update(capture_status="original_frozen", sha256=hashlib.sha256(raw).hexdigest())
            captures.append((snapshot, raw))
    return captures


def store_captures(session, scope, captures):
    snapshots = []
    for metadata, raw in captures:
        snapshot = dict(metadata)
        if raw is not None:
            source = preserve_blob(session, raw, snapshot.get("file_name") or "source.bin",
                kind="workbench_evidence", metadata={"knowledge_id": snapshot["knowledge_id"],
                    "knowledge_base_id": scope.knowledge_base_id, "captured_by": scope.identity.user_id,
                    "title": snapshot.get("title", ""), "registered_hash": snapshot.get("file_hash", "")})
            snapshot.update(artifact_id=source.id, sha256=source.id,
                download_url=f"/research/api/workbenches/{scope.workbench_id}/sources/{source.id}/download")
        snapshots.append(snapshot)
    return snapshots


def has_visible_reference(session, scope, artifact_id):
    if session.scalar(select(ObservationSource.id).where(ObservationSource.artifact_id == artifact_id).limit(1)):
        return True
    if session.scalar(select(SourceBatch.id).where(SourceBatch.artifact_id == artifact_id,
                                                   SourceBatch.actor == scope.identity.user_id).limit(1)):
        return True
    query = select(WorkbenchArtifactRevision.source_snapshots).join(WorkbenchArtifact,
        WorkbenchArtifactRevision.artifact_id == WorkbenchArtifact.id).where(
            (WorkbenchArtifact.module == "structure") | (WorkbenchArtifact.actor == scope.identity.user_id))
    return any(any(snapshot.get("artifact_id") == artifact_id for snapshot in snapshots)
               for snapshots in session.scalars(query))


def native_bindings(artifact):
    """Only capture-time references are candidates; current Go permissions remain authoritative."""
    details = artifact.details or {}
    descriptions = [details] + [alias.get("metadata", {}) for alias in details.get("aliases", [])
                                if isinstance(alias, dict)]
    result = []
    for description in descriptions:
        if not isinstance(description, dict):
            continue
        result.append(description)
        result.extend(binding for binding in description.get("native_source_bindings", [])
                      if isinstance(binding, dict))
    return [item for item in result if isinstance(item.get("knowledge_id"), str)
            and item.get("knowledge_id") and isinstance(item.get("knowledge_base_id"), str)]


async def authorize_frozen_download(session, scope, artifact):
    if not has_visible_reference(session, scope, artifact.id):
        raise HTTPException(403, "该冻结原件没有当前账号可见的研究引用")
    candidates = [item for item in native_bindings(artifact)
                  if item["knowledge_base_id"] == scope.knowledge_base_id]
    if not candidates:
        raise HTTPException(403, "冻结原件未登记可核验的原生来源授权，不能下载原始内容")
    parsed = urlsplit(settings.weknora_api_url or "")
    if (not scope.authorization.startswith("Bearer ") or parsed.scheme not in {"http", "https"}
            or not parsed.netloc or parsed.username or parsed.path not in {"", "/"} or parsed.query or parsed.fragment):
        raise HTTPException(503, "主平台原件读取地址或授权无效")
    async with httpx.AsyncClient(timeout=30, follow_redirects=False, trust_env=False) as client:
        for candidate in candidates:
            identifier = candidate["knowledge_id"]
            if len(identifier) > 128 or any(c in identifier for c in "/\\?#"):
                continue
            try:
                document = await go_request(scope, "/api/v1/knowledge/" + quote(identifier, safe=""))
            except HTTPException as exc:
                if exc.status_code in {403, 404}:
                    continue
                raise
            source = document.get("data")
            if (not isinstance(source, dict) or source.get("id") != identifier
                    or source.get("knowledge_base_id") != scope.knowledge_base_id
                    or source.get("parse_status") in {"deleting", "deleted"}):
                continue
            # Check current access only. Later source versions never replace the
            # immutable historical bytes returned by this API.
            try:
                async with client.stream("GET", settings.weknora_api_url.rstrip("/")
                    + "/api/v1/knowledge/" + quote(identifier, safe="") + "/download",
                    headers={"Authorization": scope.authorization,
                             "X-Tenant-ID": str(scope.identity.tenant_id)}) as response:
                    if response.status_code == 200:
                        return
                    if response.status_code == 401:
                        raise HTTPException(401, "主平台登录已失效")
                    if response.status_code not in {403, 404}:
                        raise HTTPException(503, "无法重新核验来源原件权限")
            except httpx.HTTPError:
                raise HTTPException(503, "无法重新核验来源原件权限") from None
    raise HTTPException(403, "原生来源已删除、移出知识库或无原件读取权限")
