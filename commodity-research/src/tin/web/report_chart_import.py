"""Private, expiring report-chart drafts; only explicit confirmation creates charts."""
import asyncio
import hashlib
import json
import time
from typing import Literal

from fastapi import APIRouter, HTTPException, Query, Request
from fastapi.encoders import jsonable_encoder
from pydantic import Field
from sqlalchemy import delete, update
from starlette.concurrency import run_in_threadpool

from tin.config import settings
from tin.integration.identity import research_url
from tin.integration.page_facts import catalog_snapshot
from tin.integration.workbench_scope import artifact_session, resolve_workbench
from tin.models import SourceMapping
from tin.schemas.chart import ChartModel
from tin.web import analysis_jobs, report_chart_suggestions as suggestions, strategy_citations
from tin.web.chart_data import cutoff
from tin.web.charts import create_row, definition_data, require_scope, serialize
from tin.web.report_chart_definitions import build_definition, duplicate_ids, mapping_warnings

router = APIRouter(prefix='/api/sn/charts/import/report')
PURPOSE = 'report_chart_import'
BATCH_SECONDS = 360
TOTAL_SECONDS = 1800
VERIFY_SECONDS = 120


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


def reason(error):
    return str(error.detail) if isinstance(error, HTTPException) else '本项处理失败，请重试'


def prune_legacy_previews(scope):
    # Earlier releases kept private chart previews under an independent key.
    # Retire those rows on the same per-workbench submission cadence as before.
    config = SourceMapping.config
    with artifact_session(scope) as session:
        session.execute(delete(SourceMapping).where(
            config['kind'].as_string() == 'report-chart-import.v1',
            config['workbench'].as_string() == scope.workbench_id,
            config['tenant'].as_integer() == scope.identity.tenant_id,
            config['expires'].as_float() <= time.time(),
        ))
        session.commit()


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
        mappings = [s.get('selected_id') for s in chart['series']]
        chart.setdefault('warnings', [])
        chart['mapping_warnings'] = mapping_warnings(mappings)
        chart['missing_indicators'] = any(value is None for value in mappings)
        chart['definition_failed'] = False
        if chart['missing_indicators']:
            chart['errors'].append('指标库中未找到，请手动选择或跳过')
            continue
        try:
            value = build_definition(scope, chart, mappings,
                                     chart['chart_type'], group, f"report:{identifier}:{chart['id']}")
            chart['definition'] = value.model_dump(mode='json')
            chart['possible_duplicate'] = bool(duplicate_ids(scope, value))
        except HTTPException as exc:
            if exc.status_code in {401, 403, 409}:
                raise
            chart['definition_failed'] = True
            chart['errors'].append(reason(exc))
        except ValueError:
            chart['definition_failed'] = True
            chart['errors'].append('图表定义不符合图表库格式或数量限制')
    return charts


async def recognize_report(scope, body, identifier, *, report=None, progress=None):
    charts, failed, completed = [], [], 0
    try:
        async with asyncio.timeout(TOTAL_SECONDS):
            if progress:
                await progress('reading_source')
            await fresh_scope(scope)
            if report is None:
                await run_in_threadpool(prune_legacy_previews, scope)
                report = await suggestions.read_report(scope, body.source)
            if progress:
                await progress('loading_catalog')
            catalog = (await run_in_threadpool(catalog_snapshot, scope))['catalog']
            group = report['filename'][:80]
            batches = report['batches']
            # Only bounded identities survive the worker; raw report text is
            # neither persisted as research nor copied into chart descriptions.
            original = {key: value for key, value in report.items() if key not in {'fragments', 'batches'}}
            if progress:
                await progress('analyzing')
            pending = iter(enumerate(batches))
            outcomes = {}
            publish = asyncio.Lock()

            async def worker():
                nonlocal completed
                for index, batch in pending:
                    await fresh_scope(scope)
                    batch_charts, batch_failed = [], []
                    try:
                        # Retain the existing audited model path for every stage.
                        result = await asyncio.wait_for(suggestions.recognize_batch(scope, body.model_id, batch,
                            catalog=catalog), timeout=BATCH_SECONDS)
                        batch_charts = result['charts']
                        for item in batch_charts:
                            item['id'] = f'b{index + 1}-{item["id"]}'
                        batch_failed = result.get('failed', [])
                    except TimeoutError:
                        batch_failed = [{'batch': index + 1, 'reason': f'本批处理超过{BATCH_SECONDS}秒'}]
                    except HTTPException as exc:
                        if exc.status_code in {401, 403, 409}:
                            raise
                        batch_failed = [{'batch': index + 1, 'reason': reason(exc)}]
                    except Exception as exc:
                        batch_failed = [{'batch': index + 1, 'reason': reason(exc)}]
                    # Verify each completed batch and preserve source order for review.
                    async with publish:
                        try:
                            batch_charts = await run_in_threadpool(annotate, scope, batch_charts, group, identifier)
                        except HTTPException as exc:
                            if exc.status_code in {401, 403, 409}:
                                raise
                            batch_charts = []
                            batch_failed.append({'batch': index + 1, 'reason': reason(exc)})
                        except Exception as exc:
                            batch_charts = []
                            batch_failed.append({'batch': index + 1, 'reason': reason(exc)})
                        await suggestions.verify_report(scope, body.source, original)
                        outcomes[index] = (batch_charts, batch_failed)
                        charts[:] = [chart for key in sorted(outcomes) for chart in outcomes[key][0]]
                        failed[:] = [failure for key in sorted(outcomes) for failure in outcomes[key][1]]
                        completed = len(outcomes)
            workers = [asyncio.create_task(worker()) for _ in
                       range(min(settings.report_chart_import_concurrency, len(batches)))]
            try:
                await asyncio.gather(*workers)
            finally:
                # Fatal scope/revision changes, total timeout and cancellation all
                # drain workers, including each in-flight model call's audit.
                for task in workers:
                    if not task.done():
                        task.cancel()
                await asyncio.gather(*workers, return_exceptions=True)
            if progress:
                await progress('validating')
            await fresh_scope(scope)
            await suggestions.verify_report(scope, body.source, original)
            message = '识别完成，请核对后勾选导入'
    except TimeoutError:
        if report is None or not charts:
            raise HTTPException(503, '任务超时或中断，请重新识别') from None
        # Keep completed batches only when their original evidence still verifies.
        async with asyncio.timeout(VERIFY_SECONDS):
            if progress:
                await progress('validating')
            await fresh_scope(scope)
            await suggestions.verify_report(scope, body.source, original)
        failed.append({'reason': '整体识别超时，保留已完成并复核的图；其余页面未处理'})
        message = '部分完成，请核对已完成的图表；未完成页面可重新识别'
    return {'id': identifier, 'group': group, 'charts': charts, 'failures': failed,
            '_original': original, 'progress': {'completed': completed, 'total': len(batches), 'message': message}}


