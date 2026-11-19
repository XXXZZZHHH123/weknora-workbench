"""Detached draft work using the same scheduler and private store as judgments."""
import asyncio
from copy import deepcopy
from datetime import datetime, timezone
import json
import logging
import re
import sqlite3
import time

from fastapi import APIRouter, HTTPException, Query
from fastapi.encoders import jsonable_encoder
from starlette.concurrency import run_in_threadpool

from tin.integration.identity import current_identity
from tin.integration.workbench_scope import artifact_session, current_workbench
from tin.models import SourceMapping
from tin.web import judgment_jobs as shared
from tin.web.analysis_job_purposes import DRAFT_PURPOSES, PURPOSES, WORKBENCH_SETUP
from tin.web.judgment_job_registry import ACTIVE, iso, metadata

router = APIRouter(prefix='/api/sn/analysis-jobs')
registry = shared.registry
KIND = 'research-draft-analysis-job.v1'
DRAFT_SECONDS = 2000
result_checks = {}
result_views = {}
result_writers = {}
record_audits = {}


def _scope():
    scope = current_workbench.get()
    if scope is None or not scope.authorization:
        raise HTTPException(409, '请从已授权的研究工作台打开任务')
    return scope


def _require_access(scope, purpose):
    if PURPOSES[purpose].module not in scope.view_modules:
        raise HTTPException(403, '当前工作台未开放此后台任务所在页面')
    if purpose == WORKBENCH_SETUP:
        from tin.web.workbench_setup_entry import setup_enabled
        if not setup_enabled(scope):
            raise HTTPException(403, '当前权限不能从周报建立工作台')


def _grant(scope, purpose):
    fields = [shared.grant_digest(scope), scope.view_modules, scope.identity.role,
        scope.metadata.get('can_edit'), scope.metadata.get('can_read_source')]
    if purpose == WORKBENCH_SETUP:
        fields.append(scope.sources)
    return shared.digest(fields)


async def fresh_scope(scope, purpose):
    if purpose == WORKBENCH_SETUP:
        _require_access(scope, purpose)
    verified = await shared.fresh_scope(scope)
    _require_access(verified, purpose)
    if _grant(verified, purpose) != _grant(scope, purpose):
        raise HTTPException(409, '工作台来源或权限已改变，请重新发起')
    return verified


def job_key(scope, job_id):
    return shared.digest([KIND, scope.identity.tenant_id, scope.identity.user_id, scope.workbench_id, job_id])


def owned_record(session, scope, job_id, purpose):
    _require_access(scope, purpose)
    row = session.get(SourceMapping, job_key(scope, job_id))
    value = row.config if row is not None and isinstance(row.config, dict) else {}
    expected = {'kind': KIND, 'id': job_id, 'tenant_id': scope.identity.tenant_id,
        'user_id': scope.identity.user_id, 'workbench_id': scope.workbench_id}
    if (row is None or row.confirmed_by != scope.identity.user_id
            or any(value.get(key) != item for key, item in expected.items())):
        raise HTTPException(404, '分析任务不存在或无权访问')
    if value.get('state') == 'superseded' or value.get('expires_unix', 0) <= time.time():
        raise HTTPException(410, '分析结果已过期或已有更新结果，请重新打开任务')
    if value.get('purpose') != purpose:
        raise HTTPException(404, '分析任务不存在或无权访问')
    if value.get('grant_digest') != _grant(scope, purpose):
        raise HTTPException(409, '工作台来源或权限已改变，请重新发起')
    if value.get('state') == 'succeeded':
        registry.guard_result(scope.identity, job_id, scope.workbench_id, purpose)
    return row


def _create_private(scope, body, meta):
    value = {'kind': KIND, 'id': meta['id'], 'tenant_id': scope.identity.tenant_id,
        'user_id': scope.identity.user_id, 'workbench_id': scope.workbench_id,
        'purpose': meta['purpose'], 'grant_digest': _grant(scope, meta['purpose']), 'fence': meta['fence'],
        'worker_id': meta['worker_id'], 'expires_unix': meta['expires_unix'],
        'request': body, 'state': 'queued', 'result': None}
    with artifact_session(scope) as session:
        session.add(SourceMapping(id=job_key(scope, meta['id']), config=value, version=1,
            confirmed_by=scope.identity.user_id, confirmed_at=datetime.now(timezone.utc)))
        audit = record_audits.get(meta['purpose'])
        if audit is not None:
            audit(session, scope, meta['id'], body)
        session.commit()


