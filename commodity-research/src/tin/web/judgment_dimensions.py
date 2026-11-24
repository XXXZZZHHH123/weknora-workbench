"""Model-only dimension suggestions and explicit, content-preserving version edits."""
import asyncio
from copy import deepcopy
from datetime import datetime, timezone
import json
from typing import Literal

from fastapi import APIRouter, HTTPException, Request
from pydantic import BaseModel, ConfigDict, Field
from sqlalchemy.exc import IntegrityError, OperationalError
from starlette.concurrency import run_in_threadpool

from tin.integration.workbench_scope import artifact_session
from tin.judgments.service import JudgmentError, JudgmentVersionConflict, save_version, to_payload
from tin.models import AuditLog
from tin.web import analysis_jobs, judgment_generation as generation
from tin.web.judgment_jobs import digest
from tin.web.judgment_panels import panel_scope, register_panel, selected_judgment
from tin.web.research_dimensions import checked_dimension, dimension_prompt, read_dimensions, summaries

router = APIRouter(prefix='/api/sn/judgment')
SUGGESTION_VERSION = 'judgment-dimensions-v1'
Section = Literal['supporting', 'opposing', 'thresholds', 'falsifiers']


def dimension_rows(payload):
    """Stable positions in the specified immutable version; never classify prose."""
    for side, section in (('bull', 'supporting'), ('bear', 'opposing')):
        for index, value in enumerate(getattr(payload.contradiction, side).evidence):
            yield section, index, value
    for section in ('thresholds', 'falsifiers'):
        for index, value in enumerate(getattr(payload, section)):
            yield section, index, value


def dimension_items(payload):
    labels = {'supporting': '支持依据', 'opposing': '反对依据', 'thresholds': '区间与阈值', 'falsifiers': '证伪条件'}
    return [{'section': section, 'index': index, 'dimension': value.dimension,
             'title': labels[section] + '：' + (getattr(value, 'desc', None) or getattr(value, 'name', ''))}
            for section, index, value in dimension_rows(payload)]


