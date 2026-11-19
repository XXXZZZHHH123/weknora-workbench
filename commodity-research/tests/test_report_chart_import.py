"""Explicit report imports exercise the real isolated chart store and data path."""
import asyncio
from collections import Counter
from copy import deepcopy
import json
from time import perf_counter, sleep

from fastapi import HTTPException
import httpx
import pytest
from sqlalchemy import select

from tin.integration.workbench_scope import current_workbench, isolated_session_factory
from tin.models import LlmCall, SourceMapping
from tin.models.workbench import WorkbenchArtifact
from tin.web import analysis_jobs, judgment_jobs, report_chart_import as imports, workbench_setup_jobs
from tin.web.report_chart_suggestions import recognize_batch as actual_recognize_batch
from tests.chart_fixtures import SID, OTHER, THIRD
from tests.test_chart_api import chart_client, WB  # noqa: F401

BASE = '/api/sn/charts/import/report'
JOBS = '/api/sn/analysis-jobs'
SOURCE = {'kind': 'knowledge_file', 'source_binding_id': 'binding', 'knowledge_id': 'report',
          'file_version': 'a' * 64}


def candidate(identifier='chart', selected=SID):
    return {'id': identifier, 'page': 3, 'page_label': '第3页', 'title': 'Original chart title',
        'chart_type': 'trend', 'legend_semantics': 'indicators',
        'evidence': [{'chunk_id': 'chunk', 'quote': 'Original chart title Legend'}],
        'series': [{'id': 's1', 'name': 'Original legend', 'unit': '件', 'selected_id': selected,
            'candidates': [{'id': key, 'name': name, 'unit': '件', 'frequency': 'weekly'}
                           for key, name in ((SID, 'First'), (OTHER, 'Second'))]}]}


@pytest.fixture
def report_client(chart_client, monkeypatch):
    client, state = chart_client
    state.app.include_router(imports.router)
    state.app.include_router(analysis_jobs.router)
    state.app.include_router(workbench_setup_jobs.router)
    state.report_verify = 0
    state.report_changed = False
    state.recognition_error = None
    state.report_batches = 2
    state.report_result = [candidate()]
    async def fresh(scope): return scope
    async def read(scope, citation):
        return {'filename': 'Synthetic weekly.pptx', 'file_version': SOURCE['file_version'],
            'knowledge_id': 'report', 'source_binding_id': 'binding', 'provenance': [],
            'locator_digest': 'digest', 'fragments': [],
            'batches': [[{'index': i, 'content': 'Evidence'}] for i in range(state.report_batches)]}
    async def verify(scope, citation, original):
        state.report_verify += 1
        if state.report_changed:
            raise HTTPException(409, 'Synthetic file revision changed')
    async def recognize(scope, model_id, batch, catalog=None):
        if state.recognition_error and batch[0]['index'] == 0:
            raise state.recognition_error
        return {'charts': deepcopy(state.report_result), 'failed': []}
    monkeypatch.setattr(imports, 'fresh_scope', fresh)
    monkeypatch.setattr(imports.suggestions, 'read_report', read)
    monkeypatch.setattr(imports.suggestions, 'verify_report', verify)
    monkeypatch.setattr(imports.suggestions, 'recognize_batch', recognize)
    monkeypatch.setattr(imports, 'source_status_hint', lambda scope: None)
    async def resolve(*args):
        return current_workbench.get()
    monkeypatch.setattr(judgment_jobs, 'resolve_workbench', resolve)

    @state.app.post('/fixture/expire/{identifier}')
    def fixture_expire(identifier: str):
        scope = imports.writable_scope()
        with analysis_jobs.registry.transaction() as connection:
            row = analysis_jobs.registry.owned(analysis_jobs.registry.fetch(connection, identifier),
                scope.identity, scope.workbench_id)
            analysis_jobs.registry.write(connection, {**row, 'expires_unix': 0})
        return {}

    with client:
        yield client, state


