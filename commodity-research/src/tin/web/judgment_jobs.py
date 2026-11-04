"""Detached, owner-scoped analysis tasks; completed work remains a private preview."""
import asyncio
from copy import deepcopy
import hashlib
import hmac
import json
import re
import secrets
import time
import logging
import sqlite3
from datetime import datetime, timezone

from fastapi import APIRouter, HTTPException, Query, Request
from sqlalchemy import update
from sqlalchemy.exc import IntegrityError, OperationalError
from starlette.concurrency import run_in_threadpool

from tin.integration.identity import current_identity
from tin.integration.workbench_scope import (
    artifact_session, current_workbench, isolated_session_factory, result_session_factory,
    resolve_workbench, require_shared_write, isolated_database_path,
)
from tin.db import tenant_database_path
from tin.models import SourceMapping
from tin.web import judgment_generation as generation
from tin.web.judgment_job_registry import Registry, LEASE, metadata, iso

router = APIRouter(prefix='/api/sn/judgment/jobs')
global_router = APIRouter(prefix='/api/research/analysis-jobs')
KIND = 'judgment-analysis-job.v1'
MAX_PRIVATE_BYTES = 8 * 1024 * 1024
QUEUE_SECONDS = 900
registry = Registry()


def digest(value):
    return hashlib.sha256(json.dumps(value, ensure_ascii=False, sort_keys=True,
        separators=(',', ':'), allow_nan=False).encode()).hexdigest()


def job_key(scope, job_id):
    return digest([KIND, scope.identity.tenant_id, scope.identity.user_id, scope.workbench_id, job_id])


def is_job_token(token):
    return isinstance(token, str) and token.startswith('job.')


def token_job_id(token):
    if not isinstance(token, str) or not re.fullmatch(r'job\.[a-f0-9]{32}\.[A-Za-z0-9_-]{32,64}', token):
        raise HTTPException(409, '分析预览已失效，请重新打开任务')
    return token.split('.')[1]


def grant_digest(scope):
    return digest([generation.scope_key(scope), scope.additional_knowledge_bases, scope.source_delegations])


def owned_record(session, scope, job_id, *, token=None):
    row = session.get(SourceMapping, job_key(scope, job_id))
    config = row.config if row is not None and isinstance(row.config, dict) else {}
    expected = {'kind': KIND, 'tenant_id': scope.identity.tenant_id, 'user_id': scope.identity.user_id,
        'workbench_id': scope.workbench_id, 'id': job_id}
    if (row is None or row.confirmed_by != scope.identity.user_id
            or type(row.version) is not int or row.version < 1
            or any(config.get(key) != value for key, value in expected.items())
            or token is not None and not hmac.compare_digest(config.get('draft_id', ''), token)):
        raise HTTPException(404, '分析任务不存在或无权访问')
    if config.get('grant_digest') != grant_digest(scope):
        raise HTTPException(409, '工作台来源或权限已改变，请重新分析')
    state = config.get('state')
    if state == 'queued':
        valid_version = row.version == 1 and config.get('preview') is None
    elif state in {'succeeded', 'applied'}:
        revision = (config.get('preview') or {}).get('review', {}).get('revision')
        valid_version = type(revision) is int and revision >= 1 and row.version == revision + (2 if state == 'applied' else 1)
    elif state == 'superseded':
        valid_version = config.get('superseded_revision') == row.version and config.get('preview') is None
    else:
        valid_version = False
    if not valid_version:
        raise HTTPException(409, '分析预览版本无效，请重新发起')
    if state in {'succeeded', 'applied'}:
        registry.guard_result(scope.identity, job_id, scope.workbench_id, 'judgments')
    return row


def available(config):
    if config.get('state') == 'superseded':
        raise HTTPException(410, '已有更新的分析结果，请打开最新结果')
    if config.get('expires_unix', 0) <= time.time():
        raise HTTPException(410, '分析结果已过期，请重新分析')
    if config.get('state') == 'applied':
        raise HTTPException(409, '此分析预览已经保存，请查看已保存的草稿')
    if config.get('state') != 'succeeded' or not isinstance(config.get('preview'), dict):
        raise HTTPException(409, '分析尚未完成或已中断，请查看任务状态')


def load_preview(scope, token):
    job_id = token_job_id(token)
    with artifact_session(scope) as session:
        config = owned_record(session, scope, job_id, token=token).config
        available(config)
        value = deepcopy(config['preview'])
        value.update(_job_id=job_id, _draft_id=token)
        return value


