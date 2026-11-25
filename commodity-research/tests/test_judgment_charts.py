from datetime import date

from fastapi import HTTPException
import httpx
from lxml.html import fromstring
import pytest
from sqlalchemy import select

from tests.test_knowledge_facts import canonical, FIRST, SECOND, SERIES, page
from tests.test_canonical_research_results import save_results
from tests.test_restricted_views import delegated
from tests.test_workbench_sources_v2 import source
from tin.integration import workbench_scope
from tin.integration.identity import Identity
from tin.integration.workbench_scope import isolated_session_factory
from tin.judgments.service import save_version
from tin.models import Judgment
from tin.provenance import bridge
from tin.schemas.judgment import JudgmentIn


PERIOD, OTHER_DATE = ['kb-' + letter * 48 for letter in ('b', 'c')]


@pytest.fixture
def mixed_judgment(canonical, monkeypatch):
    client, headers, _, _ = canonical
    binding = source(1)
    kb = binding['knowledge_base_id']
    metadata = {'id': FIRST, 'name': 'Synthetic workbench', 'workspace_mode': 'isolated',
        'can_read_source': True, 'can_edit': True}

    def scope(identity, authorization):
        return workbench_scope.WorkbenchScope(identity, FIRST, kb, 17, metadata, authorization,
            scope_version=2, sources_revision=7, sources=(binding,))

    async def resolve(identity, identifier, authorization):
        assert identifier == FIRST
        return scope(identity, authorization)

    monkeypatch.setattr(workbench_scope, 'resolve_workbench', resolve)
    fields = {sid: {'series_id': sid, 'metric': name, 'date_mode': mode, 'unit': '吨',
        'frequency': frequency, 'source_series_id': sid[-1], 'source_knowledge_id': 'source-one',
        'dataset_id': 'd' * 24, 'source_binding_id': binding['id'], 'source_id': kb,
        'source_type': 'knowledge_base', 'knowledge_base_id': kb, 'knowledge_base_tenant_id': 17}
        for sid, name, mode, frequency in ((PERIOD, '年度面积', 'period', '年'),
            (SERIES, '日度库存', 'date', '日'), (OTHER_DATE, '日度流量', 'date', '日'))}
    owner = Identity('researcher', 'researcher@test', 'Researcher', 17, 'owner')
    with workbench_scope.artifact_session(scope(owner, '')) as session:
        session.info.update(knowledge_facts=True, knowledge_facts_catalog=fields)
        record = save_version(session, JudgmentIn(variety='SN', author=owner.user_id, written_at=date(2026, 1, 2),
            contradiction={'bull': {'claim': 'First interpretation', 'evidence': [
                {'desc': 'Annual evidence', 'series_id': PERIOD}, {'desc': 'Daily inventory', 'series_id': SERIES},
                {'desc': 'Daily flow', 'series_id': OTHER_DATE}]}, 'bear': {'claim': 'Second interpretation',
                'evidence': [{'desc': 'Shared annual evidence', 'series_id': PERIOD}]}},
            thresholds=[{'id': 'annual_threshold', 'name': '年度面积阈值', 'series_id': PERIOD, 'operator': '>',
                'value': 10, 'type': '观察位', 'horizon': '短期'}],
            falsifiers=[{'id': 'daily_change', 'desc': '日度库存增加', 'series_id': SERIES, 'side': 'bull',
                'expression': 'change > 0', 'unit_confirmed': '吨'}]), actor=owner.user_id)
        session.commit()

    calls = []

    def facts(method, path, body, **kwargs):
        assert method == 'POST' and path == '/api/research/facts'
        calls.append(body)
        assert body['source_bindings'] == [binding['id']]
        assert body['view_module'] == 'judgments' and body['include_invalid'] is True
        identifiers = body['series_ids']
        rows = [{'series_id': sid, 'date': day, 'value': value, 'value_status': 'valid'}
                for sid in identifiers if sid != PERIOD
                for day, value in (('2026-01-01', 12), ('2026-01-02', 15))]
        return httpx.Response(200, request=httpx.Request(method, 'https://fixture.invalid' + path), json={
            'scope_version': 2, 'workbench_id': FIRST, 'sources_revision': 7, 'version': 'f' * 64,
            'source_versions': {binding['id']: 'e' * 64}, 'bounded': True, 'include_invalid': True,
            'indicators': [fields[sid] for sid in identifiers], 'rows': rows,
            'undated_series_ids': [PERIOD] if PERIOD in identifiers else [], 'unavailable_series_ids': []})

    monkeypatch.setattr(bridge, 'structured_request', facts)
    return client, headers, record.id, calls


def saved(user='researcher'):
    save_results(FIRST, user)
    with isolated_session_factory(17, FIRST)() as session:
        return session.scalar(select(Judgment.id).where(Judgment.author == user))


def query(identifier, **changes):
    return {'judgment_id': identifier, 'series_id': SERIES, 'date': '2026-01-02',
            'limit': 121, **changes}


def test_saved_bound_series_chart_is_bounded_and_read_only(canonical):
    client, headers, state, _ = canonical
    identifier = saved()
    response = client.get(page(FIRST, '/api/sn/judgment/chart'), headers=headers, params=query(identifier))
    assert response.status_code == 200, response.text
    metric = response.json()['metric']
    assert metric['series_id'] == SERIES
    assert len(metric['timeline']) <= 121
    assert state['bounded_requests'][-1]['include_invalid'] is True
    assert state['bounded_requests'][-1]['points_per_series'] == 121
    assert state['bounded_requests'][-1]['series_ids'] == [SERIES]
    assert all(stamp['date'][:10] <= '2026-01-02' for stamp in metric['timeline'])
    assert 'source_note' not in response.text


