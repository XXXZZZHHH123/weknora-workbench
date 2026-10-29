"""A shortened quote cannot erase explicit numeric or unit context."""
from copy import deepcopy

import pytest

from tin.judgments.analysis import AnalysisError, assess_draft, validate_analysis


def envelope(quote, unit, value=12, *, kind='thresholds'):
    rule = {'id': 'synthetic', 'name': 'Synthetic condition', 'series_id': 'synthetic',
        'operator': 'between' if isinstance(value, list) else '>', 'value': value,
        'type': '观察位', 'horizon': '短期'}
    if kind == 'falsifiers':
        rule = {'id': 'synthetic', 'desc': 'Synthetic condition', 'series_id': 'synthetic',
            'expression': 'value > ' + str(value), 'unit_confirmed': unit, 'side': 'bull'}
    return {'judgment': {'contradiction': {}, kind: [rule]}, 'binding_units': {'synthetic': unit},
        'rule_quotes': {'synthetic': quote}, 'unresolved': []}


def catalog(unit, *other_units):
    return [{'id': 'synthetic', 'name': 'Synthetic quantity', 'unit': unit},
        *[{'id': 'other-' + str(index), 'name': 'Other quantity', 'unit': other}
            for index, other in enumerate(other_units)]]


@pytest.mark.parametrize('source,quote,unit,other_units', [
    ('费用超过12元/千瓦时需要复核。', '费用超过12元/千瓦', '元/千瓦', []),
    ('运输超过12吨公里需要复核。', '运输超过12吨', '吨', []),
    ('产出超过12吨当量需要复核。', '产出超过12吨', '吨', []),
    ('容量超过12安时需要复核。', '容量超过12安', '安', ['安时']),
    ('数量超过12kg/m2。', '数量超过12kg/m', 'kg/m', ['kg/m2']),
    ('数量超过12kg per year。', '数量超过12kg', 'kg', ['kg per year']),
    ('数量超过12吨。', '数量超过12', 'kg', ['吨']),
    ('数量超过12kg。', '12', '吨', ['kg']),
    ('数量超过12 ounces。', '12', 'kg', []),
    ('数量超过12(kg)。', '12', '吨', ['kg']),
    ('数量超过12.5kg。', '数量超过12', 'kg', []),
    ('数量超过120kg。', '数量超过12', 'kg', []),
    ('数量超过12e3kg。', '数量超过12', 'kg', []),
    ('数量超过12万。', '数量超过12', 'kg', []),
    ('数量超过112kg。', '12kg', 'kg', []),
    ('首项12kg；次项12吨。', '12', 'kg', ['吨']),
    ('首项12 kg，次项12吨。', '首项12 kg，次项12', 'kg', ['吨']),
])
@pytest.mark.parametrize('kind', ['thresholds', 'falsifiers'])
def test_short_quote_cannot_erase_explicit_source_quantity_context(source, quote, unit, other_units, kind):
    document = envelope(quote, unit, kind=kind)
    before = deepcopy(document)
    with pytest.raises(AnalysisError) as error:
        validate_analysis(document, source, catalog(unit, *other_units), variety='TEST', actor='synthetic')
    assert error.value.issues[0]['code'] == 'unit_context'
    assert '原文' in str(error.value) and '单位' in str(error.value)
    assert document == before


@pytest.mark.parametrize('source,quote,unit,value,other_units', [
    ('数量超过12kg。', '数量超过12kg', 'kg', 12, ['吨']),
    ('数量超过12kg，下一项20吨。', '数量超过12kg', 'kg', 12, ['吨']),
    ('数量超过12 kg 时复核。', '12 kg', 'kg', 12, ['吨']),
    ('数量超过12 kg，下一项20吨。', '12', 'kg', 12, ['吨']),
    ('数量超过12 kg/m2。', '12', 'kg/m2', 12, ['kg']),
    ('数量超过12 kg per year。', '12', 'kg per year', 12, ['kg']),
    ('数量为[12,18] kg。', '数量为[12,18]', 'kg', [12, 18], ['吨']),
    ('指数>19或另一个条件。', '指数>19', '点', 19, []),
    ('指数>19或SPX破20日线。', '指数>19', '点', 19, []),
    ('首项12 kg；次项12 kg。', '12', 'kg', 12, ['吨']),
])
def test_full_units_and_existing_bare_number_inheritance_remain_usable(source, quote, unit, value, other_units):
    result = validate_analysis(envelope(quote, unit, value), source, catalog(unit, *other_units),
        variety='TEST', actor='synthetic')
    assert result.payload.thresholds[0].value == value


def test_rejected_context_keeps_the_typed_candidate_and_its_exact_number():
    source = '费用超过12元/千瓦时需要复核。'
    document = envelope('费用超过12元/千瓦', '元/千瓦')
    result = assess_draft(document, source, catalog('元/千瓦'), variety='TEST', actor='synthetic')
    assert result.document['judgment']['thresholds'] == []
    assert result.candidate_document['judgment']['thresholds'] == document['judgment']['thresholds']
    assert result.candidate_document['judgment']['thresholds'][0]['value'] == 12
    assert result.issues[0]['code'] == 'unit_context'
    assert result.result.payload.unstructured[0].business_condition == document['rule_quotes']['synthetic']


def test_same_digits_in_an_earlier_quantity_cannot_lend_evidence_to_a_truncated_array():
    source = '首项12 kg，第二项18 kg，后项[12,18]吨。'
    quote = '首项12 kg，第二项18 kg，后项[12,18]'
    with pytest.raises(AnalysisError) as error:
        validate_analysis(envelope(quote, 'kg', [12, 18]), source, catalog('kg', '吨'),
            variety='TEST', actor='synthetic')
    assert error.value.issues[0]['code'] == 'unit_context'


def test_consecutive_count_cannot_be_a_truncated_prefix_of_the_original_count():
    source = 'Quantity exceeds 12 kg for 20 periods.'
    document = envelope('Quantity exceeds 12 kg for 2', 'kg', kind='falsifiers')
    document['judgment']['falsifiers'][0]['consecutive_periods'] = 2
    with pytest.raises(AnalysisError) as error:
        validate_analysis(document, source, catalog('kg'), variety='TEST', actor='synthetic')
    assert error.value.issues[0]['code'] == 'unit_context'


def test_complete_unit_followed_by_ambiguous_chinese_remains_a_visible_candidate():
    source = '数量超过12吨时复核。'
    result = assess_draft(envelope('数量超过12吨', '吨'), source, catalog('吨'),
        variety='TEST', actor='synthetic')
    assert result.document['judgment']['thresholds'] == []
    assert result.candidate_document['judgment']['thresholds'][0]['value'] == 12
    assert result.issues[0]['code'] == 'unit_context'


def test_closed_scaled_range_keeps_catalog_unit_inheritance_before_prose():
    source = '观察区间40-41万(补充说明)。'
    result = validate_analysis(envelope('观察区间40-41万', '元/吨', [400000, 410000]), source,
        catalog('元/吨'), variety='TEST', actor='synthetic')
    assert result.payload.thresholds[0].value == [400000, 410000]


def test_overly_short_quote_with_excessive_occurrences_is_not_arbitrarily_disambiguated():
    source = '12 kg；' * 129
    with pytest.raises(AnalysisError) as error:
        validate_analysis(envelope('12', 'kg'), source, catalog('kg'), variety='TEST', actor='synthetic')
    assert error.value.issues[0]['code'] == 'unit_context'
