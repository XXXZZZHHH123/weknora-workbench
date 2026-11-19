"""Submit report setup through shared jobs and open its native review drafts."""
from copy import deepcopy
from datetime import datetime, timezone
import hmac
import re
import secrets

from fastapi import APIRouter, HTTPException, Request
from starlette.concurrency import run_in_threadpool

from tin.integration.workbench_scope import artifact_session
from tin.models import AuditLog
from tin.ownership import verified_actor
from tin.web import analysis_jobs, judgment_jobs as shared, monitors, report_chart_import
from tin.web import report_chart_suggestions, workbench_setup

PURPOSE = workbench_setup.PURPOSE
router = APIRouter(prefix='/api/sn/workbench-setup/jobs')


def owned_record(session, scope, job_id):
    return analysis_jobs.owned_record(session, scope, job_id, PURPOSE)


def record_audit(session, scope, job_id, body, result=None):
    if result is None:
        action = '提交周报建立工作台'
        detail = {'knowledge_id': body['source']['knowledge_id'], 'parts': body['parts'], 'model_id': body['model_id']}
    else:
        action = '生成周报建立工作台候选'
        detail = {'items': [{key: item[key] for key in ('part', 'state', 'counts') if key in item}
                            for item in result['items']]}
    session.add(AuditLog(at=datetime.now(timezone.utc), actor=verified_actor(session, ''),
        action=action, target_type=PURPOSE, target_id=job_id, detail={'workbench_id': scope.workbench_id, **detail}))


async def generate(scope, payload, progress, job_id):
    body = workbench_setup.SetupInput.model_validate(payload)

    async def checked_progress(phase):
        await analysis_jobs.fresh_scope(scope, PURPOSE)
        await progress(phase)

    try:
        result, original = await workbench_setup.generate(scope, body, job_id, checked_progress)
    except Exception as error:
        status = error.status_code if isinstance(error, HTTPException) else 503
        raise HTTPException(status, workbench_setup.result_message(error)) from None
    result['_source_identity'] = original
    for item in result['items']:
        if item['part'] == 'judgment':
            candidate = item.pop('data', None)
            item['review_available'] = candidate is not None
            if candidate is not None:
                result['_judgment'] = candidate
                result['_judgment_token'] = 'setup.' + job_id + '.' + secrets.token_urlsafe(24)
    return result


async def verify_result(scope, payload, result):
    body = workbench_setup.SetupInput.model_validate(payload)
    await report_chart_suggestions.verify_report(scope, body.source, result['_source_identity'])


analysis_jobs.record_audits[PURPOSE] = record_audit
analysis_jobs.result_checks[PURPOSE] = verify_result


async def start_job(scope, body, idempotency_key):
    payload = body.model_dump(mode='json', exclude={'source': {'snapshot'}})
    # Each setup step has its own deadline. The full sequence can take longer
    # than a single draft job, especially when recognizing a long report.
    return await analysis_jobs.start_job(scope, PURPOSE, payload, idempotency_key, generate, timeout=None)


def load_config(scope, job_id):
    with artifact_session(scope) as session:
        return deepcopy(owned_record(session, scope, job_id).config)


async def verified_config(scope, job_id):
    await analysis_jobs.verified_result(scope, job_id, expected_purpose=PURPOSE)
    return await run_in_threadpool(load_config, scope, job_id)


def part_data(config, part):
    for item in config['result']['items']:
        if item['part'] == part and item['state'] == 'succeeded':
            return item['data']
    raise HTTPException(404, '本次建立没有该部分的待核对结果')


async def result_view(scope, config):
    result = config['result']
    for item in result['items']:
        if item['part'] == 'charts' and item['state'] == 'succeeded':
            item['data'] = await run_in_threadpool(report_chart_import.review_state, scope, item['data'],
                                                   config.get('applied', {}))
    return result


analysis_jobs.result_views[PURPOSE] = result_view


async def job_result(scope, job_id):
    result = await analysis_jobs.job_result(scope, job_id, expected_purpose=PURPOSE)
    return {key: result[key] for key in ('report', 'items')}


def judgment_job_id(token):
    if not isinstance(token, str) or not re.fullmatch(r'setup\.[a-f0-9]{32}\.[A-Za-z0-9_-]{32}', token):
        raise HTTPException(409, '研判草稿已失效，请重新打开建立任务')
    return token.split('.')[1]


def judgment_record(session, scope, token):
    row = owned_record(session, scope, judgment_job_id(token))
    result = row.config.get('result') or {}
    if ('_judgment' not in result or not hmac.compare_digest(result.get('_judgment_token', ''), token)):
        raise HTTPException(409, '研判草稿已失效或已保存，请重新打开建立任务')
    return row


def load_judgment(scope, token):
    with artifact_session(scope) as session:
        row = judgment_record(session, scope, token)
        return {**deepcopy(row.config['result']['_judgment']), '_setup_job_id': row.config['id'], '_draft_id': token}


def replace_judgment(session, scope, token, preview, revision):
    row = judgment_record(session, scope, token)
    value = deepcopy(row.config)
    stored = value['result']['_judgment']
    if stored['review']['revision'] != revision or preview['review']['revision'] != revision + 1:
        raise HTTPException(409, '逐项确认已更新，请刷新后继续')
    stored.update({key: data for key, data in preview.items() if key not in {'_setup_job_id', '_draft_id'}})
    shared._update_record(session, row, value)


def close_judgment(session, scope, token, revision=None):
    row = judgment_record(session, scope, token)
    value = deepcopy(row.config)
    result = value['result']
    if revision is not None and result['_judgment']['review']['revision'] != revision:
        raise HTTPException(409, '逐项确认已更新，请刷新预览后保存')
    result.pop('_judgment')
    result.pop('_judgment_token')
    for item in result['items']:
        if item['part'] == 'judgment':
            item['review_available'] = False
    shared._update_record(session, row, value)


@router.post('', status_code=202)
async def create(request: Request, body: workbench_setup.SetupInput):
    return await start_job(report_chart_import.writable_scope(), body, request.headers.get('Idempotency-Key'))


@router.post('/{job_id}/judgment-preview')
async def judgment_preview(job_id: str):
    from tin.web.judgment_review import current_preview, preview_response
    scope = report_chart_import.writable_scope()
    if 'judgments' not in scope.view_modules:
        raise HTTPException(403, '当前权限不能核对研判草稿')
    config = await verified_config(scope, job_id)
    token = config['result'].get('_judgment_token')
    if token is None:
        raise HTTPException(409, '研判草稿已失效或已保存，请重新打开建立任务')
    preview, state = await current_preview(scope, token)
    return {**preview_response(scope, token, preview, state),
            **{key: preview.get(key) for key in ('variety', 'source_name', 'panel_title', 'model_id')}}


@router.post('/{job_id}/monitor-preview')
async def monitor_preview(job_id: str, candidate: int = 0):
    scope = monitors.require_scope(report_chart_import.writable_scope(), write=True)
    config = await verified_config(scope, job_id)
    result = part_data(config, 'monitors')
    candidates = result['candidates'] if 'failures' in result else [result]
    if not 0 <= candidate < len(candidates):
        raise HTTPException(404, '本次建立没有该监测候选')
    draft = await run_in_threadpool(monitors.save_private_draft, scope, candidates[candidate],
                                   setup_job_id=job_id, setup_candidate=candidate)
    return {**draft, 'setup_candidate': candidate, 'setup_titles': [item['title'] for item in candidates],
            'setup_failures': result.get('failures', [])}
