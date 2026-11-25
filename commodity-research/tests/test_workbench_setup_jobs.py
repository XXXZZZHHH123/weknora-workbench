"""Offline orchestration and owner-scoped handoff checks for weekly reports."""
import asyncio
from contextlib import contextmanager
from copy import deepcopy
from dataclasses import replace
from datetime import datetime, timezone
from types import SimpleNamespace

from fastapi import FastAPI, HTTPException
import httpx
import pytest
from sqlalchemy import select

from tests.chart_fixtures import definition
from tin.config import settings
from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, artifact_session, current_workbench
from tin.models import AuditLog, DiagramTemplate, Judgment, SourceMapping
from tin.models.workbench import WorkbenchArtifact
from tin.web import analysis_jobs, judgment_generation, judgment_jobs, report_chart_import, workbench_setup as setup
from tin.web import workbench_setup_jobs as jobs
from tin.web.judgment_job_registry import Registry

WB = '10000000-0000-4000-8000-000000000683'
SERIES = 'kb-' + 'a' * 48
CHART_SERIES = 'kb-' + 'b' * 48
TEXT = '过去观察到本期采购和销售发生变化。'


@contextmanager
def active(scope):
    owner = current_identity.set(scope.identity)
    board = current_workbench.set(scope)
    try:
        yield
    finally:
        current_workbench.reset(board)
        current_identity.reset(owner)


def body(*parts):
    return setup.SetupInput(model_id='fixture-model', parts=list(parts), source={
        'kind': 'knowledge_file', 'knowledge_id': 'report-1', 'source_binding_id': 'binding-1',
        'file_version': 'a' * 64})


