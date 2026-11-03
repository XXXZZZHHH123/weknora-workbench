"""Repeated imports reuse immutable AI interpretation, never another reviewer's choices."""
import asyncio
from copy import deepcopy

import pytest

from tests.test_judgment_generation import (
    TEXT, SERIES, endpoint, generator, rows,
)
from tests.test_knowledge_facts import FIRST, SECOND, canonical, page
from tin.web import judgment_generation as generation
from tests.test_judgment_analysis_jobs import background, scope_for_fixture


@pytest.fixture
def repeated(generator, monkeypatch):
    client, headers, upstream, response, calls = generator
    state = {'inventory': False}

    async def completion(scope, model, prompt):
        calls.append({'model': model, 'prompt': prompt})
        if 'research-source-inventory.v1' in prompt:
            state['inventory'] = True
            return {'blocks': [{'id': 's0001', 'disposition': 'research', 'reason': 'Research source'}],
                'items': [
                    {'id': 'i0001', 'block_ids': ['s0001'], 'quote': 'A supply contraction',
                        'kind': 'bull_claim', 'source_unit': None},
                    {'id': 'i0002', 'block_ids': ['s0001'], 'quote': 'soft orders',
                        'kind': 'bear_claim', 'source_unit': None},
                    {'id': 'i0003', 'block_ids': ['s0001'], 'quote': 'Stocks above 12 吨',
                        'kind': 'bull_evidence', 'source_unit': '吨'},
                    {'id': 'i0004', 'block_ids': ['s0001'], 'quote': 'Stocks above 12 吨',
                        'kind': 'threshold', 'source_unit': '吨'},
                ]}
        document = deepcopy(response)
        if state.get('change_model_while_generating'):
            upstream['model_revision'] = '2030-01-03T00:00:00Z'
            state['change_model_while_generating'] = False
        if state['inventory']:
            document['source_coverage'] = {
                'i0001': '/judgment/contradiction/bull/claim',
                'i0002': '/judgment/contradiction/bear/claim',
                'i0003': '/judgment/contradiction/bull/evidence/0',
                'i0004': '/judgment/thresholds/0',
            }
        return document

    monkeypatch.setattr(generation, 'completion', completion)
    return client, headers, upstream, state, calls


def generate(case, *, workbench=FIRST, base=0, text=TEXT, **extra):
    client, headers, *_ = case
    result = client.post(page(workbench, '/api/sn/judgment/generate'), headers=headers,
        json={'model_id': 'synthetic-chat', 'text': text, 'base_version': base, **extra})
    assert result.status_code == 200, result.text
    return result.json()


def item(preview, kind='binding'):
    return next(row for section in preview['review']['sections'] for row in section['items']
        if row['kind'] == kind)


def test_saving_an_older_preview_keeps_its_actual_analysis_protocol(generator, monkeypatch):
    from sqlalchemy import select
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import AuditLog
    client, headers, *_ = generator
    actual_protocol = generation.PROMPT_VERSION
    preview = client.post(endpoint('/generate'), headers=headers,
        json={'model_id': 'synthetic-chat', 'text': TEXT, 'base_version': 0}).json()
    monkeypatch.setattr(generation, 'PROMPT_VERSION', 'synthetic-newer-protocol')
    saved = client.post(endpoint('/apply'), headers=headers,
        json={'draft_id': preview['draft_id'], 'base_version': 0})
    assert saved.status_code == 200, saved.text
    with isolated_session_factory(17, FIRST)() as session:
        log = session.scalar(select(AuditLog).where(AuditLog.action == '研究逻辑分析保存'))
        assert log.detail['prompt_version'] == actual_protocol


def test_same_source_with_new_filename_and_panel_title_has_no_second_model_bill(repeated):
    first = generate(repeated, source_name='first.md', panel_title='First')
    count = len(repeated[-1])
    second = generate(repeated, source_name='renamed.md', panel_title='Second')
    assert len(repeated[-1]) == count
    assert first['draft_id'] != second['draft_id']
    assert first['markdown'] == second['markdown']
    assert first['bindings'] == second['bindings']
    assert rows() == []


def test_saving_a_draft_does_not_make_the_same_source_a_new_analysis(repeated):
    client, headers, *_ = repeated
    first = generate(repeated)
    count = len(repeated[-1])
    saved = client.post(endpoint('/apply'), headers=headers,
        json={'draft_id': first['draft_id'], 'base_version': 0})
    assert saved.status_code == 200, saved.text
    second = generate(repeated, base=1)
    assert second['base_version'] == 1
    assert len(repeated[-1]) == count
    assert first['bindings'] == second['bindings']
    assert len(rows()) == 1


