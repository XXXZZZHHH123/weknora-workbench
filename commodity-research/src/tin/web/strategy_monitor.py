"""Read-only status of the conditions and indicators a strategy card cites.

The card text itself is never parsed into rules. Only explicit citations are
checked: a cited judgment condition is evaluated with the same rule engine as
the judgment page (without writing signals), and a cited indicator shows its
latest observations. Nothing here places, changes or confirms a trade.
"""
from datetime import date, datetime

from fastapi import HTTPException

from tin.config import SHANGHAI
from tin.integration.page_facts import page_read_scope
from tin.integration import workbench_scope

MAX_JUDGMENTS = 8
MAX_INDICATORS = 32
CHART_POINTS = 24
FIELD_LABELS = {'trigger': '适用 / 触发', 'response_action': '应对措施', 'invalidation': '暂停 / 撤回',
                'thesis': '适用范围与目标', 'watch_for': '执行前核对', 'original_text': '原始研究文字'}
STATE = {'已触发': '触发', '在区间内': '触发', '未触发': '未触发', '数据缺失': '数据缺失'}


def _cutoff(day):
    if day is None:
        return datetime.now(SHANGHAI).date()
    if type(day) is date:
        return day
    try:
        value = date.fromisoformat(day)
        if value.isoformat() != day:
            raise ValueError
        return value
    except (TypeError, ValueError):
        raise HTTPException(400, '监测日期应为YYYY-MM-DD') from None