@pytest.fixture
def workbench(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://synthetic.invalid')
    registry = Registry(tmp_path / 'analysis-index.sqlite')
    monkeypatch.setattr(judgment_jobs, 'registry', registry)
    monkeypatch.setattr(analysis_jobs, 'registry', registry)
    monkeypatch.setattr(analysis_jobs, 'runtime', analysis_jobs.Runtime())
    state = {'revision': 1, 'edited_report': False, 'model_failure': False, 'phases': [], 'model_calls': []}
    fragments = [{'id': 'chunk1', 'index': 0, 'page': 1, 'content': TEXT}]
    report = {'filename': '样例周报.pptx', 'knowledge_id': 'report-1', 'source_binding_id': 'binding-1',
              'file_version': 'a' * 64, 'provenance': [], 'locator_digest': 'positions',
              'fragments': fragments, 'batches': [fragments]}
    source = {'id': 'binding-1', 'source_type': 'knowledge_base', 'source_id': 'fixture-kb',
              'knowledge_base_id': 'fixture-kb', 'tenant_id': 68, 'kind': 'primary',
              'permission': 'editor', 'delegated': False, 'view_modules': ['home', 'indicators']}
    identity = Identity('owner', 'owner@fixture.invalid', 'Researcher', 68, 'owner')
    scope = WorkbenchScope(identity, WB, 'fixture-kb', 68, {'workspace_mode': 'isolated', 'can_edit': True,
                           'can_read_source': True}, 'Bearer synthetic', scope_version=2, sources_revision=1,
                           sources=(source,))
    fields = {SERIES: {'id': SERIES, 'series_id': SERIES, 'metric': '研究指标', 'name': '研究指标',
                       'sheet': '调查', 'unit': '吨', 'frequency': '周'}}

    async def resolve(identity, workbench_id, authorization):
        assert (workbench_id, authorization) == (scope.workbench_id, scope.authorization)
        return replace(scope, identity=identity, sources_revision=state['revision'])

    async def read_report(scope, citation):
        assert citation.source_binding_id == 'binding-1'
        assert current_identity.get() == scope.identity and current_workbench.get() == scope
        return deepcopy(report)

    async def verify(scope, citation, original):
        assert original == setup.original_identity(report)
        if state['edited_report']:
            raise HTTPException(409, '解析正文变更')

    async def digest(scope, binding, knowledge, version):
        assert (binding, knowledge, version) == ('binding-1', 'report-1', 'a' * 64)
        return {'supported': True, 'text': TEXT, 'sha256': 'content', 'provenance': [],
                'knowledge_id': knowledge, 'filename': report['filename'], 'content_kind': 'parsed_fragments'}

    async def complete(scope, model_id, prompt, *, purpose=None):
        assert model_id == 'fixture-model'
        state['model_calls'].append(purpose)
        if purpose == 'dimension_inference':
            if state['model_failure']:
                raise HTTPException(502, '模型不可用')
            assert TEXT in prompt
            return {'dimensions': [{'label': '原料环节', 'category': '供给',
                                    'sections': [{'label': '采购'}]}]}
        if purpose == 'core_focus_sheets':
            assert TEXT in prompt
            return {'worksheets': [{'worksheet_id': 'w1', 'group': '供给'}]}
        if purpose == 'core_focus_fields':
            assert TEXT in prompt
            item = {'series_id': SERIES, 'group': '供给', 'reason': '持续跟踪采购变化'}
            return {'items': [item, {**item, 'series_id': 'unknown'}] if state.get('invalid_focus_id') else [item]}
        if purpose == 'workbench_setup_report_excerpts':
            return {'quotes': [TEXT]}
        raise AssertionError(purpose)

    touch = registry.touch

    def track(job_id, worker, fence, phase=None):
        if phase:
            state['phases'].append(phase)
        touch(job_id, worker, fence, phase)

    monkeypatch.setattr(registry, 'touch', track)
    monkeypatch.setattr(judgment_jobs, 'resolve_workbench', resolve)
    monkeypatch.setattr(report_chart_import, 'resolve_workbench', resolve)
    monkeypatch.setattr(report_chart_import, 'source_status_hint', lambda scope: None)
    monkeypatch.setattr(setup.report_chart_suggestions, 'read_report', read_report)
    monkeypatch.setattr(setup.report_chart_suggestions, 'verify_report', verify)
    monkeypatch.setattr(setup.strategy_citations, 'read_structure_digest', digest)
    monkeypatch.setattr(setup.strategy_text, 'completion', complete)
    monkeypatch.setattr(setup.focus_suggestions, 'catalog_snapshot', lambda scope: {'catalog': fields})
    return SimpleNamespace(scope=scope, registry=registry, state=state, fields=fields, report=report)


async def completed(workbench, *parts):
    job = await jobs.start_job(workbench.scope, body(*parts), 'fixture-setup-idempotency-' + '-'.join(parts))
    assert job['state'] == 'queued' and job['purpose'] == 'workbench_setup'
    await analysis_jobs.runtime.tasks[job['id']]
    status = await analysis_jobs.status(workbench.scope, job['id'])
    assert status['state'] == 'succeeded', status
    return job['id'], await jobs.job_result(workbench.scope, job['id'])


@pytest.mark.parametrize(('saved', 'draft'), [(False, True), (True, True), (False, False)])
def test_setup_judgment_uses_saved_dimensions_then_same_task_draft_then_defaults(workbench, monkeypatch, saved, draft):
    import re

    from tests.test_judgment_generation import TEXT as REVIEW_TEXT, document, model_fixture
    from tin.integration import kb_facts
    from tin.web.judgment_review import stale_review_dimensions
    from tin.web.research_dimensions import DIMENSIONS_KEY, read_dimensions
    from tin.web.restricted_views import mapping_key

    response = document()
    fields = response['judgment']
    evidence, rule = fields['contradiction']['bull']['evidence'][0], fields['thresholds'][0]
    evidence['series_id'] = rule['series_id'] = fields['marginal_focus'][0]['series_id'] = SERIES
    fields['whitelist'] = [SERIES]
    response['binding_units'] = {SERIES: '吨'}
    if saved:
        with artifact_session(workbench.scope) as session:
            session.add(SourceMapping(id=mapping_key(session, DIMENSIONS_KEY), version=1, confirmed_by='owner',
                confirmed_at=datetime.now(timezone.utc),
                config={'dimensions': [{'id': 'saved-flow', 'label': '已存维度', 'category': '其他',
                                        'description': ''}]}))
            session.commit()

    async def historical(self, purpose, maximum):
        return REVIEW_TEXT

    async def current_model(scope, model):
        return 'fixture-model-version'

    async def complete(scope, model, prompt):
        if not prompt.startswith('research-source-inventory.v1'):
            if saved:
                assert 'saved-flow（已存维度' in prompt and '原料环节' not in prompt
                tag = 'saved-flow'
            elif draft:
                tag = re.search(r'- (d-[a-f0-9]+)（原料环节', prompt).group(1)
                assert 'supply（供给' not in prompt
            else:
                assert 'supply（供给' in prompt
                tag = 'supply'
            evidence['dimension'] = rule['dimension'] = tag
        return model_fixture(prompt, response)

    monkeypatch.setattr(setup.Setup, 'historical_text', historical)
    monkeypatch.setattr(judgment_generation, 'resolve_workbench', judgment_jobs.resolve_workbench)
    monkeypatch.setattr(judgment_generation, 'current_model', current_model)
    monkeypatch.setattr(judgment_generation, 'completion', complete)
    monkeypatch.setattr(kb_facts, 'catalog_snapshot', lambda scope: {
        'version': 'fixture-fact-version', 'catalog': workbench.fields, 'datasets': []})

    async def run():
        identifier, result = await completed(workbench, *(['dimensions', 'judgment'] if draft else ['judgment']))
        row = result['items'][-1]
        assert row['state'] == 'succeeded', row
        config = jobs.load_config(workbench.scope, identifier)
        preview = jobs.load_judgment(workbench.scope, config['result']['_judgment_token'])
        expected = 'saved-flow' if saved else result['items'][0]['data']['dimensions'][0]['id'] if draft else 'supply'
        assert {item['value']['dimension'] for item in preview['review']['items'] if
                item['kind'] == 'thresholds' or item['path'][:3] == ['contradiction', 'bull', 'evidence']} == {expected}
        if draft and not saved:
            dimensions = result['items'][0]['data']['dimensions']
            with artifact_session(workbench.scope) as session:
                stale = stale_review_dimensions(preview, read_dimensions(session))
                assert len(stale) == 2 and all('维度「原料环节」已不在本研究台' in reason for _, reason in stale)
                session.add(SourceMapping(id=mapping_key(session, DIMENSIONS_KEY), version=1,
                    confirmed_by='owner', confirmed_at=datetime.now(timezone.utc), config={'dimensions': dimensions}))
                session.commit()
            with artifact_session(workbench.scope) as session:
                assert not stale_review_dimensions(preview, read_dimensions(session))

    asyncio.run(run())


def test_selected_steps_have_fixed_phases_and_publish_only_private_candidates(workbench, monkeypatch):
    async def diagram(body, *, series_ids):
        assert TEXT in body.instructions and '历史' in body.instructions and series_ids == {SERIES}
        return {'markdown': '# fixture', 'nodes': 4, 'version': 'fixture-facts'}

    monkeypatch.setattr(setup.diagram_generation, 'generate', diagram)
    monkeypatch.setattr(setup.dimension_suggestions, 'dimensions_for_scope', lambda scope: [
        {'sections': [{'items': [{'kind': 'indicator', 'id': SERIES}, {'kind': 'chart', 'id': 'saved-chart'}]}]}])

    async def run():
        identifier, result = await completed(workbench, 'diagram', 'focus', 'dimensions')
        assert result['report'] == {'knowledge_id': 'report-1', 'name': '样例周报.pptx'}
        assert workbench.state['phases'] == ['reading_source', 'dimensions', 'focus', 'diagram']
        assert workbench.state['model_calls'] == ['dimension_inference', 'core_focus_sheets', 'core_focus_fields']
        assert [(row['part'], row['state'], row['counts']) for row in result['items']] == [
            ('dimensions', 'succeeded', {'dimensions': 1, 'sections': 1}),
            ('focus', 'succeeded', {'suggestions': 1}), ('diagram', 'succeeded', {'nodes': 4})]
        assert workbench.registry.latest(workbench.scope.identity, WB, jobs.PURPOSE)['id'] == identifier
        with artifact_session(workbench.scope) as session:
            assert session.scalars(select(WorkbenchArtifact)).all() == []
            assert session.scalars(select(Judgment)).all() == []
            assert session.scalars(select(DiagramTemplate)).all() == []
            assert [row.config['kind'] for row in session.scalars(select(SourceMapping))] == [analysis_jobs.KIND]
            audit = list(session.scalars(select(AuditLog).order_by(AuditLog.id)))
            assert [row.target_id for row in audit] == [identifier, identifier]
            assert all(TEXT not in str(row.detail) for row in audit)
        app = FastAPI()
        app.include_router(analysis_jobs.router)
        app.include_router(judgment_jobs.global_router)
        base = '/api/sn/analysis-jobs'
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app), base_url='http://test.invalid') as client:
            with active(workbench.scope):
                latest = await client.get(base + '/latest', params={'purpose': jobs.PURPOSE})
                assert latest.json()['job']['id'] == identifier
                fetched = await client.get(f'{base}/{identifier}/result')
                assert fetched.status_code == 200
                assert fetched.json()['items'] == result['items'] and '_source_identity' not in fetched.json()
                dismissed = await client.post(f'/api/research/analysis-jobs/{identifier}/dismiss')
                assert dismissed.json()['dismissed'] is True
    asyncio.run(run())


