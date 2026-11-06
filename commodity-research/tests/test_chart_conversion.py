from copy import deepcopy
from datetime import date, timedelta

import pytest

from tin.compute.chart_import import group_suggestions, propose_chart
from tin.compute.cycles import compute_cycles
from tests.chart_fixtures import FIELDS, KEY, OTHER, SID, THIRD, native_chart, native_curve, yearly_native


def test_year_cycle_start_is_inferred_from_real_dates_and_labels_are_not_parsed():
    native = yearly_native()
    original = deepcopy(native)
    result = propose_chart(native, FIELDS)
    assert native == original
    assert result['convertible'] and result['mode'] == 'cycle'
    assert result['definition']['time']['template']['start'] == '01-01'
    assert result['definition']['time']['cycles'] == 3 and len(result['definition']['curves']) == 1
    assert result['source_curves'][0]['label'] == '早段'
    assert result['definition']['origin'] == {'kind': 'native', 'ref': KEY}


def test_mixed_chart_converts_dominant_yearly_series_and_reports_the_other_indicator():
    native = yearly_native()
    native['curves'].insert(0, native_curve(series_id=OTHER, label='其他指标', chart_type='barChart', axis='secondary'))
    original = deepcopy(native)
    result = propose_chart(native, FIELDS)
    assert native == original
    assert result['convertible'] and result['mode'] == 'cycle'
    assert result['definition']['time']['template']['kind'] == 'repeat'
    assert result['definition']['time']['template']['start'] == '01-01'
    assert result['definition']['time']['cycles'] == 3
    assert [curve['series_id'] for curve in result['definition']['curves']] == [SID]
    assert result['definition']['curves'][0]['label'] == FIELDS[SID]['metric']
    assert result['missing_curves'] == [{'index': 0, 'label': '其他指标', 'reason': '周期图一期只保留一个指标'}]
    assert '其他指标（周期图一期只保留一个指标）' in result['definition']['note']
    assert result['source_curves'] == native['curves']


@pytest.mark.parametrize('first_id,second_id', [(SID, OTHER), (OTHER, SID)])
def test_dominant_series_ties_choose_the_group_appearing_first_in_the_chart(first_id, second_id):
    curves = [native_curve(series_id=sid, label=f'曲线{index}-{order}',
                           first_date=f'{year}-01-04', last_date=f'{year}-12-31')
              for index, year in enumerate((2020, 2021, 2022)) for order, sid in enumerate((first_id, second_id))]
    result = propose_chart(native_chart(curves=curves), FIELDS)
    assert result['convertible'] and result['mode'] == 'cycle'
    assert result['definition']['time']['cycles'] == 3
    assert result['definition']['curves'][0]['series_id'] == first_id
    assert [item['index'] for item in result['missing_curves']] == [1, 3, 5]
    assert all(item['reason'] == '周期图一期只保留一个指标' for item in result['missing_curves'])


def test_only_the_largest_series_group_is_considered_for_cycle_detection():
    curves = [native_curve(label=f'重复起点{index}') for index in range(4)]
    curves += [native_curve(series_id=OTHER, first_date=f'{year}-01-04', last_date=f'{year}-12-31')
               for year in (2020, 2021, 2022)]
    result = propose_chart(native_chart(curves=curves), FIELDS)
    assert result['convertible'] and result['mode'] == 'trend'
    assert len(result['definition']['curves']) == 7 and result['missing_curves'] == []


def test_mixed_cycle_missing_curves_keep_original_order_and_existing_reasons():
    native = yearly_native()
    native['curves'][:0] = [native_curve(series_id=OTHER, label='其他指标'),
                            native_curve(status='pending_source', label='待确认', reason='来源待确认')]
    result = propose_chart(native, FIELDS)
    assert result['convertible'] and result['mode'] == 'cycle'
    assert result['missing_curves'] == [
        {'index': 0, 'label': '其他指标', 'reason': '周期图一期只保留一个指标'},
        {'index': 1, 'label': '待确认', 'reason': '来源待确认'}]


def test_september_start_uses_month_first_day_instead_of_september_last_day():
    native = native_chart(curves=[native_curve(label=str(i), first_date=f'{year}-09-30', last_date=f'{year + 1}-09-25')
                                  for i, year in enumerate((2019, 2020, 2021))])
    result = propose_chart(native, FIELDS)
    assert result['definition']['time']['template']['start'] == '09-01'
    override = propose_chart(native, FIELDS, start_override='10-01')
    assert override['definition']['time']['template']['start'] == '10-01'


