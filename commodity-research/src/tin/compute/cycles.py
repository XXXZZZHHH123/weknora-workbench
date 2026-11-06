"""Calendar cycle comparison on real observations only; no I/O or imputation.

Intervals are inclusive. Ties choose the earlier observation. Forward changes
use raw observations, including observations outside the displayed segments.
"""
from bisect import bisect_left
from copy import deepcopy
from datetime import date, timedelta
from math import floor, isfinite
from statistics import mean, median

from pydantic import TypeAdapter

from tin.schemas.chart import CycleTemplate

SPRING_FESTIVAL = tuple(date.fromisoformat(day) for day in (
    '2000-02-05 2001-01-24 2002-02-12 2003-02-01 2004-01-22 2005-02-09 2006-01-29 2007-02-18 '
    '2008-02-07 2009-01-26 2010-02-14 2011-02-03 2012-01-23 2013-02-10 2014-01-31 2015-02-19 '
    '2016-02-08 2017-01-28 2018-02-16 2019-02-05 2020-01-25 2021-02-12 2022-02-01 2023-01-22 '
    '2024-02-10 2025-01-29 2026-02-17 2027-02-06 2028-01-26 2029-02-13 2030-02-03'
).split())

BUILTIN_TEMPLATES = {
    '自然年': {'kind': 'repeat', 'name': '自然年', 'start': '01-01', 'period': 'year',
            'align': 'days', 'transform': 'raw'},
    '春节前后8周': {'kind': 'event', 'name': '春节前后8周', 'calendar': 'spring_festival',
                'before_days': 56, 'after_days': 56, 'align': 'days', 'transform': 'raw'},
}
_TEMPLATE = TypeAdapter(CycleTemplate)


def _date(value):
    return value if type(value) is date else date.fromisoformat(value)


def repeat_bounds(template, day):
    """Containing calendar interval. Invalid calendar starts are skipped, never invented."""
    day = _date(day)
    month, dom = map(int, template['start'].split('-'))
    step = {'year': 12, 'half': 6, 'quarter': 3, 'month': 1}[template['period']]
    anchor = month - 1 if step != 1 else 0
    cursor = (day.year - 1) * 12 + day.month - 1
    cursor -= (cursor - anchor) % step

    def boundary(index):
        year, mon = divmod(index, 12)
        try:
            return date(year + 1, mon + 1, dom)
        except ValueError:
            return None

    start = boundary(cursor)
    while start is None or start > day:
        cursor -= step
        if cursor < 0:
            raise ValueError('日期早于首个有效周期起点')
        start = boundary(cursor)
    next_cursor = cursor + step
    end = boundary(next_cursor)
    while end is None:
        next_cursor += step
        if next_cursor >= 9999 * 12:
            raise ValueError('周期终点超出日历范围')
        end = boundary(next_cursor)
    return start, end - timedelta(days=1)


def _intervals(template, observations, as_of):
    if template['kind'] == 'segments':
        return [(part['label'], _date(part['start']), _date(part['end']), _date(part['start']))
                for part in template['segments']]
    if not observations:
        return []
    if template['kind'] == 'event':
        anchors = ([(str(day.year), day) for day in SPRING_FESTIVAL] if template.get('calendar') else
                   [(str(_date(item['date']).year), _date(item['date'])) for item in template['anchors']])
        days = [day for day, _ in observations]
        result = []
        for label, anchor in anchors:
            start = anchor - timedelta(days=template['before_days'])
            end = anchor + timedelta(days=template['after_days'])
            index = bisect_left(days, start)
            if index < len(days) and days[index] <= end:
                result.append((label, start, end, anchor))
        return result
    start, end = repeat_bounds(template, observations[0][0])
    intervals = []
    while start <= as_of:
        if template['period'] == 'year':
            label = str(start.year) if (start.month, start.day) == (1, 1) else f'{start.year % 100:02d}/{end.year % 100:02d}'
        else:
            label = start.isoformat()
        intervals.append((label, start, end, start))
        start, end = repeat_bounds(template, end + timedelta(days=1))
    return intervals


