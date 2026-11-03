"""Whitespace cannot detach a coefficient and create an implicit one."""
from datetime import date
from decimal import Decimal

import pytest

from tests.test_judgment_falsifier_quantity_audit import ACTOR, SID, declaration, document
from tin.judgments.analysis import AnalysisError, validate_analysis
from tin.judgments.numbers import quoted_quantities


SPACES = ['\u00a0', '\u202f', '\u2009', '\u2003', '\n', '\r\n']
FORMAT_MARKS = ['\u200b', '\u200c', '\u200d', '\u2060', '\ufeff',
                '\u0301', '\u034f', '\ufe0f', '\u20dd']


@pytest.mark.parametrize('space', SPACES)
@pytest.mark.parametrize('coefficient,expected', [('3', '30000'), ('0.1', '1000')])
def test_arabic_coefficient_is_not_reinterpreted_as_an_implicit_one(space, coefficient, expected):
    assert quoted_quantities(coefficient + space + '万吨', unit='吨') == {Decimal(expected)}


@pytest.mark.parametrize('space', SPACES)
def test_chinese_coefficient_cannot_be_dropped_at_unicode_whitespace(space):
    values = quoted_quantities('三' + space + '万吨', unit='吨')
    assert Decimal(10000) not in values
    assert values <= {Decimal(30000)}


@pytest.mark.parametrize('space', SPACES)
def test_shortened_quote_cannot_drop_a_coefficient_across_unicode_whitespace(space):
    body = document('万吨')
    with pytest.raises(AnalysisError):
        validate_analysis(body, '0.1' + space + '万吨',
            [{'id': SID, 'name': 'Synthetic quantity', 'unit': None}],
            variety='UNIT', actor=ACTOR, written_at=date(2033, 1, 2),
            caliber_confirmations={'quantity_review': declaration()})


@pytest.mark.parametrize('mark', FORMAT_MARKS)
def test_invisible_or_combining_mark_cannot_create_an_implicit_one(mark):
    assert Decimal(10000) not in quoted_quantities('3' + mark + '万吨', unit='吨')


@pytest.mark.parametrize('mark', FORMAT_MARKS)
def test_shortened_quote_cannot_drop_a_coefficient_at_an_invisible_mark(mark):
    body = document('万吨')
    with pytest.raises(AnalysisError):
        validate_analysis(body, '0.1' + mark + '万吨',
            [{'id': SID, 'name': 'Synthetic quantity', 'unit': None}],
            variety='UNIT', actor=ACTOR, written_at=date(2033, 1, 2),
            caliber_confirmations={'quantity_review': declaration()})


@pytest.mark.parametrize('mark', FORMAT_MARKS)
@pytest.mark.parametrize('literal', ['万吨', '1万吨', '万吨以上'])
def test_invisible_mark_cannot_hide_a_compound_unit_denominator(mark, literal):
    assert quoted_quantities(literal + mark + '/年', unit='吨') == set()
