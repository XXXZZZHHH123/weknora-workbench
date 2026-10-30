"""Synthetic source accounting, independent of any commodity or business keyword."""
from copy import deepcopy
from datetime import date
import json

import pytest

from tin.judgments.analysis import AnalysisError, assess_draft
from tin.judgments.coverage import (source_blocks, inventory_prompt, validate_inventory,
    coverage_protocol, source_output_protocol, split_coverage_document, retain_coverage)


TEXT = 'Capacity contracts support value.\nOrders remain weak.\nQuantity > 12 kg\nResearch background.'
CATALOG = [{'id': 'source-quantity', 'name': 'Measured quantity', 'unit': 'kg'}]


def inventory(text=TEXT):
    blocks = source_blocks(text)
    return {'blocks': [{'id': b['id'], 'disposition': 'research' if i < 3 else 'context',
                        'reason': 'An explicit research statement.' if i < 3 else 'Background only.'}
                       for i, b in enumerate(blocks)],
            'items': [{'id': 'a', 'block_ids': [blocks[0]['id']], 'quote': blocks[0]['text'],
                       'kind': 'bull_evidence', 'source_unit': None},
                      {'id': 'b', 'block_ids': [blocks[1]['id']], 'quote': blocks[1]['text'],
                       'kind': 'bear_evidence', 'source_unit': None},
                      {'id': 'c', 'block_ids': [blocks[2]['id']], 'quote': blocks[2]['text'],
                       'kind': 'threshold', 'source_unit': 'kg'}]}


def document():
    return {'judgment': {'contradiction': {'bull': {'claim': 'Positive evidence', 'evidence': []},
                'bear': {'claim': 'Negative evidence', 'evidence': []}}, 'pricing_power': {}},
            'binding_units': {}, 'rule_quotes': {}, 'unresolved': []}


def keep(doc=None, mapping=None):
    doc = document() if doc is None else doc
    first = assess_draft(doc, TEXT, CATALOG, variety='UNIT', actor='synthetic-user', written_at=date(2030, 1, 1))
    return retain_coverage(first, validate_inventory(inventory(), TEXT), mapping or {}, TEXT,
                           CATALOG, variety='UNIT', actor='synthetic-user', written_at=date(2030, 1, 1))


def test_mechanical_blocks_cover_all_nonwhitespace_including_arbitrary_formats():
    text = ' \n# Heading\r\n| field | 13 |\n' + 'x' * 5000 + '\n'
    blocks = source_blocks(text)
    assert all(b['text'] == text[b['start']:b['end']] for b in blocks)
    assert ''.join(b['text'] for b in blocks).replace('\r', '').replace('\n', '').strip() == text.replace('\r', '').replace('\n', '').strip()
    assert len({b['id'] for b in blocks}) == len(blocks)
    assert max(len(b['text']) for b in blocks) <= 2048


def test_inventory_prompt_preserves_every_source_block_and_asks_model_to_classify():
    prompt = inventory_prompt(TEXT)
    assert 'research-source-inventory.v1' in prompt
    assert json.loads(prompt.split('完整原文段块：', 1)[1]) == source_blocks(TEXT)
    assert 'source_unit' in prompt and 'context' in prompt


def test_complete_inventory_has_exact_source_quotes_and_explicit_units():
    result = validate_inventory(inventory(), TEXT)
    assert result == inventory()
    assert result['items'][2]['source_unit'] == 'kg'


@pytest.mark.parametrize('change', [
    lambda d: d['blocks'].pop(),
    lambda d: d['blocks'].append(deepcopy(d['blocks'][0])),
    lambda d: d['items'].pop(0),
    lambda d: d['items'][0].update(quote='Invented source'),
    lambda d: d['items'][0].update(block_ids=['s9999']),
    lambda d: d['items'][0].update(block_ids=['s0002']),
    lambda d: d['items'][0].update(source_unit='USD/ton'),
    lambda d: d['items'][0].update(kind='keyword-special-case'),
    lambda d: d['items'][1].update(id='a'),
    lambda d: d['blocks'][3].update(reason=''),
    lambda d: d['blocks'][0].update(disposition=[]),
])
def test_inventory_rejects_missing_blocks_items_fabricated_quotes_units_and_identity(change):
    value = inventory()
    change(value)
    with pytest.raises(AnalysisError):
        validate_inventory(value, TEXT)


def test_analysis_protocol_includes_complete_inventory_without_business_defaults():
    protocol = coverage_protocol(validate_inventory(inventory(), TEXT))
    assert 'source_coverage' in protocol
    assert json.loads(protocol.split('完整原文条目清单：', 1)[1]) == inventory()['items']


