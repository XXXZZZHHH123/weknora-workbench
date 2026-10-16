"""Explicit human research; no inferred publication, seeded facts, or execution."""
from datetime import datetime, timezone
from copy import deepcopy
from enum import Enum
from urllib.parse import quote
from uuid import uuid4

from fastapi import APIRouter, Depends, HTTPException, Query
from pydantic import BaseModel, ConfigDict, Field, StrictBool
from sqlalchemy import func, select, update

from tin.integration.workbench_scope import WorkbenchScope, go_request, verified_workbench, workbench_session
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision
from tin.web.workbench_artifact_evidence import capture_sources, store_captures

router = APIRouter(prefix="/api/workbenches")


class ArtifactModule(str, Enum):
    structure = "structure"
    judgments = "judgments"
    notes = "notes"
    briefs = "briefs"
    strategies = "strategies"


class ArtifactStatus(str, Enum):
    draft = "draft"
    confirmed = "confirmed"


class ArtifactInput(BaseModel):
    model_config = ConfigDict(extra="forbid")
    title: str = Field(min_length=1, max_length=120)
    body: str = Field(min_length=1, max_length=60000)
    status: ArtifactStatus = ArtifactStatus.draft
    source_ids: list[str] = Field(default_factory=list, max_length=100)
    confirmed: StrictBool = False
    revision: int | None = Field(default=None, ge=1)


def artifact_query(scope, module):
    query = select(WorkbenchArtifact).where(WorkbenchArtifact.module == module)
    if module != "structure":
        query = query.where(WorkbenchArtifact.actor == scope.identity.user_id)
    return query


def serialize_artifact(row):
    return {key: getattr(row, key) for key in ("id", "module", "title", "body", "status", "source_ids", "source_snapshots", "revision", "created_at", "updated_at")}


def record_revision(session, row, editor):
    session.add(WorkbenchArtifactRevision(artifact_id=row.id, revision=row.revision,
        title=row.title, body=row.body, status=row.status, source_ids=list(row.source_ids),
        source_snapshots=deepcopy(row.source_snapshots), edited_by=editor, recorded_at=row.updated_at))


def serialize_revision(row):
    return {key: getattr(row, key) for key in ("artifact_id", "revision", "title", "body", "status", "source_ids",
                                              "source_snapshots", "edited_by", "recorded_at")}


def list_owned_artifacts(session, scope, module, limit=100, offset=0):
    query = artifact_query(scope, module)
    total = session.scalar(select(func.count()).select_from(query.subquery()))
    rows = session.scalars(query.order_by(WorkbenchArtifact.updated_at.desc(), WorkbenchArtifact.id).offset(offset).limit(limit)).all()
    return {"items": [serialize_artifact(row) for row in rows], "total": total,
            "offset": offset, "truncated": offset + len(rows) < total}


def artifact_counts(session, scope):
    return {module.value: session.scalar(select(func.count()).select_from(artifact_query(scope, module.value).subquery()))
            for module in ArtifactModule}


async def validated_input(scope, body):
    if not scope.identity.can_write:
        raise HTTPException(403, "当前空间角色只有读取权限")
    title, text = body.title.strip(), body.body.strip()
    if not title or not text:
        raise HTTPException(400, "请填写标题和研究内容")
    if body.status == ArtifactStatus.confirmed and (not body.confirmed or not body.source_ids):
        raise HTTPException(400, "确认研究成果前，请明确勾选确认并关联原始资料")
    source_ids = list(dict.fromkeys(body.source_ids))
    sources = []
    for identifier in source_ids:
        if not identifier or len(identifier) > 128 or any(c in identifier for c in "/\\?#"):
            raise HTTPException(400, "资料编号无效")
        document = await go_request(scope, "/api/v1/knowledge/" + quote(identifier, safe=""))
        file = document.get("data")
        if (not isinstance(file, dict) or file.get("id") != identifier
                or file.get("knowledge_base_id") != scope.knowledge_base_id
                or file.get("parse_status") in {"deleting", "deleted"}):
            raise HTTPException(403, "引用资料不属于当前工作台绑定知识库")
        sources.append(file)
    return {"title": title, "body": text, "status": body.status.value, "source_ids": source_ids}, sources


