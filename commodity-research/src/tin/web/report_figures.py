"""Small report-authorized charts from saved real-ID bindings, without writes."""
from datetime import date
import math

from fastapi import APIRouter, HTTPException
from sqlalchemy import func, select

from tin.compute.engine import day_end
from tin.config import SHANGHAI
from tin.integration.page_facts import page_read_scope, projected_page_session
from tin.integration.workbench_scope import artifact_session
from tin.models import Indicator, Judgment, Observation, Researcher, Strategy
from tin.web.market_history import _chart as chart_view

router = APIRouter(prefix='/api/sn/reports')


def points_for_period(period):
    if period not in {'daily', 'weekly'}:
        raise HTTPException(400, '报告周期应为daily或weekly')
    return 120 if period == 'weekly' else 90


def validate_selection(identifiers, allowed):
    if (not isinstance(identifiers, list) or len(identifiers) > 6
            or any(not isinstance(value, str) or not value or len(value) > 128 for value in identifiers)
            or len(set(identifiers)) != len(identifiers)):
        raise HTTPException(422, '报告最多选择6条不同的真实绑定指标')
    if set(identifiers) - set(allowed):
        raise HTTPException(403, '选图超出本台已保存的研判、策略或核心指标绑定')
    return list(identifiers)


def saved_bound_series(session, scope):
    """Metadata only: never create a researcher or enumerate a fact catalogue."""
    from tin.web.restricted_views import core_config
    from tin.judgments.service import _refs, to_payload
    identifiers = list(core_config(session))
    rid = session.scalar(select(Researcher.id).where(Researcher.weknora_user_id == scope.identity.user_id))
    if rid is None:
        return identifiers
    judgments = session.scalars(select(Judgment).where(Judgment.researcher_id == rid)
        .order_by(Judgment.created_at.desc(), Judgment.id.desc()).limit(40)).all()
    strategies = session.scalars(select(Strategy).where(Strategy.researcher_id == rid)
        .order_by(Strategy.created_at.desc(), Strategy.id.desc()).limit(40)).all()
    from tin.web.research_references import record_in_workbench
    # In legacy tenant storage a positive stamp is required. Isolated storage
    # also honors any existing foreign stamp rather than treating it as local.
    judgments = [row for row in judgments if record_in_workbench(session, scope, 'judgment', row.id, row)]
    strategies = [row for row in strategies if record_in_workbench(session, scope, 'plan', row.id, row)]
    if judgments:
        variety = judgments[0].variety
        same_variety = [row for row in judgments if row.variety == variety]
        selected = max(same_variety, key=lambda row: (row.status == '生效', row.version))
        identifiers.extend(identifier for _, _, identifier in _refs(to_payload(selected)))
        strategies = [row for row in strategies if row.variety == variety]
    for row in strategies:
        plan = row.plan if isinstance(row.plan, dict) else {}
        identifiers.extend(item['quote_series_id'] for item in plan.get('legs') or []
                           if isinstance(item, dict) and isinstance(item.get('quote_series_id'), str))
        identifiers.extend(item['series_id'] for item in plan.get('evidence') or []
                           if isinstance(item, dict) and isinstance(item.get('series_id'), str))
    return list(dict.fromkeys(identifiers))


def read_figures(session, identifiers, through, period):
    limit = points_for_period(period)
    if len(identifiers) > 6 or len(set(identifiers)) != len(identifiers):
        raise HTTPException(422, '报告图表超过6条或重复')
    if type(through) is not date:
        raise HTTPException(400, '图表需要明确的截止日期')
    specs = {row.series_id: row for row in session.scalars(select(Indicator)
        .where(Indicator.series_id.in_(identifiers)))} if identifiers else {}
    if set(specs) != set(identifiers):
        raise HTTPException(409, '所选指标的来源已改变，请重新确认报告选图')
    figures = []
    for identifier in identifiers:
        ranked = select(Observation.as_of, Observation.value, Observation.status,
            Observation.caliber_snapshot,
            func.row_number().over(partition_by=Observation.as_of,
                order_by=(Observation.revision.desc(), Observation.id.desc())).label('rank')).where(
            Observation.series_id == identifier, Observation.as_of < day_end(through)).subquery()
        rows = session.execute(select(ranked).where(ranked.c.rank == 1)
            .order_by(ranked.c.as_of.desc()).limit(limit)).mappings().all()
        points, seen, invalid, calibers = [], set(), 0, []
        for row in reversed(rows):
            stamp = row['as_of'].astimezone(SHANGHAI).date().isoformat()
            if stamp in seen:
                raise HTTPException(409, '所选序列同日有多个时点，请先明确日期口径')
            seen.add(stamp)
            if row['status'] != '正常':
                invalid += 1
                continue
            if type(row['value']) not in {int, float} or not math.isfinite(row['value']):
                raise HTTPException(503, '图表含无效数值，未返回部分结果')
            points.append({'date': stamp, 'value': row['value']})
            calibers.append(row['caliber_snapshot'])
        spec = specs[identifier]
        chart = chart_view(identifier, spec.name, spec.unit, points)
        chart.update(source=spec.source, frequency=spec.frequency, invalid_points=invalid,
            caliber_changed=bool(calibers and any(value != calibers[0] for value in calibers[1:])),
            notice='存在无效观测，未用旧修订复活或补零' if invalid else '')
        # A single polyline across differing definitions is misleading. Keep the
        # actual values available for audit but require definition confirmation.
        if chart['caliber_changed']:
            chart['path'] = ''
            chart['notice'] = '图中观测口径变化，暂不连线；请核对原始记录'
        figures.append(chart)
    return {'series': figures, 'through': through.isoformat(), 'points_per_series': limit,
            'point_budget': 6 * limit, 'source_version': session.info.get('knowledge_facts_version')}


@router.get('/figures')
def report_figures(date: str, period: str = 'daily', revision: int | None = None):
    from tin.web.report_drafts import require_scope, validate_period, load_report, bound_series
    scope = require_scope()
    if scope.scope_version != 2:
        raise HTTPException(409, '当前来源授权不支持有界报告图表，请重新打开研究台更新授权')
    through = validate_period(date, period)
    with artifact_session(scope) as results:
        engine = results.get_bind()
        saved = load_report(results, scope, date, period, strict=True)
        if revision is not None and (saved is None or saved['revision'] != revision):
            raise HTTPException(409, '报告选图已有新版本，请重新打开')
        snapshots = saved['draft']['reference_snapshots'] if saved else []
        allowed = bound_series(results, scope, snapshots)
        chosen = [row['series_id'] for row in saved['draft']['figures']] if saved else allowed[:6]
        validate_selection(chosen, allowed)
        captions = {row['series_id']: row['caption'] for row in saved['draft']['figures']} if saved else {}
    if not chosen:
        return {'series': [], 'through': date, 'points_per_series': points_for_period(period),
                'point_budget': 6 * points_for_period(period), 'draft_revision': saved['revision'] if saved else None}
    with page_read_scope('reports', series_ids=chosen, day=through, points_per_series=points_for_period(period)):
        # The already verified results engine avoids the ordinary session
        # bootstrap's persistent isolated_scope INSERT/commit on a chart read.
        with projected_page_session(engine, scope) as facts:
            payload = read_figures(facts, chosen, through, period)
    for row in payload['series']:
        row['caption'] = captions.get(row['series_id'], '')
    payload['draft_revision'] = saved['revision'] if saved else None
    return payload
