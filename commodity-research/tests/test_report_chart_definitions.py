"""Report structures use shown IDs and the shared chart definition validator."""
from copy import deepcopy

from fastapi import HTTPException
import pytest

from tin.schemas.chart import MAX_VISIBLE_CYCLES, ChartDefinition, RepeatTemplate
from tin.web import chart_data, report_chart_definitions as report
from tests.chart_fixtures import FIELDS, SID, OTHER, THIRD, definition
from tests.test_chart_api import chart_client, create


def recognized(*identifiers):
    return {'title': '原文图标题', 'series': [
        {'name': f'原文图例{index}', 'unit': '不能代替真实单位',
         'candidates': [{'id': identifier, 'name': '本次展示的真实字段'}]}
        for index, identifier in enumerate(identifiers)]}


@pytest.fixture
def fields(monkeypatch):
    current = deepcopy(FIELDS)
    def catalog(scope, identifiers):
        return {identifier: current[identifier] for identifier in identifiers if identifier in current}
    monkeypatch.setattr(report, 'catalog_fields', catalog)
    monkeypatch.setattr(chart_data, 'catalog_fields', catalog)
    return current


def build(chart, mappings, kind='trend'):
    return report.build_definition(None, chart, mappings, kind, '往期周报.pdf', 'report:test')


def test_original_titles_legends_current_units_and_empty_note(fields):
    result = build(recognized(SID, THIRD), [SID, THIRD], 'comparison')
    assert result.title == '原文图标题' and result.note == ''
    assert result.group == '往期周报.pdf' and result.origin.kind == 'ppt'
    assert result.time.mode == 'recent' and result.time.transform == 'raw'
    assert [(curve.label, curve.axis) for curve in result.curves] == [('原文图例0', 'left'), ('原文图例1', 'right')]


def test_duplicate_real_bindings_collapse_for_every_chart_type(fields):
    chart = recognized(SID, SID, OTHER)
    result = build(chart, [SID, SID, OTHER], 'seasonal')
    assert [curve.series_id for curve in result.curves] == [SID, OTHER]
    assert result.time.mode == 'cycle' and result.time.template.name == '自然年'
    assert result.time.template.period == 'year' and result.time.template.start == '01-01'
    assert result.time.template.transform == 'raw'
    assert len(build(chart, [SID, SID, OTHER], 'trend').curves) == 2
    assert len(build(chart, [SID, SID, OTHER], 'comparison').curves) == 2
    assert report.mapping_warnings([SID, SID, OTHER]) == [
        '重复映射已合并：3条原文系列映射到2个指标，定义已去重；请核对指标口径']
    assert report.mapping_warnings([SID, OTHER, None]) == []


def test_model_grouped_period_legends_form_one_metric_cycle(fields):
    chart = recognized(SID)
    chart['legend_semantics'] = 'periods'
    chart['series'][0].update(name='原文指标价格', legends=['2019年度', '2020年度', '2021年度'])
    result = build(chart, [SID], 'seasonal')
    assert len(result.curves) == 1
    assert result.curves[0].series_id == SID and result.curves[0].label == '原文指标价格'
    assert result.time.mode == 'cycle' and result.time.template.period == 'year'
    assert result.time.cycles == 8 and result.time.visible_cycles is None
    assert chart['series'][0]['legends'] == ['2019年度', '2020年度', '2021年度']


def test_multiple_model_grouped_metrics_keep_independent_cycle_bindings(fields):
    chart = recognized(SID, OTHER)
    chart['legend_semantics'] = 'periods'
    chart['series'][0].update(name='原文指标甲', legends=['甲上一期', '甲本期'])
    chart['series'][1].update(name='原文指标乙', legends=['乙上一期', '乙本期'])
    result = build(chart, [SID, OTHER], 'seasonal')
    assert [curve.series_id for curve in result.curves] == [SID, OTHER]
    assert [curve.label for curve in result.curves] == ['原文指标甲', '原文指标乙']


