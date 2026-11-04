"""Strategy cards report the live state of what they explicitly cite, without writing anything."""
from contextlib import contextmanager
from dataclasses import replace
from datetime import date, datetime, timezone
import json
from uuid import uuid4

from sqlalchemy import event, func, select

from tests.test_judgment_panels import panels  # noqa: F401
from tests.test_structured_judgment_import import payload
from tin.config import SHANGHAI
from tin.integration import page_facts, workbench_scope
from tin.judgments.service import save_version
from tin.models import Indicator, Observation, ReviewTask, Signal
from tin.models.workbench import WorkbenchArtifact
from tin.schemas.judgment import JudgmentIn
from tin.web.research_references import remember_record_scope

STAMP = datetime(2026, 1, 3, tzinfo=timezone.utc)


def seeded(panels, *, response='Reduce exposure step by step', citations=None, observations=((1, 3), (2, 4))):
    _client, state = panels
    scope = replace(state['scope'], scope_version=2)
    document = payload()
    identifier = document['whitelist'][0]
    document['falsifiers'] = [{'id': 'fixture_warning', 'desc': 'Measured condition',
        'series_id': identifier, 'expression': 'value > 2', 'unit_confirmed': '吨',
        'consecutive_periods': 2, 'side': 'bull'}]
    with workbench_scope.artifact_session(scope) as session:
        session.info.update(knowledge_facts=True, knowledge_facts_catalog={identifier: {'unit': '吨'}})
        row = save_version(session, JudgmentIn.model_validate(document), actor=scope.identity.user_id)
        row.status = '生效'
        session.add(Indicator(series_id=identifier, name='A real fixture series', unit='吨', variety='XY',
            category='fixture', frequency='日', source='Fixture', caliber={}, fetch_mode='readonly'))
        for day, value in observations:
            session.add(Observation(series_id=identifier, as_of=datetime(2026, 1, day, tzinfo=SHANGHAI),
                value=value, status='正常', revision=0, source='Fixture', caliber_snapshot={},
                fetched_at=datetime(2026, 1, 2, tzinfo=SHANGHAI)))
        session.flush()
        cited = citations if citations is not None else [
            {'kind': 'judgment_condition', 'field': 'invalidation', 'id': str(row.id), 'revision': row.version,
             'rule_kind': 'falsifier', 'rule_id': 'fixture_warning', 'snapshot': {'title': 'Measured condition'}},
            {'kind': 'indicator', 'field': 'trigger', 'series_id': identifier, 'source_version': 'fixture-version',
             'snapshot': {'title': 'A real fixture series'}}]
        body = {'format': 'research-strategy-card.v1', 'title': 'Fixture strategy', 'direction': 'range',
            'thesis': 'NOT_HOME_CARD_BODY', 'watch_for': '', 'trigger': 'Enter on confirmation',
            'invalidation': 'Withdraw when the cited condition holds', 'review_date': '2026-02-01',
            'citations': [dict(item, field=item['field']) for item in cited]}
        if response is not None:
            body['response_action'] = response
        card = WorkbenchArtifact(id=str(uuid4()), module='strategies', actor=scope.identity.user_id,
            title='Fixture strategy', status='draft', body=json.dumps(body), source_ids=[], source_snapshots=[],
            revision=1, created_at=STAMP, updated_at=STAMP)
        session.add(card); session.flush()
        remember_record_scope(session, 'artifact', card.id)
        session.commit()
        return scope, {'judgment': row.id, 'card': card.id, 'series': identifier, 'engine': session.get_bind()}


def bounded_reads(monkeypatch, scope):
    requests = []

    @contextmanager
    def bounded(received):
        assert received == scope
        requests.append(dict(page_facts._page.get()))
        with workbench_scope.artifact_session(scope) as session:
            yield session
    monkeypatch.setattr(workbench_scope, 'workbench_session', bounded)
    return requests


def read_card(scope, card_id):
    from tin.web.research_strategy import _read_card
    with workbench_scope.artifact_session(scope) as session:
        return _read_card(session, scope, card_id)


