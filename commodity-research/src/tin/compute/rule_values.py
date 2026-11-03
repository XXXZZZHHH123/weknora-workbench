"""Decimal arithmetic over existing numeric values, without inventing lost digits."""

from decimal import Decimal, DecimalException, localcontext


def decimal_value(value: int | float | str | Decimal) -> Decimal:
    """Read the stored spelling; bound legacy literals before doing arithmetic."""
    literal = str(value)
    if len(literal) > 128:
        raise ValueError('研究规则数值过长，无法精确比较')
    try:
        number = Decimal(literal)
    except DecimalException:
        raise ValueError('研究规则数值无法精确比较') from None
    if not number.is_finite() or abs(number.as_tuple().exponent) > 1000:
        raise ValueError('研究规则数值非有限值或指数超出可比较范围')
    return number


def decimal_difference(left: int | float | Decimal, right: int | float | Decimal) -> Decimal:
    """Subtract exactly, including float spellings with widely separated exponents."""
    left, right = decimal_value(left), decimal_value(right)
    lowest = min(left.as_tuple().exponent, right.as_tuple().exponent)
    highest = max(left.adjusted(), right.adjusted())
    with localcontext() as context:
        # Finite float inputs need at most about 650 digits. Legacy literals are
        # bounded above, so this precision cannot grow from an unbounded exponent.
        context.prec = max(1, highest - lowest + 2)
        return left - right
