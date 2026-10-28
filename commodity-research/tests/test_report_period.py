"""Daily/weekly reports use saved research and bounded, dated source facts."""
from copy import deepcopy
from datetime import date, datetime

from sqlalchemy import select

from tin.config import SHANGHAI
from tin.models import Indicator, Observation, Strategy, StrategyEvent
from tests.test_isolated_workbenches import isolated, confirm_kb_source
from tests.test_workbench_shared_ui import login, page
from tests.test_brief_export import export_client, endpoint, WB


def test_nested_judgment_is_readable_without_python_dict_representations():
    from tin.web.report_view import present_snapshot
    payload = {'as_of': '2026-01-09', 'judgment': {
        'tone': '中性', 'contradiction': {'text': '供需仍待验证',
            'bull': {'claim': '供应收紧', 'evidence': [{'desc': '交付减少', 'series_id': 'real-a'}]},
            'bear': {'claim': '需求偏弱', 'evidence': [{'desc': '采购减少'}]}},
        'thresholds': [{'id': 'support', 'name': '已保存区间', 'series_id': 'real-a',
                        'operator': 'between', 'value': [0, 3]}],
        'falsifiers': [{'id': 'recheck', 'desc': '连续复核', 'series_id': 'real-a',
                        'expression': 'value > 3', 'consecutive_periods': 2}]},
        'signals': [{'rule_id': 'support', 'state': '在区间内'}]}
    original = deepcopy(payload)
    result = present_snapshot(payload, {'real-a': '已核对字段'})
    assert result['judgment']['contradiction'] == '供需仍待验证'
    assert result['judgment']['cases'][0]['claim'] == '供应收紧'
    assert result['judgment']['cases'][0]['evidence'][0]['name'] == '已核对字段'
    assert result['judgment']['thresholds'][0]['condition'] == '0 – 3'
    assert result['judgment']['thresholds'][0]['state'] == '在区间内'
    assert result['judgment']['falsifiers'][0]['condition'] == '指标数值高于 3 · 连续 2 期'
    assert result['judgment']['falsifiers'][0]['expression'] == 'value > 3'
    assert payload == original


def _observation(session, identifier, day, value, revision=0, caliber=None):
    when = datetime.fromisoformat(day).replace(tzinfo=SHANGHAI)
    session.add(Observation(series_id=identifier, value=value, as_of=when, fetched_at=when,
                            source='isolated-source', caliber_snapshot=caliber or {},
                            status='正常', revision=revision))


def test_weekly_period_uses_first_and_last_valid_values_latest_revision_and_no_future(session):
    from tin.web.report_period import build_period
    identifier = 'fixture.capacity'
    session.add(Indicator(series_id=identifier, name='产能样例', variety='SN', unit='吨',
                          frequency='日', fetch_mode='readonly', source='isolated-source', category='测试', caliber={}))
    session.flush()
    _observation(session, identifier, '2026-01-02T15:00', 777)
    _observation(session, identifier, '2026-01-05T15:00', 0)
    _observation(session, identifier, '2026-01-09T15:00', 10)
    _observation(session, identifier, '2026-01-09T15:00', 12, revision=1)
    _observation(session, identifier, '2026-01-10T15:00', 999)
    session.commit()
    source = {'as_of': '2026-01-09', 'observations': [{'series_id': identifier, 'value': 12,
        'unit': '吨', 'as_of': '2026-01-09T15:00:00+08:00', 'source': 'isolated-source'}]}
    result = build_period(session, 'SN', date(2026, 1, 9), 'weekly', source=source)
    assert result['period'] == {'kind': 'weekly', 'start': '2026-01-05', 'end': '2026-01-09'}
    assert result['changes'][0]['first']['value'] == 0
    assert result['changes'][0]['last']['value'] == 12
    assert result['changes'][0]['change'] == 12
    assert result['changes'][0]['change_pct'] is None
    assert result['changes'][0]['observation_count'] == 2
    assert result['changes'][0]['comparable'] is True