def test_grounded_season_start_and_labels_define_repeat_window_without_axis_override(fields):
    chart = recognized(SID)
    chart['shape'] = {'season_start_month': 9, 'period_kind': 'crop_year',
        'period_labels': ['19/20', '20/21', '21/22'], 'y_ticks': {'min': '20', 'max': '100'}}
    result = build(chart, [SID], 'seasonal')
    assert isinstance(result.time.template, RepeatTemplate)
    assert result.time.template.start == '09-01' and result.time.template.name == '9月起始年度'
    assert (result.time.template.period, result.time.template.align, result.time.template.transform) == (
        'year', 'days', 'raw')
    assert result.time.cycles == result.time.visible_cycles == 3
    assert result.axes.left.min is None and result.axes.left.max is None
    assert build(chart, [SID], 'trend').time.mode == 'recent'


def test_period_count_respects_chart_limit_and_contracts_keep_natural_year(fields):
    chart = {**recognized(SID), 'chart_type': 'seasonal', 'shape': {'season_start_month': 9,
        'period_kind': 'contract', 'period_labels': [f'{i:02d}05' for i in range(MAX_VISIBLE_CYCLES + 2)]}}
    result = build(chart, [SID], 'seasonal')
    assert result.time.template.start == '01-01' and result.time.template.name == '自然年'
    assert result.time.cycles == result.time.visible_cycles == MAX_VISIBLE_CYCLES
    assert report.shape_warnings(chart) == ['原图按合约分年（如 1905），重画按年度对齐，曲线分段可能不同']


@pytest.mark.parametrize('values,mismatch', [([101, 121], True), ([100, 120], False),
    ([-200, 301], True), ([-200, 300], False), ([0, 19.9], True), ([0, 20], False), ([], False)])
def test_axis_range_checks_overlap_and_strict_span_boundaries(fields, values, mismatch):
    chart = recognized(SID, THIRD)
    chart['shape'] = {'y_ticks': {'min': '0', 'max': '100'}}
    value = build(chart, [SID, THIRD], 'comparison')
    payload = {'curves': [{'axis': 'left', 'points': [{'value': n} for n in values]},
                         {'axis': 'right', 'points': [{'value': 10_000}]}]}
    warning = report.range_warning(None, chart, value, payload=payload)
    assert bool(warning) is mismatch
    if warning:
        assert '数值范围与原图刻度不符（原图 0–100，当前数据 ' in warning


def test_range_check_reads_only_authorized_observations_in_seasonal_window(chart_client):
    from tin.web.charts import require_scope
    from tin.web.report_chart_import import annotate

    client, state = chart_client
    state.observations[SID] = [('2019-12-01', 10_000), ('2020-09-01', 20), ('2021-08-31', 80),
                              ('2021-09-01', 30), ('2022-01-01', 50)]
    chart = recognized(SID)
    chart.update(id='chart', chart_type='seasonal')
    chart['series'][0]['selected_id'] = SID
    chart['shape'] = {'season_start_month': 9, 'period_labels': ['20/21', '21/22'],
        'y_ticks': {'min': '0', 'max': '100'}}

    @state.app.get('/test/report-range')
    def check():
        return annotate(require_scope(), [deepcopy(chart)], '', 'test')[0]

    response = client.get('/test/report-range')
    assert response.status_code == 200 and response.json()['range_mismatch'] is False
    assert not response.json()['shape_warnings']
    assert any(not request.get('catalog_only') for _, request in state.requests)
    state.observations[SID][-1] = ('2022-01-01', 900)
    mismatch = client.get('/test/report-range').json()
    assert mismatch['range_mismatch'] is True and not mismatch['definition_failed']
    assert '当前数据 20–900' in mismatch['shape_warnings'][0]


def test_missing_indicator_is_explained_without_dropping_other_bindings(fields):
    chart = recognized(SID, OTHER)
    with pytest.raises(HTTPException) as caught:
        build(chart, [SID, None], 'comparison')
    assert caught.value.detail == '指标库中未找到，请手动选择或跳过'


def test_deduplication_does_not_bypass_each_legends_candidate_check(fields):
    with pytest.raises(HTTPException) as caught:
        build(recognized(SID, OTHER), [SID, SID], 'seasonal')
    assert '本次展示' in caught.value.detail


