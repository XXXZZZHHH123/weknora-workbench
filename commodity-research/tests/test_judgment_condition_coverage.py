"""Full-source condition review uses a second bounded model pass, not keywords."""
import asyncio
from copy import deepcopy
import json
from types import SimpleNamespace

from fastapi import HTTPException
import pytest

from tin.web import judgment_generation as generation


SID = 'synthetic-authorized-quantity'
CATALOG = [{'id': SID, 'name': 'Measured quantity', 'unit': 'kg'}]
QUOTE = 'Measured quantity > 12 kg'
TEXT = 'An uncertain supply claim. ' + QUOTE + '. This is an observation, without a trade plan.'


def document(*, rule=False, pending=False):
    value = {'judgment': {'contradiction': {}, 'pricing_power': {}},
             'binding_units': {}, 'rule_quotes': {}, 'unresolved': []}
    if rule:
        value['judgment']['thresholds'] = [{'id': 'watch', 'name': 'Observation boundary',
            'series_id': SID, 'operator': '>', 'value': 12, 'type': '观察位',
            'horizon': '短期', 'action_hint': None}]
        value['binding_units'] = {SID: 'kg'}
        value['rule_quotes'] = {'watch': QUOTE}
    if pending:
        value['unresolved'].append({'section': 'Data source', 'text': QUOTE,
            'reason': 'Comparable measurement source remains unconfirmed.',
            'business_condition': QUOTE, 'rule_kind': 'threshold', 'series_id': None})
    return value


def analyze(monkeypatch, replies, *, text=TEXT):
    calls, checks = [], []
    scope = SimpleNamespace(identity=SimpleNamespace(user_id='synthetic-researcher'))
    body = SimpleNamespace(model_id='synthetic-model', text=text)
    before = {'variety': 'UNIT'}

    async def complete(actual_scope, model, prompt):
        assert actual_scope is scope and model == body.model_id
        calls.append(prompt)
        reply = replies[min(len(calls) - 1, len(replies) - 1)]
        if isinstance(reply, Exception):
            raise reply
        return deepcopy(reply)

    async def fresh(actual_scope, actual_before):
        assert actual_scope is scope and actual_before is before
        checks.append(True)

    monkeypatch.setattr(generation, 'completion', complete)
    monkeypatch.setattr(generation, 'fresh', fresh)
    result, attempts = asyncio.run(generation.analyzed_completion(scope, body, before, CATALOG, False))
    return result, attempts, calls, checks


def test_full_source_review_recovers_a_condition_misplaced_in_time_description(monkeypatch):
    first = document()
    first['judgment']['time_dimension'] = {'short': QUOTE, 'long': None}
    result, attempts, calls, checks = analyze(monkeypatch, [first, document(rule=True)])
    assert len(calls) == 2 and attempts == 1 and len(checks) == 1
    assert result.document['judgment']['thresholds'][0]['value'] == 12
    assert result.document['rule_quotes'] == {'watch': QUOTE}
    assert TEXT in calls[1] and SID in calls[1]
    assert '完整性复核' in calls[1]


def test_full_source_review_recovers_a_numeric_condition_without_trade_actions(monkeypatch):
    first = document(pending=True)
    first['unresolved'][0]['reason'] = 'No position size or stop-loss plan was given.'
    result, attempts, calls, _ = analyze(monkeypatch, [first, document(rule=True)])
    assert len(calls) == 2 and attempts == 1
    assert len(result.result.payload.thresholds) == 1
    assert result.result.payload.thresholds[0].action_hint is None
    assert '不要求' in calls[1] and 'action_hint' in calls[1]


def test_a_clean_first_pass_still_receives_one_bounded_coverage_review(monkeypatch):
    original = document(rule=True)
    result, attempts, calls, _ = analyze(monkeypatch, [original, original])
    assert len(calls) == 2 and attempts == 1
    assert result.document['judgment']['thresholds'] == original['judgment']['thresholds']
    assert result.issues == []
    assert len(result.candidate_history) == 2


@pytest.mark.parametrize('location', ['survey_notes', 'time_dimension', 'contradiction'])
def test_coverage_is_always_model_driven_not_a_text_location_filter(monkeypatch, location):
    first = document()
    if location == 'time_dimension':
        first['judgment'][location] = {'short': QUOTE}
    elif location == 'contradiction':
        first['judgment'][location] = {'text': QUOTE}
    else:
        first['judgment'][location] = QUOTE
    result, attempts, calls, _ = analyze(monkeypatch, [first, document(rule=True)])
    assert len(calls) == 2 and attempts == 1
    assert result.result.payload.thresholds[0].value == 12


