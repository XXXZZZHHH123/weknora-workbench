"""Condition semantics, as-of calendars and bounded real source projections."""
from datetime import date, timedelta
from decimal import Decimal
from time import perf_counter

from fastapi import HTTPException
import pytest

from tin.compute.condition_backtest import (
    backtest_conditions, condition_period_states, evaluate_window, validate_rules)
from tin.integration.workbench_scope import current_workbench
from tests.chart_fixtures import SID, OTHER
from tests.test_chart_api import chart_client  # noqa: F401


def rule(identifier='one', **changes):
    return {'series_id': identifier, 'metric': 'value', 'operator': '>', 'value': 1,
            'consecutive_periods': 1, 'label': 'Synthetic', 'definition': 'Synthetic threshold', **changes}


def history(*points, **metadata):
    return {'points': [{'date': day, 'value': value} for day, value in points], **metadata}


def run(rules, series, logic='all', start='2026-01-01', end='2026-01-10'):
    return backtest_conditions(rules, logic, series, start, end)


@pytest.mark.parametrize('change', [
    {'metric': 'rate'}, {'operator': '='}, {'value': True}, {'value': float('nan')},
    {'value': '2'}, {'consecutive_periods': True}, {'consecutive_periods': 0},
    {'consecutive_periods': 121}, {'series_id': ''},
])
def test_rule_validation_rejects_invalid_structure(change):
    with pytest.raises(ValueError):
        validate_rules([rule(**change)], 'all')


@pytest.mark.parametrize('rules,logic', [([], 'all'), ([rule()] * 7, 'all'), ([rule()], 'xor')])
def test_rule_and_logic_bounds(rules, logic):
    with pytest.raises(ValueError):
        validate_rules(rules, logic)


@pytest.mark.parametrize('metric', ['value', 'change'])
@pytest.mark.parametrize('comparison', ['>', '>=', '<', '<='])
@pytest.mark.parametrize('periods', [1, 2, 4])
def test_shared_rolling_and_window_evaluation_are_identical(metric, comparison, periods):
    values = [0.1, 0.2, 0.3, None, 0.4, 0.5, 0.2, 0.7, 0.8]
    rolling = condition_period_states(metric, comparison, Decimal('0.1'), values, periods)
    for index, state in enumerate(rolling):
        window = evaluate_window(metric, comparison, Decimal('0.1'), values[:index + 1], periods)
        assert state['triggered'] is window['triggered']
        assert state['quantity'] == window['current_value']


def test_change_is_exact_adjacent_difference_and_consecutive_actual_periods():
    data = {'one': history(('2026-01-01', 0.1), ('2026-01-08', 0.2), ('2026-01-15', 0.3),
                           ('2026-01-22', 0.4), ('2026-01-29', 0.3), ('2026-02-05', 0.4),
                           ('2026-02-12', 0.5))}
    result = run([rule(metric='change', operator='>=', value=0.1, consecutive_periods=2)],
                 data, end='2026-02-12')
    assert result['trigger_dates'] == ['2026-01-15', '2026-02-12']
    assert result['trigger_count'] == 2 and result['last_trigger'] == '2026-02-12'
    assert result['intervals'][0] == {'start': '2026-01-15', 'end': '2026-01-28',
        'duration_periods': 2, 'duration_days': 14, 'ongoing': False, 'started_before_range': False}
    assert result['current_state'] == '触发'
    assert result['series'][0]['quantities'][1]['value'] == 0.1
    assert result['daily_states'][13]['state'] == '无数据'
    assert result['daily_states'][14]['state'] == '触发'


def test_missing_observation_breaks_change_window_and_is_never_skipped():
    result = run([rule(metric='change', value=0, consecutive_periods=2)], {'one': history(
        ('2026-01-01', 1), ('2026-01-02', 2), ('2026-01-03', None),
        ('2026-01-04', 3), ('2026-01-05', 4), ('2026-01-06', 5))})
    assert [day['triggered'] for day in result['daily_states'][:6]] == [None, None, None, None, None, True]
    assert result['trigger_dates'] == ['2026-01-06']


@pytest.mark.parametrize('values', [(-1e308, 1e308), (1e308, -1e308)])
def test_finite_observations_with_unrepresentable_change_are_rejected(values):
    with pytest.raises(ValueError, match='变化量超出'):
        run([rule(metric='change')], {'one': history(
            ('2026-01-01', values[0]), ('2026-01-02', values[1]))})


