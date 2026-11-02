"""Durable private diagram previews sharing the research analysis admission limits."""
import asyncio
from copy import deepcopy
from datetime import datetime, timezone
import json
import logging
import re
import sqlite3
import time

from fastapi import APIRouter, HTTPException, Request
from pydantic import ValidationError
from sqlalchemy import update
from sqlalchemy.exc import IntegrityError, OperationalError
from starlette.concurrency import run_in_threadpool

from tin.integration.identity import current_identity
from tin.integration.workbench_scope import artifact_session, current_workbench, require_shared_write, resolve_workbench, source_scope_for_binding
from tin.models import SourceMapping
from tin.web import diagram_generation as generation
from tin.web import judgment_jobs as shared
from tin.web.judgment_job_registry import LEASE, iso, metadata

router = APIRouter(prefix='/api/sn/diagram/jobs')
KIND = 'diagram-generation-job.v1'
PURPOSE = 'diagram'
GENERATION_SECONDS = 600
MAX_PRIVATE_BYTES = 512 * 1024
MAX_REQUEST_BYTES = 8 * 1024 * 1024
registry = shared.registry


def trusted_native_messages():
    details = [{'phase': ('response_validation' if kind in {'empty_response', 'invalid_response'} else 'completion'),
        'error_class': kind} for kind in ('request_timeout', 'transport_error', 'outbound_blocked',
            'request_cancelled', 'client_error', 'empty_response', 'invalid_response')]
    details.extend({'phase': 'completion', 'error_class': 'provider_http_error', 'provider_http_status': status}
        for status in range(400, 600))
    return frozenset(message for item in details
        if isinstance(message := generation.native_failure_message({'code': 1008, 'details': item}, 503), str))


TRUSTED_NATIVE_MESSAGES = trusted_native_messages()


def job_key(scope, job_id):
    return shared.digest([KIND, scope.identity.tenant_id, scope.identity.user_id, scope.workbench_id, job_id])


def owned_record(session, scope, job_id):
    row = session.get(SourceMapping, job_key(scope, job_id))
    value = row.config if row is not None and isinstance(row.config, dict) else {}
    expected = {'kind': KIND, 'id': job_id, 'tenant_id': scope.identity.tenant_id,
        'user_id': scope.identity.user_id, 'workbench_id': scope.workbench_id}
    if (row is None or row.confirmed_by != scope.identity.user_id
            or any(value.get(key) != item for key, item in expected.items())):
        raise HTTPException(404, '产业图任务不存在或无权访问')
    if value.get('grant_digest') != shared.grant_digest(scope):
        raise HTTPException(409, '工作台来源或权限已改变，请重新生成')
    if isinstance(value.get('result'), dict):
        registry.guard_result(scope.identity, job_id, scope.workbench_id, PURPOSE)
    return row


async def fresh_scope(scope):
    verified = await resolve_workbench(scope.identity, scope.workbench_id, scope.authorization)
    require_shared_write(verified)
    if not verified.identity.can_write or verified.restricted or verified.metadata.get('can_read_source') is False:
        raise HTTPException(403, '当前权限不能生成或读取产业图配置')
    if shared.grant_digest(verified) != shared.grant_digest(scope):
        raise HTTPException(409, '工作台来源或权限已改变，请重新生成')
    return verified


async def original_fingerprint(scope, source_id):
    if not source_id:
        return None
    local, identity = scope, None
    if scope.scope_version == 2:
        identity = await generation.original_identity(scope, source_id)
        matches = [source for source in scope.sources if not source['delegated']
            and source['knowledge_base_id'] == identity['knowledge_base_id']]
        if len(matches) != 1:
            raise HTTPException(403, '原件不属于当前直接授权的数据来源')
        local = source_scope_for_binding(scope, matches[0]['id'], direct_only=True)
    content = await generation.source_content(source_id, scope=local)
    digest = content.get('sha256')
    if content.get('supported') is not True or not isinstance(digest, str) or not re.fullmatch('[a-f0-9]{64}', digest):
        raise HTTPException(409, '原件暂时无法核验，请重新选择当前资料')
    return {'identity': identity, 'sha256': digest}