def submit(client):
    from uuid import uuid4
    response = client.post(BASE + '/recognize', json={'source': SOURCE, 'model_id': 'model'},
                           headers={'Idempotency-Key': uuid4().hex})
    assert response.status_code == 202, response.text
    job = response.json()
    assert job['phase'] == 'queued' and job['purpose'] == 'report_chart_import'
    return job['id']


def wait(client, identifier):
    for _ in range(500):
        response = client.get(f'{JOBS}/{identifier}')
        assert response.status_code == 200, response.text
        job = response.json()
        if job['state'] not in {'queued', 'running'}:
            return job
        assert job['phase_label']
        sleep(.01)
    pytest.fail('report chart job did not complete')


def complete(client):
    identifier = submit(client)
    job = wait(client, identifier)
    assert job['state'] == 'succeeded', job
    response = client.get(f'{JOBS}/{identifier}/result')
    assert response.status_code == 200, response.text
    return identifier, response.json()


def selection(identifier='b1-chart', **changes):
    return {'chart_id': identifier, 'chart_type': 'trend', 'mappings': [SID], **changes}


def preview(client, identifier, selected=None):
    return client.post(BASE + '/preview', json={'job_id': identifier, 'group': 'Editable group',
                                               **(selected or selection())})


def apply(client, identifier, charts=None, **kwargs):
    return client.post(BASE + '/apply', json={'job_id': identifier, 'group': 'Editable group',
                                             'charts': charts or [selection()]}, **kwargs)


@pytest.mark.parametrize('purpose', ['report_chart_import', 'workbench_setup'])
def test_end_to_end_drafts_mapping_real_preview_explicit_import_and_retry_receipt(report_client, purpose):
    client, state = report_client
    if purpose == 'workbench_setup':
        response = client.post('/api/sn/workbench-setup/jobs', json={
            'source': SOURCE, 'model_id': 'model', 'parts': ['charts'],
        }, headers={'Idempotency-Key': 'setup-chart-import-test'})
        assert response.status_code == 202, response.text
        identifier = response.json()['id']
        job = wait(client, identifier)
        assert job['state'] == 'succeeded', job
        response = client.get(f'{JOBS}/{identifier}/result')
        assert response.status_code == 200, response.text
        result = response.json()['items'][0]['data']
        assert result['job_id'] == identifier
    else:
        identifier, result = complete(client)
        assert result['job']['state'] == 'succeeded'
    assert len(result['charts']) == 2
    assert result['progress']['completed'] == result['progress']['total'] == 2
    assert client.get('/api/sn/charts').json()['total'] == 0, 'Recognizing must not import'
    changed = selection(mappings=[OTHER])
    response = preview(client, identifier, changed)
    assert response.status_code == 200, response.text
    body = response.json()
    assert body['definition']['curves'][0]['series_id'] == OTHER
    assert body['payload']['curves'][0]['series_id'] == OTHER
    assert body['payload']['curves'][0]['points'] == [{'date': '2023-01-01', 'value': 20.0}]
    assert state.report_verify >= 5, 'Recheck after recognition and either side of preview'
    assert client.get('/api/sn/charts').json()['total'] == 0
    response = apply(client, identifier, [changed])
    assert response.status_code == 200, response.text
    created = response.json()['created']
    assert len(created) == 1 and not response.json()['failed']
    assert created[0]['definition']['title'] == 'Original chart title'
    assert created[0]['definition']['note'] == '' and created[0]['definition']['group'] == 'Editable group'
    assert '/sn/charts/' in created[0]['href']
    assert apply(client, identifier, [changed]).json()['created'][0]['id'] == created[0]['id']
    assert client.get('/api/sn/charts').json()['total'] == 1


def test_duplicate_of_preserves_source_order_and_library_flags_refresh_after_apply(report_client):
    client, state = report_client
    state.report_batches = 1
    state.report_result = [candidate('first'), {**candidate('second'), 'page': 5}]
    identifier, result = complete(client)
    first, second = result['charts']
    assert 'duplicate_of' not in first and not first['possible_duplicate']
    assert second['duplicate_of'] == {'id': first['id'], 'page': 3, 'title': first['title']}
    assert not second['possible_duplicate']
    response = apply(client, identifier, [selection(first['id'])])
    assert response.status_code == 200, response.text
    assert response.json()['possible_duplicates'] == {first['id']: True, second['id']: True}
    restored = client.get(f'{JOBS}/{identifier}/result').json()
    assert all(chart['possible_duplicate'] for chart in restored['charts'])
    assert restored['applied'][first['id']]['id'] == response.json()['created'][0]['id']
    response = apply(client, identifier, [selection(second['id'])])
    assert len(response.json()['created']) == 1
    assert client.get('/api/sn/charts').json()['total'] == 2


