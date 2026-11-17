"""Deterministic condition windows and calendar backtests, independent of storage.

``backtest_conditions(rules, logic, series, start, end)`` accepts verified
observation windows keyed by series ID. It never resolves names, reads a model,
interpolates observations or fills absent values. The same window evaluator is
used by judgment falsifiers.
"""
from collections import deque
from datetime import date, timedelta
from math import isfinite
import operator

from tin.compute.rule_values import decimal_difference, decimal_value

OPS = {'>': operator.gt, '>=': operator.ge, '<': operator.lt, '<=': operator.le}


def condition_quantities(metric, values):
    """Return exact values or adjacent-observation differences (no percentages)."""
    if metric not in {'value', 'change'}:
        raise ValueError('条件只支持 value 或 change')
    if metric == 'change':
        return [None if left is None or right is None else decimal_difference(right, left)
                for left, right in zip(values, values[1:])]
    return [None if value is None else decimal_value(value) for value in values]


def condition_period_states(metric, comparison, target, values, consecutive_periods=1):
    """Shared linear-time comparison and consecutive-period evaluator."""
    if comparison not in OPS or type(consecutive_periods) is not int or consecutive_periods < 1:
        raise ValueError('条件运算符或连续期数无效')
    threshold = decimal_value(target)
    quantities = condition_quantities(metric, values)
    hits, result = deque(), []
    missing = failed = 0
    for index in range(len(values)):
        quantity = quantities[index - 1] if metric == 'change' and index else (
            quantities[index] if metric == 'value' else None)
        if metric == 'change' and index == 0:
            result.append({'triggered': None, 'quantity': None, 'hit': None})
            continue
        hit = None if quantity is None else OPS[comparison](quantity, threshold)
        hits.append(hit)
        missing += hit is None
        failed += hit is False
        if len(hits) > consecutive_periods:
            old = hits.popleft()
            missing -= old is None
            failed -= old is False
        ready = len(hits) == consecutive_periods and not missing
        result.append({'triggered': failed == 0 if ready else None, 'quantity': quantity, 'hit': hit})
    return result


def evaluate_window(metric, comparison, target, values, consecutive_periods=1):
    """Evaluate the last N actual periods, preserving missing observations.

    A change window of N periods needs N+1 observations. ``None`` means the
    window cannot be evaluated, including a missing point anywhere in it.
    Quantities and hits are returned for evidence consumers such as judgments.
    """
    want = consecutive_periods + int(metric == 'change')
    selected = list(values)[-want:]
    states = condition_period_states(metric, comparison, target, selected, consecutive_periods)
    compared = states[1:] if metric == 'change' else states
    return {'triggered': states[-1]['triggered'] if states else None,
            'quantities': [state['quantity'] for state in compared],
            'hits': [state['hit'] for state in compared],
            'current_value': states[-1]['quantity'] if states else None}


