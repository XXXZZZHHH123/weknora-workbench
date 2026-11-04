"""A researcher can discard an unsaved background analysis result; nothing saved is touched."""
import asyncio

import pytest
from fastapi import HTTPException

from tests.test_judgment_analysis_jobs import background, body, completed_job  # noqa: F401
from tests.test_judgment_generation import generator, rows  # noqa: F401
from tests.test_knowledge_facts import FIRST, canonical, page  # noqa: F401
from tin.integration.workbench_scope import artifact_session
from tin.models import SourceMapping
from tin.web import judgment_jobs as jobs


def private_record(scope, job_id):
    with artifact_session(scope) as session:
        return session.get(SourceMapping, jobs.job_key(scope, job_id)).config


def discard(client, headers, job_id):
    return client.post(page(FIRST, f'/api/sn/judgment/jobs/{job_id}/discard'), headers=headers, json={})


def test_discard_retires_the_result_prunes_the_preview_and_forgets_the_cached_analysis(background):
    client, headers, _, _, calls = background
    scope, meta, result = asyncio.run(completed_job())
    assert len(calls) == 3
    response = discard(client, headers, meta['id'])
    assert response.status_code == 200, response.text
    job = response.json()['job']
    assert job['state'] == 'superseded' and job['discarded'] is True and job['dismissed'] is True
    assert job['message'] == '已舍弃暂存结果' and job['result_available'] is False
    assert asyncio.run(jobs.latest_job(scope))['job'] is None
    with pytest.raises(HTTPException) as caught:
        asyncio.run(jobs.job_result(scope, meta['id']))
    assert caught.value.status_code == 410
    with pytest.raises(HTTPException):
        jobs.load_preview(scope, result['draft_id'])
    record = private_record(scope, meta['id'])
    assert record['state'] == 'superseded' and record['preview'] is None and record['input'] == {}
    assert rows() == []
    again = discard(client, headers, meta['id'])
    assert again.status_code == 200 and again.json()['job']['discarded'] is True
    # The same source is analysed afresh instead of returning the discarded result from the cache.
    async def rerun():
        started = await jobs.start_job(scope, body(), 'synthetic-idempotency-key-after-discard')
        await jobs.runtime.tasks[started['id']]
        return await jobs.job_status(scope, started['id'])
    assert asyncio.run(rerun())['state'] == 'succeeded' and len(calls) > 3


def test_saved_and_foreign_results_cannot_be_discarded(background):
    client, headers, _, _, _ = background
    scope, meta, result = asyncio.run(completed_job())
    other = {**headers, 'Authorization': 'Bearer source-viewer'}
    assert discard(client, other, meta['id']).status_code in {403, 404}
    saved = client.post(page(FIRST, '/api/sn/judgment/apply'), headers=headers,
        json={'draft_id': result['draft_id'], 'base_version': 0, 'review_revision': 1})
    assert saved.status_code == 200, saved.text
    refused = discard(client, headers, meta['id'])
    assert refused.status_code == 409 and '已保存' in refused.json()['detail']
    assert len(rows()) == 1 and rows()[0].status == '草稿'


def test_an_unsaved_preview_of_any_origin_can_be_discarded_before_saving(background):
    from tests.test_judgment_generation import generate
    client, headers, _, _, _ = background
    preview = generate(client, headers).json()
    other = {**headers, 'Authorization': 'Bearer source-viewer'}
    assert client.post(page(FIRST, '/api/sn/judgment/preview/discard'), headers=other,
                       json={'draft_id': preview['draft_id']}).status_code in {403, 404}
    discarded = client.post(page(FIRST, '/api/sn/judgment/preview/discard'), headers=headers, json={'draft_id': preview['draft_id']})
    assert discarded.status_code == 200 and discarded.json()['discarded'] is True
    saved = client.post(page(FIRST, '/api/sn/judgment/apply'), headers=headers,
                        json={'draft_id': preview['draft_id'], 'base_version': 0, 'review_revision': preview['review']['revision']})
    assert saved.status_code == 409 and rows() == []
    again = client.post(page(FIRST, '/api/sn/judgment/preview/discard'), headers=headers, json={'draft_id': preview['draft_id']})
    assert again.status_code == 200, 'a preview that is already gone needs nothing more'


def test_a_background_result_is_discarded_through_the_same_review_action(background):
    client, headers, _, _, _ = background
    scope, meta, result = asyncio.run(completed_job())
    response = client.post(page(FIRST, '/api/sn/judgment/preview/discard'), headers=headers, json={'draft_id': result['draft_id']})
    assert response.status_code == 200 and response.json()['job']['discarded'] is True
    assert asyncio.run(jobs.latest_job(scope))['job'] is None and private_record(scope, meta['id'])['preview'] is None
