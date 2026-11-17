"""Read-only dimension observations, with one shared budget for all operands.

Bindings and calendar arithmetic are explicit. Labels never choose sources,
units, groups or a calculation. A failed item stays in its original position.
"""
from copy import deepcopy
from datetime import date, timedelta
from math import isfinite

from fastapi import HTTPException

from tin.compute.cycles import BUILTIN_TEMPLATES, compute_cycles
from tin.integration.workbench_scope import artifact_session
from tin.models.workbench import WorkbenchArtifactRevision
from tin.web import charts
from tin.web.chart_data import catalog_fields, chart_read_scope, cutoff, definition_series_ids, read_series
from tin.web.number_format import format_number, source_decimals
from tin.web.strategy_monitor import _staleness

MAX_POINTS = 20000
MAX_SERIES = 256
SERIES_POINTS = 2000
# These are source frequency protocol values, never indicator-name matching.
HIGH_FREQUENCIES = frozenset(('日', '日度', '周', '周度', 'daily', 'weekly'))
LOW_FREQUENCIES = frozenset(('月', '月度', '季', '季度', '半年', '年', '年度',
                            'monthly', 'quarterly', 'semiannual', 'annual', 'yearly'))


def _reason(error):
    return str(error.detail) if isinstance(error, HTTPException) else f'条目读取或计算失败（{type(error).__name__}）'


def _fatal(error):
    # A partial result must never retry against a different authorization or
    # source revision. Failures of a single ordinary source are isolated below.
    return isinstance(error, HTTPException) and error.status_code in {401, 403, 409}


def _observations(series, day):
    return [point for point in series.get('points', [])
            if point['date'] <= day.isoformat() and type(point.get('value')) in {int, float}
            and isfinite(point['value'])]


def indicator_data(identifier, field, series, day, period):
    """Calculate one card from actual observations, retaining the real unit."""
    points = _observations(series, day)
    if not points:
        raise HTTPException(404, '截至所选日期没有有效观测')
    unit = field.get('unit') or series.get('unit') or ''
    if ((field.get('unit') or '').strip() and
            field['unit'].strip() != (series.get('unit') or '').strip()):
        raise HTTPException(409, '指标单位在目录与观测读取之间已变化，请刷新核对')
    latest = points[-1]
    frequency = field.get('frequency') or ''
    frequency_key = str(frequency).strip().lower()
    low_frequency = (frequency_key in LOW_FREQUENCIES or
                     frequency_key not in HIGH_FREQUENCIES and (series.get('freq_days') or 0) >= 28)
    previous = None
    target = None
    if low_frequency or period == 'daily':
        previous = points[-2] if len(points) > 1 else None
    else:
        target = (day - timedelta(days=7)).isoformat()
        previous = next((point for point in reversed(points) if point['date'] <= target), None)
    change = latest['value'] - previous['value'] if previous else None
    change = change if change is None or isfinite(change) else None
    pct = change / abs(previous['value']) if change is not None and previous['value'] != 0 else None
    pct = pct if pct is None or isfinite(pct) else None
    label = '较上期' if low_frequency else ('较 ' + previous['date'][5:] if previous else '缺少对比观测')
    baseline = {'date': previous['date'] if previous else None, 'value': previous['value'] if previous else None,
                'label': label, 'target_date': target, 'kind': 'previous' if low_frequency or period == 'daily' else 'week_ago'}
    template = deepcopy(BUILTIN_TEMPLATES['自然年'])
    # The cycle engine infers matching tolerance from the observed frequency;
    # every frequency uses the built-in natural-year calendar, not a name rule.
    cycle = compute_cycles([(point['date'], point['value']) for point in points], template, day,
                           cycles=6, band_cycles=5, details=False)
    raw_position = cycle.get('position') or {}
    n, rank = raw_position.get('n', 0), raw_position.get('rank')
    total = n + 1
    rank_low = total + 1 - rank if rank is not None else None
    notice = ('有效往期同期样本不足3年，不提供排名' if n < 3 else
              '当前周期观测覆盖不足，不提供排名' if rank is None else '')
    text = (f'近 {total} 年同期第 {rank_low} 低' if rank_low is not None and rank_low < rank else
            f'近 {total} 年同期第 {rank} 高' if rank is not None else notice)
    position = {**raw_position, 'n': n, 'rank': rank, 'total': total, 'rank_low': rank_low,
                'status': raw_position.get('status', 'insufficient_cycles'), 'notice': notice,
                'text': text, 'template': template}
    precision = source_decimals(field)
    warnings = list(cycle['warnings'])
    if series.get('limited'):
        warnings.append('history_limited')
    if previous is None:
        warnings.append('baseline_unavailable')
    return {'kind': 'indicator', 'id': identifier, 'series_id': identifier, 'status': 'ok',
            'name': field.get('metric') or field.get('name') or series.get('name') or identifier,
            'unit': unit, 'frequency': frequency, 'source': field.get('source') or field.get('source_file') or '',
            'source_file': field.get('source_file') or '', 'sheet': field.get('sheet') or '',
            'latest': latest['value'], 'date': latest['date'],
            'previous': previous['value'] if previous else None,
            'previous_date': previous['date'] if previous else None, 'baseline': baseline,
            'change': change, 'change_pct': pct,
            'direction': None if change is None else 'up' if change > 0 else 'down' if change < 0 else 'flat',
            'freshness': _staleness({'date': latest['date'], 'points': points[-26:]}, day),
            'position': position, 'points': points[-26:], 'warnings': warnings,
            'limited': bool(series.get('limited')), 'point_limit': series.get('point_limit'),
            'display_decimals': precision,
            'display': {'latest': format_number(latest['value'], decimals=precision),
                        'change': format_number(change, decimals=precision, signed=True),
                        'change_pct': format_number(pct * 100 if pct is not None else None, signed=True) + ('%' if pct is not None else '')}}