def _create_private(scope, body, meta):
    value = {'kind': KIND, 'id': meta['id'], 'tenant_id': scope.identity.tenant_id,
        'user_id': scope.identity.user_id, 'workbench_id': scope.workbench_id,
        'grant_digest': shared.grant_digest(scope), 'fence': meta['fence'],
        'worker_id': meta['worker_id'], 'expires_unix': meta['expires_unix'],
        'request': {**body.model_dump(mode='json', exclude={'image'}), 'has_image': bool(body.image)},
        'result': None, 'source_identity': None}
    with artifact_session(scope) as session:
        session.add(SourceMapping(id=job_key(scope, meta['id']), config=value, version=1,
            confirmed_by=scope.identity.user_id, confirmed_at=datetime.now(timezone.utc)))
        session.commit()


def _complete(scope, meta, result, source_identity):
    with registry.fenced(meta['id'], meta['worker_id'], meta['fence']) as (connection, current):
        if current.get('purpose', 'judgments') != PURPOSE:
            raise HTTPException(409, '任务类型已改变，请重新生成')
        current.update(state='succeeded', phase='succeeded', updated_at=iso(), finished_at=iso())
        registry.write(connection, current)
        retired = [item for item in registry.compact(connection)
            if registry.scope_key(item) == registry.scope_key(current)]
        with artifact_session(scope) as session:
            session.execute(update(SourceMapping).where(SourceMapping.id == job_key(scope, meta['id']))
                .values(version=SourceMapping.version), execution_options={'synchronize_session': False})
            row = owned_record(session, scope, meta['id'])
            if row.version != 1 or row.config.get('fence') != meta['fence']:
                raise HTTPException(409, '产业图生成已中断，请重新发起')
            value = {**row.config, 'result': deepcopy(result), 'source_identity': source_identity}
            if len(json.dumps(value, ensure_ascii=False, allow_nan=False).encode()) > MAX_PRIVATE_BYTES:
                raise HTTPException(413, '生成配置超过预览容量，请缩小范围')
            try:
                changed = session.execute(update(SourceMapping).where(SourceMapping.id == row.id,
                    SourceMapping.version == 1).values(config=value, version=2,
                        confirmed_at=datetime.now(timezone.utc)), execution_options={'synchronize_session': False})
                if changed.rowcount != 1:
                    raise HTTPException(409, '产业图预览已更新，请重新打开')
                for old in retired:
                    shared._discard_private_record(session, old, KIND)
                session.commit()
            except (IntegrityError, OperationalError):
                raise HTTPException(409, '产业图预览正在更新，请稍后重试') from None
        for old in retired:
            old['payload_pruned'] = True
            registry.write(connection, old)
    if result.get('analysis_snapshot'):
        from tin.web.diagram_analysis_cache import mark_delivered
        try:
            mark_delivered(scope, result['analysis_snapshot'])
        except HTTPException:
            # The private preview has already committed. Deferred housekeeping
            # must not turn a successfully delivered result into a failed job.
            logging.getLogger(__name__).warning('Diagram structure delivery cleanup deferred')


def cleanup_expired():
    shared.cleanup_discarded(KIND, registry, PURPOSE)


def failure_message(error):
    if isinstance(error, TimeoutError):
        return '产业图生成超时，请重新发起'
    if isinstance(error, HTTPException):
        safe = generation.NATIVE_GENERATION_MESSAGES | generation.SELECTION_FAILURE_MESSAGES | TRUSTED_NATIVE_MESSAGES | {
            '模型没有给出有效指标筛选结果', '模型没有返回完整有效的配置，请调整资料后重试',
            '产业图配置无法解析，请核对分组、节点和连线',
            '模型服务暂时不可用，现有产业图保持原样',
            '所选模型不支持图片，请选择视觉对话模型或提供文字资料',
        }
        if isinstance(error.detail, str) and error.detail in safe:
            return error.detail
        return {400: '生成资料不完整，请核对图片或说明', 401: '登录已过期，请重新登录',
            403: '当前权限不能继续生成', 404: '生成所需来源或模型已不存在',
            409: '来源或授权已改变，请重新生成', 413: '资料或目录超过生成容量，请缩小范围',
            422: '模型配置未通过校验，请核对资料后重试', 429: '模型请求受限，请稍后重试'}.get(
                error.status_code, '产业图生成服务暂不可用，请稍后重试')
    return '产业图生成未完成，请重新发起'


