"""Synthetic diagrams use real durable jobs without creating saved diagrams."""
import asyncio
from contextlib import contextmanager
from copy import deepcopy
import importlib
import json
import threading
from types import SimpleNamespace

from fastapi import HTTPException
import pytest
from sqlalchemy import func, select

from tin.config import settings
from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, artifact_session, current_workbench
from tin.models import DiagramTemplate, SourceMapping
from tin.web import diagram_generation as generation
from tin.web import judgment_jobs
from tin.web.judgment_job_registry import Registry


WB = '10000000-0000-4000-8000-000000000561'
OTHER_WB = '10000000-0000-4000-8000-000000000562'
SERIES = 'kb-' + 'e' * 48
VERSION = 'a' * 64


@contextmanager
def active(scope):
    actor = current_identity.set(scope.identity)
    board = current_workbench.set(scope)
    try:
        yield
    finally:
        current_workbench.reset(board)
        current_identity.reset(actor)


@pytest.fixture
def case(tmp_path, monkeypatch):
    jobs = importlib.import_module('tin.web.diagram_jobs')
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://synthetic.invalid')
    index = Registry(tmp_path / 'job-index.sqlite')
    monkeypatch.setattr(judgment_jobs, 'registry', index)
    if hasattr(jobs, 'registry'):
        monkeypatch.setattr(jobs, 'registry', index)
    monkeypatch.setattr(jobs, 'runtime', jobs.Runtime())
    state = {'can_edit': True, 'can_read_source': True, 'revision': 1,
             'version': VERSION, 'calls': [], 'failure': None, 'block': None, 'entered': None}

    def scope(user='diagram-owner', tenant=56, workbench=WB):
        identity = Identity(user, user + '@fixture.invalid', user, tenant, 'contributor')
        source = {'id': 'source-binding', 'source_type': 'knowledge_base', 'source_id': 'fixture-kb',
                  'knowledge_base_id': 'fixture-kb', 'tenant_id': tenant, 'kind': 'primary',
                  'permission': 'editor', 'delegated': False, 'view_modules': ['diagram', 'judgments']}
        return WorkbenchScope(identity, workbench, 'fixture-kb', tenant,
            {'workspace_mode': 'isolated', 'can_edit': state['can_edit'],
             'can_read_source': state['can_read_source']},
            'Bearer synthetic-' + user, scope_version=2, sources_revision=state['revision'], sources=(source,))

    catalog = [{'id': SERIES, 'name': 'Synthetic supply', 'unit': 'kg', 'frequency': '日',
                'knowledge_base_id': 'fixture-kb', 'knowledge_base_tenant_id': 56,
                'source_file': 'synthetic.xlsx', 'sheet': 'Synthetic', 'dimensions': {}}]

    async def resolve(identity, workbench_id, authorization):
        assert authorization == 'Bearer synthetic-' + identity.user_id
        return scope(identity.user_id, identity.tenant_id, workbench_id)

    def snapshot(verified=None):
        assert current_workbench.get() is not None
        return state['version'], deepcopy(catalog)

    async def complete(verified, model_id, prompt, image=None):
        state['calls'].append({'user': verified.identity.user_id, 'board': verified.workbench_id,
                               'model': model_id, 'image': image})
        assert current_identity.get() == verified.identity and current_workbench.get() == verified
        if state['entered'] is not None:
            state['entered'].set()
        if state['block'] is not None:
            await state['block'].wait()
        if state['failure'] is not None:
            raise state['failure']
        return {'title': 'Synthetic flow', 'groups': [{'id': 'supply', 'label': 'Supply', 'parent_id': None}],
                'nodes': [{'id': 'raw', 'label': 'Input', 'group_id': 'supply', 'series_id': SERIES}],
                'edges': []}

    monkeypatch.setattr(generation, 'catalog_snapshot', snapshot)
    monkeypatch.setattr(generation, 'completion', complete)
    monkeypatch.setattr(generation, 'resolve_workbench', resolve)
    monkeypatch.setattr(judgment_jobs, 'resolve_workbench', resolve)
    if hasattr(jobs, 'resolve_workbench'):
        monkeypatch.setattr(jobs, 'resolve_workbench', resolve)
    return SimpleNamespace(jobs=jobs, registry=index, scope=scope, first=scope(), state=state,
                           catalog=catalog, path=tmp_path)


def body(**values):
    return generation.GenerateRequest(model_id='synthetic-vision', instructions='Synthetic supply diagram', **values)


