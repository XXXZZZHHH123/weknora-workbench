"""Monitor authorization, model protocol, provenance, revisions, and edge-triggered alerts."""
from dataclasses import replace
from datetime import date
import json

from fastapi import FastAPI, HTTPException
from fastapi.testclient import TestClient
from pydantic import ValidationError
import pytest
from sqlalchemy import select

from tin.config import settings
from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, artifact_session, current_workbench
from tin.models.workbench import WorkbenchArtifactRevision
from tin.web import monitors

URL = '/api/sn/monitors'
SID = 'KB.fixture.warehouse'
OTHER_SID = 'KB.fixture.basis'
REAL_CANDIDATE_METADATA = monitors._candidate_metadata


def rule(**changes):
    return {'series_id': SID, 'metric': 'change', 'operator': '>', 'value': 0,
            'consecutive_periods': 2, 'label': '仓单', 'definition': '仓单本期比上一期增加', **changes}


def model_rule(**changes):
    return {'indicator_description': '仓单', 'metric': 'change', 'operator': '>', 'value': 0,
            'value_source': 'explicit', 'consecutive_periods': 2,
            'label': '仓单', 'definition': '仓单本期比上一期增加', **changes}


def model_output(**changes):
    return {'title': '库存组合', 'logic': 'all', 'rules': [model_rule()], 'confirmations': [], **changes}


