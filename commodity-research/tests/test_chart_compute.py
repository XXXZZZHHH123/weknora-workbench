"""Explicit formulas, common cycle periods and current catalog unit checks."""
from copy import deepcopy
import json

from fastapi import HTTPException
from pydantic import ValidationError
import pytest

from tin.compute.cycles import BUILTIN_TEMPLATES, compute_cycles, compute_multi_cycles
from tin.schemas.chart import ChartCurve, ChartDefinition
from tin.web import chart_data
from tin.web.chart_data import (curve_unit, definition_series_ids, resolve_curves,
                                unit_notices, validate_definition)
from tests.chart_fixtures import FIELDS, OTHER, SID, THIRD, definition, segment_fixture


def derived(op='sub', **changes):
    return {'derived': {'op': op, 'a': SID, 'b': OTHER}, 'label': '研究员指定计算', **changes}


def source(points, **changes):
    return {'points': [{'date': day, 'value': value} for day, value in points],
            'freq_days': 1, 'limited': False, 'point_limit': 2000, 'source_version': 'version', **changes}


def test_curve_explicit_source_exclusive_and_formula_strictness():
    for curve in ({'label': '无来源'}, derived(series_id=SID),
                  {'label': '不支持运算', 'derived': {'op': 'add', 'a': SID, 'b': OTHER}},
                  {'label': '确认不可隐式转换', 'derived': {'op': 'sub', 'a': SID, 'b': OTHER, 'unit_confirmed': 'yes'}}):
        with pytest.raises(ValidationError):
            ChartCurve.model_validate(curve)
    curve = ChartCurve.model_validate(derived(curve_cycles='current_and_previous'))
    assert curve.series_id is None and curve.derived.a == SID
    assert curve.derived.unit_confirmed is False


def test_cycle_eight_curve_limit_and_display_selection():
    time = {'mode': 'cycle', 'template': BUILTIN_TEMPLATES['自然年'], 'curve_cycles': 'current_and_previous'}
    curves = [{'series_id': SID, 'label': str(index)} for index in range(9)]
    value = ChartDefinition.model_validate(definition(curves=curves[:8], time=time))
    assert value.time.curve_cycles == 'current_and_previous'
    assert len(value.curves) == 8
    with pytest.raises(ValidationError):
        ChartDefinition.model_validate(definition(curves=curves, time=time))
    with pytest.raises(ValidationError):
        ChartDefinition.model_validate(definition(time={**time, 'curve_cycles': 'all'}))
    for mode in ({'mode': 'recent', 'points': 120}, {'mode': 'since', 'start': '2023-01-01'},
                 {'mode': 'range', 'start': '2023-01-01', 'end': '2023-01-03'}):
        assert ChartDefinition.model_validate(definition(time={**mode, 'transform': 'rebase'})).time.transform == 'rebase'


def test_derived_uses_only_valid_common_dates_and_is_not_stored_as_results():
    value = ChartDefinition.model_validate(definition(curves=[derived()]))
    data = {SID: source([('2023-01-01', 10), ('2023-01-02', 14), ('2023-01-03', None), ('2023-01-04', 20)]),
            OTHER: source([('2023-01-02', 4), ('2023-01-03', 5), ('2023-01-04', 8), ('2023-01-05', 9)], limited=True)}
    original = deepcopy((data, value.model_dump()))
    item = resolve_curves(value, FIELDS, data)[0]
    assert item['points'] == [{'date': '2023-01-02', 'value': 10}, {'date': '2023-01-04', 'value': 12}]
    assert item['unit'] == '件' and item['limited'] is True and item['freq_days'] == 2
    assert definition_series_ids(value) == [SID, OTHER]
    assert (data, value.model_dump()) == original
    assert 'points' not in value.model_dump()['curves'][0]


def test_division_ratio_unit_and_zero_denominator_missing_point():
    value = ChartDefinition.model_validate(definition(curves=[derived('div')]))
    fields = deepcopy(FIELDS)
    fields[OTHER]['unit'] = '另一种单位'
    assert unit_notices(value, fields, reject=True) == []
    data = {SID: source([('2023-01-01', 10), ('2023-01-02', 14)]),
            OTHER: source([('2023-01-01', 2), ('2023-01-02', 0)])}
    item = resolve_curves(value, fields, data)[0]
    assert item['unit'] == '倍'
    assert item['points'] == [{'date': '2023-01-01', 'value': 5}, {'date': '2023-01-02', 'value': None}]
    assert item['warnings'] == [{'code': 'division_by_zero', 'date': '2023-01-02'}]