def run(case, scenario):
    async def execute():
        try:
            return await scenario()
        finally:
            await case.jobs.runtime.shutdown()
    return asyncio.run(execute())


async def completed(case, scope=None, key='synthetic-diagram-request'):
    scope = scope or case.first
    with active(scope):
        meta = await case.jobs.start_job(scope, body(), key)
        work = case.jobs.runtime.tasks.get(meta['id'])
        if work is not None:
            await asyncio.wait_for(work, 5)
        status = await case.jobs.job_status(scope, meta['id'])
        assert status['state'] == 'succeeded', status
        return meta, await case.jobs.job_result(scope, meta['id'])


def test_detached_generation_runs_real_compiler_and_restores_private_preview_without_saving(case, monkeypatch):
    async def scenario():
        meta, result = await completed(case)
        assert meta['purpose'] == 'diagram'
        assert result['version'] == VERSION and result['nodes'] == 1
        assert result['bindings'] == [{'node': 'Input', 'series_id': SERIES, 'name': 'Synthetic supply', 'unit': 'kg'}]
        assert 'Synthetic flow' in result['markdown'] and len(case.state['calls']) == 1
        with active(case.first), artifact_session(case.first) as session:
            assert session.scalar(select(func.count()).select_from(DiagramTemplate)) == 0
        monkeypatch.setattr(case.jobs, 'runtime', case.jobs.Runtime())
        with active(case.first):
            restored = await case.jobs.job_result(case.first, meta['id'])
        assert restored['markdown'] == result['markdown'] and len(case.state['calls']) == 1
    run(case, scenario)


def test_inline_image_reaches_generation_but_is_not_persisted_or_returned(case):
    image = ('data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0l'
             'EQVR42mP8/x8AAwMCAO+aYv0AAAAASUVORK5CYII=')

    async def scenario():
        with active(case.first):
            meta = await case.jobs.start_job(case.first, body(image=image), 'synthetic-inline-image-request')
            work = case.jobs.runtime.tasks.get(meta['id'])
            if work is not None:
                await asyncio.wait_for(work, 5)
            result = await case.jobs.job_result(case.first, meta['id'])
            assert result['request']['has_image'] is True and 'image' not in result['request']
            with artifact_session(case.first) as session:
                row = session.get(SourceMapping, case.jobs.job_key(case.first, meta['id']))
                assert image not in json.dumps(row.config) and 'image' not in row.config['request']
            assert image not in json.dumps(result)
        assert len(case.state['calls']) == 1 and case.state['calls'][0]['image'] == image
    run(case, scenario)


def test_duplicate_request_reuses_one_model_call_and_rejects_different_body(case):
    async def scenario():
        with active(case.first):
            first = await case.jobs.start_job(case.first, body(), 'synthetic-diagram-request')
            second = await case.jobs.start_job(case.first, body(), 'synthetic-diagram-request')
            assert first['id'] == second['id']
            work = case.jobs.runtime.tasks.get(first['id'])
            if work is not None:
                await asyncio.wait_for(work, 5)
            assert (await case.jobs.job_status(case.first, first['id']))['state'] == 'succeeded'
            third = await case.jobs.start_job(case.first, body(), 'synthetic-diagram-request')
            assert third['id'] == first['id'] and len(case.state['calls']) == 1
            altered = generation.GenerateRequest(model_id='another-model', instructions='Synthetic supply diagram')
            with pytest.raises(HTTPException) as error:
                await case.jobs.start_job(case.first, altered, 'synthetic-diagram-request')
            assert error.value.status_code == 409
        assert current_identity.get() is None and current_workbench.get() is None
    run(case, scenario)


@pytest.mark.parametrize('foreign', ['user', 'tenant', 'workbench'])
def test_private_result_and_status_cannot_cross_scope(case, foreign):
    async def scenario():
        meta, _ = await completed(case)
        other = {'user': case.scope(user='another-owner'), 'tenant': case.scope(tenant=57),
                 'workbench': case.scope(workbench=OTHER_WB)}[foreign]
        with active(other):
            for operation in (case.jobs.job_status, case.jobs.job_result):
                with pytest.raises(HTTPException) as error:
                    await operation(other, meta['id'])
                assert error.value.status_code in {403, 404, 409}
    run(case, scenario)