def _nearest(points, positions, target, tolerance):
    index = bisect_left(positions, target)
    candidates = [i for i in (index - 1, index) if 0 <= i < len(points)]
    if not candidates:
        return None
    chosen = min(candidates, key=lambda i: (abs(positions[i] - target), positions[i]))
    return points[chosen] if abs(positions[chosen] - target) <= tolerance else None


def _finite_result(result):
    """Keep arithmetic overflow explicit and the read model JSON-safe."""
    unavailable = False

    def clean(value):
        nonlocal unavailable
        if isinstance(value, float) and not isfinite(value):
            unavailable = True
            return None
        if isinstance(value, dict):
            return {key: clean(item) for key, item in value.items()}
        if isinstance(value, list):
            return [clean(item) for item in value]
        return value

    result = clean(result)
    if unavailable and 'non_finite_result' not in result['warnings']:
        result['warnings'].append('non_finite_result')
    return result


def compute_cycles(observations, template, as_of, *, band_cycles=5, horizons=(4, 8, 12), cycles=None,
                   current_start=None):
    """Return the chart.v1 cycle read model, without changing the input.

    A single observation cannot establish a frequency: curves remain available,
    with ``frequency_unavailable`` and ``insufficient_cycles`` notices. Statistical
    fields are omitted when fewer than three eligible past cycles match.
    """
    template = _TEMPLATE.validate_python(template).model_dump(mode='json', exclude_none=True)
    as_of = _date(as_of)
    current_start = _date(current_start) if current_start is not None else None
    if type(band_cycles) is not int or band_cycles < 1:
        raise ValueError('区间带周期数需为正整数')
    if cycles is not None and (type(cycles) is not int or cycles < 1):
        raise ValueError('展示周期数需为正整数')
    if not horizons or len(horizons) > 40 or any(type(h) is not int or not 1 <= h <= 2000 for h in horizons):
        raise ValueError('后续窗口需为明确的正整数期数')
    rows, previous = [], None
    for stamp, value in observations:
        day = _date(stamp)
        if previous is not None and day <= previous:
            raise ValueError('观测必须按日期升序且同日唯一')
        previous = day
        if isinstance(value, bool) or not isinstance(value, (int, float)) or not isfinite(value):
            raise ValueError('周期计算只接受有效有限观测')
        if day <= as_of:
            rows.append((day, float(value)))
    gaps = [(right[0] - left[0]).days for left, right in zip(rows, rows[1:])]
    freq = median(gaps) if gaps else None
    tol = max(1, floor(freq / 2)) if freq is not None else 1
    result = {'template': deepcopy(template), 'freq_days': freq, 'tolerance_days': tol,
              'as_of': as_of.isoformat(), 'segments': [], 'omitted_segments': [],
              'position': None, 'band': [], 'forward': [], 'warnings': []}
    intervals = _intervals(template, rows, as_of)
    sorted_intervals = sorted(intervals, key=lambda part: part[1])
    if any(right[1] <= left[2] for left, right in zip(sorted_intervals, sorted_intervals[1:])):
        result['warnings'].append('segments_overlap')
    if freq is None:
        result['warnings'].append('frequency_unavailable')
    row_days = [day for day, _ in rows]
    candidates = []
    transform = template['transform']
    for label, start, end, anchor in intervals:
        lo = bisect_left(row_days, start)
        hi = bisect_left(row_days, end + timedelta(days=1))
        selected = rows[lo:hi]
        if not selected:
            result['omitted_segments'].append(label)
            continue
        flags = ['incomplete'] if end > as_of else []
        # A stale series must not look sparse just because the cutoff is today:
        # the latest real observation bounds the elapsed part of an open segment.
        effective_days = (min(end, as_of, row_days[-1]) - start).days + 1
        if freq is not None and (effective_days <= 0 or len(selected) / effective_days * freq < 0.5):
            flags.append('sparse')
        if selected and transform != 'raw' and (selected[0][0] - start).days > tol:
            flags.append('start_missing')
        base = selected[0][1] if selected else None
        if transform == 'rebase' and base is not None and base <= 0:
            flags.append('invalid_base')
        points = []
        for day, raw in selected:
            value = (raw if transform == 'raw' else raw - base if transform == 'change' else
                     raw / base * 100 if base > 0 else None)
            if value is not None and not isfinite(value):
                value = None
                if 'invalid_transform' not in flags:
                    flags.append('invalid_transform')
                if 'non_finite_transform' not in result['warnings']:
                    result['warnings'].append('non_finite_transform')
            points.append({'x': (day - anchor).days, 'date': day.isoformat(), 'value': value, 'raw': raw})
        part = {'label': label, 'start': start.isoformat(), 'end': end.isoformat(),
                'current': False, 'flags': flags, 'points': points}
        result['segments'].append(part)
        if points:
            candidates.append((points[-1]['date'], start, len(result['segments']) - 1))
    if result['omitted_segments']:
        result['warnings'].append('empty_segments_omitted')
    if not candidates:
        result['warnings'].extend(['no_observations', 'insufficient_cycles'])
        return _finite_result(result)
    if current_start is None:
        _, _, current_index = max(candidates)
    else:
        current_index = next((index for _, start, index in candidates if start == current_start), None)
        result['current_start'] = current_start.isoformat()
        if current_index is None:
            # A multi-curve comparison shares the period, including for stale
            # series. Their old observations must not masquerade as current.
            available = sorted((part for part in result['segments'] if _date(part['start']) < current_start),
                               key=lambda part: part['start'], reverse=True)
            selected = {id(part) for part in available[:max(0, cycles - 1)]} if cycles is not None else None
            result['segments'] = [part for part in result['segments'] if
                _date(part['start']) < current_start and (selected is None or id(part) in selected)]
            result['warnings'].extend(['current_missing', 'insufficient_cycles'])
            return _finite_result(result)
    current = result['segments'][current_index]
    current['current'] = True
    last = current['points'][-1]
    eligible = lambda part: not set(part['flags']) & {'sparse', 'start_missing', 'invalid_base', 'invalid_transform'}
    past = sorted((part for i, part in enumerate(result['segments'])
                   if i != current_index and part['start'] <= current['start']), key=lambda part: part['start'], reverse=True)
    if cycles is not None:
        # Count back from the actual current observation. Empty calendar years
        # after a stale series must not push its current segment out of view.
        past = past[:cycles - 1]
        visible = {id(part) for part in [current, *past]}
        result['segments'] = [part for part in result['segments'] if id(part) in visible]
    peers = []
    for part in past:
        if not eligible(part):
            continue
        point = _nearest(part['points'], [p['x'] for p in part['points']], last['x'], tol)
        if point is not None:
            peers.append({'label': part['label'], 'date': point['date'], 'value': point['value'], 'raw': point['raw']})
    position = {'x': last['x'], 'date': last['date'], 'value': last['value'], 'n': len(peers),
                'peers': [{key: peer[key] for key in ('label', 'date', 'value')} for peer in peers]}
    result['position'] = position
    if freq is None or len(peers) < 3:
        position['status'] = 'insufficient_cycles'
        result['warnings'].append('insufficient_cycles')
        return _finite_result(result)
    if not eligible(current):
        position['status'] = 'unusable_current'
        return _finite_result(result)
    values = [peer['value'] for peer in peers]
    recent = peers[:5]
    recent_mean = mean(peer['value'] for peer in recent)
    percentage = transform == 'raw' and recent_mean > 0
    position.update(status='ok', rank=1 + sum(value > last['value'] for value in values),
                    share_below=sum(value < last['value'] for value in values) / len(values),
                    min=min(values), max=max(values), median=median(values), mean=mean(values),
                    recent_mean=recent_mean,
                    vs_recent_mean={'kind': 'pct' if percentage else 'change',
                        'value': (last['value'] - recent_mean) / recent_mean if percentage else last['value'] - recent_mean,
                        'cycles': len(recent)})
    band_parts = [part for part in past if eligible(part)][:band_cycles]
    if template['kind'] == 'event':
        first_x, last_x = -template['before_days'], template['after_days']
    else:
        first_x = 0
        last_x = max((_date(part['end']) - _date(part['start'])).days for part in past)
    step = max(1, floor(freq + 0.5))
    band_points = [(part['points'], [point['x'] for point in part['points']]) for part in band_parts]
    for x in range(first_x, last_x + 1, step):
        matches = [_nearest(points, positions, x, tol) for points, positions in band_points]
        values = [match['value'] for match in matches if match is not None]
        if values:
            result['band'].append({'x': x, 'min': min(values), 'max': max(values), 'mean': mean(values), 'n': len(values)})
    ordinals = [day.toordinal() for day, _ in rows]
    for h in horizons:
        items = []
        days = h * freq
        for peer in peers:
            target = _date(peer['date']).toordinal() + days
            following = _nearest(rows, ordinals, target, tol)
            if following is None or following[0] <= _date(peer['date']):
                continue
            change = following[1] - peer['raw']
            if not isfinite(change):
                if 'non_finite_result' not in result['warnings']:
                    result['warnings'].append('non_finite_result')
                continue
            items.append({'label': peer['label'], 'date': peer['date'], 'end_date': following[0].isoformat(),
                          'change': change, 'pct': change / abs(peer['raw']) if peer['raw'] != 0 else None})
        changes = [item['change'] for item in items]
        result['forward'].append({'h': h, 'days': days, 'items': items, 'n': len(items),
                                 'median': median(changes) if changes else None,
                                 'min': min(changes) if changes else None, 'max': max(changes) if changes else None,
                                 'up': sum(change > 0 for change in changes)})
    return _finite_result(result)


