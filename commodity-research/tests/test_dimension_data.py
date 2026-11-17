"""Synthetic arithmetic plus real chart/projection ACL integration."""
from datetime import date, timedelta
from fastapi import HTTPException
import pytest
from tin.integration.workbench_scope import current_workbench
from tin.web import dimension_data as data
from tests.chart_fixtures import FIELDS, SID, OTHER, THIRD
from tests.test_chart_api import chart_client, create, SECOND_WB


def card(points, *, frequency='日', period='weekly', day='2024-03-29', **changes):
    return data.indicator_data(SID, {**FIELDS[SID], 'frequency': frequency},
        {'unit': '件', 'points': [{'date': stamp, 'value': value} for stamp, value in points],
         'freq_days': 1, 'limited': False, **changes}, date.fromisoformat(day), period)


@pytest.mark.parametrize('period,expected', [('daily', ('2024-03-28', 1)), ('weekly', ('2024-03-21', 4))])
def test_daily_previous_and_weekly_cutoff_minus_seven(period, expected):
    item = card([('2024-03-20', 10), ('2024-03-21', 11), ('2024-03-25', 13),
                 ('2024-03-28', 14), ('2024-03-29', 15)], period=period)
    assert (item['previous_date'], item['change']) == expected
    assert item['change_pct'] == item['change'] / item['previous']
    assert item['baseline']['label'] == '较 ' + expected[0][5:]


@pytest.mark.parametrize('frequency', ['月', '季', '年', 'monthly', 'quarterly', 'annual'])
def test_low_frequency_always_previous(frequency):
    item = card([('2024-01-31', 10), ('2024-02-29', 11), ('2024-03-28', 15)], frequency=frequency)
    assert item['previous_date'] == '2024-02-29' and item['change'] == 4
    assert item['baseline']['label'] == '较上期'


def test_missing_baseline_zero_negative_and_future_values():
    item = card([('2024-03-28', 0), ('2024-03-29', -1), ('2024-03-30', 100)], period='daily')
    assert item['latest'] == -1 and item['change'] == -1 and item['change_pct'] is None
    assert item['direction'] == 'down' and len(item['points']) == 2
    negative = card([('2024-03-28', -2), ('2024-03-29', -1)], period='daily')
    assert negative['change_pct'] == .5
    missing = card([('2024-03-29', 0)])
    assert missing['previous'] is None and missing['change'] is None
    assert missing['display']['latest'] == '0' and missing['display']['change'] == '—'
    assert missing['position']['rank'] is None and '不足3年' in missing['position']['notice']


def test_sparkline_and_reused_staleness():
    points = [((date(2023, 1, 1) + timedelta(days=7 * index)).isoformat(), index) for index in range(40)]
    item = card(points)
    assert len(item['points']) == 26 and item['freshness']['stale']
    assert item['freshness']['usual_interval_days'] == 7


def test_natural_year_position_and_sample_limit():
    points = [(f'{year}-{month:02d}-01', year - 2019) for year in range(2020, 2025) for month in range(1, 13)]
    item = card(points, frequency='月', day='2024-03-01', freq_days=31)
    assert item['position']['rank'] == 1 and item['position']['n'] == 4
    assert item['position']['total'] == 5 and item['position']['text'] == '近 5 年同期第 1 高'
    short = card(points, frequency='月', day='2022-03-01', freq_days=31)
    assert short['position']['n'] == 2 and short['position']['rank'] is None


def dimensions(*items):
    return [{'id': 'supply', 'label': '供给', 'category': '供给', 'description': '',
             'sections': [{'id': 'main', 'label': '主环节', 'items': list(items)}]}]


def bind(state, value):
    @state.app.get('/dimension-data-test')
    def endpoint(day: str | None = None, period: str = 'weekly'):
        return data.dimension_data(current_workbench.get(), value, day, period)