def replace_preview(session, scope, token, preview, revision):
    row = owned_record(session, scope, token_job_id(token), token=token)
    available(row.config)
    if row.config['preview']['review']['revision'] != revision:
        raise HTTPException(409, '分析预览已更新，请重新打开后继续')
    if preview.get('review', {}).get('revision') != revision + 1:
        raise HTTPException(409, '分析预览版本不一致，请重新打开后继续')
    config = {**row.config, 'preview': deepcopy(preview)}
    _update_record(session, row, config)


def atomic_replace(scope, token, preview, revision):
    with artifact_session(scope) as session:
        replace_preview(session, scope, token, preview, revision)
        session.commit()


def _update_record(session, row, config):
    if len(json.dumps(config, ensure_ascii=False, allow_nan=False).encode()) > MAX_PRIVATE_BYTES:
        raise HTTPException(413, '分析预览超过任务保存容量，请缩小范围')
    try:
        changed = session.execute(update(SourceMapping).where(SourceMapping.id == row.id,
            SourceMapping.version == row.version).values(config=config, version=row.version + 1,
                confirmed_at=datetime.now(timezone.utc)), execution_options={'synchronize_session': False})
        if changed.rowcount != 1:
            raise HTTPException(409, '分析预览已更新，请重新打开后继续')
    except (IntegrityError, OperationalError):
        raise HTTPException(409, '分析预览正在更新，请稍后重新打开') from None


def mark_applied(session, scope, preview):
    if not preview.get('_job_id'):
        return
    row = owned_record(session, scope, preview['_job_id'], token=preview['_draft_id'])
    available(row.config)
    if row.config['preview']['review']['revision'] != preview['review']['revision']:
        raise HTTPException(409, '分析预览已更新，请重新打开后保存')
    _update_record(session, row, {**row.config, 'state': 'applied', 'applied_at': iso()})


def acknowledge_applied(scope, job_id):
    try:
        registry.applied(scope.identity, job_id, scope.workbench_id)
    except (sqlite3.Error, OSError):
        # The authoritative WB transaction has already saved and consumed the
        # preview. A list/status read can repair this advisory index afterward.
        logging.getLogger(__name__).warning('Analysis task index synchronization deferred')


def _create_private(scope, body, meta):
    token = 'job.' + meta['id'] + '.' + secrets.token_urlsafe(24)
    now = datetime.now(timezone.utc)
    config = {'kind': KIND, 'id': meta['id'], 'tenant_id': scope.identity.tenant_id,
        'user_id': scope.identity.user_id, 'workbench_id': scope.workbench_id,
        'state': 'queued', 'draft_id': token, 'grant_digest': grant_digest(scope),
        'fence': meta['fence'], 'worker_id': meta['worker_id'], 'expires_unix': meta['expires_unix'],
        'input': body.model_dump(mode='json'), 'preview': None}
    with artifact_session(scope) as session:
        session.add(SourceMapping(id=job_key(scope, meta['id']), config=config, version=1,
            confirmed_by=scope.identity.user_id, confirmed_at=now))
        session.commit()


def _complete(scope, meta, preview):
    with registry.fenced(meta['id'], meta['worker_id'], meta['fence']) as (connection, current):
        current.update(state='succeeded', phase='succeeded', updated_at=iso(), finished_at=iso())
        registry.write(connection, current)
        retired = [item for item in registry.compact(connection)
            if registry.scope_key(item) == registry.scope_key(current)]
        superseded = any(item['id'] == meta['id'] for item in retired)
        with artifact_session(scope) as session:
            # Serialize with a review/save transaction before reading versions.
            # A save already committed remains a business record; a stale save
            # started afterward sees the superseded private preview.
            session.execute(update(SourceMapping).where(SourceMapping.id == job_key(scope, meta['id']))
                .values(version=SourceMapping.version), execution_options={'synchronize_session': False})
            row = owned_record(session, scope, meta['id'])
            if (row.config.get('fence') != meta['fence'] or row.config.get('state') != 'queued'):
                raise HTTPException(409, '分析任务已中断，请重新发起')
            # An older submission can finish after a newer successful one.
            # Retire its queued private row directly: publishing and then
            # pruning that same row would use an obsolete CAS revision.
            if not superseded:
                value = deepcopy(preview)
                value.update(_job_id=meta['id'], _draft_id=row.config['draft_id'])
                _update_record(session, row, {**row.config, 'state': 'succeeded', 'preview': value})
            for old in retired:
                _discard_private_record(session, old, KIND)
            session.commit()
        for old in retired:
            old['payload_pruned'] = True
            registry.write(connection, old)
        if not superseded:
            try:
                from tin.web.judgment_analysis_cache import prune_snapshots_after_success
                prune_snapshots_after_success(scope, preview.get('analysis_snapshot'))
            except Exception:
                logging.getLogger(__name__).warning('Analysis snapshot cleanup deferred')


