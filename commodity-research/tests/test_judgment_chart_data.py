"""Real saved-rule chart boundaries and missing observations stay explicit."""
from datetime import date
from types import SimpleNamespace

from tin.web import judgment_research as research
from tests.test_judgment_research_view import A, B, DAY, facts, point, saved


def test_chart_timeline_keeps_invalid_points_as_gaps(facts):
    point(facts, A, 1, 5)
    point(facts, A, 2, 999, status='无效')
    point(facts, A, 3, 0)
    metric = research.judgment_research_view(facts, saved(whitelist=[A]), DAY)['metrics'][A]
    assert [row['value'] for row in metric['timeline']] == [5, None, 0]
    assert len(metric['timeline']) == 3
    assert metric['timeline'][1]['date'].startswith('2030-01-02')


def test_valid_only_legacy_snapshots_do_not_claim_an_exact_direction_streak(facts):
    point(facts, A, 1, 1)
    point(facts, A, 2, 2)
    facts.info['knowledge_facts'] = True
    metric = research._metric(facts, A, DAY)
    assert metric['history_complete'] is False
    assert metric['streak'] is None
    facts.info['knowledge_facts_include_invalid'] = True
    metric = research._metric(facts, A, DAY)
    assert metric['history_complete'] is True
    assert metric['streak']['count'] == 1


def test_chart_only_shows_confirmed_boundaries_of_the_same_actual_indicator(facts):
    point(facts, A, 1, 10)
    point(facts, B, 1, 20)
    judgment = saved(whitelist=[A, B])
    judgment.thresholds = [
        {'id': 'upper', 'name': 'Upper observation', 'series_id': A, 'operator': '>',
         'value': 12, 'type': '观察位', 'horizon': '短期'},
        {'id': 'range', 'name': 'Entry range', 'series_id': A, 'operator': 'between',
         'value': [8, 11], 'type': '介入位', 'horizon': '短期'},
        {'id': 'other', 'name': 'Other field', 'series_id': B, 'operator': '<',
         'value': 3, 'type': '风控线', 'horizon': '短期'}]
    view = research.judgment_research_view(facts, judgment, DAY)
    assert [(b['value'], b['name']) for b in view['metrics'][A]['boundaries']] == [
        (12, 'Upper observation'), (8, 'Entry range'), (11, 'Entry range')]
    assert [b['value'] for b in view['metrics'][B]['boundaries']] == [3]
    assert all('series_id' not in item for item in view['metrics'][A]['boundaries'])


def test_metric_chart_points_are_bounded_when_more_history_is_explicitly_requested(facts):
    for day in range(1, 21):
        point(facts, A, day, day)
    metric = research._metric(facts, A, DAY, 5)
    assert len(metric['timeline']) == 5
    assert metric['timeline'][0]['value'] == 16


def test_trend_streak_uses_real_steps_and_stops_at_flat_or_invalid(facts):
    for day, value in [(1, 10), (2, 10), (3, 12), (4, 15), (5, 18)]:
        point(facts, A, day, value)
    metric = research._metric(facts, A, DAY)
    assert metric['streak']['direction'] == 'up'
    assert metric['streak']['count'] == 3
    assert metric['streak']['period'] == '个数据日'
    assert metric['streak']['change'] == 8
    assert metric['streak']['change_pct'] == 80
    point(facts, A, 6, 500, status='无效')
    assert research._metric(facts, A, DAY)['streak'] is None


def test_weekly_streak_and_semantics_are_explicit_not_inferred_from_name(facts):
    from tin.models import Indicator
    indicator = facts.get(Indicator, A)
    indicator.name = '库存 warehouse stock 库存'
    indicator.frequency = '周'
    facts.commit()
    for day, value in [(1, 30), (8, 20), (15, 0)]:
        point(facts, A, day, value)
    metric = research._metric(facts, A, DAY)
    assert metric['streak']['direction'] == 'down' and metric['streak']['count'] == 2
    assert metric['streak']['period'] == '期'
    assert metric['streak']['change'] == -30
    assert metric['trend_semantics'] == 'change'
    indicator.caliber = {'trend_semantics': 'inventory'}
    facts.commit()
    assert research._metric(facts, A, DAY)['trend_semantics'] == 'inventory'


def test_streak_latest_zero_is_real_and_flat_stops_the_direction(facts):
    point(facts, A, 1, 2)
    point(facts, A, 2, 1)
    point(facts, A, 3, 0)
    assert research._metric(facts, A, DAY)['streak']['count'] == 2
    point(facts, A, 4, 0)
    assert research._metric(facts, A, DAY)['streak'] is None


def test_shipped_chart_data_algorithms():
    from pathlib import Path
    import shutil
    import subprocess
    node = shutil.which('node')
    assert node, 'Node is required for native chart checks'
    result = subprocess.run([node, '--test', str(Path(__file__).with_name('judgment-chart.test.cjs'))],
                            capture_output=True, text=True, timeout=15)
    assert result.returncode == 0, result.stdout + result.stderr


def test_saved_ma_rule_only_expands_its_bound_series_window(facts, monkeypatch):
    observed = []
    actual = research.recent_points
    def capture(session, sid, day, count):
        observed.append((sid, count))
        return actual(session, sid, day, count)
    monkeypatch.setattr(research, 'recent_points', capture)
    judgment = saved(whitelist=[A, B])
    judgment.thresholds = [{'id': 'average', 'name': 'Average boundary', 'series_id': A,
        'operator': '<', 'value': {'kind': 'sma', 'window': 60}, 'type': '观察位', 'horizon': '短期'}]
    view = research.judgment_research_view(facts, judgment, DAY)
    assert observed == [(A, 83), (B, 24)]
    assert view['metrics'][A]['boundaries'] == [{'kind': 'sma', 'window': 60,
        'name': 'Average boundary', 'operator': '<', 'type': '观察位'}]


def test_absolute_falsifier_boundaries_show_on_the_same_axis_but_change_rules_do_not(facts):
    from tests.test_judgment_research_view import rule
    judgment = saved(falsifiers=[rule('absolute', A, 'bull', 'value > 19'),
        rule('delta', A, 'bear', 'change < 0')])
    boundaries = research.judgment_research_view(facts, judgment, DAY)['metrics'][A]['boundaries']
    assert len(boundaries) == 1
    assert boundaries[0]['value'] == 19
    assert boundaries[0]['name'] == 'Original falsification condition absolute'


def test_daily_streak_collapses_intraday_points_without_inventing_extra_days():
    from datetime import datetime, timezone
    rows = [SimpleNamespace(value=value, status='正常', as_of=datetime(2030, 1, day, hour, tzinfo=timezone.utc))
        for day, hour, value in [(1, 1, 2), (1, 4, 4), (2, 1, 6), (2, 4, 7), (3, 1, 8)]]
    streak = research._streak(rows, '日', 24)
    assert streak['count'] == 2 and streak['change'] == 4
    assert streak['period'] == '个数据日'