@pytest.mark.parametrize('setup', [False, True])
@pytest.mark.parametrize('fail', [False, True])
def test_rematch_retrieves_original_draft_and_preserves_confirmations(report_client, monkeypatch, setup, fail):
    client, state = report_client
    state.report_batches = 1
    mixed = {**candidate('unmapped', selected=None), 'title': 'Pending chart title'}
    state.report_result = [candidate('ready'), mixed]
    state.fields.pop(OTHER)
    hint = {'pending': 3, 'message': '知识库还有 3 张工作表待确认'}
    monkeypatch.setattr(imports, 'source_status_hint', lambda scope: hint)

    def charts(result):
        return result['items'][0]['data'] if setup else result

    if setup:
        response = client.post('/api/sn/workbench-setup/jobs', json={
            'source': SOURCE, 'model_id': 'model', 'parts': ['charts'],
        }, headers={'Idempotency-Key': 'setup-rematch-test'})
        assert response.status_code == 202, response.text
        identifier = response.json()['id']
        assert wait(client, identifier)['state'] == 'succeeded'
    else:
        identifier = submit(client)
        assert wait(client, identifier)['state'] == 'succeeded'
    base_url = f'{JOBS}/{identifier}/result'
    first = apply(client, identifier, [selection('b1-ready')]).json()['created'][0]
    before = charts(client.get(base_url).json())
    assert before['source_status_hint'] == hint
    assert before['applied']['b1-ready']['id'] == first['id']

    from tests.chart_fixtures import FIELDS
    state.fields[OTHER] = FIELDS[OTHER]
    calls = []

    async def completion(scope, model_id, prompt, *, purpose):
        assert model_id == 'new-model' and len(prompt.encode()) <= imports.suggestions.PROMPT_BYTES
        assert 'Pending chart title' in prompt
        assert 'b1-ready/' not in prompt
        calls.append(purpose)
        if purpose == 'report_chart_queries':
            return {'queries': {'b1-unmapped/s1': ['字段乙']}}
        assert purpose == 'report_chart_fields' and OTHER in prompt
        if fail:
            raise HTTPException(502, '模拟模型调用失败')
        return {'picks': {'b1-unmapped/s1': {'series_id': OTHER, 'reason': '字段口径一致'}}}

    monkeypatch.setattr(imports.suggestions.strategy_text, 'completion', completion)
    response = client.post(BASE + '/rematch', json={'job_id': identifier, 'model_id': 'new-model'},
                           headers={'Idempotency-Key': 'report-rematch-test'})
    assert response.status_code == 202, response.text
    task = response.json()
    assert task['id'] != identifier and task['purpose'] == 'report_chart_import'
    rematch_url = f"{JOBS}/{task['id']}/result"
    assert wait(client, task['id'])['state'] == ('failed' if fail else 'succeeded')
    assert calls == ['report_chart_queries', 'report_chart_fields']
    parent = client.get(base_url)
    assert parent.status_code == 200, parent.text
    after = charts(parent.json())
    response = client.get(rematch_url)
    if fail:
        assert response.status_code == 409
        assert after == before
    else:
        assert response.status_code == 200, response.text
        restored = response.json()
        assert restored['id'] == identifier and restored['request']['rematch_of'] == identifier
        assert restored['charts'] == after['charts']
        assert restored['applied'] == before['applied'] == after['applied']
        assert after['charts'][0] == before['charts'][0]
        resolved = after['charts'][1]
        assert not resolved['missing_indicators'] and after['source_status_hint'] is None
        assert resolved['series'][0]['selected_id'] == OTHER
        for field in ('id', 'title', 'chart_type', 'evidence'):
            assert resolved[field] == before['charts'][1][field]
        assert resolved['series'][0]['name'] == before['charts'][1]['series'][0]['name']
        latest = client.get(JOBS + '/latest?purpose=report_chart_import').json()['job']
        assert latest['id'] == task['id']
        assert client.get(rematch_url, headers={'x-actor': 'other'}).status_code == 404
    assert apply(client, identifier, [selection('b1-ready')]).json()['created'][0]['id'] == first['id']
    assert client.get('/api/sn/charts').json()['total'] == 1