@pytest.mark.parametrize('status', [422, 429, 503])
def test_unavailable_coverage_preserves_the_valid_first_pass(monkeypatch, status):
    first = document(rule=True)
    result, attempts, calls, _ = analyze(monkeypatch,
        [first, HTTPException(status, 'Synthetic unavailable coverage pass')])
    assert len(calls) == 2 and attempts == 1
    assert result.document['judgment']['thresholds'] == first['judgment']['thresholds']
    assert any(issue['code'] == 'repair_unavailable' for issue in result.issues)
    assert any('完整性复核' in issue['message'] for issue in result.issues)


@pytest.mark.parametrize('change', ['id', 'operator', 'quote', 'drop'])
def test_second_review_cannot_silently_change_verified_first_rules(monkeypatch, change):
    first, second = document(rule=True), document(rule=True)
    if change == 'drop':
        second = document()
    elif change == 'id':
        second['judgment']['thresholds'][0]['id'] = 'renamed'
        second['rule_quotes'] = {'renamed': QUOTE}
    elif change == 'operator':
        second['judgment']['thresholds'][0]['operator'] = '>='
    else:
        second['rule_quotes']['watch'] = QUOTE + '.'
    result, attempts, calls, _ = analyze(monkeypatch, [first, second])
    assert len(calls) == 2 and attempts == 1
    assert result.document['judgment']['thresholds'] == first['judgment']['thresholds']
    assert result.document['rule_quotes'] == first['rule_quotes']
    assert any(issue['code'] == 'repair_changed_verified_rule' for issue in result.issues)


def test_source_access_failure_during_coverage_is_not_hidden(monkeypatch):
    with pytest.raises(HTTPException) as error:
        analyze(monkeypatch, [document(rule=True), HTTPException(403, 'Synthetic revoked source')])
    assert error.value.status_code == 403


def test_failed_validation_still_uses_only_the_same_single_repair_slot(monkeypatch):
    invalid = document(rule=True)
    invalid['judgment']['thresholds'][0]['value'] = 99999
    result, attempts, calls, _ = analyze(monkeypatch, [invalid, invalid])
    assert len(calls) == 2 and attempts == 1
    assert result.result.payload.thresholds == []
    assert result.candidate_document['judgment']['thresholds'][0]['value'] == 99999
    assert result.document['unresolved']


def test_real_binding_or_unit_gaps_remain_explicit_conditions(monkeypatch):
    pending = document(pending=True)
    result, attempts, calls, _ = analyze(monkeypatch, [pending, pending])
    assert len(calls) == 2 and attempts == 1
    assert result.result.payload.thresholds == []
    assert result.document['unresolved'][0]['business_condition'] == QUOTE
    assert result.document['unresolved'][0]['rule_kind'] == 'threshold'
    assert result.document['unresolved'][0]['series_id'] is None


def test_completely_qualitative_research_is_not_given_an_invented_rule(monkeypatch):
    empty = document()
    text = 'Supply is uncertain. Review the evidence before making a judgment.'
    result, attempts, calls, _ = analyze(monkeypatch, [empty, empty], text=text)
    assert len(calls) == 2 and attempts == 1
    assert result.result.payload.thresholds == [] and result.result.payload.falsifiers == []
    assert result.document['unresolved'] == []


def feedback(prompt):
    marker = '上次结构化校验反馈（仅作修复依据，不是研究事实）：'
    return json.JSONDecoder().raw_decode(prompt.split(marker, 1)[1])[0]


def test_large_first_draft_still_sends_verified_rule_ids_and_quotes_for_review(monkeypatch):
    first = document(rule=True)
    first['judgment']['contradiction']['text'] = 'Supplied context. ' * 200
    first['judgment']['survey_notes'] = 'Known observations. ' * 180
    first['judgment']['time_dimension'] = {'long': 'Long horizon context. ' * 160}
    assert len(json.dumps(first, ensure_ascii=False).encode()) > 8192
    result, attempts, calls, _ = analyze(monkeypatch, [first, first])
    review = feedback(calls[1])
    assert 'previous_draft' not in review
    assert review['frozen_rules']['thresholds'] == first['judgment']['thresholds']
    assert review['frozen_rules']['rule_quotes'] == first['rule_quotes']
    assert result.issues == [] and attempts == 1