def test_short_disjoint_segments_preserve_labels_and_dates():
    native = native_chart(curves=[native_curve(label=str(i), first_date=f'2020-{month:02d}-01', last_date=f'2020-{month:02d}-20')
                                 for i, month in enumerate((1, 4, 6))])
    result = propose_chart(native, FIELDS)
    template = result['definition']['time']['template']
    assert template['kind'] == 'segments'
    assert [(s['label'], s['start'], s['end']) for s in template['segments']] == [
        (c['label'], c['first_date'], c['last_date']) for c in native['curves']]


@pytest.mark.parametrize('curves', [
    [native_curve(label='2020'), native_curve(label='2021')],
    [native_curve(), native_curve(first_date='2020-01-04'), native_curve(first_date='2020-11-01')],
    [native_curve(), native_curve(series_id=OTHER), native_curve(series_id=OTHER)],
])
def test_other_structures_are_open_ended_trends_even_when_names_look_like_years(curves):
    result = propose_chart(native_chart(title='年度周期', curves=curves), FIELDS)
    assert result['convertible'] and result['mode'] == 'trend'
    assert result['definition']['time'] == {'mode': 'since', 'start': '2020-01-04', 'transform': 'raw'}
    assert len(result['definition']['curves']) == len(curves)


@pytest.mark.parametrize('count', [12, 16])
def test_trends_with_up_to_sixteen_curves_can_be_converted(count):
    identifiers = ['kb-' + f'{index:048x}' for index in range(count)]
    fields = {sid: {**FIELDS[SID], 'series_id': sid} for sid in identifiers}
    curves = [native_curve(series_id=sid, label=f'曲线{index}') for index, sid in enumerate(identifiers)]
    result = propose_chart(native_chart(curves=curves), fields)
    assert result['convertible'] and result['mode'] == 'trend'
    assert result['definition']['time'] == {'mode': 'since', 'start': '2020-01-04', 'transform': 'raw'}
    assert [curve['series_id'] for curve in result['definition']['curves']] == identifiers
    assert result['missing_curves'] == []


def test_trend_preserves_order_kinds_axes_and_styles_and_lists_missing_curves():
    curves = [native_curve(series_id=OTHER, chart_type='barChart', axis='secondary'),
              native_curve(chart_type='areaChart'), native_curve(series_id=None, status='unconfirmed_sheet', label='未确认曲线')]
    result = propose_chart(native_chart(curves=curves), FIELDS)
    assert result['convertible']
    saved = result['definition']['curves']
    assert [(c['series_id'], c['kind'], c['axis']) for c in saved] == [(OTHER, 'bar', 'right'), (SID, 'area', 'left')]
    assert saved[0]['style'] == {'color': '#C00000', 'width': 2.25, 'dash': 'solid'}
    assert result['missing_curves'][0]['label'] == '未确认曲线'
    assert '未确认曲线' in result['definition']['note']


def test_no_linked_curves_or_disappeared_fields_are_unconvertible():
    result = propose_chart(native_chart(curves=[native_curve(status='pending_source', reason='来源待确认')]), FIELDS)
    assert not result['convertible'] and result['reason'] == '没有可用曲线'
    assert result['missing_curves'][0]['reason'] == '来源待确认'
    assert not propose_chart(native_chart(), {})['convertible']


def test_latest_cycle_warning_requires_a_new_calendar_period_in_observed_data():
    result = propose_chart(yearly_native(), FIELDS, {SID: '2023-02-01'})
    assert result['warnings'] == [{'code': 'latest_cycle_missing', 'label': '2023',
                                   'message': '原图未包含 2023，转换后会显示'}]
    assert propose_chart(yearly_native(end='12-20'), FIELDS, {SID: '2022-12-31'})['warnings'] == []
    assert propose_chart(yearly_native(), FIELDS, {SID: '2023-01-03'})['warnings'] == []
    assert propose_chart(yearly_native(), FIELDS, {SID: '2023-01-04'})['warnings'][0]['code'] == 'latest_cycle_missing'


def test_open_ended_overlapping_native_ranges_still_form_annual_cycles():
    native = yearly_native()
    for curve in native['curves']:
        curve['last_date'] = '2025-12-31'
    result = propose_chart(native, FIELDS)
    assert result['definition']['time']['template']['kind'] == 'repeat'
    assert result['definition']['time']['cycles'] == 3


def test_dominant_start_month_is_preserved_when_annual_origins_skip_years():
    native = native_chart(curves=[native_curve(first_date=start, last_date='2025-12-31', label=str(i))
        for i, start in enumerate(('2019-09-01', '2020-09-01', '2021-08-31'))])
    result = propose_chart(native, FIELDS)
    assert result['definition']['time']['template']['start'] == '09-01'
    native['curves'][2]['first_date'] = '2023-08-31'
    result = propose_chart(native, FIELDS)
    assert result['definition']['time']['template']['kind'] == 'repeat'
    assert result['definition']['time']['template']['start'] == '09-01'