async def process(scope, payload, progress, identifier):
    return await recognize_report(scope, RecognizeInput.model_validate(payload), identifier, progress=progress)


async def verify_result(scope, body, result):
    citation = strategy_citations.KnowledgeFileCitation.model_validate(body['source'])
    await suggestions.verify_report(scope, citation, result['_original'])


analysis_jobs.result_checks[PURPOSE] = verify_result


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
async def recognize(body: RecognizeInput, request: Request):
    scope = writable_scope()
    payload = body.model_dump(mode='json', exclude={'source': {'snapshot'}})
    return await analysis_jobs.start_job(scope, PURPOSE, payload, request.headers.get('idempotency-key'), process)


async def verified_state(scope, identifier):
    from tin.web import workbench_setup_jobs

    meta = await run_in_threadpool(analysis_jobs.registry.get, scope.identity, identifier, scope.workbench_id)
    purpose = meta.get('purpose')
    if purpose == workbench_setup_jobs.PURPOSE:
        _, config = await analysis_jobs.verified_result(scope, identifier, expected_purpose=purpose)
        return workbench_setup_jobs.part_data(config, 'charts'), purpose
    return await analysis_jobs.job_result(scope, identifier, expected_purpose=PURPOSE), PURPOSE


@router.post('/preview')
async def preview(body: PreviewInput):
    scope = writable_scope()
    state, _ = await verified_state(scope, body.job_id)
    def render():
        _, value = draft(scope, state, body, body.group)
        payload = definition_data(scope, value, cutoff())
        return {'definition': value.model_dump(mode='json'), 'payload': payload,
                'possible_duplicate': bool(duplicate_ids(scope, value)), 'errors': [],
                'warnings': mapping_warnings(body.mappings)}
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


def apply_selections(scope, body, definitions, failed, purpose):
    created = []
    with artifact_session(scope) as session:
        # Serialize confirmation/retry in the existing artifact store. The
        # receipt and chart rows commit together, including per-chart failures.
        session.execute(update(SourceMapping).where(SourceMapping.id == analysis_jobs.job_key(scope, body.job_id))
            .values(version=SourceMapping.version + 1), execution_options={'synchronize_session': False})
        row = analysis_jobs.owned_record(session, scope, body.job_id, purpose)
        source = row.config['request']['source']
        applied = dict(row.config.get('applied', {}))
        for selection, chart, value in definitions:
            if selection.chart_id in applied:
                created.append(applied[selection.chart_id])
                continue
            try:
                with session.begin_nested():
                    saved = create_row(session, scope, value, snapshots=[{
                        'kind': 'report_chart_structure', 'knowledge_id': source['knowledge_id'],
                        'file_version': source['file_version'], 'page': chart.get('page'),
                        'legends': [name for s in chart['series'] for name in s.get('legends', [s['name']])]}])
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
    state, purpose = await verified_state(scope, body.job_id)
    definitions, failed = await run_in_threadpool(prepare_selections, scope, body, state)
    # Definition preparation reads the current indicator catalog. Recheck the
    # original report afterwards, immediately before opening the write lock.
    await verified_state(scope, body.job_id)
    return await run_in_threadpool(apply_selections, scope, body, definitions, failed, purpose)