@router.get("/{workbench_id}/{module}")
def list_artifacts(module: ArtifactModule, scope: WorkbenchScope = Depends(verified_workbench),
                   limit: int = Query(100, ge=1, le=200), offset: int = Query(0, ge=0, le=100000)):
    with workbench_session(scope) as session:
        return list_owned_artifacts(session, scope, module.value, limit, offset)


@router.post("/{workbench_id}/{module}", status_code=201)
async def create_artifact(module: ArtifactModule, body: ArtifactInput, scope: WorkbenchScope = Depends(verified_workbench)):
    values, sources = await validated_input(scope, body)
    captures = await capture_sources(scope, sources)
    now = datetime.now(timezone.utc)
    with workbench_session(scope) as session:
        row = WorkbenchArtifact(id=str(uuid4()), module=module.value, actor=scope.identity.user_id,
            source_snapshots=store_captures(session, scope, captures),
            revision=1, created_at=now, updated_at=now, **values)
        session.add(row)
        session.flush()
        record_revision(session, row, scope.identity.user_id)
        session.commit()
        return serialize_artifact(row)


@router.patch("/{workbench_id}/{module}/{artifact_id}")
async def update_artifact(module: ArtifactModule, artifact_id: str, body: ArtifactInput,
                          scope: WorkbenchScope = Depends(verified_workbench)):
    if body.revision is None:
        raise HTTPException(400, "请携带当前版本，避免覆盖其他修改")
    # Reject missing / private / already-stale records before downloading files.
    with workbench_session(scope) as session:
        row = session.scalar(artifact_query(scope, module.value).where(WorkbenchArtifact.id == artifact_id))
        if row is None:
            raise HTTPException(404, "研究记录不存在或无权访问")
        if row.revision != body.revision:
            raise HTTPException(409, "研究记录已更新，请刷新后继续修改")
    values, sources = await validated_input(scope, body)
    captures = await capture_sources(scope, sources)
    with workbench_session(scope) as session:
        row = session.scalar(artifact_query(scope, module.value).where(WorkbenchArtifact.id == artifact_id))
        if row is None:
            raise HTTPException(404, "研究记录不存在或无权访问")
        values["source_snapshots"] = store_captures(session, scope, captures)
        result = session.execute(update(WorkbenchArtifact).where(WorkbenchArtifact.id == artifact_id,
            WorkbenchArtifact.revision == body.revision).values(**values, revision=body.revision + 1,
                updated_at=datetime.now(timezone.utc)))
        if result.rowcount != 1:
            raise HTTPException(409, "研究记录已更新，请刷新后继续修改")
        session.refresh(row)
        record_revision(session, row, scope.identity.user_id)
        session.commit()
        return serialize_artifact(row)


@router.get("/{workbench_id}/{module}/{artifact_id}/revisions")
def artifact_revisions(module: ArtifactModule, artifact_id: str, scope: WorkbenchScope = Depends(verified_workbench),
                       limit: int = Query(100, ge=1, le=200), offset: int = Query(0, ge=0, le=100000)):
    with workbench_session(scope) as session:
        row = session.scalar(artifact_query(scope, module.value).where(WorkbenchArtifact.id == artifact_id))
        if row is None:
            raise HTTPException(404, "研究记录不存在或无权访问")
        query = select(WorkbenchArtifactRevision).where(WorkbenchArtifactRevision.artifact_id == artifact_id)
        total = session.scalar(select(func.count()).select_from(query.subquery()))
        rows = session.scalars(query.order_by(WorkbenchArtifactRevision.revision.desc()).offset(offset).limit(limit)).all()
        return {"items": [serialize_revision(item) for item in rows], "total": total,
                "offset": offset, "truncated": offset + len(rows) < total,
                "notice": "仅列出实际保留的版本；旧版未冻结的引用不会补造历史原件"}
