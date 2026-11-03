"""A lightweight personal inbox; rendering it never evaluates market facts."""
from datetime import date, datetime
import json
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
    fields = ['format', 'review_date', 'date', 'period', 'workbench_id', 'direction']
    columns = [WorkbenchArtifact.id, WorkbenchArtifact.title, WorkbenchArtifact.status,
               WorkbenchArtifact.revision, WorkbenchArtifact.updated_at]
    columns += [func.json_extract(safe_body, '$.' + name).label(name) for name in fields]
    # Only whether a response exists and which kinds are cited; never the authored prose.
    columns += [func.length(func.trim(func.coalesce(func.json_extract(safe_body, '$.response_action'), ''))).label('response_length'),
                func.json_extract(safe_body, '$.citations').label('citations')]
    rows = list(session.execute(select(*columns).where(WorkbenchArtifact.actor == scope.identity.user_id,
        WorkbenchArtifact.module == module).order_by(WorkbenchArtifact.updated_at.desc(),
        WorkbenchArtifact.id.desc()).limit(RECORD_BUDGET + 1)).mappings())
    return [row for row in rows[:RECORD_BUDGET] if record_in_workbench(
        session, scope, 'artifact', row['id'], record=row)], len(rows) > RECORD_BUDGET


JOB_PHASES = {'judgments': {'queued': '等待分析', 'selecting': '核对资料范围', 'analyzing': '正在分析',
                             'validating': '核对分析结果'},
              'diagram': {'queued': '等待生成', 'reading_source': '读取原件', 'loading_catalog': '读取指标目录',
                          'selecting': '匹配指标', 'generating': '生成图结构', 'validating': '核对配置'}}


def _analysis_jobs(scope, modules):
    """Background 研判/产业图 analyses of this workbench that still need attention."""
    import sqlite3
    from tin.web.judgment_job_registry import ACTIVE
    from tin.web.judgment_jobs import list_jobs
    try:
        items = list_jobs(scope.identity, 20)['items']
    except (HTTPException, OSError, ValueError, sqlite3.Error):
        return []
    rows, finished = [], set()
    for job in items:  # newest first
        purpose = job.get('purpose')
        if (job.get('workbench_id') != scope.workbench_id or job.get('dismissed')
                or purpose not in JOB_PHASES or purpose not in modules):
            continue
        state, page = job.get('state'), '/sn/judgment' if purpose == 'judgments' else '/sn/diagram'
        if state not in ACTIVE:
            # Only the newest finished analysis of each kind needs attention; a
            # later result or failure replaces what an older attempt reported.
            if purpose in finished:
                continue
            finished.add(purpose)
        if state in ACTIVE:
            status, detail = '进行中', JOB_PHASES[purpose].get(job.get('phase'), '后台运行中')
        elif state == 'succeeded' and job.get('result_available'):
            status, detail = '待核对', ('分析已完成，核对后保存为研判草稿' if purpose == 'judgments'
                                         else '配置已生成，核对后导入产业图')
        elif state in {'failed', 'interrupted'}:
            status = '未完成'
            detail = (str(job.get('message') or '') or ('分析已中断' if state == 'interrupted' else '分析未完成'))[:200]
        else:
            continue
        stamp = job.get('updated_at') or job.get('created_at')
        try:
            stamp = _stamp(datetime.fromisoformat(stamp)) if isinstance(stamp, str) else ''
        except ValueError:
            stamp = ''
        rows.append({'title': job.get('title') or '后台分析', 'status': status,
            'detail': detail + (' · ' + stamp if stamp else ''), 'href': page + '?analysis_job=' + str(job['id'])})
    return rows


