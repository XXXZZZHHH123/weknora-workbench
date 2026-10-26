"""Bounded daily/weekly facts and saved personal research, without generating opinions."""
from copy import deepcopy
from datetime import date, datetime, time, timedelta
from decimal import Decimal, localcontext
from math import isfinite

from fastapi import HTTPException
from sqlalchemy import func, select

from tin.config import SHANGHAI
from tin.models import DailyBrief, Indicator, Judgment, Observation, Strategy, StrategyEvent
from tin.ownership import scope_query

STATUS = {'draft': '草稿', 'watching': '等待入场', 'open': '跟踪中', 'closed': '已退出', 'cancelled': '已作废'}
EVENTS = {'created': '建立草稿', 'published': '确认发布', 'opened': '记录开仓',
          'closed': '记录平仓', 'cancelled': '作废', 'revised': '修订策略'}


def _local(value):
    return value.astimezone(SHANGHAI).isoformat(timespec='seconds') if isinstance(value, datetime) else value


def period_start(end, kind):
    if kind not in {'daily', 'weekly'}:
        raise HTTPException(400, '报告周期应为 daily 或 weekly')
    return end - timedelta(days=end.weekday()) if kind == 'weekly' else end


def _changes(session, identifiers, names, end, kind):
    if not identifiers:
        return []
    start = period_start(end, kind)
    cutoff = datetime.combine(end + timedelta(days=1), time.min, tzinfo=SHANGHAI)
    base = select(Observation.id, Observation.series_id, Observation.value, Observation.as_of,
        Observation.caliber_snapshot, Observation.source, Observation.revision,
        func.row_number().over(partition_by=[Observation.series_id, Observation.as_of],
            order_by=[Observation.revision.desc(), Observation.id.desc()]).label('revision_rank'))\
        .where(Observation.series_id.in_(identifiers), Observation.as_of < cutoff, Observation.status == '正常')
    if kind == 'weekly':
        base = base.where(Observation.as_of >= datetime.combine(start, time.min, tzinfo=SHANGHAI))
    revisions = base.subquery()
    valid = select(revisions).where(revisions.c.revision_rank == 1).subquery()
    ranked = select(valid, func.row_number().over(partition_by=valid.c.series_id,
        order_by=valid.c.as_of.asc()).label('first_rank'),
        func.row_number().over(partition_by=valid.c.series_id,
            order_by=valid.c.as_of.desc()).label('last_rank'),
        func.count().over(partition_by=valid.c.series_id).label('point_count')).subquery()
    # Only endpoints leave the database; no full history is materialized in Python.
    query = select(ranked).where((ranked.c.first_rank == 1) | (ranked.c.last_rank == 1)) if kind == 'weekly' else \
        select(ranked).where(ranked.c.last_rank <= 2)
    points = {}
    for row in session.execute(query).mappings():
        points.setdefault(row['series_id'], []).append(dict(row))
    output = []
    for identifier in identifiers:
        spec = names[identifier]
        rows = sorted(points.get(identifier, []), key=lambda row: row['as_of'])
        count = int(rows[0]['point_count']) if rows else 0
        first, last = (rows[0], rows[-1]) if rows else (None, None)
        comparable = len(rows) >= 2 and first['caliber_snapshot'] == last['caliber_snapshot']
        note = ('' if comparable else '期内仅一个有效观测，不能计算变化' if rows else '期内无有效观测，未沿用更早值作为期内变化')
        if len(rows) >= 2 and not comparable:
            note = '首末观测口径发生变化，未计算差值'
        change, pct = None, None
        if comparable:
            before, after = Decimal(str(first['value'])), Decimal(str(last['value']))
            with localcontext() as context:
                context.prec = max(34, max(before.adjusted(), after.adjusted())
                                   - min(before.as_tuple().exponent, after.as_tuple().exponent) + 4)
                delta = after - before
                candidate = float(delta)
                change = candidate if isfinite(candidate) else None
                if before != 0:
                    candidate = float(delta / abs(before) * 100)
                    pct = candidate if isfinite(candidate) else None
            if change is None:
                note = '变化超出可显示数值范围，原值保留在来源记录中'
        if comparable and first['value'] == 0:
            note = '起值为零，变化率不适用'
        if kind == 'daily' and last and last['as_of'].astimezone(SHANGHAI).date() != end:
            comparable, change, pct = False, None, None
            note = '所选日无新观测；以下为最近数据，未记作当日变化'
        def endpoint(row):
            return {'value': row['value'], 'as_of': _local(row['as_of']), 'source': row['source'],
                    'revision': row['revision']} if row else None
        output.append({'series_id': identifier, 'name': spec.name, 'unit': spec.unit,
            'first': endpoint(first), 'last': endpoint(last), 'observation_count': count,
            'change': change, 'change_pct': pct, 'comparable': comparable, 'note': note})
    return output


