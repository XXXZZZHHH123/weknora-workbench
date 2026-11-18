"""Fenced workbench setup tasks and the handoff to native review editors."""
import asyncio
from copy import deepcopy
from datetime import datetime, timezone
import hmac
import logging
import re
import secrets
import sqlite3
import time

from fastapi import APIRouter, HTTPException, Request
from starlette.concurrency import run_in_threadpool

from tin.integration.identity import current_identity
from tin.integration.workbench_scope import artifact_session, current_workbench
from tin.models import AuditLog, SourceMapping
from tin.ownership import verified_actor
from tin.web import judgment_jobs as shared, monitors, report_chart_import, report_chart_suggestions, workbench_setup
from tin.web.judgment_job_registry import iso, metadata
from tin.web.workbench_setup_entry import setup_enabled

PURPOSE = workbench_setup.PURPOSE
KIND = 'workbench-setup-job.v1'
registry = shared.registry
router = APIRouter(prefix='/api/sn/workbench-setup/jobs')


def require_scope():
    return check_scope(report_chart_import.writable_scope())


def check_scope(scope):
    if not setup_enabled(scope):
        raise HTTPException(403, '当前权限不能从周报建立工作台')
    return scope


def grant_digest(scope):
    return shared.digest([shared.grant_digest(scope), report_chart_import.grant(scope)])


async def fresh_scope(scope):
    check_scope(scope)
    verified = check_scope(await shared.fresh_scope(scope))
    if grant_digest(verified) != grant_digest(scope):
        raise HTTPException(409, '研究台来源或权限已改变，请重新建立')
    return verified


def job_key(scope, job_id):
    return shared.digest([KIND, scope.identity.tenant_id, scope.identity.user_id, scope.workbench_id, job_id])


def owned_record(session, scope, job_id):
    row = session.get(SourceMapping, job_key(scope, job_id))
    value = row.config if row is not None and isinstance(row.config, dict) else {}
    expected = {'kind': KIND, 'id': job_id, 'tenant_id': scope.identity.tenant_id,
                'user_id': scope.identity.user_id, 'workbench_id': scope.workbench_id}
    if (row is None or row.confirmed_by != scope.identity.user_id
            or any(value.get(key) != item for key, item in expected.items())):
        raise HTTPException(404, '建立任务不存在或无权访问')
    if value.get('grant_digest') != grant_digest(scope):
        raise HTTPException(409, '研究台来源或权限已改变，请重新建立')
    if value.get('expires_unix', 0) <= time.time():
        raise HTTPException(410, '建立任务结果已过期，请重新建立')
    if value.get('state') == 'superseded':
        raise HTTPException(410, '已有更新的建立任务结果，请打开最新结果')
    if value.get('state') == 'succeeded':
        registry.guard_result(scope.identity, job_id, scope.workbench_id, PURPOSE)
    return row


def audit(session, scope, job_id, action, detail):
    session.add(AuditLog(at=datetime.now(timezone.utc), actor=verified_actor(session, ''),
        action=action, target_type=PURPOSE, target_id=job_id, detail={'workbench_id': scope.workbench_id, **detail}))


def create_private(scope, body, meta):
    source = body.source.model_dump(mode='json', exclude={'snapshot'})
    value = {'kind': KIND, 'id': meta['id'], 'tenant_id': scope.identity.tenant_id,
        'user_id': scope.identity.user_id, 'workbench_id': scope.workbench_id, 'state': 'queued',
        'grant_digest': grant_digest(scope), 'fence': meta['fence'], 'worker_id': meta['worker_id'],
        'expires_unix': meta['expires_unix'], 'request': {'source': source, 'parts': body.parts, 'model_id': body.model_id}}
    with artifact_session(scope) as session:
        session.add(SourceMapping(id=job_key(scope, meta['id']), config=value, version=1,
            confirmed_by=scope.identity.user_id, confirmed_at=datetime.now(timezone.utc)))
        audit(session, scope, meta['id'], '提交周报建立工作台',
            {'knowledge_id': body.source.knowledge_id, 'parts': body.parts, 'model_id': body.model_id})
        session.commit()


def complete(scope, meta, result, original):
    with registry.fenced(meta['id'], meta['worker_id'], meta['fence']) as (connection, current):
        if current.get('purpose') != PURPOSE:
            raise HTTPException(409, '任务类型已改变，请重新建立')
        current.update(state='succeeded', phase='succeeded', updated_at=iso(), finished_at=iso())
        registry.write(connection, current)
        retired = [item for item in registry.compact(connection)
                   if registry.scope_key(item) == registry.scope_key(current)]
        with artifact_session(scope) as session:
            row = owned_record(session, scope, meta['id'])
            if row.version != 1 or row.config.get('fence') != meta['fence']:
                raise HTTPException(409, '建立任务已中断，请重新提交')
            if all(item['id'] != meta['id'] for item in retired):
                token = 'setup.' + meta['id'] + '.' + secrets.token_urlsafe(24)
                value = {**row.config, 'state': 'succeeded', 'result': result, 'source_identity': original,
                         'judgment_token': token}
                shared._update_record(session, row, value)
                audit(session, scope, meta['id'], '生成周报建立工作台候选',
                    {'items': [{key: item[key] for key in ('part', 'state', 'counts') if key in item}
                               for item in result['items']]})
            for old in retired:
                shared._discard_private_record(session, old, KIND)
            session.commit()
        for old in retired:
            old['payload_pruned'] = True
            registry.write(connection, old)