def test_missing_annual_origins_convert_to_repeat_without_fabricating_empty_years():
    years = (2009, 2010, 2011, 2016, 2017, 2018, 2019, 2020, 2021, 2022)
    native = native_chart(curves=[native_curve(label=f'任意段{i}', first_date=f'{year}-08-01',
                                               last_date=f'{year + 1}-07-31')
                                  for i, year in enumerate(years)])
    time = propose_chart(native, FIELDS)['definition']['time']
    assert time['template']['kind'] == 'repeat'
    assert time['template']['start'] == '08-01'
    assert time['cycles'] == len(years)
    observations = [(date(year, 8, 1), float(i)) for i, year in enumerate(years)]
    output = compute_cycles(observations, time['template'], '2023-07-31', cycles=time['cycles'])
    assert [part['start'] for part in output['segments']] == [f'{year}-08-01' for year in years]
    assert output['omitted_segments'] == ['12/13', '13/14', '14/15', '15/16']


@pytest.mark.parametrize('gaps,expected', [
    ((330, 400), 'repeat'),
    ((695, 765), 'repeat'),
    ((329, 365), 'segments'),
    ((401, 365), 'segments'),
    ((694, 730), 'segments'),
    ((766, 730), 'segments'),
])
def test_annual_gap_tolerance_accepts_only_whole_year_multiples(gaps, expected):
    starts = [date(2010, 8, 1)]
    for gap in gaps:
        starts.append(starts[-1] + timedelta(days=gap))
    native = native_chart(curves=[native_curve(label=f'段{i}', first_date=start.isoformat(),
                                               last_date=(start + timedelta(days=900)).isoformat())
                                  for i, start in enumerate(starts)])
    assert propose_chart(native, FIELDS)['definition']['time']['template']['kind'] == expected


def test_segments_cap_every_end_by_typical_start_spacing_and_exclude_the_next_reset():
    starts = [date(2020, 1, 1), date(2020, 10, 27), date(2021, 12, 1)]
    # Gaps of 300 and 400 have a 350-day median but do not form annual cycles.
    native = native_chart(curves=[native_curve(label=f'段{i}', first_date=start.isoformat(),
                                               last_date='2025-12-31') for i, start in enumerate(starts)])
    template = propose_chart(native, FIELDS)['definition']['time']['template']
    assert template['kind'] == 'segments'
    assert [part['end'] for part in template['segments']] == ['2020-10-26', '2021-10-12', '2022-11-16']
    observations = [(starts[-1] + timedelta(days=offset), value)
                    for offset, value in ((0, 1), (349, 2), (350, 3), (351, 0), (352, 100))]
    output = compute_cycles(observations, template, '2023-01-01')
    assert [point['raw'] for point in output['segments'][-1]['points']] == [1, 2, 3]


def test_segment_cap_keeps_earlier_source_end_and_floors_fractional_median_days():
    starts = [date(2020, 1, 1), date(2020, 4, 10), date(2020, 7, 20)]
    native = native_chart(curves=[native_curve(label=f'段{i}', first_date=start.isoformat(),
        last_date=(start + timedelta(days=10 if i == 1 else 500)).isoformat())
        for i, start in enumerate(starts)])
    template = propose_chart(native, FIELDS)['definition']['time']['template']
    assert [part['end'] for part in template['segments']] == ['2020-04-09', '2020-04-20', '2020-10-28']


def test_segments_with_typical_spacing_over_400_days_keep_the_original_end_limits():
    starts = [date(2010, 1, 1), date(2011, 5, 16), date(2013, 1, 5)]
    native = native_chart(curves=[native_curve(label=f'段{i}', first_date=start.isoformat(),
                                               last_date='2016-12-31') for i, start in enumerate(starts)])
    template = propose_chart(native, FIELDS)['definition']['time']['template']
    assert template['kind'] == 'segments'
    assert [part['end'] for part in template['segments']] == ['2011-05-15', '2013-01-04', '2016-12-31']


def test_sheet_group_summary_and_unknown_chart_type():
    suggestions = [propose_chart(yearly_native(), FIELDS), propose_chart(native_chart(chart_key='nc-' + '2' * 32), FIELDS),
                   propose_chart(native_chart(chart_key='nc-' + '3' * 32, curves=[native_curve(chart_type='pieChart')]), FIELDS)]
    group = group_suggestions(suggestions)[0]
    assert (group['total'], group['cycles'], group['trends'], group['unconvertible']) == (3, 1, 1, 1)
    assert group['summary'] == '本表 3 张：1 张周期图（起点 01-01 1 张），1 张走势图，1 张无法转换'


