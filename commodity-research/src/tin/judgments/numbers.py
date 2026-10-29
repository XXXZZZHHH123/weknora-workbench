"""Bounded numeric syntax and exact decimal evidence; never business intent."""
from decimal import Decimal, DecimalException
import math
import re


NUMBER = r'[+-]?(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?'
_GROUPED = r'[+-]?(?:[0-9]{1,3}(?:,[0-9]{3})+(?:\.[0-9]+)?|[0-9]+(?:\.[0-9]+)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?'
_CONNECTORS = '+-/:*^×÷⁄∕‐‑‒–—~～'
_START = r'(?<![0-9A-Za-z_.,' + re.escape(_CONNECTORS) + r'万亿千\]])'
_SCALAR = re.compile(_START + _GROUPED)
_SHARED_UNIT_PAIR = re.compile(_START + '(' + _GROUPED + r')\s+and\s+(' + _GROUPED + ')', re.IGNORECASE)
_MEMBER = re.compile(NUMBER)
_SCALE = re.compile(r'万亿|亿|万|千')
_POWERS = {'千': 3, '万': 4, '亿': 8, '万亿': 12}
_WIDTH = {code: code - 0xFEE0 for code in range(0xFF01, 0xFF5F)}
_WIDTH.update({0x3000: ord(' '), 0x2212: ord('-')})
_GROUPED_SPACE = re.compile(r'[0-9]+(?:(?:[\u00a0\u202f\u2009][0-9]+)|(?: +[0-9]{3}))+')
_ARITHMETIC = re.compile(_GROUPED + r'\s*[*^×÷⁄∕/]\s*' + _GROUPED)


def _width(text):
    # Width variants only: NFKC would invent digits from ², ①, ½ and styled
    # numerals, turning an unsupported source quantity into a different value.
    return text.translate(_WIDTH)


class NumericPrecisionError(ValueError):
    pass


def _decimal(literal):
    if not isinstance(literal, str) or len(literal) > 128:
        raise ValueError('numeric literal too long')
    value = Decimal(literal)
    if not value.is_finite() or abs(value.as_tuple().exponent) > 1000:
        raise ValueError('nonfinite or excessive numeric exponent')
    return value


def exact_float(literal):
    """Reject a lossy JSON/expression conversion instead of rounding silently."""
    try:
        decimal = _decimal(literal)
        value = float(decimal)
        if (not math.isfinite(value) or Decimal(str(value)) != decimal
                or decimal == decimal.to_integral_value() and Decimal.from_float(value) != decimal):
            raise ValueError('lossy conversion')
    except (ValueError, DecimalException, OverflowError):
        raise NumericPrecisionError('研究规则数值超出当前存储精度，不能舍入、截断或改为零') from None
    return value


def check_rule_number(value):
    if type(value) not in (int, float):
        raise ValueError('研究规则必须使用有限JSON数字，不能使用布尔值或字符串')
    exact_float(str(value))
    return value


def _scale(value, power):
    # Constructing the decimal tuple changes only the explicit base-ten scale;
    # Decimal's ambient precision cannot round the original significand.
    parts = value.as_tuple()
    return Decimal((parts.sign, parts.digits, parts.exponent + power))


def _unit_boundary(remainder):
    if not remainder:
        return True
    following = remainder.lstrip()
    if not following:
        return True
    if following.startswith('· '):
        return True  # Renderer field delimiter, rather than kg·m.
    if following[0] in '/^*·×÷%‰‱':
        return False
    if remainder[0].isspace():
        return True
    return not (remainder[0].isalnum() or remainder[0] == '_')