def test_default_latest_and_summary(chart_client):
    client, state = chart_client
    state.observations[OTHER] = [('2023-01-01', 20), ('2023-01-03', 15)]
    bind(state, dimensions({'kind': 'indicator', 'id': SID}, {'kind': 'indicator', 'id': OTHER}))
    response = client.get('/dimension-data-test?period=daily')
    assert response.status_code == 200, response.text
    output = response.json()
    assert output['as_of'] == output['latest_data_date'] == '2023-01-04'
    one, two = output['dimensions'][0]['sections'][0]['items']
    assert one['latest'] == 13 and two['date'] == '2023-01-03'
    assert output['dimensions'][0]['summary'] == {'up': 1, 'down': 1, 'flat': 0, 'extreme': 0,
        'extreme_high': 0, 'extreme_low': 0, 'unavailable': 0}


def test_whole_request_budget_and_batches(chart_client):
    client, state = chart_client
    identifiers = ['kb-' + f'{index:048x}' for index in range(300)]
    for identifier in identifiers:
        state.fields[identifier] = {**FIELDS[SID], 'series_id': identifier}
        state.observations[identifier] = [('2023-01-01', 1), ('2023-01-02', 2)]
    bind(state, dimensions(*({'kind': 'indicator', 'id': sid} for sid in identifiers)))
    result = client.get('/dimension-data-test?day=2023-01-02')
    assert result.status_code == 200, result.text
    reads = [body for _, body in state.requests if 'points_per_series' in body]
    assert len(reads) >= 2 and all(len(body['series_ids']) <= 256 for body in reads)
    assert sum(len(body['series_ids']) * body['points_per_series'] for body in reads) <= 20000
    assert result.json()['point_limit'] == 66


def test_partial_failures_keep_cards(chart_client, monkeypatch):
    client, state = chart_client
    original = data.read_series
    def failing(ids, *args, **kwargs):
        if OTHER in ids:
            raise HTTPException(503, '隔离测试读取失败')
        return original(ids, *args, **kwargs)
    monkeypatch.setattr(data, 'read_series', failing)
    missing = 'kb-' + 'd' * 48
    state.observations[THIRD] = []
    bind(state, dimensions(*({'kind': 'indicator', 'id': sid} for sid in (SID, OTHER, THIRD, missing))))
    result = client.get('/dimension-data-test?day=2023-01-04')
    assert result.status_code == 200, result.text
    output = result.json()
    assert len(output['failed']) == 3
    cards = output['dimensions'][0]['sections'][0]['items']
    assert [item['id'] for item in cards] == [SID, OTHER, THIRD, missing]
    assert cards[0]['status'] == 'ok' and all(item['status'] == 'unavailable' for item in cards[1:])
    assert cards[1]['reason'] == '隔离测试读取失败'


@pytest.mark.parametrize('status', [401, 403, 409])
def test_auth_or_source_change_rejects_read(chart_client, monkeypatch, status):
    client, state = chart_client
    def failing(*args, **kwargs):
        raise HTTPException(status, '权限或来源已变')
    monkeypatch.setattr(data, 'read_series', failing)
    bind(state, dimensions({'kind': 'indicator', 'id': SID}))
    assert client.get('/dimension-data-test').status_code == status


def test_pinned_chart_and_shared_operands(chart_client):
    client, state = chart_client
    saved = create(client, title='原修订标题')
    assert client.patch('/api/sn/charts/' + saved['id'], json={'revision': 1, 'title': '新标题'}).status_code == 200
    bind(state, dimensions({'kind': 'indicator', 'id': SID}, {'kind': 'chart', 'id': saved['id'], 'revision': 1}))
    state.requests.clear()
    result = client.get('/dimension-data-test?day=2023-01-04')
    assert result.status_code == 200, result.text
    indicator, chart = result.json()['dimensions'][0]['sections'][0]['items']
    assert chart['title'] == '原修订标题' and chart['payload']['revision'] == 1
    assert chart['unit'] == '件' and chart['source'] == 'fixture.xlsx'
    assert chart['sources'][0]['id'] == SID and chart['sources'][0]['unit'] == '件'
    assert chart['payload']['curves'][0]['points'][-1]['value'] == indicator['latest']
    reads = [body for _, body in state.requests if 'points_per_series' in body]
    assert len(reads) == 1 and reads[0]['series_ids'] == [SID]


