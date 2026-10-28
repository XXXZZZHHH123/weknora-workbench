"""Synthetic numeric syntax only; no commodity, source, date or answer fixtures."""
from copy import deepcopy
from decimal import Decimal

import pytest

from tin.judgments.analysis import AnalysisError, quoted_quantities, validate_analysis
from tin.schemas.judgment import Falsifier, Threshold


@pytest.mark.parametrize('quote,expected', [
    ('between [13.5, 28.0]', ['13.5', '28.0']),
    ('[-13.5, -6.25]', ['-13.5', '-6.25']),
    ('[0, 0.125]', ['0', '0.125']),
    ('[+1.25e-3, 2.5e+2]', ['0.00125', '250']),
    ('［－１３．５，＋２８．０］', ['-13.5', '28.0']),
    ('13.5, 28.0', ['13.5', '28.0']),
    ('1,200.50 kg', ['1200.50']),
    ('−.125 kg', ['-0.125']),
    ('quantity +.125 kg', ['0.125']),
    ('quantity 12.', ['12']),
    ('-1.2e-3 kg', ['-0.0012']),
    ('1.2万 kg', ['12000']),
    ('0.12亿 kg', ['12000000']),
    ('0.12345678901234567890123456789', ['0.12345678901234567890123456789']),
    ('[1,200]', ['1', '200']),
])
def test_unambiguous_literals_keep_their_exact_decimal_values(quote, expected):
    assert quoted_quantities(quote) == {Decimal(value) for value in expected}


@pytest.mark.parametrize('quote', [
    '1,20', '1.2.3', '1e+', '--12', '+-12', 'label-12', 'code_123',
    '2024-03-31', '1/2', '1:2', '1-2', '1e3,2e3', '12abc',
    '[true, 28]', '["13", 28]', '[13, 28', '[[13, 28], 42]',
    '[13,,28]', '[13,28,]', '[NaN, 28]', '[Infinity, 28]',
    '1.2万万', '1e999999999999999999999999999999999',
])
def test_invalid_or_ambiguous_notation_never_supplies_partial_numbers(quote):
    assert quoted_quantities(quote) == set()


@pytest.mark.parametrize('quote,unit,expected', [
    ('quantity 1.2万吨', '万吨', ['1.2']),
    ('quantity 1.2万吨', '吨', ['12000']),
    ('quantity 1.2万元/吨', '万元/吨', ['1.2']),
    ('quantity 1.2万元/吨', '元/吨', ['12000']),
    ('[1.2, 2.5]万吨', '万吨', ['1.2', '2.5']),
    ('[1.2, 2.5]万吨', '吨', ['12000', '25000']),
    ('12kg', 'kg', ['12']),
    ('12%', '%', ['12']),
    ('12％', '％', ['12']),
    ('12%', 'kg', []),
    ('1.2万元/吨', '吨', []),
    ('1.2吨', '万吨', []),
    ('1.2万', 'kg', []),
])
def test_explicit_multiplier_respects_the_actual_catalog_unit(quote, unit, expected):
    assert quoted_quantities(quote, unit=unit) == {Decimal(value) for value in expected}


def test_other_explicit_catalog_units_cannot_license_the_selected_unit():
    assert quoted_quantities('quantity 1.2吨', unit='kg', units=['吨', 'kg']) == set()
    assert quoted_quantities('quantity 1.2元/吨', unit='元', units=['元', '元/吨']) == set()


def envelope(value, operator='between'):
    sid = 'synthetic-series'
    return {'judgment': {'contradiction': {}, 'thresholds': [{
        'id': 'boundary', 'name': 'Explicit observation interval', 'series_id': sid,
        'operator': operator, 'value': value, 'type': '观察位', 'horizon': '短期'}]},
        'binding_units': {sid: 'kg'}, 'rule_quotes': {}, 'unresolved': []}


def test_export_style_interval_keeps_both_endpoints_and_never_fills_missing_ones():
    quote = 'between [13.5, 28.0]'
    body = envelope([13.5, 28.0])
    body['rule_quotes']['boundary'] = quote
    before = deepcopy(body)
    result = validate_analysis(body, quote, [{'id': 'synthetic-series', 'unit': 'kg'}],
                               variety='TEST', actor='synthetic')
    assert result.payload.thresholds[0].value == [13.5, 28.0] and body == before
    for invalid in ([13.5, 29], [-13.5, 28], [28, 13.5], [13.5, 13.5]):
        body['judgment']['thresholds'][0]['value'] = invalid
        with pytest.raises(AnalysisError):
            validate_analysis(body, quote, [{'id': 'synthetic-series', 'unit': 'kg'}],
                              variety='TEST', actor='synthetic')


@pytest.mark.parametrize('value', [9007199254740993, 10 ** 23, True, '12', float('inf')])
def test_typed_rules_reject_precision_loss_and_non_numeric_values(value):
    from pydantic import ValidationError
    rule = envelope(value, '>')['judgment']['thresholds'][0]
    with pytest.raises(ValidationError):
        Threshold.model_validate(rule)


@pytest.mark.parametrize('literal', ['-0.125', '+0.125', '.125', '1.25e-3', '-2.5e+2'])
def test_falsifier_numbers_preserve_exact_supported_expression_text(literal):
    expression = 'value >= ' + literal
    rule = Falsifier.model_validate({'id': 'boundary', 'desc': 'Explicit condition',
        'series_id': 'synthetic-series', 'expression': expression, 'unit_confirmed': 'kg', 'side': 'bull'})
    assert rule.expression == expression


@pytest.mark.parametrize('literal', ['0.10000000000000001', '1e-400', '1e400',
                                   '9007199254740993', '1,200', 'NaN', '--12'])
def test_falsifier_never_rounds_underflows_or_evaluates_unsupported_notation(literal):
    from pydantic import ValidationError
    with pytest.raises(ValidationError):
        Falsifier.model_validate({'id': 'boundary', 'desc': 'Explicit condition',
            'series_id': 'synthetic-series', 'expression': 'value >= ' + literal,
            'unit_confirmed': 'kg', 'side': 'bull'})
