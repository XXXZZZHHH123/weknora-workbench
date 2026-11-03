"""Only a rule-local explicit cadence can fill missing source metadata."""
from copy import deepcopy
from types import SimpleNamespace

import pytest

from tin.compute.rule_caliber import frequency_kind, rule_frequency


def rule(frequency='周', series_id='synthetic-series'):
    return {'series_id': series_id, 'caliber_confirmation': {
        'series_id': 'synthetic-series', 'unit': 'kg', 'basis': 'Synthetic table metadata checked',
        'scope_note': '', 'frequency': frequency, 'confirmed_by': 'synthetic-owner',
        'confirmed_at': '2035-01-02T03:04:05+00:00', 'catalog_version': 'synthetic-catalog'}}


@pytest.mark.parametrize('source', [None, '', '—', '未注明', '未确认'])
def test_missing_source_cadence_is_filled_for_only_this_rule_without_mutation(source):
    value = rule()
    before = deepcopy(value)
    assert rule_frequency(value, source) == '周'
    assert rule_frequency({'series_id': 'synthetic-series'}, source) is None
    assert value == before


@pytest.mark.parametrize('source', ['周', '周度', 'weekly', '1w'])
def test_registered_same_cadence_keeps_its_original_label(source):
    assert rule_frequency(rule(), source) == source


@pytest.mark.parametrize('source', ['日', '月', '季度', '年', 'hourly', 'nonstandard'])
def test_confirmation_cannot_overwrite_registered_different_or_unknown_cadence(source):
    with pytest.raises(ValueError):
        rule_frequency(rule(), source)


def test_confirmation_cannot_be_borrowed_by_another_series():
    with pytest.raises(ValueError):
        rule_frequency(rule(series_id='other-series'), None)


def test_no_cadence_is_inferred_from_the_indicator_title_or_free_text():
    assert frequency_kind('Synthetic weekly stock / weekly') is None
    assert frequency_kind('weekly') == 'week'


def test_old_unit_only_declarations_do_not_supply_a_frequency():
    value = rule()
    value['caliber_confirmation'].pop('frequency')
    assert rule_frequency(value, None) is None
    assert rule_frequency(value, '日') == '日'


@pytest.mark.parametrize('frequency,state', [(None, '已触发'), ('周度', '已触发'), ('日', '数据缺失')])
def test_saved_threshold_checks_current_source_frequency_again_without_mutating_it(frequency, state):
    from tin.export.board import threshold_radar_values
    threshold = {**rule(), 'id': 'synthetic-boundary', 'name': 'Synthetic boundary',
        'operator': '>', 'value': 12, 'type': '观察位', 'horizon': '短期'}
    values = {'synthetic-series': {'name': 'Synthetic measurement', 'unit': 'kg',
        'frequency': frequency, 'value': 13, 'date': '2035-01-02', 'points': []}}
    before = deepcopy(values)
    output = threshold_radar_values(SimpleNamespace(thresholds=[threshold]), values)
    assert output[0]['state'] == state
    assert values == before


def test_falsifier_frequency_conflict_stops_before_reading_observations(monkeypatch):
    from tin.compute import signals
    value = {**rule(), 'id': 'synthetic-continuity', 'expression': 'change > 0',
        'unit_confirmed': 'kg', 'consecutive_periods': 2, 'side': 'bull'}
    session = SimpleNamespace(get=lambda *args: SimpleNamespace(unit='kg', frequency='日'))
    monkeypatch.setattr(signals, 'recent_points', lambda *args: pytest.fail('Do not evaluate a conflicting cadence'))
    captured = []
    monkeypatch.setattr(signals, '_upsert', lambda *args: captured.append(args) or args[5])
    assert signals._falsifier_signal(session, SimpleNamespace(id=1), value, None) == '数据缺失'
    assert len(captured) == 1


def test_one_rules_frequency_declaration_does_not_confirm_another_rule_for_the_same_series():
    from tin.judgments.analysis import AnalysisError, validate_analysis
    source = 'Synthetic observation changes，连续两周。'
    first = {**rule(), 'id': 'first', 'desc': 'Synthetic first condition',
        'expression': 'change > 0', 'unit_confirmed': 'kg', 'consecutive_periods': 2, 'side': 'bull'}
    second = {key: value for key, value in first.items() if key != 'caliber_confirmation'}
    second['id'] = 'second'
    document = {'judgment': {'contradiction': {}, 'falsifiers': [first, second]},
        'binding_units': {'synthetic-series': 'kg'}, 'rule_quotes': {'first': source, 'second': source},
        'unresolved': []}
    with pytest.raises(AnalysisError) as error:
        validate_analysis(document, source, [{'id': 'synthetic-series', 'unit': 'kg', 'frequency': None}],
            variety='SYNTHETIC', actor='synthetic-owner',
            caliber_confirmations={'first': first['caliber_confirmation']})
    assert error.value.issues[0]['code'] == 'period_frequency'
    assert error.value.issues[0]['path'] == '/judgment/falsifiers/1/consecutive_periods'
