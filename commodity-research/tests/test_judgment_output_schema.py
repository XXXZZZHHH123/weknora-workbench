"""Exercise the actual prompt schema with an independent JSON Schema validator."""
from copy import deepcopy
from datetime import date
import json

from jsonschema import Draft202012Validator
import pytest

from tin.judgments.analysis import ALLOWED_FIELDS
from tin.judgments.prompt import OUTPUT_PROTOCOL
from tin.schemas.judgment import JudgmentIn


MARKER = '完整研究逻辑输出的 JSON Schema：\n'
SCHEMA, _ = json.JSONDecoder().raw_decode(OUTPUT_PROTOCOL.split(MARKER, 1)[1])
VALIDATOR = Draft202012Validator(SCHEMA)


def document():
    return {'judgment': {'contradiction': {}, 'pricing_power': {}},
            'binding_units': {}, 'rule_quotes': {}, 'unresolved': []}


def test_prompt_schema_covers_the_complete_model_surface_without_server_fields():
    Draft202012Validator.check_schema(SCHEMA)
    assert set(SCHEMA['properties']['judgment']['properties']) == ALLOWED_FIELDS
    body = document()
    before = deepcopy(body)
    VALIDATOR.validate(body)
    assert body == before  # A schema never fills a category, fact, or viewpoint.
    JudgmentIn.model_validate({**body['judgment'], 'variety': 'TEST',
                              'author': 'test', 'written_at': date(2030, 1, 1)})


@pytest.mark.parametrize('name', [
    'variety', 'author', 'written_at', 'review_period_days', 'source_note', 'unstructured'])
def test_server_owned_fields_cannot_enter_a_model_judgment(name):
    body = document()
    body['judgment'][name] = None
    assert not VALIDATOR.is_valid(body)


@pytest.mark.parametrize('field,value', [
    ('pricing_power', None), ('pricing_power', []), ('pricing_power', ''),
    ('pricing_power', False), ('pricing_power', 0),
    ('pricing_power', {'short': 'claim'}), ('pricing_power', {'long': None}),
    ('pricing_power', {'sources': [None]}),
    ('pricing_power', {'category': 'invented-category'}),
    ('cost_line', {'type': 'cash或full或null'}),
    ('marginal_focus', None), ('whitelist', None),
    ('time_dimension', {'short': []}),
    ('tone', 'invented-tone'), ('paradigm', 'invented-paradigm'),
])
def test_invalid_description_shapes_and_enums_are_rejected(field, value):
    body = document()
    body['judgment'][field] = value
    assert not VALIDATOR.is_valid(body)


def test_typed_empty_and_nullable_descriptions_are_valid_without_claims():
    body = document()
    body['judgment'].update(
        pricing_power={'category': None, 'short': [], 'long': [], 'key': None, 'sources': []},
        cost_line={'type': None, 'value': None, 'source': None, 'items': [], 'note': None},
        time_dimension={'short': None, 'long': None},
        marginal_focus=[], whitelist=[], tone=None, paradigm=None)
    VALIDATOR.validate(body)


@pytest.mark.parametrize('section,field,value', [
    ('evidence', 'value', 12), ('evidence', 'as_of', 'unverified-date'),
    ('evidence', 'source', 'unverified-source'),
    ('cost', 'value', 12), ('cost', 'source', 'unverified-source'),
    ('cost_item', 'value', 12),
])
def test_model_facts_remain_forbidden_even_when_the_domain_accepts_their_types(section, field, value):
    body = document()
    if section == 'evidence':
        item = {'desc': 'Unverified evidence', 'value': None, 'as_of': None, 'source': None}
        body['judgment']['contradiction'] = {'bull': {'evidence': [item]}}
    elif section == 'cost':
        item = {'value': None, 'source': None}
        body['judgment']['cost_line'] = item
    else:
        item = {'desc': 'Unverified cost component', 'value': None}
        body['judgment']['cost_line'] = {'items': [item]}
    VALIDATOR.validate(body)
    item[field] = value
    assert not VALIDATOR.is_valid(body)


@pytest.mark.parametrize('metadata,valid', [
    ({}, False), ({'business_condition': None}, False),
    ({'business_condition': None, 'rule_kind': None}, True),
    ({'business_condition': 'Original condition', 'rule_kind': 'threshold'}, True),
    ({'business_condition': 'Original condition', 'rule_kind': 'falsifier'}, True),
    ({'business_condition': 'Original condition', 'rule_kind': None}, False),
    ({'business_condition': None, 'rule_kind': 'threshold'}, False),
    ({'business_condition': '', 'rule_kind': 'falsifier'}, False),
    ({'business_condition': 'Original condition', 'rule_kind': 'invented-kind'}, False),
])
def test_model_pending_items_require_explicit_paired_business_metadata(metadata, valid):
    body = document()
    body['unresolved'] = [{'section': 'Observation', 'text': 'Pending content',
                           'reason': 'Missing evidence', **metadata}]
    assert VALIDATOR.is_valid(body) is valid


@pytest.mark.parametrize('operator,value,valid', [
    ('>', 12, True), ('>', '12', False), ('>', True, False), ('>', None, False),
    ('>', [12, 13], False), ('between', [12, 13], True),
    ('between', [12], False), ('between', [12, 13, 14], False),
    ('between', [12, '13'], False), ('between', 12, False),
])
def test_threshold_schema_preserves_numeric_scalar_and_interval_protocol(operator, value, valid):
    body = document()
    body['judgment']['thresholds'] = [{
        'id': 'watch', 'name': 'Boundary', 'series_id': 'synthetic-id',
        'operator': operator, 'value': value, 'type': '观察位', 'horizon': '短期'}]
    assert VALIDATOR.is_valid(body) is valid