def test_failure_blocks_dependent_indicators_and_independent_steps_run(workbench):
    workbench.state['model_failure'] = True

    async def run():
        _, result = await completed(workbench, 'focus', 'indicators', 'dimensions')
        assert [(row['part'], row['state']) for row in result['items']] == [
            ('dimensions', 'failed'), ('indicators', 'blocked'), ('focus', 'succeeded')]
        assert result['items'][0]['message'] and result['items'][0]['detail'] == '模型不可用'
        assert result['items'][1]['message']
        assert workbench.state['phases'] == ['reading_source', 'dimensions', 'focus']
    asyncio.run(run())


def test_diagram_uses_current_step_bindings_and_partially_mapped_chart_series(workbench, monkeypatch):
    async def indicators(scope, model_id, dimensions):
        return {'items': [{'series_id': SERIES}], 'sections': [{'selected_count': 1}],
                'ignored_count': 2, 'ignored_reason': '模型返回 2 个不在候选目录的指标，已忽略'}

    async def charts(scope, body, job_id, *, report):
        return {'_original': setup.original_identity(report), 'charts': [
            {'missing_indicators': True, 'series': [{'selected_id': CHART_SERIES}, {'selected_id': None}]},
            {'definition': definition(curves=[{'series_id': SERIES, 'label': '研究指标'}]),
             'series': [{'selected_id': SERIES}]}]}

    async def diagram(body, *, series_ids):
        assert series_ids == {SERIES, CHART_SERIES}
        return {'nodes': 4}

    def no_fallback(scope):
        pytest.fail('已有本次候选时无需读取已保存的维度绑定')

    workbench.state['invalid_focus_id'] = True
    monkeypatch.setattr(setup.dimension_suggestions, 'generate_suggestions', indicators)
    monkeypatch.setattr(setup.dimension_suggestions, 'dimensions_for_scope', no_fallback)
    monkeypatch.setattr(setup.report_chart_import, 'recognize_report', charts)
    monkeypatch.setattr(setup.diagram_generation, 'generate', diagram)

    async def run():
        _, result = await completed(workbench, 'dimensions', 'indicators', 'charts', 'focus', 'diagram')
        _, bindings, _, focus, diagram_item = result['items']
        assert all(row['state'] == 'succeeded' for row in result['items'])
        assert bindings['counts']['ignored_count'] == 2
        assert focus['counts']['ignored_count'] == 1
        assert diagram_item['counts'] == {'nodes': 4}
    asyncio.run(run())