def test_extended_output_schema_and_top_level_instruction_agree_on_coverage():
    from tin.judgments.output_schema import OUTPUT_SCHEMA
    protocol = source_output_protocol(inventory())
    assert '字段只有 judgment、binding_units、rule_quotes、unresolved、source_coverage。' in protocol
    schema, _ = json.JSONDecoder().raw_decode(protocol.split('完整研究逻辑输出的 JSON Schema：', 1)[1])
    assert schema['required'].count('source_coverage') == 1
    assert set(schema['properties']['source_coverage']['properties']) == {'a', 'b', 'c'}
    assert 'source_coverage' not in OUTPUT_SCHEMA['properties']


def test_split_separates_verified_inventory_mapping_from_legacy_strict_document():
    body = {**document(), 'source_coverage': {'a': '/judgment/contradiction/bull/evidence/0'}}
    original = deepcopy(body)
    doc, mapping = split_coverage_document(body, inventory())
    assert doc == document() and body == original
    assert mapping == original['source_coverage']
    with pytest.raises(AnalysisError):
        split_coverage_document({**body, 'source_coverage': {'foreign': '/unresolved/0'}}, inventory())


def test_omitted_semantic_items_remain_in_multiside_evidence_and_pending_rules():
    result, coverage = keep()
    sides = result.result.payload.contradiction
    assert sides.bull.evidence[0].desc == inventory()['items'][0]['quote']
    assert sides.bear.evidence[0].desc == inventory()['items'][1]['quote']
    assert sides.bull.evidence[0].series_id is None
    assert sides.bear.evidence[0].series_id is None
    pending = result.result.payload.unstructured[0]
    assert pending.business_condition == inventory()['items'][2]['quote']
    assert pending.rule_kind == 'threshold' and pending.series_id is None
    assert result.result.payload.thresholds == []
    assert coverage['total'] == 3 and coverage['retained'] == 3
    assert any(issue['code'] == 'source_item_retained' for issue in result.issues)


def test_already_mapped_evidence_is_not_duplicated_and_carries_source_unit_for_review():
    doc = document()
    doc['judgment']['contradiction']['bull']['evidence'] = [{'desc': 'Contraction', 'series_id': None}]
    result, coverage = keep(doc, {'a': '/judgment/contradiction/bull/evidence/0'})
    assert len(result.result.payload.contradiction.bull.evidence) == 1
    assert coverage['items'][0]['path'] == '/judgment/contradiction/bull/evidence/0'
    assert coverage['items'][0]['candidate_path'] == '/judgment/contradiction/bull/evidence/0'
    assert coverage['items'][0]['source_unit'] is None
    assert coverage['items'][2]['source_unit'] == 'kg'


@pytest.mark.parametrize('target', ['/judgment',
    '/binding_units', '/judgment/contradiction/bear/evidence/0', '/judgment/contradiction/bull/evidence/99'])
def test_container_metadata_and_missing_mapping_never_hide_an_item(target):
    result, coverage = keep(mapping={'a': target})
    assert result.result.payload.contradiction.bull.evidence[0].desc == inventory()['items'][0]['quote']
    assert coverage['items'][0]['retained'] is True


def test_demotion_cannot_make_threshold_inventory_point_to_a_different_shifted_rule():
    doc = document()
    doc['judgment']['thresholds'] = [{'id': 'invalid', 'name': 'Quantity', 'series_id': 'source-quantity',
        'operator': '>', 'value': 999, 'type': '观察位', 'horizon': '短期'}]
    doc['binding_units'] = {'source-quantity': 'kg'}
    doc['rule_quotes'] = {'invalid': 'Quantity > 12 kg'}
    result, coverage = keep(doc, {'c': '/judgment/thresholds/0'})
    assert result.result.payload.thresholds == []
    assert any(item.business_condition == 'Quantity > 12 kg' for item in result.result.payload.unstructured)
    assert coverage['items'][2]['path'].startswith('/unresolved/')


def test_retained_items_stay_in_review_prefix_before_program_quarantine_notes():
    doc = document()
    doc['judgment']['thresholds'] = [{'id': 'invalid', 'name': 'Quantity', 'series_id': 'source-quantity',
        'operator': '>', 'value': 999, 'type': '观察位', 'horizon': '短期'}]
    doc['binding_units'] = {'source-quantity': 'kg'}
    doc['rule_quotes'] = {'invalid': 'Quantity > 12 kg'}
    result, coverage = keep(doc, {'c': '/judgment/thresholds/0'})
    # The review intentionally strips program-only quarantine diagnostics by
    # keeping this prefix; retained source statements must survive that cut.
    reviewed = result.document['unresolved'][:len(result.candidate_document['unresolved'])]
    assert any(item['text'] == inventory()['items'][2]['quote'] for item in reviewed)
    pointer = int(coverage['items'][2]['candidate_path'].split('/')[-1])
    assert result.candidate_document['unresolved'][pointer]['text'] == reviewed[pointer]['text']


