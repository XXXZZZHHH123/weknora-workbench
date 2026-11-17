"""Compare synthetic observations against the pre-optimization implementation.

Run the reproducible CPU benchmark separately with:
    PYTHONPATH=src python -m tests.test_cycles_performance
No real research data or running services are used.
"""
from copy import deepcopy
from datetime import date, timedelta
import json
import random
from statistics import mean, median
from time import perf_counter

import pytest

from tin.compute import cycles
from tests import cycles_reference as reference


def synthetic_cases():
    rng = random.Random(71239)
    for stride in (1, 7, 30):
        start = date(2010, 1, 1)
        points = [(start + timedelta(days=index * stride), rng.uniform(0.1, 1000))
                  for index in range(2000)]
        # Irregular gaps, stale cutoffs, and cross-year starts use actual dates.
        for transform in ('raw', 'change', 'rebase'):
            for selected in (points, [point for index, point in enumerate(points) if index % 13 != 0]):
                yield selected, {**cycles.BUILTIN_TEMPLATES['自然年'], 'start': '09-01',
                                 'transform': transform}, selected[-1][0], {'cycles': 8}
    starts = [date(2020, month, 1) for month in range(1, 8)]
    segments = [{'label': str(index), 'start': start.isoformat(),
                 'end': (start + timedelta(days=14)).isoformat()}
                for index, start in enumerate(starts)]
    template = {'kind': 'segments', 'name': '合成周期', 'segments': segments}
    for transform in ('raw', 'change', 'rebase'):
        for magnitude in (1.0, 1e308, -1e308, 5e-324):
            points = [(start + timedelta(days=offset), magnitude * ((index % 2) * 2 - 1))
                      for index, start in enumerate(starts) for offset in range(15)]
            yield points, {**template, 'transform': transform}, points[-1][0], {}
    # Overlap, empty intervals, fractional frequency, current-missing, and no
    # observations keep all early-return branches under the same comparison.
    for observations in ([], [(starts[0], 1)],
                         [(start + timedelta(days=offset), float(index + offset))
                          for index, start in enumerate(starts) for offset in (0, 2, 4, 7, 10)]):
        altered = deepcopy(template)
        altered['segments'][1]['start'] = altered['segments'][0]['end']
        for options in ({}, {'current_start': '2021-01-01', 'cycles': 4}):
            yield observations, altered, starts[-1] + timedelta(days=20), options
    for transform in ('raw', 'change', 'rebase'):
        anchors = [date(year, 5, 1) for year in range(2010, 2020)]
        event = {'kind': 'event', 'name': '合成事件', 'before_days': 14, 'after_days': 14,
                 'anchors': [{'label': str(index), 'date': anchor.isoformat()}
                             for index, anchor in enumerate(anchors)], 'transform': transform}
        observations = [(anchor + timedelta(days=offset), float(index + offset))
                        for index, anchor in enumerate(anchors) for offset in range(-14, 15, 7)]
        yield observations, event, anchors[-1], {'band_cycles': 3}


@pytest.mark.parametrize('observations,template,as_of,options', list(synthetic_cases()))
def test_complete_and_position_models_match_frozen_reference(observations, template, as_of, options):
    before = deepcopy(observations)
    prepared = cycles.prepare_observations(observations)
    # Current and preceding cutoffs share validation, not results or frequency.
    for cutoff in (as_of, as_of - timedelta(days=1)):
        expected = reference.compute_cycles(observations, template, cutoff, **options)
        actual = cycles.compute_cycles(prepared, template, cutoff, **options)
        assert actual == expected
        expected['band'] = []
        expected['forward'] = []
        assert cycles.compute_cycles(prepared, template, cutoff, details=False, **options) == expected
        json.dumps(actual, allow_nan=False)
    assert observations == before


def test_exact_float_mean_keeps_statistics_rounding():
    rng = random.Random(8137)
    batches = [[1e308, -1e308, 1e-308], [5e-324, 0.0], [1.0, 1e100, -1e100],
               [1e308] * 5, [-0.0], [1.0, 1.0 + 2 ** -52]]
    batches.extend([rng.uniform(-1, 1) * 2 ** rng.randint(-1022, 1023)
                    for _ in range(rng.randint(1, 40))] for _ in range(1000))
    for values in batches:
        assert cycles._mean(values) == mean(values)


