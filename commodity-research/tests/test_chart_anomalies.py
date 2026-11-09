"""Synthetic cycle rankings through the real chart router and bounded projection."""
from copy import deepcopy
from datetime import date, timedelta

from fastapi import HTTPException
import pytest

from tin.integration.workbench_scope import isolated_session_factory
from tin.models.workbench import WorkbenchArtifact
from tin.web import charts
from tests.chart_fixtures import FIELDS, SID, OTHER, THIRD
from tests.test_chart_api import chart_client, create, SECOND_WB
from tests.test_workbench_sources_v2 import WB


def cycle_input(state, sid=SID, *, peers=(10, 20, 30, 40, 50, 60), current=(35, 35)):
    """Two observations per short explicit period, with no imputation or sparsity."""
    periods = [(value, value) for value in peers] + [current]
    segments, points = [], []
    for index, values in enumerate(periods):
        start = date(2020, 1, 1) + timedelta(days=14 * index)
        segments.append({'label': '样本' + str(index), 'start': start.isoformat(),
                         'end': (start + timedelta(days=1)).isoformat()})
        points.extend(((start + timedelta(days=offset)).isoformat(), value)
                      for offset, value in enumerate(values))
    state.fields[sid] = {**FIELDS[SID], 'series_id': sid, 'metric': '真实指标 <来源>'}
    state.observations[sid] = points
    return {'mode': 'cycle', 'template': {'kind': 'segments', 'name': '合成周期', 'segments': segments},
            'cycles': len(periods)}, points[-1][0]


def anomalies(client, day, **params):
    response = client.get('/api/sn/charts/anomalies', params={'date': day, **params})
    assert response.status_code == 200, response.text
    return response.json()


@pytest.mark.parametrize('peers', [(), (10,), (10, 20)])
def test_fewer_than_three_valid_past_cycles_are_excluded(chart_client, peers):
    client, state = chart_client
    time, day = cycle_input(state, peers=peers, current=(99, 0))
    create(client, time=time)
    data = anomalies(client, day)
    assert data['items'] == [] and data['total'] == 0 and data['failed'] == []


@pytest.mark.parametrize('value, rank, reason', [
    (70, 1, 'extreme_high'), (55, 2, 'extreme_high'),
    (15, 6, 'extreme_low'), (5, 7, 'extreme_low'),
])
def test_top_and_bottom_two_and_returned_statistics(chart_client, value, rank, reason):
    client, state = chart_client
    time, day = cycle_input(state, current=(value, value))
    saved = create(client, time=time)
    data = anomalies(client, day)
    assert data['total'] == 1 and data['failed'] == []
    item, = data['items']
    assert item['id'] == saved['id'] and item['title'] == saved['title'] and item['revision'] == 1
    assert item['series_id'] == SID and item['metric'] == '真实指标 <来源>'
    assert item['date'] == day and item['value'] == value
    assert (item['rank'], item['n']) == (rank, 6)
    assert (item['previous_rank'], item['previous_n']) == (rank, 6)
    assert item['previous_date'] == (date.fromisoformat(day) - timedelta(days=1)).isoformat()
    assert item['rank_change'] == 0 and item['reasons'] == [reason]
    assert item['share_below'] == sum(peer < value for peer in (10, 20, 30, 40, 50, 60)) / 6
    assert item['recent_mean'] == 40 and item['recent_cycles'] == 5
    assert item['vs_recent_mean'] == {'kind': 'pct', 'value': (value - 40) / 40, 'cycles': 5}


@pytest.mark.parametrize('value, rank, reason', [(25, 2, 'extreme_high'), (15, 3, 'extreme_low')])
def test_exactly_three_past_cycles_can_be_included(chart_client, value, rank, reason):
    client, state = chart_client
    time, day = cycle_input(state, peers=(10, 20, 30), current=(value, value))
    create(client, time=time)
    item, = anomalies(client, day)['items']
    assert item['rank'] == rank and item['n'] == 3 and item['reasons'] == [reason]


@pytest.mark.parametrize('previous, current, change, included', [
    (55, 35, 2, True), (15, 35, -2, True), (15, 45, -3, True),
    (45, 35, 1, False), (35, 45, -1, False), (35, 35, 0, False),
])
def test_rank_change_boundary_without_extreme_current_rank(chart_client, previous, current, change, included):
    client, state = chart_client
    time, day = cycle_input(state, current=(previous, current))
    create(client, time=time)
    data = anomalies(client, day)
    assert bool(data['items']) == included
    if included:
        assert data['items'][0]['reasons'] == ['rank_change']
        assert data['items'][0]['rank_change'] == change


