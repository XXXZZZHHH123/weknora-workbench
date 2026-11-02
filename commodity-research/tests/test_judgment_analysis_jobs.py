"""Offline background-task scheduling and durable preview transactions."""
import asyncio
from copy import deepcopy
import multiprocessing
import threading
from pathlib import Path
from types import SimpleNamespace

import pytest
from fastapi import HTTPException
from sqlalchemy import event, select
from sqlalchemy.orm import Session

from tests.test_knowledge_facts import canonical, FIRST, page
from tests.test_judgment_generation import generator, TEXT, SERIES, rows
from tin.config import settings
from tin.integration.identity import Identity
from tin.integration.workbench_scope import resolve_workbench, isolated_session_factory
from tin.models import AuditLog, Judgment, SourceMapping
from tin.web import judgment_generation as generation, judgment_jobs as jobs, judgment_review as review
from tin.web.judgment_job_registry import Registry


def body():
    return generation.GenerateRequest(model_id='synthetic-chat', text=TEXT, base_version=0)


async def scope_for_fixture():
    return await resolve_workbench(Identity(user_id='researcher', email='researcher@test',
        display_name='Researcher', tenant_id=17, role='owner'), FIRST, 'Bearer source-user')


@pytest.fixture
def background(generator, monkeypatch):
    runtime = jobs.Runtime()
    monkeypatch.setattr(jobs, 'runtime', runtime)
    monkeypatch.setattr(jobs, 'registry', Registry(settings.data_dir / 'job-index.sqlite'))
    return generator


async def completed_job():
    scope = await scope_for_fixture()
    meta = await jobs.start_job(scope, body(), 'synthetic-idempotency-key')
    await jobs.runtime.tasks[meta['id']]
    status = await jobs.job_status(scope, meta['id'])
    assert status['state'] == 'succeeded', status
    result = await jobs.job_result(scope, meta['id'])
    return scope, meta, result


def test_detached_result_survives_memory_restart_and_keeps_the_same_canonical_token(background, monkeypatch):
    _, _, _, _, calls = background
    async def run():
        scope, meta, result = await completed_job()
        assert result['draft_id'] != meta['id'] and meta['id'] in result['draft_id']
        assert rows() == [] and len(calls) == 3
        generation.previews.items.clear()
        monkeypatch.setattr(jobs, 'runtime', jobs.Runtime())
        monkeypatch.setattr(jobs, 'registry', Registry(settings.data_dir / 'job-index.sqlite'))
        restored = await jobs.job_result(scope, meta['id'])
        assert restored['draft_id'] == result['draft_id']
        assert restored['review'] == result['review'] and len(calls) == 3
        return scope, meta, result
    asyncio.run(run())


def test_duplicate_start_runs_one_analysis_and_scope_context_is_reset(background):
    from tin.integration.identity import current_identity
    from tin.integration.workbench_scope import current_workbench
    _, _, _, _, calls = background
    async def run():
        scope = await scope_for_fixture()
        first = await jobs.start_job(scope, body(), 'synthetic-idempotency-key')
        second = await jobs.start_job(scope, body(), 'synthetic-idempotency-key')
        assert first['id'] == second['id']
        await jobs.runtime.tasks[first['id']]
        assert current_identity.get() is None and current_workbench.get() is None
        assert len(calls) == 3 and rows() == []
    asyncio.run(run())


def test_job_review_uses_durable_cas_and_retains_an_explicit_empty_selection(background):
    async def run():
        scope, _, result = await completed_job()
        token = result['draft_id']
        before = generation.previews.get(scope, token)
        stale = deepcopy(before)
        changed = deepcopy(before)
        item = next(row for row in changed['review']['items'] if row['kind'] == 'binding')
        item.update(series_id=None, status='kept_text', origin='user')
        changed['review']['revision'] += 1
        changed['document'] = review.build_document(changed, changed['review']['catalog'])
        review.record_decisions(scope, token, before, changed, changed['review']['catalog'], [(item, False)])
        restored = generation.previews.get(scope, token)
        assert next(row for row in restored['review']['items'] if row['kind'] == 'binding')['series_id'] is None
        assert restored['document']['judgment']['contradiction']['bull']['evidence'][0]['series_id'] is None
        assert restored['review']['revision'] == 2
        with pytest.raises(HTTPException) as caught:
            review.record_decisions(scope, token, stale, changed, changed['review']['catalog'], [(item, False)])
        assert caught.value.status_code == 409
        with isolated_session_factory(17, FIRST)() as session:
            assert len(session.scalars(select(AuditLog).where(AuditLog.action == '研判逐项确认')).all()) == 1
    asyncio.run(run())


