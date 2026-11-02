"""Personal notification dismissal and bounded private analysis results."""
from types import SimpleNamespace
from datetime import date, datetime, timezone
from copy import deepcopy

from fastapi import HTTPException
import pytest
from sqlalchemy import select

from tests.test_judgment_analysis_jobs_isolation import (
    job_case, active, completed, run, SOURCE,
)
from tests.test_diagram_background_jobs import case, completed as diagram_completed, run as diagram_run
from tin.integration.workbench_scope import artifact_session
from tin.models import Judgment, SourceMapping, WorkbenchArtifact
from tin.web import judgment_jobs as jobs
from tin.web.judgment_job_registry import Registry, metadata


def simple_scope(user='owner', tenant=17, workbench='workbench'):
    return SimpleNamespace(identity=SimpleNamespace(user_id=user, tenant_id=tenant),
        workbench_id=workbench, metadata={})


def registry_job(index, scope, key, purpose='judgments', state='succeeded'):
    row, _ = index.create(scope, key, key, 'worker-' + key, purpose)
    if state != 'queued':
        index.finish(row['id'], row['worker_id'], row['fence'], state)
    return index.get(scope.identity, row['id'])


def test_dismissal_is_owner_scoped_persistent_idempotent_and_never_cancels_active_work(tmp_path):
    path = tmp_path / 'index.sqlite'
    index, scope = Registry(path), simple_scope()
    running = registry_job(index, scope, 'first', state='queued')
    with pytest.raises(HTTPException) as active_error:
        index.dismiss(scope.identity, running['id'])
    assert active_error.value.status_code == 409
    index.finish(running['id'], running['worker_id'], running['fence'], 'failed')
    with pytest.raises(HTTPException) as other_error:
        index.dismiss(simple_scope(user='other').identity, running['id'])
    assert other_error.value.status_code == 404
    first = index.dismiss(scope.identity, running['id'])
    second = Registry(path).dismiss(scope.identity, running['id'])
    assert first['dismissed'] is True and first['dismissed_at'] == second['dismissed_at']
    assert Registry(path).listing(scope.identity)[0]['dismissed'] is True
    assert metadata(first)['dismissed'] is True and first['state'] == 'failed'


def test_only_new_success_replaces_the_previous_result_and_reconciliation_cannot_resurrect_it(tmp_path):
    index, scope = Registry(tmp_path / 'index.sqlite'), simple_scope()
    first = registry_job(index, scope, 'first')
    failed = registry_job(index, scope, 'failed', state='failed')
    assert index.latest(scope.identity, scope.workbench_id, 'judgments')['id'] == first['id']
    assert index.get(scope.identity, failed['id'])['state'] == 'failed'
    second = registry_job(index, scope, 'second')
    old = index.get(scope.identity, first['id'])
    assert old['state'] == 'superseded' and old['dismissed'] is True
    assert old['superseded_by'] == second['id']
    assert index.latest(scope.identity, scope.workbench_id, 'judgments')['id'] == second['id']
    assert index.reconcile(scope.identity, first['id'], scope.workbench_id, 'succeeded')['state'] == 'superseded'


def test_latest_results_and_dismissal_are_partitioned_by_owner_tenant_workbench_and_purpose(tmp_path):
    index, owner = Registry(tmp_path / 'index.sqlite'), simple_scope()
    originals = []
    for scope, purpose in [(owner, 'judgments'), (owner, 'diagram'),
            (simple_scope(user='other'), 'judgments'), (simple_scope(tenant=18), 'judgments'),
            (simple_scope(workbench='other'), 'judgments')]:
        originals.append((scope, purpose, registry_job(index, scope,
            scope.identity.user_id + scope.workbench_id + str(scope.identity.tenant_id) + purpose, purpose)))
    replacement = registry_job(index, owner, 'replacement')
    for scope, purpose, old in originals:
        latest = index.latest(scope.identity, scope.workbench_id, purpose)
        assert latest['id'] == (replacement['id'] if scope is owner and purpose == 'judgments' else old['id'])


@pytest.mark.parametrize('job_case', ['legacy', 'isolated'], indirect=True)
def test_replaced_judgment_preview_rejects_old_reads_updates_and_save_without_touching_saved_records(job_case):
    async def scenario():
        first_id, first = await completed(job_case, key='synthetic-old-request')
        first_token = first['draft_id']
        with active(job_case.first), artifact_session(job_case.first) as session:
            previous = jobs.load_preview(job_case.first, first_token)
            now = datetime.now(timezone.utc)
            session.add(Judgment(variety='ZZ', version=1, author=job_case.first.identity.user_id,
                written_at=date(2030, 1, 2), review_due=date(2030, 2, 2), contradiction={},
                marginal_focus=[], pricing_power={}, whitelist=[], thresholds=[], falsifiers=[],
                created_at=now))
            session.add(WorkbenchArtifact(id='saved-artifact', module='judgments',
                actor=job_case.first.identity.user_id, title='Already saved research',
                body='Synthetic saved research body', status='草稿', source_ids=[],
                created_at=now, updated_at=now))
            session.commit()
        second_id, second = await completed(job_case, key='synthetic-new-request')
        with active(job_case.first):
            assert jobs.list_jobs(job_case.first.identity)['items'][0]['id'] == second_id
            status = await jobs.job_status(job_case.first, first_id)
            assert status['state'] == 'superseded' and not status['result_available']
            assert (await jobs.latest_job(job_case.first))['job']['id'] == second_id
            for operation in [lambda: jobs.load_preview(job_case.first, first_token),
                    lambda: jobs.atomic_replace(job_case.first, first_token, {}, 1)]:
                with pytest.raises(HTTPException) as gone:
                    operation()
                assert gone.value.status_code in {409, 410}
            with pytest.raises(HTTPException) as old_result:
                await jobs.job_result(job_case.first, first_id)
            assert old_result.value.status_code in {409, 410}
            with artifact_session(job_case.first) as session, pytest.raises(HTTPException) as old_save:
                jobs.mark_applied(session, job_case.first, previous)
            assert old_save.value.status_code in {409, 410}
            with artifact_session(job_case.first) as session:
                stale = session.get(SourceMapping, jobs.job_key(job_case.first, first_id))
                assert stale.config.get('preview') is None and stale.config.get('input') in (None, {})
                assert SOURCE not in str(stale.config)
                assert len(session.scalars(select(Judgment)).all()) == 1
                assert session.get(WorkbenchArtifact, 'saved-artifact').body == 'Synthetic saved research body'
            assert (await jobs.job_result(job_case.first, second_id))['markdown'] == second['markdown']
    run(job_case, scenario)


