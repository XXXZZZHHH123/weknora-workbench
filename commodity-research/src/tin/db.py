import sqlite3
from datetime import datetime, timezone

from sqlalchemy import JSON, DateTime, create_engine, event
from sqlalchemy.dialects.postgresql import JSONB
from sqlalchemy.engine import Engine
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
        cur.close()


def make_engine(url: str | None = None, **kwargs) -> Engine:
    engine = create_engine(url or settings.database_url, **kwargs)
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
            factory = sessionmaker(tenant_engine, expire_on_commit=False)
            # Workspace-wide indicator definitions; no personal default judgment.
            from tin.jobs.seed import seed_indicators
            with factory() as session:
                seed_indicators(session)
                session.commit()
            _tenant_factories[key] = factory
            return factory


def SessionLocal(**kwargs):
    if not settings.weknora_api_url:
        return _legacy_sessions(**kwargs)
    from tin.integration.identity import current_identity
    identity = current_identity.get()
    if identity is None:
        raise PermissionError("Verified WeKnora identity is required")
    session = tenant_session_factory(identity.tenant_id)(**kwargs)
    session.info.update(identity.session_info())
    from tin.ownership import current_researcher
    try:
        current_researcher(session)
        session.commit()
    except Exception:
        session.close()
        raise
    return session


# Compatibility for standalone schema checks and callers inspecting sessionmaker.kw.
SessionLocal.kw = _legacy_sessions.kw
