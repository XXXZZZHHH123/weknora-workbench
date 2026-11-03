"""Integration contracts for stable source skeletons and private result reuse."""
import asyncio
from copy import deepcopy

import pytest

from tests.test_knowledge_facts import canonical, FIRST, SERIES, page
from tin.web import diagram_generation as generation


def structure():
    return {'title': 'Research map', 'groups': [
        {'id': 'supply', 'label': 'Supply', 'role': 'supply', 'parent_id': None},
        {'id': 'target', 'label': 'Target', 'role': 'target', 'parent_id': None},
        {'id': 'demand', 'label': 'Demand', 'role': 'demand', 'parent_id': None}],
        'nodes': [{'id': role + '_concept', 'label': role, 'group_id': role}
                  for role in ('supply', 'target', 'demand')],
        'edges': [{'from': 'supply', 'to': 'target', 'kind': 'flow'},
                  {'from': 'target', 'to': 'demand', 'kind': 'flow'}],
        'coverage': [{'id': 'issue_' + role, 'label': role,
                      'node_ids': [role + '_concept']} for role in ('supply', 'target', 'demand')]}


def bound_graph():
    document = structure()
    document.pop('coverage')
    for node in document['nodes']:
        node['series_id'] = SERIES if node['id'] == 'supply_concept' else None
    return document


@pytest.fixture
def stable_generator(canonical, monkeypatch):
    client, headers, state, _ = canonical
    state.update(model_signature='model-v1', outline=structure(), graph=bound_graph(),
                 completions=[], model_checks=0)

    async def model(scope, model_id, requires_image=False):
        state['model_checks'] += 1
        if state.get('model_disabled'):
            from fastapi import HTTPException
            raise HTTPException(400, '模型已停用')
        return state['model_signature']

    async def complete(scope, model_id, prompt, image=None):
        state['completions'].append({'prompt': prompt, 'image': image})
        if state.get('provider_failed'):
            from fastapi import HTTPException
            raise HTTPException(503, '模型服务暂时不可用')
        if prompt.startswith('阶段：研究骨架提取'):
            return deepcopy(state['outline'])
        return deepcopy(state['graph'])

    monkeypatch.setattr(generation, 'current_model', model)
    monkeypatch.setattr(generation, 'completion', complete)
    return client, headers, state


def request(client, headers, **extra):
    return client.post(page(FIRST, '/api/sn/diagram/generation'), headers=headers,
                       json={'model_id': 'synthetic-chat', 'instructions': 'Complete source outline', **extra})


def test_same_input_reuses_validated_graph_without_model_calls(stable_generator):
    client, headers, state = stable_generator
    first = request(client, headers)
    assert first.status_code == 200, first.text
    assert len(state['completions']) == 2
    state['graph']['nodes'][0]['label'] = 'A different independent generation'
    second = request(client, headers)
    assert second.status_code == 200, second.text
    assert second.json()['markdown'] == first.json()['markdown']
    assert second.json()['analysis_reused'] is True
    assert len(state['completions']) == 2
    assert state['model_checks'] >= 4


def test_binding_stage_uses_frozen_outline_without_reinterpreting_image(stable_generator):
    client, headers, state = stable_generator
    image = 'data:image/png;base64,eA=='
    result = request(client, headers, image=image)
    assert result.status_code == 200, result.text
    assert state['completions'][0]['image'] == image
    assert state['completions'][1]['image'] is None
    assert 'issue_demand' in state['completions'][1]['prompt']


def test_missing_source_axis_gets_one_repair_and_never_becomes_a_reusable_result(stable_generator):
    client, headers, state = stable_generator
    state['graph']['nodes'].pop()
    result = request(client, headers)
    assert result.status_code == 422, result.text
    assert len(state['completions']) == 3
    assert state['completions'][-1]['prompt'].startswith('阶段：覆盖修正')
    state['graph'] = bound_graph()
    repaired = request(client, headers)
    assert repaired.status_code == 200, repaired.text
    assert repaired.json()['analysis_reused'] is False


def test_changed_model_or_instructions_trigger_new_interpretation(stable_generator):
    client, headers, state = stable_generator
    assert request(client, headers).status_code == 200
    state['model_signature'] = 'model-v2'
    assert request(client, headers).status_code == 200
    assert request(client, headers, instructions='A different research boundary').status_code == 200
    assert len(state['completions']) == 6


def test_model_revocation_blocks_reuse(stable_generator):
    client, headers, state = stable_generator
    assert request(client, headers).status_code == 200
    state['model_disabled'] = True
    assert request(client, headers).status_code == 400
    assert len(state['completions']) == 2


def test_explicit_rebuild_failure_keeps_previous_success(stable_generator):
    client, headers, state = stable_generator
    first = request(client, headers)
    assert first.status_code == 200, first.text
    state['provider_failed'] = True
    assert request(client, headers, rebuild=True).status_code == 503
    restored = request(client, headers)
    assert restored.status_code == 200, restored.text
    assert restored.json()['markdown'] == first.json()['markdown']
    assert restored.json()['analysis_reused'] is True