def test_subtraction_units_and_missing_unit_need_explicit_confirmation():
    value = ChartDefinition.model_validate(definition(curves=[derived()]))
    fields = deepcopy(FIELDS)
    fields[OTHER]['unit'] = '元'
    with pytest.raises(HTTPException, match='422'):
        unit_notices(value, fields, reject=True)
    assert unit_notices(value, fields)[0]['code'] == 'derived_unit_invalid'
    assert curve_unit(value.curves[0], fields) == ''
    fields[OTHER]['unit'] = ''
    with pytest.raises(HTTPException, match='422'):
        unit_notices(value, fields, reject=True)
    value.curves[0].derived.unit_confirmed = True
    assert curve_unit(value.curves[0], fields, reject=True) == '件'
    assert unit_notices(value, fields, reject=True)[0]['code'] == 'unit_confirmed'
    fields[SID]['unit'] = ''
    assert unit_notices(value, fields, reject=True)[0]['code'] == 'unit_missing'
    fields.pop(OTHER)
    assert curve_unit(value.curves[0], fields) == ''
    assert unit_notices(value, fields)[0]['code'] == 'missing'


def test_subtraction_changed_units_suppresses_calculation_and_version_change_rejected():
    value = ChartDefinition.model_validate(definition(curves=[derived()]))
    data = {SID: source([('2023-01-01', 10)]), OTHER: source([('2023-01-01', 4)])}
    fields = deepcopy(FIELDS)
    fields[OTHER]['unit'] = '元'
    item = resolve_curves(value, fields, data)[0]
    assert item['status'] == 'invalid' and item['points'] == []
    data[OTHER]['source_version'] = 'changed'
    with pytest.raises(HTTPException) as error:
        resolve_curves(value, FIELDS, data)
    assert error.value.status_code == 409


def test_axis_units_no_conversion_rebase_and_change_rules():
    fields = deepcopy(FIELDS)
    fields[OTHER]['unit'] = '%'
    curves = [{'series_id': SID, 'label': '甲'}, {'series_id': OTHER, 'label': '乙', 'axis': 'right'},
              {'series_id': THIRD, 'label': '丙'}]
    value = ChartDefinition.model_validate(definition(curves=curves))
    with pytest.raises(HTTPException) as error:
        unit_notices(value, fields, reject=True)
    assert 'rebase' in error.value.detail
    value.time.transform = 'change'
    with pytest.raises(HTTPException):
        unit_notices(value, fields, reject=True)
    value.time.transform = 'rebase'
    for curve in value.curves:
        curve.axis = 'left'
    assert unit_notices(value, fields, reject=True) == []


def test_validation_reads_all_real_operands_and_refuses_missing_sources(monkeypatch):
    selected = []
    value = ChartDefinition.model_validate(definition(curves=[derived(), {'series_id': SID, 'label': '原始'}]))
    monkeypatch.setattr(chart_data, 'catalog_fields', lambda scope, ids: selected.extend(ids) or deepcopy(FIELDS))
    assert validate_definition(None, value) == []
    assert selected == [SID, OTHER]
    monkeypatch.setattr(chart_data, 'catalog_fields', lambda scope, ids: {SID: FIELDS[SID]})
    with pytest.raises(HTTPException) as error:
        validate_definition(None, value)
    assert error.value.status_code == 422


def test_derived_cycle_statistics_follow_same_alignment_and_rank_rules():
    template, rows = segment_fixture()
    value = ChartDefinition.model_validate(definition(curves=[derived()], time={'mode': 'cycle', 'template': template}))
    a = [(day.isoformat(), 2 * raw) for day, raw in rows]
    b = [(day.isoformat(), raw) for day, raw in rows]
    curve = resolve_curves(value, FIELDS, {SID: source(a), OTHER: source(b)})[0]
    result = compute_cycles([(p['date'], p['value']) for p in curve['points']], template, '2020-04-08')
    assert result['position']['rank'] == 1 and result['position']['n'] == 3
    assert result['position']['recent_mean'] == 27
    assert result['position']['vs_recent_mean']['value'] == pytest.approx(20 / 27)
    assert result['segments'][-1]['points'][-1]['raw'] == 47
    ratio = ChartDefinition.model_validate(definition(curves=[derived('div')]))
    curve = resolve_curves(ratio, FIELDS, {SID: source(a), OTHER: source(b)})[0]
    result = compute_cycles([(p['date'], p['value']) for p in curve['points']], template, '2020-04-08')
    assert result['position']['rank'] == 1 and result['position']['recent_mean'] == 2