def test_multiple_source_items_can_share_a_model_declared_combined_card():
    inv = inventory()
    inv['items'][1]['kind'] = 'bull_evidence'
    doc = document()
    doc['judgment']['contradiction']['bull']['evidence'] = [{'desc': 'Contraction', 'series_id': None}]
    first = assess_draft(doc, TEXT, CATALOG, variety='UNIT', actor='synthetic-user')
    result, coverage = retain_coverage(first, validate_inventory(inv, TEXT),
        {'a': '/judgment/contradiction/bull/evidence/0', 'b': '/judgment/contradiction/bull/evidence/0'},
        TEXT, CATALOG, variety='UNIT', actor='synthetic-user')
    assert len(result.result.payload.contradiction.bull.evidence) == 1
    assert coverage['retained'] == 1
    assert all(not item['retained'] for item in coverage['items'][:2])


def test_model_can_reorganize_descriptions_but_cannot_hide_quantitative_conditions_in_a_summary():
    result, coverage = keep(mapping={'a': '/judgment/contradiction/bull/claim',
                                     'b': '/judgment/contradiction/bear/claim',
                                     'c': '/judgment/contradiction/bull/claim'})
    assert len(result.result.payload.contradiction.bull.evidence) == 0
    assert len(result.result.payload.contradiction.bear.evidence) == 0
    assert len(result.result.payload.unstructured) == 1
    assert coverage['retained'] == 1


def test_rules_must_map_to_their_own_source_condition_even_if_both_are_valid():
    text = TEXT + '\nQuantity < 18 kg'
    inv = inventory()
    inv['blocks'].append({'id': source_blocks(text)[-1]['id'], 'disposition': 'research', 'reason': 'A separate boundary.'})
    inv['items'].append({'id': 'd', 'block_ids': [source_blocks(text)[-1]['id']],
        'quote': 'Quantity < 18 kg', 'kind': 'threshold', 'source_unit': 'kg'})
    doc = document()
    doc['judgment']['thresholds'] = [{'id': 'lower', 'name': 'Separate boundary', 'series_id': 'source-quantity',
        'operator': '<', 'value': 18, 'type': '观察位', 'horizon': '短期'}]
    doc['binding_units'] = {'source-quantity': 'kg'}
    doc['rule_quotes'] = {'lower': 'Quantity < 18 kg'}
    assessment = assess_draft(doc, text, CATALOG, variety='UNIT', actor='synthetic-user')
    result, coverage = retain_coverage(assessment, validate_inventory(inv, text),
        {'c': '/judgment/thresholds/0', 'd': '/judgment/thresholds/0'},
        text, CATALOG, variety='UNIT', actor='synthetic-user')
    assert result.result.payload.thresholds[0].value == 18
    assert any(row.business_condition == inventory()['items'][2]['quote'] for row in result.result.payload.unstructured)
    assert coverage['items'][2]['retained'] is True
    assert coverage['items'][3]['retained'] is False


def test_out_of_capacity_rejects_explicitly_without_mutating_earlier_assessment():
    doc = document()
    doc['judgment']['contradiction']['bull']['evidence'] = [
        {'desc': f'Existing independent source statement {i}', 'series_id': None} for i in range(32)]
    assessment = assess_draft(doc, TEXT, CATALOG, variety='UNIT', actor='synthetic-user')
    before = deepcopy(assessment)
    with pytest.raises(AnalysisError, match='未静默省略'):
        retain_coverage(assessment, inventory(), {}, TEXT, CATALOG, variety='UNIT', actor='synthetic-user')
    assert assessment == before


def test_coverage_does_not_mutate_model_response_or_inventory():
    inv, doc = inventory(), document()
    assessment = assess_draft(doc, TEXT, CATALOG, variety='UNIT', actor='synthetic-user')
    before, original_inv = deepcopy(assessment), deepcopy(inv)
    retain_coverage(assessment, inv, {}, TEXT, CATALOG, variety='UNIT', actor='synthetic-user')
    assert assessment == before and inv == original_inv


def test_a_source_quote_can_span_multiple_physical_lines_when_all_locations_are_declared():
    inv = inventory()
    inv['items'][0]['quote'] = source_blocks(TEXT)[0]['text'] + source_blocks(TEXT)[1]['text']
    inv['items'][0]['block_ids'].append('s0002')
    assert validate_inventory(inv, TEXT)['items'][0]['quote'] in TEXT


def test_repeated_source_wording_is_located_within_its_declared_late_block():
    text = '\n'.join('Repeated source wording.' for _ in range(150))
    blocks = source_blocks(text)
    value = {'blocks': [{'id': block['id'], 'disposition': 'research' if i == 149 else 'context',
                        'reason': 'The relevant supplied statement.'} for i, block in enumerate(blocks)],
        'items': [{'id': 'late', 'block_ids': [blocks[-1]['id']], 'quote': 'Repeated source wording.',
                   'kind': 'other', 'source_unit': None}]}
    assert validate_inventory(value, text)['items'][0]['block_ids'] == ['s0150']


@pytest.mark.parametrize('source', [' ', 'x' * (48 * 1024 + 1), '\n'.join('x' for _ in range(513))])
def test_source_capacity_is_explicit_and_never_truncated(source):
    with pytest.raises(AnalysisError):
        source_blocks(source)
