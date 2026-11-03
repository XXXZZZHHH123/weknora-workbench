"""Source-only model transport exercises binding, coverage, review, and durable reuse together."""
import asyncio
from copy import deepcopy

from fastapi import HTTPException
import pytest
from sqlalchemy import select

from tests.test_judgment_generation import generator, endpoint, rows
from tests.test_judgment_analysis_jobs import scope_for_fixture
from tests.test_knowledge_facts import canonical, SERIES
from tin.integration.workbench_scope import isolated_session_factory
from tin.models import SourceMapping
from tin.web import judgment_generation as generation
from tin.web import judgment_analysis_cache as cache


SOURCE = ('Capacity contracts while flows weaken.\nOrders soften.\n'
          'Quantity > 12 吨.\nCost moves.')
CAPACITY = 'synthetic-capacity'
FLOWS = 'synthetic-flows'
ORDERS = 'synthetic-orders'
COST = 'synthetic-cost'


def source_inventory():
    statements = [
        ('core', 's0001', 'Capacity contracts while flows weaken.', 'bull_evidence', None),
        ('orders', 's0002', 'Orders soften.', 'bear_evidence', None),
        ('bound', 's0003', 'Quantity > 12 吨', 'bull_evidence', '吨'),
        ('rule', 's0003', 'Quantity > 12 吨', 'threshold', '吨'),
        ('margin', 's0004', 'Cost moves.', 'marginal_focus', None),
        ('cost', 's0004', 'Cost moves.', 'cost_line', None),
    ]
    return {'blocks': [{'id': f's{index + 1:04d}', 'disposition': 'research', 'reason': 'Explicit source statement.'}
                       for index in range(4)],
        'items': [{'id': name, 'block_ids': [block], 'quote': quote, 'kind': kind, 'source_unit': unit}
                  for name, block, quote, kind, unit in statements]}


def analysis_document():
    return {'judgment': {'contradiction': {
        'bull': {'claim': 'Capacity and flows support value.', 'evidence': [
            {'desc': 'Capacity contracts while flows weaken.', 'series_id': None, 'note': 'Original combined evidence.'},
            {'desc': 'Original quantity anchor.', 'series_id': SERIES, 'note': 'Keep this binding.'}]},
        'bear': {'claim': 'Orders oppose that view.', 'evidence': [{'desc': 'Orders soften.', 'series_id': None}]}},
        'marginal_focus': [{'desc': 'Cost movement.', 'series_id': None}],
        'cost_line': {'type': 'cash', 'value': None, 'source': None,
            'items': [{'desc': 'Cost measure.', 'series_id': None, 'value': None}]},
        'whitelist': [SERIES], 'thresholds': [{
            'id': 'watch', 'name': 'Quantity watch', 'series_id': SERIES, 'operator': '>',
            'value': 12, 'type': '观察位', 'horizon': '短期'}]},
        'binding_units': {SERIES: '吨'}, 'rule_quotes': {'watch': 'Quantity > 12 吨'}, 'unresolved': [],
        'source_coverage': {'core': '/judgment/contradiction/bull/evidence/0',
            'orders': '/judgment/contradiction/bear/evidence/0',
            'bound': '/judgment/contradiction/bull/evidence/1', 'rule': '/judgment/thresholds/0',
            'margin': '/judgment/marginal_focus/0', 'cost': '/judgment/cost_line/items/0'}}


def binding_decision():
    return {'bindings': [
        {'path': '/judgment/contradiction/bull/evidence/0', 'items': [
            {'desc': 'Capacity contracts.', 'quote': 'Capacity contracts', 'series_id': CAPACITY,
             'reason': 'Same source entity and scope.'},
            {'desc': 'Flows weaken.', 'quote': 'flows weaken', 'series_id': FLOWS,
             'reason': 'Same source flow measure.'}]},
        {'path': '/judgment/contradiction/bear/evidence/0', 'items': [
            {'desc': 'Orders soften.', 'quote': 'Orders soften.', 'series_id': ORDERS,
             'reason': 'Same demand measure.'}]},
        {'path': '/judgment/marginal_focus/0', 'items': [
            {'desc': 'Cost movement.', 'quote': 'Cost moves.', 'series_id': COST,
             'reason': 'Same marginal cost measure.'}]},
        {'path': '/judgment/cost_line/items/0', 'items': [
            {'desc': 'Cost measure.', 'quote': 'Cost moves.', 'series_id': COST,
             'reason': 'Same explicit cost measure.'}]},
    ]}