def _replace_worker(data_dir, scope, token, barrier, queue):
    settings.data_dir = Path(data_dir)
    settings.weknora_api_url = 'http://native.test'
    jobs.registry = Registry(Path(data_dir) / 'job-index.sqlite')
    preview = jobs.load_preview(scope, token)
    preview['review']['revision'] += 1
    barrier.wait(timeout=20)
    try:
        jobs.atomic_replace(scope, token, preview, 1)
        queue.put(200)
    except HTTPException as error:
        queue.put(error.status_code)


def test_two_processes_cannot_overwrite_the_same_durable_revision(background):
    scope, _, result = asyncio.run(completed_job())
    context = multiprocessing.get_context('spawn')
    barrier, queue = context.Barrier(2), context.Queue()
    processes = [context.Process(target=_replace_worker, args=(str(settings.data_dir), scope,
        result['draft_id'], barrier, queue)) for _ in range(2)]
    for process in processes:
        process.start()
    try:
        outcomes = sorted(queue.get(timeout=40) for _ in processes)
        assert outcomes == [200, 409]
    finally:
        for process in processes:
            process.join(timeout=5)
            if process.is_alive():
                process.terminate()
                process.join(timeout=5)
    assert jobs.load_preview(scope, result['draft_id'])['review']['revision'] == 2


def test_apply_consumes_job_in_the_judgment_transaction_and_cannot_be_restored_twice(background):
    client, headers, _, _, _ = background
    scope, meta, result = asyncio.run(completed_job())
    def fail_commit(session):
        if any(isinstance(row, Judgment) for row in session.identity_map.values()):
            raise RuntimeError('synthetic final commit failure')
    event.listen(Session, 'before_commit', fail_commit)
    try:
        with pytest.raises(RuntimeError, match='synthetic final commit failure'):
            client.post(page(FIRST, '/api/sn/judgment/apply'), headers=headers,
                json={'draft_id': result['draft_id'], 'base_version': 0, 'review_revision': 1})
    finally:
        event.remove(Session, 'before_commit', fail_commit)
    assert rows() == [] and jobs.load_preview(scope, result['draft_id'])['review']['revision'] == 1
    saved = client.post(page(FIRST, '/api/sn/judgment/apply'), headers=headers,
        json={'draft_id': result['draft_id'], 'base_version': 0, 'review_revision': 1})
    assert saved.status_code == 200, saved.text
    assert len(rows()) == 1 and rows()[0].status == '草稿'
    with pytest.raises(HTTPException):
        jobs.load_preview(scope, result['draft_id'])
    assert asyncio.run(jobs.job_status(scope, meta['id']))['state'] == 'applied'


def test_registry_limits_are_atomic_between_instances_and_lease_loss_fences_the_worker(tmp_path):
    first, second = Registry(tmp_path / 'index.sqlite'), Registry(tmp_path / 'index.sqlite')
    def scope(user):
        return SimpleNamespace(identity=SimpleNamespace(user_id=user, tenant_id=17), workbench_id=FIRST, metadata={})
    a, _ = first.create(scope('a'), 'hash-a', 'key-a', 'worker-a')
    b, _ = second.create(scope('b'), 'hash-b', 'key-b', 'worker-b')
    c, _ = first.create(scope('c'), 'hash-c', 'key-c', 'worker-c')
    assert first.claim(a['id'], 'worker-a', a['fence'])
    assert second.claim(b['id'], 'worker-b', b['fence'])
    assert not first.claim(c['id'], 'worker-c', c['fence'])
    d, created = second.create(scope('a'), 'another-hash', 'another-key', 'another-worker')
    assert created and d['id'] != a['id']
    assert not second.claim(d['id'], 'another-worker', d['fence'])
    with first.transaction() as connection:
        a['lease_until'] = 0
        first.write(connection, a)
    assert second.get(scope('a').identity, a['id'])['state'] == 'interrupted'
    with pytest.raises(HTTPException):
        first.touch(a['id'], 'worker-a', a['fence'])
    assert second.claim(c['id'], 'worker-c', c['fence'])
    assert not second.claim(d['id'], 'another-worker', d['fence'])


