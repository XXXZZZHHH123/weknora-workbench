"""Synthetic interval syntax and exact unit evidence; no business-specific parser."""
from decimal import Decimal

import pytest

from tin.judgments.analysis import AnalysisError, validate_analysis
from tin.judgments.numbers import quoted_quantities


@pytest.mark.parametrize('quote,unit,expected', [
    ('Quantity 12-18 kg.', 'kg', ('12', '18')),
    ('Quantity 12–18 kg.', 'kg', ('12', '18')),
    ('Quantity 12—18 kg.', 'kg', ('12', '18')),
    ('Quantity 12~18 kg.', 'kg', ('12', '18')),
    ('Quantity 12～18 kg.', 'kg', ('12', '18')),
    ('数量12至18kg。', 'kg', ('12', '18')),
    ('数量12到18kg。', 'kg', ('12', '18')),
    ('Quantity 12 to 18 kg.', 'kg', ('12', '18')),
    ('Quantity -18--12 kg.', 'kg', ('-18', '-12')),
    ('Quantity −18–−12 kg.', 'kg', ('-18', '-12')),
    ('Quantity -0.125 – +0.25 kg.', 'kg', ('-0.125', '0.25')),
    ('Quantity 1.25e-3–2.5e-3 kg.', 'kg', ('0.00125', '0.0025')),
    ('Quantity 1,200.5-2,800.25 kg.', 'kg', ('1200.5', '2800.25')),
    ('Quantity 1.2-2.5万吨。', '吨', ('12000', '25000')),
    ('Quantity 1.2-2.5万吨。', '万吨', ('1.2', '2.5')),
    ('Quantity 4-8万元/吨。', '元/吨', ('40000', '80000')),
    ('Quantity 4-8万元/吨。', '万元/吨', ('4', '8')),
    ('Quantity 1万-2万吨。', '吨', ('10000', '20000')),
    ('Quantity 12kg-18kg.', 'kg', ('12', '18')),
    ('Quantity 0.123456789012345678901-0.223456789012345678901 kg.', 'kg',
        ('0.123456789012345678901', '0.223456789012345678901')),
])
def test_connected_interval_keeps_both_exact_endpoints_with_a_complete_source_unit(quote, unit, expected):
    assert quoted_quantities(quote, unit=unit) == {Decimal(value) for value in expected}


@pytest.mark.parametrize('quote,expected', [
    ('4-8万', ('40000', '80000')),
    ('4万-8万', ('40000', '80000')),
    ('0.12-0.25亿', ('12000000', '25000000')),
    ('-18--12千', ('-18000', '-12000')),
])
def test_closed_scale_can_inherit_an_unscaled_catalog_unit_without_changing_the_digits(quote, expected):
    assert quoted_quantities(quote) == {Decimal(value) for value in expected}
    assert quoted_quantities(quote, unit='kg') == {Decimal(value) for value in expected}
    assert quoted_quantities(quote, unit='万kg') == set()


@pytest.mark.parametrize('quote', [
    '1.2万人民币', '1.2万未知单位', '1.2万吨', '1.2万kg/m', '1.2万%',
    '1.2万 / 2', '1.2万万', '1.2万1',
    '1.2万.3', '1.2万,3', '1.2万,kg', '1.2万..',
])
def test_scale_inheritance_never_replaces_an_explicit_or_unknown_source_unit(quote):
    assert quoted_quantities(quote, unit='kg', units=['kg', '吨', 'kg/m', '%']) == set()


@pytest.mark.parametrize('quote', ['1.2万', '1.2万。', '1.2万, ', '1.2万. '])
def test_closed_scalar_scale_inherits_only_an_unscaled_catalog_unit(quote):
    assert quoted_quantities(quote, unit='kg') == {Decimal(12000)}
    assert quoted_quantities(quote, unit='万kg') == set()


@pytest.mark.parametrize('quote', [
    '18-12 kg', '12-12 kg', '-12--18 kg', '1-2', '2024-03-31',
    '12-18-24 kg', '12/18 kg', '12:18 kg', '12*18 kg',
    '12-18unknown', '12kg-18unknown', '12-18kgm',
    '12-18kg/m', '12-18kg·m', '12-18吨公里',
    '12吨-18kg', '12-18%', '12–4 kg', '–12 kg',
    'label-12-18 kg', 'code_12-18kg', '²12-18 kg',
    '1\u202f000-2\u202f000 kg', '[12-18] kg', '[[12-18],24] kg',
    '12-18e+kg', '1.2万万-2.5万kg',
    '12to18kg',
])
def test_malformed_reversed_ratio_or_unknown_unit_never_recovers_partial_endpoints(quote):
    assert quoted_quantities(quote, unit='kg', units=['kg', '吨', '吨公里', 'kg/m', 'kg·m', '%']) == set()


def test_analysis_accepts_exact_interval_and_does_not_mutate_the_quote_or_invent_a_unit():
    quote = 'Quantity 1.25–2.5万kg.'
    sid = 'synthetic-range'
    body = {'judgment': {'contradiction': {}, 'thresholds': [{
        'id': 'range', 'name': 'Synthetic range', 'series_id': sid,
        'operator': 'between', 'value': [12500, 25000], 'type': '观察位', 'horizon': '短期'}]},
        'binding_units': {sid: 'kg'}, 'rule_quotes': {'range': quote}, 'unresolved': []}
    result = validate_analysis(body, quote, [{'id': sid, 'unit': 'kg'}], variety='TEST', actor='synthetic')
    assert result.payload.thresholds[0].value == [12500, 25000]
    assert body['rule_quotes']['range'] == quote
    source_with_unknown_unit = 'Quantity 1.25–2.5万 unknown.'
    body['rule_quotes']['range'] = source_with_unknown_unit
    with pytest.raises(AnalysisError):
        validate_analysis(body, source_with_unknown_unit, [{'id': sid, 'unit': 'kg'}], variety='TEST', actor='synthetic')


def test_an_array_mask_does_not_license_a_range_attached_to_the_closing_bracket():
    assert quoted_quantities('[1,2]12-18 kg', unit='kg') == set()


def test_unit_iterators_keep_their_full_compound_evidence_for_each_numeric_pass():
    assert quoted_quantities('Quantity 12 kg m.', unit='kg', units=iter(['kg', 'kg m'])) == set()