@pytest.mark.parametrize('operation', ['jobs', 'preview', 'apply'])
def test_source_revision_change_409_blocks_confirmed_actions(report_client, operation):
    client, state = report_client
    identifier, _ = complete(client)
    state.report_changed = True
    response = (client.get(f'{JOBS}/{identifier}/result') if operation == 'jobs' else
                preview(client, identifier) if operation == 'preview' else apply(client, identifier))
    # Completed results, previews and writes must all reject revised evidence.
    assert response.status_code == 409
    assert client.get('/api/sn/charts').json()['total'] == 0


@pytest.mark.parametrize('headers,status', [({'x-can-edit': 'false'}, 403), ({'x-role': 'viewer'}, 403),
    ({'x-restricted': 'true'}, 403), ({'x-scope-version': '1'}, 409)])
@pytest.mark.parametrize('operation', ['files', 'recognize', 'preview', 'apply'])
def test_every_report_route_requires_write_unrestricted_current_source(report_client, headers, status, operation):
    client, state = report_client
    paths = {'files': ('GET', BASE + '/files', None),
             'recognize': ('POST', BASE + '/recognize', {'source': SOURCE, 'model_id': 'model'}),
             'preview': ('POST', BASE + '/preview', {'job_id': 'unknown', **selection()}),
             'apply': ('POST', BASE + '/apply', {'job_id': 'unknown', 'group': '', 'charts': [selection()]})}
    method, url, body = paths[operation]
    response = client.request(method, url, json=body, headers=headers)
    assert response.status_code == status, response.text
    assert state.requests == []


def test_drafts_private_to_owner_and_workbench_and_expire(report_client):
    client, _ = report_client
    identifier, _ = complete(client)
    assert client.get(f'{JOBS}/{identifier}/result', headers={'x-actor': 'second'}).status_code == 404
    assert apply(client, identifier, headers={'x-actor': 'second'}).status_code == 404
    assert client.get(f'{JOBS}/{identifier}/result',
        headers={'x-workbench': '10000000-0000-4000-8000-000000000099'}).status_code == 404
    assert client.post('/fixture/expire/' + identifier).status_code == 200
    assert client.get(f'{JOBS}/{identifier}/result').status_code == 410


def test_one_failed_batch_does_not_discard_other_pages_and_409_is_fatal(report_client):
    client, state = report_client
    state.recognition_error = HTTPException(422, 'Synthetic malformed model output')
    _, result = complete(client)
    assert len(result['charts']) == 1 and result['charts'][0]['id'] == 'b2-chart'
    assert result['failures'] == [{'batch': 1, 'reason': 'Synthetic malformed model output'}]
    state.recognition_error = HTTPException(409, 'Synthetic source changed')
    identifier = submit(client)
    assert wait(client, identifier)['state'] == 'failed'
    assert client.get(f'{JOBS}/{identifier}/result').status_code == 409


def test_timeout_isolated_to_batch(report_client, monkeypatch):
    client, state = report_client
    original = imports.suggestions.recognize_batch
    async def timed(scope, model_id, batch, catalog=None):
        if batch[0]['index'] == 0: raise TimeoutError()
        return await original(scope, model_id, batch, catalog=catalog)
    monkeypatch.setattr(imports.suggestions, 'recognize_batch', timed)
    _, result = complete(client)
    assert len(result['charts']) == 1 and '超过' in result['failures'][0]['reason']