def test_mixed_period_and_dated_judgment_renders_only_dated_trends(mixed_judgment):
    client, headers, identifier, calls = mixed_judgment
    response = client.get(page(FIRST, '/sn/judgment'), headers=headers,
        params={'judgment_id': identifier, 'date': '2026-01-02'})
    assert response.status_code == 200, response.text
    assert len(calls) == 1 and set(calls[0]['series_ids']) == {PERIOD, SERIES, OTHER_DATE}
    html = fromstring(response.text)
    annual = html.get_element_by_id('judgment-evidence-bull0')
    compact = html.get_element_by_id('judgment-evidence-bear0')
    for item in (annual, compact):
        assert '年度面积' in item.text_content()
        assert '按期间记录，暂无日期走势' in item.text_content()
        assert not item.xpath('.//*[@data-metric-trend or @data-chart-ma or @data-chart-view or @data-compact-preview]')
    assert '无法按日期判断' in html.get_element_by_id('judgment-threshold-annual_threshold').text_content()
    for sid in (SERIES, OTHER_DATE):
        chart, = html.xpath(f'//*[@data-chart-series="{sid}"]')
        assert chart.xpath('.//*[@data-metric-trend]')
        assert chart.xpath('.//*[@data-chart-ma]')
        assert chart.xpath('.//*[@data-chart-view="cycle"]')


def test_undated_chart_endpoint_returns_no_observation_window(mixed_judgment):
    client, headers, identifier, _ = mixed_judgment
    response = client.get(page(FIRST, '/api/sn/judgment/chart'), headers=headers,
        params=query(identifier, series_id=PERIOD))
    assert response.status_code == 200, response.text
    metric = response.json()['metric']
    assert metric['undated'] is True
    assert metric['timeline'] == metric['segments'] == metric['boundaries'] == []


def test_chart_rejects_unbound_series_before_reading_observations(canonical):
    client, headers, state, _ = canonical
    identifier = saved()
    before = len(state['calls'])
    response = client.get(page(FIRST, '/api/sn/judgment/chart'), headers=headers,
                          params=query(identifier, series_id='kb-' + 'b' * 48))
    assert response.status_code == 403, response.text
    assert not any(path == '/api/research/facts' for _, path in state['calls'][before:])


def test_chart_never_reads_other_users_or_workbenches(canonical):
    client, headers, state, _ = canonical
    identifier = saved('another-user')
    assert client.get(page(FIRST, '/api/sn/judgment/chart'), headers=headers,
                      params=query(identifier)).status_code == 404
    own = saved()
    assert client.get(page(SECOND, '/api/sn/judgment/chart'), headers=headers,
                      params=query(own)).status_code == 404


def test_chart_validates_window_date_and_unexpected_parameters(canonical):
    client, headers, state, _ = canonical
    identifier = saved()
    for values in [query(identifier, limit=122), query(identifier, limit=1),
                   query(identifier, date='2026-1-2'), query(identifier, date='2999-01-01'),
                   query(identifier, unexpected='arbitrary-query')]:
        assert client.get(page(FIRST, '/api/sn/judgment/chart'), headers=headers,
                          params=values).status_code == 422


def test_shared_chart_only_reads_owner_bindings_with_page_permission(canonical):
    client, headers, state, _ = canonical
    identifier = saved()
    delegated(state, ('judgments',))
    visitor = {**headers, 'Authorization': 'Bearer source-viewer'}
    parameters = query(identifier)
    parameters.pop('date')
    response = client.get(page(FIRST, '/api/sn/judgment/chart'), headers=visitor,
                          params=parameters)
    assert response.status_code == 200, response.text
    assert response.json()['metric']['series_id'] == SERIES
    assert client.get(page(FIRST, '/api/sn/judgment/chart'), headers=visitor,
                      params=query(identifier)).status_code == 403
    delegated(state, ('market',))
    assert client.get(page(FIRST, '/api/sn/judgment/chart'), headers=visitor,
                      params=parameters).status_code == 403


def test_chart_keeps_missing_dates_and_zero_values():
    from tin.web.judgment_charts import _metric
    data = {'indicators': [{'series_id': SERIES, 'metric': 'Synthetic inventory', 'unit': 'kg', 'frequency': '日'}],
            'rows': [{'series_id': SERIES, 'date': '2026-01-01', 'value': 0, 'value_status': 'valid'},
                     {'series_id': SERIES, 'date': '2026-01-02', 'value': None, 'value_status': 'missing'}]}
    result = _metric(data, SERIES, date(2026, 1, 2), 121)
    assert [point['value'] for point in result['timeline']] == [0, None]
    assert result['streak'] is None


@pytest.mark.parametrize('change', [
    {'series_id': 'kb-' + 'b' * 48}, {'date': '2026-01-03'}, {'date': '2026-1-2'},
    {'value': True}, {'value': float('inf')}, {'value_status': 'unexpected'}])
def test_chart_rejects_out_of_scope_or_malformed_source_points(change):
    from tin.web.judgment_charts import _metric
    point = {'series_id': SERIES, 'date': '2026-01-02', 'value': 7, 'value_status': 'valid', **change}
    data = {'indicators': [{'series_id': SERIES, 'metric': 'Synthetic'}], 'rows': [point]}
    with pytest.raises(HTTPException) as error:
        _metric(data, SERIES, date(2026, 1, 2), 121)
    assert error.value.status_code == 503