async def fresh_scope(scope):
    verified = await resolve_workbench(scope.identity, scope.workbench_id, scope.authorization)
    require_shared_write(verified)
    if (not verified.identity.can_write or verified.restricted
            or verified.metadata.get('can_read_source') is False):
        raise HTTPException(403, '当前权限不能分析或读取分析预览')
    if grant_digest(verified) != grant_digest(scope):
        raise HTTPException(409, '工作台来源或权限已改变，请重新分析')
    return verified


def _storage_factory(meta):
    path = (tenant_database_path(meta['tenant_id']) if meta['storage_legacy']
        else isolated_database_path(meta['tenant_id'], meta['workbench_id']))
    if not path.exists():
        return None
    return (result_session_factory(tenant_database_path(meta['tenant_id'])) if meta['storage_legacy']
        else isolated_session_factory(meta['tenant_id'], meta['workbench_id']))


def _discard_private_record(session, meta, kind):
    key = digest([kind, meta['tenant_id'], meta['user_id'], meta['workbench_id'], meta['id']])
    row = session.get(SourceMapping, key)
    config = row.config if row is not None and isinstance(row.config, dict) else {}
    expected = {'kind': kind, 'id': meta['id'], 'tenant_id': meta['tenant_id'],
        'user_id': meta['user_id'], 'workbench_id': meta['workbench_id']}
    if (row is None or row.confirmed_by != meta['user_id']
            or any(config.get(name) != value for name, value in expected.items())):
        return
    if meta['state'] == 'expired':
        session.delete(row)
        return
    if config.get('state') == 'superseded':
        return
    # Retain only authority and a tombstone, never previous source or result.
    value = {name: config[name] for name in ('kind', 'id', 'tenant_id', 'user_id',
        'workbench_id', 'grant_digest', 'fence', 'worker_id', 'expires_unix', 'draft_id') if name in config}
    value.update(state='superseded', superseded_by=meta.get('superseded_by'),
        superseded_revision=row.version + 1)
    if kind == KIND:
        value.update(input={}, preview=None)
    else:
        value.update(request={}, result=None, source_identity=None)
    _update_record(session, row, value)


def cleanup_discarded(kind, index, purpose):
    for meta in [row for row in index.discarded() if row.get('purpose', 'judgments') == purpose][:20]:
        factory = _storage_factory(meta)
        if factory is not None:
            with factory() as session:
                _discard_private_record(session, meta, kind)
                session.commit()
        if meta['state'] == 'expired':
            index.remove(meta['id'])
        else:
            index.mark_pruned(meta['id'])


def cleanup_expired():
    cleanup_discarded(KIND, registry, 'judgments')


