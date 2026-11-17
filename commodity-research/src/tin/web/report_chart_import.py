"""Private, expiring report-chart drafts; only explicit confirmation creates charts."""
import asyncio
from copy import deepcopy
from datetime import datetime, timezone
import hashlib
import json
import time
from typing import Literal
from uuid import uuid4

from fastapi import APIRouter, HTTPException, Query
from fastapi.encoders import jsonable_encoder
from pydantic import Field
from sqlalchemy import select, update
from starlette.concurrency import run_in_threadpool

from tin.integration.identity import research_url
from tin.integration.page_facts import catalog_snapshot
from tin.integration.workbench_scope import artifact_session, current_workbench, resolve_workbench
from tin.models import SourceMapping
from tin.schemas.chart import ChartModel
from tin.web import report_chart_suggestions as suggestions, strategy_citations
from tin.web.chart_data import cutoff
from tin.web.charts import create_row, definition_data, require_scope, serialize
from tin.web.report_chart_definitions import build_definition, duplicate_ids

router = APIRouter(prefix='/api/sn/charts/import/report')
KIND = 'report-chart-import.v1'
TTL = 3600
BATCH_SECONDS = 360
TOTAL_SECONDS = 1800
VERIFY_SECONDS = 120
MAX_JOBS = 2
_tasks = set()


class RecognizeInput(ChartModel):
    source: strategy_citations.KnowledgeFileCitation
    model_id: str = Field(min_length=1, max_length=128)


class Selection(ChartModel):
    chart_id: str = Field(min_length=1, max_length=128)
    mappings: list[str | None] = Field(min_length=1, max_length=16)
    chart_type: Literal['trend', 'seasonal', 'comparison']


class PreviewInput(Selection):
    job_id: str = Field(min_length=1, max_length=64)
    group: str = Field(default='', max_length=80)


class ApplyInput(ChartModel):
    job_id: str = Field(min_length=1, max_length=64)
    group: str = Field(max_length=80)
    charts: list[Selection] = Field(min_length=1, max_length=300)


def writable_scope():
    scope = require_scope(write=True)
    if (scope.metadata.get('can_read_source') is False or not scope.identity.can_write
            or 'indicators' not in scope.view_modules or not scope.authorization.startswith('Bearer ')):
        raise HTTPException(403, '当前权限不能读取周报或导入常用图')
    return scope


def grant(scope):
    value = [scope.workbench_id, scope.identity.tenant_id, scope.identity.user_id,
             scope.sources_revision, scope.sources, scope.view_modules, scope.metadata.get('can_edit'),
             scope.metadata.get('can_read_source')]
    return hashlib.sha256(json.dumps(value, sort_keys=True, ensure_ascii=False).encode()).hexdigest()


async def fresh_scope(scope):
    verified = await resolve_workbench(scope.identity, scope.workbench_id, scope.authorization)
    if grant(verified) != grant(scope) or verified.restricted or not verified.can_edit:
        raise HTTPException(409, '研究台来源或权限已改变，请重新识别')
    return verified


def job_key(identifier):
    return 'report-chart-' + identifier


def owned(session, scope, identifier):
    row = session.get(SourceMapping, job_key(identifier))
    config = row.config if row is not None else {}
    if (config.get('kind') != KIND or config.get('owner') != scope.identity.user_id
            or config.get('tenant') != scope.identity.tenant_id or config.get('workbench') != scope.workbench_id):
        raise HTTPException(404, '导入草稿不存在或无权读取')
    if config.get('expires', 0) <= time.time():
        raise HTTPException(409, '导入草稿已过期，请重新识别')
    if config.get('grant') != grant(scope):
        raise HTTPException(409, '研究台来源或权限已改变，请重新识别')
    return row


def save_state(scope, identifier, **changes):
    with artifact_session(scope) as session:
        row = owned(session, scope, identifier)
        row.config = {**row.config, **changes, 'updated': time.time()}
        row.version += 1
        session.commit()