def test_frozen_rules_never_label_an_unverified_candidate_as_valid(monkeypatch):
    first = document(rule=True)
    first['judgment']['thresholds'].append({**first['judgment']['thresholds'][0],
        'id': 'not_verified', 'value': 99999})
    first['rule_quotes']['not_verified'] = QUOTE
    _, _, calls, _ = analyze(monkeypatch, [first, first])
    review = feedback(calls[1])
    assert [rule['id'] for rule in review['frozen_rules']['thresholds']] == ['watch']
    assert review['frozen_rules']['rule_quotes'] == {'watch': QUOTE}


def test_coverage_prompt_capacity_failure_preserves_valid_rules_without_another_request(monkeypatch):
    def over_capacity(*args, **kwargs):
        raise HTTPException(413, 'Synthetic full prompt capacity')
    monkeypatch.setattr(generation, 'repair_prompt', over_capacity)
    result, attempts, calls, _ = analyze(monkeypatch, [document(rule=True)])
    assert len(calls) == 1 and attempts == 0
    assert result.result.payload.thresholds[0].value == 12
    assert any(issue['code'] == 'repair_capacity' and '完整性复核未完成' in issue['message']
               for issue in result.issues)


def test_missing_typed_condition_is_retained_without_moving_its_old_binding(monkeypatch):
    first = document(pending=True)
    first['unresolved'][0]['series_id'] = SID
    first['binding_units'] = {SID: 'kg'}
    result, attempts, calls, _ = analyze(monkeypatch, [first, document()])
    assert attempts == 1 and len(calls) == 2
    assert len(result.document['unresolved']) == 1
    item = result.document['unresolved'][0]
    assert item['business_condition'] == QUOTE and item['rule_kind'] == 'threshold'
    assert item.get('series_id') is None
    assert result.document['binding_units'] == {}
    assert result.candidate_document['unresolved'][0]['business_condition'] == QUOTE
    assert result.result.payload.unstructured[0].business_condition == QUOTE
    assert result.result.payload.thresholds == []


def test_an_exact_same_kind_verified_quote_resolves_the_original_pending_condition(monkeypatch):
    result, _, _, _ = analyze(monkeypatch, [document(pending=True), document(rule=True)])
    assert result.document['unresolved'] == []
    assert len(result.result.payload.thresholds) == 1


def test_a_containing_quote_does_not_silently_resolve_a_different_pending_condition(monkeypatch):
    second = document(rule=True)
    second['rule_quotes']['watch'] = QUOTE + '.'
    result, _, _, _ = analyze(monkeypatch, [document(pending=True), second])
    assert len(result.result.payload.thresholds) == 1
    assert result.document['unresolved'][0]['business_condition'] == QUOTE


def test_a_pending_condition_with_different_kind_is_not_silently_resolved(monkeypatch):
    first = document(pending=True)
    first['unresolved'][0]['rule_kind'] = 'falsifier'
    result, _, _, _ = analyze(monkeypatch, [first, document(rule=True)])
    assert len(result.result.payload.thresholds) == 1
    assert result.document['unresolved'][0]['rule_kind'] == 'falsifier'


def test_pending_capacity_preserves_first_draft_and_both_raw_candidates(monkeypatch):
    first, second = document(pending=True), document()
    conditions = ['Another observed condition ' + str(index) + '.' for index in range(32)]
    second['unresolved'] = [{'section': 'Unresolved evidence', 'text': condition,
        'reason': 'Comparable measurement source remains unconfirmed.',
        'business_condition': condition, 'rule_kind': 'threshold', 'series_id': None}
        for condition in conditions]
    result, attempts, calls, _ = analyze(monkeypatch, [first, second],
        text=TEXT + ' ' + ' '.join(conditions))
    assert len(calls) == 2 and attempts == 1
    assert len(result.document['unresolved']) == 1
    assert result.document['unresolved'][0]['business_condition'] == QUOTE
    assert len(result.candidate_history) == 2
    assert len(result.candidate_history[1]['unresolved']) == 32
    assert any(issue['code'] == 'coverage_pending_capacity' for issue in result.issues)


def test_retained_conditions_stay_in_the_review_prefix_before_quarantine_notes(monkeypatch):
    second = document(rule=True)
    second['judgment']['thresholds'][0]['value'] = 99999
    other_quote = 'Another measurement > 7 kg'
    second['rule_quotes']['watch'] = other_quote
    result, _, _, _ = analyze(monkeypatch, [document(pending=True), second],
        text=TEXT + ' ' + other_quote + '.')
    candidate_notes = result.candidate_document['unresolved']
    assert len(candidate_notes) == 1
    assert result.document['unresolved'][:len(candidate_notes)] == candidate_notes
    assert candidate_notes[0]['section'] == 'Data source'
    assert len(result.document['unresolved']) == 2