def test_previous_observation_without_usable_rank_does_not_block_extreme(chart_client):
    client, state = chart_client
    time, day = cycle_input(state, peers=(10, 20, 30), current=(99,))
    create(client, time=time)
    item, = anomalies(client, day)['items']
    assert item['reasons'] == ['extreme_high'] and item['rank'] == 1
    assert item['previous_rank'] is None and item['previous_n'] == 2 and item['rank_change'] is None
    assert item['previous_date'] == state.observations[SID][-2][0]


def test_missing_previous_observation_is_supported(chart_client, monkeypatch):
    # Exercise the optional previous-observation branch independently of the
    # engine's frequency requirement (a sole observation has no real rank).
    client, state = chart_client
    time, day = cycle_input(state, current=(99,))
    saved = create(client, time=time)
    definition = charts.ChartDefinition.model_validate(saved['definition'])
    series = {'points': [{'date': day, 'value': 99}], 'limited': False, 'name': '合成指标'}
    calls = []

    def position(*args, **kwargs):
        calls.append(args)
        return {'position': {'date': day, 'value': 99, 'rank': 1, 'n': 3, 'share_below': 1,
            'peers': [{'value': value} for value in (10, 20, 30)],
            'vs_recent_mean': {'kind': 'pct', 'value': 3.95, 'cycles': 3}}, 'warnings': []}

    monkeypatch.setattr(charts, 'cycle_result', position)
    with isolated_session_factory(17, WB)() as session:
        row = session.get(WorkbenchArtifact, saved['id'])
        item = charts.anomaly_item(row, definition, state.fields[SID], series, date.fromisoformat(day))
    assert item['previous_date'] is None and item['previous_rank'] is None and item['previous_n'] is None
    assert item['rank_change'] is None and item['reasons'] == ['extreme_high'] and len(calls) == 1


def test_cutoff_uses_latest_observation_and_previous_observation_not_previous_calendar_day(chart_client):
    client, state = chart_client
    time, last_day = cycle_input(state, current=(55, 35))
    create(client, time=time)
    cutoff = (date.fromisoformat(last_day) + timedelta(days=5)).isoformat()
    state.observations[SID].append(((date.fromisoformat(cutoff) + timedelta(days=1)).isoformat(), 999))
    data = anomalies(client, cutoff)
    item, = data['items']
    assert data['as_of'] == cutoff and item['date'] == last_day and item['value'] == 35
    assert item['previous_rank'] == 2 and item['rank'] == 4 and item['rank_change'] == 2


def test_sort_extremes_first_then_absolute_rank_change_and_limit_after_sort(chart_client):
    client, state = chart_client
    saved = []
    for index, values in enumerate(((70, 70), (70, 5), (5, 35), (55, 35))):
        sid = 'kb-' + f'{index:048x}'
        time, day = cycle_input(state, sid, current=values)
        saved.append(create(client, title='图表' + str(index), time=time, curves=[{'series_id': sid, 'label': '指标'}]))
    expected = [saved[index]['id'] for index in (1, 0, 2, 3)]
    assert [item['id'] for item in anomalies(client, day)['items']] == expected
    data = anomalies(client, day, limit=2)
    assert [item['id'] for item in data['items']] == expected[:2] and data['total'] == 4
    assert data['items'][0]['reasons'] == ['extreme_low', 'rank_change']


def test_batch_point_and_metric_budgets_and_shared_metric_deduplication(chart_client):
    client, state = chart_client
    for index in range(23):
        sid = 'kb-' + f'{index:048x}'
        time, day = cycle_input(state, sid, current=(99, 99))
        create(client, title='周期' + str(index), time=time, curves=[{'series_id': sid, 'label': '指标'}])
    create(client, title='复用指标的图表', time=time, curves=[{'series_id': sid, 'label': '同指标'}])
    create(client, title='走势图')
    state.requests.clear()
    data = anomalies(client, day, limit=1)
    assert data['total'] == 24 and data['failed'] == [] and len(data['items']) == 1
    reads = [body for _, body in state.requests if 'points_per_series' in body]
    assert len(reads) == 3
    assert all(len(body['series_ids']) <= 256 and body['points_per_series'] == 2000
               and len(body['series_ids']) * body['points_per_series'] <= 20000 for body in reads)
    fetched = [sid for body in reads for sid in body['series_ids']]
    assert len(fetched) == len(set(fetched)) == 23


def test_single_series_read_failure_is_isolated_even_with_a_shared_batch(chart_client, monkeypatch):
    client, state = chart_client
    records = []
    for sid in (SID, OTHER, THIRD):
        time, day = cycle_input(state, sid, current=(99, 99))
        records.append(create(client, time=time, curves=[{'series_id': sid, 'label': '指标'}]))
    read = charts.read_series

    def failing(identifiers, *args, **kwargs):
        if OTHER in identifiers:
            raise HTTPException(503, '合成来源暂时不可读 <原因>')
        return read(identifiers, *args, **kwargs)

    monkeypatch.setattr(charts, 'read_series', failing)
    data = anomalies(client, day)
    assert {item['id'] for item in data['items']} == {records[0]['id'], records[2]['id']}
    assert data['failed'] == [{'id': records[1]['id'], 'title': records[1]['title'],
                              'reason': '合成来源暂时不可读 <原因>'}]