@pytest.mark.parametrize('logic,entries', [('all', ['2026-01-08']), ('any', ['2026-01-02'])])
def test_daily_weekly_asof_alignment_without_interpolation(logic, entries):
    result = run([rule('daily'), rule('weekly')], {
        'daily': history(('2026-01-01', 0), ('2026-01-02', 2), ('2026-01-09', 0)),
        'weekly': history(('2026-01-01', 0), ('2026-01-08', 2))}, logic=logic)
    assert result['trigger_dates'] == entries
    assert result['daily_states'][6]['rule_states'] == [True, False]
    assert result['daily_states'][7]['rule_states'] == [True, True]
    assert result['daily_states'][8]['rule_states'] == [False, True]
    assert result['current_state'] == ('未触发' if logic == 'all' else '触发')


@pytest.mark.parametrize('logic,first,state', [
    ('all', 2, '无数据'), ('all', 0, '未触发'), ('any', 2, '触发'), ('any', 0, '无数据')])
def test_three_valued_combination_preserves_unknown(logic, first, state):
    result = run([rule('one'), rule('absent')], {'one': history(('2026-01-01', first))}, logic)
    assert result['current_state'] == state
    assert result['daily_states'][0]['rule_states'][1] is None


def test_stale_and_absent_series_have_explicit_status_and_latest_date():
    stale = run([rule()], {'one': history(('2026-01-01', 2), ('2026-01-02', 3))}, end='2026-03-01')
    assert stale['current_state'] == '数据停更' and stale['condition_state'] == '触发'
    assert stale['series'][0]['latest_date'] == '2026-01-02'
    assert stale['series'][0]['notice'] == '该指标最新数据 2026-01-02，监测可能不会更新'
    absent = run([rule()], {})
    assert absent['current_state'] == '无数据' and absent['series'][0]['latest_date'] is None
    assert absent['trigger_dates'] == []


def test_pre_range_observations_warmup_without_fake_entry_and_future_is_excluded():
    result = run([rule(metric='change', value=0, consecutive_periods=2)], {'one': history(
        ('2025-12-28', 0), ('2025-12-29', 1), ('2025-12-30', 2),
        ('2026-01-02', 3), ('2026-01-11', -9))})
    assert result['daily_states'][0]['triggered'] is True
    assert result['trigger_dates'] == []
    assert result['intervals'][0]['started_before_range'] is True
    assert result['series'][0]['latest_date'] == '2026-01-02'
    assert result['series'][0]['quantities'] == [{'date': '2026-01-02', 'value': 1.0}]


def test_six_daily_rules_two_years_finish_within_three_seconds():
    first = date(2024, 1, 1)
    dates = [(first + timedelta(days=index)).isoformat() for index in range(732)]
    rules = [rule(str(index), metric='change', value=0, consecutive_periods=120) for index in range(6)]
    series = {item['series_id']: history(*[(day, index % 200) for index, day in enumerate(dates)])
              for item in rules}
    started = perf_counter()
    result = backtest_conditions(rules, 'all', series, dates[0], dates[-1])
    elapsed = perf_counter() - started
    assert elapsed < 3, elapsed
    assert len(result['daily_states']) == 732 and result['trigger_count'] == 4


def complete_window_transport(monkeypatch):
    import httpx
    from tin.integration import page_facts
    from tin.provenance import bridge
    original = bridge.structured_request
    reads = []

    def transport(method, endpoint, body, **kwargs):
        if body.get('include_invalid'):
            assert body['view_module'] == 'judgments'
            assert 1 <= body['points_per_series'] <= 121
            assert len(body['series_ids']) <= 256
            assert len(body['series_ids']) * body['points_per_series'] <= 20000
            reads.append(body.copy())
            # The chart fixture models the ordinary indicator projection. Adapt
            # only that fixture's context; assert the complete-window wire
            # request above before handing its rows back to the real reader.
            with page_facts.page_read_scope('indicators', series_ids=body['series_ids'],
                    day=body['as_of'], points_per_series=body['points_per_series']):
                adapted = {**body, 'view_module': 'indicators'}
                response = original(method, endpoint, adapted, **kwargs)
            content = response.json()
            content['include_invalid'] = True
            for row in content.get('rows', []):
                if row['value'] is None:
                    row['value_status'] = 'missing'
            return httpx.Response(response.status_code, json=content, request=response.request)
        return original(method, endpoint, body, **kwargs)
    monkeypatch.setattr(bridge, 'structured_request', transport)
    return reads


def attach_reader(state, rules, logic='all'):
    from tin.web.monitor_data import backtest_monitor
    # Catalog coverage describes the full source, independently of the bounded
    # historical window requested below.
    for sid, field in state.fields.items():
        valid = sorted(stamp for stamp, value in state.observations.get(sid, []) if value is not None)
        field.update(first_valid_date=valid[0] if valid else None,
                     last_valid_date=valid[-1] if valid else None, valid_count=len(valid))

    @state.app.get('/monitor-data-test')
    def read(day: str = '2024-03-29', start: str | None = None):
        return backtest_monitor(current_workbench.get(), rules, logic, start=start, end=day)