def test_weekly_does_not_call_an_old_observation_a_weekly_change(session):
    from tin.web.report_period import build_period
    identifier = 'fixture.old'
    session.add(Indicator(series_id=identifier, name='缺口样例', variety='SN', unit='吨',
                          frequency='月', fetch_mode='readonly', source='isolated-source', category='测试', caliber={}))
    session.flush()
    _observation(session, identifier, '2025-12-31T15:00', 25)
    session.commit()
    source = {'as_of': '2026-01-09', 'observations': [{'series_id': identifier, 'value': 25,
        'unit': '吨', 'as_of': '2025-12-31T15:00:00+08:00', 'source': 'isolated-source'}]}
    result = build_period(session, 'SN', date(2026, 1, 9), 'weekly', source=source)
    assert result['changes'][0]['first'] is None
    assert result['changes'][0]['last'] is None
    assert result['changes'][0]['change'] is None
    assert '期内无有效观测' in result['changes'][0]['note']


def test_weekly_rejects_caliber_changes_instead_of_subtracting_unlike_values(session):
    from tin.web.report_period import build_period
    identifier = 'fixture.mixed'
    session.add(Indicator(series_id=identifier, name='口径样例', variety='SN', unit='吨',
                          frequency='日', fetch_mode='readonly', source='isolated-source', category='测试', caliber={}))
    session.flush()
    _observation(session, identifier, '2026-01-05T15:00', 10, caliber={'note': '样本一'})
    _observation(session, identifier, '2026-01-09T15:00', 20, caliber={'note': '样本二'})
    session.commit()
    source = {'observations': [{'series_id': identifier, 'value': 20, 'unit': '吨'}]}
    row = build_period(session, 'SN', date(2026, 1, 9), 'weekly', source=source)['changes'][0]
    assert row['comparable'] is False and row['change'] is None
    assert '口径发生变化' in row['note']


def test_weekly_report_page_has_period_switch_and_readable_download(isolated):
    client, headers, state, _ = isolated
    field = confirm_kb_source(state, [
        {'date': '2026-01-05', 'value': 5, 'value_status': 'valid', 'source_cell': 'B3', 'date_cell': 'A3'},
        {'date': '2026-01-09', 'value': 8, 'value_status': 'valid', 'source_cell': 'B4', 'date_cell': 'A4'},
        {'date': '2026-01-10', 'value': 99, 'value_status': 'valid', 'source_cell': 'B5', 'date_cell': 'A5'}])
    login(client, headers)
    response = client.get(page('/sn/reports?period=weekly&date=2026-01-09'))
    assert response.status_code == 200
    assert 'id="report-period-switch"' in response.text
    assert '2026-01-05 — 2026-01-09' in response.text
    assert '首个有效值' in response.text and '末个有效值' in response.text
    panel_ids = ['id="report-summary"', 'id="report-research"', 'id="report-dashboard"', 'id="report-watch"']
    assert all(identifier in response.text for identifier in panel_ids)
    assert [response.text.index(identifier) for identifier in panel_ids] == sorted(response.text.index(identifier) for identifier in panel_ids)
    assert '待关注' in response.text and 'report-figures.js' in response.text
    assert '实际成交记录' in response.text
    download = client.get(page('/sn/reports/2026-01-09/weekly.md'))
    assert download.status_code == 200
    assert 'text/markdown' in download.headers['content-type']
    assert '# ' in download.text and '周报' in download.text
    assert '| Quantity |' in download.text and field in download.text
    assert '99.0' not in download.text
    assert '## 研究判断' in download.text
    assert all('\n## ' + title in download.text for title in ['摘要', '研判策略', '数据展板', '待关注'])
    assert client.get(page('/sn/reports?period=unknown')).status_code == 400


def test_saved_strategy_events_are_period_scoped_without_executing_a_trade(session):
    from tin.web.report_period import build_period
    when = datetime(2026, 1, 5, 10, tzinfo=SHANGHAI)
    row = Strategy(title='模拟研究方案', variety='SN', kind='outright', mode='simulation',
        status='watching', plan={'thesis':'仅已保存研究', 'invalidation':'需要复核',
        'legs':[], 'entry': {'low': 1, 'high': 2}}, author='fixture', version=1,
        created_at=when, entry_fills=[], exit_fills=[])
    session.add(row); session.flush()
    session.add_all([StrategyEvent(strategy_id=row.id, event_type='published', actor='fixture',
        reason='研究员确认候选', at=when, payload={}),
        StrategyEvent(strategy_id=row.id, event_type='opened', actor='fixture',
        reason='未来事件不得出现', at=datetime(2026,1,10,10,tzinfo=SHANGHAI), payload={})])
    session.commit()
    result = build_period(session, 'SN', date(2026,1,9), 'weekly', source={'observations':[]})
    assert [event['event'] for event in result['strategy_events']] == ['published']
    assert result['strategies'][0]['thesis'] == '仅已保存研究'
    assert session.scalar(select(Strategy.status).where(Strategy.id==row.id)) == 'watching'
    assert session.get(Strategy,row.id).entry_fills == []


