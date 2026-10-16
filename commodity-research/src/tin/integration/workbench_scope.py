"""Server-verified, seed-free storage for an explicitly isolated workbench."""
from contextlib import contextmanager
from contextvars import ContextVar
from dataclasses import dataclass, field
from pathlib import Path
from urllib.parse import quote
from uuid import UUID

import httpx
from fastapi import HTTPException, Request
from sqlalchemy.orm import sessionmaker
from sqlalchemy import text
from sqlalchemy.pool import NullPool

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
    authorization: str = field(default='', repr=False, compare=False)
    additional_knowledge_bases: tuple[dict, ...] = ()


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
    identity = current_identity.get()
    authorization = request.headers.get("authorization", "")
    scope = await resolve_workbench(identity, workbench_id, authorization)
    marker = current_workbench.set(scope)
    try:
        yield scope
    finally:
        current_workbench.reset(marker)


async def resolve_workbench(identity, workbench_id, authorization):
    """Browser and API entry points share the same fresh server-side verification."""
    canonical_workbench_id(workbench_id)
    if identity is None or not authorization.startswith("Bearer "):
        raise HTTPException(401, "请先登录主平台")
    scope = WorkbenchScope(identity, workbench_id, "", 0, {}, authorization)
    doc = await go_request(scope, "/api/v1/workspaces/research/" + quote(workbench_id, safe=""))
    metadata = doc.get("data")
    if (not isinstance(metadata, dict) or metadata.get("id") != workbench_id
            or type(metadata.get("scope_tenant_id")) is not int
            or metadata["scope_tenant_id"] != identity.tenant_id):
        raise HTTPException(403, "研究工作台与当前空间不匹配")
    if metadata.get("workspace_mode") not in {"isolated", "legacy"}:
        raise HTTPException(409, "研究工作台来源模式无效，未使用历史事实")
    kb_id = metadata.get("knowledge_base_id")
    if not isinstance(kb_id, str) or not kb_id or len(kb_id) > 128:
        raise HTTPException(403, "工作台没有有效的知识库绑定")
    kb_doc = await go_request(scope, "/api/v1/knowledge-bases/" + quote(kb_id, safe=""))
    kb = kb_doc.get("data")
    if (not isinstance(kb, dict) or kb.get("id") != kb_id or type(kb.get("tenant_id")) is not int
            or kb["tenant_id"] <= 0 or (kb["tenant_id"] != identity.tenant_id
                and kb.get("my_permission") not in {"viewer", "editor", "admin"})):
        raise HTTPException(403, "知识库授权范围不匹配")
    additional = ()
    default = metadata.get('default_knowledge_base')
    if default is not None:
        if (not isinstance(default, dict) or default.get('kind') != 'macro'
                or type(default.get('tenant_id')) is not int or default['tenant_id'] != identity.tenant_id
                or not isinstance(default.get('knowledge_base_id'), str)
                or not default['knowledge_base_id'] or len(default['knowledge_base_id']) > 128):
            raise HTTPException(403, '工作台默认知识库范围无效')
        default_id = default['knowledge_base_id']
        if default_id != kb_id:
            default_doc = await go_request(scope, '/api/v1/knowledge-bases/' + quote(default_id, safe=''))
            default_kb = default_doc.get('data')
            if (not isinstance(default_kb, dict) or default_kb.get('id') != default_id
                    or type(default_kb.get('tenant_id')) is not int
                    or default_kb['tenant_id'] != identity.tenant_id):
                raise HTTPException(403, '默认知识库授权范围不匹配')
            additional = ({'knowledge_base_id': default_id, 'tenant_id': default_kb['tenant_id'],
                           'kind': 'macro', 'name': default_kb.get('name') or default.get('name') or ''},)
    scope = WorkbenchScope(identity, workbench_id, kb_id, kb["tenant_id"], metadata, authorization, additional)
    return scope


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
    return result_session_factory(path)


def result_session_factory(path):
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
            if path.exists():
                from tin.schema_check import backup, migration_gap
                check_engine = make_engine(f'sqlite:///{path}')
                try:
                    if migration_gap(check_engine):
                        backup(check_engine, settings.migrate_backups)
                finally:
                    check_engine.dispose()
            config = Config()
            config.set_main_option("script_location", str(ROOT / "alembic"))
            config.set_main_option("sqlalchemy.url", f"sqlite:///{path}")
            command.upgrade(config, "head")
            path.chmod(0o600)
            engine = make_engine(f"sqlite:///{path}", connect_args={"check_same_thread": False}, poolclass=NullPool)
            with engine.begin() as connection:
                connection.exec_driver_sql("PRAGMA journal_mode=WAL")
            factory = sessionmaker(engine, expire_on_commit=False)
            # This engine persists research results only. Existing fact tables
            # remain an archive and are masked by request-local read-only views.
            _factories[key] = factory
            return factory


@contextmanager
def workbench_session(scope: WorkbenchScope):
    marker = current_workbench.set(scope)
    identity_marker = current_identity.set(scope.identity) if current_identity.get() is None else None
    try:
        with open_workbench_session(scope) as session:
            yield session
    finally:
        current_workbench.reset(marker)
        if identity_marker is not None:
            current_identity.reset(identity_marker)


def open_workbench_session(scope: WorkbenchScope, **kwargs):
    identity = current_identity.get()
    if identity is not None and (identity.tenant_id != scope.identity.tenant_id or identity.user_id != scope.identity.user_id):
        raise PermissionError("Workbench identity does not match the verified caller")
    legacy = scope.metadata.get('workspace_mode') == 'legacy'
    if legacy:
        from tin.db import tenant_database_path
        factory = result_session_factory(tenant_database_path(scope.identity.tenant_id))
    else:
        factory = isolated_session_factory(scope.identity.tenant_id, scope.workbench_id)
    session = factory()
    try:
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
        session.close()
        from tin.integration.kb_facts import projected_session
        session = projected_session(factory.kw['bind'], scope, **kwargs)
        session.info.update(scope.identity.session_info(), workbench_id=scope.workbench_id,
                            knowledge_base_id=scope.knowledge_base_id,
                            knowledge_base_tenant_id=scope.knowledge_base_tenant_id,
                            source_storage_legacy=legacy)
        return session
    except BaseException:
        session.close()
        raise


def service_scope(identity: Identity):
    if not identity.workbench_id or not identity.knowledge_base_id or identity.knowledge_base_tenant_id <= 0:
        raise PermissionError("Signed isolated workbench scope is incomplete")
    canonical_workbench_id(identity.workbench_id)
    return WorkbenchScope(identity, identity.workbench_id, identity.knowledge_base_id,
                          identity.knowledge_base_tenant_id, {"id": identity.workbench_id,
                          "knowledge_base_id": identity.knowledge_base_id, "workspace_mode": "isolated"}, identity.authorization)