@pytest.fixture
def anchored(generator, monkeypatch):
    client, headers, upstream, _, calls = generator
    original_snapshot = generation.snapshot
    state = {'mode': 'valid'}

    def snapshot(*args, **kwargs):
        value = original_snapshot(*args, **kwargs)
        value['catalog'] = deepcopy(value['catalog']) + [
            {'id': CAPACITY, 'name': 'Synthetic capacity', 'unit': 'units'},
            {'id': FLOWS, 'name': 'Synthetic flows', 'unit': None},
            {'id': ORDERS, 'name': 'Synthetic orders', 'unit': 'units'},
            {'id': COST, 'name': 'Synthetic cost', 'unit': 'currency/unit'}]
        return value

    async def completion(scope, model, prompt):
        if prompt.startswith('research-source-inventory.v1'):
            stage = 'inventory'
            result = source_inventory()
        elif prompt.startswith('research-anchor-binding.v1'):
            stage = 'binding'
            if isinstance(state['mode'], int):
                calls.append({'stage': stage, 'prompt': prompt})
                raise HTTPException(state['mode'], 'Synthetic model/source access outcome.')
            result = binding_decision()
            if state['mode'] == 'invalid-id':
                result['bindings'][0]['items'][0]['series_id'] = 'unauthorized-field'
            elif state['mode'] == 'invalid-quote':
                result['bindings'][0]['items'][0]['quote'] = 'Invented evidence.'
            elif state['mode'] == 'revoke-source':
                upstream['revoked'] = True
        else:
            stage = 'repair' if '上次结构化校验反馈' in prompt else 'analysis'
            result = analysis_document()
            if state.get('bound_core'):
                result['judgment']['contradiction']['bull']['evidence'][0]['series_id'] = CAPACITY
                result['judgment']['contradiction']['bear']['evidence'][0]['series_id'] = ORDERS
                result['binding_units'].update({CAPACITY: 'units', ORDERS: 'units'})
        calls.append({'stage': stage, 'prompt': prompt})
        return deepcopy(result)

    monkeypatch.setattr(generation, 'snapshot', snapshot)
    monkeypatch.setattr(generation, 'completion', completion)
    return client, headers, upstream, state, calls


def generate(case):
    client, headers, *_ = case
    return client.post(endpoint('/generate'), headers=headers,
        json={'model_id': 'synthetic-chat', 'text': SOURCE, 'base_version': 0})


def private_preview(case, response):
    assert response.status_code == 200, response.text
    scope = asyncio.run(scope_for_fixture())
    return generation.previews.get(scope, response.json()['draft_id'])


def stored_analyses():
    with isolated_session_factory(17, '10000000-0000-4000-8000-000000000011')() as session:
        return [row for row in session.scalars(select(SourceMapping))
                if row.config.get('kind') == cache.KIND]