class Runtime(shared.Runtime):
    async def sweep(self):
        while True:
            try:
                await run_in_threadpool(cleanup_expired)
            except (sqlite3.Error, OSError, HTTPException):
                logging.getLogger(__name__).warning('Diagram task cleanup deferred')
            await asyncio.sleep(60)

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
        identity_marker = current_identity.set(scope.identity)
        scope_marker = current_workbench.set(scope)
        heartbeat = asyncio.create_task(self.heartbeat(meta, asyncio.current_task()))
        try:
            async def execute():
                deadline = time.monotonic() + shared.QUEUE_SECONDS
                while not await run_in_threadpool(registry.claim, meta['id'], self.worker_id, meta['fence']):
                    if time.monotonic() >= deadline:
                        raise HTTPException(503, '等待生成超时，请重新发起')
                    await asyncio.sleep(.25)
                await fresh_scope(scope)
                source_identity = await original_fingerprint(scope, body.source_id)
                async def progress(phase):
                    await fresh_scope(scope)
                    await run_in_threadpool(registry.touch, meta['id'], self.worker_id, meta['fence'], phase)
                callback = getattr(generation, '_generation_progress', None)
                marker = callback.set(progress) if callback is not None else None
                structure_ticket = None
                try:
                    result = await generation.generate(body)
                    structure_ticket = result.pop('_analysis_ticket', None)
                    await fresh_scope(scope)
                    if source_identity is not None and await original_fingerprint(scope, body.source_id) != source_identity:
                        raise HTTPException(409, '生成期间原件已改变')
                    await run_in_threadpool(_complete, scope, meta, result, source_identity)
                except BaseException:
                    if structure_ticket is not None:
                        from tin.web.diagram_analysis_cache import abandon
                        await run_in_threadpool(abandon, scope, structure_ticket)
                    raise
                finally:
                    if marker is not None:
                        callback.reset(marker)
            await asyncio.wait_for(execute(), timeout=GENERATION_SECONDS)
        except asyncio.CancelledError:
            await run_in_threadpool(registry.finish, meta['id'], self.worker_id, meta['fence'],
                'interrupted', '产业图生成已中断，请重新发起')
        except Exception as error:
            await run_in_threadpool(registry.finish, meta['id'], self.worker_id, meta['fence'],
                'failed', failure_message(error))
        finally:
            heartbeat.cancel()
            await asyncio.gather(heartbeat, return_exceptions=True)
            current_workbench.reset(scope_marker)
            current_identity.reset(identity_marker)


runtime = Runtime()


async def start_job(scope, body, idempotency_key):
    if not isinstance(idempotency_key, str) or not re.fullmatch(r'[\x21-\x7e]{16,128}', idempotency_key):
        raise HTTPException(422, '请提供有效的生成请求标识')
    if not body.instructions.strip() and not body.image and not body.source_id:
        raise HTTPException(400, '请添加产业图图片、知识库原件或结构说明')
    await fresh_scope(scope)
    if runtime.closing:
        raise HTTPException(503, '服务正在重启，请稍后发起生成')
    async def admit():
        meta, created = None, False
        try:
            await run_in_threadpool(cleanup_expired)
            meta, created = await run_in_threadpool(registry.create, scope,
                shared.digest(body.model_dump(mode='json')), shared.digest(idempotency_key), runtime.worker_id, PURPOSE)
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
                    'interrupted', '产业图生成已中断，请重新发起')
            raise
        except Exception:
            if created:
                await run_in_threadpool(registry.finish, meta['id'], runtime.worker_id, meta['fence'],
                    'failed', '生成任务未能创建，请重新发起')
            raise
    admission = asyncio.create_task(admit(), name='diagram-generation-admission')
    runtime.admissions.add(admission)
    def admitted(done):
        runtime.admissions.discard(done)
        if not done.cancelled(): done.exception()
    admission.add_done_callback(admitted)
    return await asyncio.shield(admission)


