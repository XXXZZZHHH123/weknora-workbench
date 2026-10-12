"""One auditable pipeline for scheduled and manual shared-fact refreshes."""
import fcntl
import hashlib
import tempfile
import logging
import re
import uuid
from contextlib import contextmanager
from datetime import date, datetime, time, timedelta, timezone
from pathlib import Path

from sqlalchemy import func, select
from sqlalchemy.exc import IntegrityError

from tin.config import SHANGHAI, settings
from tin.db import SystemSession, tenant_database_path
from tin.models import FetchRun, Indicator, Observation, Researcher
from tin.models.refresh import RefreshRun, RefreshSettings
from tin.compute.engine import compute_day, latest_trade_date
from tin.compute.signals import evaluate_signals
from tin.ingest.runner import FETCHERS, fetch_all

log = logging.getLogger(__name__)

SOURCE_NAMES = {"shfe_quotes": "上期所行情", "shfe_warrant": "上期所仓单",
                "shfe_weekly_stock": "上期所库存", "cfets_fx": "人民币中间价",
                "cboe_vix": "CBOE 波动率指数", "fred_macro": "FRED 宏观数据"}
DEFAULT_SOURCES = tuple(f.name for f in FETCHERS)
DEFAULT_TIMES = ["08:00", "16:30"]
STATUS_LABELS = {"pending": "等待更新", "running": "正在更新", "success": "更新成功",
                 "partial": "部分成功", "failed": "更新失败", "not_published": "尚未发布",
                 "interrupted": "更新已中断"}


class RefreshBusy(ValueError):
    pass


def utc_now():
    return datetime.now(timezone.utc)


def validate_sources(sources):
    if (not isinstance(sources, list) or not sources or len(sources) > len(DEFAULT_SOURCES)
            or any(not isinstance(source, str) or source not in DEFAULT_SOURCES for source in sources)
            or len(set(sources)) != len(sources)):
        raise ValueError("请至少选择一个已接入的官方来源，来源不能重复")
    return list(sources)


def validate_plan(enabled, times, sources):
    if not isinstance(enabled, bool) or not isinstance(times, list) or not isinstance(sources, list):
        raise ValueError("请提供启停开关、更新时间和来源列表")
    if not 1 <= len(times) <= 6 or any(not isinstance(t, str) or not re.fullmatch(r"(?:[01]\d|2[0-3]):[0-5]\d", t) for t in times):
        raise ValueError("每天可设置 1 至 6 个更新时间，格式 HH:MM（北京时间）")
    if len(set(times)) != len(times):
        raise ValueError("更新时间不能重复")
    return sorted(times), validate_sources(sources)


def next_slot(now: datetime, times: list[str]):
    if now.tzinfo is None:
        raise ValueError("更新时间必须带时区")
    local = now.astimezone(SHANGHAI)
    slots = [datetime.combine(local.date() + timedelta(days=offset), time.fromisoformat(t), SHANGHAI)
             for offset in (0, 1) for t in times]
    return min(slot for slot in slots if slot > local).astimezone(timezone.utc)


def ensure_plan(session, now=None):
    plan = session.get(RefreshSettings, 1)
    if plan is not None:
        return plan
    now = now or utc_now()
    values = dict(id=1, enabled=True, times=list(DEFAULT_TIMES), sources=list(DEFAULT_SOURCES),
                  next_run_at=next_slot(now, DEFAULT_TIMES), updated_at=now, updated_by="系统初始化")
    if session.bind.dialect.name == "sqlite":
        from sqlalchemy.dialects.sqlite import insert
        session.execute(insert(RefreshSettings).values(**values).on_conflict_do_nothing(index_elements=["id"]))
        session.commit()
        return session.get(RefreshSettings, 1)
    try:
        with session.begin_nested():
            session.add(RefreshSettings(**values))
            session.flush()
        session.commit()
    except IntegrityError:
        session.expire_all()
    return session.get(RefreshSettings, 1)


def update_plan(session, *, enabled, times, sources, actor, now=None):
    times, sources = validate_plan(enabled, times, sources)
    plan = ensure_plan(session, now)
    now = now or utc_now()
    plan.enabled, plan.times, plan.sources = enabled, times, sources
    plan.next_run_at = next_slot(now, times) if enabled else None
    plan.updated_at, plan.updated_by = now, actor
    session.commit()
    return plan


