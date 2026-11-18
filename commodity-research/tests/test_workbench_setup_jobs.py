"""Offline orchestration and owner-scoped handoff checks for weekly reports."""
import asyncio
from contextlib import contextmanager
from copy import deepcopy
from dataclasses import replace
from types import SimpleNamespace

from fastapi import HTTPException
import pytest
from sqlalchemy import select

from tin.config import settings
from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, artifact_session, current_workbench
from tin.models import AuditLog, DiagramTemplate, Judgment, SourceMapping
from tin.models.workbench import WorkbenchArtifact
from tin.web import judgment_generation, judgment_jobs, report_chart_import, workbench_setup as setup
from tin.web import workbench_setup_jobs as jobs
from tin.web.judgment_job_registry import Registry

WB = '10000000-0000-4000-8000-000000000683'
SERIES = 'kb-' + 'a' * 48
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
    monkeypatch.setattr(jobs, 'registry', registry)
    monkeypatch.setattr(jobs, 'runtime', jobs.Runtime())
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
            return {'items': [{'series_id': SERIES, 'group': '供给', 'reason': '持续跟踪采购变化'}]}
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
    monkeypatch.setattr(setup.report_chart_suggestions, 'read_report', read_report)
    monkeypatch.setattr(setup.report_chart_suggestions, 'verify_report', verify)
    monkeypatch.setattr(setup.strategy_citations, 'read_structure_digest', digest)
    monkeypatch.setattr(setup.strategy_text, 'completion', complete)
    monkeypatch.setattr(setup.focus_suggestions, 'catalog_snapshot', lambda scope: {'catalog': fields})
    return SimpleNamespace(scope=scope, registry=registry, state=state, fields=fields, report=report)


async def completed(workbench, *parts):
    job = await jobs.start_job(workbench.scope, body(*parts), 'fixture-setup-idempotency-' + '-'.join(parts))
    assert job['state'] == 'queued' and job['purpose'] == 'workbench_setup'
    await jobs.runtime.tasks[job['id']]
    status = await jobs.job_status(workbench.scope, job['id'])
    assert status['state'] == 'succeeded', status
    return job['id'], await jobs.job_result(workbench.scope, job['id'])


def test_selected_steps_have_fixed_phases_and_publish_only_private_candidates(workbench, monkeypatch):
    async def diagram(body):
        assert TEXT in body.instructions and '历史' in body.instructions
        return {'markdown': '# fixture', 'nodes': 4, 'version': 'fixture-facts'}

    monkeypatch.setattr(setup.diagram_generation, 'generate', diagram)

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
            assert [row.config['kind'] for row in session.scalars(select(SourceMapping))] == [jobs.KIND]
            audit = list(session.scalars(select(AuditLog).order_by(AuditLog.id)))
            assert [row.target_id for row in audit] == [identifier, identifier]
            assert all(TEXT not in str(row.detail) for row in audit)
    asyncio.run(run())


def test_failure_blocks_dependent_indicators_and_independent_steps_run(workbench):
    workbench.state['model_failure'] = True

    async def run():
        _, result = await completed(workbench, 'focus', 'indicators', 'dimensions')
        assert [(row['part'], row['state']) for row in result['items']] == [
            ('dimensions', 'failed'), ('indicators', 'blocked'), ('focus', 'succeeded')]
        assert result['items'][0]['message'] and result['items'][1]['message']
        assert workbench.state['phases'] == ['reading_source', 'dimensions', 'focus']
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


def test_chart_counts_and_child_prefill_use_original_recognition_and_setup_expiry(workbench, monkeypatch):
    ready, pending, invalid = [
        {'id': name, 'title': name, 'chart_type': 'trend', 'series': [{'selected_id': SERIES}]}
        for name in ('mapped', 'pending', 'invalid')]

    async def recognize(scope, model_id, batch, *, catalog):
        assert batch[0]['content'] == TEXT and catalog == workbench.fields
        return {'charts': deepcopy([ready, pending, invalid]), 'failed': [{'reason': '样例识别失败'}]}

    def annotate(scope, charts, group, identifier):
        charts[0]['definition'] = {'curves': [{'series_id': SERIES}]}
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
        meta = workbench.registry.get(workbench.scope.identity, identifier, WB)
        chart_job_id = row['data']['job_id']
        private = report_chart_import.load_state(workbench.scope, chart_job_id)
        assert private['expires'] == meta['expires_unix']
        with active(workbench.scope):
            review = await report_chart_import.job(chart_job_id)
        assert [chart['id'] for chart in review['charts']] == ['b1-mapped', 'b1-pending', 'b1-invalid']
        with artifact_session(workbench.scope) as session:
            assert session.scalars(select(WorkbenchArtifact)).all() == []
    asyncio.run(run())


def test_judgment_uses_native_durable_review_and_monitor_is_private_until_review(workbench, monkeypatch):
    async def analyze(scope, request):
        assert request.source_name == '样例周报.pptx' and TEXT in request.text and '当前判断' in request.text
        return {'review': {'revision': 1}, 'text': request.text}, {}

    async def candidate(scope, sentence, model_id, *, persist):
        assert not persist and TEXT in sentence
        return {'title': '监测草稿', 'sentence': sentence, 'rules': [{'series_id': SERIES}], 'candidates': []}

    monkeypatch.setattr(judgment_generation, '_analyze', analyze)
    monkeypatch.setattr(judgment_generation, 'snapshot', lambda: {'base_version': 0})
    monkeypatch.setattr(setup.monitors, 'draft_from_text', candidate)

    async def run():
        identifier, result = await completed(workbench, 'judgment', 'monitors')
        assert result['items'][0]['review_available'] and 'data' not in result['items'][0]
        assert result['items'][1]['counts'] == {'candidates': 1, 'rules': 1}
        with artifact_session(workbench.scope) as session:
            assert session.scalars(select(WorkbenchArtifact)).all() == []
        token = jobs.load_config(workbench.scope, identifier)['judgment_token']
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
        assert first['draft_id'] == second['draft_id']
        with artifact_session(workbench.scope) as session:
            draft, = session.scalars(select(WorkbenchArtifact)).all()
            assert draft.module == setup.monitors.DRAFT_MODULE and draft.status == 'draft'
            jobs.close_judgment(session, workbench.scope, token, 2)
            session.commit()
        assert (await jobs.job_result(workbench.scope, identifier))['items'][0]['review_available'] is False
    asyncio.run(run())
