"""A logical boundary needs complete numeric syntax in its own next clause."""
from decimal import Decimal

import pytest

from tin.judgments.numbers import _logical_clause_boundary, quoted_quantities


@pytest.mark.parametrize('operand', [
    'BBB>23e+', 'BBB>23e-', 'BBB>23abc', 'BBB>23²', 'BBB>1/2',
    'BBB>2024-03-31', 'BBB>[true,23]', 'BBB>[[23,24]]', 'BBB>[23,',
    'BBB>1,20', 'BBB>1.2.3', 'BBB>23e999999999999999999999999999999',
    'BBB>23e+或CCC>24', 'BBB>1/2且CCC>24',
    'BBB>23e+ OR CCC>24', 'BBB>23² && CCC>24',
])
def test_malformed_next_operand_cannot_license_the_left_quantity(operand):
    assert not _logical_clause_boundary('或' + operand)
    assert Decimal('17.5') not in quoted_quantities('AAA>17.5或' + operand, unit='点')


@pytest.mark.parametrize('operand', [
    'BBB>23', 'BBB>23.5', 'BBB>-0.25', 'BBB>1.25e-3', 'BBB破23日线',
    'BBB>23%', 'BBB>23‰', 'BBB>23‱', 'BBB floor 23', 'BBB factor 23',
    'BBB>23且CCC>24', 'BBB>23 AND CCC>24',
])
def test_complete_next_operand_keeps_unrelated_quantity_syntax_available(operand):
    assert _logical_clause_boundary('或' + operand)
    assert Decimal('17.5') in quoted_quantities('AAA>17.5或' + operand, unit='点')


def test_nested_logical_boundaries_do_not_recursively_rescan_later_clauses(monkeypatch):
    from tin.judgments import numbers
    original = numbers._logical_clause_boundary
    calls = []

    def observed(remainder):
        calls.append(remainder)
        assert len(calls) == 1, 'The operand lexer recursively interpreted another logical clause'
        return original(remainder)

    monkeypatch.setattr(numbers, '_logical_clause_boundary', observed)
    assert observed('或BBB>23' + '且CCC>24' * 1000)
    assert len(calls) == 1
