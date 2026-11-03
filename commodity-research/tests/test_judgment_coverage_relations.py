"""Source provenance is many-to-many, independent of presentation sections."""
from copy import deepcopy
import json

import pytest

from tin.judgments.analysis import AnalysisError, assess_draft
from tin.judgments.coverage import (coverage_issues, retain_coverage, source_blocks,
    source_output_protocol, split_coverage_document, validate_inventory)


TEXT = 'Orders weaken.\nSample dispersion remains wide.\nExternal demand and domestic financing matter.\nQuantity > 12 kg'
CATALOG = [{'id': 'real-quantity', 'name': 'Quantity', 'unit': 'kg'}]


def inventory():
    blocks = source_blocks(TEXT)
    return {'blocks': [{'id': b['id'], 'disposition': 'research', 'reason': 'A source statement.'} for b in blocks],
        'items': [{'id': 'survey_a', 'block_ids': [blocks[0]['id']], 'kind': 'survey_notes',
                   'quote': blocks[0]['text'], 'source_unit': None},
                  {'id': 'survey_b', 'block_ids': [blocks[1]['id']], 'kind': 'survey_notes',
                   'quote': blocks[1]['text'], 'source_unit': None},
                  {'id': 'pricing', 'block_ids': [blocks[2]['id']], 'kind': 'pricing_power',
                   'quote': blocks[2]['text'], 'source_unit': None},
                  {'id': 'watch', 'block_ids': [blocks[3]['id']], 'kind': 'threshold',
                   'quote': blocks[3]['text'], 'source_unit': 'kg'}]}


def document():
    return {'judgment': {'contradiction': {},
        'survey_notes': 'Orders weaken; sample dispersion remains wide.',
        'pricing_power': {'short': ['External demand', 'Domestic financing']},
        'thresholds': [{'id': 'watch', 'name': 'Quantity boundary', 'series_id': 'real-quantity',
            'operator': '>', 'value': 12, 'type': '观察位', 'horizon': '短期'}],
        'whitelist': ['real-quantity']},
        'binding_units': {'real-quantity': 'kg'}, 'rule_quotes': {'watch': 'Quantity > 12 kg'}, 'unresolved': []}


def mapping():
    return {'survey_a': '/judgment/survey_notes', 'survey_b': '/judgment/survey_notes',
        'pricing': ['/judgment/pricing_power/short/0', '/judgment/pricing_power/short/1'],
        'watch': '/judgment/thresholds/0'}


def assess(doc=None):
    return assess_draft(doc or document(), TEXT, CATALOG, variety='UNIT', actor='synthetic-user')


def test_multiple_source_statements_can_use_one_combined_survey_without_fake_pending_items():
    doc, inv, paths = document(), validate_inventory(inventory(), TEXT), mapping()
    before = deepcopy(inv)
    result, coverage = retain_coverage(assess(doc), inv, paths, TEXT, CATALOG,
        variety='UNIT', actor='synthetic-user')
    assert result.document['unresolved'] == [] and coverage['retained'] == 0
    assert result.document['judgment']['survey_notes'] == doc['judgment']['survey_notes']
    assert inv == before


def test_one_source_statement_can_map_to_multiple_pricing_leaves():
    body = {**document(), 'source_coverage': mapping()}
    doc, parsed = split_coverage_document(body, inventory())
    result, coverage = retain_coverage(assess(doc), inventory(), parsed, TEXT, CATALOG,
        variety='UNIT', actor='synthetic-user')
    item = next(item for item in coverage['items'] if item['id'] == 'pricing')
    assert item['paths'] == mapping()['pricing']
    assert item['candidate_paths'] == mapping()['pricing']
    assert item['retained'] is False and item['invalid_reason'] is None


def test_a_reference_table_classified_as_marginal_can_map_to_an_existing_whitelist_anchor():
    inv = inventory()
    inv['items'][0]['kind'] = 'marginal_focus'
    paths = mapping()
    paths['survey_a'] = '/judgment/whitelist/0'
    result, coverage = retain_coverage(assess(), inv, paths, TEXT, CATALOG,
        variety='UNIT', actor='synthetic-user')
    assert coverage['retained'] == 0 and result.document['unresolved'] == []


def test_quantitative_source_cannot_be_marked_complete_using_only_a_prose_description():
    paths = mapping()
    paths['watch'] = '/judgment/survey_notes'
    assessment = assess()
    before = deepcopy(assessment)
    issues = coverage_issues(assessment, inventory(), paths)
    assert assessment == before
    assert len(issues) == 1 and issues[0]['code'] == 'source_mapping_invalid'
    assert issues[0]['source_item_id'] == 'watch'
    assert issues[0]['proposed_paths'] == ['/judgment/survey_notes']
    assert '量化' in issues[0]['message']


def test_invalid_mapping_is_reported_for_repair_without_adding_any_unresolved_item():
    paths = mapping()
    paths['pricing'] = ['/judgment/pricing_power/short', '/judgment/missing']
    assessment = assess()
    issues = coverage_issues(assessment, inventory(), paths)
    assert len(issues) == 1 and issues[0]['source_item_id'] == 'pricing'
    assert issues[0]['invalid_reason']
    assert assessment.document['unresolved'] == []


def test_invalid_mapping_fallback_preserves_original_proposal_and_reason_for_audit():
    paths = mapping()
    paths['pricing'] = ['/judgment/pricing_power/short', '/judgment/missing']
    result, coverage = retain_coverage(assess(), inventory(), paths, TEXT, CATALOG,
        variety='UNIT', actor='synthetic-user')
    item = next(item for item in coverage['items'] if item['id'] == 'pricing')
    assert item['proposed_paths'] == paths['pricing'] and item['invalid_reason']
    assert item['retained'] is True
    assert result.document['unresolved'][0]['text'] == inventory()['items'][2]['quote']


@pytest.mark.parametrize('path', ['/judgment', '/judgment/pricing_power', '/judgment/pricing_power/short',
    '/binding_units/real-quantity', '/rule_quotes/watch', '/judgment/thresholds/0/id', '/unresolved/99'])
def test_container_metadata_or_absent_path_is_not_research_coverage(path):
    paths = mapping()
    paths['survey_a'] = path
    assert coverage_issues(assess(), inventory(), paths)


def test_output_schema_accepts_path_list_and_legacy_single_string_without_mutating_base():
    protocol = source_output_protocol(inventory())
    schema, _ = json.JSONDecoder().raw_decode(protocol.split('完整研究逻辑输出的 JSON Schema：', 1)[1])
    choices = schema['properties']['source_coverage']['properties']['pricing']['anyOf']
    assert {choice['type'] for choice in choices} == {'string', 'array', 'null'}


def test_quantitative_mapping_can_mix_a_verified_branch_and_explicit_pending_branch():
    doc = document()
    doc['unresolved'] = [{'section': 'Unresolved source', 'text': 'Quantity > 12 kg',
        'reason': 'A source condition needs review.', 'business_condition': 'Quantity > 12 kg',
        'rule_kind': 'threshold', 'series_id': None}]
    paths = mapping()
    paths['watch'] = ['/judgment/thresholds/0', '/unresolved/0']
    assert coverage_issues(assess(doc), inventory(), paths) == []


@pytest.mark.parametrize('paths', [True, [None], ['/judgment/survey_notes'] * 2,
                                 ['/judgment/survey_notes'] * 33])
def test_invalid_path_transport_stays_strict(paths):
    body = {**document(), 'source_coverage': {'survey_a': paths}}
    with pytest.raises(AnalysisError):
        split_coverage_document(body, inventory())
