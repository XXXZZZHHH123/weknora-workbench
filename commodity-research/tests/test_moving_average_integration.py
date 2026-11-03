"""Generic typed MA plans, source evidence and deterministic boundary evaluation."""
from datetime import date
from types import SimpleNamespace

from jsonschema import Draft202012Validator
import pytest

from tin.compute.moving_average import sma_source_window
from tin.export.board import threshold_radar, threshold_radar_values
from tin.integration.page_facts import _judgment_windows
from tin.judgments.analysis import AnalysisError, validate_analysis, assess_draft
from tin.judgments.markdown import dump_markdown
from tin.judgments.output_schema import OUTPUT_SCHEMA
from tests.test_moving_average import facts, point, SID, DAY

CATALOG = [{'id': SID, 'name': '合成观测', 'unit': 'kg', 'frequency': '日'}]

def body(window=3, operator='<'):
    return {'judgment': {'contradiction': {}, 'thresholds': [{'id': 'moving', 'name': '均线观察',
        'series_id': SID, 'operator': operator, 'value': {'kind': 'sma', 'window': window},
        'type': '观察位', 'horizon': '短期'}]}, 'binding_units': {SID: 'kg'},
        'rule_quotes': {'moving': '合成观测低于3日均线时复核'}, 'unresolved': []}

def parse(document=None, original=None, catalog=None):
    document = body() if document is None else document
    return validate_analysis(document, original or document['rule_quotes']['moving'], catalog or CATALOG,
        variety='TEST', actor='合成研究员', written_at=date(2031, 1, 1)).payload

def test_actual_prompt_schema_accepts_typed_ma_without_fact_or_zero_placeholder():
    document = body(); Draft202012Validator(OUTPUT_SCHEMA).validate(document)
    payload = parse(document)
    assert payload.thresholds[0].model_dump()['value'] == {'kind': 'sma', 'window': 3}
    assert _judgment_windows(payload) == {SID: 3}

@pytest.mark.parametrize('value', [{'kind': 'ema', 'window': 3}, {'kind': 'sma', 'window': 3.1},
                                   {'kind': 'sma', 'window': True}, {'kind': 'sma', 'window': 121}])
def test_invalid_derived_target_does_not_bypass_model_document_validation(value):
    document = body(); document['judgment']['thresholds'][0]['value'] = value
    assert not Draft202012Validator(OUTPUT_SCHEMA).is_valid(document)
    with pytest.raises(AnalysisError): parse(document)

@pytest.mark.parametrize('quote', ['低于2日均线时复核', '低于3吨时复核', '低于EMA3时复核', '低于MA3.5时复核'])
def test_unproven_window_or_other_formula_remains_nonexecutable(quote):
    document = body(); document['rule_quotes']['moving'] = quote
    with pytest.raises(AnalysisError): parse(document)
    assessed = assess_draft(document, quote, CATALOG, variety='TEST', actor='合成研究员')
    assert assessed.result.payload.thresholds == [] and assessed.issues

def test_day_operand_cannot_use_unknown_or_monthly_metadata():
    for frequency in [None, '', '月', 'monthly']:
        with pytest.raises(AnalysisError): parse(catalog=[{**CATALOG[0], 'frequency': frequency}])
    document = body(); document['rule_quotes']['moving'] = '低于MA3时复核'
    assert parse(document, catalog=[{**CATALOG[0], 'frequency': '月'}]).thresholds

def test_short_quote_cannot_truncate_a_different_period_in_the_full_source():
    document = body(); document['rule_quotes']['moving'] = 'MA3'
    with pytest.raises(AnalysisError): parse(document, original='低于MA30时复核')
    assert not sma_source_window('MA3', 3, '低于MA30时复核')

def test_exported_typed_rule_round_trips_and_keeps_the_complete_operand():
    payload = parse(); markdown = dump_markdown(payload)
    line = next(line for line in markdown.splitlines() if line.startswith('| 均线观察'))
    document = body(); document['rule_quotes']['moving'] = line
    assert parse(document, markdown).thresholds[0].model_dump()['value'] == {'kind': 'sma', 'window': 3}
    document['judgment']['thresholds'][0]['value']['window'] = 4
    with pytest.raises(AnalysisError): parse(document, markdown)

