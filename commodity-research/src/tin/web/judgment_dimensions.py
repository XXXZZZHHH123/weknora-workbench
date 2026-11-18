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
from tin.judgments.service import JudgmentError, save_version, to_payload
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
    targets = [{'section': section, 'index': index} for section, index, _ in dimension_rows(payload)]
    prompt = (SUGGESTION_VERSION + '\n你只为指定研判版本的依据和规则建议研究维度，不得改写、补充或删去任何判断内容。'
        '\n根据完整研判、原文和本研究台维度说明逐条理解；不能确认时 dimension 为 null。'
        + dimension_prompt(dimensions)
        + '\n仅输出 JSON：{"suggestions":[{"section":"supporting|opposing|thresholds|falsifiers",'
        '"index":0,"dimension":"当前维度编号或null","reason":"简短理由"}]}。'
        '\n必须覆盖目标清单的每条且各一次；不得返回其他字段。目标清单：'
        + json.dumps(targets, ensure_ascii=False) + '\n完整研判：' + json.dumps(content, ensure_ascii=False))
    if len(prompt.encode()) > generation.PROMPT_BYTES:
        raise HTTPException(413, '此研判超过维度补标容量，请手动标注')
    return prompt


def checked_suggestions(response, payload, dimensions):
    if not isinstance(response, dict) or set(response) != {'suggestions'} or not isinstance(response['suggestions'], list):
        raise HTTPException(422, '模型维度建议格式无效，未更改研判')
    wanted = {(section, index) for section, index, _ in dimension_rows(payload)}
    result, seen = [], set()
    for item in response['suggestions']:
        if (not isinstance(item, dict) or set(item) - {'section', 'index', 'dimension', 'reason'}
                or not isinstance(item.get('section'), str) or type(item.get('index')) is not int
                or not isinstance(item.get('reason'), str) or not 1 <= len(item['reason'].strip()) <= 500):
            raise HTTPException(422, '模型维度建议条目无效，未更改研判')
        key = (item['section'], item['index'])
        if key not in wanted or key in seen:
            raise HTTPException(422, '模型维度建议与指定版本的条目不一致，未更改研判')
        seen.add(key)
        result.append({'section': key[0], 'index': key[1],
            'dimension': checked_dimension(item.get('dimension'), dimensions), 'reason': item['reason'].strip()})
    if seen != wanted:
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


async def run_suggestions(scope, request_body, progress, job_id):
    body = SuggestionRequest.model_validate(request_body)
    payload, dimensions = await run_in_threadpool(suggestion_context, scope, body)
    prompt = suggestion_prompt(payload, dimensions)
    try:
        await generation.current_model(scope, body.model_id)
        await progress('analyzing')
        response = await asyncio.wait_for(generation.completion(scope, body.model_id, prompt), generation.GENERATION_SECONDS)
        await progress('validating')
        verified = await generation.resolve_workbench(scope.identity, scope.workbench_id, scope.authorization)
        if (verified.restricted or not verified.can_edit or 'judgments' not in verified.view_modules
                or generation.scope_key(verified) != generation.scope_key(scope)):
            raise HTTPException(403, '当前研究台权限已变化，请刷新后补标')
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
        except (JudgmentError, IntegrityError, OperationalError):
            session.rollback()
            raise HTTPException(409, '研判或来源已变化，请刷新后保存维度') from None


@router.post('/dimensions')
async def save_dimensions(request: Request):
    scope = generation.scope_for_analysis(write=True)
    panel_scope(write=True)
    body = await generation.read_body(request, SaveDimensionsRequest)
    return await run_in_threadpool(persist_dimensions, scope, body)
