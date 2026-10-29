"""A read-only research comparison over the selected saved judgment."""
from copy import deepcopy
import math

from fastapi import HTTPException

from tin.compute.rule_values import decimal_difference, decimal_value
from tin.compute.signals import recent_points, _falsifier_signal, _transient_signals
from tin.config import SHANGHAI
from tin.judgments.service import _refs, to_payload
from tin.models import Indicator
from tin.schemas.judgment import EXPRESSION
from tin.web.report_watch import watch_items
from tin.web.research_display import condition_text, human_text


def _valid(point):
    return point.status == '正常' and type(point.value) in {int, float} and math.isfinite(point.value)


def _finite(value):
    converted = float(value)
    return converted if math.isfinite(converted) else None


def _stamp(point):
    return point.as_of.astimezone(SHANGHAI).isoformat()


def _chart(rows):
    valid = [row for row in rows if _valid(row)]
    if not valid:
        return [], []
    low, high = min(row.value for row in valid), max(row.value for row in valid)
    span = decimal_difference(high, low)
    first, last = valid[0].as_of, valid[-1].as_of
    duration = (last - first).total_seconds()
    points, segments, segment = [], [], []
    for row in rows:
        if not _valid(row):
            if segment:
                segments.append(' '.join(segment))
                segment = []
            continue
        x = (row.as_of - first).total_seconds() / duration * 240 if duration else 120
        y = 24 if not span else 3 + float(decimal_difference(high, row.value) / span) * 42
        points.append({'date': _stamp(row), 'value': row.value, 'x': x, 'y': y})
        segment.append(f'{x:.2f},{y:.2f}')
    if segment:
        segments.append(' '.join(segment))
    return points, segments


def _metric(session, identifier, day):
    indicator = session.get(Indicator, identifier)
    rows = recent_points(session, identifier, day, 24) if indicator is not None and day is not None else []
    valid = [row for row in rows if _valid(row)]
    latest = valid[-1] if valid else None
    previous = valid[-2] if len(valid) > 1 else None
    change = decimal_difference(latest.value, previous.value) if previous is not None else None
    points, segments = _chart(rows)
    name = human_text(indicator.name) if indicator is not None else ''
    return {'series_id': identifier, 'name': name or '指标名称待核对',
        'unit': indicator.unit if indicator is not None else '',
        'latest': latest.value if latest is not None else None,
        'previous': previous.value if previous is not None else None,
        'date': _stamp(latest) if latest is not None else None,
        'previous_date': _stamp(previous) if previous is not None else None,
        'change': _finite(change) if change is not None else None,
        'change_pct': _finite(change / decimal_value(previous.value) * 100)
            if previous is not None and previous.value != 0 else None,
        'points': points, 'segments': segments, 'svg_points': segments[0] if len(segments) == 1 else '',
        'point_count': len(points)}


def _windows(payload):
    windows = {identifier: 24 for _, _, identifier in _refs(payload)}
    for rule in payload.falsifiers:
        match = EXPRESSION.fullmatch(rule.expression.strip())
        if match is None or not 1 <= rule.consecutive_periods <= 120:
            raise HTTPException(409, '已保存的证伪规则窗口无效，请核对研究逻辑')
        windows[rule.series_id] = max(windows[rule.series_id],
            rule.consecutive_periods + int(match[1] == 'change'))
    if len(windows) > 256 or sum(windows.values()) > 20000:
        raise HTTPException(413, '当前研究引用超过必要观测范围，请拆分研究逻辑')
    return windows