class Runtime:
    def __init__(self):
        self.worker_id = secrets.token_hex(16)
        self.tasks = {}
        self.admissions = set()
        self.maintenance = None
        self.closing = False

    async def startup(self):
        self.closing = False
        if self.maintenance is None:
            self.maintenance = asyncio.create_task(self.sweep(), name='judgment-analysis-maintenance')

    async def sweep(self):
        while True:
            try:
                await run_in_threadpool(cleanup_expired)
            except (sqlite3.Error, OSError, HTTPException):
                logging.getLogger(__name__).warning('Analysis task cleanup deferred')
            await asyncio.sleep(60)

    def launch(self, scope, body, meta):
        task = asyncio.create_task(self.run(scope, body, meta), name='judgment-analysis-' + meta['id'])
        self.tasks[meta['id']] = task
        def finished(done):
            self.tasks.pop(meta['id'], None)
            if not done.cancelled() and done.exception() is not None:
                logging.getLogger(__name__).warning('Analysis task stopped before status synchronization')
        task.add_done_callback(finished)

    async def heartbeat(self, meta, work):
        try:
            while True:
                await asyncio.sleep(LEASE / 4)
                await run_in_threadpool(registry.touch, meta['id'], self.worker_id, meta['fence'])
        except asyncio.CancelledError:
            raise
        except Exception:
            work.cancel()

    async def run(self, scope, body, meta):
        failure_stage = 'queued'
        identity_marker = current_identity.set(scope.identity)
        scope_marker = current_workbench.set(scope)
        heartbeat = asyncio.create_task(self.heartbeat(meta, asyncio.current_task()))
        try:
            deadline = time.monotonic() + QUEUE_SECONDS
            while not await run_in_threadpool(registry.claim, meta['id'], self.worker_id, meta['fence']):
                if time.monotonic() >= deadline:
                    raise HTTPException(503, '等待分析超时，请重新发起')
                await asyncio.sleep(0.25)

            async def progress(phase):
                nonlocal failure_stage
                from tin.web.judgment_failures import STAGES
                failure_stage = phase if isinstance(phase, str) and phase in STAGES else 'unknown'
                await fresh_scope(scope)
                await run_in_threadpool(registry.touch, meta['id'], self.worker_id, meta['fence'], phase)

            preview, _ = await asyncio.wait_for(generation._analyze(scope, body, progress),
                timeout=generation.GENERATION_SECONDS)
            await fresh_scope(scope)
            await run_in_threadpool(_complete, scope, meta, preview)
        except asyncio.CancelledError:
            await run_in_threadpool(registry.finish, meta['id'], self.worker_id, meta['fence'],
                'interrupted', '分析已中断，请重新发起')
        except Exception as error:
            from tin.web.judgment_failures import failure_for
            failure = failure_for(error, failure_stage)
            await run_in_threadpool(registry.finish, meta['id'], self.worker_id, meta['fence'],
                'failed', failure['message'], diagnostic=failure['diagnostic'])
        finally:
            heartbeat.cancel()
            await asyncio.gather(heartbeat, return_exceptions=True)
            current_workbench.reset(scope_marker)
            current_identity.reset(identity_marker)

    async def shutdown(self):
        self.closing = True
        tasks = list(self.tasks.values()) + list(self.admissions)
        if self.maintenance is not None:
            tasks.append(self.maintenance)
        for task in tasks:
            task.cancel()
        if tasks:
            await asyncio.gather(*tasks, return_exceptions=True)
        self.maintenance = None


runtime = Runtime()


async def start_job(scope, body, idempotency_key):
    if not isinstance(idempotency_key, str) or not re.fullmatch(r'[\x21-\x7e]{16,128}', idempotency_key):
        raise HTTPException(422, '请提供有效的分析请求标识')
    if not body.text.strip() or len(body.text.encode('utf-8')) > generation.SOURCE_BYTES:
        raise HTTPException(422, '请提供48 KiB以内的研究原文')
    await fresh_scope(scope)
    if runtime.closing:
        raise HTTPException(503, '服务正在重启，请稍后发起分析')

    async def admit():
        meta, created = None, False
        try:
            await run_in_threadpool(cleanup_expired)
            meta, created = await run_in_threadpool(registry.create, scope, digest(body.model_dump(mode='json')),
                digest(idempotency_key), runtime.worker_id)
            if not created:
                return metadata(meta)
            await run_in_threadpool(_create_private, scope, body, meta)
            if runtime.closing:
                raise asyncio.CancelledError()
            runtime.launch(scope, body, meta)
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

    admission = asyncio.create_task(admit(), name='judgment-analysis-admission')
    runtime.admissions.add(admission)
    def admitted(done):
        runtime.admissions.discard(done)
        if not done.cancelled():
            done.exception()
    admission.add_done_callback(admitted)
    return await asyncio.shield(admission)


async def job_status(scope, job_id):
    await fresh_scope(scope)
    meta = await run_in_threadpool(registry.get, scope.identity, job_id, scope.workbench_id)
    if meta.get('purpose', 'judgments') != 'judgments':
        raise HTTPException(404, '分析任务不存在或无权访问')
    if meta['state'] in {'superseded', 'expired'}:
        return metadata(meta)
    with artifact_session(scope) as session:
        stored = session.get(SourceMapping, job_key(scope, job_id))
        row = owned_record(session, scope, job_id) if stored is not None or meta['state'] == 'succeeded' else None
        state = row.config.get('state') if row is not None else None
    if state in {'succeeded', 'applied', 'superseded'} and state != meta['state']:
        meta = await run_in_threadpool(registry.reconcile, scope.identity, job_id, scope.workbench_id, state)
    return metadata(meta)


async def job_result(scope, job_id):
    verified = await fresh_scope(scope)
    identity_marker = current_identity.set(verified.identity)
    scope_marker = current_workbench.set(verified)
    try:
        return await _job_result(verified, job_id)
    finally:
        current_workbench.reset(scope_marker)
        current_identity.reset(identity_marker)


