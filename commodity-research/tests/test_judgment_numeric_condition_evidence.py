"""Generic scale, mathematical-zero and observation-period evidence boundaries."""
from copy import deepcopy
from decimal import Decimal

import pytest

from tin.judgments.analysis import AnalysisError, validate_analysis
from tin.judgments.numbers import quoted_quantities


SID = 'synthetic-measurement'


def check(source, *, quote=None, unit='kg', frequency='周', expression='change > 0', periods=1,
          value=None, operator='<'):
    quote = source if quote is None else quote
    if value is None:
        kind, rule = 'falsifiers', {'id': 'condition', 'desc': 'Synthetic condition',
            'series_id': SID, 'expression': expression, 'unit_confirmed': unit,
            'consecutive_periods': periods, 'side': 'bull'}
    else:
        kind, rule = 'thresholds', {'id': 'condition', 'name': 'Synthetic condition',
            'series_id': SID, 'operator': operator, 'value': value,
            'type': '观察位', 'horizon': '短期'}
    body = {'judgment': {'contradiction': {}, kind: [rule]}, 'binding_units': {SID: unit},
        'rule_quotes': {'condition': quote}, 'unresolved': []}
    before = deepcopy(body)
    result = validate_analysis(body, source, [{'id': SID, 'name': 'Synthetic quantity',
        'unit': unit, 'frequency': frequency}], variety='TEST', actor='synthetic')
    assert body == before
    return result


@pytest.mark.parametrize('source,unit,value', [
    ('报价在40万以下才有补充意愿。', '元/吨', 400000),
    ('观测量达到2.5万以上需要复核。', 'kg', 25000),
    ('观测量处于0.12亿及以下。', '件', 12000000),
    ('观测量在3千以内。', 'm', 3000),
])
def test_decimal_multiplier_before_comparison_inherits_an_unscaled_confirmed_unit(source, unit, value):
    assert Decimal(str(value)) in quoted_quantities(source, unit=unit)
    assert check(source, unit=unit, value=value).payload.thresholds[0].value == value


@pytest.mark.parametrize('source', [
    '报价40万美元/吨以下。', '报价40万元/千克以下。', '报价40万未知单位以下。',
    '报价40万以下/年。', '报价40万以下 +2。', '报价40万以下% 。',
])
def test_scaled_comparison_does_not_license_currency_conversion_or_compound_units(source):
    assert Decimal(400000) not in quoted_quantities(source, unit='元/吨', units=['美元/吨', '元/千克'])


@pytest.mark.parametrize('source,quote,value', [
    ('读数40万以下。', '读数4', 4),
    ('读数40万美元/吨以下。', '读数40万', 400000),
    ('读数40万以下/年。', '读数40万以下', 400000),
])
def test_scaled_comparison_preserves_complete_original_quantity_context(source, quote, value):
    with pytest.raises(AnalysisError):
        check(source, quote=quote, unit='元/吨', value=value)


@pytest.mark.parametrize('expression', ['change > 0', 'change < 0', 'change >= 0', 'change <= -0.0'])
def test_model_selected_change_uses_mathematical_zero_without_a_literal_zero(expression):
    assert check('观测变化持续，作为验证条件。', expression=expression).payload.falsifiers[0].expression == expression


@pytest.mark.parametrize('expression', ['value > 0', 'value < -0.0', 'change > 3', 'change < -2'])
def test_absolute_zero_and_nonzero_changes_still_require_original_numeric_evidence(expression):
    with pytest.raises(AnalysisError) as caught:
        check('观测变化持续，作为验证条件。', expression=expression)
    assert caught.value.issues[0]['code'] == 'number_not_in_source'


@pytest.mark.parametrize('count,periods', [('两', 2), ('三', 3), ('十二', 12), ('二十一', 21),
    ('一百零二', 102), ('一百二十', 120), ('2', 2)])
def test_chinese_and_arabic_counts_of_weekly_observations(count, periods):
    assert check(f'观测变化持续，连续{count}周。', periods=periods).payload.falsifiers[0].consecutive_periods == periods


@pytest.mark.parametrize('frequency,source', [('月', '观测变化持续，连续两个月。'),
    ('日度', '观测变化持续，连续三天。'), ('yearly', '观测变化持续，连续三年。'),
    (None, '观测变化持续，连续两期。')])
def test_periods_respect_the_actual_observation_cadence(frequency, source):
    periods = 2 if '两' in source else 3
    assert check(source, frequency=frequency, periods=periods).payload.falsifiers[0].consecutive_periods == periods


@pytest.mark.parametrize('frequency,source', [('周', '观测变化持续，连续两个月。'),
    ('月', '观测变化持续，连续两周。'), ('日', '观测变化持续，连续2周。'),
    (None, '观测变化持续，连续两周。')])
def test_calendar_period_count_cannot_become_a_different_or_unknown_observation_frequency(frequency, source):
    with pytest.raises(AnalysisError):
        check(source, frequency=frequency, periods=2)


@pytest.mark.parametrize('source,quote', [
    ('观测变化持续，连续二十二周。', '观测变化持续，连续二十'),
    ('观测变化持续，连续十二两周。', '两周'),
    ('观测变化持续，连续两周/月。', '观测变化持续，连续两周'),
    ('观测变化持续，连续2周度。', '观测变化持续，连续2周'),
])
def test_period_quote_cannot_cut_a_count_or_a_compound_cadence(source, quote):
    with pytest.raises(AnalysisError):
        check(source, quote=quote, periods=2)


