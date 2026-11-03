"""Model-declared crossing events preserve prior and current observation windows."""
from copy import deepcopy
from datetime import date
from types import SimpleNamespace

from jsonschema import Draft202012Validator
import pytest
from pydantic import ValidationError

from tin.export.board import threshold_radar_values
from tin.integration.page_facts import _judgment_windows
from tin.judgments.analysis import AnalysisError, assess_draft, validate_analysis
from tin.judgments.markdown import dump_markdown
from tin.judgments.output_schema import OUTPUT_SCHEMA
from tin.schemas.judgment import Threshold
from tests.test_moving_average import facts

SID = 'NEW.CROSS'
OTHER = 'NEW.RISK'
DAY = date(2032, 1, 8)
CATALOG = [{'id': SID, 'name': '合成基准', 'unit': '点', 'frequency': '日'},
           {'id': OTHER, 'name': '合成风险', 'unit': '点', 'frequency': '日'}]


def rule(operator='crosses_below', window=2):
    return {'id': 'cross_check', 'name': '均线交叉', 'series_id': SID, 'operator': operator,
            'value': {'kind': 'sma', 'window': window}, 'type': '风控线', 'horizon': '短期',
            'action_hint': '减少暴露'}


def observations(items):
    return {SID: {'name': '合成基准', 'unit': '点', 'date': DAY.isoformat(), 'value': items[-1],
                  'points': [{'value': value, 'unit': '点'} for value in items]}}


def radar(items, operator='crosses_below', window=2):
    return threshold_radar_values(SimpleNamespace(thresholds=[rule(operator, window)]), observations(items))[0]


def document(operator='crosses_below', window=2):
    direction = '上穿' if operator == 'crosses_above' else '下穿'
    quote = f'合成基准{direction}{window}日均线时减少暴露'
    return {'judgment': {'contradiction': {}, 'thresholds': [rule(operator, window)]},
            'binding_units': {SID: '点'}, 'rule_quotes': {'cross_check': quote}, 'unresolved': []}


def parse(body, source=None):
    return validate_analysis(body, source or body['rule_quotes']['cross_check'], CATALOG,
        variety='TEST', actor='合成研究员', written_at=DAY).payload


@pytest.mark.parametrize('operator', ['crosses_below', 'crosses_above'])
def test_crossing_is_a_typed_rule_and_requires_one_more_observation(operator):
    body = document(operator, 120)
    Draft202012Validator(OUTPUT_SCHEMA).validate(body)
    payload = parse(body)
    assert _judgment_windows(payload) == {SID: 121}
    assert payload.thresholds[0].operator == operator


@pytest.mark.parametrize('value', [0, [1, 2]])
def test_crossing_cannot_be_silently_compared_to_a_fixed_number(value):
    candidate = {**rule(), 'value': value}
    with pytest.raises(ValidationError): Threshold.model_validate(candidate)
    body = document(); body['judgment']['thresholds'] = [candidate]
    assert not Draft202012Validator(OUTPUT_SCHEMA).is_valid(body)


@pytest.mark.parametrize('items,operator,expected', [
    ([1, 1, 0], 'crosses_below', '已触发'),
    ([1, 2, 0], 'crosses_below', '已触发'),
    ([3, 2, 1], 'crosses_below', '未触发'),
    ([1, 1, 2], 'crosses_above', '已触发'),
    ([0, 1, 2], 'crosses_above', '未触发'),
    ([1, 1, 1], 'crosses_below', '未触发'),
    ([1, 1, 1], 'crosses_above', '未触发'),
])
def test_event_is_only_the_new_crossing_not_a_persistent_level(items, operator, expected):
    row = radar(items, operator)
    assert row['state'] == expected
    assert row['condition'] == ('下穿 MA2' if operator == 'crosses_below' else '上穿 MA2')
    assert 0 <= row['progress'] <= 100


def test_crossing_uses_exact_sums_when_display_average_rounds_to_current_value():
    row = radar([1.0000000000000002, 1.0000000000000002, 1])
    assert row['target_value'] == 1.0 and row['value'] == 1
    assert row['state'] == '已触发'


@pytest.mark.parametrize('items', [[1, 0], [None, 1, 0], [1, None, 0]])
def test_incomplete_or_invalid_previous_window_is_missing_not_a_crossing(items):
    row = radar(items)
    assert row['state'] == '数据缺失' and row['progress'] == 0
    assert row['target_value'] is None


def test_previous_window_unit_change_cannot_create_an_event():
    values = observations([1, 1, 0]); values[SID]['points'][0]['unit'] = 'kg'
    row = threshold_radar_values(SimpleNamespace(thresholds=[rule()]), values)[0]
    assert row['state'] == '数据缺失' and row['progress'] == 0