def test_personal_reports_never_include_another_researchers_brief_strategy_or_version(session):
    from tin.models import DailyBrief, Judgment, Researcher
    from tin.web.report_period import build_period
    when = datetime(2026, 1, 5, 10, tzinfo=SHANGHAI)
    people = [Researcher(username=name, weknora_user_id=name, display_name=name,
                         varieties=[], created_at=when) for name in ('report-owner', 'other-researcher')]
    session.add_all(people); session.flush()
    for person in people:
        session.add(DailyBrief(researcher_id=person.id, variety='SN', trade_date='2026-01-05',
            status='草稿', summary=person.username + ' private brief', generated_at=when,
            claims=[], unverified=[], playbook_item_ids=[], signal_ids=[]))
        strategy = Strategy(researcher_id=person.id, title=person.username + ' private strategy',
            variety='SN', kind='outright', mode='simulation', status='draft', plan={}, author=person.username,
            version=1, created_at=when, entry_fills=[], exit_fills=[])
        session.add(strategy); session.flush()
        session.add(StrategyEvent(strategy_id=strategy.id, event_type='created', actor=person.username,
                                 reason=person.username + ' private event', at=when, payload={}))
        session.add(Judgment(researcher_id=person.id, variety='SN', version=person.id,
            author=person.username, written_at=when.date(), review_due=date(2026,1,12),
            review_period_days=7, contradiction={}, marginal_focus=[], pricing_power={}, whitelist=[],
            thresholds=[], falsifiers=[], unstructured=[], status='草稿', created_at=when))
    session.commit()
    session.info['weknora_user_id'] = 'report-owner'
    result = build_period(session, 'SN', date(2026,1,9), 'weekly', source={'observations':[]})
    assert [row['summary'] for row in result['briefs']] == ['report-owner private brief']
    assert [row['title'] for row in result['strategies']] == ['report-owner private strategy']
    assert [row['reason'] for row in result['strategy_events']] == ['report-owner private event']
    assert [row['version'] for row in result['research_events']] == [people[0].id]
    assert 'other-researcher' not in str(result)


def test_report_includes_only_owned_current_research_cards_without_original_source_blobs(session):
    import json
    from uuid import uuid4
    from tin.integration.identity import Identity
    from tin.integration.workbench_scope import WorkbenchScope, current_workbench
    from tin.models import WorkbenchArtifact
    from tin.web.report_period import build_period
    from tin.web.report_view import readable_markdown
    from tin.web.research_references import remember_record_scope
    identity = Identity('card-owner', 'cards@test.invalid', 'Cards', 17, 'owner')
    scope = WorkbenchScope(identity, '50505050-5050-4050-8050-505050505050', 'library', 17,
                           {'workspace_mode': 'isolated', 'can_edit': True}, 'Bearer fixture')
    session.info.update(identity.session_info(), workbench_id=scope.workbench_id)
    when = datetime(2026, 1, 5, 10, tzinfo=SHANGHAI)
    for actor in ('card-owner', 'foreign-card-owner'):
        body = {'format': 'research-strategy-card.v1', 'title': actor + '人工研究卡', 'direction':'observe',
                'thesis':'仅保存研究假设', 'watch_for':'等待真实需求数据', 'trigger':'原件数据确认后复核',
                'invalidation':'证据改变则撤回', 'review_date':'2026-01-12',
                'original_text':'PRIVATE_ORIGINAL_SOURCE_TEXT'}
        row = WorkbenchArtifact(id=str(uuid4()), module='strategies', actor=actor, title=body['title'],
            body=json.dumps(body,ensure_ascii=False), status='draft', source_ids=[],
            source_snapshots=[{'private':'PRIVATE_SOURCE_SNAPSHOT'}], revision=1, created_at=when, updated_at=when)
        session.add(row)
        session.flush()
        if actor == identity.user_id:
            remember_record_scope(session, 'artifact', row.id)
    session.commit()
    marker = current_workbench.set(scope)
    try:
        result = build_period(session,'SN',date(2026,1,9),'weekly',source={'observations':[]})
    finally:
        current_workbench.reset(marker)
    assert [row['title'] for row in result['research_cards']] == ['card-owner人工研究卡']
    assert result['research_cards'][0]['trigger'] == '原件数据确认后复核'
    assert '原件数据确认后复核' in ' '.join(row['text'] for row in result['next_review'])
    assert 'foreign-card-owner' not in str(result)
    assert 'PRIVATE_ORIGINAL_SOURCE_TEXT' not in str(result)
    assert 'PRIVATE_SOURCE_SNAPSHOT' not in str(result)
    md = readable_markdown(result)
    assert 'card-owner人工研究卡' in md and '等待真实需求数据' in md
    assert '当前保存的人工研究策略' in md


