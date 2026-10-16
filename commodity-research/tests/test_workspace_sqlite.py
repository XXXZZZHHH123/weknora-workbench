"""A real workspace writer must not block authenticated status polling."""
import sqlite3
import threading
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone

from sqlalchemy import event, select

from tin.config import settings
from tin.db import SessionLocal, tenant_database_path, tenant_session_factory
from tin.integration.identity import Identity, current_identity
from tin.models import Researcher
from tin.refresh.service import ensure_plan, refresh_state, workspace_lock


def test_live_file_writer_allows_authenticated_status_read_and_no_journal_change(tmp_path,monkeypatch):
    monkeypatch.setattr(settings,'data_dir',tmp_path)
    monkeypatch.setattr(settings,'weknora_api_url',None)
    identity=Identity('reader','reader@test','Reader',91,'owner')
    marker=current_identity.set(identity)
    try:
        # A file/SQLite concurrency test of the retained standalone repository;
        # integrated calls now require a KB scope and cannot refresh local facts.
        factory=tenant_session_factory(91)
        def open_session():
            session=factory()
            session.info.update(identity.session_info())
            return session
        with open_session() as session:
            plan=ensure_plan(session)
            original=plan.updated_by
            from tin.ownership import current_researcher
            current_researcher(session)
            session.commit()
        engine=tenant_session_factory(91).kw['bind']
        with engine.connect() as connection:
            assert connection.exec_driver_sql('PRAGMA journal_mode').scalar()=='wal'
            assert connection.exec_driver_sql('PRAGMA busy_timeout').scalar()==30000
        statements=[]
        event.listen(engine,'before_cursor_execute',lambda conn,cursor,statement,parameters,context,executemany:statements.append(statement))
        writer=sqlite3.connect(tenant_database_path(91),isolation_level=None)
        writer.execute('BEGIN EXCLUSIVE')
        writer.execute("UPDATE refresh_settings SET updated_by='uncommitted writer' WHERE id=1")
        def read_status():
            scope=current_identity.set(identity)
            try:
                with open_session() as session:
                    result=refresh_state(session)
                    assert session.get(__import__('tin.models.refresh',fromlist=['RefreshSettings']).RefreshSettings,1).updated_by==original
                    assert session.scalar(select(Researcher.id).where(Researcher.weknora_user_id=='reader'))
                    return result
            finally:current_identity.reset(scope)
        try:
            with ThreadPoolExecutor(max_workers=1) as pool:
                pending=pool.submit(read_status)
                try:
                    assert pending.result(timeout=2)['times']==['08:00','16:30']
                finally:
                    writer.rollback()
        finally:writer.close()
        assert not any('journal_mode' in statement.lower() for statement in statements)
        assert not any(statement.lstrip().upper().startswith(('INSERT','UPDATE','DELETE')) for statement in statements)
    finally:current_identity.reset(marker)


def test_memory_database_refresh_lock_never_creates_checkout_artifact(tmp_path,monkeypatch):
    from pathlib import Path
    monkeypatch.chdir(tmp_path)
    monkeypatch.setattr(settings,'weknora_api_url',None)
    monkeypatch.setattr(settings,'database_url','sqlite://')
    monkeypatch.setattr(settings,'data_dir',tmp_path)
    with workspace_lock(0): pass
    assert list(tmp_path.iterdir())==[]