def _complete(scope, meta, result):
    if (not isinstance(result, dict) or len(json.dumps(result, ensure_ascii=False, allow_nan=False)
            .encode()) > shared.MAX_PRIVATE_BYTES):
        raise HTTPException(422, '分析结果超过暂存容量，请减少输入范围后重新发起')
    purpose = meta['purpose']
    with registry.fenced(meta['id'], meta['worker_id'], meta['fence']) as (connection, current):
        current.update(state='succeeded', phase='succeeded', updated_at=iso(), finished_at=iso())
        registry.write(connection, current)
        retired = [old for old in registry.compact(connection)
                   if registry.result_key(old) == registry.result_key(current)]
        with artifact_session(scope) as session:
            row = owned_record(session, scope, meta['id'], purpose)
            if row.config['fence'] != meta['fence'] or row.config['state'] != 'queued' or row.version != 1:
                raise HTTPException(409, '分析任务已中断，请重新发起')
            if all(old['id'] != meta['id'] for old in retired):
                writer = result_writers.get(purpose)
                if writer is not None:
                    result = writer(session, scope, row.config['request'], result)
                shared._update_record(session, row, {**row.config, 'state': 'succeeded', 'result': result})
                audit = record_audits.get(purpose)
                if audit is not None:
                    audit(session, scope, meta['id'], row.config['request'], result)
            for old in retired:
                shared._discard_private_record(session, old, KIND)
            session.commit()
        for old in retired:
            old['payload_pruned'] = True
            registry.write(connection, old)


def cleanup_expired():
    shared.cleanup_discarded(KIND, registry, DRAFT_PURPOSES)


def _failure_message(error):
    if isinstance(error, asyncio.TimeoutError):
        return '后台分析超时，请缩小范围后重新发起'
    if isinstance(error, HTTPException):
        if isinstance(error.detail, str):
            return error.detail[:200]
        if isinstance(error.detail, dict) and isinstance(error.detail.get('message'), str):
            return error.detail['message'][:200]
    return '后台分析未完成，请稍后重试'


class Runtime(shared.Runtime):
    async def sweep(self):
        while True:
            try:
                await run_in_threadpool(cleanup_expired)
            except (sqlite3.Error, OSError, HTTPException):
                logging.getLogger(__name__).warning('Research draft cleanup deferred')
            await asyncio.sleep(60)

    async def run(self, scope, work, meta):
        body, generate, timeout = work
        purpose = meta['purpose']
        identity_marker = current_identity.set(scope.identity)
        scope_marker = current_workbench.set(scope)
        heartbeat = asyncio.create_task(self.heartbeat(meta, asyncio.current_task()))
        try:
            deadline = time.monotonic() + shared.QUEUE_SECONDS
            while not await run_in_threadpool(registry.claim, meta['id'], self.worker_id, meta['fence']):
                if time.monotonic() >= deadline:
                    raise HTTPException(503, '等待分析超时，请重新发起')
                await asyncio.sleep(.25)
            verified = await fresh_scope(scope, purpose)
            current_workbench.set(verified)
            current_identity.set(verified.identity)

            async def progress(phase):
                if phase not in PURPOSES[purpose].phases or phase == 'queued':
                    raise ValueError('Unsupported research draft phase')
                await run_in_threadpool(registry.touch, meta['id'], self.worker_id, meta['fence'], phase)

            result = await asyncio.wait_for(generate(verified, body, progress, meta['id']), timeout)
            await fresh_scope(verified, purpose)
            await run_in_threadpool(_complete, verified, meta, jsonable_encoder(result))
        except asyncio.CancelledError:
            await run_in_threadpool(registry.finish, meta['id'], self.worker_id, meta['fence'],
                'interrupted', '分析已中断，请重新发起')
        except Exception as error:
            await run_in_threadpool(registry.finish, meta['id'], self.worker_id, meta['fence'],
                'failed', _failure_message(error))
        finally:
            heartbeat.cancel()
            await asyncio.gather(heartbeat, return_exceptions=True)
            current_workbench.reset(scope_marker)
            current_identity.reset(identity_marker)


runtime = Runtime()


async def start_job(scope, purpose, body, idempotency_key, generate, *, timeout=DRAFT_SECONDS,
                    result_parent=None, result_slot=None):
    if purpose not in DRAFT_PURPOSES:
        raise ValueError('Unsupported draft analysis purpose')
    if not isinstance(idempotency_key, str) or not re.fullmatch(r'[\x21-\x7e]{16,128}', idempotency_key):
        raise HTTPException(422, '请提供有效的分析请求标识')
    verified = await fresh_scope(scope, purpose)
    if runtime.closing:
        raise HTTPException(503, '服务正在重启，请稍后发起分析')

    async def admit():
        meta, created = None, False
        try:
            await run_in_threadpool(cleanup_expired)
            meta, created = await run_in_threadpool(registry.create, verified, shared.digest(body),
                shared.digest(idempotency_key), runtime.worker_id, purpose, result_parent=result_parent,
                result_slot=result_slot)
            if created:
                await run_in_threadpool(_create_private, verified, body, meta)
                if runtime.closing:
                    raise asyncio.CancelledError()
                runtime.launch(verified, (body, generate, timeout), meta)
            return metadata(meta)
        except asyncio.CancelledError:
            if created:
                await run_in_threadpool(registry.finish, meta['id'], runtime.worker_id, meta['fence'],
                    'interrupted', '分析已中断，请重新发起')
            raise
        except Exception:
            if created:
                await run_in_threadpool(registry.finish, meta['id'], runtime.worker_id, meta['fence'],
                    'failed', '任务未能创建，请重新发起')
            raise

    admission = asyncio.create_task(admit(), name='research-draft-analysis-admission')
    runtime.admissions.add(admission)

    def admitted(done):
        runtime.admissions.discard(done)
        if not done.cancelled():
            done.exception()

    admission.add_done_callback(admitted)
    return await asyncio.shield(admission)


