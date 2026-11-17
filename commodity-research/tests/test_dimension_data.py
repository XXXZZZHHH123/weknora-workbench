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


@pytest.mark.parametrize('frequency', ['日', '周', '月'])
@pytest.mark.parametrize('period,latest', [('daily', '2024-03-28'), ('weekly', '2024-03-22')])
def test_no_observation_after_report_window_start(frequency, period, latest):
    item = card([('2023-09-01', 10), (latest, 15)], frequency=frequency, period=period)
    assert item['direction'] == item['update_status'] == 'no_update'
    assert item['update_notice'] == f'本期无新数据（最新 {latest}）'
    assert item['comparison_start_date'] == latest
    assert item['position'] and item['freshness']
    # Existing deterministic previous-period arithmetic stays available to
    # consumers even when there was no new observation in the report window.
    assert item['change'] == (0 if frequency != '月' and period == 'weekly' else 5)


def test_same_observation_is_no_update_and_retains_staleness():
    item = card([('2023-09-15', 10), ('2023-09-22', 15)])
    assert item['date'] == item['previous_date'] == '2023-09-22'
    assert item['change'] == item['change_pct'] == 0
    assert item['direction'] == 'no_update' and item['freshness']['stale']
    assert item['update_notice'] == '本期无新数据（最新 2023-09-22）'


def test_same_date_baseline_is_no_update_even_inside_report_window():
    update = data.observation_update('2024-03-29', '2024-03-29', date(2024, 3, 29), 'daily')
    assert update['update_status'] == 'no_update'
    assert update['update_notice'] == '本期无新数据（最新 2024-03-29）'


@pytest.mark.parametrize('frequency', ['日', '周', '月'])
@pytest.mark.parametrize('period', ['daily', 'weekly'])
def test_new_observation_with_same_value_is_flat(frequency, period):
    item = card([('2024-03-01', 15), ('2024-03-21', 15), ('2024-03-29', 15)],
                frequency=frequency, period=period)
    assert item['direction'] == 'flat' and item['update_status'] == 'updated'
    assert item['update_notice'] == ''


def test_no_update_without_comparison_observation():
    item = card([('2023-09-22', 15)], frequency='月')
    assert item['previous'] is None and item['change'] is None
    assert item['direction'] == 'no_update'
    assert 'baseline_unavailable' in item['warnings']


def test_indicator_card_uses_readable_catalog_name_without_mutating_metadata():
    field = {**FIELDS[SID], 'name': '可读指标 / ID123456 [SOURCE.EXAMPLE.value]',
             'metric': '原始字段表头', 'sheet': '真实工作表'}
    item = data.indicator_data(SID, field, {'unit': '件', 'points': [
        {'date': '2024-03-28', 'value': 10}, {'date': '2024-03-29', 'value': 15}]},
        date(2024, 3, 29), 'weekly')
    assert item['name'] == '可读指标'
    assert item['sheet'] == '真实工作表'
    assert field['name'] == '可读指标 / ID123456 [SOURCE.EXAMPLE.value]'
    assert field['metric'] == '原始字段表头'


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
    assert two['direction'] == 'no_update' and two['change'] == -5
    assert output['dimensions'][0]['summary'] == {'up': 1, 'down': 0, 'flat': 0, 'no_update': 1, 'extreme': 0,
        'extreme_high': 0, 'extreme_low': 0, 'unavailable': 0}


def test_summary_counts_no_update_separately_from_flat(chart_client):
    client, state = chart_client
    state.fields[THIRD]['unit'] = '件'
    state.observations[SID] = [('2023-09-15', 10), ('2023-09-22', 15)]
    state.observations[OTHER] = [('2024-03-21', 20), ('2024-03-29', 20)]
    state.observations[THIRD] = [('2024-03-21', 20), ('2024-03-29', 25)]
    bind(state, dimensions(*({'kind': 'indicator', 'id': sid} for sid in (SID, OTHER, THIRD))))
    response = client.get('/dimension-data-test?day=2024-03-29')
    assert response.status_code == 200, response.text
    summary = response.json()['dimensions'][0]['summary']
    assert summary['no_update'] == summary['flat'] == summary['up'] == 1
    assert summary['down'] == summary['unavailable'] == 0


def test_each_batch_budget_does_not_shrink_history_with_page_size(chart_client):
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
    assert all(len(body['series_ids']) * body['points_per_series'] <= 20000 for body in reads)
    assert all(body['points_per_series'] == 2000 for body in reads)
    output = result.json()
    assert output['point_limit'] == 2000
    assert output['point_budget'] == 300 * 2000
    assert output['batch_point_budget'] == 20000 and output['batch_series_limit'] == 256
    assert len(reads) == 30


@pytest.mark.parametrize('frequency,interval', [('日度', 1), ('weekly', 7), ('月', 30),
                                               ('季度', 91), ('年', 365), ('未知', 1)])