def test_apply_lists_invalid_definitions_and_imports_other_selected_charts(report_client):
    client, _ = report_client
    identifier, _ = complete(client)
    response = apply(client, identifier, [selection(mappings=[THIRD]), selection('b2-chart')])
    assert response.status_code == 200, response.text
    assert len(response.json()['created']) == 1 and len(response.json()['failed']) == 1
    assert '展示' in response.json()['failed'][0]['reason']
    assert preview(client, identifier, selection(mappings=[THIRD])).status_code == 422
    assert preview(client, identifier, selection('unknown')).status_code == 422
    assert apply(client, identifier, [selection(), selection()]).status_code == 422


def test_write_failure_isolated_and_retry_only_adds_missing_chart(report_client, monkeypatch):
    client, _ = report_client
    identifier, _ = complete(client)
    original = imports.create_row
    def fail_one(session, scope, value, **kwargs):
        if value.origin.ref.endswith('b1-chart'): raise RuntimeError('Synthetic unavailable write')
        return original(session, scope, value, **kwargs)
    monkeypatch.setattr(imports, 'create_row', fail_one)
    response = apply(client, identifier, [selection(), selection('b2-chart')])
    assert response.status_code == 200 and len(response.json()['created']) == 1
    assert len(response.json()['failed']) == 1
    monkeypatch.setattr(imports, 'create_row', original)
    retried = apply(client, identifier, [selection(), selection('b2-chart')]).json()
    assert len(retried['created']) == 2 and not retried['failed']
    assert client.get('/api/sn/charts').json()['total'] == 2


def test_file_picker_reuses_authorized_options_and_filters_explicit_file_metadata(report_client, monkeypatch):
    client, _ = report_client
    seen = []
    async def options(scope, search, offset, limit):
        seen.append((search, offset, limit))
        return {'items': [{'citation': {**SOURCE, 'knowledge_id': kind}, 'title': kind}
                           for kind in ('pptx', 'pdf', 'xlsx')], 'total': 3, 'next_offset': None}
    async def native(scope, binding, identifier): return scope, {'file_type': identifier}
    monkeypatch.setattr(imports.strategy_citations, '_file_options', options)
    monkeypatch.setattr(imports.strategy_citations, '_native_file', native)
    response = client.get(BASE + '/files?search=weekly&offset=0&limit=20')
    assert response.status_code == 200 and seen == [('weekly', 0, 20)]
    assert [item['title'] for item in response.json()['items']] == ['pptx', 'pdf']


@pytest.mark.parametrize('operation', ['preview', 'apply'])
def test_report_revision_change_during_definition_preparation_blocks_response_and_write(report_client, monkeypatch, operation):
    client, state = report_client
    identifier, _ = complete(client)
    original = imports.draft
    def changed(scope, draft_state, selected, group):
        value = original(scope, draft_state, selected, group)
        state.report_changed = True
        return value
    monkeypatch.setattr(imports, 'draft', changed)
    response = preview(client, identifier) if operation == 'preview' else apply(client, identifier)
    assert response.status_code == 409
    assert client.get('/api/sn/charts').json()['total'] == 0


def test_total_timeout_preserves_completed_pages_after_final_evidence_recheck(report_client, monkeypatch):
    client, state = report_client
    monkeypatch.setattr(imports.settings, 'report_chart_import_concurrency', 1)
    original = imports.suggestions.recognize_batch
    async def delayed(scope, model_id, batch, catalog=None):
        if batch[0]['index'] == 1:
            raise asyncio.CancelledError()
        return await original(scope, model_id, batch, catalog=catalog)
    monkeypatch.setattr(imports.suggestions, 'recognize_batch', delayed)
    class ExhaustedBudget:
        async def __aenter__(self): return self
        async def __aexit__(self, kind, value, traceback):
            if kind is asyncio.CancelledError: raise TimeoutError()
    monkeypatch.setattr(imports.asyncio, 'timeout', lambda seconds: ExhaustedBudget())
    identifier, result = complete(client)
    assert len(result['charts']) == 1 and result['charts'][0]['id'] == 'b1-chart'
    assert result['progress']['completed'] == 1 and result['progress']['total'] == 2
    assert any('整体识别超时' in item['reason'] for item in result['failures'])
    assert apply(client, identifier).status_code == 200


