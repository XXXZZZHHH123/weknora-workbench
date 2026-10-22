"""The original home sections projected from shared owner records and bounded values."""
from __future__ import annotations

from datetime import date, datetime

from sqlalchemy import select

from tin.config import SHANGHAI, settings
from tin.export.board import Card, threshold_radar_values
from tin.integration.workbench_scope import artifact_session
from tin.models import DailyBrief, Judgment, PlaybookItem, Researcher, ReviewTask


def _text(value):
    return value if isinstance(value, str) else ''


def _claim(item, values):
    if not isinstance(item, dict):
        return None
    result = {'text': _text(item.get('text') or item.get('claim')),
              'side': _text(item.get('side')), 'evidence': []}
    sources = item.get('evidence') if isinstance(item.get('evidence'), list) else [item]
    for source in sources[:16]:
        if not isinstance(source, dict):
            continue
        identifier = source.get('series_id')
        if not isinstance(identifier, str) or identifier not in values:
            continue
        value = values[identifier]
        result['evidence'].append({'name': value['name'], 'value': value['value'],
                                   'unit': value['unit'], 'as_of': value['date']})
    return result


def _brief(session, scope, day, values):
    from tin.web.restricted_views import shared_owner_query
    statement = shared_owner_query(scope, select(DailyBrief).where(
        DailyBrief.variety == settings.variety, DailyBrief.trade_date <= day), DailyBrief)
    row = session.scalar(statement.order_by(DailyBrief.trade_date.desc(), DailyBrief.id.desc()).limit(1))
    if row is None:
        return None
    result = {key: getattr(row, key) for key in (
        'trade_date', 'status', 'tone', 'tone_note', 'stance', 'summary', 'range_low', 'range_high')}
    result['is_draft'] = row.status == '草稿'
    result['generated_at'] = row.generated_at.astimezone(SHANGHAI).strftime('%Y-%m-%d %H:%M') if row.generated_at else ''
    result['claims'] = [claim for item in (row.claims or [])[:100] if (claim := _claim(item, values))]
    result['unverified'] = [{
        'claim': _text(item.get('claim') or item.get('text')),
        'reasons': [reason for reason in (item.get('reasons') or [])[:16] if isinstance(reason, str)],
    } for item in (row.unverified or [])[:50] if isinstance(item, dict)]
    identifiers = [identifier for identifier in (row.playbook_item_ids or [])[:100] if type(identifier) is int]
    result['playbook'] = []
    if identifiers:
        statement = shared_owner_query(scope, select(PlaybookItem).where(
            PlaybookItem.id.in_(identifiers), PlaybookItem.variety == settings.variety), PlaybookItem)
        result['playbook'] = [{'text': item.text} for item in session.scalars(statement.limit(100))]
    return result


def _todos(session, scope):
    from tin.web.restricted_views import shared_owner_query
    statement = shared_owner_query(scope, select(ReviewTask, Judgment.version, Judgment.review_due)
        .join(Judgment, ReviewTask.judgment_id == Judgment.id)
        .where(Judgment.variety == settings.variety, ReviewTask.state == '待处理'), Judgment)
    rows = session.execute(statement.order_by(ReviewTask.created_at.desc(), ReviewTask.id.desc()).limit(100))
    return [{'kind': task.trigger_type, 'title': f'判断 v{version} · {task.trigger_type}',
             'detail': f'复盘期 {due.isoformat()}' if due else '',
             'assignee': task.assignee,
             'created_at': task.created_at.astimezone(SHANGHAI).strftime('%Y-%m-%d %H:%M') if task.created_at else ''}
            for task, version, due in rows]


def build_home_context(session, scope, identifiers, values):
    """Only share records that belong to this verified workbench's actual owner.

    ``values`` is already selected by server bindings and checked against live
    page grants. No local facts, captured evidence values or full catalog are read.
    """
    from tin.web.restricted_views import shared_artifacts, shared_owner_id, shared_owner_query
    values = {identifier: values[identifier] for identifier in identifiers if identifier in values}
    today = datetime.now(SHANGHAI).date().isoformat()
    observed = []
    for item in values.values():
        try:
            value = date.fromisoformat(item.get('date') or '')
        except (ValueError, TypeError):
            continue
        if value.isoformat() <= today:
            observed.append(value.isoformat())
    day = max(observed, default=today)
    legacy = bool(session.info.get('source_storage_legacy'))
    owner = shared_owner_id(scope)
    researcher = session.scalar(select(Researcher).where(Researcher.weknora_user_id == owner)) if owner and not legacy else None
    result = {'d': max(observed, default=None), 'owner_name': researcher.display_name if researcher else '',
              'owner_unavailable': researcher is None, 'legacy_unassigned': legacy,
              'todos': [], 'brief': None, 'brief_entries': [], 'radar': [], 'gaps': [],
              'changes': {'core': [], 'macro': []}, 'cards': [],
              'view_modules': scope.view_modules, 'brief_gap': ''}
    if legacy:
        result['brief_gap'] = '历史研究记录尚未归属当前工作台，请由主人完成归属确认后共享。'
    elif researcher is None:
        result['brief_gap'] = '主人归属信息尚未同步，暂不展示个人研究记录；请由工作台主人核验归属。'
    else:
        result['todos'] = _todos(session, scope)
        result['brief'] = _brief(session, scope, day, values)
        if result['brief'] is None:
            result['brief_entries'] = shared_artifacts(session, scope, 'reports')[:5]
            result['brief_gap'] = '主人尚未保存当前日期可展示的简报。'
        statement = shared_owner_query(scope, select(Judgment).where(
            Judgment.variety == settings.variety, Judgment.status == '生效'), Judgment)
        judgment = session.scalar(statement.order_by(Judgment.version.desc()).limit(1))
        result['radar'] = threshold_radar_values(judgment, values)
    for identifier in identifiers:
        item = values.get(identifier)
        if item is None:
            result['gaps'].append({'name': identifier, 'note': '当前共享绑定尚未返回可用观测。'})
            continue
        card = Card(key=identifier, series_id=identifier, label=item['name'], unit=item['unit'],
                    status='ok' if item['value'] is not None else 'missing', value=item['value'],
                    as_of=item['date'], change=item['change'], change_pct=item['change_pct'], badge='已授权')
        result['cards'].append(card)
        if item['value'] is None:
            result['gaps'].append({'name': item['name'], 'note': '当前绑定没有有效数值，不以零代替。'})
        if item['change']:
            result['changes']['core'].append({'label': item['name'], 'unit': item['unit'],
                'value': item['value'], 'change': item['change'], 'change_pct': item['change_pct'],
                'as_of': item['date']})
    result['changes']['core'].sort(key=lambda item: abs(item['change_pct'] or 0), reverse=True)
    return result


def home_payload(scope):
    from tin.web.restricted_views import core_config, necessary_values
    with artifact_session(scope) as session:
        identifiers = core_config(session)
        values = necessary_values(session, scope, identifiers, 'home')
        return build_home_context(session, scope, identifiers, values)