def test_explicit_successful_rebuild_becomes_next_default(stable_generator):
    client, headers, state = stable_generator
    first = request(client, headers)
    state['outline']['nodes'][0]['label'] = 'Updated source-supported concept'
    state['graph']['nodes'][0]['label'] = 'Updated source-supported concept'
    second = request(client, headers, rebuild=True)
    assert second.status_code == 200, second.text
    assert second.json()['markdown'] != first.json()['markdown']
    assert second.json()['analysis_reused'] is False
    assert request(client, headers).json()['markdown'] == second.json()['markdown']
    assert len(state['completions']) == 4


def test_source_change_or_permission_revocation_cannot_reuse(stable_generator):
    client, headers, state = stable_generator
    assert request(client, headers).status_code == 200
    state['value'] = 42
    updated = request(client, headers)
    assert updated.status_code == 200, updated.text
    assert updated.json()['analysis_reused'] is False
    assert len(state['completions']) == 4
    state['revoked'] = True
    assert request(client, headers).status_code in {403, 404}
    assert len(state['completions']) == 4


def test_source_change_during_outline_stops_before_binding(stable_generator, monkeypatch):
    client, headers, state = stable_generator
    complete = generation.completion

    async def changing(*args, **kwargs):
        result = await complete(*args, **kwargs)
        state['value'] += 1
        return result

    monkeypatch.setattr(generation, 'completion', changing)
    assert request(client, headers).status_code == 409
    assert len(state['completions']) == 1


def test_current_model_hashes_configuration_without_exposing_secrets(monkeypatch):
    from types import SimpleNamespace
    model = {'id': 'configured', 'name': 'Configured model', 'tenant_id': 17,
             'type': 'KnowledgeQA', 'status': 'active', 'source': 'remote',
             'parameters': {'base_url': 'https://synthetic.invalid', 'api_key': 'fixture-secret',
                            'supports_vision': True}}

    async def lookup(scope, path):
        assert path == '/api/v1/models/configured'
        return {'data': deepcopy(model)}

    monkeypatch.setattr(generation, 'go_request', lookup)
    scope = SimpleNamespace()
    before = asyncio.run(generation.current_model(scope, 'configured', True))
    assert len(before) == 64 and 'fixture-secret' not in before
    model['source'] = 'another-provider'
    assert asyncio.run(generation.current_model(scope, 'configured', True)) != before
    model['parameters']['supports_vision'] = False
    from fastapi import HTTPException
    with pytest.raises(HTTPException) as error:
        asyncio.run(generation.current_model(scope, 'configured', True))
    assert error.value.status_code == 422
    model['status'] = 'inactive'
    with pytest.raises(HTTPException) as error:
        asyncio.run(generation.current_model(scope, 'configured'))
    assert error.value.status_code == 400


def test_binding_contract_text_change_invalidates_cached_structure(stable_generator, monkeypatch):
    from tin.web import diagram_outline
    client, headers, state = stable_generator
    assert request(client, headers).status_code == 200
    original = diagram_outline.final_constraint
    monkeypatch.setattr(diagram_outline, 'final_constraint',
                        lambda *args, **kwargs: 'Updated binding contract. ' + original(*args, **kwargs))
    result = request(client, headers)
    assert result.status_code == 200, result.text
    assert result.json()['analysis_reused'] is False
    assert len(state['completions']) == 4


def test_catalog_order_alone_does_not_change_input_identity(stable_generator, monkeypatch):
    client, headers, state = stable_generator
    snapshot = generation.catalog_snapshot

    def ordered(scope=None):
        version, rows = snapshot(scope)
        rows = [*rows, {**rows[0], 'id': 'synthetic-another-field'}]
        return version, list(reversed(rows)) if state.get('reverse_catalog') else rows

    monkeypatch.setattr(generation, 'catalog_snapshot', ordered)
    first = request(client, headers)
    assert first.status_code == 200, first.text
    state['reverse_catalog'] = True
    second = request(client, headers)
    assert second.status_code == 200, second.text
    assert second.json()['analysis_reused'] is True
    assert len(state['completions']) == 2


def test_edit_revocation_during_cache_read_is_checked_again(stable_generator, monkeypatch):
    client, headers, state = stable_generator
    assert request(client, headers).status_code == 200
    resolve = generation.resolve_workbench
    calls = []

    async def revoked_after_claim(*args, **kwargs):
        fresh = await resolve(*args, **kwargs)
        calls.append(True)
        if len(calls) > 1:
            fresh.metadata['can_edit'] = False
        return fresh

    monkeypatch.setattr(generation, 'resolve_workbench', revoked_after_claim)
    denied = request(client, headers)
    assert denied.status_code == 403, denied.text
    assert len(calls) == 2
    assert len(state['completions']) == 2