def test_repeated_analysis_does_not_inherit_manual_empty_binding_or_review_revision(repeated):
    client, headers, *_ = repeated
    first = generate(repeated)
    original = item(first)
    assert original['binding']['series_id'] == SERIES
    changed = client.post(endpoint('/review/update'), headers=headers, json={
        'draft_id': first['draft_id'], 'revision': first['review']['revision'],
        'item_id': original['id'], 'action': 'keep_text', 'series_id': None})
    assert changed.status_code == 200, changed.text
    assert item(changed.json())['binding'] is None
    count = len(repeated[-1])
    second = generate(repeated)
    assert len(repeated[-1]) == count
    assert second['review']['revision'] == 1
    assert item(second)['binding']['series_id'] == SERIES
    assert item(second)['origin'] != 'user'


def test_another_workbench_does_not_reuse_the_first_workbench_interpretation(repeated):
    generate(repeated)
    count = len(repeated[-1])
    generate(repeated, workbench=SECOND)
    assert len(repeated[-1]) > count


def test_exact_source_change_requires_new_analysis_even_if_only_whitespace_changed(repeated):
    generate(repeated)
    count = len(repeated[-1])
    generate(repeated, text=TEXT + ' ')
    assert len(repeated[-1]) > count


def test_changing_model_configuration_requires_new_analysis(repeated):
    generate(repeated)
    count = len(repeated[-1])
    repeated[2]['model_revision'] = '2030-01-02T00:00:00Z'
    generate(repeated)
    assert len(repeated[-1]) > count


def test_model_configuration_changed_during_analysis_cannot_publish_under_old_identity(repeated):
    client, headers, _, state, calls = repeated
    state['change_model_while_generating'] = True
    result = client.post(endpoint('/generate'), headers=headers,
        json={'model_id': 'synthetic-chat', 'text': TEXT, 'base_version': 0})
    assert result.status_code == 409, result.text
    assert rows() == []
    count = len(calls)
    generate(repeated)
    assert len(calls) > count


def test_changing_the_source_snapshot_requires_new_analysis(repeated):
    generate(repeated)
    count = len(repeated[-1])
    repeated[2]['value'] = 18.5
    generate(repeated)
    assert len(repeated[-1]) > count


@pytest.mark.parametrize(('change', 'status'), [({'model_revoked': True}, 404), ({'model_status': 'downloading'}, 400)])
def test_cached_analysis_still_requires_an_available_authorized_chat_model(repeated, change, status):
    client, headers, upstream, _, calls = repeated
    generate(repeated)
    count = len(calls)
    upstream.update(change)
    result = client.post(endpoint('/generate'), headers=headers,
        json={'model_id': 'synthetic-chat', 'text': TEXT, 'base_version': 0})
    assert result.status_code == status, result.text
    assert len(calls) == count


def test_background_jobs_with_distinct_request_keys_reuse_the_same_interpretation(repeated, background):
    from tin.web import judgment_jobs as jobs

    async def run():
        scope = await scope_for_fixture()
        body = generation.GenerateRequest(model_id='synthetic-chat', text=TEXT, base_version=0)
        first = await jobs.start_job(scope, body, 'synthetic-repeat-first-key')
        await jobs.runtime.tasks[first['id']]
        initial = await jobs.job_result(scope, first['id'])
        count = len(repeated[-1])
        second = await jobs.start_job(scope, body, 'synthetic-repeat-second-key')
        await jobs.runtime.tasks[second['id']]
        result = await jobs.job_result(scope, second['id'])
        assert first['id'] != second['id']
        assert result['draft_id'] != initial['draft_id']
        assert result['markdown'] == initial['markdown']
        assert len(repeated[-1]) == count
        assert rows() == []

    asyncio.run(run())


@pytest.mark.parametrize('permission', ['revoked', 'workbench_revoked'])
def test_cached_interpretation_is_not_returned_after_access_is_revoked(repeated, permission):
    client, headers, upstream, _, calls = repeated
    generate(repeated)
    count = len(calls)
    upstream[permission] = True
    result = client.post(endpoint('/generate'), headers=headers,
        json={'model_id': 'synthetic-chat', 'text': TEXT, 'base_version': 0})
    assert result.status_code in {403, 404}, result.text
    assert len(calls) == count
