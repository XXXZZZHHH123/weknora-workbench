"""Automatic recommendations stay private and require explicit review."""
import asyncio
import json

from httpx import ASGITransport, AsyncClient
import pytest

from tests.test_judgment_analysis_jobs import background, completed_job  # noqa: F401
from tests.test_judgment_catalog_recall import candidates_of, items_of
from tests.test_judgment_generation import endpoint, generator, model_fixture, rows  # noqa: F401
from tests.test_knowledge_facts import SERIES, canonical  # noqa: F401
from tests.test_workbench_setup_jobs import completed as completed_setup, workbench  # noqa: F401
from tin.web import analysis_jobs, judgment_generation as generation, judgment_jobs, judgment_recommendations
from tin.web import workbench_setup
from tin.web.judgment_recommendations import PURPOSE, compatible


@pytest.mark.parametrize('outcome', ['filled', 'invalid', 'researcher_edited'])
def test_analysis_starts_one_recommendation_job_and_keeps_review_pending(background, monkeypatch, outcome):
    client, headers, _, response, _ = background
    response['judgment']['contradiction']['bull']['evidence'][0]['series_id'] = None
    entered, release = asyncio.Event(), asyncio.Event()
    monkeypatch.setattr(analysis_jobs, 'registry', judgment_jobs.registry)
    monkeypatch.setattr(analysis_jobs, 'runtime', analysis_jobs.Runtime())

    async def complete(scope, model, prompt):
        if prompt.startswith('research-catalog-recall.v1'):
            entered.set()
            await asyncio.wait_for(release.wait(), timeout=5)
            subjects = json.loads(prompt.split('\n研究条目：', 1)[1].split('\n完整原文：', 1)[0])
            return {'queries': {item['id']: ['Measured quantity'] for item in subjects}}
        if '逐条推荐至多' in prompt:
            shown = candidates_of(prompt)
            ref = next(ref for ref, item in shown.items() if item['name'] == 'Measured quantity')
            return {'recommendations': {item['id']: {'ambiguous': False, 'candidates': [
                {'ref': 'unshown' if outcome == 'invalid' else ref, 'reason': '实测口径一致'}]}
                for item in items_of(prompt)}}
        return model_fixture(prompt, response)

    monkeypatch.setattr(generation, 'completion', complete)
    async def run():
        try:
            scope, parent, before = await completed_job()
            await asyncio.wait_for(entered.wait(), timeout=5)
            token = before['draft_id']
            anchor = next(item for section in before['review']['sections'] for item in section['items']
                          if item['title'] == '多方依据')
            assert anchor['binding'] is None
            job = analysis_jobs.registry.latest(scope.identity, scope.workbench_id, PURPOSE, include_active=True)
            assert job['state'] == 'running' and job['result_parent'] == parent['id']
            task = analysis_jobs.runtime.tasks[job['id']]
            if outcome == 'researcher_edited':
                async with AsyncClient(transport=ASGITransport(app=client.app), base_url=str(client.base_url)) as frontend:
                    edited = await frontend.post(endpoint('/review/update'), headers=headers, json={
                        'draft_id': token, 'revision': before['review']['revision'], 'item_id': anchor['id'],
                        'action': 'keep_text', 'series_id': None})
                assert edited.status_code == 200, edited.text
        finally:
            release.set()
        await task
        status = await analysis_jobs.status(scope, job['id'])
        assert status['state'] == 'succeeded', status
        after = await judgment_jobs.job_result(scope, parent['id'])
        anchor = next(item for section in after['review']['sections'] for item in section['items']
                      if item['id'] == anchor['id'])
        if outcome == 'filled':
            assert anchor['binding']['series_id'] == SERIES and anchor['status'] == 'pending'
            assert anchor['indicator_recommendation']['recommended']['reason'] == '实测口径一致'
            private = generation.previews.get(scope, token)
            assert private['document']['judgment']['contradiction']['bull']['evidence'][0]['series_id'] is None
        elif outcome == 'invalid':
            assert anchor['binding'] is None
            assert anchor['indicator_recommendation'] == {'recommended': None, 'recommendations': [], 'matched': False}
        else:
            assert anchor['binding'] is None and anchor['status'] == 'kept_text'
            assert 'indicator_recommendation' not in anchor
        assert after['review']['confirmed_count'] == 0 and rows() == []

    asyncio.run(run())


def test_weekly_report_setup_enqueues_after_persisting_judgment_draft(workbench, monkeypatch):
    item = {'id': 'synthetic-unbound', 'kind': 'binding', 'status': 'pending', 'origin': 'model', 'series_id': None,
            'binding_explicit': False, 'remembered': False}
    preview = {'model_id': 'fixture-model', 'review': {'items': [item], 'revision': 1}}
    seen = asyncio.Event()

    async def generate(scope, body, job_id, progress):
        await progress('judgment')
        result = {'report': {}, 'items': [{'part': 'judgment', 'state': 'succeeded', 'data': preview}]}
        return result, workbench_setup.original_identity(workbench.report)

    async def recommend(scope, request, progress, job_id):
        token = request['draft_id']
        assert token.startswith('setup.')
        assert generation.previews.get(scope, token)['review']['items'] == [item]
        seen.set()
        return {'draft_id': token, 'items': {}}

    monkeypatch.setattr(workbench_setup, 'generate', generate)
    monkeypatch.setattr(judgment_recommendations, 'run_recommendations', recommend)

    async def run():
        parent, _ = await completed_setup(workbench, 'judgment')
        await asyncio.wait_for(seen.wait(), timeout=5)
        scope = workbench.scope
        job = workbench.registry.latest(scope.identity, scope.workbench_id, PURPOSE, include_active=True)
        assert job['result_parent'] == parent
        task = analysis_jobs.runtime.tasks.get(job['id'])
        if task is not None:
            await task
        assert (await analysis_jobs.status(scope, job['id']))['state'] == 'succeeded'

    asyncio.run(run())


@pytest.mark.parametrize(('unit', 'frequency', 'valid'), [
    ('kg', '周', True), ('吨', '周', False), ('kg', '日', False), ('kg', None, False), (None, '周', False),
])
def test_quantitative_recommendation_requires_compatible_unit_and_frequency(unit, frequency, valid):
    quote = 'Measured stock 连续2周高于 12 kg。'
    preview = {'text': quote}
    item = {'kind': 'falsifiers', 'value': {'expression': 'value > 12', 'consecutive_periods': 2},
            'source_quote': quote, 'path': ['falsifiers', 0], 'section': 'falsifiers'}
    assert compatible(preview, item, {'id': 'synthetic-stock', 'unit': unit, 'frequency': frequency}, ('kg', '吨')) is valid
