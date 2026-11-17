"""Verified source coverage for monitors; metadata never changes a binding."""
from datetime import date

from fastapi import HTTPException

from tin.compute.condition_backtest import observation_freshness
from tin.integration.observation_windows import paged_observations
from tin.integration.page_facts import bounded_facts, _validated_calendar
from tin.web.chart_data import catalog_fields, cutoff
from tin.web.dimension_data import FREQUENCY_DAYS, LOW_FREQUENCIES

OVERLAP_WARNING = '规则之间重叠数据不足，建议更换覆盖更长的指标'


def years_before(day, years):
    """Calendar years, including the February 29 -> February 28 boundary."""
    try:
        return day.replace(year=day.year - years)
    except ValueError:
        if day.year <= years:
            return date.min
        return day.replace(year=day.year - years, day=28)


def _catalog_day(value):
    if value is None:
        return None
    try:
        parsed = date.fromisoformat(value)
        if parsed.isoformat() != value:
            raise ValueError
        return value
    except (TypeError, ValueError):
        raise HTTPException(503, '指标目录的数据覆盖日期无效') from None


def _versions(facts, versions):
    for binding, version in facts.get('source_versions', {}).items():
        if binding in versions and versions[binding] != version:
            raise HTTPException(409, '监测来源在读取覆盖期间变化，请刷新')
        versions[binding] = version


def read_coverage(scope, identifiers, *, fields=None, day=None, versions=None):
    """Read full valid-date bounds from a verified catalog or source calendar.

    The source calendar is requested separately per indicator so another
    indicator cannot supply its bounds. Older sources without that calendar
    use a bounded observation fallback. A capped fallback's first point is
    explicitly *not* presented as the first observation in the full series.
    """
    today = day or cutoff()
    identifiers = list(dict.fromkeys(identifiers))
    fields = catalog_fields(scope, identifiers) if fields is None else fields
    if any(sid not in fields for sid in identifiers):
        raise HTTPException(422, '监测指标不在当前研究台授权目录中')
    versions = {} if versions is None else versions
    result = {}
    for sid in identifiers:
        field = fields[sid]
        frequency = str(field.get('frequency') or '')
        first, latest = _catalog_day(field.get('first_valid_date')), _catalog_day(field.get('last_valid_date'))
        count = field.get('valid_count')
        count = count if type(count) is int and count >= 0 else None
        metadata = (first is not None and latest is not None and latest <= today.isoformat()
                    or count == 0 and first is None and latest is None)
        source, limited, observed_start, sample = 'catalog', False, first, []
        if not metadata:
            facts = bounded_facts(scope, [sid], 'judgments', day=today.isoformat(), points=26,
                                  direct_only=True, include_invalid=True)
            _versions(facts, versions)
            sample = [row for row in facts['rows'] if row['series_id'] == sid and row['value'] is not None]
            calendar = _validated_calendar(facts.get('calendar'), today.isoformat())
            if calendar is not None:
                first, latest = calendar.get('min_date'), calendar.get('max_date')
                source, count, observed_start = 'source_calendar', None, first
                earliest_read = min((row['date'] for row in facts['rows']), default=None)
                if (first is None or len(facts['rows']) < 26
                        or earliest_read is not None and first >= earliest_read):
                    count = len(sample)
            else:
                rows = paged_observations(scope, [sid], today, 2000, direct_only=True,
                    versions=versions, version_notice='监测来源在读取覆盖期间变化，请刷新')[sid]
                sample = [row for row in rows if row['value'] is not None]
                limited = len(rows) == 2000
                observed_start = sample[0]['date'] if sample else None
                first = None if limited else observed_start
                latest = sample[-1]['date'] if sample else None
                count = None if limited else len(sample)
                source = 'observations'
        if first and latest and first > latest:
            raise HTTPException(503, '指标目录的数据覆盖区间无效')
        freshness = observation_freshness(sample[-26:], today, latest_date=latest) if latest else None
        if latest and (freshness is None or freshness['usual_interval_days'] is None):
            interval = FREQUENCY_DAYS.get(frequency.strip().lower())
            age = (today - date.fromisoformat(latest)).days
            freshness = {'age_days': age, 'usual_interval_days': interval,
                         'stale': age > max(10, 2.5 * interval) if interval else age > 60}
        result[sid] = {'start': first, 'end': latest, 'point_count': count, 'frequency': frequency,
            'stale': bool(freshness and freshness['stale']), 'freshness': freshness,
            'limited': limited, 'observed_start': observed_start, 'source': source,
            'default_years': 5 if frequency.strip().lower() in LOW_FREQUENCIES else 2}
    return result


