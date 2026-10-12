"""Shared facts, private research, and verified actor regression tests."""
from contextlib import contextmanager
from datetime import date, datetime, timedelta, timezone

import pytest
from sqlalchemy import select, text

from tin.brief import store
from tin.brief.compose import signals_of
from tin.brief.prompt import select_playbook
from tin.compute.signals import create_review_tasks, evaluate, resolve_review_task
from tin.config import ROOT, settings
from tin.export.board import snapshot
from tin.export.excel import _judgment_series
from tin.ingest import import_jobs
from tin.ingest.excel_importer import build_preview, commit_preview
from tin.integration.identity import Identity, current_identity
from tin.jobs.seed import seed_judgment, seed_researcher
from tin.judgments.service import current, save_version
from tin.models import Indicator, PlaybookItem, ReviewTask
from tin.ownership import require_owned
from tin.strategy.generate import build_context
from tin.strategy.service import create_strategy, get_strategy, list_strategies, serialize_strategy
from tests.test_brief_store import _result
from tests.test_excel_import import xlsx
from tests.test_judgments import payload
from tin.schemas.strategy import StrategyPlan

D = date(2026, 9, 18)
PRIORITY = {"order": ["th_risk_vix", "th_entry_40w"], "note": "测试"}


def login(session, user):
    session.info.update(weknora_user_id=user, weknora_email=f"{user}@example.com",
                        weknora_display_name=user, tenant_id=1)
    row = seed_researcher(session)
    session.commit()
    return row


def active(session):
    row = save_version(session, payload(priority_rule=PRIORITY), actor="forged actor", activate=True)
    session.commit()
    return row


def test_same_variety_has_independent_active_versions(session):
    shared = {i.series_id for i in session.scalars(select(Indicator)).all()}
    a = login(session, "account-a")
    a1 = active(session)
    b = login(session, "account-b")
    assert current(session, "SN") is None
    b1 = active(session)
    assert (a1.version, b1.version) == (1, 1)
    assert (a1.researcher_id, b1.researcher_id) == (a.id, b.id)
    assert (a1.author, b1.author) == ("account-a", "account-b")
    assert current(session, "SN").id == b1.id
    login(session, "account-a")
    a2 = active(session)
    session.refresh(a1)
    session.refresh(b1)
    assert (a1.status, a2.version, b1.status) == ("已归档", 2, "生效")
    login(session, "account-b")
    assert current(session, "SN").id == b1.id
    assert {i.series_id for i in session.scalars(select(Indicator)).all()} == shared


def test_login_never_claims_or_copies_legacy_judgment(session):
    old = seed_judgment(session)
    me = seed_researcher(session)
    new = login(session, "new-account")
    assert new.id != me.id
    assert seed_judgment(session) is None
    assert current(session, "SN") is None
    assert snapshot(session, "SN", D)["judgment"] is None
    assert _judgment_series(session, "SN", [D], "judgment_version") == {}
    with pytest.raises(PermissionError):
        require_owned(session, old)


def test_signals_and_review_task_ids_cannot_cross_accounts(loaded):
    login(loaded, "a")
    ja = active(loaded)
    signals = evaluate(loaded, "SN", D)
    assert signals and {s.judgment_id for s in signals} == {ja.id}
    task = ReviewTask(judgment_id=ja.id, trigger_type="判断到期", assignee="a",
                      state="待处理", created_at=datetime.now(timezone.utc))
    loaded.add(task)
    loaded.commit()
    login(loaded, "b")
    active(loaded)
    assert snapshot(loaded, "SN", D)["signals"] == []
    for operation in (lambda: signals_of(loaded, ja.id, D),
                      lambda: create_review_tasks(loaded, signals),
                      lambda: resolve_review_task(loaded, task.id, actor="a", note="forged")):
        with pytest.raises(PermissionError):
            operation()
    loaded.refresh(task)
    assert task.state == "待处理"
    assert all(s.judgment_id != ja.id for s in evaluate(loaded, "SN", D))


def test_briefs_playbook_and_actor_are_private(session):
    a = login(session, "a")
    now = datetime.now(timezone.utc)
    item = PlaybookItem(researcher_id=a.id, variety="SN", text="A private idea",
                        status="生效", created_at=now, updated_at=now)
    session.add(item)
    session.commit()
    brief = store.save(session, _result(playbook_ids=[item.id]), researcher_id=a.id,
                       variety="SN", trade_date=D)
    b = login(session, "b")
    assert select_playbook(session, researcher_id=b.id, variety="SN", d=D) == ([], [])
    assert store.current(session, b.id, "SN", D) is None
    for operation in (lambda: store.review(session, brief.id, actor="a", action="采纳"),
                      lambda: store.current(session, a.id, "SN", D),
                      lambda: select_playbook(session, researcher_id=a.id, variety="SN", d=D),
                      lambda: store.save(session, _result(playbook_ids=[item.id]),
                                         researcher_id=b.id, variety="SN", trade_date=D)):
        with pytest.raises(PermissionError):
            operation()
    session.refresh(item)
    assert item.used_count == 1
    login(session, "a")
    revisions = store.review(session, brief.id, actor="forged", action="采纳")
    assert brief.reviewed_by == "a" and revisions[0].actor == "a"
    login(session, "b")
    with pytest.raises(PermissionError):
        store.reopen(session, brief.id, actor="a", reason="forged")