def _draft_progress(session, formal):
    """A saved draft that is not yet effective: what still needs the researcher."""
    from tin.web.report_watch import watch_items
    if formal is None or formal.status != '草稿':
        return None
    row = session.execute(select(Judgment.unstructured, Judgment.thresholds, Judgment.falsifiers)
        .where(Judgment.id == formal.id)).first()
    if row is None:
        return None
    pending = sum(item['kind'] == 'observation' for item in watch_items(
        {'unstructured': [note for note in row.unstructured or [] if isinstance(note, dict)]}))
    rules = len(row.thresholds or []) + len(row.falsifiers or [])
    parts = [f'{rules} 条可监测规则'] + ([f'{pending} 条待验证条件需核对指标'] if pending else [])
    return {'title': f'研判 v{formal.version} 尚未生效', 'status': '草稿',
            'detail': '草稿保存于 ' + formal.written_at.isoformat() + ' · ' + '，'.join(parts),
            'href': '/sn/judgment?judgment_id=' + str(formal.id)}


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
    progress, observations, report_links, monitored = [], [], [], 0
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
            review.append({'title': f'研判 v{formal.version}' + (f' · {formal.tone}' if getattr(formal, 'tone', None) else ''),
                'detail': f'{formal.status} · 复核日期 {formal.review_due.isoformat()}',
                'href': '/sn/judgment?judgment_id=' + str(formal.id), 'due_date': formal.review_due.isoformat()})
        draft = _draft_progress(session, formal)
        if draft is not None:
            progress.append(draft)
    progress[:0] = _analysis_jobs(scope, modules)
    if 'strategies' in modules:
        cards, truncated = _artifacts(session, scope, 'strategies')
        partial |= truncated
        for row in cards:
            if row['format'] != 'research-strategy-card.v1':
                continue
            due = _day(row['review_date'])
            href = '/sn/strategies?selected=research:' + row['id']
            if due is not None and due <= today:
                review.append({'title': row['title'], 'detail': '策略方案 · 复核日期 ' + due.isoformat(),
                    'href': href, 'due_date': due.isoformat()})
            if not row['response_length']:
                observations.append({'title': row['title'], 'detail': '观察方案 · 尚未填写应对措施', 'href': href})
            try:
                cited = json.loads(row['citations']) if isinstance(row['citations'], str) else []
            except ValueError:
                cited = []
            # Only cited judgment conditions can trigger; quoted indicators are context.
            monitored += isinstance(cited, list) and any(
                isinstance(item, dict) and item.get('kind') == 'judgment_condition' for item in cited)
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
        saved = {(row['period'], row['date']) for row in drafts if row['format'] == 'research-report-draft.v1'}
        day = today.isoformat()
        if ('daily', day) not in saved:
            report_links.append({'title': '今日日报 · ' + day, 'detail': '汇集当前研判与策略，可直接查看或编辑后导出',
                               'href': '/sn/reports?date=' + day + '&period=daily'})
        if today.weekday() >= 4 and ('weekly', day) not in saved:
            report_links.append({'title': '本周周报 · 截至 ' + day, 'detail': '汇集本周研判与策略变化，可编辑后导出',
                               'href': '/sn/reports?date=' + day + '&period=weekly'})
    alerts, truncated = _recorded_alerts(session, judgments, limit)
    partial |= truncated
    review.sort(key=lambda item: item['due_date'])
    counts = {'progress': len(progress), 'review': len(review), 'approvals': len(approvals) + len(observations),
              'reports': len(reports)}
    sections = ['progress'] if modules.intersection({'judgments', 'diagram'}) else []
    sections += ['review'] if modules.intersection({'judgments', 'strategies'}) else []
    sections += ['approvals'] if 'strategies' in modules else []
    sections += ['reports'] if 'reports' in modules else []
    shortcuts = [{'title': title, 'href': href} for module, title, href in [
        ('market', '市场总览', '/sn'), ('judgments', '研究判断', '/sn/judgment'),
        ('strategies', '策略', '/sn/strategies'), ('diagram', '产业图', '/sn/diagram'),
        ('reports', '日报与周报', '/sn/reports')] if module in modules]
    can_check = ('judgments' in modules and formal is not None and formal.status != '已归档') or monitored > 0
    return {'alerts': alerts, 'progress': progress[:limit], 'review': review[:limit], 'approvals': approvals[:limit],
        'observations': observations[:limit], 'reports': reports[:limit], 'report_links': report_links, 'counts': counts, 'sections': sections, 'shortcuts': shortcuts, 'partial': partial,
        'record_budget': RECORD_BUDGET, 'signal_budget': SIGNAL_BUDGET, 'monitored_strategies': monitored,
        'can_check': can_check}


@router.post('/check')
def check_alerts():
    scope = affairs_scope()
    from tin.web.affairs_check import check_current
    return check_current(scope)