def test_card_monitor_evaluates_cited_condition_and_indicator_without_writes(panels, monkeypatch):
    from tin.web.strategy_monitor import card_monitor
    scope, ids = seeded(panels)
    requests = bounded_reads(monkeypatch, scope)
    writes = []
    def watch(_conn, _cursor, sql, *_):
        if sql.lstrip().upper().startswith(('INSERT', 'UPDATE', 'DELETE')):
            writes.append(sql)
    event.listen(ids['engine'], 'before_cursor_execute', watch)
    try:
        result = card_monitor(scope, read_card(scope, ids['card']), day=date(2026, 1, 2))
    finally:
        event.remove(ids['engine'], 'before_cursor_execute', watch)
    assert not writes
    [condition] = result['conditions']
    assert condition['state'] == '触发' and condition['value'] == 4 and condition['as_of'] == '2026-01-02'
    assert condition['field_label'] == '暂停 / 撤回' and condition['outdated'] is False
    assert condition['chart']['latest'] == 4
    [indicator] = result['indicators']
    assert indicator['field_label'] == '适用 / 触发' and indicator['chart']['latest'] == 4
    assert indicator['chart']['freshness'] == {'age_days': 0, 'usual_interval_days': 1, 'stale': False}
    assert result['counts'] == {'conditions': 1, 'triggered': 1, 'missing': 0, 'indicators': 1}
    # The cited judgment's own rule window and the cited indicator's explicit window; nothing else.
    assert [(row['module'], row['judgment_id'], row['series_ids']) for row in requests] == [
        ('judgments', ids['judgment'], None), ('strategies', None, [ids['series']])]
    with workbench_scope.artifact_session(scope) as session:
        assert session.scalar(select(func.count()).select_from(Signal)) == 0
        assert session.scalar(select(func.count()).select_from(ReviewTask)) == 0


def test_untriggered_condition_and_stale_indicator_are_reported_as_such(panels, monkeypatch):
    from tin.web.strategy_monitor import card_monitor
    scope, ids = seeded(panels, observations=((1, 3), (2, 1)))
    bounded_reads(monkeypatch, scope)
    result = card_monitor(scope, read_card(scope, ids['card']), day=date(2026, 3, 1))
    assert result['conditions'][0]['state'] == '未触发'
    freshness = result['indicators'][0]['chart']['freshness']
    assert freshness['stale'] is True and freshness['age_days'] == 58


def test_condition_no_longer_in_cited_version_is_not_replaced(panels, monkeypatch):
    from tin.web.strategy_monitor import card_monitor
    scope, ids = seeded(panels, citations=[])
    with workbench_scope.artifact_session(scope) as session:
        card = session.get(WorkbenchArtifact, ids['card'])
        body = json.loads(card.body)
        body['citations'] = [{'kind': 'judgment_condition', 'field': 'trigger', 'id': str(ids['judgment']),
            'revision': 1, 'rule_kind': 'falsifier', 'rule_id': 'removed_rule'}]
        card.body = json.dumps(body)
        session.commit()
    bounded_reads(monkeypatch, scope)
    result = card_monitor(scope, read_card(scope, ids['card']), day=date(2026, 1, 2))
    assert result['conditions'][0]['state'] == '无法核对' and '重新引用' in result['conditions'][0]['note']
    assert result['counts']['missing'] == 1 and result['indicators'] == []


def test_warning_check_reports_triggered_strategy_conditions(panels, monkeypatch):
    from tin.web.affairs_check import check_current
    scope, ids = seeded(panels)
    requests = bounded_reads(monkeypatch, scope)
    result = check_current(scope, day=date(2026, 1, 2))
    strategy = [item for item in result['alerts'] if item['kind'] == 'strategy']
    assert len(strategy) == 1 and strategy[0]['title'] == 'Fixture strategy · Measured condition'
    assert strategy[0]['href'] == '/sn/strategies?selected=research:' + ids['card']
    assert '暂停 / 撤回' in strategy[0]['detail'] and strategy[0]['evidence_dates'] == ['2026-01-02']
    assert result['checked_strategies'] == 1 and '1 个策略方案' in result['notice']
    # Quoted indicators are not rules: the check reads only judgment rule windows.
    assert {row['module'] for row in requests} == {'judgments'}
    assert 'NOT_HOME_CARD_BODY' not in json.dumps(result, ensure_ascii=False)


def test_affairs_lists_monitored_and_observation_only_strategies(panels):
    from tin.web.affairs import build_affairs
    scope, ids = seeded(panels, response=None)
    with workbench_scope.artifact_session(scope) as session:
        result = build_affairs(session, scope, today=date(2026, 1, 5))
    assert result['monitored_strategies'] == 1 and result['can_check'] is True
    assert result['observations'] == [{'title': 'Fixture strategy', 'detail': '观察方案 · 尚未填写应对措施',
                                       'href': '/sn/strategies?selected=research:' + ids['card']}]
    assert 'NOT_HOME_CARD_BODY' not in json.dumps(result, ensure_ascii=False)


def test_affairs_does_not_offer_a_check_for_cards_that_only_quote_indicators(panels):
    from tin.web.affairs import build_affairs
    scope, ids = seeded(panels, citations=[{'kind': 'indicator', 'field': 'trigger', 'series_id': 'kb-' + 'f' * 48,
                                             'source_version': 'fixture-version'}])
    hidden = replace(scope, view_modules=('home', 'strategies'))
    with workbench_scope.artifact_session(hidden) as session:
        result = build_affairs(session, hidden, today=date(2026, 1, 5))
    assert result['monitored_strategies'] == 0 and result['can_check'] is False