def test_diagram_skips_without_current_or_saved_bindings(workbench, monkeypatch):
    async def no_diagram(*args, **kwargs):
        pytest.fail('空指标范围无需调用产业图生成')

    monkeypatch.setattr(setup.diagram_generation, 'generate', no_diagram)

    async def run():
        _, result = await completed(workbench, 'dimensions', 'diagram')
        assert result['items'][0]['state'] == 'succeeded'
        assert result['items'][1] == {'part': 'diagram', 'state': 'skipped', 'counts': {},
                                      'message': '跳过', 'detail': '没有可用于产业图的指标'}
        assert workbench.state['phases'] == ['reading_source', 'dimensions', 'diagram']
    asyncio.run(run())


def test_setup_diagram_expands_only_post_generation_recommendation_catalog(workbench, monkeypatch):
    generation = setup.diagram_generation
    markdown = '# Flow\n\n## Supply\n\n- Deliveries <!-- id: node1 -->\n'
    base_version, all_sources_version = 'report.fixture', 'catalog.fixture'

    async def generate_diagram(body, *, series_ids):
        assert series_ids == {SERIES}
        return {'markdown': markdown, 'nodes': 1, 'version': base_version, 'bindings': [],
                'model_id': body.model_id}

    async def recommend(scope, model, title, nodes, shared_ids, progress):
        assert title == 'Flow' and nodes[0].label == 'Deliveries' and shared_ids == []
        candidate = {'series_id': CHART_SERIES, 'name': 'Delivery volume', 'reason': 'Same measurement'}
        return {nodes[0].id: {'recommended': candidate, 'recommendations': [candidate], 'ambiguous': False}}, all_sources_version

    async def check(scope, version):
        assert version == all_sources_version

    monkeypatch.setattr(generation, 'generate', generate_diagram)
    monkeypatch.setattr(generation, 'recommend_nodes', recommend)
    monkeypatch.setattr(generation, 'verify_generation_scope', check)
    monkeypatch.setattr(setup.dimension_suggestions, 'dimensions_for_scope', lambda scope: [
        {'sections': [{'items': [{'kind': 'indicator', 'id': SERIES}]}]}])

    async def run():
        _, result = await completed(workbench, 'diagram')
        data = result['items'][0]['data']
        assert data['version'] == base_version and data['markdown'] == markdown
        assert data['indicator_recommendations']['node1']['recommended']['series_id'] == CHART_SERIES
        with artifact_session(workbench.scope) as session:
            assert session.scalars(select(DiagramTemplate)).all() == []

    asyncio.run(run())