@pytest.mark.parametrize('change', ['can_edit', 'can_read_source', 'revision', 'version'])
def test_completed_results_are_rechecked_against_current_grants_and_source_version(case, change):
    async def scenario():
        meta, _ = await completed(case)
        case.state[change] = {'revision': 2, 'version': 'b' * 64}.get(change, False)
        with active(case.first), pytest.raises(HTTPException) as error:
            await case.jobs.job_result(case.first, meta['id'])
        assert error.value.status_code in {403, 409}
        assert len(case.state['calls']) == 1
    run(case, scenario)


def test_global_listing_contains_only_safe_current_actor_metadata_and_shared_queue_is_atomic(case):
    async def scenario():
        meta, _ = await completed(case)
        listed = judgment_jobs.list_jobs(case.first.identity)['items']
        assert any(row['id'] == meta['id'] and row['purpose'] == 'diagram' for row in listed)
        payload = json.dumps(listed)
        assert 'Synthetic supply diagram' not in payload and SERIES not in payload and 'Bearer' not in payload
        assert judgment_jobs.list_jobs(case.scope(user='another-owner').identity)['items'] == []
        # Both purposes contend for the same personal/global admission limits.
        row, _ = case.registry.create(case.scope(user='busy-owner'), 'hash-one', 'one-request', 'worker-one')
        with active(case.scope(user='busy-owner')), pytest.raises(HTTPException) as error:
            await case.jobs.start_job(case.scope(user='busy-owner'), body(), 'synthetic-second-request')
        assert error.value.status_code == 429
        with case.registry.transaction() as connection:
            row['state'] = 'failed';case.registry.write(connection, row)
    run(case, scenario)


def test_both_purposes_share_the_same_two_running_task_limit(case):
    rows = []
    for number, purpose in enumerate(('diagram', 'judgments', 'diagram')):
        row, created = case.registry.create(case.scope(user=f'queue-owner-{number}'),
            f'synthetic-request-{number}', f'synthetic-key-{number}', f'synthetic-worker-{number}', purpose)
        assert created
        rows.append(row)
    first, second, third = rows
    assert case.registry.claim(first['id'], first['worker_id'], first['fence']) is True
    assert case.registry.claim(second['id'], second['worker_id'], second['fence']) is True
    assert case.registry.claim(third['id'], third['worker_id'], third['fence']) is False
    case.registry.finish(first['id'], first['worker_id'], first['fence'], 'failed')
    assert case.registry.claim(third['id'], third['worker_id'], third['fence']) is True


def test_shutdown_interrupts_without_rescheduling_or_saving(case):
    async def scenario():
        case.state.update(block=asyncio.Event(), entered=asyncio.Event())
        with active(case.first):
            meta = await case.jobs.start_job(case.first, body(), 'synthetic-diagram-request')
            await asyncio.wait_for(case.state['entered'].wait(), 5)
            await case.jobs.runtime.shutdown()
            status = await case.jobs.job_status(case.first, meta['id'])
            assert status['state'] == 'interrupted'
            with pytest.raises(HTTPException):
                await case.jobs.job_result(case.first, meta['id'])
        assert len(case.state['calls']) == 1
    run(case, scenario)


def test_cancelling_http_starter_does_not_cancel_an_accepted_generation(case, monkeypatch):
    original = case.jobs._create_private
    entered, released = threading.Event(), threading.Event()

    def delayed(*args):
        entered.set()
        assert released.wait(timeout=5)
        return original(*args)

    monkeypatch.setattr(case.jobs, '_create_private', delayed)

    async def scenario():
        try:
            with active(case.first):
                starter = asyncio.create_task(case.jobs.start_job(case.first, body(), 'synthetic-cancel-request'))
                assert await asyncio.to_thread(entered.wait, 5)
                starter.cancel()
                with pytest.raises(asyncio.CancelledError):
                    await starter
                released.set()
                await asyncio.wait_for(asyncio.gather(*list(case.jobs.runtime.admissions)), 5)
                pending = list(case.jobs.runtime.tasks.values())
                if pending:
                    await asyncio.wait_for(asyncio.gather(*pending), 5)
                listed = judgment_jobs.list_jobs(case.first.identity)['items']
                assert len(listed) == 1 and listed[0]['state'] == 'succeeded'
                restored = await case.jobs.job_result(case.first, listed[0]['id'])
                assert 'Synthetic flow' in restored['markdown']
            assert len(case.state['calls']) == 1
        finally:
            released.set()
    run(case, scenario)


