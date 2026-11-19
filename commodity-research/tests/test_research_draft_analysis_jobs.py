"""Exercise the shared scheduler, private result store and affairs links offline."""
import asyncio
from dataclasses import replace
from urllib.parse import parse_qs, urlsplit

from fastapi import FastAPI, HTTPException, Request
import httpx
import pytest
from sqlalchemy import func, select

from tests.test_judgment_analysis_jobs_isolation import OTHER_WB, WB, active
from tin.config import settings
from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, artifact_session, current_workbench
from tin.models import Judgment, SourceMapping, Strategy, WorkbenchArtifact
from tin.web import affairs, analysis_jobs, judgment_jobs
from tin.web.analysis_job_purposes import DRAFT_PURPOSES, PURPOSES, WORKBENCH_SETUP
from tin.web.app import reports_page
from tin.web.judgment_job_registry import GLOBAL_PENDING, GLOBAL_RUNNING, Registry

KEY = 'test-draft-analysis-request'
PAYLOAD = {'text': 'synthetic private input', 'day': '2026-10-18', 'period': 'weekly', 'judgment_id': 12}
SETUP_PAYLOAD = {'model_id': 'fixture-model', 'source': {'knowledge_id': 'report-1'}, 'parts': ['dimensions']}


@pytest.fixture
def draft_case(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    runtime = analysis_jobs.Runtime()
    monkeypatch.setattr(analysis_jobs, 'runtime', runtime)
    actor = Identity('researcher-a', 'example@synthetic.invalid', 'Researcher', 51, 'owner')
    scope = WorkbenchScope(actor, WB, 'test-kb', 51,
        {'workspace_mode': 'isolated', 'can_edit': True, 'can_read_source': True}, 'Bearer synthetic')
    state = {'revision': 0, 'facts': 0}

    async def resolve(identity, workbench_id, authorization):
        return replace(scope, identity=identity, workbench_id=workbench_id, sources_revision=state['revision'],
            authorization=authorization, scope_version=2)

    scope = replace(scope, scope_version=2)
    monkeypatch.setattr(judgment_jobs, 'resolve_workbench', resolve)
    app = FastAPI()
    app.include_router(analysis_jobs.router)
    return scope, state, runtime, app


def run(case, scenario):
    async def isolated():
        try:
            await scenario()
        finally:
            await case[2].shutdown()
    asyncio.run(isolated())


@pytest.mark.parametrize('purpose', sorted(DRAFT_PURPOSES))
def test_draft_admission_phase_restore_and_changed_scope(draft_case, monkeypatch, purpose):
    scope, state, runtime, app = draft_case
    phase = next(name for name in PURPOSES[purpose].phases if name not in {'queued', 'selecting'})
    payload = SETUP_PAYLOAD if purpose == WORKBENCH_SETUP else PAYLOAD

    async def check(_scope, request, result):
        assert request == payload
        if result['_verification'] != state['facts']:
            raise HTTPException(409, '事实已变化')

    monkeypatch.setitem(analysis_jobs.result_checks, purpose, check)

    async def scenario():
        entered, release = asyncio.Event(), asyncio.Event()

        async def generate(verified, body, progress, job_id):
            assert body == payload
            assert current_identity.get() == scope.identity and current_workbench.get() == verified
            assert job_id
            await progress(phase)
            entered.set()
            await release.wait()
            result = {'draft': 'synthetic private model output', '_verification': state['facts']}
            if purpose == WORKBENCH_SETUP:
                result['items'] = []
            elif purpose == 'report_chart_import':
                result['charts'] = []
            return result

        result = await analysis_jobs.start_job(scope, purpose, payload, KEY, generate)
        assert result['state'] == 'queued' and result['phase_label'] == PURPOSES[purpose].phases['queued']
        assert (await analysis_jobs.start_job(scope, purpose, payload, KEY, generate))['id'] == result['id']
        identifier = result['id']
        path = f'/api/sn/analysis-jobs/{identifier}'
        await asyncio.wait_for(entered.wait(), 3)
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app), base_url='http://test.invalid') as client:
            with active(scope):
                status = (await client.get(path)).json()
                assert status['purpose'] == purpose
                assert (status['state'], status['phase'], status['phase_label']) == (
                    'running', phase, PURPOSES[purpose].phases[phase])
                assert (await client.get(path + '/result')).status_code == 409
                latest = await client.get('/api/sn/analysis-jobs/latest', params={'purpose': purpose})
                assert latest.json()['job']['id'] == identifier
                if purpose == 'report_dimension_draft':
                    report_link = Request({'type': 'http', 'query_string': f'analysis_job={identifier}'.encode()})
                    redirected = reports_page(report_link)
                    url = urlsplit(redirected.headers['location'])
                    assert redirected.status_code == 307
                    assert url.path.endswith('/sn/reports')
                    assert parse_qs(url.query) == {
                        'date': [PAYLOAD['day']], 'period': [PAYLOAD['period']], 'analysis_job': [identifier]}
            release.set()
            task = runtime.tasks.get(identifier)
            if task:
                await task
            for foreign in (replace(scope, identity=replace(scope.identity, user_id='researcher-b')),
                            replace(scope, identity=replace(scope.identity, tenant_id=52)),
                            replace(scope, workbench_id=OTHER_WB)):
                with active(foreign):
                    assert (await client.get(path + '/result')).status_code == 404
            with active(scope):
                retrieved = (await client.get(path + '/result')).json()
                assert retrieved['draft'] == 'synthetic private model output'
                assert retrieved['request'] == payload
                assert retrieved['job']['state'] == 'succeeded' and '_verification' not in retrieved

                state['facts'] += 1
                assert (await client.get(path + '/result')).status_code == 409
                state['facts'] = 0
            state['revision'] = 1
            with active(replace(scope, sources_revision=1)):
                assert (await client.get(path + '/result')).status_code == 409
        listing = judgment_jobs.list_jobs(scope.identity)['items']
        assert listing[0]['id'] == identifier
        assert 'synthetic private' not in repr(listing)
        row, = affairs._analysis_jobs(scope, set(scope.view_modules))
        url = urlsplit(row['href'])
        assert url.path == PURPOSES[purpose].path
        assert url.fragment == PURPOSES[purpose].fragment
        assert row['status'] == '待核对' and parse_qs(url.query) == {'analysis_job': [identifier]}
        assert affairs._analysis_jobs(scope, set(scope.view_modules) - {PURPOSES[purpose].module}) == []
        with artifact_session(scope) as session:
            private = session.get(SourceMapping, analysis_jobs.job_key(scope, identifier))
            assert private.config['result']['draft'] == retrieved['draft']
            assert private.config['expires_unix'] > 0
            for model in (Judgment, Strategy, WorkbenchArtifact):
                assert session.scalar(select(func.count()).select_from(model)) == 0

    run(draft_case, scenario)