def test_diagram_reports_exact_scoped_catalog_capacity_before_any_model_call(workbench, monkeypatch):
    generation = setup.diagram_generation
    fields = [{'id': 'kb-' + format(i, '048x'), 'name': '相同长度'} for i in range(6)]

    async def charts(scope, body, job_id, *, report):
        return {'_original': setup.original_identity(report), 'charts': [
            {'series': [{'selected_id': row['id']} for row in fields[:5]]}]}

    async def no_model(*args, **kwargs):
        pytest.fail('超过容量的目录不进入生成流程')

    def scoped_catalog(scope, series_ids):
        return 'report.fixture', [row for row in fields if row['id'] in series_ids]

    monkeypatch.setattr(setup.report_chart_import, 'recognize_report', charts)
    monkeypatch.setattr(generation, 'report_catalog_snapshot', scoped_catalog)
    monkeypatch.setattr(generation, 'CATALOG_BYTES', len(generation.catalog_json(fields[:1]).encode()))
    monkeypatch.setattr(generation, 'CATALOG_MAX_BATCHES', 4)
    monkeypatch.setattr(generation, 'current_model', no_model)

    async def run():
        _, result = await completed(workbench, 'charts', 'diagram')
        assert result['items'][1] == {'part': 'diagram', 'state': 'failed', 'counts': {},
            'message': '资料或生成结果超过处理容量', 'detail': '本次识别出的指标有 5 个，超过产业图生成上限 4 个'}
    asyncio.run(run())


