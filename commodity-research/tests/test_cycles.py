from copy import deepcopy
from datetime import date, timedelta
from statistics import mean, median

import pytest

from tin.compute.cycles import BUILTIN_TEMPLATES, SPRING_FESTIVAL, compute_cycles, repeat_bounds
from tests.chart_fixtures import segment_fixture


def test_statistics_and_band_use_real_matches_without_mutating_inputs():
    template, points = segment_fixture()
    original = deepcopy((template, points))
    result = compute_cycles(points, template, '2020-04-08', band_cycles=2)
    assert (template, points) == original
    position = result['position']
    assert result['freq_days'] == 1 and result['tolerance_days'] == 1
    assert position['n'] == 3 and position['rank'] == 1 and position['share_below'] == 1
    assert (position['min'], position['max'], position['median'], position['mean']) == (17, 37, 27, 27)
    assert position['vs_recent_mean'] == {'kind': 'pct', 'value': 20 / 27, 'cycles': 3}
    assert result['band'][0] == {'x': 0, 'min': 20, 'max': 30, 'mean': 25, 'n': 2}
    assert 'incomplete' in result['segments'][-1]['flags']
    assert len(result['segments'][-1]['points']) == 8


def test_missing_same_position_is_not_replaced_or_filled():
    template, points = segment_fixture()
    points = [(day, value) for day, value in points if not (day.month == 1 and day.day >= 7)]
    result = compute_cycles(points, template, '2020-04-08')
    assert result['position']['n'] == 2
    assert result['position']['status'] == 'insufficient_cycles'
    assert 'rank' not in result['position'] and result['band'] == [] and result['forward'] == []
    assert len(result['segments'][0]['points']) == 6


def test_sparse_segments_still_display_but_do_not_enter_statistics():
    template, points = segment_fixture()
    points = [(day, value) for day, value in points if day.month != 1 or day.day in (1, 8)]
    result = compute_cycles(points, template, '2020-04-08')
    assert 'sparse' in result['segments'][0]['flags']
    assert len(result['segments'][0]['points']) == 2
    assert result['position']['n'] == 2 and 'insufficient_cycles' in result['warnings']


def test_sparse_current_is_visible_without_a_rank():
    template, points = segment_fixture()
    points = [(day, value) for day, value in points if day.month != 4 or day.day == 4]
    result = compute_cycles(points, template, '2020-04-04')
    assert 'sparse' in result['segments'][-1]['flags']
    assert result['position']['n'] == 3
    assert result['position']['status'] == 'unusable_current'
    assert 'rank' not in result['position']


def test_trading_day_current_year_uses_elapsed_coverage_at_september_cutoff():
    start, as_of = date(2020, 1, 1), date(2023, 9, 22)
    days = (start + timedelta(days=offset) for offset in range((as_of - start).days + 1))
    # Synthetic trading calendar with weekends and three one-week closures.
    points = [(day, day.toordinal()) for day in days
              if day.weekday() < 5 and not (day.month in {2, 5, 10} and day.day <= 7)]
    result = compute_cycles(points, BUILTIN_TEMPLATES['自然年'], as_of)
    current = result['segments'][-1]
    assert result['freq_days'] == 1 and len(current['points']) == 180
    assert current['current'] is True and current['flags'] == ['incomplete']
    assert result['position']['status'] == 'ok' and result['position']['n'] == 3


def test_weekly_current_year_uses_elapsed_coverage_at_mid_february_cutoff():
    points = [(date(year, 1, 1) + timedelta(days=offset), year + offset)
              for year in range(2020, 2024)
              for offset in range(0, (date(year + 1, 1, 1) - date(year, 1, 1)).days, 7)]
    result = compute_cycles(points, BUILTIN_TEMPLATES['自然年'], '2023-02-15')
    current = result['segments'][-1]
    assert result['freq_days'] == 7 and len(current['points']) == 7
    assert current['flags'] == ['incomplete']
    assert result['position']['date'] == '2023-02-12'
    assert result['position']['status'] == 'ok' and result['position']['n'] == 3