def pinned_definition(session, item):
    """Resolve only the named workbench chart and its explicit saved revision."""
    charts.load_artifact(session, item['id'])
    revision = session.get(WorkbenchArtifactRevision, (item['id'], item['revision']))
    if revision is None or revision.status == 'deleted':
        raise HTTPException(404, '所绑定的图表修订不存在')
    return charts.decode_definition(revision)


def _chart_definition(scope, item):
    charts.require_scope()
    with artifact_session(scope) as session:
        definition = pinned_definition(session, item)
        row = charts.load_artifact(session, item['id'])
        return definition, '已有新修订，仍显示绑定修订' if row.revision != item['revision'] else ''


@chart_read_scope()
def dimension_data(scope, dimensions, day=None, period='weekly'):
    """Return ordered card data; successful reads share at most 20,000 points.

    Distinct (indicator ID, effective cutoff) windows share the same per-series
    limit. Ordinary source failures are bisected, while permission/source-change
    failures reject the request. ``day=None`` selects the latest observed date.
    """
    if period not in {'daily', 'weekly'}:
        raise HTTPException(400, '周期应为daily或weekly')
    if scope is not None and scope.restricted:
        raise HTTPException(403, '受限视角暂不开放研究维度原始数据')
    requested_day = day
    day = day if type(day) is date else cutoff(day)
    indicator_cutoff = day
    output = deepcopy(dimensions)
    for dimension in output:
        dimension.setdefault('sections', [])
    windows, chart_definitions, chart_cutoffs, errors = {}, {}, {}, {}

    def plan(identifiers, effective_day):
        selected = windows.setdefault(effective_day, {})
        selected.update(dict.fromkeys(identifiers))

    for dimension in output:
        for section in dimension.get('sections', []):
            for item in section.get('items', []):
                key = (item['kind'], item['id'])
                if item['kind'] == 'indicator':
                    plan([item['id']], day)
                else:
                    try:
                        definition, notice = _chart_definition(scope, item)
                        chart_definitions[key] = (definition, notice)
                        effective_day = min(day, definition.time.end) if definition.time.mode == 'range' else day
                        chart_cutoffs[key] = effective_day
                        plan(definition_series_ids(definition), effective_day)
                    except Exception as error:
                        if isinstance(error, HTTPException) and error.status_code in {401, 403}:
                            raise
                        errors[key] = _reason(error)
    window_count = sum(len(identifiers) for identifiers in windows.values())
    point_limit = min(SERIES_POINTS, MAX_POINTS // max(1, window_count))
    fields, series, read_errors = {}, {}, {}

    def read_batch(batch, effective_day):
        try:
            metadata = catalog_fields(scope, batch)
            data = read_series([sid for sid in batch if sid in metadata], effective_day, limit=point_limit)
        except Exception as error:
            if _fatal(error):
                raise
            if len(batch) > 1:
                middle = len(batch) // 2
                read_batch(batch[:middle], effective_day)
                read_batch(batch[middle:], effective_day)
            else:
                read_errors[(batch[0], effective_day)] = _reason(error)
            return
        fields.update(metadata)
        series.update(((sid, effective_day), value) for sid, value in data.items())
        for sid in batch:
            if sid not in metadata:
                read_errors[(sid, effective_day)] = '指标已不在当前授权来源目录'
            elif sid not in data:
                read_errors[(sid, effective_day)] = '指标观测不可读'

    batch_size = min(MAX_SERIES, MAX_POINTS // max(1, point_limit))
    for effective_day, selected in windows.items():
        identifiers = list(selected)
        for offset in range(0, len(identifiers), batch_size):
            read_batch(identifiers[offset:offset + batch_size], effective_day)
    versions = {item.get('source_version') for item in series.values() if item.get('source_version')}
    if len(versions) > 1:
        raise HTTPException(409, '研究维度来源在读取期间已变化，请刷新')
    dates = [point['date'] for item in series.values() for point in _observations(item, day)]
    latest_data_date = max(dates) if dates else None
    if requested_day is None and latest_data_date:
        day = date.fromisoformat(latest_data_date)
    failed = []
    for dimension in output:
        summary = dict.fromkeys(('up', 'down', 'flat', 'extreme', 'extreme_high', 'extreme_low', 'unavailable'), 0)
        for section in dimension.get('sections', []):
            cards = []
            for item in section.get('items', []):
                key = (item['kind'], item['id'])
                try:
                    if key in errors:
                        raise HTTPException(404, errors[key])
                    if item['kind'] == 'indicator':
                        window = (item['id'], indicator_cutoff)
                        if window in read_errors:
                            raise HTTPException(404, read_errors[window])
                        card = indicator_data(item['id'], fields[item['id']], series[window], day, period)
                        if card['direction']:
                            summary[card['direction']] += 1
                        position = card['position']
                        high = position['rank'] is not None and position['rank'] <= 2
                        low = position['rank_low'] is not None and position['rank_low'] <= 2
                        summary['extreme'] += int(high or low)
                        summary['extreme_high'] += int(high)
                        summary['extreme_low'] += int(low)
                    else:
                        definition, notice = chart_definitions[key]
                        ids = definition_series_ids(definition)
                        effective_day = chart_cutoffs[key]
                        missing = next((read_errors[(sid, effective_day)] for sid in ids
                                        if (sid, effective_day) in read_errors), None)
                        if missing:
                            raise HTTPException(404, missing)
                        actual_day = min(day, definition.time.end) if definition.time.mode == 'range' else day
                        if not any(_observations(series[(sid, effective_day)], actual_day) for sid in ids):
                            raise HTTPException(404, '截至图表日期没有有效观测')
                        chart_limit = min(point_limit, MAX_POINTS // max(len(definition.curves), len(ids)))
                        chart_series = {}
                        for sid in ids:
                            source = series[(sid, effective_day)]
                            points = [point for point in source['points'] if point['date'] <= actual_day.isoformat()][-chart_limit:]
                            if definition.time.mode == 'recent':
                                points = points[-definition.time.points:]
                            chart_series[sid] = {**source, 'points': points}
                        payload = charts.definition_data(scope, definition, actual_day, chart_id=item['id'], revision=item['revision'],
                            fields={sid: fields[sid] for sid in ids}, data=chart_series, point_limit=chart_limit)
                        sources = [{'id': sid, 'name': fields[sid].get('metric') or fields[sid].get('name') or sid,
                                    'unit': fields[sid].get('unit') or '', 'frequency': fields[sid].get('frequency') or '',
                                    'source': fields[sid].get('source_file') or fields[sid].get('source') or '',
                                    'sheet': fields[sid].get('sheet') or ''} for sid in ids]
                        card = {**item, 'status': 'ok', 'name': definition.title, 'title': definition.title,
                                'unit': ' / '.join(dict.fromkeys(source['unit'] for source in sources if source['unit'])),
                                'frequency': ' / '.join(dict.fromkeys(source['frequency'] for source in sources if source['frequency'])),
                                'source': ' / '.join(dict.fromkeys(source['source'] for source in sources if source['source'])),
                                'sources': sources, 'definition': definition.model_dump(mode='json'),
                                'payload': payload, 'notice': notice}
                    cards.append(card)
                except Exception as error:
                    if isinstance(error, HTTPException) and error.status_code in {401, 403}:
                        raise
                    reason = _reason(error)
                    failed.append({**item, 'reason': reason})
                    cards.append({**item, 'status': 'unavailable', 'reason': reason})
                    summary['unavailable'] += 1
            section['items'] = cards
        dimension['summary'] = summary
    warnings = ['history_limited'] if any(item.get('limited') for item in series.values()) else []
    return {'as_of': day.isoformat(), 'latest_data_date': latest_data_date, 'period': period,
            'dimensions': output, 'failed': failed, 'point_limit': point_limit, 'point_budget': MAX_POINTS,
            'warnings': warnings}