@pytest.mark.parametrize(('detail', 'expected'), [
    ('原' * 121, '原' * 120),
    ({'message': '研究规则数字超出当前存储精度', 'issues': [{'path': '/private/rule'}]}, '研究规则数字超出当前存储精度'),
    ('模型服务失败：Bearer synthetic-token', '模型服务失败'),
    ('模型服务失败：api_key=fixture-value', '模型服务失败'),
    ('模型服务失败：sk-synthetic', '模型服务失败'),
    ('模型服务失败：/private/config', '模型服务失败'),
    ('模型服务失败：C:\\private\\config', '模型服务失败'),
    ('模型服务失败：relative/config.py', '模型服务失败'),
    ('模型服务失败。File "config.py", line 13, in call', '模型服务失败'),
    ('模型服务失败\nTraceback (most recent call last):\n详见配置', '模型服务失败'),
    ('The model failed', ''),
    (['非文本明细'], ''),
])
def test_step_http_detail_is_bounded_and_excludes_private_diagnostics(detail, expected):
    assert setup.result_detail(HTTPException(422, detail)) == expected


def test_monitor_step_retains_reasons_when_all_candidates_are_rejected(workbench, monkeypatch):
    failures = [{'index': 1, 'reason': '每个关注点须包含 1 至 6 条规则'},
                {'index': 2, 'reason': '建议阈值缺少明确说明或确认项'}]

    async def candidates(scope, sentence, model_id):
        raise setup.monitors.ReportMonitorCandidatesError(failures)

    monkeypatch.setattr(setup.monitors, 'draft_candidates_from_report', candidates)

    async def run():
        _, result = await completed(workbench, 'monitors')
        item, = result['items']
        assert item['state'] == 'failed' and item['failures'] == failures
        assert '全部 2 个监测候选未通过校验' in item['detail']
    asyncio.run(run())


def test_scope_owner_report_revision_and_write_grant_are_required(workbench):
    async def run():
        readonly = replace(workbench.scope, metadata={'can_edit': False, 'can_read_source': True})
        with pytest.raises(HTTPException) as denied:
            await jobs.start_job(readonly, body('focus'), 'fixture-setup-no-write')
        assert denied.value.status_code == 403
        assert not workbench.registry.listing(workbench.scope.identity)
        identifier, _ = await completed(workbench, 'focus')
        workbench.state['revision'] = 2
        for granted in (workbench.scope, replace(workbench.scope, sources_revision=2)):
            with pytest.raises(HTTPException) as rejected:
                await jobs.job_result(granted, identifier)
            assert rejected.value.status_code == 409
        workbench.state.update(revision=1, edited_report=True)
        with pytest.raises(HTTPException) as edited:
            await jobs.job_result(workbench.scope, identifier)
        assert edited.value.status_code == 409
        other = replace(workbench.scope, identity=replace(workbench.scope.identity, user_id='other'))
        with pytest.raises(HTTPException) as foreign:
            await jobs.job_result(other, identifier)
        assert foreign.value.status_code == 404
    asyncio.run(run())


def test_chart_counts_and_prefill_use_the_same_private_setup_result(workbench, monkeypatch):
    ready, pending, invalid = [
        {'id': name, 'title': name, 'chart_type': 'trend', 'series': [{'selected_id': SERIES}]}
        for name in ('mapped', 'pending', 'invalid')]

    async def recognize(scope, model_id, batch, *, catalog):
        assert batch[0]['content'] == TEXT and catalog == workbench.fields
        return {'charts': deepcopy([ready, pending, invalid]), 'failed': [{'reason': '样例识别失败'}]}

    def annotate(scope, charts, group, identifier):
        charts[0]['definition'] = definition(curves=[{'series_id': SERIES, 'label': '研究指标'}])
        charts[1]['missing_indicators'] = True
        charts[2]['definition_failed'] = True
        return charts

    monkeypatch.setattr(setup.report_chart_suggestions, 'recognize_batch', recognize)
    monkeypatch.setattr(report_chart_import, 'annotate', annotate)
    monkeypatch.setattr(report_chart_import, 'catalog_snapshot', lambda scope: {'catalog': workbench.fields})

    async def run():
        identifier, result = await completed(workbench, 'charts')
        row, = result['items']
        assert row['counts'] == {'ready': 1, 'needs_mapping': 1, 'failed': 2}
        assert row['data']['source']['source_binding_id'] == 'binding-1'
        assert row['data']['job_id'] == identifier
        assert [job['id'] for job in workbench.registry.listing(workbench.scope.identity)] == [identifier]
        with active(workbench.scope):
            review, purpose = await report_chart_import.verified_state(workbench.scope, identifier)
        assert purpose == 'workbench_setup'
        assert all(review[key] == row['data'][key] for key in ('source', 'job_id', 'failures'))
        assert row['data']['applied'] == {} and row['data']['source_status_hint'] is None
        assert [chart['id'] for chart in review['charts']] == ['b1-mapped', 'b1-pending', 'b1-invalid']
        with artifact_session(workbench.scope) as session:
            assert session.scalars(select(WorkbenchArtifact)).all() == []
            assert len(session.scalars(select(SourceMapping)).all()) == 1
        with workbench.registry.transaction() as connection:
            meta = workbench.registry.fetch(connection, identifier)
            workbench.registry.write(connection, {**meta, 'expires_unix': 0})
        with active(workbench.scope), pytest.raises(HTTPException) as expired:
            await report_chart_import.verified_state(workbench.scope, identifier)
        assert expired.value.status_code == 410
    asyncio.run(run())