@pytest.mark.parametrize('source,frequency', [
    ('观测量2kg，连续三周。', '周'), ('观测变化持续，连续2周/月。', '日'),
    ('观测变化持续，连续2.0周。', '日'), ('观测变化持续，连续两周年。', '周'),
    ('观测变化持续，连续1\u200b2周。', '周'), ('观测变化持续，连续一百二周。', '周'),
])
def test_period_counts_never_borrow_measurement_digits_or_malformed_time_tokens(source, frequency):
    with pytest.raises(AnalysisError):
        check(source, frequency=frequency, periods=2)


@pytest.mark.parametrize('source', ['报价40万以下美元/吨。', '报价40万以下 USD/吨。',
    '报价40万以下（USD/吨）。', '报价40万以下 $ 。'])
@pytest.mark.parametrize('shortened', [False, True])
def test_comparison_suffix_cannot_conceal_an_attached_source_currency_or_unit(source, shortened):
    with pytest.raises(AnalysisError):
        check(source, quote='报价40万' if shortened else None, unit='元/吨', value=400000)


def test_comparison_suffix_cannot_conceal_an_attached_compound_unit():
    with pytest.raises(AnalysisError):
        check('观测量2.5万及以上kg/年。', unit='kg', value=25000)


@pytest.mark.parametrize('source', ['读数3万kg以下吨。', '读数3kg以下吨。'])
def test_known_source_units_after_comparison_cannot_be_ignored(source):
    assert not quoted_quantities(source, unit='kg', units=['kg', '吨'])


@pytest.mark.parametrize('source,frequency', [
    ('Review the changes for 2 periods. Continue observation.', None),
    ('Review the changes for 2 weeks.', 'weekly'),
    ('Review the changes for 2 consecutive monthly periods.', 'month'),
    ('Monthly stocks above 12 吨 for 2 consecutive monthly periods invalidate the claim.', '月'),
    ('观测变化持续，连续２周。', '周'),
])
def test_complete_generic_and_qualified_observation_periods_remain_usable(source, frequency):
    assert check(source, frequency=frequency, periods=2).payload.falsifiers[0].consecutive_periods == 2


def test_monthly_numeric_condition_accepts_trailing_english_prose_without_losing_cadence_checks():
    source = 'Monthly stocks above 12 吨 for 2 consecutive monthly periods invalidate the claim.'
    assert check(source, unit='吨', frequency='月', expression='value > 12', periods=2).payload.falsifiers[0].consecutive_periods == 2
    with pytest.raises(AnalysisError) as error:
        check(source, unit='吨', frequency='日', expression='value > 12', periods=2)
    assert error.value.issues[0]['code'] == 'period_frequency'


@pytest.mark.parametrize('connector', ['或', '且', ' OR ', ' and ', ' || ', ' && ', '∨', '∧'])
@pytest.mark.parametrize('explicit_unit', [False, True])
def test_logical_clause_boundary_preserves_a_model_selected_numeric_condition(connector, explicit_unit):
    # The model selects the rule and operator. The numeric lexer only verifies
    # its literal inside a complete quote containing another condition.
    suffix = '点' if explicit_unit else ''
    source = f'AAA>17.5{suffix}{connector}BBB破23日线→复核'
    assert check(source, unit='点', value=17.5, operator='>').payload.thresholds[0].value == 17.5
    assert check(source, quote=f'AAA>17.5{suffix}', unit='点', value=17.5,
                 operator='>').payload.thresholds[0].value == 17.5


@pytest.mark.parametrize('amount', [
    '17.5%', '17.5‰', '17.5kg', '17.5点/年', '17.5点公里',
    '17.5万美元/吨', '17.5abc', '17.5/2', '17.5e+', '17.5²',
])
def test_logical_clause_boundaries_never_erase_a_conflicting_or_malformed_unit(amount):
    source = f'AAA>{amount}或BBB<23→复核'
    assert Decimal('17.5') not in quoted_quantities(source, unit='点',
        units=['点', '点公里', 'kg', '%', '‰', '美元/吨'])
    with pytest.raises(AnalysisError):
        check(source, quote='AAA>17.5', unit='点', value=17.5, operator='>')


@pytest.mark.parametrize('tail', ['或', '且', '或20', '或/吨', 'oranges', 'ORANGE', 'ANDY',
    '或者未知单位', '点或然率', '点或', '点且', '点∨20'])
def test_attached_words_or_incomplete_logical_operands_are_not_numeric_boundaries(tail):
    assert Decimal('17.5') not in quoted_quantities('17.5' + tail, unit='点')


@pytest.mark.parametrize('quote', ['AAA>17.5', 'AAA>17.5kg'])
def test_a_registered_longer_unit_still_owns_its_apparent_logical_suffix(quote):
    source = 'AAA>17.5kg或未知单位 23'
    document = {'judgment': {'contradiction': {}, 'thresholds': [{
        'id': 'boundary', 'name': 'Synthetic bound', 'series_id': SID,
        'operator': '>', 'value': 17.5, 'type': '观察位', 'horizon': '短期'}]},
        'binding_units': {SID: 'kg'}, 'rule_quotes': {'boundary': quote}, 'unresolved': []}
    with pytest.raises(AnalysisError):
        validate_analysis(document, source, [{'id': SID, 'unit': 'kg'},
            {'id': 'other', 'unit': 'kg或未知单位'}], variety='TEST', actor='synthetic')
