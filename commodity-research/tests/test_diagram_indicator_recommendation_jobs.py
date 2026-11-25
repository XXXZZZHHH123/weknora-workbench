"""The editor retrieves bounded, owner-scoped drafts without saving a layout."""
import asyncio
from copy import deepcopy
from datetime import datetime, timezone
import json

from fastapi import FastAPI
import httpx
from sqlalchemy import select

from tests.test_diagram_background_jobs import SERIES, active, case
from tin.integration import kb_facts, page_facts
from tin.integration.workbench_scope import artifact_session
from tin.models import AuditLog, DiagramTemplate
from tin.web import analysis_jobs, app as web_app, diagram_generation as generation


ADDITIONAL_ID = 'kb-' + '7' * 48


def test_editor_job_recalls_from_full_catalog_and_saves_only_after_review(case, monkeypatch):
    monkeypatch.setattr(analysis_jobs, 'registry', case.registry)
    monkeypatch.setattr(analysis_jobs, 'runtime', analysis_jobs.Runtime())
    generation.register_recommendation_jobs()
    source = 'source-binding'
    by_id = {row['id']: {**row, 'series_id': row['id'], 'source_binding_id': source}
             for row in [*case.catalog, {'id': ADDITIONAL_ID, 'name': 'Regional delivery volume',
                'sheet': 'Measurement directory', 'unit': 'kg', 'frequency': '周'}]}

    def directory(scope):
        return {'version': case.state['version'], 'catalog': deepcopy(by_id)}

    async def complete(scope, model, prompt):
        if prompt.startswith('research-catalog-recall.v1'):
            assert '目录用语概览' in prompt and 'Measurement directory' in prompt
            return {'queries': {'node1': ['Regional delivery volume']}}
        assert '本研究台已使用的指标' in prompt
        candidates = json.loads(prompt.split('\n候选指标：', 1)[1].split('\n完整原文：', 1)[0])
        selected = next(ref for ref, row in candidates.items() if row['name'] == by_id[ADDITIONAL_ID]['name'])
        return {'recommendations': {'node1': {'ambiguous': False, 'candidates': [
            {'ref': selected, 'reason': '与节点要求的交付统计口径对应'}]}}}

    def observations(scope, identifiers, module, **options):
        assert identifiers == [ADDITIONAL_ID] and module == 'diagram'
        assert options['points'] == 1 and options['direct_only'] is True
        return {'rows': [{'series_id': ADDITIONAL_ID}]}

    monkeypatch.setattr(kb_facts, 'catalog_snapshot', directory)
    monkeypatch.setattr(generation, 'completion', complete)
    monkeypatch.setattr(page_facts, 'bounded_facts', observations)
    monkeypatch.setattr(web_app, '_page_session', lambda *args, **kwargs: artifact_session(case.first))

    async def exercise():
        api = FastAPI()
        api.include_router(generation.router)
        api.include_router(analysis_jobs.router)
        api.post('/api/sn/diagram/templates')(web_app.diagram_save)
        with active(case.first), artifact_session(case.first) as session:
            now = datetime.now(timezone.utc)
            template = DiagramTemplate(name='Fixture', variety='SN', layout={'nodes': [], 'edges': []},
                                       created_at=now, updated_at=now)
            session.add(template)
            session.flush()
            template_id = template.id
            session.commit()
        request = {'model_id': 'synthetic-vision', 'template_id': template_id, 'title': 'Fixture',
                   'nodes': [{'id': 'node1', 'label': 'Regional deliveries'}], 'shared_ids': [SERIES]}
        try:
            async with httpx.AsyncClient(transport=httpx.ASGITransport(app=api), base_url='http://fixture') as client:
                with active(case.first):
                    url = '/api/sn/diagram/generation/indicator-recommendations'
                    response = await client.post(url, json=request, headers={'Idempotency-Key': 'fixture-diagram-indicators'})
                    assert response.status_code == 202, response.text
                    job_id = response.json()['id']
                    await asyncio.wait_for(analysis_jobs.runtime.tasks[job_id], 5)
                    url = '/api/sn/analysis-jobs/' + job_id
                    status = await client.get(url)
                    assert status.json()['state'] == 'succeeded', status.text
                    assert status.json()['context']['template_id'] == template_id
                    result = await client.get(url + '/result')
                    assert result.status_code == 200, result.text
                    reviewed = result.json()
                    assert reviewed['items']['node1']['recommended']['series_id'] == ADDITIONAL_ID
                    with artifact_session(case.first) as session:
                        assert session.get(DiagramTemplate, template_id).layout['nodes'] == []
                    binding = {'kind': 'series', 'series_id': ADDITIONAL_ID}
                    save = {'id': template_id, 'name': 'Fixture', 'recommendation_version': reviewed['version'],
                            'layout': {'nodes': [{'id': 'node1', 'label': 'Regional deliveries', 'x': 0, 'y': 0,
                                'w': 140, 'h': 80, 'binding': binding}], 'edges': []}}
                    confirmed = await client.post('/api/sn/diagram/templates', json=save)
                    assert confirmed.status_code == 200, confirmed.text
                    assert confirmed.json()['saved']['layout']['nodes'][0]['binding'] == binding
                with active(case.scope('another-researcher')):
                    assert (await client.get(url + '/result')).status_code == 404
                with active(case.first):
                    case.state['version'] = 'b' * 64
                    assert (await client.get(url + '/result')).status_code == 409
                    assert (await client.post('/api/sn/diagram/templates', json=save)).status_code == 409
                assert (await client.post('/api/sn/diagram/templates', json=save)).status_code == 409
        finally:
            await analysis_jobs.runtime.shutdown()
        with active(case.first), artifact_session(case.first) as session:
            assert session.get(DiagramTemplate, template_id).layout['nodes'][0]['binding']['series_id'] == ADDITIONAL_ID
            audits = session.scalars(select(AuditLog).where(AuditLog.target_type == generation.RECOMMENDATION_PURPOSE)).all()
            assert {entry.action for entry in audits} == {'提交产业图指标推荐', '完成产业图指标推荐'}

    asyncio.run(exercise())