@contextmanager
def workspace_lock(tenant_id: int):
    if tenant_id == 0 and not settings.weknora_api_url:
        from sqlalchemy.engine import make_url
        url = make_url(settings.database_url)
        if url.get_backend_name() == "sqlite" and url.database and url.database != ":memory:" and url.query.get("mode") != "memory":
            path = Path(url.database)
        else:
            # In-memory/other standalone databases have no usable filesystem
            # database pathname. A stable private temp lock avoids sqlite:.lock
            # artifacts in the checkout and never exposes DSN credentials.
            digest = hashlib.sha256((settings.database_url + str(settings.data_dir)).encode()).hexdigest()
            path = Path(tempfile.gettempdir()) / "weknora-research-locks" / f"{digest}.db"
    else:
        path = tenant_database_path(tenant_id)
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.with_suffix(".refresh.lock").open("a") as handle:
        try:
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RefreshBusy("当前空间正在更新，稍后查看结果即可") from None
        try:
            yield
        finally:
            fcntl.flock(handle, fcntl.LOCK_UN)


def system_session(tenant_id):
    if tenant_id == 0 and not settings.weknora_api_url:
        from tin.db import SessionLocal
        session = SessionLocal()
        session.info.update(system_refresh=True, system_actor="自动刷新")
        return session
    return SystemSession(tenant_id)


def current_run(session):
    return session.scalar(select(RefreshRun).where(RefreshRun.status.in_(["pending", "running"]))
                          .order_by(RefreshRun.started_at.desc()).limit(1))


def queue_run(session, *, trigger="manual", actor="自动刷新", scheduled_for=None, now=None, sources=None):
    """Call under workspace_lock. The committed pending row de-duplicates workers."""
    if sources is not None:
        if trigger != "manual":
            raise ValueError("自动更新按空间计划执行，不能临时替换来源")
        sources = validate_sources(sources)
    now = now or utc_now()
    active = current_run(session)
    if active:
        # The OS lock is free here; a running record belongs to a crashed process.
        # Pending records are re-submitted by the clock after restart. Never expire
        # them by age: a busy worker pool may legitimately queue a large workspace.
        if active.status == "running":
            active.status, active.finished_at = "interrupted", now
            active.error = "上次更新进程已退出，可重新更新；已保存事实和原件仍保留。"
            session.commit()
        else:
            return active, False
    plan = ensure_plan(session, now)
    if trigger == "scheduled":
        if not plan.enabled or plan.next_run_at is None or plan.next_run_at > now:
            return None, False
        scheduled_for = plan.next_run_at
        existing = session.scalar(select(RefreshRun).where(RefreshRun.scheduled_for == scheduled_for))
        if existing:
            plan.next_run_at = next_slot(now, plan.times)
            session.commit()
            return existing, False
    run = RefreshRun(id=uuid.uuid4().hex, trigger=trigger, status="pending", started_at=now,
                     scheduled_for=scheduled_for, actor=actor, sources=list(plan.sources if sources is None else sources), results=[])
    session.add(run)
    if trigger == "scheduled":
        # Skip old missed slots rather than issue a burst of duplicate downloads.
        plan.next_run_at = next_slot(now, plan.times)
    session.commit()
    return run, True


def source_target(source: str, now: datetime):
    local = now.astimezone(SHANGHAI)
    target = local.date()
    if source.startswith("shfe_"):
        if local.time() < time(16):
            target -= timedelta(days=1)
        while target.weekday() >= 5:
            target -= timedelta(days=1)
    return target


def data_cutoff(session, source):
    predicates = {
        "shfe_quotes": Observation.series_id.like("SHFE.%.%.settle"),
        "shfe_warrant": Observation.series_id.like("SHFE.%.warrant"),
        "shfe_weekly_stock": Observation.series_id.like("SHFE.%.stock.weekly"),
        "cfets_fx": Observation.series_id == "FX.USDCNY.mid",
        "cboe_vix": Observation.series_id == "MACRO.VIX",
        "fred_macro": Observation.series_id.like("FRED.%"),
    }
    return session.scalar(select(func.max(Observation.as_of)).where(predicates[source]))


def serialize_run(run):
    if run is None:
        return None
    return {"id": run.id, "trigger": run.trigger, "status": run.status,
            "label": STATUS_LABELS.get(run.status, run.status), "started_at": run.started_at,
            "finished_at": run.finished_at, "actor": run.actor, "results": run.results,
            "error": run.error, "latest_trade_date": run.latest_trade_date}


