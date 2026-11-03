from datetime import date

from fastapi import HTTPException
import pytest
from sqlalchemy import select

from tests.test_knowledge_facts import canonical, FIRST, SECOND, SERIES, page
from tests.test_canonical_research_results import save_results
from tests.test_restricted_views import delegated
from tin.integration.workbench_scope import isolated_session_factory
from tin.models import Judgment


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
