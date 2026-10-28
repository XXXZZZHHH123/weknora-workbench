"""A lightweight personal inbox; opening it never evaluates market facts."""
from datetime import date, datetime
from types import SimpleNamespace

from fastapi import APIRouter, HTTPException
from sqlalchemy import case, func, select

from tin.config import SHANGHAI
from tin.integration.workbench_scope import current_workbench
from tin.models import Judgment, Researcher, Signal, Strategy
from tin.models.workbench import WorkbenchArtifact
from tin.web.research_references import record_in_workbench

router = APIRouter(prefix='/api/sn/affairs')
RECORD_BUDGET = 100
SIGNAL_BUDGET = 200


def affairs_scope(scope=None):
    scope = scope or current_workbench.get()
    if scope is None or not scope.authorization:
        raise HTTPException(409, '请先进入已授权的研究工作台')
    if scope.restricted or 'home' not in scope.view_modules:
        raise HTTPException(403, '当前授权不开放个人事务中心')
    return scope


def _day(value):
    if type(value) is date:
        return value
    try:
        result = date.fromisoformat(value)
        return result if result.isoformat() == value else None
    except (ValueError, TypeError):
        return None


def _stamp(value):
    return value.astimezone(SHANGHAI).strftime('%Y-%m-%d %H:%M') if isinstance(value, datetime) else ''


def _owned_records(session, scope, model, columns, owner_id, kind):
    if owner_id is None:
        return [], False
    rows = list(session.execute(select(*columns).where(model.researcher_id == owner_id)
        .order_by(model.created_at.desc(), model.id.desc()).limit(RECORD_BUDGET + 1)).mappings())
    return [SimpleNamespace(**row) for row in rows[:RECORD_BUDGET] if record_in_workbench(
        session, scope, kind, str(row['id']), record=row)], len(rows) > RECORD_BUDGET


def _artifacts(session, scope, module):
    # Workbench result stores are SQLite. Project only small JSON metadata in
    # the DB; never transfer report prose, snapshots or strategy bodies home.
    safe_body = case((func.json_valid(WorkbenchArtifact.body) == 1, WorkbenchArtifact.body), else_='{}')
    fields = ['format', 'review_date', 'date', 'period', 'workbench_id']
    columns = [WorkbenchArtifact.id, WorkbenchArtifact.title, WorkbenchArtifact.status,
               WorkbenchArtifact.revision, WorkbenchArtifact.updated_at]
    columns += [func.json_extract(safe_body, '$.' + name).label(name) for name in fields]
    rows = list(session.execute(select(*columns).where(WorkbenchArtifact.actor == scope.identity.user_id,
        WorkbenchArtifact.module == module).order_by(WorkbenchArtifact.updated_at.desc(),
        WorkbenchArtifact.id.desc()).limit(RECORD_BUDGET + 1)).mappings())
    return [row for row in rows[:RECORD_BUDGET] if record_in_workbench(
        session, scope, 'artifact', row['id'], record=row)], len(rows) > RECORD_BUDGET


def _formal_judgment(rows):
    if not rows:
        return None
    return min((row for row in rows if row.variety == rows[0].variety),
        key=lambda row: (0 if row.status == '生效' else 1, -row.version, -row.id))


def _recorded_alerts(session, judgments, limit):
    by_id = {row.id: row for row in judgments if row.status != '已归档'}
    if not by_id:
        return [], False
    rows = list(session.execute(select(Signal.judgment_id, Signal.rule_id, Signal.rule_type,
        Signal.trade_date, Signal.state, Signal.gap_note, Signal.generated_at).where(
            Signal.judgment_id.in_(by_id)).order_by(Signal.trade_date.desc(), Signal.generated_at.desc(),
                Signal.id.desc()).limit(SIGNAL_BUDGET + 1)).mappings())
    seen, candidates = set(), []
    for row in rows[:SIGNAL_BUDGET]:
        key = (row['judgment_id'], row['rule_type'], row['rule_id'])
        if key in seen:
            continue
        seen.add(key)
        if row['state'] in {'触发', '数据缺失'} and _day(row['trade_date']) is not None:
            candidates.append(row)
    names = {}
    wanted = {row['judgment_id'] for row in candidates[:limit]}
    if wanted:
        for identifier, thresholds, falsifiers in session.execute(select(Judgment.id,
                Judgment.thresholds, Judgment.falsifiers).where(Judgment.id.in_(wanted))):
            for rule in thresholds or []:
                if isinstance(rule, dict): names[(identifier, 'threshold', rule.get('id'))] = rule.get('name')
            for rule in falsifiers or []:
                if isinstance(rule, dict): names[(identifier, 'falsifier', rule.get('id'))] = rule.get('desc')
    alerts = []
    for row in candidates[:limit]:
        judgment = by_id[row['judgment_id']]
        name = names.get((judgment.id, row['rule_type'], row['rule_id'])) or '规则 ' + str(row['rule_id'])
        alerts.append({'kind': '数据缺失' if row['state'] == '数据缺失' else
            {'threshold': '阈值触发', 'falsifier': '证伪触发'}.get(row['rule_type'], '已记录触发'),
            'title': str(name)[:200], 'detail': (str(row['gap_note'] or '')[:300] or
                f'研判 v{judgment.version} 的已保存运算记录'),
            'href': '/sn/judgment?judgment_id=' + str(judgment.id),
            'trade_date': row['trade_date'], 'recorded_at': _stamp(row['generated_at'])})
    return alerts, len(rows) > SIGNAL_BUDGET or len(candidates) > limit


