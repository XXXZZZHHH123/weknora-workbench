"""Synthetic owner-scoped diagram results can be discarded until they are applied."""
import asyncio
from contextlib import contextmanager
import importlib
import threading

from fastapi import FastAPI, HTTPException
import httpx
import pytest
from sqlalchemy import func, select

from tests.test_diagram_background_jobs import (  # noqa: F401
    OTHER_WB, active, assert_no_saved_diagrams, cache_record, case, completed, run,
)
from tin.integration.workbench_scope import artifact_session
from tin.models import DiagramTemplate, SourceMapping


async def post(case, job_id, action='discard', scope=None, body=None):
    app = FastAPI()
    app.include_router(case.jobs.router)
    with active(scope or case.first):
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url='http://synthetic.invalid') as client:
            return await client.post(f'/api/sn/diagram/jobs/{job_id}/{action}', json=body or {})


@pytest.fixture
def save_path(case, monkeypatch):
    """Run the existing Markdown parser/validator/save endpoint with synthetic storage."""
    app = importlib.import_module('tin.web.app')

    @contextmanager
    def page_session(module, **kwargs):
        assert module == 'diagram'
        with artifact_session(case.first) as session:
            yield session

    monkeypatch.setattr(app, '_page_session', page_session)
    return case


def test_discard_retires_private_result_cleans_cache_and_is_idempotent(case):
    async def scenario():
        meta, result = await completed(case)
        key = result['analysis_snapshot']
        assert cache_record(case, key) is not None
        response = await post(case, meta['id'])
        assert response.status_code == 200, response.text
        job = response.json()['job']
        assert job['state'] == 'superseded' and job['discarded'] is True
        assert job['dismissed'] is True and job['result_available'] is False
        assert job.get('superseded_by') is None
        assert cache_record(case, key) is None
        stored = cache_record(case, case.jobs.job_key(case.first, meta['id']))
        assert stored['state'] == 'superseded' and stored['result'] is None and stored['request'] == {}
        with active(case.first):
            assert (await case.jobs.latest_job(case.first))['job'] is None
            with pytest.raises(HTTPException) as caught:
                await case.jobs.job_result(case.first, meta['id'])
            assert caught.value.status_code == 410
        assert (await post(case, meta['id'])).json()['job']['discarded'] is True
        assert_no_saved_diagrams(case)
        assert len(case.state['calls']) == 2
        await completed(case, key='synthetic-after-discard')
        assert len(case.state['calls']) == 4, 'Discarding the structure forces fresh model analysis for the same input'
    run(case, scenario)


@pytest.mark.parametrize('foreign', ['user', 'tenant', 'workbench'])
def test_discard_cannot_cross_identity_or_workbench(case, foreign):
    async def scenario():
        meta, result = await completed(case)
        other = {'user': case.scope(user='another-owner'), 'tenant': case.scope(tenant=57),
                 'workbench': case.scope(workbench=OTHER_WB)}[foreign]
        response = await post(case, meta['id'], scope=other)
        assert response.status_code in {403, 404}, response.text
        assert cache_record(case, result['analysis_snapshot']) is not None
        assert case.registry.get(case.first.identity, meta['id'], case.first.workbench_id)['state'] == 'succeeded'
    run(case, scenario)


@pytest.mark.parametrize('permission', ['can_edit', 'can_read_source'])
def test_discard_rechecks_permissions_and_rejects_restricted_views(case, permission):
    async def scenario():
        meta, result = await completed(case)
        case.state[permission] = False
        response = await post(case, meta['id'])
        assert response.status_code == 403, response.text
        assert cache_record(case, result['analysis_snapshot']) is not None
    run(case, scenario)


def test_a_judgment_job_cannot_be_discarded_through_diagram_route(case):
    async def scenario():
        with active(case.first):
            meta, _ = case.registry.create(case.first, 'request', 'idempotency', 'worker', 'judgments')
        assert (await post(case, meta['id'])).status_code == 404
    run(case, scenario)