def test_shutdown_cancels_active_analysis_and_records_interrupted_without_retry(background, monkeypatch):
    async def run():
        entered = asyncio.Event()
        async def blocked(scope, body, progress):
            await progress('analyzing')
            entered.set()
            await asyncio.Event().wait()
        monkeypatch.setattr(generation, '_analyze', blocked)
        scope = await scope_for_fixture()
        meta = await jobs.start_job(scope, body(), 'synthetic-idempotency-key')
        await asyncio.wait_for(entered.wait(), 5)
        await jobs.runtime.shutdown()
        assert not jobs.runtime.tasks
        assert (await jobs.job_status(scope, meta['id']))['state'] == 'interrupted'
    asyncio.run(run())


def test_cancelling_the_http_starter_does_not_cancel_an_accepted_task(background, monkeypatch):
    _, _, _, _, calls = background
    original = jobs._create_private
    entered, released = threading.Event(), threading.Event()
    def delayed(*arguments):
        entered.set()
        assert released.wait(timeout=5)
        return original(*arguments)
    monkeypatch.setattr(jobs, '_create_private', delayed)
    async def run():
        scope = await scope_for_fixture()
        starter = asyncio.create_task(jobs.start_job(scope, body(), 'synthetic-idempotency-key'))
        assert await asyncio.to_thread(entered.wait, 5)
        starter.cancel()
        with pytest.raises(asyncio.CancelledError):
            await starter
        released.set()
        await asyncio.gather(*list(jobs.runtime.admissions))
        await asyncio.gather(*list(jobs.runtime.tasks.values()))
        result = jobs.list_jobs(scope.identity)['items'][0]
        assert result['state'] == 'succeeded' and len(calls) == 3 and rows() == []
    asyncio.run(run())


def test_expired_index_cannot_be_resurrected_by_result_reconciliation(tmp_path):
    index = Registry(tmp_path / 'index.sqlite')
    scope = SimpleNamespace(identity=SimpleNamespace(user_id='a', tenant_id=17), workbench_id=FIRST, metadata={})
    meta, _ = index.create(scope, 'hash', 'key', 'worker')
    with index.transaction() as connection:
        meta['expires_unix'] = 0
        index.write(connection, meta)
    assert index.get(scope.identity, meta['id'])['state'] == 'expired'
    assert index.reconcile(scope.identity, meta['id'], FIRST, 'succeeded')['state'] == 'expired'


def test_exact_global_get_accepts_bearer_and_does_not_open_other_unscoped_routes(background, monkeypatch):
    client, headers, _, _, _ = background
    monkeypatch.setattr(jobs.runtime, 'launch', lambda *args: None)
    started = client.post(page(FIRST, '/api/sn/judgment/jobs'), headers={**headers,
        'Idempotency-Key': 'synthetic-idempotency-key'}, json=body().model_dump(mode='json'))
    assert started.status_code == 202, started.text
    listed = client.get('/research/api/research/analysis-jobs', headers=headers)
    assert listed.status_code == 200, listed.text
    assert listed.json()['items'][0]['id'] == started.json()['id']
    assert 'draft_id' not in listed.text and 'source-user' not in listed.text and TEXT not in listed.text
    assert client.get('/research/api/sn/judgment/manage', headers=headers).status_code == 401
    assert client.get('/research/api/research/analysis-jobs/other', headers=headers).status_code == 401
    assert client.post('/research/api/research/analysis-jobs', headers=headers, json={}).status_code == 401
