"""Local unit confirmation cannot manufacture numeric or period evidence."""
from copy import deepcopy
from datetime import date

import pytest

from tin.judgments.analysis import AnalysisError, validate_analysis


SID = 'synthetic-unit-confirmation-quantity'
ACTOR = 'synthetic-reviewer'


def declaration():
    return {'series_id': SID, 'unit': '吨', 'basis': 'Checked the original quantity header.',
        'scope_note': '', 'confirmed_by': ACTOR, 'confirmed_at': '2033-01-02T00:00:00+00:00',
        'catalog_version': 'synthetic-source-version'}


def document(source, periods=1):
    return {'judgment': {'contradiction': {}, 'falsifiers': [{
        'id': 'quantity_review', 'desc': 'Review the explicit quantity condition.',
        'series_id': SID, 'expression': 'value >= 10000', 'unit_confirmed': '吨',
        'consecutive_periods': periods, 'side': 'bull', 'caliber_confirmation': declaration()}]},
        'binding_units': {SID: '吨'}, 'rule_quotes': {'quantity_review': source}, 'unresolved': []}


def validate(source, periods=1):
    body = document(source, periods)
    catalog = [{'id': SID, 'name': 'Synthetic measured quantity', 'unit': None}]
    before = deepcopy(catalog)
    result = validate_analysis(body, source, catalog, variety='UNIT', actor=ACTOR,
        written_at=date(2033, 1, 2), caliber_confirmations={'quantity_review': declaration()})
    assert catalog == before
    return result


def test_one_period_is_the_default_and_needs_no_invented_literal_one():
    parsed = validate('Quantity >= 10,000吨.')
    assert parsed.payload.falsifiers[0].consecutive_periods == 1
    assert parsed.payload.falsifiers[0].expression == 'value >= 10000'
    assert parsed.payload.falsifiers[0].caliber_confirmation.unit == '吨'


def test_explicit_decimal_multiplier_survives_a_rule_local_unit_declaration():
    parsed = validate('Quantity >= 1万吨.')
    assert parsed.payload.falsifiers[0].expression == 'value >= 10000'
    assert parsed.binding_units == {SID: '吨'}


def test_unit_confirmation_does_not_supply_an_absent_comparison_number():
    with pytest.raises(AnalysisError) as caught:
        validate('Review the observed quantity after its next release.')
    assert caught.value.issues[0]['code'] == 'number_not_in_source'


def test_multiple_periods_still_need_their_own_explicit_source_evidence():
    with pytest.raises(AnalysisError) as caught:
        validate('Quantity >= 10,000吨.', periods=2)
    assert caught.value.issues[0]['code'] == 'number_not_in_source'
    assert validate('Quantity >= 10,000吨; 连续2期。', periods=2).payload.falsifiers[0].consecutive_periods == 2