def _context(purpose, body):
    keys = {'report_dimension_draft': ('day', 'period'), 'judgment_dimensions': ('judgment_id',),
            'judgment_review_dimensions': ('draft_id', 'revision'),
            'judgment_indicator_recommendations': ('draft_id', 'item_id')}.get(purpose, ())
    return {key: body[key] for key in keys if key in body}


def _read_private(scope, meta):
    with artifact_session(scope) as session:
        row = owned_record(session, scope, meta['id'], meta['purpose'])
        value = deepcopy(row.config)
        completed = value.get('state') == 'succeeded' and row.version >= 2 and isinstance(value.get('result'), dict)
        if ((value.get('worker_id'), value.get('fence')) != (meta['worker_id'], meta['fence'])
                or meta['state'] == 'succeeded' and not completed):
            raise HTTPException(409, '分析结果未完整保存，请重新发起')
        return value, completed


def navigation_context(scope, job_id, purpose):
    """Read a draft's route parameters from its private workbench record."""
    if scope is None or not scope.authorization or scope.restricted or not scope.can_edit:
        return {}
    if not isinstance(job_id, str) or not re.fullmatch(r'[a-f0-9]{32}', job_id):
        return {}
    try:
        meta = registry.get(scope.identity, job_id, scope.workbench_id)
        if meta.get('purpose') == purpose and meta['state'] in ACTIVE | {'succeeded'}:
            value, _ = _read_private(scope, meta)
            return _context(purpose, value['request'])
    except (HTTPException, OSError, sqlite3.Error):
        pass
    return {}


async def _checked_status(scope, job_id, expected_purpose=None):
    meta = await run_in_threadpool(registry.get, scope.identity, job_id, scope.workbench_id)
    purpose = meta.get('purpose', 'judgments')
    if (purpose not in PURPOSES or expected_purpose and expected_purpose != purpose):
        raise HTTPException(404, '分析任务不存在或无权访问')
    verified = await fresh_scope(scope, purpose)
    if purpose not in DRAFT_PURPOSES or meta['state'] in {'expired', 'superseded'}:
        return verified, metadata(meta), None
    try:
        value, completed = await run_in_threadpool(_read_private, verified, meta)
    except HTTPException as error:
        if error.status_code == 409:
            raise HTTPException(409, {'message': error.detail, 'purpose': purpose}) from None
        if meta['state'] != 'failed' or error.status_code != 404:
            raise
        return verified, metadata(meta), None
    if completed and meta['state'] != 'succeeded':
        meta = await run_in_threadpool(registry.reconcile, verified.identity, job_id, verified.workbench_id, 'succeeded')
    return verified, {**metadata(meta), 'context': _context(purpose, value['request'])}, value


async def status(scope, job_id):
    return (await _checked_status(scope, job_id))[1]


async def verified_result(scope, job_id, expected_purpose=None, *, project=False):
    verified, job, value = await _checked_status(scope, job_id, expected_purpose)
    if job['purpose'] not in DRAFT_PURPOSES:
        raise HTTPException(404, '分析任务不存在或无权访问')
    if job['state'] != 'succeeded' or value is None:
        raise HTTPException(410 if job['state'] in {'expired', 'superseded'} else 409,
            job.get('message') or '分析尚未完成，请稍后打开任务')
    result = value['result']
    identity_marker = current_identity.set(verified.identity)
    scope_marker = current_workbench.set(verified)
    try:
        check = result_checks.get(job['purpose'])
        if check is not None:
            await check(verified, value['request'], result)
        view = result_views.get(job['purpose']) if project else None
        if view is not None:
            value['result'] = await view(verified, value)
        await fresh_scope(verified, job['purpose'])
        await run_in_threadpool(registry.guard_result, verified.identity, job_id,
            verified.workbench_id, job['purpose'])
        return job, value
    finally:
        current_workbench.reset(scope_marker)
        current_identity.reset(identity_marker)


async def job_result(scope, job_id, expected_purpose=None):
    job, value = await verified_result(scope, job_id, expected_purpose, project=True)
    return {**{key: item for key, item in value['result'].items() if not key.startswith('_')},
        'request': value['request'], 'job': job}


@router.get('/latest')
async def latest(purpose: str = Query(...)):
    scope = _scope()
    if purpose not in DRAFT_PURPOSES:
        raise HTTPException(404, '分析任务不存在或无权访问')
    verified = await fresh_scope(scope, purpose)
    meta = await run_in_threadpool(registry.latest, verified.identity, verified.workbench_id,
        purpose, include_active=True)
    return {'job': await status(verified, meta['id']) if meta is not None else None}


@router.get('/{job_id}')
async def status_route(job_id: str):
    return await status(_scope(), job_id)


@router.get('/{job_id}/result')
async def result_route(job_id: str):
    return await job_result(_scope(), job_id)
