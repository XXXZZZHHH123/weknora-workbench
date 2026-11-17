"""Explicit report imports exercise the real isolated chart store and data path."""
import asyncio
from copy import deepcopy

from fastapi import HTTPException
import pytest
from sqlalchemy import select

from tin.integration.workbench_scope import current_workbench, isolated_session_factory
from tin.models import SourceMapping
from tin.models.workbench import WorkbenchArtifact
from tin.web import report_chart_import as imports
from tests.chart_fixtures import SID, OTHER, THIRD
from tests.test_chart_api import chart_client, WB  # noqa: F401

BASE = '/api/sn/charts/import/report'
SOURCE = {'kind': 'knowledge_file', 'source_binding_id': 'binding', 'knowledge_id': 'report',
          'file_version': 'a' * 64}


def candidate(identifier='chart', selected=SID):
    return {'id': identifier, 'page': 3, 'page_label': '第3页', 'title': 'Original chart title',
        'chart_type': 'trend', 'evidence': [{'chunk_id': 'chunk', 'quote': 'Original chart title Legend'}],
        'series': [{'id': 's1', 'name': 'Original legend', 'unit': '件', 'selected_id': selected,
            'candidates': [{'id': key, 'name': name, 'unit': '件', 'frequency': 'weekly'}
                           for key, name in ((SID, 'First'), (OTHER, 'Second'))]}]}


@pytest.fixture
def report_client(chart_client, monkeypatch):
    client, state = chart_client
    state.app.include_router(imports.router)
    state.report_verify = 0
    state.report_changed = False
    state.recognition_error = None
    state.report_batches = 2
    state.report_result = [candidate()]
    state.progress = []
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
    save = imports.save_state
    def progress(scope, identifier, **changes):
        if 'progress' in changes: state.progress.append(deepcopy(changes['progress']))
        return save(scope, identifier, **changes)
    monkeypatch.setattr(imports, 'save_state', progress)

    @state.app.post('/fixture/report-job')
    async def fixture_job():
        scope = imports.writable_scope()
        body = imports.RecognizeInput.model_validate({'source': SOURCE, 'model_id': 'model'})
        identifier = imports.new_job(scope, body)
        await imports.process(scope, identifier, body)
        return {'job_id': identifier}

    @state.app.post('/fixture/expire/{identifier}')
    def fixture_expire(identifier: str):
        scope = imports.writable_scope()
        with imports.artifact_session(scope) as session:
            row = imports.owned(session, scope, identifier)
            row.config = {**row.config, 'expires': 0}
            session.commit()
        return {}
    return client, state


def complete(client):
    response = client.post('/fixture/report-job')
    assert response.status_code == 200, response.text
    identifier = response.json()['job_id']
    response = client.get(BASE + '/jobs/' + identifier)
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


def test_end_to_end_drafts_mapping_real_preview_explicit_import_and_retry_receipt(report_client):
    client, state = report_client
    identifier, result = complete(client)
    assert result['status'] == 'complete' and len(result['charts']) == 2
    assert state.progress[-1]['completed'] == state.progress[-1]['total'] == 2
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


@pytest.mark.parametrize('operation', ['jobs', 'preview', 'apply'])
def test_source_revision_change_409_blocks_confirmed_actions(report_client, operation):
    client, state = report_client
    identifier, _ = complete(client)
    state.report_changed = True
    response = (client.get(BASE + '/jobs/' + identifier) if operation == 'jobs' else
                preview(client, identifier) if operation == 'preview' else apply(client, identifier))
    # Completed results, previews and writes must all reject revised evidence.
    assert response.status_code == 409
    assert client.get('/api/sn/charts').json()['total'] == 0


@pytest.mark.parametrize('headers,status', [({'x-can-edit': 'false'}, 403), ({'x-role': 'viewer'}, 403),
    ({'x-restricted': 'true'}, 403), ({'x-scope-version': '1'}, 409)])
@pytest.mark.parametrize('operation', ['files', 'recognize', 'job', 'preview', 'apply'])
def test_every_report_route_requires_write_unrestricted_current_source(report_client, headers, status, operation):
    client, state = report_client
    paths = {'files': ('GET', BASE + '/files', None),
             'recognize': ('POST', BASE + '/recognize', {'source': SOURCE, 'model_id': 'model'}),
             'job': ('GET', BASE + '/jobs/unknown', None),
             'preview': ('POST', BASE + '/preview', {'job_id': 'unknown', **selection()}),
             'apply': ('POST', BASE + '/apply', {'job_id': 'unknown', 'group': '', 'charts': [selection()]})}
    method, url, body = paths[operation]
    response = client.request(method, url, json=body, headers=headers)
    assert response.status_code == status, response.text
    assert state.requests == []


def test_drafts_private_to_owner_and_workbench_and_expire(report_client):
    client, _ = report_client
    identifier, _ = complete(client)
    assert client.get(BASE + '/jobs/' + identifier, headers={'x-actor': 'second'}).status_code == 404
    assert apply(client, identifier, headers={'x-actor': 'second'}).status_code == 404
    assert client.get(BASE + '/jobs/' + identifier,
        headers={'x-workbench': '10000000-0000-4000-8000-000000000099'}).status_code == 404
    assert client.post('/fixture/expire/' + identifier).status_code == 200
    assert client.get(BASE + '/jobs/' + identifier).status_code == 409


def test_one_failed_batch_does_not_discard_other_pages_and_409_is_fatal(report_client):
    client, state = report_client
    state.recognition_error = HTTPException(422, 'Synthetic malformed model output')
    _, result = complete(client)
    assert len(result['charts']) == 1 and result['charts'][0]['id'] == 'b2-chart'
    assert result['failures'] == [{'batch': 1, 'reason': 'Synthetic malformed model output'}]
    state.recognition_error = HTTPException(409, 'Synthetic source changed')
    identifier = client.post('/fixture/report-job').json()['job_id']
    assert client.get(BASE + '/jobs/' + identifier).status_code == 409


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
