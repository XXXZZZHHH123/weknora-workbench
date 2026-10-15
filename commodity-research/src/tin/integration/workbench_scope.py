"""Server-verified, seed-free storage for an explicitly isolated workbench."""
from contextlib import contextmanager
from contextvars import ContextVar
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import quote
from uuid import UUID

import httpx
from fastapi import HTTPException, Request
from sqlalchemy.orm import sessionmaker
from sqlalchemy import text

from tin.config import settings
from tin.db import _tenant_lock, make_engine
from tin.integration.identity import Identity, current_identity


@dataclass(frozen=True)
class WorkbenchScope:
    identity: Identity
    workbench_id: str
    knowledge_base_id: str
    knowledge_base_tenant_id: int
    metadata: dict
    authorization: str = ""


current_workbench: ContextVar[WorkbenchScope | None] = ContextVar("isolated_workbench", default=None)
_factories = {}


def canonical_workbench_id(value: str) -> str:
    try:
        parsed = UUID(value)
        if str(parsed) != value or parsed.int == 0:
            raise ValueError
        return value
    except (ValueError, TypeError, AttributeError):
        raise HTTPException(400, "研究工作台编号无效") from None


async def go_request(scope: WorkbenchScope, path: str, *, params=None):
    if not settings.weknora_api_url or not scope.authorization:
        raise HTTPException(401, "缺少已核验的主平台登录")
    if not path.startswith("/api/v1/") or "?" in path or "#" in path:
        raise ValueError("Only fixed main-platform API paths are accepted")
    try:
        async with httpx.AsyncClient(timeout=15, follow_redirects=False, trust_env=False) as client:
            response = await client.get(settings.weknora_api_url.rstrip("/") + path, params=params,
                headers={"Authorization": scope.authorization, "X-Tenant-ID": str(scope.identity.tenant_id)})
        if response.status_code in {401, 403, 404}:
            raise HTTPException(response.status_code, "工作台或知识库授权已失效")
        response.raise_for_status()
        document = response.json()
        if not isinstance(document, dict) or document.get("success") is not True:
            raise ValueError("Invalid API envelope")
        return document
    except HTTPException:
        raise
    except (httpx.HTTPError, ValueError, TypeError):
        raise HTTPException(503, "暂时无法核验工作台来源，请稍后重试") from None


async def verified_workbench(request: Request, workbench_id: str):
    canonical_workbench_id(workbench_id)
    identity = current_identity.get()
    authorization = request.headers.get("authorization", "")
    if identity is None or not authorization.startswith("Bearer "):
        raise HTTPException(401, "请先登录主平台")
    scope = WorkbenchScope(identity, workbench_id, "", 0, {}, authorization)
    doc = await go_request(scope, "/api/v1/workspaces/research/" + quote(workbench_id, safe=""))
    metadata = doc.get("data")
    if (not isinstance(metadata, dict) or metadata.get("id") != workbench_id
            or type(metadata.get("scope_tenant_id")) is not int
            or metadata["scope_tenant_id"] != identity.tenant_id):
        raise HTTPException(403, "研究工作台与当前空间不匹配")
    if metadata.get("workspace_mode") != "isolated":
        raise HTTPException(409, "此入口仅支持独立研究工作台，请使用原工作台入口")
    kb_id = metadata.get("knowledge_base_id")
    if not isinstance(kb_id, str) or not kb_id or len(kb_id) > 128:
        raise HTTPException(403, "工作台没有有效的知识库绑定")
    kb_doc = await go_request(scope, "/api/v1/knowledge-bases/" + quote(kb_id, safe=""))
    kb = kb_doc.get("data")
    if (not isinstance(kb, dict) or kb.get("id") != kb_id or type(kb.get("tenant_id")) is not int
            or kb["tenant_id"] <= 0 or (kb["tenant_id"] != identity.tenant_id
                and kb.get("my_permission") not in {"viewer", "editor", "admin"})):
        raise HTTPException(403, "知识库授权范围不匹配")
    scope = WorkbenchScope(identity, workbench_id, kb_id, kb["tenant_id"], metadata, authorization)
    marker = current_workbench.set(scope)
    try:
        yield scope
    finally:
        current_workbench.reset(marker)


def isolated_database_path(tenant_id: int, workbench_id: str):
    if type(tenant_id) is not int or tenant_id <= 0:
        raise PermissionError("Verified positive tenant identity is required")
    canonical_workbench_id(workbench_id)
    root = (Path(settings.data_dir) / "workbenches").resolve()
    path = root / str(tenant_id) / workbench_id / "research.db"
    if not path.resolve().is_relative_to(root):
        raise PermissionError("Invalid isolated workbench storage path")
    return path


def isolated_session_factory(tenant_id: int, workbench_id: str):
    path = isolated_database_path(tenant_id, workbench_id)
    with _tenant_lock:
        key = str(path)
        if key in _factories:
            return _factories[key]
        path.parent.mkdir(parents=True, exist_ok=True)
        import fcntl
        from alembic import command
        from alembic.config import Config
        from tin.config import ROOT
        with path.with_suffix(".init.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            config = Config()
            config.set_main_option("script_location", str(ROOT / "alembic"))
            config.set_main_option("sqlalchemy.url", f"sqlite:///{path}")
            command.upgrade(config, "head")
            path.chmod(0o600)
            engine = make_engine(f"sqlite:///{path}", connect_args={"check_same_thread": False})
            with engine.begin() as connection:
                connection.exec_driver_sql("PRAGMA journal_mode=WAL")
            factory = sessionmaker(engine, expire_on_commit=False)
            # No seed, legacy session factory, personal default, or tenant DB is
            # consulted: this database is empty until a confirmed source writes.
            _factories[key] = factory
            return factory


@contextmanager
def workbench_session(scope: WorkbenchScope):
    with isolated_session_factory(scope.identity.tenant_id, scope.workbench_id)() as session:
        expected = (scope.knowledge_base_id, scope.knowledge_base_tenant_id)
        session.execute(text("INSERT INTO isolated_scope "
                             "(workbench_id,knowledge_base_id,knowledge_base_tenant_id) VALUES (:id,:kb,:tenant) "
                             "ON CONFLICT(workbench_id) DO NOTHING"),
                        {"id": scope.workbench_id, "kb": expected[0], "tenant": expected[1]})
        existing = session.execute(text("SELECT knowledge_base_id, knowledge_base_tenant_id FROM isolated_scope "
                                        "WHERE workbench_id=:id"), {"id": scope.workbench_id}).first()
        if tuple(existing) != expected:
            raise HTTPException(409, "工作台主知识库已改变，需重新建立独立来源范围，未提供旧来源数据")
        session.commit()
        session.info.update(scope.identity.session_info(), workbench_id=scope.workbench_id,
                            knowledge_base_id=scope.knowledge_base_id,
                            knowledge_base_tenant_id=scope.knowledge_base_tenant_id)
        marker = current_workbench.set(scope)
        try:
            yield session
        finally:
            current_workbench.reset(marker)


def service_scope(identity: Identity):
    if not identity.workbench_id or not identity.knowledge_base_id or identity.knowledge_base_tenant_id <= 0:
        raise PermissionError("Signed isolated workbench scope is incomplete")
    canonical_workbench_id(identity.workbench_id)
    return WorkbenchScope(identity, identity.workbench_id, identity.knowledge_base_id,
                          identity.knowledge_base_tenant_id, {"id": identity.workbench_id,
                          "knowledge_base_id": identity.knowledge_base_id, "workspace_mode": "isolated"})