def observation_freshness(points, day, *, latest_date=None):
    """The shared research-desk median-interval staleness policy.

    A single observation has no measured cadence and becomes stale after 60
    days; otherwise use max(10 days, 2.5 times the median observed interval).
    Callers choose the real observation window; no dates are filled in here.
    """
    try:
        latest = date.fromisoformat(str(latest_date or points[-1]['date'])[:10])
        stamps = sorted({date.fromisoformat(str(point['date'])[:10]) for point in points})
    except (TypeError, ValueError, KeyError, IndexError):
        return None
    gaps = sorted((right - left).days for left, right in zip(stamps, stamps[1:]) if right > left)
    usual = gaps[len(gaps) // 2] if gaps else None
    age = (day - latest).days
    return {'age_days': age, 'usual_interval_days': usual,
            'stale': age > max(10, 2.5 * usual) if usual else age > 60}


def _day(value):
    if type(value) is date:
        return value
    if not isinstance(value, str):
        raise ValueError('回测日期应为YYYY-MM-DD')
    parsed = date.fromisoformat(value)
    if parsed.isoformat() != value:
        raise ValueError('回测日期应为YYYY-MM-DD')
    return parsed


def validate_rules(rules, logic):
    """Storage-independent bounds; authorization belongs to the calling reader."""
    if logic not in {'all', 'any'} or not isinstance(rules, list) or not 1 <= len(rules) <= 6:
        raise ValueError('监测需要1至6条规则，组合方式为all或any')
    for rule in rules:
        if (not isinstance(rule, dict) or not isinstance(rule.get('series_id'), str)
                or not 1 <= len(rule['series_id']) <= 256
                or rule.get('metric') not in {'value', 'change'} or rule.get('operator') not in OPS
                or type(rule.get('value')) not in {int, float}
                or not isfinite(rule['value']) or type(rule.get('consecutive_periods')) is not int
                or not 1 <= rule['consecutive_periods'] <= 120):
            raise ValueError('监测规则的指标、比较值或连续期数无效')
        decimal_value(rule['value'])
    return rules


def _history(rule, points):
    """Compute each observed period once; calendar alignment reuses its state."""
    states = condition_period_states(rule['metric'], rule['operator'], rule['value'],
        [point['value'] for point in points], rule['consecutive_periods'])
    result = []
    for point, state in zip(points, states, strict=True):
        quantity = float(state['quantity']) if state['quantity'] is not None else None
        if quantity is not None and not isfinite(quantity):
            raise ValueError('监测变化量超出可表示的数值范围，无法回测')
        result.append((point['date'], state['triggered'], quantity))
    return result


def combine_states(states, logic):
    """Three-valued all/any: unknown never becomes an invented false value."""
    if logic == 'all' and any(state is False for state in states):
        return False
    if logic == 'any' and any(state is True for state in states):
        return True
    if any(state is None for state in states):
        return None
    return all(states) if logic == 'all' else any(states)


def backtest_conditions(rules, logic, series, start, end):
    """Return daily as-of states, entry dates and intervals for verified series.

    ``series`` is ``{id: {points: [{date: 'YYYY-MM-DD', value: number|None}],
    name, unit, frequency, ...}}``. Supply observations before ``start`` for
    change/consecutive warmup. Unknown states use three-valued all/any (a true
    resolves any; a false resolves all). Intervals count observed combination
    updates as periods and report calendar duration separately. A condition already true before the
    requested range is not counted as a new entry on its first calendar day.
    """
    validate_rules(rules, logic)
    first, last = _day(start), _day(end)
    if first > last or (last - first).days > 6 * 366:
        raise ValueError('回测时间范围应为不超过6年的递增日期')
    histories, charts, events = [], [], set()
    for rule in rules:
        source = series.get(rule['series_id']) or {}
        points, dates = [], set()
        for point in source.get('points') or []:
            stamp = _day(point['date']).isoformat()
            if stamp > last.isoformat():
                continue
            value = point.get('value')
            if value is not None and (type(value) not in {int, float} or not isfinite(value)):
                raise ValueError('回测观测必须是有限数值或明确缺失值')
            if stamp in dates:
                raise ValueError('回测观测日期重复，请先确定真实修订版本')
            dates.add(stamp)
            points.append({'date': stamp, 'value': value})
        points.sort(key=lambda point: point['date'])
        history = _history(rule, points)
        histories.append(history)
        events.update(stamp for stamp, _state, _value in history)
        valid = [point for point in points if point['value'] is not None]
        latest = valid[-1]['date'] if valid else None
        freshness = observation_freshness(valid[-26:], last)
        condition_state = history[-1][1] if history else None
        notice = (f'该指标最新数据 {latest}，监测可能不会更新'
                  if freshness and freshness['stale'] else '')
        if condition_state is None:
            notice = '；'.join(filter(None, [notice, '判定窗口包含缺失观测或观测期数不足，无法判断本条规则']))
        charts.append({'series_id': rule['series_id'], 'name': source.get('name') or rule.get('label') or '',
            'unit': source.get('unit') or '', 'frequency': source.get('frequency') or '',
            'rule': rule, 'state': '无数据' if condition_state is None else '触发' if condition_state else '未触发',
            'points': [point for point in points if point['date'] >= first.isoformat()],
            'quantities': [{'date': stamp, 'value': value} for stamp, _state, value in history
                           if stamp >= first.isoformat()],
            'latest_date': points[-1]['date'] if points else None, 'latest_valid_date': latest,
            'freshness': freshness, 'notice': notice,
            'limited': bool(source.get('limited')), 'point_limit': source.get('point_limit')})

    states, current_values = [None] * len(rules), [None] * len(rules)
    indexes = [0] * len(rules)
    daily, intervals, opened = [], [], None
    previous = None
    # Warmup observations seed as-of state without manufacturing an entry at start.
    all_dates = sorted(events | {first.isoformat(), last.isoformat()})
    snapshots = {}
    for stamp in all_dates:
        changed = stamp in events
        for index, history in enumerate(histories):
            while indexes[index] < len(history) and history[indexes[index]][0] <= stamp:
                _date, states[index], current_values[index] = history[indexes[index]]
                indexes[index] += 1
        combined = combine_states(states, logic)
        if combined is True and previous is not True:
            opened = {'start': stamp, 'end': stamp, 'duration_periods': 0, 'duration_days': 1,
                      'ongoing': True, 'started_before_range': stamp < first.isoformat()}
            intervals.append(opened)
        if opened is not None and combined is True:
            opened['end'] = stamp
            if changed:
                opened['duration_periods'] += 1
        if combined is not True and opened is not None:
            opened['end'] = (_day(stamp) - timedelta(days=1)).isoformat()
            opened['ongoing'] = False
            opened = None
        snapshots[stamp] = {'triggered': combined, 'rule_states': list(states),
                            'values': list(current_values)}
        previous = combined
    for interval in intervals:
        interval['duration_days'] = (_day(interval['end']) - _day(interval['start'])).days + 1
    intervals = [interval for interval in intervals if interval['end'] >= first.isoformat()]
    entries = [interval['start'] for interval in intervals if not interval['started_before_range']]

    snapshot_dates = iter(sorted(snapshots))
    next_stamp = next(snapshot_dates, None)
    current = None
    for offset in range((last - first).days + 1):
        stamp = (first + timedelta(days=offset)).isoformat()
        while next_stamp is not None and next_stamp <= stamp:
            current = snapshots[next_stamp]
            next_stamp = next(snapshot_dates, None)
        triggered = current['triggered']
        daily.append({'date': stamp, **current,
                      'state': '无数据' if triggered is None else '触发' if triggered else '未触发'})
    condition_state = daily[-1]['state']
    stale = any(chart['freshness'] and chart['freshness']['stale'] for chart in charts)
    current_state = ('无数据' if condition_state == '无数据' else
                     '数据停更' if stale else condition_state)
    return {'start': first.isoformat(), 'end': last.isoformat(), 'logic': logic,
            'daily_states': daily, 'trigger_dates': entries, 'intervals': intervals,
            'trigger_count': len(entries), 'last_trigger': entries[-1] if entries else None,
            'current_state': current_state, 'condition_state': condition_state,
            'triggered': current_state == '触发', 'series': charts,
            'notices': list(dict.fromkeys(chart['notice'] for chart in charts if chart['notice']))}