def test_legacy_and_new_drafts_share_the_same_running_and_pending_caps(draft_case, tmp_path):
    scope, *_ = draft_case
    registry = Registry(tmp_path / 'shared-limits.sqlite')
    purposes = ['judgments', 'diagram', *sorted(DRAFT_PURPOSES)]
    assert len(purposes) >= GLOBAL_PENDING
    jobs = [registry.create(scope, 'request', str(index), 'worker', purpose)[0]
            for index, purpose in enumerate(purposes[:GLOBAL_PENDING])]
    for index, job in enumerate(jobs):
        assert registry.claim(job['id'], job['worker_id'], job['fence']) is (index < GLOBAL_RUNNING)
    with pytest.raises(HTTPException) as rejected:
        registry.create(scope, 'request', 'overflow', 'worker', purposes[-1])
    assert rejected.value.status_code == 429


@pytest.mark.parametrize('purpose', sorted(DRAFT_PURPOSES))
def test_draft_failure_and_interruption(draft_case, purpose):
    scope, _, runtime, _ = draft_case
    payload = SETUP_PAYLOAD if purpose == WORKBENCH_SETUP else {}
    phase = 'reading_source' if purpose == WORKBENCH_SETUP else 'analyzing'

    async def scenario():
        entered = asyncio.Event()

        async def fail(*_args):
            raise RuntimeError('synthetic private failure')

        async def slow(_scope, _body, progress, _id):
            await progress(phase)
            entered.set()
            await asyncio.Event().wait()

        failed = await analysis_jobs.start_job(scope, purpose, payload, KEY, fail)
        task = runtime.tasks.get(failed['id'])
        if task:
            await task
        status = await analysis_jobs.status(scope, failed['id'])
        assert status['state'] == 'failed' and 'synthetic private failure' not in status['message']

        running = await analysis_jobs.start_job(scope, purpose, payload, KEY + '-slow', slow)
        await asyncio.wait_for(entered.wait(), 3)
        progress = affairs._analysis_jobs(scope, set(scope.view_modules))
        assert progress[0]['status'] == '进行中' and PURPOSES[purpose].phases[phase] in progress[0]['detail']
        await runtime.shutdown()
        status = await analysis_jobs.status(scope, running['id'])
        assert status['state'] == 'interrupted' and not status['result_available']
        assert affairs._analysis_jobs(scope, set(scope.view_modules))[0]['status'] == '未完成'

    run(draft_case, scenario)