@pytest.mark.parametrize('band_cycles', [2, 3])
def test_repeat_band_covers_the_longest_past_year_and_counts_actual_matches(band_cycles):
    start, as_of = date(2020, 1, 1), date(2023, 9, 22)
    days = (start + timedelta(days=offset) for offset in range((as_of - start).days + 1))
    points = [(day, 10 * (day.year - 2019)) for day in days if not (day.year == 2021 and day.month == 12)]
    result = compute_cycles(points, BUILTIN_TEMPLATES['自然年'], as_of, band_cycles=band_cycles)
    assert result['position']['status'] == 'ok' and result['position']['x'] == 264
    # The older leap year sets the grid even when it is outside band_cycles.
    assert [point['x'] for point in result['band']] == list(range(366))
    assert [point['n'] for point in result['band']] == [band_cycles if x <= 334 else band_cycles - 1 for x in range(366)]
    assert result['band'][-1] == {'x': 365, 'min': 30 if band_cycles == 2 else 10, 'max': 30,
                                  'mean': 30 if band_cycles == 2 else 20, 'n': band_cycles - 1}


def test_segments_band_uses_full_grid_and_omits_positions_without_matches():
    starts = [date(2020, month, 1) for month in range(1, 5)]
    lengths, counts = [15, 12, 7, 10], [10, 12, 7, 10]
    template = {'kind': 'segments', 'name': '不同段长', 'segments': [
        {'label': str(index), 'start': start.isoformat(), 'end': (start + timedelta(days=length - 1)).isoformat()}
        for index, (start, length) in enumerate(zip(starts, lengths))]}
    points = [(start + timedelta(days=offset), 20 * index + offset)
              for index, (start, count) in enumerate(zip(starts, counts)) for offset in range(count)]
    result = compute_cycles(points, template, '2020-04-03')
    assert result['position']['status'] == 'ok' and result['position']['x'] == 2
    assert [point['x'] for point in result['band']] == list(range(13))
    assert [point['n'] for point in result['band']] == [3] * 8 + [2] * 3 + [1] * 2


def test_event_band_starts_before_zero_and_covers_the_full_window():
    anchors = [date(year, 5, 1) for year in range(2020, 2024)]
    template = {'kind': 'event', 'name': '事件网格', 'before_days': 14, 'after_days': 14,
                'anchors': [{'label': str(anchor.year), 'date': anchor.isoformat()} for anchor in anchors]}
    points = [(anchor + timedelta(days=offset), 10 * index + offset)
              for index, anchor in enumerate(anchors) for offset in range(-14, 15, 7)]
    result = compute_cycles(points, template, '2023-05-03')
    assert result['freq_days'] == 7 and result['position']['x'] == 0
    assert result['position']['status'] == 'ok'
    assert [point['x'] for point in result['band']] == [-14, -7, 0, 7, 14]
    assert all(point['n'] == 3 for point in result['band'])
    assert result['band'][-1] == {'x': 14, 'min': 14, 'max': 34, 'mean': 24, 'n': 3}


def test_band_grid_rounds_fractional_frequency_half_up():
    starts = [date(2020, month, 1) for month in range(1, 5)]
    template = {'kind': 'segments', 'name': '非整数频率', 'segments': [
        {'label': str(index), 'start': start.isoformat(), 'end': (start + timedelta(days=10)).isoformat()}
        for index, start in enumerate(starts)]}
    offsets = [(0, 2, 4, 6, 9), (0, 2, 4, 7, 10), (0, 2, 4, 7, 10), (0, 2)]
    points = [(start + timedelta(days=offset), 10 * index + offset)
              for index, (start, positions) in enumerate(zip(starts, offsets)) for offset in positions]
    result = compute_cycles(points, template, '2020-04-03')
    assert result['freq_days'] == 2.5 and result['tolerance_days'] == 1
    assert result['position']['status'] == 'ok'
    assert [point['x'] for point in result['band']] == [0, 3, 6, 9]
    assert all(point['n'] == 3 for point in result['band'])


def test_september_year_crossing_labels_and_inclusive_boundaries():
    template = {**BUILTIN_TEMPLATES['自然年'], 'start': '09-01'}
    result = compute_cycles([('2022-08-31', 1), ('2022-09-01', 2), ('2023-08-31', 3), ('2023-09-01', 4)],
                            template, '2023-09-01')
    assert [(s['label'], s['start'], s['end']) for s in result['segments']] == [
        ('21/22', '2021-09-01', '2022-08-31'), ('22/23', '2022-09-01', '2023-08-31'),
        ('23/24', '2023-09-01', '2024-08-31')]
    assert [p['x'] for p in result['segments'][1]['points']] == [0, 364]
    assert result['segments'][2]['current'] is True