class SuggestionRequest(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    judgment_id: int = Field(ge=1, le=2**63 - 1)
    version: int = Field(ge=1, le=2**31 - 1)
    model_id: str = Field(min_length=1, max_length=128)


class DimensionEdit(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    section: Section
    index: int = Field(ge=0, le=10000)
    dimension: str | None = Field(max_length=32)


class SaveDimensionsRequest(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    judgment_id: int = Field(ge=1, le=2**63 - 1)
    base_version: int = Field(ge=1, le=2**31 - 1)
    items: list[DimensionEdit]


def suggestion_context(scope, body):
    with artifact_session(scope) as session:
        row = selected_judgment(session, body.judgment_id, scope=scope)
        if row.version != body.version:
            raise HTTPException(409, '研判版本不一致，请刷新后补标')
        return to_payload(row), read_dimensions(session)


def suggestion_prompt(payload, dimensions):
    content = payload.model_dump(mode='json', by_alias=True)
    # Frozen chart definitions are unrelated to classification. The judgment's
    # claims, evidence, rules and original source remain the model's context.
    content.pop('chart_references', None)
    targets = [(section, index) for section, index, _ in dimension_rows(payload)]
    return classification_prompt(content, targets, dimensions)


def classification_prompt(content, targets, dimensions):
    prompt = (SUGGESTION_VERSION + '\n你只为指定研判的依据和规则建议研究维度，不得改写、补充或删去任何判断内容。'
        '\n根据完整研判、原文和本研究台维度说明逐条理解；不能确认时 dimension 为 null。'
        + dimension_prompt(dimensions)
        + '\n仅输出 JSON：{"suggestions":[{"section":"supporting|opposing|thresholds|falsifiers",'
        '"index":0,"dimension":"当前维度编号或null","reason":"简短理由"}]}。'
        '\n必须覆盖目标清单的每条且各一次；不得返回其他字段。目标清单：'
        + json.dumps([{'section': section, 'index': index} for section, index in targets], ensure_ascii=False)
        + '\n完整研判：' + json.dumps(content, ensure_ascii=False))
    if len(prompt.encode()) > generation.PROMPT_BYTES:
        raise HTTPException(413, '此研判超过维度补标容量，请手动标注')
    return prompt


def checked_suggestions(response, payload, dimensions):
    targets = {(section, index) for section, index, _ in dimension_rows(payload)}
    return checked_classification(response, targets, dimensions)


def checked_classification(response, targets, dimensions, *, strict_ids=False):
    if not isinstance(response, dict) or set(response) != {'suggestions'} or not isinstance(response['suggestions'], list):
        raise HTTPException(422, '模型维度建议格式无效，未更改研判')
    result, seen = [], set()
    for item in response['suggestions']:
        if (not isinstance(item, dict) or set(item) - {'section', 'index', 'dimension', 'reason'}
                or not isinstance(item.get('section'), str) or type(item.get('index')) is not int
                or not isinstance(item.get('reason'), str) or not 1 <= len(item['reason'].strip()) <= 500):
            raise HTTPException(422, '模型维度建议条目无效，未更改研判')
        key = (item['section'], item['index'])
        if key not in targets or key in seen:
            raise HTTPException(422, '模型维度建议与指定版本的条目不一致，未更改研判')
        seen.add(key)
        tag = checked_dimension(item.get('dimension'), dimensions)
        if strict_ids and ('dimension' not in item or item['dimension'] is not None and tag is None):
            raise HTTPException(422, '模型返回了研究台不存在的维度，未更改研判')
        result.append({'section': key[0], 'index': key[1], 'dimension': tag, 'reason': item['reason'].strip()})
    if seen != targets:
        raise HTTPException(422, '模型维度建议未覆盖全部条目，未更改研判')
    return result


def audit_suggestion(scope, body, *, suggestions=None, error=None):
    with artifact_session(scope) as session:
        session.add(AuditLog(at=datetime.now(timezone.utc), actor=scope.identity.user_id,
            action='研判维度建议', target_type='judgment', target_id=str(body.judgment_id), detail={
                'workbench_id': scope.workbench_id, 'version': body.version, 'model_id': body.model_id,
                'prompt_version': SUGGESTION_VERSION, 'status': 'failed' if error else 'succeeded',
                **({'error_status': error.status_code} if error else {'suggestion_count': len(suggestions)})}))
        session.commit()


def context_stamp(payload, dimensions):
    return digest([payload.model_dump(mode='json'), summaries(dimensions)])


async def verify_suggestions(scope, body, result):
    current, dimensions = await run_in_threadpool(suggestion_context, scope, SuggestionRequest.model_validate(body))
    if context_stamp(current, dimensions) != result['_verification']:
        raise HTTPException(409, '研判或研究维度已变化，请刷新后补标')


analysis_jobs.result_checks['judgment_dimensions'] = verify_suggestions


async def classify(scope, model_id, prompt, progress):
    await generation.current_model(scope, model_id)
    await progress('analyzing')
    response = await asyncio.wait_for(generation.completion(scope, model_id, prompt), generation.GENERATION_SECONDS)
    await progress('validating')
    verified = await generation.resolve_workbench(scope.identity, scope.workbench_id, scope.authorization)
    if (verified.restricted or not verified.can_edit or 'judgments' not in verified.view_modules
            or generation.scope_key(verified) != generation.scope_key(scope)):
        raise HTTPException(403, '当前研究台权限已变化，请刷新后补标')
    return response, verified


async def run_suggestions(scope, request_body, progress, job_id):
    body = SuggestionRequest.model_validate(request_body)
    payload, dimensions = await run_in_threadpool(suggestion_context, scope, body)
    prompt = suggestion_prompt(payload, dimensions)
    try:
        response, verified = await classify(scope, body.model_id, prompt, progress)
        suggestions = checked_suggestions(response, payload, dimensions)
        result = {'judgment_id': body.judgment_id, 'version': body.version, 'suggestions': suggestions,
                  'research_dimensions': summaries(dimensions), '_verification': context_stamp(payload, dimensions)}
        await verify_suggestions(verified, request_body, result)
    except asyncio.TimeoutError:
        error = HTTPException(504, '模型维度补标超时，请重试；未更改研判')
        await run_in_threadpool(audit_suggestion, scope, body, error=error)
        raise error from None
    except HTTPException as error:
        await run_in_threadpool(audit_suggestion, scope, body, error=error)
        raise
    await run_in_threadpool(audit_suggestion, scope, body, suggestions=suggestions)
    return result


@router.post('/dimension-suggestions', status_code=202)
async def suggest_dimensions(request: Request):
    scope = generation.scope_for_analysis(write=True)
    panel_scope(write=True)
    body = await generation.read_body(request, SuggestionRequest)
    await run_in_threadpool(suggestion_context, scope, body)
    return await analysis_jobs.start_job(scope, 'judgment_dimensions', body.model_dump(mode='json'),
                                         request.headers.get('idempotency-key'), run_suggestions)


DRAFT_PURPOSE = 'judgment_review_dimensions'


class DraftDimensionsRequest(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    draft_id: str = Field(min_length=16, max_length=128)
    revision: int = Field(ge=1)
    model_id: str = Field(min_length=1, max_length=128)


def draft_targets(preview, dimensions):
    from tin.web.judgment_review import stale_review_dimensions
    targets = {}
    for item, _ in stale_review_dimensions(preview, dimensions):
        path = item['path']
        section = {'bull': 'supporting', 'bear': 'opposing'}[path[1]] if path[0] == 'contradiction' else path[0]
        targets[section, path[-1]] = item
    return targets


async def draft_context(scope, token, revision):
    from tin.web.judgment_review import check_revision, current_preview
    preview, state = await current_preview(scope, token, verify_setup=True)
    if preview.get('item_edit'):
        raise HTTPException(422, '请在逐条修改中手动选择研究维度')
    check_revision(preview, revision)
    targets = draft_targets(preview, state['research_dimensions'])
    if not targets:
        raise HTTPException(422, '没有需要处理的过期维度')
    return preview, state, targets


def draft_stamp(state, targets):
    return digest([state['research_dimensions'], [[section, index, item] for (section, index), item in targets.items()]])


async def verify_draft_suggestions(scope, body, result):
    _, state, targets = await draft_context(scope, body['draft_id'], body['revision'])
    if draft_stamp(state, targets) != result['_verification']:
        raise HTTPException(409, '研判或研究维度已变化，请刷新后补标')


analysis_jobs.result_checks[DRAFT_PURPOSE] = verify_draft_suggestions


async def run_draft_suggestions(scope, body, progress, job_id):
    preview, state, targets = await draft_context(scope, body['draft_id'], body['revision'])
    dimensions = state['research_dimensions']
    fields = deepcopy(preview['document']['judgment'])
    fields.pop('chart_references', None)
    content = {'judgment': fields, 'source': preview['text'], 'review_targets': [
        {'section': section, 'index': index, 'value': item['value'], 'source_quote': item['source_quote']}
        for (section, index), item in targets.items()]}
    response, verified = await classify(scope, body['model_id'], classification_prompt(content, targets, dimensions), progress)
    suggestions = checked_classification(response, set(targets), dimensions, strict_ids=True)
    result = {'draft_id': body['draft_id'], 'revision': body['revision'],
              'suggestions': suggestions, '_verification': draft_stamp(state, targets)}
    await verify_draft_suggestions(verified, body, result)
    return result


@router.post('/review/dimensions/jobs', status_code=202)
async def suggest_draft_dimensions(request: Request):
    scope = generation.scope_for_analysis(write=True)
    body = await generation.read_body(request, DraftDimensionsRequest)
    await draft_context(scope, body.draft_id, body.revision)
    return await analysis_jobs.start_job(scope, DRAFT_PURPOSE, body.model_dump(mode='json'),
        request.headers.get('idempotency-key'), run_draft_suggestions, result_slot=digest(body.draft_id))


async def persist_draft_dimensions(scope, token, revision, result=None):
    from tin.web.judgment_review import build_document, checked, preview_response, record_decisions
    before, state, targets = await draft_context(scope, token, revision)
    if result is not None and draft_stamp(state, targets) != result['_verification']:
        raise HTTPException(409, '研判或研究维度已变化，请刷新后补标')
    preview = deepcopy(before)
    dimensions = state['research_dimensions']
    items = draft_targets(preview, dimensions)
    suggestions = {(entry['section'], entry['index']): entry for entry in (result or {}).get('suggestions', [])}
    decisions = []
    for key, item in items.items():
        suggestion = suggestions.get(key)
        if suggestion is None or suggestion['dimension'] is None:
            item['value'].pop('dimension', None)
        else:
            item['value']['dimension'] = suggestion['dimension']
        item['dimension_only'] = True
        if suggestion is not None:
            item['dimension_suggestion'] = {'dimension': suggestion['dimension'], 'reason': suggestion['reason']}
        else:
            item.pop('dimension_suggestion', None)
        decisions.append((item, False))
    preview['document'] = build_document(preview, state['catalog'])
    checked(preview, preview['document'], state['catalog'], scope)
    preview['review']['revision'] += 1
    await run_in_threadpool(record_decisions, scope, token, before, preview, state['catalog'], decisions)
    return preview_response(scope, token, preview, state)


@router.post('/review/dimensions/clear')
async def clear_draft_dimensions(request: Request):
    from tin.web.judgment_review import ReviewRequest
    scope = generation.scope_for_analysis(write=True)
    body = await generation.read_body(request, ReviewRequest)
    async with generation.previews.mutation(scope, body.draft_id):
        return await persist_draft_dimensions(scope, body.draft_id, body.revision)


@router.post('/review/dimensions/jobs/{job_id}/apply')
async def apply_draft_suggestions(job_id: str):
    scope = generation.scope_for_analysis(write=True)
    _, value = await analysis_jobs.verified_result(scope, job_id, expected_purpose=DRAFT_PURPOSE)
    request, result = value['request'], value['result']
    async with generation.previews.mutation(scope, request['draft_id']):
        return await persist_draft_dimensions(scope, request['draft_id'], request['revision'], result)


def persist_dimensions(scope, body):
    with generation.result_session(scope) as session:
        source = selected_judgment(session, body.judgment_id, scope=scope)
        original = to_payload(source)
        payload = original.model_copy(deep=True)
        dimensions = read_dimensions(session)
        positions = {(section, index): value for section, index, value in dimension_rows(payload)}
        seen = set()
        for edit in body.items:
            key = (edit.section, edit.index)
            if key not in positions or key in seen:
                raise HTTPException(422, '维度条目与所选研判不一致，请刷新后保存')
            if edit.dimension is not None and checked_dimension(edit.dimension, dimensions) is None:
                raise HTTPException(422, '请选择当前研究台存在的维度，或选择未标注')
            seen.add(key)
            positions[key].dimension = edit.dimension
        if seen != set(positions):
            raise HTTPException(422, '请核对所选研判的全部维度条目')
        if payload == original:
            raise HTTPException(422, '维度尚未修改')
        if scope.scope_version == 2:
            from tin.judgments.service import _refs
            from tin.web.chart_data import catalog_fields
            session.info.update(knowledge_facts=True,
                knowledge_facts_catalog=catalog_fields(scope, [sid for _, _, sid in _refs(payload)]))
        declarations = {rule.id: rule.caliber_confirmation.model_dump()
            for rule in [*payload.thresholds, *payload.falsifiers] if rule.caliber_confirmation is not None}
        # These are the authorized source version's immutable chart snapshots,
        # not client-supplied pins. A label edit must retain them byte-for-byte.
        payload.chart_references = []
        try:
            saved = save_version(session, payload, actor=scope.identity.user_id,
                expected_base_version=body.base_version, caliber_confirmations=declarations)
            if 'chart_references' in source.contradiction:
                saved.contradiction = {**saved.contradiction,
                    'chart_references': deepcopy(source.contradiction['chart_references'])}
            panel = register_panel(session, scope, saved)
            session.add(AuditLog(at=datetime.now(timezone.utc), actor=scope.identity.user_id,
                action='研判维度保存', target_type='judgment', target_id=str(saved.id), detail={
                    'workbench_id': scope.workbench_id, 'source_id': source.id,
                    'source_version': source.version, 'base_version': body.base_version,
                    'version': saved.version, 'items': [item.model_dump() for item in body.items]}))
            session.commit()
            return {'id': saved.id, 'version': saved.version, 'status': saved.status, 'panel': panel}
        except (JudgmentVersionConflict, IntegrityError, OperationalError):
            session.rollback()
            raise HTTPException(409, '研判或来源已变化，请刷新后保存维度') from None
        except JudgmentError as error:
            session.rollback()
            raise HTTPException(422, str(error)) from None


@router.post('/dimensions')
async def save_dimensions(request: Request):
    scope = generation.scope_for_analysis(write=True)
    panel_scope(write=True)
    body = await generation.read_body(request, SaveDimensionsRequest)
    return await run_in_threadpool(persist_dimensions, scope, body)
