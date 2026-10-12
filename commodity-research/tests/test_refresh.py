"""Shared refreshes must be scheduled, auditable and owner-safe."""
import subprocess
import sys
from datetime import date, datetime, timezone
import pytest
from sqlalchemy import func, select
from tin.config import SHANGHAI, settings
from tin.models import DailyBrief, FetchRun, Judgment, LlmCall, Researcher, Signal
from tin.models.refresh import RefreshRun, RefreshSettings
from tin.refresh import service
from tin.refresh.scheduler import RefreshScheduler
NOW = datetime(2026, 10, 12, 8, tzinfo=SHANGHAI)


def test_schedule_uses_shanghai_and_pause_resume(session):
    plan = service.ensure_plan(session, datetime(2026, 10, 11, 23, 59, tzinfo=timezone.utc))
    assert plan.enabled and plan.times == ["08:00", "16:30"]
    assert plan.next_run_at == datetime(2026, 10, 12, 0, tzinfo=timezone.utc)
    assert service.next_slot(NOW, plan.times) == datetime(2026, 10, 12, 8, 30, tzinfo=timezone.utc)
    service.update_plan(session, enabled=False, times=["09:15"], sources=["shfe_quotes"], actor="user", now=NOW)
    assert plan.next_run_at is None
    service.update_plan(session, enabled=True, times=["09:15"], sources=["shfe_quotes"], actor="user", now=NOW)
    assert plan.next_run_at == datetime(2026, 10, 12, 1, 15, tzinfo=timezone.utc)


@pytest.mark.parametrize("times,sources", [(["24:00"], ["shfe_quotes"]), (["8:00"], ["shfe_quotes"]),
    (["08:00", "08:00"], ["shfe_quotes"]), (["08:00"], ["untrusted_http_url"]), (["08:00"], [])])
def test_only_valid_local_times_and_registered_sources(times, sources):
    with pytest.raises(ValueError): service.validate_plan(True, times, sources)


def test_morning_uses_last_published_shfe_day_and_fx_keeps_today():
    assert service.source_target("shfe_quotes", NOW).isoformat() == "2026-10-09"
    assert service.source_target("cfets_fx", NOW).isoformat() == "2026-10-12"
    assert service.source_target("shfe_quotes", NOW.replace(hour=16, minute=30)).isoformat() == "2026-10-12"


def test_workspace_lock_excludes_another_process(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, "data_dir", tmp_path)
    script = "import fcntl,sys\nf=open(sys.argv[1],'a')\ntry: fcntl.flock(f,fcntl.LOCK_EX|fcntl.LOCK_NB)\nexcept BlockingIOError: sys.exit(27)\n"
    with service.workspace_lock(55):
        lock = tmp_path / "workspaces" / "55" / "tin.refresh.lock"
        assert subprocess.run([sys.executable,"-c",script,str(lock)],check=False).returncode == 27
    assert subprocess.run([sys.executable,"-c",script,str(lock)],check=False).returncode == 0


def prepare_run(session, sources):
    service.update_plan(session, enabled=True, times=["08:00", "16:30"], sources=sources, actor="admin", now=NOW)
    run, created = service.queue_run(session, trigger="manual", actor="verified-user", now=NOW)
    assert created
    return run


def fake_fetch(statuses):
    def fetch(session, target, only):
        source = next(iter(only))
        result = FetchRun(fetcher=source, target_date=target.isoformat(), started_at=NOW,
            finished_at=NOW, status=statuses[source], attempts=1, written=3 if statuses[source]=="ok" else 0,
            error="not yet available" if statuses[source]!="ok" else None)
        session.add(result); session.commit()
        return [result]
    return fetch


@pytest.mark.parametrize("statuses,expected", [({"shfe_quotes":"ok"},"success"),
    ({"shfe_quotes":"未发布"},"not_published"), ({"shfe_quotes":"失败"},"failed"),
    ({"shfe_quotes":"ok","cfets_fx":"失败"},"partial"), ({"shfe_quotes":"部分失败"},"partial")])
