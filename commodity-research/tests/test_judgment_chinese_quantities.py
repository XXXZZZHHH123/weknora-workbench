"""A small quantity grammar, independent of research intent or rule operators."""
from decimal import Decimal

import pytest

from tin.judgments.numbers import quoted_quantities


@pytest.mark.parametrize('quote,unit,expected', [
    ('数量回万吨', '吨', '10000'),
    ('数量达到千kg。', 'kg', '1000'),
    ('数量达到亿件。', '件', '100000000'),
    ('数量达到万亿件。', '件', '1000000000000'),
    ('数量达到一万吨。', '吨', '10000'),
    ('数量达到两万吨。', '吨', '20000'),
    ('数量达到二万亿件。', '件', '2000000000000'),
    ('数量达到九千kg。', 'kg', '9000'),
    ('数量达到零万吨。', '吨', '0'),
    ('数量达到〇万件。', '件', '0'),
    ('数量达到一 万吨。', '吨', '10000'),
    ('数量达到一万元/吨。', '元/吨', '10000'),
    ('数量达到一万吨。', '万吨', '1'),
    ('数量达到万吨。', '万吨', '1'),
])
def test_only_simple_scaled_chinese_quantities_with_full_confirmed_units_are_read(quote, unit, expected):
    assert quoted_quantities(quote, unit=unit) == {Decimal(expected)}


@pytest.mark.parametrize('quote', [
    '数量百万吨', '数量十万吨', '数量一百亿吨', '数量一万三千吨',
    '数量十二万吨', '数量二十万吨', '数量一点五万吨', '数量零点一万吨',
    '数量负一万吨', '数量負一万吨', '数量正一万吨', '数量壹万吨',
    '数量千万亿吨', '数量万万吨', '数量数万吨', '数量几万吨',
    '量2一万吨', '量1.2一万吨', 'label_一万吨',
    '量-一万吨', '量/一万吨', '量×一万吨', '量+一万吨',
    '量 - 一万吨', '量 / 一万吨', '量 2 一万吨',
    '数量万吨级', '按万吨计', '数量万余吨', '数量一万余吨',
    '数量万吨/年', '数量一万吨公里', '数量一万元/吨',
    '数量一万', '数量万', '[一万吨]', '[一万吨,两万吨]',
])
def test_unsupported_or_partial_quantities_do_not_supply_a_suffix_value(quote):
    assert quoted_quantities(quote, unit='吨', units=['吨', '吨公里', '吨/年', '元/吨']) == set()


@pytest.mark.parametrize('quote', ['数量万吨', '数量一万吨', '数量千kg', '数量两万件'])
@pytest.mark.parametrize('unit', [None, '', 'kg/m'])
def test_simple_chinese_quantity_requires_the_complete_selected_unit(quote, unit):
    assert quoted_quantities(quote, unit=unit) == set()


@pytest.mark.parametrize('suffix', ['以上', '以下', '及以上', '及以下', '以内', '以外'])
@pytest.mark.parametrize('literal', ['1万', '万', '一万'])
def test_comparison_suffix_only_terminates_a_quantity_unit(literal, suffix):
    assert quoted_quantities(f'数量达到{literal}吨{suffix}，请复核。', unit='吨') == {Decimal(10000)}


@pytest.mark.parametrize('suffix', ['以上', '以下', '及以上', '及以下', '以内', '以外'])
@pytest.mark.parametrize('chain', ['/年', ' / 年', '^2', '%', '‰', '*m', '×m', '÷年', '+2'])
def test_comparison_suffix_does_not_hide_a_compound_unit_or_arithmetic_chain(suffix, chain):
    assert quoted_quantities(f'数量1万吨{suffix}{chain}', unit='吨') == set()


def test_existing_actual_compound_units_still_win_over_their_prefixes():
    assert quoted_quantities('数量一万元/吨以上', unit='元', units=['元/吨']) == set()
    assert quoted_quantities('数量一万吨公里以下', unit='吨', units=['吨公里']) == set()
    assert quoted_quantities('数量一万元/吨以上', unit='元/吨') == {Decimal(10000)}


def test_chinese_quantity_does_not_choose_or_reverse_a_comparison_operator():
    assert quoted_quantities('数量回万吨。', unit='吨') == quoted_quantities('数量降万吨。', unit='吨')
    assert quoted_quantities('数量万吨以上。', unit='吨') == quoted_quantities('数量万吨以下。', unit='吨')


@pytest.mark.parametrize('quote', [
    '数量负0.1万吨', '数量負3万吨', '数量负 3 万吨',
    '数量负[1,2]吨', '数量负1-2吨', '数量负1 and 2吨',
])
def test_an_unsupported_chinese_negative_sign_cannot_be_dropped(quote):
    assert quoted_quantities(quote, unit='吨') == set()