def test_weekly_export_on_a_weekend_uses_only_its_week_and_keeps_native_idempotency(export_client):
    from tin.config import settings
    client, headers, state = export_client
    field = 'kb-' + 'b' * 48
    with state['factories'][WB]() as session:
        session.add(Indicator(series_id=field, name='期内合成字段', variety=settings.variety,
            category='原表', caliber={}, unit='件', source='knowledge', frequency='日', fetch_mode='structured'))
        session.flush()
        _observation(session, field, '2026-02-02T15:00', 10)
        _observation(session, field, '2026-02-06T15:00', 16)
        _observation(session, field, '2026-02-09T15:00', 999)
        session.commit()
    first = client.post(endpoint('reports/2026-02-08/weekly/export'), headers=headers, json={'folder_path':'日报'})
    assert first.status_code == 200, first.text
    content = state['uploads'][0]['file'].decode()
    assert '2026-02-02 — 2026-02-08' in content and '周报' in content
    assert '| 期内合成字段 | 10 | 16 | 6 | 件 |' in content
    assert '"value": 10.0' in content and '"value": 16.0' in content
    assert '999.0' not in content
    assert all('\n## ' + title in content for title in ['摘要', '研判策略', '数据展板', '待关注'])
    repeated = client.post(endpoint('reports/2026-02-08/weekly/export'), headers=headers, json={'folder_path':'日报'})
    assert repeated.status_code == 200
    assert repeated.json()['knowledge_id'] == first.json()['knowledge_id']
    assert repeated.json()['reused'] is True and len(state['uploads']) == 1
    other = client.post(endpoint('reports/2026-02-08/weekly/export', 'a0000000-0000-4000-8000-000000000002'),
                        headers=headers, json={})
    assert other.status_code == 404


def test_report_scope_budget_is_explicit_instead_of_returning_a_partial_week(session):
    import pytest
    from fastapi import HTTPException
    from tin.web.report_period import build_period
    with pytest.raises(HTTPException) as raised:
        build_period(session, 'SN', date(2026,1,9), 'weekly',
                     source={'observations':[{'series_id': 'fixture.'+str(i)} for i in range(257)]})
    assert raised.value.status_code == 413


def test_saved_research_without_facts_still_has_a_readable_report_and_export(export_client):
    import json
    from tin.models import WorkbenchArtifact
    client, headers, state = export_client
    when = datetime(2026, 1, 5, 10, tzinfo=SHANGHAI)
    body = {'format':'research-strategy-card.v1', 'title':'无行情的人工研究卡', 'direction':'observe',
        'thesis':'记录研究假设而非成交', 'watch_for':'补充来源后再研究', 'trigger':'待原表确认',
        'invalidation':'原假设证据失效', 'review_date':'2026-01-12'}
    with state['factories'][WB]() as session:
        session.add(WorkbenchArtifact(id='a1000000-0000-4000-8000-000000000001', module='strategies',
            actor='writer', title=body['title'], body=json.dumps(body), status='draft', source_ids=[],
            source_snapshots=[], revision=1, created_at=when, updated_at=when))
        session.commit()
    response = client.get('/research/workbenches/'+WB+'/sn/reports',headers=headers)
    assert response.status_code == 200
    assert '无行情的人工研究卡' in response.text and '暂无可比较的已绑定指标' in response.text
    assert '当前保存版本用于本次数据回放' in response.text or '人工研究方案用于跟踪假设与条件' in response.text
    result = client.post(endpoint('reports/2026-01-09/export'),headers=headers,json={})
    assert result.status_code == 200, result.text
    content = state['uploads'][0]['file'].decode()
    assert '无行情的人工研究卡' in content and '不代表历史当时已知观点或实际成交' in content