def _falsifier(session, judgment, rule, day, metric):
    match = EXPRESSION.fullmatch(rule['expression'].strip())
    periods = rule.get('consecutive_periods', 1)
    need = periods + int(match[1] == 'change')
    indicator = session.get(Indicator, rule['series_id'])
    rows = recent_points(session, rule['series_id'], day, need) if indicator is not None and day is not None else []
    missing = None
    if day is None:
        missing = '暂无可用观测日期。'
    elif indicator is None:
        missing = '尚无可读指标数据。'
    elif not indicator.unit or indicator.unit != rule.get('unit_confirmed'):
        missing = '指标单位已变化或尚未确认，请核对绑定。'
    elif len(rows) < need:
        missing = f'需要 {need} 个数据点，当前只有 {len(rows)} 个。'
    elif any(not _valid(row) for row in rows):
        missing = '相关观测存在无效数值，暂不能判定。'
    signal = None
    if missing is None:
        try:
            signal = _falsifier_signal(session, judgment, rule, day)
        except (ValueError, TypeError, ArithmeticError):
            missing = '已保存条件需核对，暂不能判定。'
    condition = condition_text(rule['expression']).replace('较上一期变化', '较上一数据点变化')
    if rule.get('unit_confirmed'):
        condition += ' ' + rule['unit_confirmed']
    if periods > 1:
        condition += f'，连续 {periods} 期'
    return {'rule': deepcopy(rule), 'state': signal.state if signal is not None else '数据缺失',
        'current_value': signal.current_value if signal is not None else None,
        'condition': condition, 'gap_note': missing, 'metric': metric}


def judgment_research_view(session, judgment, day):
    sides = {side: {'claim': '', 'evidence': [], 'falsifiers': []} for side in ('bull', 'bear')}
    counts = {'bound': 0, 'available': 0, 'thresholds': 0, 'falsifiers': 0, 'triggered': 0, 'pending': 0}
    result = {'sides': sides, 'metrics': {}, 'shared': [], 'counts': counts, 'pending_conditions': []}
    if judgment is None:
        return result
    payload = to_payload(judgment)
    wanted = _windows(payload)
    with session.no_autoflush:
        metrics = {identifier: _metric(session, identifier, day) for identifier in wanted}
        names = {identifier: metric['name'] for identifier, metric in metrics.items()}
        references = {side: set() for side in sides}
        marker = _transient_signals.set(True)
        try:
            for side, view in sides.items():
                original = getattr(payload.contradiction, side)
                view['claim'] = human_text(original.claim, names)
                for evidence in original.evidence:
                    metric = metrics.get(evidence.series_id)
                    view['evidence'].append({'desc': human_text(evidence.desc, names),
                        'series_id': evidence.series_id, 'metric': metric,
                        'note': human_text(evidence.note, names)})
                    if metric is not None:
                        references[side].add(evidence.series_id)
                for rule in payload.falsifiers:
                    if rule.side != side:
                        continue
                    value = _falsifier(session, judgment, rule.model_dump(mode='json'), day, metrics[rule.series_id])
                    value['rule']['desc'] = human_text(value['rule']['desc'], names)
                    view['falsifiers'].append(value)
                    references[side].add(rule.series_id)
        finally:
            _transient_signals.reset(marker)
    shared = []
    for identifier in metrics:
        if not all(identifier in references[side] for side in sides):
            continue
        shared.append({'metric': metrics[identifier],
            **{side + '_texts': [item['desc'] for item in sides[side]['evidence'] if item['series_id'] == identifier]
               for side in sides},
            **{side + '_falsifiers': [item for item in sides[side]['falsifiers'] if item['rule']['series_id'] == identifier]
               for side in sides}})
    pending = [{**item, 'text': human_text(item['text'], names)} for item in
        watch_items(payload.model_dump(mode='json')) if item['kind'] == 'observation']
    counts.update(bound=len(metrics), available=sum(metric['latest'] is not None for metric in metrics.values()),
        thresholds=len(payload.thresholds), falsifiers=len(payload.falsifiers),
        triggered=sum(item['state'] == '触发' for side in sides.values() for item in side['falsifiers']),
        pending=len(pending))
    result.update(metrics=metrics, shared=shared, pending_conditions=pending)
    return result