def cleanup_expired():
    shared.cleanup_discarded(KIND, registry, PURPOSE)


class Runtime(shared.Runtime):
    async def sweep(self):
        while True:
            try:
                await run_in_threadpool(cleanup_expired)
            except (sqlite3.Error, OSError, HTTPException):
                logging.getLogger(__name__).warning('Workbench setup cleanup deferred')
            await asyncio.sleep(60)

    async def run(self, scope, body, meta):
        identity_marker = current_identity.set(scope.identity)
        scope_marker = current_workbench.set(scope)
        heartbeat = asyncio.create_task(self.heartbeat(meta, asyncio.current_task()))
        try:
            deadline = time.monotonic() + shared.QUEUE_SECONDS
            while not await run_in_threadpool(registry.claim, meta['id'], self.worker_id, meta['fence']):
                if time.monotonic() >= deadline:
                    raise TimeoutError()
                await asyncio.sleep(.25)

            async def progress(phase):
                await fresh_scope(scope)
                await run_in_threadpool(registry.touch, meta['id'], self.worker_id, meta['fence'], phase)

            result, original = await workbench_setup.generate(scope, body, meta['expires_unix'], progress)
            await fresh_scope(scope)
            await run_in_threadpool(complete, scope, meta, result, original)
        except asyncio.CancelledError:
            await run_in_threadpool(registry.finish, meta['id'], self.worker_id, meta['fence'],
                'interrupted', '建立任务已中断，请重新提交')
        except Exception as error:
            await run_in_threadpool(registry.finish, meta['id'], self.worker_id, meta['fence'],
                'failed', workbench_setup.result_message(error))
        finally:
            heartbeat.cancel()
            await asyncio.gather(heartbeat, return_exceptions=True)
            current_workbench.reset(scope_marker)
            current_identity.reset(identity_marker)


runtime = Runtime()


async def start_job(scope, body, idempotency_key):
    if not isinstance(idempotency_key, str) or not re.fullmatch(r'[\x21-\x7e]{16,128}', idempotency_key):
        raise HTTPException(422, '请提供有效的建立请求标识')
    await fresh_scope(scope)
    source = body.source.model_copy(update={'snapshot': None})
    body = body.model_copy(update={'source': source})
    if runtime.closing:
        raise HTTPException(503, '服务正在重启，请稍后建立')

    async def admit():
        meta, created = None, False
        try:
            await run_in_threadpool(cleanup_expired)
            meta, created = await run_in_threadpool(registry.create, scope,
                shared.digest(body.model_dump(mode='json')), shared.digest(idempotency_key), runtime.worker_id, PURPOSE)
            if created:
                await run_in_threadpool(create_private, scope, body, meta)
                if runtime.closing:
                    raise asyncio.CancelledError()
                runtime.launch(scope, body, meta)
            return metadata(meta)
        except BaseException as error:
            if created:
                state = 'interrupted' if isinstance(error, asyncio.CancelledError) else 'failed'
                await run_in_threadpool(registry.finish, meta['id'], runtime.worker_id, meta['fence'],
                    state, '建立任务未能提交，请重试')
            raise

    admission = asyncio.create_task(admit(), name='workbench-setup-admission')
    runtime.admissions.add(admission)

    def admitted(done):
        runtime.admissions.discard(done)
        if not done.cancelled():
            done.exception()

    admission.add_done_callback(admitted)
    return await asyncio.shield(admission)


def fetch_status(scope, job_id):
    meta = registry.get(scope.identity, job_id, scope.workbench_id)
    if meta.get('purpose') != PURPOSE:
        raise HTTPException(404, '建立任务不存在或无权访问')
    stored_state = None
    if meta['state'] not in {'expired', 'superseded'}:
        with artifact_session(scope) as session:
            stored = session.get(SourceMapping, job_key(scope, job_id))
            if stored is not None or meta['state'] == 'succeeded':
                row = owned_record(session, scope, job_id)
                if row.config.get('fence') != meta['fence'] or row.config.get('worker_id') != meta['worker_id']:
                    raise HTTPException(409, '建立任务版本不一致，请重试')
                stored_state = row.config.get('state')
    if stored_state == 'succeeded' and meta['state'] != 'succeeded':
        # Release the result store before taking the scheduler lock.
        meta = registry.reconcile(scope.identity, job_id, scope.workbench_id, 'succeeded')
    return metadata(meta)


async def job_status(scope, job_id):
    return await run_in_threadpool(fetch_status, await fresh_scope(scope), job_id)