def test_invalid_definition_missing_metric_and_computation_failure_do_not_hide_good_chart(chart_client, monkeypatch):
    client, state = chart_client
    time, day = cycle_input(state, current=(99, 99))
    good = create(client, time=time)
    invalid = create(client, time=time)
    missing = create(client, time=time, curves=[{'series_id': OTHER, 'label': '旧指标'}])
    broken_time = deepcopy(time)
    broken_time['template']['name'] = '合成计算失败'
    broken = create(client, time=broken_time)
    with isolated_session_factory(17, WB)() as session:
        session.get(WorkbenchArtifact, invalid['id']).body = '{'
        session.commit()
    del state.fields[OTHER]
    compute = charts.compute_cycles

    def failing(points, template, *args, **kwargs):
        if template['name'] == '合成计算失败':
            raise ValueError('合成周期计算失败')
        return compute(points, template, *args, **kwargs)

    monkeypatch.setattr(charts, 'compute_cycles', failing)
    data = anomalies(client, day)
    assert [item['id'] for item in data['items']] == [good['id']]
    failures = {item['id']: item['reason'] for item in data['failed']}
    assert failures == {invalid['id']: '已保存图表格式无效，请核对修订历史',
                        missing['id']: '指标已不在当前来源目录', broken['id']: '合成周期计算失败'}


def test_unit_change_is_a_chart_failure(chart_client, monkeypatch):
    client, state = chart_client
    time, day = cycle_input(state, current=(99, 99))
    saved = create(client, time=time)
    read = charts.read_series

    def changed(*args, **kwargs):
        result = read(*args, **kwargs)
        result[SID]['unit'] = '变化后的单位'
        return result

    monkeypatch.setattr(charts, 'read_series', changed)
    data = anomalies(client, day)
    assert data['items'] == [] and data['failed'][0]['id'] == saved['id']
    assert '单位' in data['failed'][0]['reason']


@pytest.mark.parametrize('headers, status', [({'x-restricted': 'true'}, 403),
    ({'x-scope-version': '1'}, 409), ({'x-local': 'true'}, 409)])
def test_permissions_reject_before_any_data_read(chart_client, headers, status):
    client, state = chart_client
    response = client.get('/api/sn/charts/anomalies', headers=headers)
    assert response.status_code == status and state.requests == []


def test_viewers_share_reads_and_foreign_workbenches_and_deleted_charts_are_excluded(chart_client):
    client, state = chart_client
    time, day = cycle_input(state, current=(99, 99))
    saved = create(client, time=time)
    assert client.get('/api/sn/charts/anomalies', params={'date': day},
        headers={'x-role': 'viewer', 'x-can-edit': 'false', 'x-actor': 'another'}).json()['total'] == 1
    state.requests.clear()
    response = client.get('/api/sn/charts/anomalies', headers={'x-workbench': SECOND_WB})
    assert response.status_code == 200 and response.json()['items'] == [] and state.requests == []
    assert client.delete('/api/sn/charts/' + saved['id'] + '?revision=1').status_code == 200
    assert anomalies(client, day)['total'] == 0


@pytest.mark.parametrize('status', [401, 403, 409])
def test_source_authorization_changes_reject_without_retry(chart_client, monkeypatch, status):
    client, state = chart_client
    time, day = cycle_input(state, current=(99, 99))
    create(client, time=time)
    calls = []

    def revoked(*args, **kwargs):
        calls.append(args)
        raise HTTPException(status, '来源授权或版本已变化')

    monkeypatch.setattr(charts, 'read_series', revoked)
    assert client.get('/api/sn/charts/anomalies', params={'date': day}).status_code == status
    assert len(calls) == 1


def test_date_and_limit_validation_and_limited_window_notice(chart_client, monkeypatch):
    client, state = chart_client
    assert client.get('/api/sn/charts/anomalies?date=2020-1-1').status_code == 400
    assert client.get('/api/sn/charts/anomalies?limit=0').status_code == 422
    assert client.get('/api/sn/charts/anomalies?limit=bad').status_code == 422
    time, day = cycle_input(state, current=(99, 99))
    create(client, time=time)
    read = charts.read_series

    def limited(*args, **kwargs):
        result = read(*args, **kwargs)
        result[SID]['limited'] = True
        return result

    monkeypatch.setattr(charts, 'read_series', limited)
    assert 'observation_window_limited' in anomalies(client, day)['items'][0]['warnings']