def _previous_start(template, current_start):
    """The actual preceding template interval, even when it has no observations."""
    current_start = _date(current_start)
    if template['kind'] == 'repeat':
        return repeat_bounds(template, current_start - timedelta(days=1))[0].isoformat()
    if template['kind'] == 'segments':
        starts = [_date(part['start']) for part in template['segments']]
    else:
        anchors = SPRING_FESTIVAL if template.get('calendar') else [_date(item['date']) for item in template['anchors']]
        starts = [anchor - timedelta(days=template['before_days']) for anchor in anchors]
    earlier = [start for start in starts if start < current_start]
    return max(earlier).isoformat() if earlier else None


def compute_multi_cycles(curves, template, as_of, *, band_cycles=5, cycles=None):
    """Compare curves on one explicit calendar, keeping each curve's statistics.

    Every item uses the most recent period observed by any selected curve. The
    common previous period is derived from the template, not from a label or the
    last period that happened to contain data. Bands are suppressed for multi-
    curve displays; historical observations remain available for statistics.
    """
    if not 2 <= len(curves) <= 8:
        raise ValueError('多指标周期对比需要2至8条曲线')
    template = _TEMPLATE.validate_python(template).model_dump(mode='json', exclude_none=True)
    computed = []
    for curve in curves:
        observations = [(point['date'], point['value']) for point in curve.get('points', [])
                        if type(point['value']) in {int, float} and isfinite(point['value'])]
        cycle = compute_cycles(observations, template, as_of, band_cycles=band_cycles, cycles=cycles)
        computed.append((curve, observations, cycle))
    starts = [part['start'] for _, _, cycle in computed for part in cycle['segments'] if part['current']]
    current_start = max(starts) if starts else None
    previous_start = _previous_start(template, current_start) if current_start else None
    result = {'mode': 'cycle', 'template': deepcopy(template), 'as_of': _date(as_of).isoformat(),
              'common_current_start': current_start, 'common_previous_start': previous_start,
              'curves': [], 'band': [], 'warnings': []}
    for curve, observations, cycle in computed:
        own_start = next((part['start'] for part in cycle['segments'] if part['current']), None)
        if current_start is not None and own_start != current_start:
            cycle = compute_cycles(observations, template, as_of, band_cycles=band_cycles, cycles=cycles,
                                   current_start=current_start)
        cycle['current_start'] = current_start
        cycle['previous_start'] = previous_start
        cycle['band'] = []
        if curve.get('limited') and 'observation_window_limited' not in cycle['warnings']:
            cycle['warnings'].append('observation_window_limited')
        for warning in [*cycle['warnings'], *curve.get('warnings', [])]:
            notice = ({'code': warning} if isinstance(warning, str) else deepcopy(warning))
            notice['label'] = curve.get('label', '')
            if notice not in result['warnings']:
                result['warnings'].append(notice)
        result['curves'].append({**deepcopy(curve), 'cycle': cycle})
    return result
