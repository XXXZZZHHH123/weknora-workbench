"""Authorized, bounded observation reads for reusable condition backtests."""
from collections import defaultdict
from datetime import date, timedelta
from math import ceil

from fastapi import HTTPException

from tin.compute.condition_backtest import backtest_conditions, validate_rules
from tin.web.chart_data import catalog_fields, chart_read_scope, cutoff, read_series
from tin.web.dimension_data import FREQUENCY_DAYS, LOW_FREQUENCIES


def _years(field):
    return 5 if str(field.get('frequency') or '').strip().lower() in LOW_FREQUENCIES else 2


def backtest_monitor(scope, rules, logic, start=None, end=None):
    """Read roughly two daily/weekly or five monthly/low-frequency years.

    Every request stays below 256 series / 20,000 points and 2,000 points per
    series. Extra observations precede the displayed interval for change and
    consecutive-period warmup. Mixed-frequency combinations use the common
    two-year interval; callers may supply an explicit interval up to six years.
    """
    validate_rules(rules, logic)
    if scope.restricted:
        raise HTTPException(403, '监测暂不对受限视角开放')
    end_day = end if type(end) is date else cutoff(end)
    today = cutoff()
    if end_day > today:
        raise HTTPException(422, '回测不能读取未来日期')
    identifiers = list(dict.fromkeys(rule['series_id'] for rule in rules))
    with chart_read_scope():
        fields = catalog_fields(scope, identifiers)
        if any(identifier not in fields for identifier in identifiers):
            raise HTTPException(422, '监测指标不在当前研究台授权目录中')
        years = min(_years(fields[identifier]) for identifier in identifiers)
        start_day = start if type(start) is date else cutoff(start) if start is not None else None
        start_day = start_day or end_day - timedelta(days=years * 365 + 1)
        if start_day > end_day or (end_day - start_day).days > 6 * 366:
            raise HTTPException(422, '回测时间范围应为不超过6年的递增日期')
        groups = defaultdict(list)
        for identifier in identifiers:
            field = fields[identifier]
            interval = FREQUENCY_DAYS.get(str(field.get('frequency') or '').strip().lower(), 1)
            history_days = max((_years(field) * 366), (end_day - start_day).days)
            warmup = max(rule['consecutive_periods'] + int(rule['metric'] == 'change')
                         for rule in rules if rule['series_id'] == identifier)
            limit = ceil(history_days / interval) + warmup + 2
            if limit > 2000:
                raise HTTPException(413, '回测所需窗口超过单指标2000点，请缩短时间范围或连续期数')
            groups[limit].append(identifier)
        data = {}
        for limit, selected in groups.items():
            batch_size = min(256, 20000 // limit)
            for offset in range(0, len(selected), batch_size):
                data.update(read_series(selected[offset:offset + batch_size], end_day, limit=limit, preserve_missing=True))
        for identifier in identifiers:
            source = data.setdefault(identifier, {'points': []})
            source['frequency'] = fields[identifier].get('frequency') or ''
            source.setdefault('name', fields[identifier].get('metric') or fields[identifier].get('name') or '')
            source.setdefault('unit', fields[identifier].get('unit') or '')
    try:
        return backtest_conditions(rules, logic, data, start_day, end_day)
    except ValueError as error:
        raise HTTPException(422, str(error)) from None
