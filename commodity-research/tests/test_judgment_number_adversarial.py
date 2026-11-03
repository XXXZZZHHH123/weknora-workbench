"""Synthetic adversarial numeric syntax and unit boundaries, without model calls."""
from decimal import Decimal

import pytest

from tin.judgments.analysis import AnalysisError, validate_analysis
from tin.judgments.numbers import quoted_quantities


@pytest.mark.parametrize('quote', [
    '12²', '12₂', '12①', '½', '12½', '3⁄4', '𝟙𝟚', '[1², 4]',
])
def test_compatibility_glyphs_never_create_plain_numeric_evidence(quote):
    assert quoted_quantities(quote) == set()


@pytest.mark.parametrize('quote,unit,units', [
    ('12吨公里', '吨', ['吨', '吨公里']),
    ('12千瓦时', '千瓦', ['千瓦', '千瓦时']),
    ('12千瓦时', '瓦', ['瓦', '千瓦时']),
    ('12 kg·m', 'kg', ['kg', 'kg·m']),
    ('12万kg·m', 'kg', ['kg', 'kg·m']),
    ('12 kg / m', 'kg', ['kg', 'kg/m']),
    ('12%%', '%', ['%']),
    ('12%‰', '%', ['%', '‰']),
    ('12‰', '%', ['%']),
    ('12%吨', '%', ['%', '吨']),
    ('12百万', 'kg', ['kg']),
])
def test_compound_conflicting_or_unsupported_units_do_not_license_a_prefix(quote, unit, units):
    assert quoted_quantities(quote, unit=unit, units=units) == set()


@pytest.mark.parametrize('quote', [
    '12\u202f345 kg', '12–4 kg', '–12 kg',
])
def test_unsupported_numeric_connectors_do_not_supply_unsigned_or_partial_values(quote):
    assert quoted_quantities(quote, unit='kg') == set()


@pytest.mark.parametrize('quote,value,unit,other_units', [
    ('12² kg', 122, 'kg', []),
    ('12千瓦时', 12000, '瓦', ['千瓦时']),
    ('12‰', 12, '%', []),
])
def test_numeric_boundary_failures_are_rejected_by_analysis(quote, value, unit, other_units):
    sid = 'synthetic-series'
    body = {'judgment': {'contradiction': {}, 'thresholds': [{
        'id': 'boundary', 'name': 'Synthetic boundary', 'series_id': sid,
        'operator': '>', 'value': value, 'type': '观察位', 'horizon': '短期'}]},
        'binding_units': {sid: unit}, 'rule_quotes': {'boundary': quote}, 'unresolved': []}
    catalog = [{'id': sid, 'unit': unit}]
    catalog.extend({'id': f'other-{index}', 'unit': other} for index, other in enumerate(other_units))
    with pytest.raises(AnalysisError):
        validate_analysis(body, quote, catalog, variety='TEST', actor='synthetic')


def test_fullwidth_plain_numeric_syntax_remains_supported():
    assert quoted_quantities('［－１３．５，＋２８．０］') == {Decimal('-13.5'), Decimal('28.0')}