def build_affairs(session, scope, *, today=None, limit=8):
    scope = affairs_scope(scope)
    if (session.info.get('workbench_id') != scope.workbench_id
            or str(session.info.get('weknora_user_id') or '') != scope.identity.user_id):
        raise HTTPException(403, '事务中心的工作台或账号不匹配')
    if type(limit) is not int or not 1 <= limit <= 30:
        raise HTTPException(422, '事务列表条数无效')
    today = today or datetime.now(SHANGHAI).date()
    owner_id = session.scalar(select(Researcher.id).where(Researcher.weknora_user_id == scope.identity.user_id))
    review, approvals, reports, judgments, formal = [], [], [], [], None
    partial = False
    modules = set(scope.view_modules)
    if 'judgments' in modules:
        from tin.judgments.service import authorized_judgments
        authorized = authorized_judgments(session, scope)
        judgments = authorized[:RECORD_BUDGET]
        partial |= len(authorized) > RECORD_BUDGET
        formal = _formal_judgment(authorized)
        if formal is not None and formal not in judgments:
            judgments.append(formal)
        if formal is not None and formal.status != '已归档' and formal.review_due <= today:
            review.append({'title': f'{formal.variety} 研判 · v{formal.version}',
                'detail': f'{formal.status} · 复核日期 {formal.review_due.isoformat()}',
                'href': '/sn/judgment?judgment_id=' + str(formal.id), 'due_date': formal.review_due.isoformat()})
    if 'strategies' in modules:
        cards, truncated = _artifacts(session, scope, 'strategies')
        partial |= truncated
        for row in cards:
            due = _day(row['review_date'])
            if row['format'] == 'research-strategy-card.v1' and due is not None and due <= today:
                review.append({'title': row['title'], 'detail': '研究策略 · 复核日期 ' + due.isoformat(),
                    'href': '/sn/strategies?selected=research:' + row['id'], 'due_date': due.isoformat()})
        plans, truncated = _owned_records(session, scope, Strategy,
            [Strategy.id, Strategy.researcher_id, Strategy.title, Strategy.status, Strategy.version], owner_id, 'plan')
        partial |= truncated
        approvals = [{'title': row.title, 'detail': f'交易计划草稿 · v{row.version} · 尚未发布',
            'href': '/sn/strategies/' + str(row.id)} for row in plans if row.status == 'draft']
    if 'reports' in modules:
        drafts, truncated = _artifacts(session, scope, 'briefs')
        partial |= truncated
        from tin.web.report_drafts import report_id
        for row in drafts:
            if (row['format'] == 'research-report-draft.v1' and row['workbench_id'] == scope.workbench_id
                    and _day(row['date']) is not None and row['period'] in {'daily', 'weekly'} and row['status'] == 'draft'
                    and row['id'] == report_id(scope, row['date'], row['period'])):
                reports.append({'title': row['title'],
                    'detail': ('周报' if row['period'] == 'weekly' else '日报') + f" · {row['date']} · v{row['revision']}",
                    'href': '/sn/reports?date=' + row['date'] + '&period=' + row['period']})
    alerts, truncated = _recorded_alerts(session, judgments, limit)
    partial |= truncated
    review.sort(key=lambda item: item['due_date'])
    counts = {'review': len(review), 'approvals': len(approvals), 'reports': len(reports)}
    sections = (['review'] if modules.intersection({'judgments', 'strategies'}) else [])
    sections += ['approvals'] if 'strategies' in modules else []
    sections += ['reports'] if 'reports' in modules else []
    shortcuts = [{'title': title, 'href': href} for module, title, href in [
        ('market', '市场总览', '/sn'), ('judgments', '研究判断', '/sn/judgment'),
        ('strategies', '策略', '/sn/strategies'), ('reports', '日报与周报', '/sn/reports')] if module in modules]
    return {'alerts': alerts, 'review': review[:limit], 'approvals': approvals[:limit], 'reports': reports[:limit],
        'counts': counts, 'sections': sections, 'shortcuts': shortcuts, 'partial': partial,
        'record_budget': RECORD_BUDGET, 'signal_budget': SIGNAL_BUDGET,
        'can_check': formal is not None and formal.status != '已归档'}


@router.post('/check')
def check_alerts():
    scope = affairs_scope()
    from tin.web.affairs_check import check_current
    return check_current(scope)
