"""Workspace refresh controls; the authentication middleware protects writes."""
from fastapi import APIRouter, Body, HTTPException
from pydantic import BaseModel, ConfigDict, Field

from tin.db import SessionLocal
from tin.integration.identity import current_identity, verified_actor
from tin.refresh.scheduler import scheduler
from tin.refresh.service import (RefreshBusy, current_run, queue_run, refresh_state,
                                 update_plan, validate_sources, workspace_lock)

router = APIRouter()


class PlanIn(BaseModel):
    model_config = ConfigDict(extra="forbid")
    enabled: bool
    times: list[str] = Field(min_length=1, max_length=6)
    sources: list[str] = Field(min_length=1, max_length=6)


class RunIn(BaseModel):
    model_config = ConfigDict(extra="forbid")
    sources: list[str] = Field(min_length=1, max_length=6)


def _tenant():
    identity = current_identity.get()
    return identity.tenant_id if identity else 0


def _writer():
    identity = current_identity.get()
    if identity is not None and not identity.can_write:
        raise HTTPException(403, "当前空间角色只有读取权限")


@router.get("/api/sn/refresh")
def status():
    with SessionLocal() as session:
        return refresh_state(session)


@router.put("/api/sn/refresh/settings")
def configure(body: PlanIn):
    _writer()
    try:
        with workspace_lock(_tenant()):
            with SessionLocal() as session:
                update_plan(session, enabled=body.enabled, times=body.times, sources=body.sources,
                            actor=verified_actor("研究员"))
                return refresh_state(session)
    except RefreshBusy as exc:
        raise HTTPException(409, str(exc)) from None
    except ValueError as exc:
        raise HTTPException(400, str(exc)) from None


@router.post("/api/sn/refresh/run", status_code=202)
def update_now(body: RunIn | None = Body(default=None)):
    _writer()
    try:
        sources = validate_sources(body.sources) if body is not None else None
    except ValueError as exc:
        raise HTTPException(400, str(exc)) from None
    tenant = _tenant()
    try:
        with workspace_lock(tenant):
            with SessionLocal() as session:
                run, created = queue_run(session, trigger="manual", actor=verified_actor("研究员"), sources=sources)
                run_id = run.id
    except RefreshBusy:
        with SessionLocal() as session:
            active = current_run(session)
            if active is None:
                raise HTTPException(409, "当前空间正在更新，请稍后查看状态") from None
            return {"run_id": active.id, "created": False, "message": "当前空间已有更新任务"}
    scheduler.submit(tenant, run_id)
    return {"run_id": run_id, "created": created, "message": "更新任务已启动" if created else "当前空间已有更新任务"}
