"""An explicit warning check reads saved rule windows without creating tasks."""
from contextlib import contextmanager
from dataclasses import replace
from datetime import date, datetime

from fastapi import HTTPException
import httpx
import pytest
from sqlalchemy import event, func, select

from tests.test_judgment_charts import mixed_judgment
from tests.test_judgment_panels import panels
from tests.test_knowledge_facts import FIRST, canonical, page
from tests.test_structured_judgment_import import payload
from tin.config import SHANGHAI
from tin.integration import page_facts, workbench_scope
from tin.judgments.service import save_version
from tin.models import Indicator, Observation, ReviewTask, Signal
from tin.schemas.judgment import JudgmentIn


def test_empty_warning_check_never_requests_a_source(panels, monkeypatch):
    from tin.web.affairs_check import check_current
    _client, state = panels
    scope = replace(state['scope'], scope_version=2)
    def forbidden(*args, **kwargs):
        raise AssertionError('An empty workbench must not scan its source')
    monkeypatch.setattr(workbench_scope, 'workbench_session', forbidden)
    result = check_current(scope)
    assert result['alerts'] == [] and result['checked_rules'] == 0
    assert '尚未' in result['notice']


def test_warning_check_uses_saved_rule_ids_and_is_readonly(panels, monkeypatch):
    from tin.web.affairs_check import check_current
    _client, state = panels
    scope = replace(state['scope'], scope_version=2)
    document = payload()
    identifier = document['whitelist'][0]
    document['falsifiers'] = [{'id': 'fixture_warning', 'desc': 'Measured condition',
        'series_id': identifier, 'expression': 'value > 116.58', 'unit_confirmed': '吨',
        'consecutive_periods': 2, 'side': 'bull'}]
    with workbench_scope.artifact_session(scope) as session:
        session.info.update(knowledge_facts=True, knowledge_facts_catalog={identifier: {'unit': '吨'}})
        row = save_version(session, JudgmentIn.model_validate(document), actor=scope.identity.user_id)
        row.status = '生效'
        judgment_id = row.id
        session.add(Indicator(series_id=identifier, name='A real fixture series', unit='吨', variety='XY',
            category='fixture', frequency='日', source='Fixture', caliber={}, fetch_mode='readonly'))
        session.flush()
        for day, value in [(1, 117.2), (2, 116.58000000000001)]:
            session.add(Observation(series_id=identifier, as_of=datetime(2026, 1, day, tzinfo=SHANGHAI),
                value=value, status='正常', revision=0, source='Fixture', caliber_snapshot={},
                fetched_at=datetime(2026, 1, 2, tzinfo=SHANGHAI)))
        session.commit()
        engine = session.get_bind()
    requests, writes = [], []
    @contextmanager
    def bounded(received):
        assert received == scope
        requests.append(dict(page_facts._page.get()))
        with workbench_scope.artifact_session(scope) as session:
            session.info['knowledge_facts_display_precision'] = {identifier: {'2026-01-01': 2}}
            yield session
    def watch(_conn, _cursor, sql, *_):
        if sql.lstrip().upper().startswith(('INSERT', 'UPDATE', 'DELETE')):
            writes.append(sql)
    monkeypatch.setattr(workbench_scope, 'workbench_session', bounded)
    event.listen(engine, 'before_cursor_execute', watch)
    try:
        result = check_current(scope, day=date(2026, 1, 2))
    finally:
        event.remove(engine, 'before_cursor_execute', watch)
    assert not writes
    assert requests == [{'module': 'judgments', 'series_ids': None, 'day': '2026-01-02',
        'template_id': None, 'points': None, 'judgment_id': judgment_id}]
    assert result['checked_rules'] == 1
    assert result['alerts'][0]['kind'] == 'triggered'
    assert result['alerts'][0]['trade_date'] == '2026-01-02'
    assert result['alerts'][0]['detail'] == '2026-01-01 实际值 117.20；2026-01-02 实际值 116.58'
    assert 'judgment_id=' + str(judgment_id) in result['alerts'][0]['href']
    with workbench_scope.artifact_session(scope) as session:
        assert session.scalar(select(func.count()).select_from(Signal)) == 0
        assert session.scalar(select(func.count()).select_from(ReviewTask)) == 0


def test_warning_check_routes_isolate_undated_series_and_keep_upstream_error_details(mixed_judgment, monkeypatch):
    client, headers, _, calls = mixed_judgment
    url = page(FIRST, '/api/sn/affairs/check')
    response = client.post(url, headers=headers)
    assert response.status_code == 200, response.text
    assert len(calls) == 1
    result = response.json()
    assert result['checked_rules'] == 2
    alerts = {alert['kind']: alert for alert in result['alerts']}
    assert alerts['triggered']['title'] == '日度库存增加 · 触发'
    assert alerts['undated']['title'] == '年度面积阈值 · 无法按日期判断'
    assert alerts['undated']['evidence_dates'] == []

    upstream_detail = '来源版本已变化，请刷新后重新检查'
    def transport(method, endpoint, body, **kwargs):
        assert body['view_module'] == 'judgments' and body['include_invalid'] is True
        request = httpx.Request(method, 'http://fixture.invalid' + endpoint)
        return httpx.Response(409, json={'detail': upstream_detail}, request=request)
    monkeypatch.setattr('tin.provenance.bridge.structured_request', transport)
    response = client.post(url, headers=headers)
    assert response.status_code == 409 and response.json()['detail'] == upstream_detail


def test_warning_check_does_not_expand_restricted_page_grants(panels):
    from tin.web.affairs_check import check_current
    _client, state = panels
    denied = replace(state['scope'], source_binding_id='unreadable',
        metadata={**state['scope'].metadata, 'can_read_source': False})
    with pytest.raises(HTTPException) as error:
        check_current(denied)
    assert error.value.status_code == 403


def test_archived_judgment_cannot_start_a_new_warning_check(panels, monkeypatch):
    from tin.web.affairs_check import check_current
    _client, state = panels
    scope = replace(state['scope'], scope_version=2)
    document = payload()
    document['falsifiers'] = [{'id': 'archived_condition', 'desc': 'An archived condition',
        'series_id': document['whitelist'][0], 'expression': 'value > 2',
        'unit_confirmed': '吨', 'consecutive_periods': 1, 'side': 'bull'}]
    with workbench_scope.artifact_session(scope) as session:
        session.info.update(knowledge_facts=True,
            knowledge_facts_catalog={document['whitelist'][0]: {'unit': '吨'}})
        row = save_version(session, JudgmentIn.model_validate(document), actor=scope.identity.user_id)
        row.status = '已归档'
        session.commit()
    def forbidden(*args, **kwargs):
        raise AssertionError('An archived rule must not request facts')
    monkeypatch.setattr(workbench_scope, 'workbench_session', forbidden)
    result = check_current(scope)
    assert result['checked_rules'] == 0 and result['alerts'] == []