def test_ordinary_batch_failure_retains_other_successful_pages(report_client):
    client, state = report_client
    state.recognition_error = RuntimeError('Synthetic independent batch failure')
    _, result = complete(client)
    assert len(result['charts']) == 1 and result['charts'][0]['id'] == 'b2-chart'
    assert result['failures'][0]['batch'] == 1 and result['failures'][0]['reason']


def test_unmapped_chart_is_visible_unselected_and_manual_candidate_can_repair_it(report_client):
    client, state = report_client
    state.report_result = [candidate(selected=None)]
    identifier, result = complete(client)
    chart = result['charts'][0]
    assert chart['missing_indicators'] and not chart['definition_failed']
    assert chart['errors'] == ['指标库中未找到，请手动选择或跳过']
    assert 'definition' not in chart
    assert preview(client, identifier, selection(mappings=[None])).status_code == 422
    assert preview(client, identifier).status_code == 200
    assert client.get('/api/sn/charts').json()['total'] == 0


def test_duplicate_bindings_warn_in_draft_and_in_manually_changed_preview(report_client):
    client, state = report_client
    chart = candidate()
    chart['series'].append({**deepcopy(chart['series'][0]), 'id': 's2', 'name': 'Another legend'})
    state.report_result = [chart]
    identifier, result = complete(client)
    assert len(result['charts'][0]['definition']['curves']) == 1
    assert any('重复映射' in warning for warning in result['charts'][0]['mapping_warnings'])
    response = preview(client, identifier, selection(mappings=[SID, SID]))
    assert response.status_code == 200 and len(response.json()['definition']['curves']) == 1
    assert any('重复映射' in warning for warning in response.json()['warnings'])


def test_bounded_concurrency_progress_order_failure_isolation_and_serial_equivalence(report_client, monkeypatch):
    client, state = report_client
    state.report_batches = 12
    active, peak, finished = 0, 0, []
    async def recognize(scope, model_id, batch, catalog=None):
        nonlocal active, peak
        index = batch[0]['index']
        active += 1
        peak = max(peak, active)
        try:
            # Three fake model stages, with first batch deliberately finishing late.
            for _ in range(3):
                await asyncio.sleep(0.025 if index == 0 else 0.015)
            if index == 4:
                raise HTTPException(422, 'Synthetic malformed model output')
            return {'charts': [candidate()], 'failed': []}
        finally:
            active -= 1
            finished.append(index)
    monkeypatch.setattr(imports.suggestions, 'recognize_batch', recognize)
    # Warm the artifact store/catalog before comparing concurrency. Otherwise
    # first-job initialization would inflate only the serial measurement.
    complete(client)
    results, timing = [], []
    for concurrency in (1, 3):
        monkeypatch.setattr(imports.settings, 'report_chart_import_concurrency', concurrency)
        peak, finished = 0, []
        started = perf_counter()
        _, result = complete(client)
        timing.append(perf_counter() - started)
        assert active == 0 and peak == concurrency
        assert result['progress']['completed'] == result['progress']['total'] == 12
        assert len(result['charts']) == 11
        assert result['failures'] == [{'batch': 5, 'reason': 'Synthetic malformed model output'}]
        assert [chart['id'] for chart in result['charts']] == [f'b{i + 1}-chart' for i in range(12) if i != 4]
        if concurrency == 3:
            assert finished[0] != 0, 'Exercise completion in a different order from source'
        for chart in result['charts']:
            chart['definition']['origin']['ref'] = 'normalized-job-id'
        results.append({key: value for key, value in result.items() if key not in {'id', 'job'}})
    assert results[0] == results[1]
    print('report-chart benchmark ' + json.dumps({'batches': 12, 'stages_per_batch': 3,
        'serial_seconds': round(timing[0], 4), 'parallel_seconds': round(timing[1], 4),
        'speedup': round(timing[0] / timing[1], 2)}))