def test_judgment_uses_native_durable_review_and_monitor_is_private_until_review(workbench, monkeypatch):
    async def analyze(scope, request):
        assert request.source_name == '样例周报.pptx' and TEXT in request.text and '当前判断' in request.text
        return {'review': {'revision': 1}, 'text': request.text}, {}

    async def candidates(scope, sentence, model_id):
        assert TEXT in sentence
        draft = {'title': '监测草稿', 'sentence': sentence, 'rules': [{'series_id': SERIES}], 'candidates': []}
        return {'candidates': [draft, {**draft, 'title': '另一监测草稿'}],
                'failures': [{'index': 2, 'reason': '监测规则超过上限'}]}

    monkeypatch.setattr(judgment_generation, '_analyze', analyze)
    monkeypatch.setattr(judgment_generation, 'snapshot', lambda: {'base_version': 0})
    monkeypatch.setattr(setup.monitors, 'draft_candidates_from_report', candidates)

    async def run():
        identifier, result = await completed(workbench, 'judgment', 'monitors')
        assert result['items'][0]['review_available'] and 'data' not in result['items'][0]
        assert result['items'][1]['counts'] == {'candidates': 2, 'rules': 2, 'ignored_count': 1}
        assert result['items'][1]['data']['failures'] == [{'index': 2, 'reason': '监测规则超过上限'}]
        with artifact_session(workbench.scope) as session:
            assert session.scalars(select(WorkbenchArtifact)).all() == []
        token = jobs.load_config(workbench.scope, identifier)['result']['_judgment_token']
        preview = judgment_generation.previews.get(workbench.scope, token)
        preview['review']['revision'] = 2
        judgment_jobs.atomic_replace(workbench.scope, token, preview, 1)
        assert judgment_generation.previews.get(workbench.scope, token)['review']['revision'] == 2
        with pytest.raises(HTTPException) as stale:
            judgment_jobs.atomic_replace(workbench.scope, token, preview, 1)
        assert stale.value.status_code == 409
        with active(workbench.scope):
            first = await jobs.monitor_preview(identifier)
            second = await jobs.monitor_preview(identifier)
            another = await jobs.monitor_preview(identifier, candidate=1)
            with pytest.raises(HTTPException) as missing:
                await jobs.monitor_preview(identifier, candidate=2)
        assert first['draft_id'] == second['draft_id']
        assert first['draft_id'] != another['draft_id'] and another['title'] == '另一监测草稿'
        assert first['setup_titles'] == ['监测草稿', '另一监测草稿'] and missing.value.status_code == 404
        with artifact_session(workbench.scope) as session:
            drafts = session.scalars(select(WorkbenchArtifact)).all()
            assert {draft.id for draft in drafts} == {first['draft_id'], another['draft_id']}
            assert all(draft.module == setup.monitors.DRAFT_MODULE and draft.status == 'draft' for draft in drafts)
            jobs.close_judgment(session, workbench.scope, token, 2)
            session.commit()
        assert (await jobs.job_result(workbench.scope, identifier))['items'][0]['review_available'] is False
    asyncio.run(run())