def test_pipeline_distinguishes_unpublished_partial_failure(session,tmp_path,monkeypatch,statuses,expected):
    monkeypatch.setattr(settings,"data_dir",tmp_path)
    monkeypatch.setattr(service,"fetch_all",fake_fetch(statuses))
    run=prepare_run(session,list(statuses)); session.info.update(system_refresh=True,tenant_id=55)
    service.execute_refresh(55,run.id,now=NOW,session_factory=lambda:session)
    assert run.status==expected and run.finished_at is not None
    state=service.refresh_state(session)
    assert state["last_attempt"]["status"]==expected
    assert (state["last_success"] is not None)==(expected=="success")
    for source in statuses:
        shown=next(row for row in state["sources"] if row["id"]==source)
        assert shown["status"]==statuses[source] and shown["last_attempt"] is not None


def test_manual_and_due_slot_share_one_pending_run_and_no_catchup_burst(session):
    service.ensure_plan(session,NOW.replace(hour=7))
    first,created=service.queue_run(session,trigger="scheduled",now=NOW)
    assert created and first.trigger=="scheduled"
    manual,created=service.queue_run(session,trigger="manual",actor="user",now=NOW)
    assert not created and manual.id==first.id
    assert session.get(RefreshSettings,1).next_run_at==NOW.replace(hour=16,minute=30).astimezone(timezone.utc)
    first.status,first.finished_at="success",NOW;session.commit()
    repeated,created=service.queue_run(session,trigger="scheduled",now=NOW)
    assert repeated is None and not created
    assert session.scalar(select(func.count()).select_from(RefreshRun))==1


def test_system_refresh_checks_each_owner_preserves_reviewed_brief(loaded,tmp_path,monkeypatch):
    from tests.test_signals import activate
    from tests.test_brief_store import _result
    from tin.brief import store
    from tin.jobs.seed import seed_researcher
    monkeypatch.setattr(settings,"data_dir",tmp_path)
    monkeypatch.setattr(service,"fetch_all",fake_fetch({"shfe_quotes":"ok"}))
    judgments=[]
    for user in ("alice","bob"):
        loaded.info.update(weknora_user_id=user,weknora_email=f"{user}@example.test",weknora_display_name=user)
        researcher=seed_researcher(loaded);loaded.commit();judgments.append(activate(loaded))
        if user=="alice":
            brief=store.save(loaded,_result(),researcher_id=researcher.id,variety="SN",trade_date=date(2026,9,18))
            store.review(loaded,brief.id,actor="ignored",action="改",changes={"tone":"偏空"},reason="人工判断")
            brief_id=brief.id
    originals=[(j.id,j.status,j.version,j.author) for j in judgments]
    loaded.info.clear();loaded.info.update(system_refresh=True,tenant_id=55,system_actor="自动刷新")
    run=prepare_run(loaded,["shfe_quotes"])
    service.execute_refresh(55,run.id,now=NOW,session_factory=lambda:loaded)
    assert run.status=="success",run.error
    assert set(loaded.scalars(select(Signal.judgment_id)))=={j.id for j in judgments}
    assert loaded.scalar(select(Researcher).where(Researcher.username=="me")) is None
    assert loaded.scalar(select(func.count()).select_from(LlmCall))==0
    assert [(j.id,j.status,j.version,j.author) for j in loaded.scalars(select(Judgment).order_by(Judgment.id))]==originals
    saved=loaded.get(DailyBrief,brief_id)
    assert saved.tone=="偏空" and saved.status=="已修改"
    assert "weknora_user_id" not in loaded.info