def test_binding_refinement_splits_both_sides_once_and_remaps_all_coverage_without_false_pending(anchored):
    response = generate(anchored)
    preview = private_preview(anchored, response)
    assert [call['stage'] for call in anchored[-1]] == ['inventory', 'analysis', 'repair', 'binding']
    evidence = preview['document']['judgment']['contradiction']
    assert [row['series_id'] for row in evidence['bull']['evidence']] == [CAPACITY, FLOWS, SERIES]
    assert [row['series_id'] for row in evidence['bear']['evidence']] == [ORDERS]
    assert evidence['bull']['evidence'][2]['note'] == 'Keep this binding.'
    assert preview['document']['judgment']['thresholds'][0]['value'] == 12
    assert preview['document']['rule_quotes'] == {'watch': 'Quantity > 12 吨'}
    assert preview['document']['judgment']['marginal_focus'][0]['series_id'] == COST
    assert preview['document']['judgment']['cost_line']['items'][0]['series_id'] == COST
    coverage = {row['id']: row for row in preview['source_coverage']['items']}
    assert coverage['core']['paths'] == [
        '/judgment/contradiction/bull/evidence/0', '/judgment/contradiction/bull/evidence/1']
    assert coverage['bound']['paths'] == ['/judgment/contradiction/bull/evidence/2']
    assert preview['source_coverage']['retained'] == 0
    assert not any(issue['code'] in {'source_mapping_invalid', 'source_item_retained'}
                   for issue in response.json()['validation_issues'])
    assert response.json()['review']['pending_count'] == 0
    assert preview['anchor_bindings']['attempted'] is True
    assert rows() == []


def test_completed_binding_refinement_is_reused_without_a_new_model_request(anchored):
    first = generate(anchored)
    assert first.status_code == 200, first.text
    assert len(anchored[-1]) == 4 and first.json()['analysis_reused'] is False
    anchored[3]['mode'] = 'invalid-id'
    second = generate(anchored)
    assert second.status_code == 200, second.text
    assert len(anchored[-1]) == 4 and second.json()['analysis_reused'] is True
    assert first.json()['markdown'] == second.json()['markdown']
    assert first.json()['draft_id'] != second.json()['draft_id']
    assert len(stored_analyses()) == 1 and rows() == []


def test_unbound_optional_cost_anchors_alone_do_not_start_core_binding_refinement(anchored):
    anchored[3]['bound_core'] = True
    response = generate(anchored)
    preview = private_preview(anchored, response)
    assert [call['stage'] for call in anchored[-1]] == ['inventory', 'analysis', 'repair']
    assert preview['anchor_bindings'] == {'attempted': False}
    assert preview['document']['judgment']['marginal_focus'][0]['series_id'] is None
    assert preview['document']['judgment']['cost_line']['items'][0]['series_id'] is None
    assert rows() == []


@pytest.mark.parametrize('mode', ['invalid-id', 'invalid-quote', 429, 503])
def test_invalid_or_unavailable_binding_plan_preserves_original_text_and_is_not_frozen(anchored, mode):
    anchored[3]['mode'] = mode
    first = generate(anchored)
    preview = private_preview(anchored, first)
    assert preview['document']['judgment']['contradiction']['bull']['evidence'][0]['series_id'] is None
    assert preview['document']['judgment']['contradiction']['bull']['evidence'][0]['desc'] == 'Capacity contracts while flows weaken.'
    assert preview['document']['judgment']['contradiction']['bull']['evidence'][1]['series_id'] == SERIES
    assert any(issue['code'] in {'anchor_binding_invalid', 'anchor_binding_unavailable'}
               for issue in first.json()['validation_issues'])
    assert stored_analyses() == []
    assert first.json()['analysis_reused'] is False and rows() == []
    count = len(anchored[-1])
    anchored[3]['mode'] = 'valid'
    second = generate(anchored)
    assert second.status_code == 200, second.text
    assert len(anchored[-1]) == count + 4 and second.json()['analysis_reused'] is False
    assert private_preview(anchored, second)['document']['judgment']['contradiction']['bull']['evidence'][0]['series_id'] == CAPACITY


@pytest.mark.parametrize('mode', [401, 403, 'revoke-source'])
def test_authorization_failure_during_binding_never_returns_partial_content(anchored, mode):
    anchored[3]['mode'] = mode
    response = generate(anchored)
    assert response.status_code == (401 if mode == 401 else 403), response.text
    assert 'markdown' not in response.json() and 'review' not in response.json()
    assert generation.previews.items == {} and stored_analyses() == [] and rows() == []