def refresh_state(session):
    plan = ensure_plan(session)
    latest = session.scalar(select(RefreshRun).order_by(RefreshRun.started_at.desc()).limit(1))
    last_success = session.scalar(select(RefreshRun).where(RefreshRun.status == "success")
                                 .order_by(RefreshRun.finished_at.desc()).limit(1))
    active = current_run(session)
    sources = []
    for key in DEFAULT_SOURCES:
        last = session.scalar(select(FetchRun).where(FetchRun.fetcher == key).order_by(FetchRun.id.desc()).limit(1))
        success = session.scalar(select(FetchRun).where(FetchRun.fetcher == key,
            FetchRun.status == "ok").order_by(FetchRun.id.desc()).limit(1))
        sources.append({"id": key, "name": SOURCE_NAMES[key], "enabled": key in plan.sources,
                        "status": last.status if last else "尚未尝试", "error": last.error if last else None,
                        "last_attempt": last.started_at if last else None,
                        "last_success": success.finished_at if success else None,
                        "data_cutoff": data_cutoff(session, key), "written": last.written if last else 0})
    automatic_available = bool(settings.weknora_api_url and settings.refresh_scheduler_enabled)
    return {"enabled": plan.enabled, "times": plan.times, "timezone": "Asia/Shanghai",
            "scheduler_available": automatic_available,
            "automatic_active": bool(plan.enabled and automatic_available),
            "next_run_at": plan.next_run_at if automatic_available else None, "last_attempt": serialize_run(latest),
            "last_success": last_success.finished_at if last_success else None,
            "active": serialize_run(active), "sources": sources,
            "latest_trade_date": latest_trade_date(session),
            "notice": "自动与手动更新只处理共享事实、派生指标和各账号的确定性信号；不改个人判断、不覆盖已审核简报、不调用收费模型。"}


def execute_refresh(tenant_id: int, run_id: str, *, now=None, session_factory=None):
    """No user token/password and no LLM. The lock spans fetch, commit and compute."""
    try:
        with workspace_lock(tenant_id):
            with (session_factory() if session_factory else system_session(tenant_id)) as session:
                run = session.get(RefreshRun, run_id)
                if run is None or run.status != "pending":
                    return
                run.status = "running"
                session.commit()
                results = []
                try:
                    reference = now or utc_now()
                    for source in run.sources:
                        fetched = fetch_all(session, source_target(source, reference), only={source})
                        for item in fetched:
                            results.append({"source": source, "name": SOURCE_NAMES[source], "status": item.status,
                                            "target_date": item.target_date, "written": item.written,
                                            "error": item.error, "fetch_run_id": item.id,
                                            "data_cutoff": (data_cutoff(session, source).isoformat()
                                                            if data_cutoff(session, source) else None)})
                        run.results = list(results)
                        session.commit()
                    day = latest_trade_date(session)
                    if day is not None:
                        compute_day(session, day)
                        # The system owns the refresh. Scope each deterministic check
                        # to its existing researcher; never synthesize a user identity.
                        signal_failures = 0
                        for researcher in session.scalars(select(Researcher).where(Researcher.status == "在职")).all():
                            session.info["researcher_scope_id"] = researcher.id
                            try:
                                evaluate_signals(session, settings.variety, day, today=reference.astimezone(SHANGHAI).date())
                            except Exception:
                                session.rollback()
                                signal_failures += 1
                                # Personal rule expressions may contain private research;
                                # the shared status shows only the failure count.
                                log.exception("Workspace %s researcher %s signal refresh failed", tenant_id, researcher.id)
                            finally:
                                session.info.pop("researcher_scope_id", None)
                        run.latest_trade_date = day.isoformat()
                        if signal_failures:
                            run.error = f"共享事实已更新，但 {signal_failures} 个账号的信号检查未完成。"
                    statuses = {item["status"] for item in results}
                    if statuses and statuses <= {"ok"}:
                        run.status = "success"
                    elif "ok" in statuses or "部分失败" in statuses:
                        run.status = "partial"
                    elif statuses and statuses <= {"未发布"}:
                        run.status = "not_published"
                    else:
                        run.status = "failed"
                    if run.error and run.status == "success":
                        run.status = "partial"
                    run.finished_at = utc_now()
                    session.commit()
                except Exception as exc:
                    session.rollback()
                    run = session.get(RefreshRun, run_id)
                    run.status = "partial" if any(item["status"] in {"ok", "部分失败"} for item in results) else "failed"
                    run.finished_at = utc_now()
                    run.results, run.error = list(results), f"{type(exc).__name__}: {exc}"[:4000]
                    session.commit()
    except RefreshBusy:
        # Another process has acquired the same pending run. Its durable state
        # remains visible, so this worker need not start another pipeline.
        return