def test_scheduler_uses_only_existing_tenant_db(tmp_path,monkeypatch,session):
    monkeypatch.setattr(settings,"data_dir",tmp_path)
    base=tmp_path/"workspaces";(base/"55").mkdir(parents=True);(base/"55"/"tin.db").touch()
    (base/"bad").mkdir();(base/"66").mkdir()
    service.ensure_plan(session,NOW.replace(hour=7))
    import tin.refresh.scheduler as module
    monkeypatch.setattr(module,"system_session",lambda tenant:session)
    scheduled=[];scheduler=RefreshScheduler()
    monkeypatch.setattr(scheduler,"submit",lambda tenant,run:scheduled.append((tenant,run)))
    scheduler.tick(NOW)
    assert len(scheduled)==1 and scheduled[0][0]==55
    assert not (base/"66"/"tin.db").exists()


def test_pipeline_exception_is_durable_and_preserves_previous_success(session,tmp_path,monkeypatch):
    monkeypatch.setattr(settings,"data_dir",tmp_path)
    monkeypatch.setattr(service,"fetch_all",fake_fetch({"shfe_quotes":"ok"}))
    first=prepare_run(session,["shfe_quotes"])
    session.info.update(system_refresh=True,tenant_id=55)
    service.execute_refresh(55,first.id,now=NOW,session_factory=lambda:session)
    first_success=first.finished_at
    second,_=service.queue_run(session,trigger="manual",now=NOW.replace(hour=9))
    def fail(*args,**kwargs): raise RuntimeError("upstream temporarily unavailable")
    monkeypatch.setattr(service,"fetch_all",fail)
    service.execute_refresh(55,second.id,now=NOW,session_factory=lambda:session)
    state=service.refresh_state(session)
    assert state["last_attempt"]["status"]=="failed"
    assert "temporarily unavailable" in state["last_attempt"]["error"]
    assert state["last_success"]==first_success


def test_orphaned_running_record_can_recover_under_released_os_lock(session):
    run=prepare_run(session,["shfe_quotes"])
    run.status="running";session.commit()
    new,created=service.queue_run(session,trigger="manual",now=NOW.replace(hour=9))
    assert created and new.id!=run.id
    assert run.status=="interrupted" and run.finished_at is not None


def test_one_personal_signal_failure_does_not_block_other_owners_or_leak_rules(loaded,tmp_path,monkeypatch):
    monkeypatch.setattr(settings,"data_dir",tmp_path)
    monkeypatch.setattr(service,"fetch_all",fake_fetch({"shfe_quotes":"ok"}))
    for name in ('first','second'):
        loaded.add(Researcher(username=name,display_name=name,varieties=['SN'],status='在职',created_at=NOW))
    loaded.commit()
    people=list(loaded.scalars(select(Researcher).order_by(Researcher.id)))
    checked=[]
    def signals(session,*args,**kwargs):
        owner=session.info['researcher_scope_id'];checked.append(owner)
        if owner==people[0].id: raise ValueError('PRIVATE_RULE_EXPRESSION')
        return [],[]
    monkeypatch.setattr(service,'evaluate_signals',signals)
    loaded.info.update(system_refresh=True,tenant_id=55,system_actor='自动刷新')
    run=prepare_run(loaded,['shfe_quotes'])
    service.execute_refresh(55,run.id,now=NOW,session_factory=lambda:loaded)
    assert checked==[person.id for person in people]
    assert run.status=='partial' and '1 个账号' in run.error
    assert 'PRIVATE_RULE_EXPRESSION' not in run.error
    assert 'researcher_scope_id' not in loaded.info



def test_pending_job_is_not_expired_just_because_the_pool_is_busy(session):
    first=prepare_run(session,['cfets_fx'])
    second,created=service.queue_run(session,trigger='manual',now=NOW.replace(hour=16))
    assert not created and second.id==first.id
    assert first.status=='pending' and first.sources==['cfets_fx']


def test_disabled_global_clock_does_not_claim_a_next_automatic_run(session,monkeypatch):
    monkeypatch.setattr(settings,'weknora_api_url','http://weknora.internal')
    monkeypatch.setattr(settings,'refresh_scheduler_enabled',False)
    service.ensure_plan(session,NOW)
    shown=service.refresh_state(session)
    assert shown['enabled'] and not shown['scheduler_available'] and not shown['automatic_active']
    assert shown['next_run_at'] is None