@pytest.mark.parametrize('change', ['identity', 'hash', 'unavailable'])
def test_original_replacement_or_removal_revokes_an_existing_preview(case, monkeypatch, change):
    document = {'id': 'synthetic-source', 'knowledge_base_id': 'fixture-kb',
                'file_hash': 'original-revision-one', 'updated_at': 'synthetic-revision-one'}
    content = {'supported': True, 'sha256': 'c' * 64, 'text': 'Synthetic source evidence'}

    async def original_identity(verified, source_id):
        assert source_id == 'synthetic-source' and verified.workbench_id == WB
        if change == 'unavailable' and case.state.get('removed'):
            raise HTTPException(404, 'Original unavailable')
        return deepcopy(document)

    async def source_content(source_id, scope):
        assert source_id == 'synthetic-source' and scope.knowledge_base_id == 'fixture-kb'
        return deepcopy(content)

    monkeypatch.setattr(generation, 'original_identity', original_identity)
    monkeypatch.setattr(generation, 'source_content', source_content)

    async def scenario():
        with active(case.first):
            meta = await case.jobs.start_job(case.first, body(source_id='synthetic-source'), 'synthetic-original-request')
            work = case.jobs.runtime.tasks.get(meta['id'])
            if work is not None:
                await asyncio.wait_for(work, 5)
            restored = await case.jobs.job_result(case.first, meta['id'])
            assert 'Synthetic flow' in restored['markdown']
            if change == 'identity':
                document['file_hash'] = 'original-revision-two'
            elif change == 'hash':
                content['sha256'] = 'd' * 64
            else:
                case.state['removed'] = True
            with pytest.raises(HTTPException) as error:
                await case.jobs.job_result(case.first, meta['id'])
            assert error.value.status_code in {403, 404, 409}
        assert len(case.state['calls']) == 1
    run(case, scenario)


def test_private_record_owner_tampering_cannot_restore_result(case):
    async def scenario():
        meta, _ = await completed(case)
        with active(case.first), artifact_session(case.first) as session:
            row = session.get(SourceMapping, case.jobs.job_key(case.first, meta['id']))
            row.confirmed_by = 'another-owner'
            session.commit()
        with active(case.first), pytest.raises(HTTPException) as error:
            await case.jobs.job_result(case.first, meta['id'])
        assert error.value.status_code == 404 and len(case.state['calls']) == 1
    run(case, scenario)


def test_streamed_request_capacity_is_checked_before_parsing_or_model_work(case, monkeypatch):
    monkeypatch.setattr(case.jobs, 'MAX_REQUEST_BYTES', 64)

    class Request:
        async def stream(self):
            yield b' ' * 64
            yield b' '

    async def scenario():
        with pytest.raises(HTTPException) as error:
            await case.jobs.read_body(Request())
        assert error.value.status_code == 413 and case.state['calls'] == []
    run(case, scenario)


def test_failed_generation_returns_safe_failure_and_never_automatically_retries(case):
    async def scenario():
        case.state['failure'] = RuntimeError('private-provider-url bearer-secret synthetic-source')
        with active(case.first):
            meta = await case.jobs.start_job(case.first, body(), 'synthetic-diagram-request')
            await case.jobs.runtime.tasks[meta['id']]
            status = await case.jobs.job_status(case.first, meta['id'])
            assert status['state'] == 'failed'
            assert 'provider-url' not in json.dumps(status) and 'bearer-secret' not in json.dumps(status)
            with pytest.raises(HTTPException):await case.jobs.job_result(case.first, meta['id'])
        assert len(case.state['calls']) == 1
    run(case, scenario)


def native_failure_messages():
    for kind in ('request_timeout', 'transport_error', 'outbound_blocked', 'request_cancelled',
                 'client_error', 'empty_response', 'invalid_response'):
        phase = 'response_validation' if kind in {'empty_response', 'invalid_response'} else 'completion'
        yield generation.native_failure_message({'code': 1008,
            'details': {'phase': phase, 'error_class': kind}}, 503)
    for status in range(400, 600):
        yield generation.native_failure_message({'code': 1008, 'details': {'phase': 'completion',
            'error_class': 'provider_http_error', 'provider_http_status': status}}, 503)


@pytest.mark.parametrize('message', [*generation.SELECTION_FAILURE_MESSAGES, *native_failure_messages()])
def test_background_failure_preserves_only_enumerated_application_messages(message):
    jobs = importlib.import_module('tin.web.diagram_jobs')
    assert isinstance(message, str)
    assert jobs.failure_message(HTTPException(422, message)) == message


