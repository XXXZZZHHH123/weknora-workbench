"""Explicit, bounded warning checks; opening the affairs page performs no scan."""
from datetime import date, datetime
from urllib.parse import urlencode

from fastapi import HTTPException

from tin.config import SHANGHAI
from tin.integration import workbench_scope
from tin.integration.page_facts import _judgment_windows, page_read_scope
from tin.integration.research_profile import research_variety
from tin.judgments.service import current, to_payload
from tin.web.judgment_panels import selected_judgment
from tin.web.research_references import record_in_workbench


def check_current(scope, *, day=None):
    if (scope is None or not scope.authorization or scope.restricted
            or 'home' not in scope.view_modules or 'judgments' not in scope.view_modules):
        raise HTTPException(403, '当前授权不开放研判预警检查')
    if scope.scope_version != 2:
        raise HTTPException(409, '请重新打开研究台以更新来源授权后检查预警')
    cutoff = day or datetime.now(SHANGHAI).date()
    if type(cutoff) is not date:
        raise HTTPException(422, '预警检查需要明确的日期')
    checked = datetime.now(SHANGHAI).isoformat(timespec='seconds')
    result = {'alerts': [], 'checked_at': checked, 'checked_rules': 0, 'partial': False,
              'notice': '尚未保存可检查的研判规则，不能据此认定未触发。'}
    with workbench_scope.artifact_session(scope) as session:
        from tin.models import Researcher
        from sqlalchemy import select
        # Unlike writable ownership helpers, a first read must not create a profile.
        owner = session.scalar(select(Researcher.id).where(Researcher.weknora_user_id == scope.identity.user_id))
        if owner is None:
            return result
        judgment = current(session, research_variety(session, scope=scope), scope=scope)
        if (judgment is None or judgment.status == '已归档'
                or not record_in_workbench(session, scope, 'judgment', str(judgment.id), judgment)):
            return result
        windows = _judgment_windows(to_payload(judgment))
        expected = len(judgment.thresholds) + len(judgment.falsifiers)
        if not expected:
            return result
        if len(windows) > 256 or sum(windows.values()) > 20000 or expected > 256:
            raise HTTPException(413, '当前研判超过预警检查容量，请先拆分规则')
        identifier, variety = judgment.id, judgment.variety
        labels = {('threshold', rule['id']): rule.get('name') or rule['id'] for rule in judgment.thresholds}
        labels.update({('falsifier', rule['id']): rule.get('desc') or rule['id'] for rule in judgment.falsifiers})
    with page_read_scope('judgments', judgment_id=identifier, day=cutoff):
        with workbench_scope.workbench_session(scope) as session:
            selected = selected_judgment(session, identifier, scope=scope)
            formal = current(session, variety, scope=scope)
            if formal is None or formal.id != selected.id:
                raise HTTPException(409, '当前生效研判已变化，请刷新后重新检查')
            from tin.compute.signals import evaluate_preview
            try:
                signals = evaluate_preview(session, variety, cutoff)
            except ValueError as exc:
                raise HTTPException(409, str(exc)) from None
            if len(signals) != expected or any(row.judgment_id != identifier for row in signals):
                raise HTTPException(409, '当前规则在检查期间已变化，请刷新后重试')
            href = '/sn/judgment?' + urlencode({'judgment_id': identifier, 'date': cutoff.isoformat()})
            for signal in signals:
                if signal.state not in {'触发', '数据缺失'}:
                    continue
                evidence = signal.evidence or []
                detail = signal.gap_note or '；'.join(str(item.get('desc') or '') for item in evidence)
                result['alerts'].append({'kind': 'triggered' if signal.state == '触发' else 'missing',
                    'title': labels.get((signal.rule_type, signal.rule_id), signal.rule_id) + ' · ' + signal.state,
                    'detail': detail[:1000], 'href': href, 'recorded_at': checked,
                    'trade_date': cutoff.isoformat(),
                    'evidence_dates': list(dict.fromkeys(str(item.get('as_of') or '') for item in evidence
                                                         if item.get('as_of')))})
    result['checked_rules'] = expected
    result['notice'] = (f'按截至 {cutoff.isoformat()} 的可读数据试算 {expected} 条当前研判规则；'
                        '数据时点以各条依据为准，不是实时行情，不写入信号、复核任务或成交。')
    return result