def test_crossing_rule_round_trip_preserves_event_operator_and_window():
    body = document(); payload = parse(body)
    markdown = dump_markdown(payload)
    line = next(line for line in markdown.splitlines() if line.startswith('| 均线交叉'))
    body['rule_quotes']['cross_check'] = line
    assert parse(body, markdown).thresholds[0].operator == 'crosses_below'
    changed = deepcopy(body); changed['judgment']['thresholds'][0]['operator'] = '<'
    with pytest.raises(AnalysisError): parse(changed, markdown)


def test_preview_fetches_only_the_extra_needed_point_for_crossing(monkeypatch):
    from tin.web import judgment_radar as preview
    calls = []
    def read(scope, ids, catalog, day, count, **options):
        calls.append((ids, count, options))
        return [{'series_id': SID, 'name': '合成基准', 'unit': '点', 'as_of': DAY.isoformat(),
                 'latest_value': 0, 'points': observations([1,1,0])[SID]['points']}]
    monkeypatch.setattr(preview, 'read_series', read)
    result = preview.read_radar(SimpleNamespace(scope_version=2), [rule()], CATALOG, DAY)
    assert calls == [([SID], 3, {'include_invalid': True})]
    assert result[0]['state'] == '已触发'


def test_or_branches_can_be_independent_monitors_with_the_same_explicit_action():
    body = document()
    body['judgment']['thresholds'].append({'id': 'risk_check', 'name': '合成风险警戒', 'series_id': OTHER,
        'operator': '>', 'value': 17, 'type': '风控线', 'horizon': '短期', 'action_hint': '减少暴露'})
    body['binding_units'][OTHER] = '点'
    body['rule_quotes']['cross_check'] = '合成基准下穿2日均线'
    body['rule_quotes']['risk_check'] = '合成风险>17'
    source = '合成风险>17或合成基准下穿2日均线时减少暴露'
    payload = parse(body, source)
    values = observations([3,2,1]); values[OTHER] = {'name': '合成风险', 'unit': '点',
        'date': DAY.isoformat(), 'value': 18, 'points': [{'value': 18}]}
    rows = threshold_radar_values(SimpleNamespace(thresholds=payload.model_dump()['thresholds']), values)
    assert [row['state'] for row in rows] == ['未触发', '已触发']
    assert {row['action_hint'] for row in rows} == {'减少暴露'}
    # A malformed second branch must not discard the first valid source rule.
    body['binding_units'][OTHER] = '错误单位'
    assessed = assess_draft(body, source, CATALOG, variety='TEST', actor='合成研究员')
    assert [entry.id for entry in assessed.result.payload.thresholds] == ['cross_check']


def test_database_radar_crossing_uses_adjacent_distinct_observations_and_newest_revisions(facts):
    from tin.export.board import threshold_radar
    from tests.test_moving_average import point, SID as LOCAL_SID, DAY as LOCAL_DAY
    point(facts, 1, 8); point(facts, 2, 8); point(facts, 3, 9)
    point(facts, 3, 7, revision=1)
    candidate = {**rule(), 'series_id': LOCAL_SID}
    row = threshold_radar(facts, 'TEST', LOCAL_DAY, judgment=SimpleNamespace(thresholds=[candidate]))[0]
    assert row['state'] == '已触发' and row['value'] == 7 and row['target_value'] == 7.5
    point(facts, 4, 6)
    row = threshold_radar(facts, 'TEST', LOCAL_DAY, judgment=SimpleNamespace(thresholds=[candidate]))[0]
    assert row['state'] == '未触发', 'Remaining below the moving average is not a second crossing'


def test_maximum_window_uses_exactly_121_points_and_no_fabricated_history():
    assert radar([1] * 120 + [0], window=120)['state'] == '已触发'
    assert radar([1] * 119 + [0], window=120)['state'] == '数据缺失'


def test_report_watch_report_view_and_model_brief_use_readable_crossing_conditions():
    from tin.web.report_watch import present_watch, watch_items
    from tin.web.report_view import _judgment_view
    from tin.brief.prompt import _threshold_line
    body = document(); payload = parse(body).model_dump(mode='json')
    watch = present_watch(watch_items(payload), {SID: '合成基准'}, {SID: '点'})
    assert '合成基准下穿 MA2' in watch[0]['text']
    view = _judgment_view(payload, {SID: '合成基准'}, [])
    assert view['thresholds'][0]['condition'] == '下穿 MA2'
    brief = _threshold_line(body['judgment']['thresholds'][0])
    assert '下穿 MA2' in brief and 'crosses_below' not in brief and "'kind'" not in brief


def test_inline_schema_and_full_schema_both_accept_crossing_and_reject_fixed_substitutes():
    from tin.judgments.prompt import THRESHOLD_SCHEMA
    checker = Draft202012Validator(THRESHOLD_SCHEMA)
    checker.validate(rule())
    assert not checker.is_valid({**rule(), 'value': 0})