def _unit_value(value, tail, unit, units):
    following = tail.lstrip()
    expected = _width(unit).strip() if isinstance(unit, str) else None
    known = {_width(item).strip() for item in units if isinstance(item, str) and item.strip()}
    known.update({'%', '‰', '‱'})
    if expected:
        known.add(expected)
        prefix = _SCALE.match(expected)
        if prefix:
            known.add(expected[prefix.end():])
    # A compound source unit wins over its prefix. Only an explicit base-ten
    # multiplier plus the complete expected unit can change the numeric scale.
    unit_prefix = False
    for actual in sorted(known - {''}, key=len, reverse=True):
        if following.startswith(actual):
            unit_prefix = True
            if not _unit_boundary(following[len(actual):]):
                continue
            if actual == expected:
                return value
            power = _SCALE.match(actual)
            if expected and power and actual[power.end():] == expected:
                return _scale(value, _POWERS[power[0]])
            return None
    # A failed unit boundary is not permission to reinterpret a quantity as
    # unitless. Unknown attached text is equally ambiguous for a typed quantity.
    if unit_prefix:
        return None
    match = _SCALE.match(following)
    if match:
        remainder = following[match.end():].lstrip()
        closed = (not remainder or remainder[0] in '，;；。!?！？、)]}）'
            or remainder[0] in ',.' and (len(remainder) == 1 or remainder[1].isspace()))
        if remainder and (remainder[0] in '万亿千0123456789' + _CONNECTORS
                or remainder[0] in ',.' and not closed):
            return None
        if expected:
            if closed:
                # A closed explicit multiplier is numeric evidence like a bare
                # literal. An already scaled catalog unit needs an actual
                # source unit to disambiguate the repeated multiplier.
                if _SCALE.match(expected):
                    return None
            elif not remainder.startswith(expected) or not _unit_boundary(remainder[len(expected):]):
                return None
        return _scale(value, _POWERS[match[0]])
    if expected and following and (following[0].isalpha() or following[0] in '([{'):
        return None
    if following and (following[0].isnumeric() or following[0] in '十百千万亿兆两〇零一二三四五六七八九%‰‱'):
        return None
    if following and following[0] in _CONNECTORS:
        return None
    if tail and not tail[0].isspace():
        first = tail[0]
        if first in ',.':
            if len(tail) > 1 and not tail[1].isspace():
                return None
        elif first in '+-/:0123456789' or first.isascii() and (first.isalpha() or first == '_'):
            return None
    return value


def _range_pattern(unit, units):
    known = {_width(item).strip() for item in units if isinstance(item, str) and item.strip()}
    known.update({'%', '‰', '‱'})
    if isinstance(unit, str) and unit.strip():
        expected = _width(unit).strip()
        known.add(expected)
        prefix = _SCALE.match(expected)
        if prefix:
            known.add(expected[prefix.end():])
    literal_units = '|'.join(re.escape(item) for item in sorted(known - {''}, key=lambda item: (-len(item), item)))
    scale = _SCALE.pattern
    suffix = rf'(?:(?:{scale})\s*(?:{literal_units})|(?:{literal_units})|(?:{scale}))'
    return re.compile(_START + rf'(?P<lower>{_GROUPED})(?:\s*(?P<lower_unit>{suffix}))?\s*'
        + rf'(?:-|–|—|~|～|至|到|(?i:\bto\b))\s*(?P<upper>{_GROUPED})(?:\s*(?P<upper_unit>{suffix}))?')