@pytest.fixture
def monitor_client(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    app, state = FastAPI(), {}
    app.include_router(monitors.router)
    @app.middleware('http')
    async def scoped(request, call_next):
        identity = Identity(request.headers.get('x-actor', 'first'), '', 'Fixture', 17,
            request.headers.get('x-role', 'owner'))
        scope = WorkbenchScope(identity, request.headers.get('x-workbench', '9e761d83-5abe-4adf-859d-6cd57816ddaf'),
            'fixture-kb', 17, {'workspace_mode': 'isolated', 'can_edit': True,
            'can_read_source': request.headers.get('x-restricted') != 'true'}, 'Bearer fixture', view_modules=('home',), scope_version=2)
        state['scope'] = scope
        token, actor = current_workbench.set(scope), current_identity.set(identity)
        try:
            return await call_next(request)
        finally:
            current_workbench.reset(token)
            current_identity.reset(actor)
    monkeypatch.setattr(monitors, '_validate_catalog', lambda scope, rules: {})
    monkeypatch.setattr(monitors, '_candidate_metadata', lambda *args: {})
    with TestClient(app) as client:
        client.get(URL)
        yield client, state


def seed_draft(state, candidates=None):
    scope = state['scope']
    with artifact_session(scope) as session:
        row = monitors._new_artifact(scope, monitors.DRAFT_MODULE, '草稿', {'workbench_id': scope.workbench_id,
            'sentence': '连续增加时提醒我', 'candidates': candidates or [{'series_id': SID}]})
        session.add(row)
        session.commit()
        return row.id


def body(state, **changes):
    return {'draft_id': seed_draft(state), 'title': '库存组合', 'sentence': '连续增加时提醒我',
            'logic': 'all', 'rules': [rule()], 'enabled': True, **changes}


@pytest.mark.parametrize('changes', [
    {'operator': '='}, {'metric': 'pct'}, {'value': True}, {'value': float('inf')},
    {'consecutive_periods': 0}, {'consecutive_periods': 121}, {'consecutive_periods': True},
    {'label': ' '}, {'definition': ''}, {'extra': 'injected'},
])
def test_rule_validation(changes):
    with pytest.raises((ValidationError, ValueError)):
        monitors.Rule.model_validate(rule(**changes))


def test_personal_monitor_revisions_and_provenance(monitor_client):
    client, state = monitor_client
    request = body(state)
    invalid = {**request, 'rules': [rule(series_id=OTHER_SID)]}
    assert client.post(URL, json=invalid).status_code == 422
    assert client.post(URL, json={**request, 'sentence': 'new request'}).status_code == 422
    saved = client.post(URL, json=request)
    assert saved.status_code == 201, saved.text
    item = saved.json()
    assert item['revision'] == 1 and item['sentence'] == request['sentence']
    assert client.get(URL, headers={'x-actor': 'second'}).json()['items'] == []
    assert client.patch(URL + '/' + item['id'], json={'revision': 1, 'enabled': False},
                        headers={'x-actor': 'second'}).status_code == 404
    assert client.get(URL).json()['items'][0]['id'] == item['id']
    changed = client.patch(URL + '/' + item['id'], json={'revision': 1, 'enabled': False})
    assert changed.status_code == 200 and changed.json()['revision'] == 2
    assert client.patch(URL + '/' + item['id'], json={'revision': 1, 'enabled': True}).status_code == 409
    assert client.delete(URL + '/' + item['id'] + '?revision=1').status_code == 409
    assert client.delete(URL + '/' + item['id'] + '?revision=2').status_code == 200
    assert client.get(URL).json()['items'] == []
    history = client.get(URL + '/' + item['id'] + '/revisions').json()['items']
    assert [row['revision'] for row in history] == [3, 2, 1]
    assert history[0]['status'] == 'deleted'
    assert client.get(URL + '/' + item['id'] + '/revisions', headers={'x-actor': 'second'}).status_code == 404


def test_draft_belongs_to_account_and_workspace(monitor_client):
    client, state = monitor_client
    request = body(state)
    assert client.post(URL, json=request, headers={'x-actor': 'second'}).status_code == 404
    assert client.post(URL, json=request, headers={'x-workbench': '36a33274-1730-4ce8-8c32-62c2d23dd032'}).status_code == 404
    assert client.post(URL, json=request, headers={'x-restricted': 'true'}).status_code == 403
    assert client.post(URL, json=request, headers={'x-role': 'viewer'}).status_code == 403


def test_model_draft_recall_and_per_rule_candidate_validation(monitor_client, monkeypatch):
    from tin.web import strategy_text
    client, state = monitor_client
    calls = []
    catalog = [{'id': SID, 'series_id': SID, 'name': '仓单', 'unit': '吨', 'frequency': '周'}]
    monkeypatch.setattr(monitors, '_draft_context', lambda scope: ([], catalog))
    async def completion(scope, model, prompt, **kwargs):
        calls.append((prompt, kwargs))
        return model_output() if len(calls) == 1 else {'picks': {'0': ['r1']}}
    monkeypatch.setattr(strategy_text, 'completion', completion)
    response = client.post(URL + '/draft', json={'sentence': '连续增加时提醒我', 'model_id': 'fixture'})
    assert response.status_code == 200, response.text
    draft = response.json()
    assert draft['rules'][0]['series_id'] == SID
    assert draft['candidates'][0]['series_id'] == SID
    assert draft['candidates'][0]['rule_index'] == 0
    assert calls[0][1]['purpose'] == 'monitor_draft'
    assert calls[1][1]['purpose'] == 'monitor_selection'
    assert '不要输出series_id' in calls[0][0]
    assert client.post(URL, json={key: draft[key] for key in (
        'draft_id', 'title', 'sentence', 'logic', 'rules')}).status_code == 201


def test_draft_displays_real_candidate_coverage_without_changing_model_selection(monitor_client, monkeypatch):
    from tin.web import chart_data, monitor_coverage, strategy_text
    client, state = monitor_client
    fields = [
        {'id': SID, 'series_id': SID, 'name': '仓单', 'frequency': '周'},
        {'id': OTHER_SID, 'series_id': OTHER_SID, 'name': '仓单另一口径', 'frequency': '日'},
    ]
    monkeypatch.setattr(monitors, '_draft_context', lambda scope: ([], fields))
    monkeypatch.setattr(chart_data, 'catalog_fields', lambda scope, ids: {
        field['series_id']: field for field in fields if field['series_id'] in ids})
    coverage = {
        SID: {'start': '2022-07-13', 'end': '2022-11-04', 'point_count': 79, 'frequency': '周', 'stale': True},
        OTHER_SID: {'start': '2020-01-01', 'end': '2026-11-01', 'point_count': 900,
                    'frequency': '日', 'stale': False},
    }
    reads = []
    def read(scope, identifiers, **kwargs):
        reads.append(identifiers)
        return coverage
    monkeypatch.setattr(monitor_coverage, 'read_coverage', read)
    # The shared client isolates the coverage transport; restore only this real enrichment path.
    monkeypatch.setattr(monitors, '_candidate_metadata', REAL_CANDIDATE_METADATA)
    calls = []
    async def completion(*args, **kwargs):
        calls.append(1)
        return model_output() if len(calls) == 1 else {'picks': {'0': ['r1']}}
    monkeypatch.setattr(strategy_text, 'completion', completion)
    response = client.post(URL + '/draft', json={'sentence': '连续增加时提醒我', 'model_id': 'fixture'})
    assert response.status_code == 200, response.text
    draft = response.json()
    assert draft['rules'][0]['series_id'] == SID
    assert set(reads[0]) == {SID, OTHER_SID}
    assert {item['series_id']: item['coverage'] for item in draft['candidates']} == coverage
    assert draft['default_window']['end'] == coverage[SID]['end']
    with artifact_session(state['scope']) as session:
        stored = monitors._body(monitors._owned(session, state['scope'], draft['draft_id'], monitors.DRAFT_MODULE))
        assert stored['candidates'] == draft['candidates']


@pytest.mark.parametrize('output', [
    model_output(rules=[model_rule(value_source='suggested', value=123)]),
    model_output(rules=[model_rule(value_source='suggested', definition='建议值，请确认')]),
    model_output(rules=[]), model_output(logic='xor'),
    model_output(rules=[{**model_rule(), 'series_id': SID}]),
])
def test_illegal_model_draft_is_rejected_without_selection_or_fallback(monitor_client, monkeypatch, output):
    from tin.web import strategy_text
    client, _ = monitor_client
    monkeypatch.setattr(monitors, '_draft_context', lambda scope: ([], []))
    calls = []
    async def completion(*args, **kwargs):
        calls.append(1)
        return output
    monkeypatch.setattr(strategy_text, 'completion', completion)
    assert client.post(URL + '/draft', json={'sentence': '请提醒', 'model_id': 'fixture'}).status_code == 422
    assert calls == [1]
    assert client.get(URL).json()['items'] == []


def test_suggested_threshold_requires_visible_confirmation():
    output = monitors.DraftOutput.model_validate(model_output(rules=[model_rule(value=123,
        value_source='suggested', definition='建议值，请确认：本期值大于123')], confirmations=['确认建议阈值123']))
    assert output.rules[0].value == 123


def test_model_failure_is_reported_without_fallback(monitor_client, monkeypatch):
    from tin.web import strategy_text
    client, _ = monitor_client
    monkeypatch.setattr(monitors, '_draft_context', lambda scope: ([], []))
    async def failed(*args, **kwargs):
        raise HTTPException(503, 'fixture model unavailable')
    monkeypatch.setattr(strategy_text, 'completion', failed)
    result = client.post(URL + '/draft', json={'sentence': '请提醒', 'model_id': 'fixture'})
    assert result.status_code == 503 and result.json()['detail'] == 'fixture model unavailable'
    assert client.get(URL).json()['items'] == []


@pytest.mark.parametrize('picks', [{'0': ['fake']}, {'wrong': ['r1']}, {'0': []}, {'0': ['r1', 'r1']}])
def test_unshown_model_candidate_rejected(monitor_client, monkeypatch, picks):
    from tin.web import strategy_text
    client, _ = monitor_client
    monkeypatch.setattr(monitors, '_draft_context', lambda scope: ([], [
        {'id': SID, 'series_id': SID, 'name': '仓单'}]))
    calls = []
    async def completion(*args, **kwargs):
        calls.append(1)
        return model_output() if len(calls) == 1 else {'picks': picks}
    monkeypatch.setattr(strategy_text, 'completion', completion)
    assert client.post(URL + '/draft', json={'sentence': '请提醒', 'model_id': 'fixture'}).status_code == 422


def test_checks_alert_only_on_transition_and_persist(monitor_client, monkeypatch):
    client, state = monitor_client
    saved = client.post(URL, json=body(state)).json()
    current = {'state': '未触发'}
    monkeypatch.setattr(monitors, 'backtest', lambda *args, **kwargs: {
        'current_state': current['state'], 'last_trigger': '2026-01-02' if current['state'] == '触发' else None,
        'series': [{'latest_date': '2026-01-02'}]})
    scope = state['scope']
    def check():
        return monitors.check_monitors(scope, date(2026, 1, 2), '2026-01-02T12:00:00+08:00')
    assert check()['alerts'] == []
    current['state'] = '触发'
    assert len(check()['alerts']) == 1
    assert check()['alerts'] == []
    current['state'] = '数据停更'
    assert check()['alerts'] == []
    current['state'] = '触发'
    assert check()['alerts'] == []
    current['state'] = '未触发'
    assert check()['alerts'] == []
    current['state'] = '触发'
    assert len(check()['alerts']) == 1
    listed = client.get(URL).json()['items'][0]
    assert listed['current_state'] == '触发' and listed['last_trigger'] == '2026-01-02'
    with artifact_session(scope) as session:
        assert len(monitors.recorded_monitor_alerts(session, scope)) == 2
        assert len(list(session.scalars(select(WorkbenchArtifactRevision).where(
            WorkbenchArtifactRevision.artifact_id == saved['id'])))) == 1
    client.patch(URL + '/' + saved['id'], json={'revision': 1, 'enabled': False})
    assert check()['checked'] == 0


def test_home_only_monitor_is_included_in_affairs_check(monitor_client, monkeypatch):
    from tin.web.affairs_check import check_current
    client, state = monitor_client
    client.post(URL, json=body(state))
    scope = replace(state['scope'], scope_version=2)
    monkeypatch.setattr(monitors, 'backtest', lambda *args, **kwargs: {'current_state': '触发', 'series': []})
    first = check_current(scope, day=date(2026, 1, 2))
    assert first['checked_monitors'] == 1 and len(first['alerts']) == 1
    assert check_current(scope, day=date(2026, 1, 2))['alerts'] == []


def test_expired_candidate_draft_rejected(monitor_client):
    from datetime import timedelta
    client, state = monitor_client
    request = body(state)
    with artifact_session(state['scope']) as session:
        draft = monitors._owned(session, state['scope'], request['draft_id'], monitors.DRAFT_MODULE)
        draft.created_at -= timedelta(days=2)
        session.commit()
    assert client.post(URL, json=request).status_code == 409


def test_backtest_enforces_candidate_ids_without_writing_a_monitor(monitor_client, monkeypatch):
    client, state = monitor_client
    calls = []
    monkeypatch.setattr(monitors, 'backtest', lambda scope, rules, logic, **kwargs: (
        calls.append((rules, logic)) or {'current_state': '未触发', 'trigger_dates': []}))
    request = body(state)
    assert client.post(URL + '/backtest', json={**request, 'rules': [rule(series_id=OTHER_SID)]}).status_code == 422
    assert calls == []
    assert client.post(URL + '/backtest', json=request).status_code == 200
    assert calls == [(request['rules'], 'all')]
    assert client.get(URL).json()['items'] == []
    saved = client.post(URL, json=request).json()
    assert client.get(URL + '/' + saved['id'] + '/backtest').status_code == 200
    assert client.get(URL + '/' + saved['id'] + '/backtest', headers={'x-actor': 'second'}).status_code == 404


def test_backtest_dates_forwarded_for_draft_body_query_and_saved_monitor(monitor_client, monkeypatch):
    client, state = monitor_client
    calls = []
    def read(scope, rules, logic, **kwargs):
        calls.append(kwargs)
        return {'start': kwargs.get('start'), 'end': kwargs.get('end')}
    monkeypatch.setattr(monitors, 'backtest', read)
    request = body(state)
    window = {'start': '2022-01-01', 'end': '2022-11-04'}
    assert client.post(URL + '/backtest', json={**request, **window}).json() == window
    assert client.post(URL + '/backtest', params=window, json=request).json() == window
    saved = client.post(URL, json=request).json()
    assert client.get(URL + '/' + saved['id'] + '/backtest', params=window).json() == window
    assert calls == [window] * 3
    conflict = client.post(URL + '/backtest', params={'end': '2023-01-01'}, json={**request, **window})
    assert conflict.status_code == 422 and len(calls) == 3
    assert client.post(URL + '/backtest', json={**request, 'start': 20220101}).status_code == 422
    assert client.post(URL, json={**request, **window}).status_code == 422
    assert client.get(URL).json()['items'][0]['rules'] == request['rules']


@pytest.mark.parametrize('condition_state', ['触发', '无数据'])
def test_stale_monitor_check_has_latest_date_and_never_creates_alert(monitor_client, monkeypatch, condition_state):
    client, state = monitor_client
    client.post(URL, json=body(state))
    monkeypatch.setattr(monitors, 'backtest', lambda *args, **kwargs: {
        'current_state': condition_state, 'last_trigger': '2022-11-04',
        'series': [
            {'latest_date': '2023-09-25', 'freshness': {'stale': True}},
            {'latest_date': '2022-11-04', 'latest_valid_date': '2022-11-04', 'freshness': {'stale': True}},
        ]})
    checked = monitors.check_monitors(state['scope'], date(2026, 11, 18), '2026-11-18T12:00:00+08:00')
    assert checked['checked'] == 1 and checked['alerts'] == []
    assert client.get(URL).json()['items'][0]['current_state'] == '数据停更（最新 2022-11-04）'
    with artifact_session(state['scope']) as session:
        assert monitors.recorded_monitor_alerts(session, state['scope']) == []


def test_model_cannot_pick_candidate_shown_only_for_another_rule(monitor_client, monkeypatch):
    from tin.web import strategy_text, judgment_recall
    client, _ = monitor_client
    monkeypatch.setattr(monitors, '_draft_context', lambda scope: ([], [
        {'id': SID, 'series_id': SID, 'name': '仓单'},
        {'id': OTHER_SID, 'series_id': OTHER_SID, 'name': '基差'}]))
    monkeypatch.setattr(judgment_recall, 'recall_groups', lambda index, inventory, phrased, per_item: [
        (inventory['items'][0], [SID]), (inventory['items'][1], [OTHER_SID])])
    calls = []
    async def completion(*args, **kwargs):
        calls.append(1)
        return model_output(rules=[model_rule(), model_rule(indicator_description='基差')]) if len(calls) == 1 else {
            'picks': {'0': ['r2'], '1': ['r1']}}
    monkeypatch.setattr(strategy_text, 'completion', completion)
    assert client.post(URL + '/draft', json={'sentence': '仓单与基差', 'model_id': 'fixture'}).status_code == 422


def test_same_condition_toggle_does_not_repeat_alert_and_keeps_history(monitor_client, monkeypatch):
    client, state = monitor_client
    saved = client.post(URL, json=body(state)).json()
    monkeypatch.setattr(monitors, 'backtest', lambda *args, **kwargs: {'current_state': '触发', 'series': []})
    scope = state['scope']
    check = lambda: monitors.check_monitors(scope, date(2026, 1, 2), '2026-01-02T12:00:00+08:00')
    assert len(check()['alerts']) == 1
    assert client.patch(URL + '/' + saved['id'], json={'revision': 1, 'enabled': False}).status_code == 200
    assert client.patch(URL + '/' + saved['id'], json={'revision': 2, 'enabled': True}).status_code == 200
    assert check()['alerts'] == []
    with artifact_session(scope) as session:
        assert len(monitors.recorded_monitor_alerts(session, scope)) == 1


def test_undecidable_current_check_retains_rule_reason_and_alert_transition(monitor_client, monkeypatch):
    client, state = monitor_client
    draft_id = seed_draft(state, [{'series_id': SID}, {'series_id': OTHER_SID}])
    saved = client.post(URL, json=body(state, draft_id=draft_id,
                                    rules=[rule(), rule(series_id=OTHER_SID)]))
    assert saved.status_code == 201, saved.text
    detail = '所选指标含未确认日期，无法确定均线观测顺序，请先核对原表日期'
    current = {'failed': False}

    def backtest(*args, **kwargs):
        if not current['failed']:
            return {'current_state': '触发', 'series': []}
        return {'current_state': '无法判断', 'condition_state': '无法判断', 'series': [
            {'series_id': SID, 'rule': rule(), 'state': '无法判断', 'notice': detail,
             'latest_date': None, 'read_error': detail},
            {'series_id': OTHER_SID, 'rule': rule(series_id=OTHER_SID), 'state': '触发',
             'notice': '该指标数据停更', 'latest_date': '2022-11-04', 'freshness': {'stale': True}},
        ]}

    monkeypatch.setattr(monitors, 'backtest', backtest)
    check = lambda: monitors.check_monitors(state['scope'], date(2026, 11, 18), '2026-11-18T12:00:00+08:00')
    assert len(check()['alerts']) == 1
    current['failed'] = True
    assert check()['alerts'] == []
    listed = client.get(URL).json()['items'][0]
    assert listed['current_state'] == '无法判断'
    assert listed['rule_states'][0]['state'] == '无法判断'
    assert listed['rule_states'][0]['notice'] == detail
    current['failed'] = False
    assert check()['alerts'] == []


@pytest.mark.parametrize('status', [401, 403])
def test_current_check_does_not_degrade_authorization_errors(monitor_client, monkeypatch, status):
    client, state = monitor_client
    client.post(URL, json=body(state))

    def denied(*args, **kwargs):
        raise HTTPException(status, '当前来源授权已失效')

    monkeypatch.setattr(monitors, 'backtest', denied)
    with pytest.raises(HTTPException) as caught:
        monitors.check_monitors(state['scope'], date(2026, 11, 18), '2026-11-18T12:00:00+08:00')
    assert caught.value.status_code == status
    assert client.get(URL).json()['items'][0]['current_state'] == '未检查'


def test_foreign_artifacts_and_retired_check_states_do_not_consume_active_limit(monitor_client):
    client, state = monitor_client
    request = body(state)
    saved = client.post(URL, json=request).json()
    scope = state['scope']
    with artifact_session(scope) as session:
        for index in range(101):
            session.add(monitors._new_artifact(scope, monitors.MODULE, 'foreign', {
                'workbench_id': 'foreign-workbench', 'enabled': True}))
            session.add(monitors._new_artifact(scope, monitors.STATE_MODULE, 'retired', {
                'workbench_id': scope.workbench_id, 'monitor_id': 'retired-' + str(index), 'alerts': []}))
        session.commit()
    listed = client.get(URL)
    assert listed.status_code == 200 and [item['id'] for item in listed.json()['items']] == [saved['id']]
    with artifact_session(scope) as session:
        assert monitors.recorded_monitor_alerts(session, scope) == []


def test_concurrent_checks_cannot_duplicate_transition_alert(monitor_client, monkeypatch):
    from concurrent.futures import ThreadPoolExecutor
    from threading import Barrier
    from sqlalchemy import event
    client, state = monitor_client
    client.post(URL, json=body(state))
    scope = state['scope']
    current = {'state': '未触发'}
    monkeypatch.setattr(monitors, 'backtest', lambda *args, **kwargs: {
        'current_state': current['state'], 'series': []})
    check = lambda: monitors.check_monitors(scope, date(2026, 1, 2), '2026-01-02T12:00:00+08:00')
    assert check()['alerts'] == []
    current['state'] = '触发'
    ready = Barrier(2)
    def synchronize(_conn, _cursor, sql, *_args):
        if sql.startswith('UPDATE workbench_artifacts SET'):
            ready.wait(timeout=10)
    with artifact_session(scope) as session:
        engine = session.get_bind()
    event.listen(engine, 'before_cursor_execute', synchronize)
    try:
        with ThreadPoolExecutor(max_workers=2) as executor:
            results = list(executor.map(lambda unused: check(), [0, 1]))
    finally:
        event.remove(engine, 'before_cursor_execute', synchronize)
    assert sum(len(result['alerts']) for result in results) == 1
    assert check()['alerts'] == []
    with artifact_session(scope) as session:
        alerts = monitors.recorded_monitor_alerts(session, scope)
        assert len(alerts) == 1 and alerts[0]['id']