def load_config(scope, job_id):
    with artifact_session(scope) as session:
        row = owned_record(session, scope, job_id)
        return deepcopy(row.config)


async def verified_config(scope, job_id):
    scope = await fresh_scope(scope)
    status = await run_in_threadpool(fetch_status, scope, job_id)
    if status['state'] != 'succeeded':
        raise HTTPException(410 if status['state'] in {'expired', 'superseded'} else 409,
            status['message'] or '建立任务尚未完成，请查看任务进度')
    config = await run_in_threadpool(load_config, scope, job_id)
    if config.get('state') != 'succeeded' or not isinstance(config.get('result'), dict):
        raise HTTPException(409, '建立结果未完整保存，请重新建立')
    body = workbench_setup.SetupInput.model_validate(config['request'])
    await report_chart_suggestions.verify_report(scope, body.source, config['source_identity'])
    await fresh_scope(scope)
    return await run_in_threadpool(load_config, scope, job_id)


def part_data(config, part):
    for item in config['result']['items']:
        if item['part'] == part and item['state'] == 'succeeded':
            return item['data']
    raise HTTPException(404, '本次建立没有该部分的待核对结果')


async def job_result(scope, job_id):
    config = await verified_config(scope, job_id)
    result = config['result']
    for item in result['items']:
        if item['part'] == 'judgment':
            # Full candidates and rule-review state only go through the native
            # render/validation path when the researcher opens this part.
            item['review_available'] = item.pop('data', None) is not None
    return result


def judgment_job_id(token):
    if not isinstance(token, str) or not re.fullmatch(r'setup\.[a-f0-9]{32}\.[A-Za-z0-9_-]{32}', token):
        raise HTTPException(409, '研判草稿已失效，请重新打开建立任务')
    return token.split('.')[1]


def judgment_record(session, scope, token):
    row = owned_record(session, scope, judgment_job_id(token))
    config = row.config
    if (config.get('state') != 'succeeded' or not hmac.compare_digest(config.get('judgment_token', ''), token)
            or config.get('judgment_closed')):
        raise HTTPException(409, '研判草稿已失效或已保存，请重新打开建立任务')
    return row


def load_judgment(scope, token):
    with artifact_session(scope) as session:
        row = judgment_record(session, scope, token)
        return {**deepcopy(part_data(row.config, 'judgment')), '_setup_job_id': row.config['id'], '_draft_id': token}


def replace_judgment(session, scope, token, preview, revision):
    row = judgment_record(session, scope, token)
    value = deepcopy(row.config)
    stored = part_data(value, 'judgment')
    if stored['review']['revision'] != revision or preview['review']['revision'] != revision + 1:
        raise HTTPException(409, '逐项确认已更新，请刷新后继续')
    stored.update({key: data for key, data in preview.items() if key not in {'_setup_job_id', '_draft_id'}})
    shared._update_record(session, row, value)


def close_judgment(session, scope, token, revision=None):
    row = judgment_record(session, scope, token)
    value = deepcopy(row.config)
    if revision is not None and part_data(value, 'judgment')['review']['revision'] != revision:
        raise HTTPException(409, '逐项确认已更新，请刷新预览后保存')
    for item in value['result']['items']:
        if item['part'] == 'judgment':
            item.pop('data', None)
    value['judgment_closed'] = True
    shared._update_record(session, row, value)


@router.post('', status_code=202)
async def create(request: Request, body: workbench_setup.SetupInput):
    return await start_job(require_scope(), body, request.headers.get('Idempotency-Key'))


@router.get('/latest')
async def latest():
    scope = await fresh_scope(require_scope())
    meta = await run_in_threadpool(registry.latest, scope.identity, scope.workbench_id, PURPOSE)
    return {'job': await run_in_threadpool(fetch_status, scope, meta['id']) if meta else None}


@router.get('/{job_id}')
async def status(job_id: str):
    return await job_status(require_scope(), job_id)


@router.get('/{job_id}/result')
async def result(job_id: str):
    return await job_result(require_scope(), job_id)


@router.post('/{job_id}/judgment-preview')
async def judgment_preview(job_id: str):
    from tin.web.judgment_review import current_preview, preview_response
    scope = require_scope()
    if 'judgments' not in scope.view_modules:
        raise HTTPException(403, '当前权限不能核对研判草稿')
    config = await verified_config(scope, job_id)
    token = config['judgment_token']
    preview, state = await current_preview(scope, token)
    return {**preview_response(scope, token, preview, state),
            **{key: preview.get(key) for key in ('variety', 'source_name', 'panel_title', 'model_id')}}


@router.post('/{job_id}/monitor-preview')
async def monitor_preview(job_id: str):
    scope = monitors.require_scope(require_scope(), write=True)
    config = await verified_config(scope, job_id)
    return await run_in_threadpool(monitors.save_private_draft, scope, part_data(config, 'monitors'),
                                   setup_job_id=job_id)