def load_state(scope, identifier):
    with artifact_session(scope) as session:
        return deepcopy(owned(session, scope, identifier).config)


def new_job(scope, body):
    identifier, now = uuid4().hex, time.time()
    with artifact_session(scope) as session:
        if session.get_bind().dialect.name == 'sqlite':
            session.connection().exec_driver_sql('BEGIN IMMEDIATE')
        # Private drafts live in the existing isolated WB store, never in the
        # shared chart library. Prune expired evidence on the next submission.
        rows = session.scalars(select(SourceMapping)).all()
        active = 0
        for row in rows:
            value = row.config or {}
            if (value.get('kind') != KIND or value.get('workbench') != scope.workbench_id
                    or value.get('tenant') != scope.identity.tenant_id):
                continue
            if value.get('expires', 0) <= now:
                session.delete(row)
            elif value.get('status') in {'queued', 'running'} and value.get('started', 0) + TOTAL_SECONDS > now:
                active += 1
        if active >= MAX_JOBS:
            raise HTTPException(429, '当前研究台已有周报识别任务，请等待完成')
        config = {'kind': KIND, 'id': identifier, 'owner': scope.identity.user_id,
            'tenant': scope.identity.tenant_id, 'workbench': scope.workbench_id, 'grant': grant(scope),
            'started': now, 'updated': now, 'expires': now + TTL, 'status': 'queued',
            'source': body.source.model_dump(mode='json', exclude={'snapshot'}),
            'progress': {'completed': 0, 'total': 0, 'message': '正在读取已解析片段'},
            'charts': [], 'failures': [], 'applied': {}}
        session.add(SourceMapping(id=job_key(identifier), config=config, version=1,
            confirmed_by=scope.identity.user_id, confirmed_at=datetime.now(timezone.utc)))
        session.commit()
    return identifier


def reason(error):
    return str(error.detail) if isinstance(error, HTTPException) else '本项处理失败，请重试'


def draft(scope, state, selection, group):
    chart = next((item for item in state['charts'] if item['id'] == selection.chart_id), None)
    if chart is None:
        raise HTTPException(422, '所选图不在本次识别结果中')
    value = build_definition(scope, chart, selection.mappings, selection.chart_type, group,
                             f"report:{state['id']}:{chart['id']}")
    return chart, value


def annotate(scope, charts, group, identifier):
    for chart in charts:
        chart.setdefault('errors', [])
        try:
            value = build_definition(scope, chart, [s.get('selected_id') for s in chart['series']],
                                     chart['chart_type'], group, f"report:{identifier}:{chart['id']}")
            chart['definition'] = value.model_dump(mode='json')
            chart['possible_duplicate'] = bool(duplicate_ids(scope, value))
        except HTTPException as exc:
            if exc.status_code in {401, 403, 409}:
                raise
            chart['errors'].append(reason(exc))
        except ValueError:
            chart['errors'].append('图表定义不符合图表库格式或数量限制')
    return charts