def test_leap_days_use_natural_day_offsets_without_fabricated_observations():
    result = compute_cycles([('2023-02-28', 1), ('2023-03-01', 2), ('2024-02-28', 3),
                             ('2024-02-29', 4), ('2024-03-01', 5)], BUILTIN_TEMPLATES['自然年'], '2024-03-01')
    assert [point['x'] for point in result['segments'][0]['points']] == [58, 59]
    assert [point['x'] for point in result['segments'][1]['points']] == [58, 59, 60]
    assert result['segments'][1]['end'] == '2024-12-31'


@pytest.mark.parametrize('period,start,day,expected', [
    ('year', '09-01', '2024-02-29', ('2023-09-01', '2024-08-31')),
    ('half', '09-01', '2024-02-29', ('2023-09-01', '2024-02-29')),
    ('quarter', '09-01', '2024-02-29', ('2023-12-01', '2024-02-29')),
    ('month', '09-15', '2024-02-14', ('2024-01-15', '2024-02-14')),
    ('month', '01-31', '2024-02-10', ('2024-01-31', '2024-03-30')),
])
def test_repeat_calendar_periods(period, start, day, expected):
    assert tuple(value.isoformat() for value in repeat_bounds({'period': period, 'start': start}, day)) == expected


def test_spring_festival_uses_only_the_supplied_calendar_and_event_zero():
    assert len(SPRING_FESTIVAL) == 31
    result = compute_cycles([('2023-12-15', 0), ('2023-12-16', 1), ('2024-02-03', 2), ('2024-02-10', 3),
                             ('2024-04-06', 4), ('2024-04-07', 5)], BUILTIN_TEMPLATES['春节前后8周'], '2024-04-07')
    assert len(result['segments']) == 1
    segment = result['segments'][0]
    assert segment['label'] == '2024'
    assert (segment['start'], segment['end']) == ('2023-12-16', '2024-04-06')
    assert [point['x'] for point in segment['points']] == [-56, -7, 0, 56]
    assert compute_cycles([('2031-02-01', 1)], BUILTIN_TEMPLATES['春节前后8周'], '2031-03-01')['segments'] == []


def test_custom_event_anchors_align_at_the_event_not_the_window_start():
    template = {'kind': 'event', 'name': '事件', 'anchors': [{'label': '原始标签', 'date': '2024-02-10'}],
                'before_days': 2, 'after_days': 2, 'transform': 'change'}
    result = compute_cycles([('2024-02-08', 5), ('2024-02-10', 8), ('2024-02-12', 9)], template, '2024-02-12')
    points = result['segments'][0]['points']
    assert [p['x'] for p in points] == [-2, 0, 2]
    assert [p['value'] for p in points] == [0, 3, 4]


def test_overlapping_segments_preserve_each_curve_and_warn():
    template = {'kind': 'segments', 'name': '重叠', 'segments': [
        {'label': '第一个', 'start': '2024-01-01', 'end': '2024-01-08'},
        {'label': '第二个', 'start': '2024-01-08', 'end': '2024-01-10'}]}
    result = compute_cycles([('2024-01-08', 3), ('2024-01-09', 4)], template, '2024-01-10')
    assert 'segments_overlap' in result['warnings']
    assert result['segments'][0]['points'][0]['raw'] == result['segments'][1]['points'][0]['raw'] == 3
    assert sum(segment['current'] for segment in result['segments']) == 1


def test_cutoff_also_limits_frequency_current_band_and_forward_observations():
    template, observations = segment_fixture()
    result = compute_cycles(observations + [(date(2021, 1, 1), 9000)], template, '2020-03-08')
    assert result['freq_days'] == 1
    assert result['position']['date'] == '2020-03-08'
    assert all(p['date'] <= '2020-03-08' for s in result['segments'] for p in s['points'])
    assert result['forward'] == []


@pytest.mark.parametrize('transform', ['change', 'rebase'])
def test_transform_values_and_statistics_use_the_first_real_observation(transform):
    template, observations = segment_fixture(transform)
    result = compute_cycles(observations, template, '2020-04-08')
    expected = 7 if transform == 'change' else 47 / 40 * 100
    assert result['position']['value'] == expected
    assert result['position']['vs_recent_mean']['kind'] == 'change'
    transformed = [7 if transform == 'change' else (base + 7) / base * 100 for base in (10, 20, 30)]
    assert result['position']['mean'] == pytest.approx(mean(transformed))
    assert result['position']['median'] == pytest.approx(median(transformed))
    assert result['segments'][0]['points'][0]['raw'] == 10