def test_authorized_real_projection_frequency_limits_and_warmup(chart_client, monkeypatch):
    client, state = chart_client
    state.fields[SID]['frequency'] = '日'
    state.fields[OTHER]['frequency'] = '月'
    today = date(2024, 3, 29)
    reads = complete_window_transport(monkeypatch)
    state.observations[SID] = [((today - timedelta(days=index)).isoformat(), 1500 - index)
                               for index in reversed(range(1400))]
    state.observations[OTHER] = [((today - timedelta(days=index * 30)).isoformat(), 100 - index)
                                 for index in reversed(range(100))]
    attach_reader(state, [rule(SID, metric='change', value=0, consecutive_periods=2), rule(OTHER)])
    response = client.get('/monitor-data-test')
    assert response.status_code == 200, response.text
    payload = response.json()
    assert payload['current_state'] == '触发'
    assert payload['start'] == (today - timedelta(days=731)).isoformat()
    for identifier, expected_limit in ((SID, 737), (OTHER, 69)):
        pages = [body for body in reads if identifier in body['series_ids']]
        assert sum(body['points_per_series'] for body in pages) == expected_limit
        boundary = today.isoformat()
        stitched = []
        for body in pages:
            assert body['as_of'] == boundary
            page = [point for point in state.observations[identifier]
                    if point[0] <= boundary][-body['points_per_series']:]
            stitched.extend(page)
            boundary = (date.fromisoformat(page[0][0]) - timedelta(days=1)).isoformat()
        assert sorted(stitched) == state.observations[identifier][-expected_limit:]
        assert len({stamp for stamp, _ in stitched}) == expected_limit
    assert len([body for body in reads if SID in body['series_ids']]) > 1
    assert payload['daily_states'][0]['rule_states'] == [True, True]
    assert len(payload['series'][0]['points']) == 732


def test_reader_rejects_unauthorized_id_and_restricted_scope(chart_client):
    client, state = chart_client
    attach_reader(state, [rule('kb-' + '9' * 48)])
    assert client.get('/monitor-data-test').status_code == 422
    assert client.get('/monitor-data-test', headers={'x-restricted': 'true'}).status_code == 403
    assert not any('points_per_series' in body for _, body in state.requests)


def test_reader_rejects_future_and_invalid_ranges(chart_client):
    client, state = chart_client
    attach_reader(state, [rule(SID)])
    assert client.get('/monitor-data-test?day=2099-01-01').status_code == 422
    assert client.get('/monitor-data-test?day=2024-03-29&start=2000-01-01').status_code == 422


def test_reader_caps_large_window_and_reports_actual_coverage(chart_client, monkeypatch):
    client, state = chart_client
    state.fields[SID]['frequency'] = '日'
    today = date(2024, 3, 29)
    state.observations[SID] = [((today - timedelta(days=index)).isoformat(), 2201 - index)
                               for index in reversed(range(2200))]
    reads = complete_window_transport(monkeypatch)
    attach_reader(state, [rule(SID)])
    response = client.get('/monitor-data-test?day=2024-03-29&start=2018-04-01')
    assert response.status_code == 200, response.text
    payload = response.json()
    assert sum(body['points_per_series'] for body in reads) == 2000
    assert len(payload['series'][0]['points']) == 2000
    actual_first = (today - timedelta(days=1999)).isoformat()
    notices = '；'.join(payload['notices'])
    assert actual_first in notices and today.isoformat() in notices
    assert payload['series'][0]['limited'] is True


def test_reader_reports_unrepresentable_change_as_validation_error(chart_client, monkeypatch):
    client, state = chart_client
    complete_window_transport(monkeypatch)
    state.observations[SID] = [('2024-03-28', -1e308), ('2024-03-29', 1e308)]
    attach_reader(state, [rule(SID, metric='change')])
    response = client.get('/monitor-data-test')
    assert response.status_code == 422, response.text
    assert '变化量超出' in response.json()['detail']


def test_real_projection_preserves_missing_periods_for_change(chart_client, monkeypatch):
    client, state = chart_client
    complete_window_transport(monkeypatch)
    state.observations[SID] = [('2024-03-25', 1), ('2024-03-26', 2), ('2024-03-27', None),
                               ('2024-03-28', 3), ('2024-03-29', 4)]
    attach_reader(state, [rule(SID, metric='change', value=0, consecutive_periods=2)])
    response = client.get('/monitor-data-test?start=2024-03-25')
    assert response.status_code == 200, response.text
    payload = response.json()
    assert payload['current_state'] == '无数据'
    assert payload['series'][0]['points'][2]['value'] is None
    assert payload['series'][0]['quantities'][3]['value'] is None
    assert payload['trigger_dates'] == []