def test_deleted_or_foreign_chart_unavailable(chart_client):
    client, state = chart_client
    saved = create(client)
    bind(state, dimensions({'kind': 'chart', 'id': saved['id'], 'revision': 1}))
    foreign = client.get('/dimension-data-test', headers={'x-workbench': SECOND_WB})
    assert foreign.status_code == 200 and foreign.json()['failed'][0]['id'] == saved['id']
    assert client.request('DELETE', '/api/sn/charts/' + saved['id'], json={'revision': 1}).status_code == 200
    deleted = client.get('/dimension-data-test')
    assert deleted.status_code == 200 and len(deleted.json()['failed']) == 1


def test_restricted_raw_read_blocked(chart_client):
    client, state = chart_client
    bind(state, dimensions({'kind': 'indicator', 'id': SID}))
    assert client.get('/dimension-data-test', headers={'x-restricted': 'true'}).status_code == 403


def test_explicit_daily_frequency_wins_over_sparse_observation_interval():
    item = card([('2024-01-01', 10), ('2024-02-01', 12), ('2024-03-01', 15)],
                day='2024-03-29', frequency='日', freq_days=31)
    assert item['baseline']['kind'] == 'week_ago'
    assert item['previous_date'] == '2024-03-01'
    assert item['change'] == 0


def test_repeated_chart_curves_keep_chart_library_definition_cap(chart_client):
    client, state = chart_client
    saved = create(client, curves=[{'series_id': SID, 'label': str(index)} for index in range(16)],
                   time={'mode': 'since', 'start': '2020-01-01'})
    state.observations[SID] = [((date(2020, 1, 1) + timedelta(days=index)).isoformat(), index) for index in range(1500)]
    bind(state, dimensions({'kind': 'indicator', 'id': SID}, {'kind': 'chart', 'id': saved['id'], 'revision': 1}))
    result = client.get('/dimension-data-test')
    assert result.status_code == 200, result.text
    indicator, chart = result.json()['dimensions'][0]['sections'][0]['items']
    assert indicator['point_limit'] == 2000
    assert chart['payload']['point_limit'] == 1250
    assert all(len(curve['points']) == 1250 for curve in chart['payload']['curves'])


def test_past_range_reads_its_own_cutoff_window_and_deduplicates_shared_windows(chart_client):
    client, state = chart_client
    time = {'mode': 'range', 'start': '2020-01-01', 'end': '2020-02-01'}
    old = create(client, title='旧范围', time=time)
    same = create(client, title='同一历史窗口', time=time)
    state.observations[SID] = [((date(2020, 1, 1) + timedelta(days=index)).isoformat(), index)
                               for index in range(2300)]
    bind(state, dimensions({'kind': 'indicator', 'id': SID},
                           {'kind': 'chart', 'id': old['id'], 'revision': 1},
                           {'kind': 'chart', 'id': same['id'], 'revision': 1}))
    state.requests.clear()
    response = client.get('/dimension-data-test?day=2026-03-01')
    assert response.status_code == 200, response.text
    result = response.json()
    assert result['failed'] == []
    indicator, past, shared = result['dimensions'][0]['sections'][0]['items']
    assert indicator['date'] == '2026-03-01'
    for chart in (past, shared):
        assert chart['payload']['as_of'] == '2020-02-01'
        assert chart['payload']['curves'][0]['points'][0] == {'date': '2020-01-01', 'value': 0}
        assert chart['payload']['curves'][0]['points'][-1] == {'date': '2020-02-01', 'value': 31}
    reads = [body for _, body in state.requests if 'points_per_series' in body]
    assert len(reads) == 2
    assert {body['as_of'] for body in reads} == {'2020-02-01', '2026-03-01'}
    assert all(body['series_ids'] == [SID] for body in reads)
    assert sum(len(body['series_ids']) * body['points_per_series'] for body in reads) <= 20000