@pytest.mark.parametrize('detail', [
    'private-provider-url bearer-secret synthetic-source',
    '连接模型服务超时，请检查网络或代理设置 bearer-secret',
    '模型服务返回 HTTP 600，请检查模型服务配置、访问权限或可用额度',
    {'message': 'bearer-secret', 'error_class': 'transport_error'},
])
def test_background_failure_does_not_pass_through_arbitrary_provider_details(detail):
    jobs = importlib.import_module('tin.web.diagram_jobs')
    message = jobs.failure_message(HTTPException(503, detail))
    assert message == '产业图生成服务暂不可用，请稍后重试'
    assert 'bearer-secret' not in message and 'HTTP 600' not in message


def test_total_generation_deadline_fails_without_retrying_or_saving_a_diagram(case, monkeypatch):
    monkeypatch.setattr(case.jobs, 'GENERATION_SECONDS', 1)

    async def scenario():
        case.state['block'] = asyncio.Event()
        case.state['entered'] = asyncio.Event()
        with active(case.first):
            meta = await case.jobs.start_job(case.first, body(), 'synthetic-timeout-request')
            work = case.jobs.runtime.tasks[meta['id']]
            await asyncio.wait_for(case.state['entered'].wait(), 2)
            await asyncio.wait_for(work, 3)
            status = await case.jobs.job_status(case.first, meta['id'])
            assert status['state'] == 'failed' and status['message'] == '产业图生成超时，请重新发起'
            with pytest.raises(HTTPException) as unavailable:
                await case.jobs.job_result(case.first, meta['id'])
            assert unavailable.value.status_code == 409
            with artifact_session(case.first) as session:
                assert session.scalar(select(func.count()).select_from(DiagramTemplate)) == 0
                stored = session.get(SourceMapping, case.jobs.job_key(case.first, meta['id']))
                assert stored.config['result'] is None
            repeated = await case.jobs.start_job(case.first, body(), 'synthetic-timeout-request')
            assert repeated['id'] == meta['id'] and repeated['state'] == 'failed'
        assert len(case.state['calls']) == 1 and not case.state['block'].is_set()
    run(case, scenario)


def test_task_ids_cannot_be_read_through_the_other_purpose(case):
    async def scenario():
        meta, _ = await completed(case)
        with active(case.first), pytest.raises(HTTPException) as diagram_as_judgment:
            await judgment_jobs.job_status(case.first, meta['id'])
        assert diagram_as_judgment.value.status_code == 404
        ordinary, _ = case.registry.create(case.first, 'ordinary-request', 'ordinary-key', 'ordinary-worker')
        with active(case.first), pytest.raises(HTTPException) as judgment_as_diagram:
            await case.jobs.job_status(case.first, ordinary['id'])
        assert judgment_as_diagram.value.status_code == 404
    run(case, scenario)


def test_expired_index_is_not_revived_by_a_persisted_completed_result(case):
    async def scenario():
        meta, _ = await completed(case)
        with case.registry.transaction() as connection:
            raw = case.registry.fetch(connection, meta['id'])
            raw['expires_unix'] = 0
            case.registry.write(connection, raw)
        with active(case.first):
            assert (await case.jobs.job_status(case.first, meta['id']))['state'] == 'expired'
            with pytest.raises(HTTPException) as error:
                await case.jobs.job_result(case.first, meta['id'])
            assert error.value.status_code == 410
        assert len(case.state['calls']) == 1
    run(case, scenario)


def test_empty_generation_input_does_not_admit_model_work(case):
    async def scenario():
        empty = generation.GenerateRequest(model_id='synthetic-vision')
        with active(case.first), pytest.raises(HTTPException) as error:
            await case.jobs.start_job(case.first, empty, 'synthetic-empty-request')
        assert error.value.status_code in {400, 422} and case.state['calls'] == []
    run(case, scenario)


@pytest.mark.parametrize('key', [None, '', 'short', '\nsynthetic-request-key'])
def test_invalid_request_identifiers_do_not_admit_model_work(case, key):
    async def scenario():
        with active(case.first), pytest.raises(HTTPException) as error:
            await case.jobs.start_job(case.first, body(), key)
        assert error.value.status_code == 422 and case.state['calls'] == []
    run(case, scenario)
