"""Synthetic model decisions can bind source anchors without changing rules or facts."""
from copy import deepcopy
import json

from jsonschema import Draft202012Validator
import pytest

from tin.judgments.analysis import AnalysisError
from tin.judgments.anchor_binding import targets, binding_protocol, apply_binding_plan, BINDING_SCHEMA


SOURCE = ('Capacity contracts in the north.\nFlows weaken in the west.\nOrders soften in the south.\n'
          'Cost rises at the mill.\nQuantity > 12 kg.')
CATALOG = [{'id': 'original-bound', 'name': 'Existing quantity', 'unit': 'kg'},
           {'id': 'capacity', 'name': 'Capacity measure', 'unit': 'units'},
           {'id': 'flows', 'name': 'Flow measure', 'unit': None},
           {'id': 'orders', 'name': 'Orders measure', 'unit': 'units'},
           {'id': 'cost', 'name': 'Cost measure', 'unit': 'currency/unit'}]


def document():
    return {'judgment': {'contradiction': {
        'bull': {'claim': 'Positive evidence', 'evidence': [
            {'desc': 'Capacity contracts and flows weaken.', 'series_id': None,
             'value': None, 'as_of': None, 'source': None, 'note': 'Original note'},
            {'desc': 'Already bound evidence', 'series_id': 'original-bound', 'note': 'Unchanged'}]},
        'bear': {'claim': 'Negative evidence', 'evidence': [
            {'desc': 'Orders soften.', 'series_id': None}]}},
        'marginal_focus': [{'desc': 'Cost movement.', 'series_id': None}],
        'cost_line': {'type': 'cash', 'value': None, 'source': None,
            'items': [{'desc': 'Cost measure.', 'series_id': None, 'value': None}], 'note': 'Cost note'},
        'whitelist': ['original-bound'],
        'thresholds': [{'id': 'original-rule', 'name': 'Quantity boundary', 'series_id': 'original-bound',
            'operator': '>', 'value': 12, 'type': '观察位', 'horizon': '短期'}],
        'falsifiers': [], 'priority_rule': None},
        'binding_units': {'original-bound': 'kg'}, 'rule_quotes': {'original-rule': 'Quantity > 12 kg'},
        'unresolved': [{'section': 'Rule', 'text': 'Quantity condition needs review.',
            'reason': 'Not a descriptive anchor', 'series_id': None,
            'business_condition': 'Quantity > 12 kg', 'rule_kind': 'threshold'}]}


def plan(doc=None):
    doc = document() if doc is None else doc
    quotes = {'/judgment/contradiction/bull/evidence/0': 'Capacity contracts in the north.',
        '/judgment/contradiction/bear/evidence/0': 'Orders soften in the south.',
        '/judgment/marginal_focus/0': 'Cost rises at the mill.',
        '/judgment/cost_line/items/0': 'Cost rises at the mill.'}
    return {'bindings': [{'path': target['path'], 'items': [{'desc': target['description'],
        'quote': quotes[target['path']], 'series_id': None, 'reason': 'No compatible metric identified.'}]}
        for target in targets(doc)]}


def test_only_unbound_descriptive_anchors_are_targets():
    values = targets(document())
    assert [row['path'] for row in values] == [
        '/judgment/contradiction/bull/evidence/0', '/judgment/contradiction/bear/evidence/0',
        '/judgment/marginal_focus/0', '/judgment/cost_line/items/0']
    assert [row['kind'] for row in values] == ['evidence', 'evidence', 'marginal_focus', 'cost_line']
    assert [row.get('side') for row in values[:2]] == ['bull', 'bear']


def test_complete_null_decisions_preserve_the_entire_source_document_and_reasons():
    doc = document(); original = deepcopy(doc)
    updated, metadata = apply_binding_plan(doc, plan(doc), SOURCE, CATALOG)
    assert updated == original and doc == original
    assert metadata['path_remap']['/judgment/contradiction/bull/evidence/1'] == [
        '/judgment/contradiction/bull/evidence/1']
    assert len(metadata['bindings']) == 4
    assert all(row['items'][0]['reason'] == 'No compatible metric identified.' for row in metadata['bindings'])


def test_split_evidence_preserves_order_other_fields_bound_entries_and_original_rules():
    doc = document(); original = deepcopy(doc); decisions = plan(doc)
    decisions['bindings'][0]['items'] = [
        {'desc': 'Capacity contracts.', 'quote': 'Capacity contracts in the north.',
         'series_id': 'capacity', 'reason': 'Same source entity, scope, and quantity.'},
        {'desc': 'Flows weaken.', 'quote': 'Flows weaken in the west.',
         'series_id': 'flows', 'reason': 'Matching measure; original unit is not recorded.'}]
    updated, metadata = apply_binding_plan(doc, decisions, SOURCE, CATALOG)
    assert doc == original
    evidence = updated['judgment']['contradiction']['bull']['evidence']
    assert [row['series_id'] for row in evidence] == ['capacity', 'flows', 'original-bound']
    assert evidence[2] == original['judgment']['contradiction']['bull']['evidence'][1]
    assert all(row['note'] == 'Original note' and row['value'] is None and row['source'] is None for row in evidence[:2])
    assert metadata['path_remap']['/judgment/contradiction/bull/evidence/0'] == [
        '/judgment/contradiction/bull/evidence/0', '/judgment/contradiction/bull/evidence/1']
    assert metadata['path_remap']['/judgment/contradiction/bull/evidence/1'] == [
        '/judgment/contradiction/bull/evidence/2']
    assert updated['judgment']['thresholds'] == original['judgment']['thresholds']
    assert updated['judgment']['whitelist'] == original['judgment']['whitelist']
    assert updated['rule_quotes'] == original['rule_quotes'] and updated['unresolved'] == original['unresolved']
    assert updated['binding_units'] == {'original-bound': 'kg', 'capacity': 'units', 'flows': None}


