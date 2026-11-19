"""Detached, source-checked indicator recommendations for a single review item."""
import json

from fastapi import APIRouter, HTTPException, Request
from pydantic import BaseModel, ConfigDict, Field
from starlette.concurrency import run_in_threadpool

from tin.web import analysis_jobs, judgment_generation as generation, judgment_recall as recall, judgment_review as review

PURPOSE = 'judgment_indicator_recommendations'
router = APIRouter(prefix='/api/sn/judgment')


class RecommendationRequest(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    draft_id: str = Field(min_length=16, max_length=128)
    item_id: str = Field(min_length=1, max_length=80)
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


async def recommendation_context(scope, body):
    preview, state = await review.current_preview(scope, body.draft_id, verify_setup=True)
    item = next((row for row in preview['review']['items'] if row['id'] == body.item_id), None)
    if item is None or item['kind'] not in ('binding', 'unresolved_binding', 'whitelist', 'thresholds', 'falsifiers'):
        raise HTTPException(422, '请选择可关联指标的研判条目')
    evidence = {'条目': item['title'], '条目文字': review.describe(item['value']),
                '原文引句': source_quote(preview, item)}
    inventory = {'items': [{'id': item['id'], 'kind': item['kind'],
                            'quote': evidence['原文引句'] or evidence['条目文字']}]}
    stamp = review.digest([preview['fact_version'], inventory, evidence])
    return state['catalog'], inventory, evidence, stamp


async def verify_result(scope, request, result):
    body = RecommendationRequest.model_validate(request)
    _, _, _, stamp = await recommendation_context(scope, body)
    if stamp != result['_verification']:
        raise HTTPException(409, '条目原文已变化，请重新推荐指标')


analysis_jobs.result_checks[PURPOSE] = verify_result


async def run_recommendations(scope, request, progress, _job_id):
    body = RecommendationRequest.model_validate(request)
    await progress('loading_catalog')
    catalog, inventory, evidence, stamp = await recommendation_context(scope, body)
    await generation.current_model(scope, body.model_id)
    shared = await run_in_threadpool(recall.shared_candidate_ids, scope, catalog)
    text = json.dumps(evidence, ensure_ascii=False)
    prompt, mapping = await generation.recall_shortlist(scope, body.model_id, catalog, text, inventory,
        shared=shared, search=body.search, recommend=True, progress=progress)
    recommendations = []
    if mapping:
        await progress('analyzing')
        response = await generation.selection_completion(scope, body.model_id, prompt)
        by_id = {field['id']: field for field in catalog}
        recommendations = [{**review.binding(by_id[sid]), 'dimensions': by_id[sid].get('dimensions'), 'reason': reason}
                           for sid, reason in recall.picked_recommendations(response, mapping)]
    result = {'draft_id': body.draft_id, 'item_id': body.item_id,
              'recommendations': recommendations, '_verification': stamp}
    await progress('validating')
    await verify_result(scope, request, result)
    return result


@router.post('/indicator-recommendations', status_code=202)
async def recommend_indicators(request: Request):
    scope = generation.scope_for_analysis(write=True)
    body = await generation.read_body(request, RecommendationRequest)
    await recommendation_context(scope, body)
    return await analysis_jobs.start_job(scope, PURPOSE, body.model_dump(mode='json'),
        request.headers.get('idempotency-key'), run_recommendations, timeout=generation.GENERATION_SECONDS,
        result_slot=review.digest([body.draft_id, body.item_id]))