def test_running_results_cannot_be_discarded(case):
    async def scenario():
        with active(case.first):
            meta, _ = case.registry.create(case.first, 'request', 'idempotency', 'worker', 'diagram')
        assert (await post(case, meta['id'])).status_code == 409
    run(case, scenario)


def test_applied_result_is_durable_and_cannot_be_discarded(save_path):
    case = save_path
    async def scenario():
        meta, result = await completed(case)
        response = await post(case, meta['id'], action='apply', body={'markdown': result['markdown']})
        assert response.status_code == 200, response.text
        saved = response.json()['saved']
        assert saved['layout']['nodes']
        stored = cache_record(case, case.jobs.job_key(case.first, meta['id']))
        assert stored['state'] == 'applied' and stored['applied_template_id'] == saved['id']
        with active(case.first):
            assert (await case.jobs.job_status(case.first, meta['id']))['state'] == 'applied'
            assert (await case.jobs.latest_job(case.first))['job'] is None
        response = await post(case, meta['id'])
        assert response.status_code == 409
        assert '产业图结果已应用' in response.json()['detail']
        assert cache_record(case, result['analysis_snapshot']) is not None
        # Repair an advisory index write lost after the authoritative layout commit.
        with case.registry.transaction() as connection:
            index = case.registry.fetch(connection, meta['id'])
            index.update(state='succeeded', phase='succeeded')
            case.registry.write(connection, index)
        assert (await post(case, meta['id'])).status_code == 409
        with active(case.first):
            assert (await case.jobs.job_status(case.first, meta['id']))['state'] == 'applied'
        with active(case.first), artifact_session(case.first) as session:
            assert session.scalar(select(func.count()).select_from(DiagramTemplate)) == 1
    run(case, scenario)


def test_failed_application_rolls_back_layout_and_consumed_marker(save_path, monkeypatch):
    case = save_path
    def rejected(session, template_id):
        raise HTTPException(409, '合成事务失败')
    monkeypatch.setattr(case.jobs, 'mark_application', rejected)

    async def scenario():
        meta, result = await completed(case)
        response = await post(case, meta['id'], action='apply', body={'markdown': result['markdown']})
        assert response.status_code == 409
        assert_no_saved_diagrams(case)
        stored = cache_record(case, case.jobs.job_key(case.first, meta['id']))
        assert stored.get('state') != 'applied'
        assert case.registry.get(case.first.identity, meta['id'], case.first.workbench_id)['state'] == 'succeeded'
        assert (await post(case, meta['id'])).status_code == 200
    run(case, scenario)


def test_discarded_result_cannot_be_applied(save_path):
    case = save_path
    async def scenario():
        meta, result = await completed(case)
        assert (await post(case, meta['id'])).status_code == 200
        response = await post(case, meta['id'], action='apply', body={'markdown': result['markdown']})
        assert response.status_code == 410
        assert_no_saved_diagrams(case)
    run(case, scenario)


def test_apply_and_discard_are_serialized_around_the_layout_transaction(save_path, monkeypatch):
    case = save_path
    entered, release = threading.Event(), threading.Event()
    actual = case.jobs.mark_application

    def held(session, template_id):
        entered.set()
        assert release.wait(5)
        actual(session, template_id)

    monkeypatch.setattr(case.jobs, 'mark_application', held)

    async def scenario():
        meta, result = await completed(case)
        applying = asyncio.create_task(post(case, meta['id'], action='apply', body={'markdown': result['markdown']}))
        assert await asyncio.to_thread(entered.wait, 5)
        discarding = asyncio.create_task(post(case, meta['id']))
        await asyncio.sleep(.05)
        assert not discarding.done(), 'Discard waits until the layout and its consumed marker commit'
        release.set()
        applied, discarded = await asyncio.gather(applying, discarding)
        assert applied.status_code == 200, applied.text
        assert discarded.status_code == 409, discarded.text
        with active(case.first), artifact_session(case.first) as session:
            assert session.scalar(select(func.count()).select_from(DiagramTemplate)) == 1
            row = session.get(SourceMapping, case.jobs.job_key(case.first, meta['id']))
            assert row.config['state'] == 'applied'
    try:
        run(case, scenario)
    finally:
        release.set()
