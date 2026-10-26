"""Human-authored typed research: bounded identity validation, preview, explicit save."""
from datetime import datetime, timezone, timedelta
import hashlib
import json
import math
import re

from fastapi import APIRouter, HTTPException, Request
from pydantic import BaseModel, ConfigDict
from sqlalchemy.exc import IntegrityError, OperationalError
from starlette.concurrency import run_in_threadpool

from tin.integration.workbench_scope import resolve_workbench, require_shared_write
from tin.judgments.markdown import dump_markdown, version_diff
from tin.judgments.service import _refs, latest, save_version, to_payload, JudgmentError
from tin.judgments.validate import activation_blockers
from tin.models import AuditLog
from tin.schemas.judgment import JudgmentIn, EXPRESSION
from tin.web.judgment_generation import (ApplyRequest, PreviewStore, result_session,
                                         scope_for_analysis, scope_key, read_body)
from tin.config import SHANGHAI

router = APIRouter(prefix='/api/sn/judgment/structured')
previews = PreviewStore()


class StructuredInput(BaseModel):
    model_config = ConfigDict(extra='forbid')
    payload: JudgmentIn


def _validate_payload(payload, catalog):
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,15}', payload.variety):
        raise HTTPException(422, '研究品种需要1至16位明确代码，只允许字母、数字、点、横线和下划线')
    from tin.integration.page_facts import _judgment_windows
    try:
        _judgment_windows(payload)
    except HTTPException as error:
        raise HTTPException(422, error.detail) from None
    if payload.written_at > datetime.now(SHANGHAI).date():
        raise HTTPException(422, '研究稿撰写日期不能晚于今天')
    try:
        payload.written_at + timedelta(days=payload.review_period_days)
    except (OverflowError, ValueError):
        raise HTTPException(422, '研究稿复核日期超出可表示范围') from None
    def finite(value):
        if isinstance(value, float) and not math.isfinite(value):
            raise HTTPException(422, '研究稿数值必须为有限数')
        if isinstance(value, dict):
            for item in value.values(): finite(item)
        if isinstance(value, list):
            for item in value: finite(item)
    finite(payload.model_dump(mode='json'))
    unknown = {'', '—', '未注明'}
    for rule in [*payload.thresholds, *payload.falsifiers]:
        if not re.fullmatch(r'[A-Za-z][A-Za-z0-9_-]{0,63}', rule.id):
            raise HTTPException(422, '研究规则局部编号需要1至64位，以字母开头，仅含字母、数字、横线和下划线')
        unit = catalog[rule.series_id].get('unit')
        if not isinstance(unit, str) or unit.strip() in unknown:
            raise HTTPException(422, '量化规则引用的指标缺少已确认单位；请先作为待确认文字保留')
        if hasattr(rule, 'unit_confirmed') and rule.unit_confirmed.strip() != unit.strip():
            raise HTTPException(422, '证伪条件单位与真实指标单位不一致')
        if hasattr(rule, 'expression'):
            expression = EXPRESSION.fullmatch(rule.expression)
            if expression is None or not math.isfinite(float(expression.group(3))):
                raise HTTPException(422, '证伪条件中的比较值必须是有限数')
    evidence = [*payload.contradiction.bull.evidence, *payload.contradiction.bear.evidence]
    costs = payload.cost_line
    if (any(item.value is not None for item in evidence)
            or costs and (costs.value is not None or any(item.value is not None for item in costs.items))):
        raise HTTPException(422, '人工结构化稿不导入未经取数核验的事实数值；请在来源原文保留这些说明，用真实指标绑定取数')


def _state(scope, payload):
    wanted = sorted({sid for _, _, sid in _refs(payload)})
    if len(wanted) > 128:
        raise HTTPException(413, '一份研究逻辑最多绑定128个明确指标')
    metadata = None
    if scope.scope_version == 2:
        from tin.integration.kb_facts import catalog_page
        page = catalog_page(scope, limit=200, series_ids=wanted)
        metadata = {'catalog': {row['series_id']: row for row in page['series']},
                    'version': page['version'], 'datasets': page.get('datasets', [])}
    with result_session(scope) as session:
        catalog = metadata['catalog'] if metadata is not None else session.info['knowledge_facts_catalog']
        if set(wanted) - set(catalog):
            raise HTTPException(422, '部分指标不在当前已授权来源中；请核对真实指标ID，不按名称替代绑定')
        row = latest(session, payload.variety)
        return {'catalog': catalog,
                'version': metadata['version'] if metadata is not None else session.info['knowledge_facts_version'],
                'datasets': metadata['datasets'] if metadata is not None else session.info.get('knowledge_facts_datasets', []),
                'base_version': row.version if row else 0, 'before': to_payload(row) if row else None}