@pytest.mark.parametrize('frequency', ['daily', 'weekly', 'monthly', 'quarterly', 'annual', '未知', ''])
def test_true_frequency_metadata_uses_existing_year_template_without_guessing_event(fields, frequency):
    fields[SID]['frequency'] = frequency
    result = build(recognized(SID), [SID], 'seasonal')
    assert result.time.template.kind == 'repeat' and result.time.template.name == '自然年'


@pytest.mark.parametrize(('mappings', 'kind'), [([], 'trend'), ([None], 'trend'), ([OTHER], 'trend'),
    (['invented'], 'trend'), ([SID], 'unrecognized'), ([SID, OTHER], 'trend')])
def test_unshown_or_incomplete_ids_and_invalid_type_rejected(fields, mappings, kind):
    with pytest.raises(HTTPException) as caught:
        build(recognized(SID), mappings, kind)
    assert caught.value.status_code == 422


def test_candidate_from_another_legend_cannot_be_substituted(fields):
    with pytest.raises(HTTPException) as caught:
        build(recognized(SID, OTHER), [OTHER, SID])
    assert '本次展示' in caught.value.detail


def test_real_source_disappearance_is_not_repaired_by_descriptor(fields):
    chart = recognized(SID)
    del fields[SID]
    with pytest.raises(HTTPException) as caught:
        build(chart, [SID])
    assert caught.value.status_code == 422 and '来源目录' in caught.value.detail


def test_three_real_units_fail_but_missing_unit_is_not_invented(fields):
    fields[OTHER]['unit'] = '吨'
    with pytest.raises(HTTPException) as caught:
        build(recognized(SID, OTHER, THIRD), [SID, OTHER, THIRD], 'comparison')
    assert caught.value.status_code == 422 and '两种真实单位' in caught.value.detail
    fields[OTHER]['unit'] = ''
    result = build(recognized(SID, OTHER, THIRD), [SID, OTHER, THIRD], 'comparison')
    assert [curve.axis for curve in result.curves] == ['left', 'left', 'right']


def test_shared_definition_errors_are_readable_and_title_is_not_silently_truncated(fields):
    chart = recognized(SID)
    chart['title'] = '原' * 121
    with pytest.raises(HTTPException) as caught:
        build(chart, [SID])
    assert caught.value.status_code == 422 and 'title' in caught.value.detail


def test_shared_validator_runs_for_generated_definition(fields, monkeypatch):
    def reject(scope, definition):
        raise HTTPException(422, '当前真实来源不允许该定义')
    monkeypatch.setattr(report, 'validate_definition', reject)
    with pytest.raises(HTTPException) as caught:
        build(recognized(SID), [SID])
    assert caught.value.detail == '当前真实来源不允许该定义'


def test_duplicate_trend_type_is_independent_of_date_window():
    recent = ChartDefinition.model_validate(definition())
    ranged = ChartDefinition.model_validate({**recent.model_dump(),
        'time': {'mode': 'since', 'start': '2020-01-01'}})
    assert report.duplicate_signature(recent) == report.duplicate_signature(ranged)


def test_duplicate_query_uses_real_id_set_and_time_mode_with_workbench_scope(chart_client):
    client, state = chart_client
    first = create(client)
    create(client, title='另一个指标', curves=[{'series_id': OTHER, 'label': '同名无意义'}])
    create(client, time={'mode': 'cycle', 'template': {'kind': 'repeat', 'name': '自然年',
        'start': '01-01', 'period': 'year'}})
    @state.app.post('/test/report-duplicates')
    def duplicates(body: ChartDefinition):
        from tin.web.charts import require_scope
        return report.duplicate_ids(require_scope(), body)
    value = definition(title='标题不同也可能重复')
    assert client.post('/test/report-duplicates', json=value).json() == [first['id']]
    assert client.post('/test/report-duplicates', json=value,
        headers={'x-workbench': '10000000-0000-4000-8000-000000000092'}).json() == []