def test_radar_comparison_uses_exact_sum_when_display_average_rounds_to_the_latest_value(facts):
    for day, value in enumerate([1.0000000000000002, 1, 1], 1): point(facts, day, value)
    judgment = SimpleNamespace(thresholds=body()['judgment']['thresholds'])
    row = threshold_radar(facts, 'TEST', DAY, judgment=judgment)[0]
    assert row['target_value'] == 1.0
    assert row['value'] == 1 and row['state'] == '已触发'
    assert row['dynamic_target'] == {'kind': 'sma', 'window': 3, 'basis': 'observations'}
    assert row['condition'] == '< MA3' and row['progress'] == 100

@pytest.mark.parametrize('operator,expected', [('>', '未触发'), ('>=', '已触发'), ('<', '未触发'), ('<=', '已触发')])
def test_strict_and_inclusive_equal_boundaries_preserve_operator_semantics(facts, operator, expected):
    for day in (1,2,3): point(facts, day, 0.1)
    rule = body(operator=operator)['judgment']['thresholds'][0]
    assert threshold_radar(facts, 'TEST', DAY, judgment=SimpleNamespace(thresholds=[rule]))[0]['state'] == expected

def test_incomplete_and_invalid_windows_have_no_fake_progress_or_computed_value(facts):
    judgment = SimpleNamespace(thresholds=body()['judgment']['thresholds'])
    point(facts, 1, 5); point(facts, 2, 8)
    row = threshold_radar(facts, 'TEST', DAY, judgment=judgment)[0]
    assert row['state'] == '数据缺失' and row['value'] is None and row['target_value'] is None
    assert row['progress'] == 0 and '只有2次' in row['distance']
    point(facts, 3, 9, status='无效'); row = threshold_radar(facts, 'TEST', DAY, judgment=judgment)[0]
    assert row['state'] == '数据缺失' and row['value'] is None and row['target_value'] is None
    assert '无效' in row['distance']

def test_permission_bounded_preview_uses_same_exact_arithmetic_and_rejects_gaps():
    values = {SID: {'name': '合成观测', 'unit': 'kg', 'date': DAY.isoformat(), 'value': 1,
        'points': [{'value': 1.0000000000000002}, {'value': 1}, {'value': 1}]}}
    judgment = SimpleNamespace(thresholds=body()['judgment']['thresholds'])
    assert threshold_radar_values(judgment, values)[0]['state'] == '已触发'
    values[SID]['points'][1]['value'] = None
    assert threshold_radar_values(judgment, values)[0]['state'] == '数据缺失'

def test_preview_requests_only_bound_rules_and_each_necessary_window(monkeypatch):
    from tin.web import judgment_radar as radar
    requests = []
    def read(scope, ids, catalog, day, points, **kwargs):
        assert kwargs == {'include_invalid': True}
        requests.append((ids, points))
        return [{'series_id': SID, 'name': '合成观测', 'unit': 'kg', 'as_of': DAY.isoformat(),
                 'latest_value': 1, 'points': [{'value': value} for value in [3,2,1]]}]
    monkeypatch.setattr(radar, 'read_series', read)
    rows = radar.read_radar(SimpleNamespace(scope_version=2), body()['judgment']['thresholds'], CATALOG, DAY)
    assert requests == [([SID], 3)] and rows[0]['state'] == '已触发'

def test_legacy_average_requires_explicit_complete_window_transport(monkeypatch):
    from tin.web import judgment_radar as radar
    def read(scope, identifiers, catalog, day, points, **kwargs):
        assert points == 3 and identifiers == [SID] and kwargs == {'include_invalid': True}
        return [{'series_id': SID, 'name': '合成观测', 'unit': 'kg', 'as_of': DAY.isoformat(),
                 'latest_value': 1, 'points': [{'value': 3}, {'value': None}, {'value': 1}]}]
    monkeypatch.setattr(radar, 'read_series', read)
    rows = radar.read_radar(SimpleNamespace(scope_version=1), body()['judgment']['thresholds'], CATALOG, DAY)
    assert rows[0]['state'] == '数据缺失'
