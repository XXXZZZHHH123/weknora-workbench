"""Bounded simple moving averages over recorded observations, never calendar filling."""
from decimal import Decimal, localcontext
import re

from tin.compute.rule_values import decimal_value

CROSSING_OPERATORS = frozenset({'crosses_below', 'crosses_above'})


def moving_average_points(value, operator):
    moving = moving_average_target(value)
    return moving['window'] + int(operator in CROSSING_OPERATORS) if moving else 1


def moving_average_target(value):
    if not isinstance(value, dict):
        return None
    if (set(value) != {'kind', 'window'} or value['kind'] != 'sma'
            or type(value['window']) is not int or not 2 <= value['window'] <= 120):
        raise ValueError('均线须使用2至120个观测的简单移动平均')
    return value


def _get(point, field, default=None):
    return point.get(field, default) if isinstance(point, dict) else getattr(point, field, default)


def _number(point):
    value = _get(point, 'value')
    if value is None or isinstance(value, bool) or _get(point, 'status', '正常') != '正常':
        return None
    try:
        return decimal_value(value)
    except (ValueError, TypeError):
        return None


def _sum(values):
    lowest = min(value.as_tuple().exponent for value in values)
    highest = max(value.adjusted() for value in values)
    with localcontext() as context:
        context.prec = max(28, highest - lowest + len(str(len(values))) + 3)
        return sum(values, Decimal(0))


def _window(points):
    values = [_number(point) for point in points]
    units = {_get(point, 'unit') or (_get(point, 'caliber_snapshot', {}) or {}).get('unit')
             for point in points}
    units.discard(None)
    units.discard('')
    if any(value is None for value in values) or len(units) > 1:
        return None
    return _sum(values)


def sma_values(points, window):
    """Return aligned display values; missing/invalid observations break each window."""
    moving_average_target({'kind': 'sma', 'window': window})
    result = []
    for index in range(len(points)):
        subset = points[max(0, index - window + 1):index + 1]
        total = _window(subset) if len(subset) == window else None
        result.append(float(total / window) if total is not None else None)
    return result


def sma_result(points, window, unit=None):
    moving_average_target({'kind': 'sma', 'window': window})
    points = points[-window:]
    result = {'value': None, 'total': None, 'count': len(points), 'error': None, 'points': points}
    if len(points) < window:
        result['error'] = f'均线需要{window}次观测，目前只有{len(points)}次'
        return result
    total = _window(points)
    snapshots = {_get(point, 'unit') or (_get(point, 'caliber_snapshot', {}) or {}).get('unit') for point in points}
    snapshots.discard(None)
    snapshots.discard('')
    if total is None or unit is not None and any(value != unit for value in snapshots):
        result['error'] = '均线窗口含缺失、无效观测或单位变化'
    else:
        result.update(value=float(total / window), total=total)
    return result


def sma_crossing_result(points, window, unit=None):
    """Two complete adjacent windows; do not turn an ongoing level into an event."""
    moving_average_target({'kind': 'sma', 'window': window})
    points = points[-(window + 1):]
    result = sma_result(points[1:], window, unit)
    result.update(previous_total=None, previous_value=None)
    if len(points) < window + 1:
        result.update(value=None, total=None,
            error=f'均线交叉需要{window + 1}次观测，目前只有{len(points)}次')
        return result
    previous = sma_result(points[:-1], window, unit)
    if previous['error'] is not None:
        result.update(value=None, total=None, error='前一期' + previous['error'])
    elif result['error'] is None:
        result.update(previous_total=previous['total'], previous_value=_number(points[-2]))
    return result


def sma_boundary(session, series_id, day, window, crossing=False):
    moving_average_target({'kind': 'sma', 'window': window})
    # Imported lazily: signals uses board, whose dynamic thresholds call this helper.
    from tin.compute.signals import recent_points
    from tin.models import Indicator
    if session.info.get('knowledge_facts') and not session.info.get('knowledge_facts_include_invalid'):
        return {'value': None, 'total': None, 'count': 0, 'points': [],
                'error': '当前来源快照未保留缺失期，请使用完整的均线观测窗口'}
    points = recent_points(session, series_id, day, window + int(crossing))
    indicator = session.get(Indicator, series_id)
    result = (sma_crossing_result if crossing else sma_result)(points, window, indicator.unit if indicator else None)
    if indicator is None or not indicator.unit:
        result['error'] = '指标或单位尚未确认'
        result.update(value=None, total=None)
    return result


def sma_source_window(quote, window, original=None):
    """Check a model-declared SMA operand against explicit source notation only."""
    moving_average_target({'kind': 'sma', 'window': window})
    number = str(window)
    patterns = [
        rf'(?<![A-Za-z0-9_.+-])(?:SMA|MA)\s*(?:\(\s*{number}\s*\)|{number}(?![0-9.A-Za-z_]))',
        rf'(?<![0-9.+-]){number}\s*(?:日|期|次)\s*(?:简单移动平均线|移动平均线|均线)(?![0-9])',
        rf'(?<![0-9.+-]){number}\s*日线(?![0-9.])',
    ]
    if not any(re.search(pattern, quote, flags=re.I) for pattern in patterns):
        return False
    if original is None:
        return True
    spans = [match.span() for pattern in patterns for match in re.finditer(pattern, original, flags=re.I)]
    position = original.find(quote)
    while position >= 0:
        if not any(position <= start and end <= position + len(quote) for start, end in spans):
            return False
        position = original.find(quote, position + 1)
    return bool(spans)