def test_dismiss_latest_result_keeps_its_payload_and_recovery_entry(job_case):
    async def scenario():
        identifier, result = await completed(job_case)
        with active(job_case.first):
            dismissed = jobs.dismiss_job(job_case.first.identity, identifier)
            assert dismissed['dismissed'] is True
            assert (await jobs.latest_job(job_case.first))['job']['id'] == identifier
            assert (await jobs.latest_job(job_case.first))['job']['dismissed'] is True
            assert (await jobs.job_result(job_case.first, identifier))['draft_id'] == result['draft_id']
    run(job_case, scenario)


def test_new_failed_analysis_does_not_destroy_the_latest_usable_result(job_case):
    async def scenario():
        first_id, first = await completed(job_case)
        job_case.state['fail_analysis'] = True
        with active(job_case.first):
            from tin.web.judgment_generation import GenerateRequest
            newest = await jobs.start_job(job_case.first, GenerateRequest(model_id='synthetic-model',
                text=SOURCE, base_version=0), 'synthetic-failed-new-request')
            await jobs.runtime.tasks[newest['id']]
            assert (await jobs.job_status(job_case.first, newest['id']))['state'] == 'failed'
            assert (await jobs.latest_job(job_case.first))['job']['id'] == first_id
            assert (await jobs.job_result(job_case.first, first_id))['draft_id'] == first['draft_id']
    run(job_case, scenario)


def test_legacy_multiple_successes_cannot_use_old_tokens_before_or_after_index_compaction(job_case, monkeypatch):
    async def scenario():
        compact = jobs.registry.compact
        monkeypatch.setattr(jobs.registry, 'compact', lambda connection: [])
        old_id, first = await completed(job_case, key='synthetic-legacy-first')
        token = first['draft_id']
        with active(job_case.first):
            stale = jobs.load_preview(job_case.first, token)
        current_id, _ = await completed(job_case, key='synthetic-legacy-current')
        monkeypatch.setattr(jobs.registry, 'compact', compact)
        with active(job_case.first):
            for after_compaction in (False, True):
                if after_compaction:
                    jobs.list_jobs(job_case.first.identity)
                with pytest.raises(HTTPException) as old_read:
                    jobs.load_preview(job_case.first, token)
                assert old_read.value.status_code in {409, 410}
                updated = deepcopy(stale)
                updated['review']['revision'] += 1
                with pytest.raises(HTTPException) as old_update:
                    jobs.atomic_replace(job_case.first, token, updated, 1)
                assert old_update.value.status_code in {409, 410}
                with artifact_session(job_case.first) as session, pytest.raises(HTTPException) as old_apply:
                    jobs.mark_applied(session, job_case.first, stale)
                assert old_apply.value.status_code in {409, 410}
            jobs.cleanup_expired()
            with artifact_session(job_case.first) as session:
                assert session.get(SourceMapping, jobs.job_key(job_case.first, old_id)).config['preview'] is None
            assert (await jobs.latest_job(job_case.first))['job']['id'] == current_id
    run(job_case, scenario)


def test_new_diagram_result_replaces_old_private_payload_but_dismiss_keeps_latest(case):
    async def scenario():
        first_id, first = await diagram_completed(case)
        second_id, second = await diagram_completed(case, key='synthetic-new-diagram')
        with active(case.first):
            assert (await case.jobs.latest_job(case.first))['job']['id'] == second_id['id']
            with pytest.raises(HTTPException) as old:
                await case.jobs.job_result(case.first, first_id['id'])
            assert old.value.status_code in {409, 410}
            with artifact_session(case.first) as session:
                row = session.get(SourceMapping, case.jobs.job_key(case.first, first_id['id']))
                assert row.config.get('result') is None and row.config.get('request') in (None, {})
            jobs.dismiss_job(case.first.identity, second_id['id'])
            assert (await case.jobs.latest_job(case.first))['job']['dismissed'] is True
            assert (await case.jobs.job_result(case.first, second_id['id']))['markdown'] == second['markdown']
    diagram_run(case, scenario)