def test_other_anchors_bind_without_changing_descriptions_or_existing_wrong_units():
    doc = document(); doc['binding_units']['cost'] = 'unverified-original-unit'; original = deepcopy(doc)
    decisions = plan(doc)
    for row in decisions['bindings'][2:]:
        row['items'][0].update(series_id='cost', reason='Matching explicit source quantity.')
    updated, _ = apply_binding_plan(doc, decisions, SOURCE, CATALOG)
    assert updated['binding_units']['cost'] == 'unverified-original-unit'
    assert updated['judgment']['marginal_focus'] == [{'desc': 'Cost movement.', 'series_id': 'cost'}]
    assert updated['judgment']['cost_line']['items'] == [{'desc': 'Cost measure.', 'series_id': 'cost', 'value': None}]
    assert doc == original


@pytest.mark.parametrize('change', [
    lambda value: value['bindings'].pop(),
    lambda value: value['bindings'].append(deepcopy(value['bindings'][0])),
    lambda value: value['bindings'][0].update(path='/judgment/contradiction/bull/evidence/1'),
    lambda value: value['bindings'][0].update(path='/judgment/thresholds/0'),
    lambda value: value['bindings'][0].update(path='/judgment/contradiction/bull/evidence/00'),
    lambda value: value['bindings'][0]['items'][0].update(series_id='invented-private-field'),
    lambda value: value['bindings'][0]['items'][0].update(series_id=True),
    lambda value: value['bindings'][0]['items'][0].update(quote='Invented source wording.'),
    lambda value: value['bindings'][0]['items'][0].update(quote=''),
    lambda value: value['bindings'][0]['items'][0].update(reason=''),
    lambda value: value['bindings'][0]['items'][0].update(value=999),
    lambda value: value['bindings'][0]['items'][0].update(unit='invented'),
    lambda value: value['bindings'][0].update(side='bear'),
    lambda value: value.update(judgment={}),
    lambda value: value['bindings'][0].update(items=[]),
    lambda value: value['bindings'][0].update(items=value['bindings'][0]['items'] * 9),
    lambda value: value['bindings'][2].update(items=value['bindings'][2]['items'] * 2),
    lambda value: value['bindings'][2]['items'][0].update(desc='Rewritten marginal view.'),
])
def test_plan_rejects_missing_duplicate_foreign_mutating_or_unsupported_decisions(change):
    doc = document(); original = deepcopy(doc); decisions = plan(doc); change(decisions)
    with pytest.raises(AnalysisError) as error:
        apply_binding_plan(doc, decisions, SOURCE, CATALOG)
    assert error.value.issues[0]['code'] == 'anchor_binding'
    assert doc == original


def test_null_multi_item_plan_does_not_replace_unmatched_original_meaning():
    doc = document(); decisions = plan(doc)
    decisions['bindings'][0]['items'] *= 2
    updated, metadata = apply_binding_plan(doc, decisions, SOURCE, CATALOG)
    assert updated == doc
    assert len(metadata['bindings'][0]['items']) == 2


def test_evidence_side_total_limit_is_rejected_without_silently_truncating():
    doc = document()
    doc['judgment']['contradiction']['bull']['evidence'].extend([
        {'desc': f'Existing evidence {index}', 'series_id': 'original-bound'} for index in range(30)])
    decisions = plan(doc)
    decisions['bindings'][0]['items'] = [{
        'desc': 'Capacity contracts.', 'quote': 'Capacity contracts in the north.',
        'series_id': 'capacity', 'reason': 'Explicit compatible field.'}] * 2
    with pytest.raises(AnalysisError):
        apply_binding_plan(doc, decisions, SOURCE, CATALOG)
    assert len(doc['judgment']['contradiction']['bull']['evidence']) == 32


def test_empty_targets_need_no_decisions_and_do_not_change_any_field():
    doc = document()
    for side in ('bull', 'bear'):
        for row in doc['judgment']['contradiction'][side]['evidence']:
            row['series_id'] = 'original-bound'
    doc['judgment']['marginal_focus'][0]['series_id'] = 'original-bound'
    doc['judgment']['cost_line']['items'][0]['series_id'] = 'original-bound'
    assert targets(doc) == []
    assert apply_binding_plan(doc, {'bindings': []}, SOURCE, CATALOG)[0] == doc


def test_binding_protocol_exposes_a_bounded_schema_and_all_targets_without_business_defaults():
    requested = targets(document()); prompt = binding_protocol(requested)
    Draft202012Validator.check_schema(BINDING_SCHEMA)
    assert Draft202012Validator(BINDING_SCHEMA).is_valid(plan())
    assert all(row['path'] in prompt and row['description'] in prompt for row in requested)
    assert 'source_unit' not in json.dumps(BINDING_SCHEMA)
    assert '真实' in prompt and 'null' in prompt