@pytest.mark.parametrize('transform', ['change', 'rebase'])
def test_start_missing_displays_transformed_points_but_excludes_statistics(transform):
    template, observations = segment_fixture(transform)
    observations = [(day, value) for day, value in observations if day.month != 1 or day.day >= 3]
    result = compute_cycles(observations, template, '2020-04-08')
    segment = result['segments'][0]
    assert 'start_missing' in segment['flags'] and 'sparse' not in segment['flags']
    assert segment['points'][0]['value'] == (0 if transform == 'change' else 100)
    assert result['position']['n'] == 2
    raw = compute_cycles(observations, {**template, 'transform': 'raw'}, '2020-04-08')
    assert raw['position']['n'] == 3 and 'start_missing' not in raw['segments'][0]['flags']


@pytest.mark.parametrize('base', [0, -10])
def test_rebase_nonpositive_base_is_unavailable(base):
    template, observations = segment_fixture('rebase')
    observations = [(day, base + day.day - 1 if day.month == 1 else value) for day, value in observations]
    result = compute_cycles(observations, template, '2020-04-08')
    assert 'invalid_base' in result['segments'][0]['flags']
    assert all(p['value'] is None for p in result['segments'][0]['points'])
    assert result['position']['n'] == 2


def test_forward_looks_across_segment_boundaries_and_summarizes_observed_changes():
    starts = [date(2020, 1, 1) + timedelta(days=10 * i) for i in range(4)]
    template = {'kind': 'segments', 'name': '连续窗口', 'segments': [
        {'label': str(i), 'start': start.isoformat(), 'end': (start + timedelta(days=9)).isoformat()}
        for i, start in enumerate(starts)]}
    points = [(date(2020, 1, 1) + timedelta(days=i), i + 100) for i in range(40)]
    result = compute_cycles(points, template, '2020-02-04', horizons=(8, 12))
    forward = result['forward'][0]
    assert forward['n'] == forward['up'] == 3
    assert (forward['min'], forward['max'], forward['median']) == (8, 8, 8)
    assert forward['items'][0]['end_date'] == '2020-02-02'
    assert result['forward'][1]['n'] == 2, 'A future point beyond the cutoff cannot enter historical follow-through'


def test_nearest_ties_choose_earlier_date_and_tolerance_is_floor_half_median():
    starts = [date(2020, month, 1) for month in range(1, 5)]
    template = {'kind': 'segments', 'name': '周窗口', 'segments': [
        {'label': str(i), 'start': s.isoformat(), 'end': (s + timedelta(days=27)).isoformat()} for i, s in enumerate(starts)]}
    points = [(start + timedelta(days=offset), 10 * i + offset) for i, start in enumerate(starts[:3]) for offset in (0, 6, 12, 18, 24)]
    points += [(starts[3] + timedelta(days=offset), offset + 100) for offset in (0, 6, 12, 21)]
    result = compute_cycles(points, template, '2020-04-22')
    assert result['freq_days'] == 6 and result['tolerance_days'] == 3
    assert all(peer['date'].endswith('-19') for peer in result['position']['peers'])


def test_recent_five_mean_and_band_do_not_use_all_old_cycles():
    starts = [date(2020, month, 1) for month in range(1, 8)]
    template = {'kind': 'segments', 'name': '多期', 'segments': [
        {'label': str(i), 'start': s.isoformat(), 'end': (s + timedelta(days=9)).isoformat()} for i, s in enumerate(starts)]}
    points = [(start + timedelta(days=offset), 10 * (i + 1) + offset) for i, start in enumerate(starts) for offset in range(10)]
    result = compute_cycles(points, template, '2020-07-08')
    assert result['position']['n'] == 6
    assert result['position']['vs_recent_mean'] == {'kind': 'pct', 'value': 30 / 47, 'cycles': 5}
    assert result['band'][0]['n'] == 5 and result['band'][0]['min'] == 20
    limited = compute_cycles(points, template, '2020-07-08', cycles=4)
    assert len(limited['segments']) == 4 and limited['position']['n'] == 3


def test_empty_and_single_observation_have_no_invented_frequency_or_statistics():
    empty = compute_cycles([], BUILTIN_TEMPLATES['自然年'], '2024-01-01')
    assert empty['freq_days'] is None and empty['position'] is None
    single = compute_cycles([('2024-01-01', 0)], BUILTIN_TEMPLATES['自然年'], '2024-01-01')
    assert single['freq_days'] is None and single['position']['value'] == 0
    assert single['position']['status'] == 'insufficient_cycles'