def _connected_ranges(text, unit, units, original):
    """Consume whole candidate intervals, including rejected ones.

    Only explicit units or multipliers distinguish these from bare subtraction
    or date fragments. The model, rather than this syntax reader, still chooses
    whether the original describes a threshold, observation, or other rule.
    """
    masked, values = list(text), set()
    for match in _range_pattern(unit, units).finditer(text):
        masked[match.start():match.end()] = ' ' * (match.end() - match.start())
        lower_suffix, upper_suffix = match['lower_unit'] or '', match['upper_unit'] or ''
        preceding = original[:match.start()].rstrip()
        after = text[match.end():].lstrip()
        if (not re.compile(_START).match(original, match.start()) or not upper_suffix
                or preceding and (preceding[-1].isnumeric() or preceding[-1] in _CONNECTORS)
                or after and (after[0].isnumeric() or after[0] in _CONNECTORS)):
            continue
        try:
            upper_tail = text[match.end('upper'):]
            if not lower_suffix:
                lower_tail = upper_tail
            elif _SCALE.fullmatch(lower_suffix):
                prefix = _SCALE.match(upper_suffix)
                base_unit = upper_suffix[prefix.end():].strip() if prefix else upper_suffix
                lower_tail = lower_suffix + base_unit
            else:
                lower_tail = lower_suffix
            lower = _unit_value(_decimal(match['lower'].replace(',', '')), lower_tail, unit, units)
            upper = _unit_value(_decimal(match['upper'].replace(',', '')), upper_tail, unit, units)
            if lower is not None and upper is not None and lower < upper:
                values.update((lower, upper))
        except (ValueError, DecimalException):
            continue
    return ''.join(masked), values


def _arrays(text):
    start, depth = None, 0
    for index, char in enumerate(text):
        if char == '[':
            if depth == 0:
                start = index
            depth += 1
        elif char == ']' and depth:
            depth -= 1
            if depth == 0:
                yield start, index + 1
    if depth:
        yield start, len(text)


def quoted_quantities(quote, *, unit=None, units=()):
    """Read explicit decimal literals, masking malformed/nested list contents.

    A flat numeric list owns its commas: [1,200] contains 1 and 200, never 1200.
    Outside lists, only complete thousands groups are grouped; comma plus space
    is a separator. No arithmetic, ratios, code fragments or unit aliases are
    inferred. The caller still checks full original quotes and catalog units.
    """
    units = tuple(units)
    text = _width(quote)
    masked, values = list(text), set()
    for start, end in _arrays(text):
        masked[start:end] = ' ' * (end - start)
        if text[end - 1] != ']':
            continue
        members = text[start + 1:end - 1].split(',')
        if len(members) > 32 or any(not _MEMBER.fullmatch(member.strip()) for member in members):
            continue
        try:
            parsed = [_unit_value(_decimal(member.strip()), text[end:], unit, units) for member in members]
            if all(value is not None for value in parsed):
                values.update(parsed)
        except (ValueError, DecimalException):
            continue
    scalar_text = ''.join(masked)
    for pattern in (_GROUPED_SPACE, _ARITHMETIC):
        scalar_text = pattern.sub(lambda match: 'x' * len(match[0]), scalar_text)
    scalar_text, range_values = _connected_ranges(scalar_text, unit, units, text)
    values.update(range_values)
    masked = list(scalar_text)
    # A literal pair can share its final unit. Both endpoints must validate
    # together; an unknown word after the first number is never a bare value.
    for pair in _SHARED_UNIT_PAIR.finditer(scalar_text):
        try:
            preceding = text[:pair.start()].rstrip()
            invalid_start = preceding and (preceding[-1].isnumeric() or preceding[-1] in '*/^×÷⁄∕‐‑‒–—~～')
            parsed = [] if invalid_start else [
                _unit_value(_decimal(member.replace(',', '')), text[pair.end():], unit, units)
                for member in pair.groups()]
            if all(value is not None for value in parsed):
                values.update(parsed)
        except (ValueError, DecimalException):
            pass
        masked[pair.start():pair.end()] = ' ' * (pair.end() - pair.start())
    scalar_text = ''.join(masked)
    for match in _SCALAR.finditer(scalar_text):
        try:
            preceding = text[:match.start()].rstrip()
            if preceding and (preceding[-1].isnumeric() or preceding[-1] in '*/^×÷⁄∕‐‑‒–—~～'):
                continue
            value = _unit_value(_decimal(match[0].replace(',', '')), text[match.end():], unit, units)
            if value is not None:
                values.add(value)
        except (ValueError, DecimalException):
            continue
    return values
