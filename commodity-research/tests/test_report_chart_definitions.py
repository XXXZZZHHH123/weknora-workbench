"""Report structures use shown IDs and the shared chart definition validator."""
from copy import deepcopy

from fastapi import HTTPException
import pytest

from tin.schemas.chart import ChartDefinition
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


def test_seasonal_year_legends_collapse_only_explicit_same_real_binding(fields):
    chart = recognized(SID, SID, OTHER)
    result = build(chart, [SID, SID, OTHER], 'seasonal')
    assert [curve.series_id for curve in result.curves] == [SID, OTHER]
    assert result.time.mode == 'cycle' and result.time.template.name == '自然年'
    assert result.time.template.period == 'year' and result.time.template.start == '01-01'
    assert result.time.template.transform == 'raw'
    assert len(build(chart, [SID, SID, OTHER], 'trend').curves) == 3


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