def test_multi_cycles_common_period_no_band_and_statistics_for_each_curve():
    template, rows = segment_fixture()
    curves = [{'series_id': SID, 'label': '第一条', **source([(day.isoformat(), raw) for day, raw in rows])},
              {'series_id': OTHER, 'label': '第二条', 'curve_cycles': 'current_and_previous',
               **source([(day.isoformat(), raw * 3) for day, raw in rows])}]
    result = compute_multi_cycles(curves, template, '2020-04-08', cycles=8)
    assert result['mode'] == 'cycle' and result['band'] == []
    assert result['common_current_start'] == '2020-04-01'
    assert result['common_previous_start'] == '2020-03-01'
    for index, curve in enumerate(result['curves']):
        cycle = curve['cycle']
        assert cycle['band'] == []
        assert cycle['position']['rank'] == 1 and cycle['position']['n'] == 3
        assert cycle['position']['recent_mean'] == (27 if index == 0 else 81)
        assert next(part['start'] for part in cycle['segments'] if part['current']) == '2020-04-01'


def test_multi_cycles_stale_and_missing_periods_do_not_become_current_or_previous():
    template = BUILTIN_TEMPLATES['自然年']
    curves = [{'label': '新数据', **source([('2024-01-01', 1), ('2024-01-02', 2)])},
              {'label': '旧数据', **source([('2021-01-01', 3), ('2021-01-02', 4)])}]
    result = compute_multi_cycles(curves, template, '2024-02-01', cycles=8)
    assert result['common_current_start'] == '2024-01-01'
    assert result['common_previous_start'] == '2023-01-01'
    stale = result['curves'][1]['cycle']
    assert stale['position'] is None and 'current_missing' in stale['warnings']
    assert not any(part['current'] for part in stale['segments'])
    assert stale['previous_start'] == '2023-01-01'
    assert [part['start'] for part in stale['segments']] == ['2021-01-01']


def test_multi_cycle_previous_event_is_calendar_period_even_when_unobserved():
    template = {'kind': 'event', 'name': '事件', 'before_days': 2, 'after_days': 2,
                'anchors': [{'label': str(year), 'date': f'{year}-05-01'} for year in range(2020, 2024)]}
    curves = [{'label': '甲', **source([('2020-05-01', 1), ('2023-05-01', 2)])},
              {'label': '乙', **source([('2023-05-01', 3), ('2023-05-02', 4)])}]
    result = compute_multi_cycles(curves, template, '2023-05-02')
    assert result['common_current_start'] == '2023-04-29'
    assert result['common_previous_start'] == '2022-04-29'


def test_multi_cycle_propagates_read_limit_and_labeled_compute_and_formula_notices():
    curves = [
        {'label': '达到上限', **source([('2023-01-01', 1), ('2023-01-02', 2)], limited=True)},
        {'label': '计算曲线', **source([('2022-01-01', 3), ('2022-01-02', 4)]),
         'warnings': [{'code': 'division_by_zero', 'date': '2023-01-01'}]},
    ]
    result = compute_multi_cycles(curves, BUILTIN_TEMPLATES['自然年'], '2023-01-02')
    assert 'observation_window_limited' in result['curves'][0]['cycle']['warnings']
    assert {'code': 'observation_window_limited', 'label': '达到上限'} in result['warnings']
    assert {'code': 'current_missing', 'label': '计算曲线'} in result['warnings']
    assert {'code': 'division_by_zero', 'date': '2023-01-01', 'label': '计算曲线'} in result['warnings']
    assert {'code': 'insufficient_cycles', 'label': '达到上限'} in result['warnings']
    # Notices remain with their source curve as well as being visible at the top.
    assert result['curves'][1]['warnings'] == curves[1]['warnings']
    assert curves[1]['warnings'][0].get('label') is None


@pytest.mark.parametrize('transform,values', [('change', [1e308, -1e308]), ('rebase', [1e-308, 1e308])])
def test_cycle_transform_overflow_is_missing_and_explicitly_warned(transform, values):
    result = compute_cycles([('2023-01-01', values[0]), ('2023-01-02', values[1])],
                            {**BUILTIN_TEMPLATES['自然年'], 'transform': transform}, '2023-01-02')
    current = result['segments'][-1]
    assert current['points'][-1]['value'] is None
    assert 'invalid_transform' in current['flags']
    assert 'non_finite_transform' in result['warnings']
    assert 'rank' not in result['position']
    json.dumps(result, allow_nan=False)


@pytest.mark.parametrize('op,a,b', [('sub', 1e308, -1e308), ('div', 1e308, 1e-308)])
def test_formula_overflow_is_missing_and_json_safe(op, a, b):
    value = ChartDefinition.model_validate(definition(curves=[derived(op)]))
    item = resolve_curves(value, FIELDS, {SID: source([('2023-01-01', a)]),
                                       OTHER: source([('2023-01-01', b)])})[0]
    assert item['points'] == [{'date': '2023-01-01', 'value': None}]
    assert item['warnings'] == [{'code': 'derived_non_finite', 'date': '2023-01-01'}]
    json.dumps(item, allow_nan=False)