async def job_status(scope, job_id):
    await fresh_scope(scope)
    meta = await run_in_threadpool(registry.get, scope.identity, job_id, scope.workbench_id)
    if meta.get('purpose', 'judgments') != PURPOSE:
        raise HTTPException(404, '产业图任务不存在或无权访问')
    if meta['state'] in {'superseded', 'expired'}:
        return metadata(meta)
    with artifact_session(scope) as session:
        stored = session.get(SourceMapping, job_key(scope, job_id))
        row = owned_record(session, scope, job_id) if stored is not None or meta['state'] == 'succeeded' else None
        superseded = row is not None and row.config.get('state') == 'superseded'
        completed = row is not None and row.version == 2 and isinstance(row.config.get('result'), dict)
        if meta['state'] == 'succeeded' and not completed and not superseded:
            raise HTTPException(409, '产业图预览未完整保存，请重新生成')
        if completed and (row.config.get('fence') != meta['fence'] or row.config.get('worker_id') != meta['worker_id']):
            raise HTTPException(409, '产业图任务状态不一致，请重新生成')
    if superseded:
        meta = await run_in_threadpool(registry.reconcile, scope.identity, job_id, scope.workbench_id, 'superseded')
    elif completed and meta['state'] != 'succeeded':
        meta = await run_in_threadpool(registry.reconcile, scope.identity, job_id, scope.workbench_id, 'succeeded')
    return metadata(meta)


async def job_result(scope, job_id):
    verified = await fresh_scope(scope)
    identity_marker = current_identity.set(verified.identity)
    scope_marker = current_workbench.set(verified)
    try:
        status = await job_status(verified, job_id)
        if status['state'] != 'succeeded':
            raise HTTPException(410 if status['state'] in {'expired', 'superseded'} else 409,
                status.get('message') or '产业图尚未完成或预览已失效')
        with artifact_session(verified) as session:
            row = owned_record(session, verified, job_id)
            value = deepcopy(row.config)
        if value.get('state') == 'superseded':
            raise HTTPException(410, '已有更新的产业图结果，请打开最新结果')
        if value.get('expires_unix', 0) <= time.time():
            raise HTTPException(410, '产业图预览已过期，请重新生成')
        result = value['result']
        await generation.verify_generation_scope(verified, result['version'])
        source_id = value['request'].get('source_id')
        if value.get('source_identity') is not None and await original_fingerprint(verified, source_id) != value['source_identity']:
            raise HTTPException(409, '原件已改变，请重新生成产业图')
        await fresh_scope(verified)
        if result.get('analysis_snapshot'):
            from tin.web.diagram_analysis_cache import mark_delivered
            try:
                await run_in_threadpool(mark_delivered, verified, result['analysis_snapshot'])
            except HTTPException:
                logging.getLogger(__name__).warning('Diagram structure delivery cleanup deferred')
        return {**result, 'request': value['request'], 'job': status}
    finally:
        current_workbench.reset(scope_marker)
        current_identity.reset(identity_marker)


async def read_body(request):
    content = bytearray()
    async for piece in request.stream():
        if len(content) + len(piece) > MAX_REQUEST_BYTES:
            raise HTTPException(413, '生成请求超过图片与说明容量')
        content.extend(piece)
    try:
        return generation.GenerateRequest.model_validate_json(bytes(content))
    except (ValidationError, ValueError, UnicodeError, RecursionError):
        raise HTTPException(422, '产业图生成请求格式无效') from None


async def latest_job(scope):
    await fresh_scope(scope)
    meta = await run_in_threadpool(registry.latest, scope.identity, scope.workbench_id, PURPOSE)
    return {'job': metadata(meta) if meta is not None else None}


@router.post('', status_code=202)
async def create(request: Request):
    return await start_job(generation.scope_for_generation(), await read_body(request), request.headers.get('idempotency-key'))


@router.get('/latest')
async def latest():
    return await latest_job(generation.scope_for_generation())


@router.get('/{job_id}')
async def status(job_id: str):
    return await job_status(generation.scope_for_generation(), job_id)


@router.get('/{job_id}/result')
async def result(job_id: str):
    return await job_result(generation.scope_for_generation(), job_id)