def test_unit_mismatch_does_not_automatically_move_a_curve_or_convert_units():
    native = native_chart(curves=[native_curve(), native_curve(series_id=THIRD)])
    result = propose_chart(native, FIELDS)
    assert not result['convertible'] and '单位不一致' in result['reason']


def test_changing_names_does_not_change_conversion_decision():
    native = yearly_native()
    other = deepcopy(native)
    other.update(title='连续走势', source_file='周期没有含义.xlsx', sheet='任意名字')
    for index, curve in enumerate(other['curves']):
        curve['label'] = f'不含年份{index}'
    original_time = propose_chart(native, FIELDS)['definition']['time']
    renamed_time = propose_chart(other, FIELDS)['definition']['time']
    assert original_time['template'] == renamed_time['template']
    assert original_time['cycles'] == renamed_time['cycles']
    assert [item['label'] for item in renamed_time['segment_styles']] == [c['label'] for c in other['curves']]


@pytest.mark.parametrize('metric', ['目录指标名称', '', None])
def test_cycle_label_and_unit_warning_use_the_catalogue_metric_or_chart_title(metric):
    native = yearly_native(title='原表图标题')
    native['curves'][0]['label'] = '2018'
    fields = {SID: {**FIELDS[SID], 'metric': metric, 'unit': ''}}
    result = propose_chart(native, fields)
    label = metric or native['title']
    assert result['definition']['curves'][0]['label'] == label
    assert result['warnings'] == [{'code': 'unit_missing', 'series_id': SID, 'label': label}]
    assert result['definition']['time']['segment_styles'][0]['label'] == '2018'


@pytest.mark.parametrize('start_dates', [('2020-01-01', '2021-01-01', '2022-01-01'),
                                        ('2020-01-01', '2020-04-01', '2020-06-01')])
def test_cycle_keeps_exact_segment_labels_and_individual_native_styles(start_dates):
    labels = ['20/21', '不解释为年份', ' 末段 ']
    styles = [{'color': '#12aBcD', 'width_pt': 1.25, 'dash': 'dot'},
              {'color': '#987654', 'width_pt': 3, 'dash': 'dash'},
              {'color': None, 'width_pt': None, 'dash': None}]
    native = native_chart(curves=[native_curve(label=label, first_date=start, last_date='2023-12-31', style=style)
                                  for label, start, style in zip(labels, start_dates, styles)])
    original = deepcopy(native)
    result = propose_chart(native, FIELDS)
    assert result['convertible'] and native == original
    assert result['definition']['time']['segment_styles'] == [
        {'label': label, 'color': style['color'], 'width': style['width_pt'], 'dash': style['dash']}
        for label, style in zip(labels, styles)]


def test_sheet_summary_counts_charts_by_start_even_when_their_cycle_counts_differ():
    yearly = propose_chart(yearly_native(), FIELDS)
    longer = deepcopy(yearly)
    longer['definition']['time']['cycles'] = 8
    september = propose_chart(yearly_native(start='09-01'), FIELDS)
    manual = propose_chart(native_chart(curves=[native_curve(first_date=f'2020-{month:02d}-01', label=str(month))
                                               for month in (1, 4, 6)]), FIELDS)
    trend = propose_chart(native_chart(), FIELDS)
    unavailable = propose_chart(native_chart(curves=[native_curve(status='pending_source')]), FIELDS)
    suggestions = [deepcopy(chart) for chart, count in ((yearly, 15), (longer, 15), (september, 12),
                                                      (manual, 4), (trend, 1), (unavailable, 86)) for _ in range(count)]
    group = group_suggestions(suggestions)[0]
    assert group['summary'] == ('本表 133 张：46 张周期图（起点 01-01 30 张、起点 09-01 12 张、原表分段 4 张），'
                                '1 张走势图，86 张无法转换')


def test_seasonal_columns_with_shared_dates_stay_trends_and_keep_each_year_column():
    identifiers = (SID, OTHER, THIRD)
    native = native_chart(curves=[native_curve(series_id=sid, label=str(2020 + index))
                                  for index, sid in enumerate(identifiers)])
    # Different columns are distinct real series, regardless of their year-like legends.
    result = propose_chart(native, {sid: {**FIELDS[sid], 'unit': '件'} for sid in identifiers})
    assert result['convertible'] and result['mode'] == 'trend'
    assert result['definition']['time'] == {'mode': 'since', 'start': '2020-01-04', 'transform': 'raw'}
    assert [c['series_id'] for c in result['definition']['curves']] == list(identifiers)
    assert [c['label'] for c in result['definition']['curves']] == ['2020', '2021', '2022']