def test_prepared_input_is_immutable_and_validates_future_rows():
    observations = [('2024-01-01', 1), ('2024-01-02', 2)]
    prepared = cycles.prepare_observations(observations)
    observations[0] = ('2024-01-01', 99)
    assert prepared.rows[0][1] == 1
    assert cycles.prepare_observations(prepared) is prepared
    for invalid in ([('2024-01-01', 1), ('2025-01-01', float('nan'))],
                    [('2024-01-01', 1), ('2024-01-01', 2)]):
        with pytest.raises(ValueError):
            cycles.compute_cycles(invalid, cycles.BUILTIN_TEMPLATES['自然年'], '2024-01-01')


def test_nearest_retains_tie_and_tolerance_boundaries():
    for positions in ([], [0], [0, 2], [-14, -7, 0, 7, 14], [1, 5, 12, 19]):
        points = [('synthetic', index) for index in range(len(positions))]
        for target in range(-20, 26):
            for tolerance in (0, 1, 2, 3, 7):
                assert cycles._nearest(points, positions, target, tolerance) == reference._nearest(
                    points, positions, target, tolerance)


def test_multi_curve_models_match_frozen_reference():
    observations, template, cutoff, options = next(synthetic_cases())
    curves = [{'label': '合成' + str(index), 'unit': '单位', 'limited': index == 1,
               'points': [{'date': day.isoformat(), 'value': value * (index + 1)}
                          for day, value in observations[:len(observations) - index * 400]]}
              for index in range(3)]
    expected = reference.compute_multi_cycles(curves, template, cutoff, **options)
    assert cycles.compute_multi_cycles(curves, template, cutoff, **options) == expected
    for curve in expected['curves']:
        curve['cycle']['forward'] = []
    assert cycles.compute_multi_cycles(curves, template, cutoff, details=False, **options) == expected


def benchmark():
    """108 synthetic daily series × current/previous date, 2000 points each."""
    start = date(2019, 1, 1)
    series = [[(start + timedelta(days=index), 100 + metric + (index % 83) / 7)
               for index in range(2000)] for metric in range(108)]
    cutoff = series[0][-1][0]
    template = cycles.BUILTIN_TEMPLATES['自然年']

    def run(engine, *, prepare=False, details=True):
        started = perf_counter()
        for observations in series:
            observations = cycles.prepare_observations(observations) if prepare else observations
            for day in (cutoff, cutoff - timedelta(days=1)):
                kwargs = {'details': details} if engine is cycles.compute_cycles else {}
                engine(observations, template, day, cycles=8, **kwargs)
        return perf_counter() - started

    stages = {}
    stages['reference_full'] = median(run(reference.compute_cycles) for _ in range(3))
    original_clean, original_mean, original_nearest = reference._finite_result, reference.mean, reference._nearest
    try:
        reference._finite_result = cycles._finite_result
        stages['reference_selective_clean'] = median(run(reference.compute_cycles) for _ in range(3))
        reference.mean = cycles._mean
        stages['reference_selective_clean_exact_mean'] = median(run(reference.compute_cycles) for _ in range(3))
        reference._nearest = cycles._nearest
        stages['reference_selective_clean_exact_mean_nearest'] = median(run(reference.compute_cycles) for _ in range(3))
    finally:
        reference._finite_result, reference.mean, reference._nearest = original_clean, original_mean, original_nearest
    stages['optimized_full'] = median(run(cycles.compute_cycles) for _ in range(3))
    stages['optimized_prepared'] = median(run(cycles.compute_cycles, prepare=True) for _ in range(3))
    stages['optimized_position'] = median(run(cycles.compute_cycles, prepare=True, details=False) for _ in range(3))
    print(json.dumps({'series': 108, 'points_each': 2000, 'computations': 216,
                      'median_seconds_3_runs': stages}, indent=2))


if __name__ == '__main__':
    benchmark()