@router.post('/preview')
async def preview(request: Request):
    body = await read_body(request, StructuredInput)
    scope = scope_for_analysis(write=True)
    require_shared_write(scope)
    payload = body.payload.model_copy(update={'author': scope.identity.user_id})
    encoded = payload.model_dump_json(by_alias=True)
    if len(encoded.encode()) > 48 * 1024:
        raise HTTPException(413, '结构化研究逻辑超过48 KiB')
    state = await run_in_threadpool(_state, scope, payload)
    _validate_payload(payload, state['catalog'])
    token = previews.put(scope, {'payload': payload.model_dump(mode='json', by_alias=True),
                                'base_version': state['base_version'], 'fact_version': state['version']})
    bindings = [{'series_id': sid, 'name': spec.get('metric') or spec.get('name') or sid,
                 'unit': spec.get('unit')} for sid, spec in state['catalog'].items()
                 if sid in {sid for _, _, sid in _refs(payload)}]
    return {'kind': 'structured', 'draft_id': token, 'base_version': state['base_version'],
            'markdown': dump_markdown(payload), 'diff': version_diff(state['before'], payload),
            'bindings': bindings, 'unresolved': [row.model_dump() for row in payload.unstructured],
            'activation_blockers': activation_blockers(payload),
            'catalog_total': len(state['catalog']), 'candidate_count': len(bindings),
            'notice': '已核对结构与真实指标身份，原文观点仍需人工审核。未调用模型；确认后仅保存草稿，不自动生效。'}


@router.post('/apply')
async def apply(request: Request):
    body = await read_body(request, ApplyRequest)
    scope = scope_for_analysis(write=True)
    require_shared_write(scope)
    saved = previews.get(scope, body.draft_id)
    if body.base_version != saved['base_version']:
        raise HTTPException(409, '预览基准版本不一致，请重新预览')
    fresh = await resolve_workbench(scope.identity, scope.workbench_id, scope.authorization)
    require_shared_write(fresh)
    if scope_key(scope) != scope_key(fresh):
        raise HTTPException(409, '研究台权限或来源已改变，请重新预览')
    payload = JudgmentIn.model_validate(saved['payload'])
    scope = fresh
    state = await run_in_threadpool(_state, scope, payload)
    _validate_payload(payload, state['catalog'])
    if state['base_version'] != saved['base_version'] or state['version'] != saved['fact_version']:
        raise HTTPException(409, '研究逻辑或来源数据已改变，请重新预览')

    def persist():
        with result_session(scope) as session:
            session.info.update(knowledge_facts=True, knowledge_facts_catalog=state['catalog'],
                                knowledge_facts_version=state['version'])
            try:
                row = save_version(session, payload, actor=scope.identity.user_id,
                                   expected_base_version=saved['base_version'])
                digest = hashlib.sha256(json.dumps(saved['payload'], sort_keys=True, ensure_ascii=False).encode()).hexdigest()
                session.add(AuditLog(at=datetime.now(timezone.utc), actor=scope.identity.user_id,
                    action='结构化研究逻辑导入', target_type='judgment', target_id=str(row.id),
                    detail={'version': row.version, 'variety': row.variety, 'payload_sha256': digest,
                            'fact_version': state['version'], 'model_calls': 0, 'status': '草稿'}))
                session.commit()
                return {'version': row.version, 'status': row.status, 'variety': row.variety,
                        'notice': '已保存为新草稿；上一版本保留，请核对后再生效。'}
            except (JudgmentError, IntegrityError, OperationalError):
                session.rollback()
                raise HTTPException(409, '研究逻辑已有新版本或正在保存，请重新预览') from None
    result = await run_in_threadpool(persist)
    previews.consume(body.draft_id)
    return result