def _staleness(metric, cutoff):
    """Days since the latest point, flagged only when beyond the series' own usual interval."""
    try:
        latest = date.fromisoformat(str(metric.get('date'))[:10])
        stamps = sorted({date.fromisoformat(str(point['date'])[:10]) for point in metric.get('points') or []})
    except (TypeError, ValueError, KeyError):
        return None
    age = (cutoff - latest).days
    gaps = sorted((right - left).days for left, right in zip(stamps, stamps[1:]) if (right - left).days > 0)
    usual = gaps[len(gaps) // 2] if gaps else None
    stale = age > max(10, 2.5 * usual) if usual else age > 60
    return {'age_days': age, 'usual_interval_days': usual, 'stale': stale}


def _chart(metric, cutoff=None):
    if not isinstance(metric, dict):
        return None
    result = {key: metric.get(key) for key in ('series_id', 'name', 'unit', 'latest', 'previous', 'date',
                                               'change', 'change_pct', 'points', 'segments', 'point_count')}
    result['freshness'] = _staleness(metric, cutoff) if cutoff is not None and metric.get('date') else None
    return result


def _judgment_states(scope, judgment_id, wanted, cutoff):
    """Evaluate only the requested rules of one saved judgment version."""
    from tin.compute.signals import _falsifier_signal, _transient_signals
    from tin.export.board import threshold_radar
    from tin.judgments.service import current
    from tin.web.judgment_panels import selected_judgment
    from tin.web.judgment_research import _metric
    with page_read_scope('judgments', judgment_id=judgment_id, day=cutoff.isoformat()):
        with workbench_scope.workbench_session(scope) as session:
            judgment = selected_judgment(session, judgment_id, scope=scope)
            latest = current(session, judgment.variety, scope=scope)
            radar = {row['id']: row for row in threshold_radar(session, judgment.variety, cutoff, judgment=judgment)}
            marker = _transient_signals.set(True)
            states = {}
            try:
                for rule in judgment.thresholds:
                    if ('threshold', rule['id']) not in wanted:
                        continue
                    row = radar.get(rule['id']) or {}
                    states[('threshold', rule['id'])] = {
                        'state': STATE.get(row.get('state'), '数据缺失'), 'value': row.get('value'),
                        'as_of': (row.get('as_of') or '')[:10] or None, 'unit': row.get('unit') or '',
                        'condition': row.get('condition') or '', 'distance': row.get('distance') or '',
                        'series_id': rule['series_id'], 'name': rule.get('name') or '',
                        'chart': _chart(_metric(session, rule['series_id'], cutoff, CHART_POINTS), cutoff)}
                for rule in judgment.falsifiers:
                    if ('falsifier', rule['id']) not in wanted:
                        continue
                    signal = _falsifier_signal(session, judgment, rule, cutoff)
                    dates = [str(item.get('as_of'))[:10] for item in signal.evidence or [] if item.get('as_of')]
                    states[('falsifier', rule['id'])] = {
                        'state': signal.state, 'value': signal.current_value, 'as_of': dates[-1] if dates else None,
                        'unit': rule.get('unit_confirmed') or '', 'condition': rule.get('expression') or '',
                        'distance': signal.gap_note or '', 'series_id': rule['series_id'], 'name': rule.get('desc') or '',
                        'chart': _chart(_metric(session, rule['series_id'], cutoff, CHART_POINTS), cutoff)}
            finally:
                _transient_signals.reset(marker)
            version = {'id': str(judgment.id), 'revision': judgment.version, 'status': judgment.status,
                       'tone': judgment.tone or '', 'current_revision': latest.version if latest else None,
                       'is_current': latest is not None and latest.id == judgment.id}
    return version, states


def _indicator_charts(scope, identifiers, cutoff):
    from tin.web.judgment_research import _metric
    with page_read_scope('strategies', series_ids=list(identifiers), day=cutoff.isoformat(),
                         points_per_series=CHART_POINTS):
        with workbench_scope.workbench_session(scope) as session:
            return {identifier: _chart(_metric(session, identifier, cutoff, CHART_POINTS), cutoff) for identifier in identifiers}


def monitor_cards(scope, cards, *, day=None, include_indicators=True):
    """Status for several cards at once; each cited judgment is read only once."""
    from tin.web.research_display import condition_text, human_text
    cutoff = _cutoff(day)
    wanted, indicators = {}, []
    for card in cards:
        for citation in (card.get('card') or {}).get('citations') or []:
            if citation.get('kind') == 'judgment_condition':
                wanted.setdefault(citation['id'], set()).add((citation['rule_kind'], citation['rule_id']))
            elif include_indicators and citation.get('kind') == 'indicator' and citation['series_id'] not in indicators:
                indicators.append(citation['series_id'])
    if len(wanted) > MAX_JUDGMENTS or len(indicators) > MAX_INDICATORS:
        raise HTTPException(413, f'一次最多监测{MAX_JUDGMENTS}个研判版本和{MAX_INDICATORS}个指标，请分开查看')
    judgments, errors = {}, {}
    for identifier, rules in wanted.items():
        try:
            if not identifier.isdecimal():
                raise HTTPException(404, '所引研判不存在')
            judgments[identifier] = _judgment_states(scope, int(identifier), rules, cutoff)
        except HTTPException as exc:
            errors[identifier] = exc.detail if isinstance(exc.detail, str) else '所引研判暂不可读'
    charts, chart_error = {}, None
    if indicators:
        try:
            charts = _indicator_charts(scope, indicators, cutoff)
        except HTTPException as exc:
            chart_error = exc.detail if isinstance(exc.detail, str) else '所引指标暂不可读'
    results = {}
    for card in cards:
        body, conditions, series, library_charts = card.get('card') or {}, [], [], []
        for citation in body.get('citations') or []:
            snapshot = citation.get('snapshot') or {}
            field = FIELD_LABELS.get(citation.get('field'), '')
            if citation.get('kind') == 'judgment_condition':
                version, states = judgments.get(citation['id'], (None, {}))
                state = states.get((citation['rule_kind'], citation['rule_id']))
                row = {'field': citation.get('field'), 'field_label': field, 'rule_kind': citation['rule_kind'],
                       'title': human_text(snapshot.get('title')) or ('阈值条件' if citation['rule_kind'] == 'threshold' else '证伪条件'),
                       'judgment_id': citation['id'], 'revision': citation['revision']}
                if version is None or state is None:
                    row.update(state='无法核对', note=errors.get(citation['id']) or '所引条件已不在该研判版本中，请重新引用')
                else:
                    condition = state['condition']
                    if citation['rule_kind'] == 'falsifier':
                        condition = condition_text(condition)
                    row.update(state=state['state'], value=state['value'], unit=state['unit'], as_of=state['as_of'],
                               title=human_text(state['name']) or row['title'], condition=condition,
                               note=human_text(state['distance']), chart=state['chart'],
                               judgment={**version, 'cited_revision': citation['revision']},
                               outdated=version['current_revision'] is not None and not version['is_current'])
                conditions.append(row)
            elif include_indicators and citation.get('kind') == 'indicator':
                chart = charts.get(citation['series_id'])
                series.append({'field': citation.get('field'), 'field_label': field, 'series_id': citation['series_id'],
                               'title': human_text(snapshot.get('title')) or (chart or {}).get('name') or '已引用指标',
                               'chart': chart, 'note': chart_error if chart is None else ''})
            elif include_indicators and citation.get('kind') == 'chart' and not scope.restricted:
                from tin.web.chart_references import render_chart_reference
                reference = {key: value for key, value in citation.items() if key not in {'field', 'snapshot'}}
                monitoring_reference = {**reference, 'as_of': cutoff.isoformat()}
                try:
                    rendered = render_chart_reference(scope, monitoring_reference)
                except HTTPException as exc:
                    rendered = {'reference': monitoring_reference, 'status': 'unavailable',
                        'notice': exc.detail if isinstance(exc.detail, str) else '图表引用暂不可读', 'payload': None}
                library_charts.append({**rendered, 'frozen_as_of': reference.get('as_of'),
                    'monitor_as_of': cutoff.isoformat(), 'field': citation.get('field'), 'field_label': field})
        triggered = [row for row in conditions if row['state'] == '触发']
        results[card['id']] = {'card_id': card['id'], 'as_of': cutoff.isoformat(), 'conditions': conditions,
            'indicators': series, 'counts': {'conditions': len(conditions), 'triggered': len(triggered),
                'missing': sum(row['state'] in {'数据缺失', '无法核对'} for row in conditions), 'indicators': len(series)},
            'notice': f'按截至 {cutoff.isoformat()} 的可读数据试算所引条件；不写入信号，也不执行或改变交易。'}
        if library_charts:
            results[card['id']]['charts'] = library_charts
        if scope.restricted and any(row.get('kind') == 'chart' for row in body.get('citations') or []):
            results[card['id']]['chart_notice'] = '图表引用暂不对受限视角开放'
    return results


def card_monitor(scope, card, *, day=None):
    return monitor_cards(scope, [card], day=day)[card['id']]