async def _job_result(verified, job_id):
    await job_status(verified, job_id)
    meta = await run_in_threadpool(registry.get, verified.identity, job_id, verified.workbench_id)
    if meta['state'] != 'succeeded':
        raise HTTPException(410 if meta['state'] in {'expired', 'superseded'} else 409,
            meta.get('message') or '分析尚未完成或预览已经保存')
    with artifact_session(verified) as session:
        row = owned_record(session, verified, job_id)
        available(row.config)
        token = row.config['draft_id']
    preview = await run_in_threadpool(load_preview, verified, token)
    from tin.web.judgment_review import review_snapshot, preview_response
    state = await run_in_threadpool(review_snapshot, verified, preview)
    return {**preview_response(verified, token, preview, state), 'job': metadata(meta),
        **{key: preview.get(key) for key in ('variety', 'source_name', 'panel_title', 'model_id')}}


def list_jobs(identity, limit=20):
    if identity is None:
        raise HTTPException(401, '请先登录研究工作台')
    return {'items': [metadata(meta) for meta in registry.listing(identity, limit)]}


def dismiss_job(identity, job_id):
    if identity is None:
        raise HTTPException(401, '请先登录研究工作台')
    return metadata(registry.dismiss(identity, job_id))


async def latest_job(scope):
    await fresh_scope(scope)
    meta = await run_in_threadpool(registry.latest, scope.identity, scope.workbench_id, 'judgments')
    return {'job': metadata(meta) if meta is not None else None}


def _discard_snapshot_key(scope, job_id):
    with artifact_session(scope) as session:
        config = owned_record(session, scope, job_id).config
        return (config.get('preview') or {}).get('analysis_snapshot') if config.get('state') == 'succeeded' else None


def _forget_discarded(scope, key):
    cleanup_discarded(KIND, registry, 'judgments')
    from tin.web.judgment_analysis_cache import forget_snapshot
    forget_snapshot(scope, key)


async def discard_job(scope, job_id):
    verified = await fresh_scope(scope)
    meta = await run_in_threadpool(registry.get, verified.identity, job_id, verified.workbench_id)
    if meta.get('purpose', 'judgments') != 'judgments':
        raise HTTPException(404, '分析任务不存在或无权访问')
    key = await run_in_threadpool(_discard_snapshot_key, verified, job_id) if meta['state'] == 'succeeded' else None
    meta = await run_in_threadpool(registry.discard_result, verified.identity, job_id, verified.workbench_id, 'judgments')
    try:
        await run_in_threadpool(_forget_discarded, verified, key)
    except (sqlite3.Error, OSError, HTTPException):
        # The result is already retired; the maintenance sweep prunes the private preview later.
        logging.getLogger(__name__).warning('Discarded analysis cleanup deferred')
    return {'job': metadata(meta)}


@router.post('', status_code=202)
async def create(request: Request):
    scope = generation.scope_for_analysis(write=True)
    body = await generation.read_body(request, generation.GenerateRequest)
    return await start_job(scope, body, request.headers.get('idempotency-key'))


@router.get('/latest')
async def latest():
    return await latest_job(generation.scope_for_analysis(write=True))


@router.get('/{job_id}')
async def status(job_id: str):
    return await job_status(generation.scope_for_analysis(write=True), job_id)


@router.post('/{job_id}/discard')
async def discard(job_id: str):
    return await discard_job(generation.scope_for_analysis(write=True), job_id)


@router.get('/{job_id}/result')
async def result(job_id: str):
    return await job_result(generation.scope_for_analysis(write=True), job_id)


@global_router.get('')
def listing(request: Request, limit: int = Query(default=20, ge=1, le=20)):
    if set(request.query_params) - {'limit'}:
        raise HTTPException(422, '任务查询参数无效')
    return list_jobs(current_identity.get(), limit)


@global_router.post('/{job_id}/dismiss')
def dismiss(job_id: str):
    return dismiss_job(current_identity.get(), job_id)


@global_router.post('/dismiss-all')
async def dismiss_all(request: Request):
    identity = current_identity.get()
    if identity is None:
        raise HTTPException(401, '请先登录研究工作台')
    if request.query_params:
        raise HTTPException(422, '批量忽略不接受账号或任务选择参数')
    async for chunk in request.stream():
        if chunk:
            raise HTTPException(422, '批量忽略无需请求正文')
    return {'dismissed_ids': await run_in_threadpool(registry.dismiss_all, identity)}