async def process(scope, identifier, body):
    report, charts, failed, completed = None, [], [], 0
    try:
        async with asyncio.timeout(TOTAL_SECONDS):
            await fresh_scope(scope)
            report = await suggestions.read_report(scope, body.source)
            catalog = (await run_in_threadpool(catalog_snapshot, scope))['catalog']
            group = report['filename'][:80]
            batches = report['batches']
            # Only bounded identities survive the worker; raw report text is
            # neither persisted as research nor copied into chart descriptions.
            original = {key: value for key, value in report.items() if key not in {'fragments', 'batches'}}
            await run_in_threadpool(save_state, scope, identifier, status='running', group=group,
                original=original, progress={'completed': 0, 'total': len(batches), 'message': '正在识别证据页上的图'})
            for index, batch in enumerate(batches):
                await fresh_scope(scope)
                try:
                    result = await asyncio.wait_for(suggestions.recognize_batch(scope, body.model_id, batch,
                        catalog=catalog), timeout=BATCH_SECONDS)
                    batch_charts = result['charts']
                    for item in batch_charts:
                        item['id'] = f'b{index + 1}-{item["id"]}'
                    charts.extend(await run_in_threadpool(annotate, scope, batch_charts, group, identifier))
                    failed.extend(result.get('failed', []))
                except TimeoutError:
                    failed.append({'batch': index + 1, 'reason': f'本批处理超过{BATCH_SECONDS}秒'})
                except HTTPException as exc:
                    if exc.status_code in {401, 403, 409}:
                        raise
                    failed.append({'batch': index + 1, 'reason': reason(exc)})
                except Exception as exc:
                    failed.append({'batch': index + 1, 'reason': reason(exc)})
                await suggestions.verify_report(scope, body.source, original)
                completed = index + 1
                await run_in_threadpool(save_state, scope, identifier, progress={
                    'completed': index + 1, 'total': len(batches), 'message': f'已处理{index + 1}/{len(batches)}批'},
                    failures=failed)
            await fresh_scope(scope)
            await suggestions.verify_report(scope, body.source, original)
            await run_in_threadpool(save_state, scope, identifier, status='complete', charts=charts,
                failures=failed, progress={'completed': len(batches), 'total': len(batches), 'message': '识别完成，请核对后勾选导入'})
    except (Exception, asyncio.CancelledError) as exc:
        if isinstance(exc, TimeoutError) and report is not None and charts:
            # The overall budget must not discard successful earlier pages.
            # Publish only if their complete original evidence still verifies.
            try:
                async def recheck():
                    await fresh_scope(scope)
                    await suggestions.verify_report(scope, body.source, original)
                await asyncio.wait_for(recheck(), timeout=VERIFY_SECONDS)
                failed.append({'reason': '整体识别超时，保留已完成并复核的图；其余页面未处理'})
                await run_in_threadpool(save_state, scope, identifier, status='complete', charts=charts,
                    failures=failed, progress={'completed': completed, 'total': len(report['batches']),
                        'message': '部分完成，请核对已完成的图表；未完成页面可重新识别'})
                return
            except Exception as verification_error:
                exc = verification_error
        try:
            await run_in_threadpool(save_state, scope, identifier, status='failed', charts=[],
                error_status=exc.status_code if isinstance(exc, HTTPException) else 503,
                error='任务超时或中断，请重新识别' if isinstance(exc, (TimeoutError, asyncio.CancelledError)) else reason(exc))
        except HTTPException:
            # Expired drafts are no longer writable or readable.
            pass


@router.get('/files')
async def files(search: str = Query('', max_length=200), offset: int = Query(0, ge=0, le=10000),
                limit: int = Query(20, ge=1, le=100)):
    scope = writable_scope()
    page = await strategy_citations._file_options(scope, search, offset, limit)
    # File type is explicit metadata; this filter makes no business judgement.
    items = []
    for item in page['items']:
        citation = item['citation']
        _, source = await strategy_citations._native_file(scope, citation['source_binding_id'], citation['knowledge_id'])
        if str(source.get('file_type', '')).lower().lstrip('.') in {'pptx', 'pdf'}:
            items.append(item)
    return {**page, 'items': items}


@router.post('/recognize', status_code=202)
async def recognize(body: RecognizeInput):
    scope = writable_scope()
    identifier = await run_in_threadpool(new_job, scope, body)
    task = asyncio.create_task(process(scope, identifier, body))
    _tasks.add(task)
    task.add_done_callback(_tasks.discard)
    return {'job_id': identifier, 'status': 'queued'}