def test_cycle_display_count_is_relative_to_latest_real_observation_in_a_stale_series():
    points = [(date(2020, 1, 1) + timedelta(days=i), i + 1) for i in range(366)]
    result = compute_cycles(points, BUILTIN_TEMPLATES['自然年'], '2031-01-01', cycles=2)
    assert result['position']['date'] == '2020-12-31'
    assert [segment['label'] for segment in result['segments']] == ['2020']
    assert result['segments'][0]['current'] is True
    assert result['omitted_segments'] == [str(year) for year in range(2021, 2032)]
    assert result['warnings'].count('empty_segments_omitted') == 1


@pytest.mark.parametrize('cycles', [None, 2])
def test_empty_repeat_years_are_omitted_before_applying_the_display_count(cycles):
    points = [(date(year, 1, 1) + timedelta(days=i), i + 1) for year in (2020, 2022, 2023) for i in range(100)]
    result = compute_cycles(points, BUILTIN_TEMPLATES['自然年'], '2026-11-06', cycles=cycles)
    assert [s['label'] for s in result['segments']] == (['2020', '2022', '2023'] if cycles is None else ['2022', '2023'])
    assert result['omitted_segments'] == ['2021', '2024', '2025', '2026']
    assert result['warnings'].count('empty_segments_omitted') == 1
    assert all(s['points'] for s in result['segments'])
    assert next(s['label'] for s in result['segments'] if s['current']) == '2023'


def test_empty_manual_segments_report_original_labels_and_do_not_consume_cycle_slots():
    template, points = segment_fixture()
    points = [(day, value) for day, value in points if day.month != 3]
    original = deepcopy((template, points))
    result = compute_cycles(points, template, '2020-04-08', cycles=2)
    assert (template, points) == original
    assert [s['label'] for s in result['segments']] == ['段1', '段3']
    assert result['omitted_segments'] == ['段2']
    assert result['position']['n'] == 1
    # Observations after the cutoff cannot keep a future segment in the output.
    earlier = compute_cycles(points, template, '2020-02-08')
    assert earlier['omitted_segments'] == ['段2', '段3']
    empty = compute_cycles([], template, '2020-04-08')
    assert empty['segments'] == [] and empty['position'] is None
    assert empty['omitted_segments'] == ['段0', '段1', '段2', '段3']
    assert {'empty_segments_omitted', 'no_observations', 'insufficient_cycles'} <= set(empty['warnings'])


def test_nonempty_cycles_have_no_omission_notice_even_with_unusable_transforms():
    template, points = segment_fixture('rebase')
    points = [(day, 0 if day.month == 1 else value) for day, value in points]
    result = compute_cycles(points, template, '2020-04-08')
    assert result['omitted_segments'] == [] and 'empty_segments_omitted' not in result['warnings']
    assert result['segments'][0]['flags'] == ['invalid_base']
    assert result['segments'][0]['points'] and all(p['value'] is None for p in result['segments'][0]['points'])


@pytest.mark.parametrize('points', [[('2024-01-02', 1), ('2024-01-01', 2)],
                                   [('2024-01-01', 1), ('2024-01-01', 2)],
                                   [('2024-01-01', float('nan'))], [('2024-01-01', True)]])
def test_invalid_pure_function_input_is_rejected(points):
    with pytest.raises(ValueError):
        compute_cycles(points, BUILTIN_TEMPLATES['自然年'], '2024-01-02')


def test_stale_series_current_segment_coverage_uses_latest_observation():
    """Data stopping months before the cutoff keeps a weekly current segment usable."""
    from datetime import date, timedelta
    from tin.compute.cycles import compute_cycles
    rows = []
    for year in range(2016, 2024):
        start = date(year, 8, 3)
        weeks = 52 if year < 2023 else 6
        rows.extend((start + timedelta(days=7 * i), float(10 + year - 2016 + i)) for i in range(weeks))
    template = {'kind': 'repeat', 'name': '美棉年度', 'start': '08-01', 'period': 'year', 'align': 'days', 'transform': 'raw'}
    result = compute_cycles(rows, template, '2026-11-06')
    current = next(part for part in result['segments'] if part['current'])
    assert 'sparse' not in current['flags']
    assert result['position']['status'] == 'ok' and result['position']['n'] == 7
