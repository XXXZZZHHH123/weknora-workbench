import sqlite3
from datetime import datetime, timezone

from sqlalchemy import JSON, DateTime, create_engine, event
from sqlalchemy.dialects.postgresql import JSONB
from sqlalchemy.engine import Engine, make_url
from sqlalchemy.orm import DeclarativeBase, sessionmaker
from sqlalchemy.types import TypeDecorator

from tin.config import settings

JSONType = JSON().with_variant(JSONB(), "postgresql")


class UTCDateTime(TypeDecorator):
    """带时区的时间统一存 UTC；拒绝 naive datetime，逼调用方显式声明时区。"""

    impl = DateTime(timezone=True)
    cache_ok = True

    def process_bind_param(self, value: datetime | None, dialect):
        if value is None:
            return None
        if value.tzinfo is None:
            raise ValueError(f"拒绝入库不带时区的时间：{value!r}")
        value = value.astimezone(timezone.utc)
        return value.replace(tzinfo=None) if dialect.name == "sqlite" else value

    def process_result_value(self, value: datetime | None, dialect):
        if value is None:
            return None
        return value.replace(tzinfo=timezone.utc) if value.tzinfo is None else value


class Base(DeclarativeBase):
    pass


def _enable_sqlite_fk(dbapi_conn, _record):
    # SQLite 默认不强制外键；不打开的话"证伪必须绑定已登记指标"形同虚设
    if isinstance(dbapi_conn, sqlite3.Connection):
        cur = dbapi_conn.cursor()
        cur.execute("PRAGMA foreign_keys=ON")
        cur.execute(f"PRAGMA busy_timeout={int(settings.sqlite_timeout_seconds * 1000)}")
        cur.close()


def make_engine(url: str | None = None, **kwargs) -> Engine:
    selected = url or settings.database_url
    if make_url(selected).get_backend_name() == "sqlite":
        connect_args = dict(kwargs.get("connect_args") or {})
        connect_args.setdefault("timeout", settings.sqlite_timeout_seconds)
        kwargs["connect_args"] = connect_args
    engine = create_engine(selected, **kwargs)
    event.listen(engine, "connect", _enable_sqlite_fk)
    return engine


engine = make_engine()
_legacy_sessions = sessionmaker(engine, expire_on_commit=False)


def tenant_database_path(tenant_id: int):
    from pathlib import Path
    if isinstance(tenant_id, bool) or not isinstance(tenant_id, int) or tenant_id <= 0:
        raise ValueError("A verified positive workspace id is required")
    return Path(settings.data_dir) / "workspaces" / str(tenant_id) / "tin.db"


# All migrations are serialized because Alembic's environment is process-global.
# The OS lock also protects two service workers initializing the same workspace.
import threading
_tenant_lock = threading.RLock()
_tenant_factories = {}


def tenant_session_factory(tenant_id: int):
    with _tenant_lock:
        path = tenant_database_path(tenant_id)
        key = str(path)
        if key in _tenant_factories:
            return _tenant_factories[key]
        path.parent.mkdir(parents=True, exist_ok=True)
        import fcntl
        from alembic import command
        from alembic.config import Config
        from tin.config import ROOT
        with path.with_suffix(".init.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            # Back up existing SQLite data before schema changes; a new workspace
            # has no legacy data to preserve.
            if path.exists():
                from tin.schema_check import backup, migration_gap
                check_engine = make_engine(f"sqlite:///{path}")
                if migration_gap(check_engine):
                    backup(check_engine, settings.migrate_backups)
                check_engine.dispose()
            cfg = Config()
            cfg.set_main_option("script_location", str(ROOT / "alembic"))
            cfg.set_main_option("sqlalchemy.url", f"sqlite:///{path}")
            command.upgrade(cfg, "head")
            path.chmod(0o600)
            tenant_engine = make_engine(f"sqlite:///{path}", connect_args={"check_same_thread": False})
            # Change journal mode exactly once per engine initialization, before
            # serving requests. WAL permits status/page readers during an ingest
            # write transaction; per-connection busy_timeout handles short write
            # contention without taking a global reader lock.
            with tenant_engine.connect() as connection:
                mode = connection.exec_driver_sql("PRAGMA journal_mode=WAL").scalar()
                if mode != "wal":
                    raise RuntimeError("Workspace SQLite WAL initialization failed")
                connection.commit()
            factory = sessionmaker(tenant_engine, expire_on_commit=False)
            # Workspace-wide indicator definitions; no personal default judgment.
            if not settings.weknora_api_url:
                from tin.jobs.seed import seed_indicators
                with factory() as session:
                    seed_indicators(session)
                    session.commit()
            _tenant_factories[key] = factory
            return factory


def SessionLocal(**kwargs):
    from tin.integration.workbench_scope import current_workbench, open_workbench_session
    scope = current_workbench.get()
    if scope is not None:
        return open_workbench_session(scope, **kwargs)
    if not settings.weknora_api_url:
        return _legacy_sessions(**kwargs)
    from fastapi import HTTPException
    raise HTTPException(409, '请先选择绑定知识库的研究工作台，未读取旧工作台事实')


# Compatibility for standalone schema checks and callers inspecting sessionmaker.kw.
SessionLocal.kw = _legacy_sessions.kw



def SystemSession(tenant_id: int):
    """Internal scheduler access to workspace facts; never creates a user account.

    Callers must obtain the workspace from verified HTTP context or the local
    registry of existing databases. This factory is never exposed as an endpoint.
    """
    if settings.weknora_api_url:
        raise PermissionError('集成平台事实只能在知识库维护，不能运行旧研究台事实刷新')
    session = tenant_session_factory(tenant_id)()
    session.info.update(tenant_id=tenant_id, system_refresh=True, system_actor="自动刷新")
    return session