def test_fatal_revision_change_cancels_and_drains_other_batches(report_client, monkeypatch):
    client, state = report_client
    state.report_batches = 8
    monkeypatch.setattr(imports.settings, 'report_chart_import_concurrency', 3)
    entered, cancelled = [], []
    async def recognize(scope, model_id, batch, catalog=None):
        index = batch[0]['index']
        entered.append(index)
        if index == 0:
            await asyncio.sleep(0.01)
            raise HTTPException(409, 'Synthetic source changed')
        try:
            await asyncio.Event().wait()
        finally:
            cancelled.append(index)
    monkeypatch.setattr(imports.suggestions, 'recognize_batch', recognize)
    identifier = submit(client)
    assert wait(client, identifier)['state'] == 'failed'
    assert client.get(f'{JOBS}/{identifier}/result').status_code == 409
    assert entered == [0, 1, 2] and sorted(cancelled) == [1, 2]
    assert client.get('/api/sn/charts').json()['total'] == 0


@pytest.mark.parametrize('failure_kind', ['http', 'timeout'])
def test_concurrent_real_model_path_persists_every_stage_audit_including_failure(report_client, monkeypatch, failure_kind):
    client, state = report_client
    state.report_batches = 3
    monkeypatch.setattr(imports.settings, 'report_chart_import_concurrency', 3)
    monkeypatch.setattr(imports.settings, 'weknora_api_url', 'http://fixture.invalid')
    monkeypatch.setattr(imports, 'BATCH_SECONDS', 1)
    text = 'Original chart title\nOriginal legend'
    async def read(scope, citation):
        return {'filename': 'Synthetic weekly.pptx', 'file_version': SOURCE['file_version'],
            'knowledge_id': 'report', 'source_binding_id': 'binding', 'provenance': [],
            'locator_digest': 'digest', 'fragments': [], 'batches': [[{
                'id': f'chunk-{i}', 'index': i, 'page': i + 1, 'content': text}] for i in range(3)]}
    extraction_started = 0
    ready = None
    async def upstream(request):
        nonlocal extraction_started, ready
        prompt = json.loads(request.content)['prompt']
        if prompt.startswith('report-chart-extract'):
            if ready is None:
                ready = asyncio.Event()
            extraction_started += 1
            if extraction_started == 3:
                ready.set()
            await asyncio.wait_for(ready.wait(), 1)
            fragments = json.loads(prompt.split('\n解析片段：', 1)[1])
            fragment = fragments[0]
            # One transport error is still audited and cannot poison its neighbours.
            if fragment['index'] == 1:
                if failure_kind == 'timeout':
                    await asyncio.Event().wait()
                return httpx.Response(503, json={'success': False})
            document = {'charts': [{'title': 'Original chart title', 'page': fragment['page'],
                'chart_type': 'trend', 'legend_semantics': 'indicators',
                'series': [{'name': 'Original legend', 'unit': '', 'legends': ['Original legend']}],
                'evidence': [{'chunk_id': fragment['id'], 'quote': text}]}]}
        elif prompt.startswith('report-chart-map'):
            document = {'picks': {'c1/s1': {'series_id': SID, 'reason': 'Same measurement scope'}}}
        else:
            document = {'queries': {'c1/s1': ['真实字段']}}
        return httpx.Response(200, json={'success': True, 'data': {
            'model_id': 'model', 'content': json.dumps(document), 'finish_reason': 'stop'}})
    original_client = httpx.AsyncClient
    monkeypatch.setattr(imports.suggestions, 'read_report', read)
    monkeypatch.setattr(imports.suggestions, 'recognize_batch', actual_recognize_batch)
    monkeypatch.setattr(httpx, 'AsyncClient', lambda *args, **kwargs: original_client(
        *args, **{**kwargs, 'transport': httpx.MockTransport(upstream)}))
    _, result = complete(client)
    assert len(result['charts']) == 2 and len(result['failures']) == 1
    with isolated_session_factory(17, WB)() as session:
        calls = session.scalars(select(LlmCall)).all()
        assert Counter(call.purpose for call in calls) == {
            'report_chart_extract': 3, 'report_chart_queries': 2, 'report_chart_fields': 2}
        assert Counter(call.status for call in calls) == {'ok': 6, 'error': 1}
        assert all(call.model == 'model' and call.request for call in calls)