@router.get('/jobs/{identifier}')
async def job(identifier: str):
    scope = writable_scope()
    state = await run_in_threadpool(load_state, scope, identifier)
    if (state['status'] in {'queued', 'running'}
            and state['started'] + TOTAL_SECONDS + VERIFY_SECONDS < time.time()):
        await run_in_threadpool(save_state, scope, identifier, status='failed', error='识别任务已中断，请重试', charts=[])
        state = await run_in_threadpool(load_state, scope, identifier)
    if state['status'] == 'failed':
        raise HTTPException(state.get('error_status', 503), state.get('error', '识别失败'))
    if state['status'] == 'complete':
        citation = strategy_citations.KnowledgeFileCitation.model_validate(state['source'])
        await suggestions.verify_report(scope, citation, state['original'])
    return {key: state[key] for key in ('status', 'progress', 'charts', 'failures', 'group') if key in state}


async def verified_state(scope, identifier):
    state = await run_in_threadpool(load_state, scope, identifier)
    if state['status'] != 'complete':
        raise HTTPException(409, '请等待识别完成后核对导入')
    citation = strategy_citations.KnowledgeFileCitation.model_validate(state['source'])
    await suggestions.verify_report(scope, citation, state['original'])
    return state


@router.post('/preview')
async def preview(body: PreviewInput):
    scope = writable_scope()
    state = await verified_state(scope, body.job_id)
    def render():
        _, value = draft(scope, state, body, body.group)
        payload = definition_data(scope, value, cutoff())
        return {'definition': value.model_dump(mode='json'), 'payload': payload,
                'possible_duplicate': bool(duplicate_ids(scope, value)), 'errors': []}
    result = await run_in_threadpool(render)
    await verified_state(scope, body.job_id)
    return result


def prepare_selections(scope, body, state):
    failed = []
    if len({s.chart_id for s in body.charts}) != len(body.charts):
        raise HTTPException(422, '请勿重复勾选同一候选图')
    definitions = []
    for selection in body.charts:
        try:
            chart, value = draft(scope, state, selection, body.group)
            definitions.append((selection, chart, value))
        except (HTTPException, ValueError) as exc:
            if isinstance(exc, HTTPException) and exc.status_code in {401, 403, 409}:
                raise
            failed.append({'chart_id': selection.chart_id, 'reason': reason(exc)})
    return definitions, failed


def apply_selections(scope, body, definitions, failed):
    created = []
    with artifact_session(scope) as session:
        # Serialize confirmation/retry in the existing artifact store. The
        # receipt and chart rows commit together, including per-chart failures.
        session.execute(update(SourceMapping).where(SourceMapping.id == job_key(body.job_id))
            .values(version=SourceMapping.version + 1), execution_options={'synchronize_session': False})
        row = owned(session, scope, body.job_id)
        applied = dict(row.config.get('applied', {}))
        for selection, chart, value in definitions:
            if selection.chart_id in applied:
                created.append(applied[selection.chart_id])
                continue
            try:
                with session.begin_nested():
                    saved = create_row(session, scope, value, snapshots=[{
                        'kind': 'report_chart_structure', 'knowledge_id': row.config['source']['knowledge_id'],
                        'file_version': row.config['source']['file_version'], 'page': chart.get('page'),
                        'legends': [s['name'] for s in chart['series']]}])
                    result = jsonable_encoder(serialize(saved, definition=value))
                    result['href'] = research_url('/sn/charts/' + saved.id)
                applied[selection.chart_id] = result
                created.append(result)
            except Exception:
                failed.append({'chart_id': selection.chart_id, 'reason': '写入图表库失败，请重试'})
        row.config = {**row.config, 'applied': applied}
        session.commit()
    return {'created': created, 'failed': failed}


@router.post('/apply')
async def apply(body: ApplyInput):
    scope = writable_scope()
    state = await verified_state(scope, body.job_id)
    definitions, failed = await run_in_threadpool(prepare_selections, scope, body, state)
    # Definition preparation reads the current indicator catalog. Recheck the
    # original report afterwards, immediately before opening the write lock.
    await verified_state(scope, body.job_id)
    return await run_in_threadpool(apply_selections, scope, body, definitions, failed)