def test_frequency_sized_reads_retain_five_real_comparable_cycles(chart_client, frequency, interval):
    client, state = chart_client
    state.fields[SID]['frequency'] = frequency
    day = date(2024, 3, 29)
    state.observations[SID] = [((day - timedelta(days=interval * index)).isoformat(), index)
                               for index in reversed(range(4000 // interval + 1))]
    bind(state, dimensions({'kind': 'indicator', 'id': SID}))
    response = client.get('/dimension-data-test?day=2024-03-29')
    assert response.status_code == 200, response.text
    output = response.json()
    item = output['dimensions'][0]['sections'][0]['items'][0]
    assert item['position']['n'] == 5 and item['position']['rank'] is not None
    assert not item['history_coverage']['limited'] and not output['history_coverage_limited']
    reads = [body for _, body in state.requests if 'points_per_series' in body]
    assert len(reads) == 1 and 26 <= reads[0]['points_per_series'] <= 2000
    assert (day - date.fromisoformat(item['history_coverage']['start_date'])).days > 5 * 365


def test_mixed_frequencies_use_independent_limits_and_keep_chart_window(chart_client):
    client, state = chart_client
    state.fields[SID]['frequency'] = '月'
    state.fields[OTHER]['frequency'] = '周度'
    state.fields[THIRD]['frequency'] = 'monthly'
    chart = create(client)
    bind(state, dimensions(*({'kind': 'indicator', 'id': sid} for sid in (SID, OTHER, THIRD)),
                           {'kind': 'chart', 'id': chart['id'], 'revision': 1}))
    state.requests.clear()
    response = client.get('/dimension-data-test?day=2023-01-04')
    assert response.status_code == 200, response.text
    reads = [body for _, body in state.requests if 'points_per_series' in body]
    assert {tuple(body['series_ids']): body['points_per_series'] for body in reads} == {
        (SID,): 2000, (OTHER,): 316, (THIRD,): 81}
    assert all(len(body['series_ids']) * body['points_per_series'] <= 20000 for body in reads)
    output = response.json()
    assert output['point_budget'] == 2000 + 316 + 81
    cards = output['dimensions'][0]['sections'][0]['items']
    assert [item['point_limit'] for item in cards[:3]] == [2000, 316, 81]
    assert cards[3]['payload']['point_limit'] == 120  # The saved recent-chart display window stays intact.


def test_truncation_notice_only_when_read_window_affects_five_prior_cycles():
    points = [((date(2018, 10, 1) + timedelta(days=index)).isoformat(), index) for index in range(2000)]
    complete = card(points, limited=True)
    assert complete['limited'] and 'history_limited' in complete['warnings']
    assert complete['position']['n'] == 5
    assert not complete['history_coverage']['limited']
    assert complete['history_coverage']['available_cycles'] == 5
    truncated = card(points[-400:], limited=True)
    assert truncated['history_coverage']['limited']
    assert truncated['position']['n'] < 3
    # A genuinely short source must not be described as a budget shortfall.
    short_source = card(points[-400:])
    assert not short_source['history_coverage']['limited']


def test_second_batch_source_version_change_rejects_entire_request(chart_client, monkeypatch):
    client, state = chart_client
    identifiers = ['kb-' + f'{index:048x}' for index in range(12)]
    for identifier in identifiers:
        state.fields[identifier] = {**FIELDS[SID], 'series_id': identifier, 'frequency': '日度'}
        state.observations[identifier] = [('2023-01-01', 1), ('2023-01-02', 2)]
    original = data.read_series
    calls = []

    def change_after_first_batch(*args, **kwargs):
        result = original(*args, **kwargs)
        calls.append(args[0])
        state.facts_version = 'a' * 64
        return result

    monkeypatch.setattr(data, 'read_series', change_after_first_batch)
    bind(state, dimensions(*({'kind': 'indicator', 'id': sid} for sid in identifiers)))
    response = client.get('/dimension-data-test?day=2023-01-02')
    assert response.status_code == 409, response.text
    assert len(calls) == 2


def test_single_series_failure_leaves_later_batches_available(chart_client, monkeypatch):
    client, state = chart_client
    identifiers = ['kb-' + f'{index:048x}' for index in range(12)]
    for identifier in identifiers:
        state.fields[identifier] = {**FIELDS[SID], 'series_id': identifier}
        state.observations[identifier] = [('2023-01-01', 1), ('2023-01-02', 2)]
    broken = identifiers[3]
    original = data.read_series

    def fail_one(ids, *args, **kwargs):
        if broken in ids:
            raise HTTPException(503, '隔离单条来源故障')
        return original(ids, *args, **kwargs)

    monkeypatch.setattr(data, 'read_series', fail_one)
    bind(state, dimensions(*({'kind': 'indicator', 'id': sid} for sid in identifiers)))
    response = client.get('/dimension-data-test?day=2023-01-02')
    assert response.status_code == 200, response.text
    output = response.json()
    assert [item['id'] for item in output['failed']] == [broken]
    cards = output['dimensions'][0]['sections'][0]['items']
    assert [item['id'] for item in cards] == identifiers
    assert sum(item['status'] == 'ok' for item in cards) == 11
    assert cards[-1]['status'] == 'ok'


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
    assert item['direction'] == 'no_update'


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