def default_window(fields, coverages, today=None):
    today = today or cutoff()
    selected = [coverages[sid] for sid in fields]
    latest = [item['end'] for item in selected if item.get('end')]
    end = min(today, date.fromisoformat(min(latest))) if latest else today
    years = min(5 if str(field.get('frequency') or '').strip().lower() in LOW_FREQUENCIES else 2
                for field in fields.values())
    if not latest:
        reason = '暂无有效观测，暂以今天为回测终点'
    elif len(latest) != len(selected):
        reason = f'可读指标数据最新至 {end.isoformat()}，按可用数据确定回测终点'
    else:
        reason = (f'数据最新至 {end.isoformat()}，按数据可用区间回测'
                  if end < today else '各规则指标数据覆盖至今天，按默认区间回测')
    if len(latest) != len(selected):
        reason += '；部分规则指标没有有效观测，无法确定完整共同数据区间'
    return {'start': years_before(end, years).isoformat(), 'end': end.isoformat(), 'window_reason': reason}


def rule_overlap(rules, coverages, observations=None, *, start=None, end=None):
    """Intersect valid bounds; count real observations, never frequency estimates.

    Observation periods are the minimum per-rule valid count in the overlap,
    permitting different update calendars. A capped window reports a lower
    bound rather than claiming its observed count is the complete count.
    """
    selected = [coverages[rule['series_id']] for rule in rules]
    starts = [item.get('start') for item in selected]
    ends = [item.get('end') for item in selected]
    first = max(starts) if all(starts) else None
    last = min(ends) if all(ends) else None
    if first and start:
        first = max(first, str(start))
    if last and end:
        last = min(last, str(end))
    unknown_start = all(ends) and not all(starts)
    days = (max(0, (date.fromisoformat(last) - date.fromisoformat(first)).days + 1)
            if first and last else None if unknown_start else 0)
    if first and last and first > last:
        first = last = None
    counts, limits, limited = [], [], False
    exact_short = any(item.get('point_count') is not None and item['point_count'] < 10 for item in selected)
    bound = 'exact'
    if observations is not None and not unknown_start:
        for rule in rules:
            source = observations.get(rule['series_id'], {})
            points = source.get('points') or []
            count = sum(point['value'] is not None and first <= point['date'] <= last
                        for point in points) if first and last else 0
            counts.append(count)
            earliest = points[0]['date'] if points else None
            latest = points[-1]['date'] if points else None
            read_end = source.get('as_of') or latest
            source_limited = bool(first and last and (
                source.get('limited') and (earliest is None or earliest > first)
                or read_end is None or read_end < last))
            limits.append(source_limited)
            exact_short |= count < 10 and not source_limited
        limited = all(flag for count, flag in zip(counts, limits) if count == min(counts))
        bound = 'lower' if limited else 'exact'
    elif days == 0:
        counts = [0]
    elif all(item.get('point_count') is not None for item in selected):
        # Total valid counts bound overlap counts from above, and can prove a
        # shortage. They do not tell us an exact count in a narrower interval.
        total = min(item['point_count'] for item in selected)
        if total < 10:
            counts, limited = [total], True
            bound = 'upper'
    periods = min(counts) if counts else None
    insufficient = len(rules) > 1 and (days is not None and days < 180 or exact_short
                                      or periods is not None and periods < 10 and not limited)
    if len(rules) > 1 and observations is None and periods is not None and periods < 10:
        insufficient = True
    return {'start': first, 'end': last, 'days': days, 'observation_periods': periods,
            'observation_periods_limited': limited, 'insufficient': insufficient,
            'observation_periods_bound': bound if periods is not None else None,
            'warning': OVERLAP_WARNING if insufficient else '',
            'observation_periods_method': ('各规则在重叠区间内有效观测数的最小值'
                if observations is not None else '候选总有效观测数的上界，回测后统计重叠区间内实际观测')}
