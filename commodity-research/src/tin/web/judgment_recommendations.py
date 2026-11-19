"""Source-checked indicator recommendations for private judgment drafts."""
from datetime import datetime, timezone
from decimal import Decimal
import json
import logging

from fastapi import APIRouter, HTTPException, Request
from pydantic import BaseModel, ConfigDict, Field

from tin.compute.rule_caliber import unit_present
from tin.judgments.analysis import rule_frequency_issues
from tin.judgments.numbers import quoted_quantities
from tin.models import AuditLog
from tin.ownership import verified_actor
from tin.schemas.judgment import EXPRESSION
from tin.web import analysis_jobs, judgment_generation as generation, judgment_jobs
from tin.web import judgment_quantity as quantity, judgment_recall as recall, judgment_review as review

PURPOSE = 'judgment_indicator_recommendations'
router = APIRouter(prefix='/api/sn/judgment')


class AutomaticRequest(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    draft_id: str = Field(min_length=16, max_length=128)


class RecommendationRequest(AutomaticRequest):
    item_id: str | None = Field(default=None, min_length=1, max_length=80)
    model_id: str = Field(min_length=1, max_length=128)
    search: str = Field(default='', max_length=200)


def source_quote(preview, item):
    if item['source_quote']:
        return item['source_quote']
    path = ('/unresolved/' + '/'.join(map(str, item['path'][1:])) if item['section'] == 'unresolved'
            else '/judgment/' + '/'.join(map(str, item['path'])))
    for covered in preview.get('source_coverage', {}).get('items', []):
        paths = [covered.get('path'), covered.get('candidate_path'),
                 *covered.get('paths', []), *covered.get('candidate_paths', [])]
        quote = covered.get('quote')
        if path in paths and quote and quote in preview['text']:
            return quote
    return ''


def item_evidence(preview, item):
    return {'条目': item['title'], '条目文字': review.describe(item['value']),
            '原文引句': source_quote(preview, item)}


def item_stamp(preview, item):
    return review.digest([preview['fact_version'], item['kind'], item_evidence(preview, item)])


def compatible(preview, item, field, units):
    if field['id'] in item.get('related_series_ids', ()):
        return False
    kind = item['kind']
    if kind not in quantity.RULE_KINDS:
        return True
    unit = field.get('unit')
    if not unit_present(unit):
        return False
    declared = review.source_unit_for_item(preview, item) or item['value'].get('unit_confirmed')
    if unit_present(declared) and declared != unit:
        return False
    quote = source_quote(preview, item)
    if not quote or rule_frequency_issues(kind, item['value'], quote, field.get('frequency')):
        return False
    values = quantity.source_value(item)
    if values is None:
        return True
    if kind == 'falsifiers' and values == 0:
        statement = EXPRESSION.fullmatch(item['value']['expression'].strip())
        if statement and statement.group(1) == 'change':
            return True  # Directional zero carries no quantity unit in the source.
    parsed = quoted_quantities(quote, unit=unit, units=units)
    return all(Decimal(str(value)) in parsed for value in (values if isinstance(values, list) else [values]))


async def recommendation_context(scope, body):
    preview, state = await review.current_preview(scope, body.draft_id, verify_setup=True)
    items = {row['id']: row for row in preview['review']['items'] if review.can_bind(row)}
    if body.item_id and body.item_id not in items:
        raise HTTPException(422, '请选择可关联指标的研判条目')
    return preview, state['catalog'], items


async def verify_result(scope, request, result):
    body = RecommendationRequest.model_validate(request)
    preview, _, items = await recommendation_context(scope, body)
    expected = result.get('_verification')
    if body.item_id and isinstance(expected, str):
        # Earlier jobs exposed manual choices only and did not persist a draft binding.
        item = items[body.item_id]
        evidence = item_evidence(preview, item)
        inventory = {'items': [{'id': item['id'], 'kind': item['kind'],
                                'quote': evidence['原文引句'] or evidence['条目文字']}]}
        if review.digest([preview['fact_version'], inventory, evidence]) == expected:
            return
    if not isinstance(expected, dict) or (
            body.item_id and item_stamp(preview, items[body.item_id]) != expected.get(body.item_id)):
        raise HTTPException(409, '条目原文已变化，请重新推荐指标')


async def run_recommendations(scope, request, progress, _job_id):
    body = RecommendationRequest.model_validate(request)
    await progress('loading_catalog')
    preview, catalog, items = await recommendation_context(scope, body)
    items = ({body.item_id: items[body.item_id]} if body.item_id else
             {key: item for key, item in items.items() if review.needs_indicator_recommendation(item)})
    evidence = {key: item_evidence(preview, item) for key, item in items.items()}
    inventory = {'items': [{'id': key, 'kind': item['kind'],
                            'quote': evidence[key]['原文引句'] or evidence[key]['条目文字']}
                           for key, item in items.items()]}
    recommendations = {}
    if items:
        await generation.current_model(scope, body.model_id)
        units = tuple(dict.fromkeys(field['unit'] for field in catalog if unit_present(field.get('unit'))))
        text = json.dumps(evidence[body.item_id], ensure_ascii=False) if body.item_id else preview['text']
        recommendations = await recall.recommend(scope, body.model_id, catalog, text, inventory,
            search=body.search, progress=progress, compatible=lambda subject, field:
                compatible(preview, items[subject['id']], field, units))
    result = {'draft_id': body.draft_id, 'item_id': body.item_id, 'items': recommendations,
              '_verification': {key: item_stamp(preview, item) for key, item in items.items()}}
    if body.item_id:
        result.update(recommendations.get(body.item_id, {'recommended': None, 'recommendations': []}))
    await progress('validating')
    await verify_result(scope, request, result)
    return result


def store_recommendations(session, scope, request, result):
    token = result['draft_id']
    preview = generation.previews.get(scope, token)
    changed = False
    for item in preview['review']['items']:
        identifier = item['id']
        proposed = result['items'].get(identifier)
        if proposed is None or item_stamp(preview, item) != result['_verification'][identifier]:
            continue
        if not request.get('item_id') and not review.needs_indicator_recommendation(item):
            continue
        recommended = proposed['recommended'] if review.can_fill_recommendation(item) else None
        item['indicator_recommendation'] = {'recommended': recommended, 'recommendations': proposed['recommendations'],
                                            'matched': proposed['recommended'] is not None}
        if recommended:
            item['status'] = 'pending'
        changed = True
    if changed:
        revision = preview['review']['revision']
        preview['review']['revision'] = revision + 1
        if judgment_jobs.is_job_token(token):
            judgment_jobs.replace_preview(session, scope, token, preview, revision)
        else:
            generation.previews.replace(scope, token, preview, revision)
    return result


def record_audit(session, scope, job_id, body, result=None):
    detail = {'workbench_id': scope.workbench_id, 'draft_sha256': review.digest(body['draft_id']),
              'item_id': body.get('item_id'), 'model_id': body['model_id']}
    if result is not None:
        detail['candidate_count'] = sum(len(row['recommendations']) for row in result['items'].values())
    session.add(AuditLog(at=datetime.now(timezone.utc), actor=verified_actor(session, ''),
        action='提交研判指标推荐' if result is None else '完成研判指标推荐',
        target_type=PURPOSE, target_id=job_id, detail=detail))


analysis_jobs.result_checks[PURPOSE] = verify_result
analysis_jobs.result_writers[PURPOSE] = store_recommendations
analysis_jobs.record_audits[PURPOSE] = record_audit


async def submit(scope, body, key):
    token = body.draft_id
    parent = token.split('.')[1] if judgment_jobs.is_job_token(token) else None
    return await analysis_jobs.start_job(scope, PURPOSE, body.model_dump(mode='json'), key,
        run_recommendations, timeout=generation.GENERATION_SECONDS, result_parent=parent,
        result_slot=review.digest([token, body.item_id]))


async def automatic_job(scope, token, preview):
    if preview.get('model_id') and any(review.needs_indicator_recommendation(item) for item in preview['review']['items']):
        body = RecommendationRequest(draft_id=token, model_id=preview['model_id'])
        return await submit(scope, body, review.digest([PURPOSE, token]))
    return None


async def start_automatic(scope, token, preview):
    try:
        await automatic_job(scope, token, preview)
    except Exception:
        logging.getLogger(__name__).warning('Automatic judgment indicator recommendation admission deferred')


@router.post('/indicator-recommendations/automatic', status_code=202)
async def automatic_recommendations(request: Request):
    scope = generation.scope_for_analysis(write=True)
    body = await generation.read_body(request, AutomaticRequest)
    preview, _ = await review.current_preview(scope, body.draft_id, verify_setup=True)
    return {'job': await automatic_job(scope, body.draft_id, preview)}


@router.post('/indicator-recommendations', status_code=202)
async def recommend_indicators(request: Request):
    scope = generation.scope_for_analysis(write=True)
    body = await generation.read_body(request, RecommendationRequest)
    if body.item_id is None:
        raise HTTPException(422, '请选择可关联指标的研判条目')
    await recommendation_context(scope, body)
    return await submit(scope, body, request.headers.get('idempotency-key'))