def plan():
    now = datetime.now(timezone.utc)
    return StrategyPlan(title="Test", kind="outright",
        legs=[{"contract": "SN2611", "side": "buy", "quote_series_id": "SHFE.SN.2611.close"}],
        thesis="test", alternative_explanation="test", market_expectation="test",
        evidence=[{"as_of": now, "source": "test", "statement": "test"}],
        invalidation="test", catalyst="test", horizon_days=1, as_of=now,
        review_at=now + timedelta(days=1), entry={"low": 100, "high": 110, "reason": "test"},
        target={"low": 120, "high": 130, "reason": "test"},
        stop={"low": 80, "high": 90, "reason": "test"}, entry_condition="test", risk_budget=100)


def test_strategy_ids_revisions_and_generation_context_are_private(session):
    a = login(session, "a")
    active(session)
    row = create_strategy(session, plan(), actor="forged", reason="test")
    session.commit()
    assert row.researcher_id == a.id and row.author == "a"
    b = login(session, "b")
    assert list_strategies(session) == []
    for operation in (lambda: get_strategy(session, row.id),
                      lambda: serialize_strategy(session, row),
                      lambda: create_strategy(session, plan(), actor="a", reason="test", revision_of=row.id),
                      lambda: build_context(session, "SN", D, researcher_id=a.id)):
        with pytest.raises(PermissionError):
            operation()
    context, blockers = build_context(session, "SN", D, researcher_id=b.id)
    assert context["research"] is None and any("研究判断" in s for s in blockers)


def test_import_preview_cannot_be_committed_by_another_account(session):
    login(session, "a")
    preview = build_preview(session, xlsx([["日期", "SMM 1# 锡现货"], ["2026-09-18", 406000]]),
                            "source.xlsx", "forged", "SN")
    login(session, "b")
    with pytest.raises(PermissionError):
        commit_preview(session, preview["preview_id"], [], {}, "a", "test")


@contextmanager
def identity(user="a", tenant=1):
    token = current_identity.set(Identity(user, f"{user}@example.com", user, tenant, "owner"))
    try:
        yield
    finally:
        current_identity.reset(token)


def test_import_staging_and_job_status_isolate_user_and_workspace(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, "staging_dir", tmp_path)
    with identity():
        token = import_jobs.stage(b"test")
        path = import_jobs.staging_path(token)
        job_id = import_jobs.create_terminal_job(token, "forged", "source.xlsx")
        assert import_jobs.snapshot(job_id)["actor"] == "a"
    for user, tenant in (("b", 1), ("a", 2)):
        with identity(user, tenant):
            assert import_jobs.snapshot(job_id) is None
            assert not import_jobs.staging_path(token).exists()
            with pytest.raises(PermissionError):
                import_jobs.execute(job_id)
    with identity():
        assert path.read_bytes() == b"test" and import_jobs.snapshot(job_id) is not None


def test_background_import_restores_verified_identity(tmp_path, monkeypatch, session):
    from sqlalchemy.orm import sessionmaker
    from tin.ingest.vendor_terminal import LoadReport
    monkeypatch.setattr(settings, "staging_dir", tmp_path)
    observed = []

    def factory():
        observed.append(current_identity.get())
        return sessionmaker(session.get_bind(), expire_on_commit=False)()

    def load(*args, **kwargs):
        observed.append(current_identity.get())
        return LoadReport()

    monkeypatch.setattr("tin.db.SessionLocal", factory)
    monkeypatch.setattr("tin.ingest.vendor_terminal.load", load)
    with identity("a", 7):
        token = import_jobs.stage(b"test")
        job_id = import_jobs.create_terminal_job(token, "forged", "source.xlsx")
    assert current_identity.get() is None
    import_jobs.execute(job_id)
    assert current_identity.get() is None
    assert all(i.user_id == "a" and i.tenant_id == 7 for i in observed)
    with identity("a", 7):
        assert import_jobs.snapshot(job_id)["state"] == "done"
        assert not import_jobs.staging_path(token).exists()


def test_migration_preserves_legacy_research_with_foreign_keys(tmp_path):
    from alembic import command
    from alembic.config import Config
    from tin.db import make_engine
    cfg = Config()
    cfg.set_main_option("script_location", str(ROOT / "alembic"))
    cfg.set_main_option("sqlalchemy.url", f"sqlite:///{tmp_path / 'legacy.db'}")
    command.upgrade(cfg, "aa91f2283c14")
    engine = make_engine(cfg.get_main_option("sqlalchemy.url"))
    with engine.begin() as conn:
        conn.execute(text("INSERT INTO researchers (id,username,display_name,varieties,status,created_at) "
                          "VALUES (1,'me','legacy','[]','在职','2026-09-18')"))
        conn.execute(text("INSERT INTO playbook_items (researcher_id,variety,text,scope,priority,source,source_ref,"
                          "status,version,used_count,created_at,updated_at) "
                          "VALUES (1,'SN','private','{}',100,'手写','{}','生效',1,0,'2026-09-18','2026-09-18')"))
        conn.execute(text("INSERT INTO strategies (id,title,variety,kind,mode,status,plan,author,version,created_at,entry_fills,exit_fills) "
                          "VALUES (1,'legacy','SN','outright','simulation','draft','{}','me',1,'2026-09-18','[]','[]')"))
        conn.execute(text("INSERT INTO strategy_events (strategy_id,event_type,actor,reason,at,payload) "
                          "VALUES (1,'created','me','legacy','2026-09-18','{}')"))
    command.upgrade(cfg, "head")
    with engine.connect() as conn:
        assert conn.scalar(text("SELECT text FROM playbook_items")) == "private"
        assert conn.scalar(text("SELECT actor FROM strategy_events")) == "me"
        assert conn.scalar(text("SELECT researcher_id FROM strategies")) is None
        assert conn.scalar(text("SELECT weknora_user_id FROM researchers")) is None
        assert conn.execute(text("PRAGMA foreign_key_check")).all() == []