def build_period(session, variety, end: date, kind='daily', *, source=None, preview_rules=None):
    from tin.export.board import snapshot
    from tin.integration.workbench_scope import current_workbench
    from tin.web.research_strategy import report_projection
    start = period_start(end, kind)
    result = deepcopy(source if source is not None else snapshot(session, variety, end))
    identifiers = list(dict.fromkeys(row['series_id'] for row in result.get('observations') or []))
    if len(identifiers) > 256:
        raise HTTPException(413, '报告绑定超过256项，请先在市场总览和研究逻辑中缩小指标范围')
    specs = {row.series_id: row for row in session.scalars(select(Indicator).where(Indicator.series_id.in_(identifiers)))}
    identifiers = [identifier for identifier in identifiers if identifier in specs]
    for row in result.get('observations') or []:
        if row['series_id'] in specs:
            row['name'] = specs[row['series_id']].name
    result['period'] = {'kind': kind, 'start': start.isoformat(), 'end': end.isoformat()}
    result['changes'] = _changes(session, identifiers, specs, end, kind)
    begins = datetime.combine(start, time.min, tzinfo=SHANGHAI)
    finishes = datetime.combine(end + timedelta(days=1), time.min, tzinfo=SHANGHAI)
    briefs = session.scalars(scope_query(session, select(DailyBrief).where(DailyBrief.variety == variety,
        DailyBrief.trade_date >= start.isoformat(), DailyBrief.trade_date <= end.isoformat())
        .order_by(DailyBrief.trade_date.desc(), DailyBrief.id.desc()).limit(8), DailyBrief)).all()
    result['briefs'] = [{key: _local(getattr(row, key)) for key in
        ('id', 'trade_date', 'status', 'tone', 'tone_note', 'stance', 'summary', 'range_low',
         'range_high', 'range_series_id', 'claims', 'unverified', 'generated_at')} for row in briefs]
    strategies = session.scalars(scope_query(session, select(Strategy).where(Strategy.variety == variety)
        .order_by(Strategy.created_at.desc(), Strategy.id.desc()).limit(41), Strategy)).all()
    result['strategies'] = []
    for row in strategies[:40]:
        plan = row.plan if isinstance(row.plan, dict) else {}
        result['strategies'].append({'id': row.id, 'title': row.title, 'status': row.status,
            'status_label': STATUS.get(row.status, row.status), 'mode': row.mode,
            'mode_label': '模拟跟踪' if row.mode == 'simulation' else '实际成交记录',
            'thesis': plan.get('thesis') or '', 'invalidation': plan.get('invalidation') or '',
            'entry': plan.get('entry'), 'review_at': plan.get('review_at'), 'created_at': _local(row.created_at),
            'legs': [{'contract': leg.get('contract'), 'side': leg.get('side'), 'quote_series_id': leg.get('quote_series_id')}
                     for leg in plan.get('legs') or [] if isinstance(leg, dict)]})
    events = session.execute(scope_query(session, select(StrategyEvent, Strategy.title).join(Strategy)
        .where(Strategy.variety == variety, StrategyEvent.at >= begins, StrategyEvent.at < finishes)
        .order_by(StrategyEvent.at.desc(), StrategyEvent.id.desc()).limit(101), Strategy)).all()
    result['strategy_events'] = [{'id': row.id, 'title': title, 'strategy_id': row.strategy_id,
        'event': row.event_type, 'label': EVENTS.get(row.event_type, row.event_type), 'reason': row.reason,
        'at': _local(row.at)} for row, title in events[:100]]
    versions = session.scalars(scope_query(session, select(Judgment).where(Judgment.variety == variety,
        Judgment.created_at >= begins, Judgment.created_at < finishes)
        .order_by(Judgment.created_at.desc(), Judgment.id.desc()).limit(41), Judgment)).all()
    result['research_events'] = [{'version': row.version, 'status': row.status,
        'written_at': row.written_at.isoformat(), 'recorded_at': _local(row.created_at)} for row in versions[:40]]
    result['coverage'] = {'strategies_truncated': len(strategies) > 40, 'events_truncated': len(events) > 100,
        'research_events_truncated': len(versions) > 40, 'current_research_version': True,
        'latest_data_date': max((row.get('as_of') or '' for row in result.get('observations') or []), default=None)}
    cards = report_projection(session, current_workbench.get(), limit=40)
    result['research_cards'] = [{key: _local(value) for key, value in row.items()} for row in cards['items']]
    result['coverage'].update(research_cards_truncated=cards['truncated'],
        research_cards_legacy=cards.get('legacy_unformatted_count_in_page', 0))
    judgment = result.get('judgment') or {}
    live_rules = source is None if preview_rules is None else preview_rules
    if live_rules and judgment:
        from tin.compute.signals import evaluate_preview
        try:
            preview = evaluate_preview(session, variety, end)
        except ValueError as exc:
            raise HTTPException(409, str(exc)) from None
        expected = len(judgment.get('thresholds') or []) + len(judgment.get('falsifiers') or [])
        if len(preview) != expected or any(row.judgment_id != judgment.get('id') for row in preview):
            raise HTTPException(409, '研究逻辑在报告读取期间已变化，请刷新后重新试算')
        result['rule_preview'] = {'mode': 'readonly_preview', 'as_of': end.isoformat(),
            'judgment_id': judgment.get('id'), 'judgment_status': judgment.get('status'),
            'label': '当前草稿规则试算' if judgment.get('status') == '草稿' else '当前规则试算',
            'notice': '以当前保存规则和截至所选日可读数据试算，不是历史当时已知结论；数据时点见各条依据，不写入信号、复盘任务或成交。',
            'signals': [{key: deepcopy(getattr(row, key)) for key in
                ('rule_id','rule_type','trade_date','state','current_value','evidence','gap_note')} for row in preview]}
    pending = []
    for item in judgment.get('falsifiers') or []:
        pending.append({'kind': 'falsifier', 'text': item.get('desc') or item.get('id') or '已保存证伪条件',
                        'series_id': item.get('series_id')})
    for item in judgment.get('thresholds') or []:
        pending.append({'kind': 'threshold', 'text': item.get('name') or item.get('id') or '已保存区间',
                        'series_id': item.get('series_id')})
    for item in judgment.get('unstructured') or []:
        pending.append({'kind': 'unconfirmed', 'text': str(item.get('text') or '') + ' · ' + str(item.get('reason') or '')})
    for row in result['strategies']:
        if row['status'] in {'watching', 'open', 'draft'}:
            pending.append({'kind': 'strategy', 'text': row['title'] + '：' + (row['invalidation'] or '复核入场、退出与失效条件')})
    for row in result['research_cards']:
        pending.append({'kind': 'research_card', 'text': row['title'] + '：' + row['trigger']
                        + ' · 复核日期 ' + row['review_date']})
    result['next_review'] = pending
    return result
