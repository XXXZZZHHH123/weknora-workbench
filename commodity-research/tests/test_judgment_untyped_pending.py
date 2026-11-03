"""Incomplete model metadata remains source text, never an executable guess."""
from copy import deepcopy
from datetime import date
from types import SimpleNamespace

import pytest

from tests.test_knowledge_facts import canonical, FIRST, page
from tests.test_judgment_generation import (
    generator as model_generator, generate, apply, rows,
)

from tin.judgments.analysis import AnalysisError, assess_draft, validate_analysis
from tin.web.judgment_review import (build_document, checked, initialize_review,
                                    review_response)


SID = 'kb-' + 'd' * 48
CATALOG = [{'id': SID, 'name': 'Measured quantity', 'unit': 'kg'}]
QUOTE = 'Review a sustained increase in recorded stocks.'
SOURCE = 'Quantity above 12 kg. ' + QUOTE


def document(*, text='Independent observation', kind=None, condition=QUOTE):
    return {'judgment': {'contradiction': {}, 'thresholds': [
        {'id': 'verified', 'name': 'Verified boundary', 'series_id': SID,
         'operator': '>', 'value': 12, 'type': '观察位', 'horizon': '短期'}]},
        'binding_units': {SID: 'kg'}, 'rule_quotes': {'verified': 'Quantity above 12 kg'},
        'unresolved': [{'section': 'Source observation', 'text': text,
                        'reason': 'Needs manual review', 'business_condition': condition,
                        'rule_kind': kind, 'series_id': None}]}


def assess(body, *, kinds=None, source=SOURCE):
    kwargs = {} if kinds is None else {'condition_kinds': kinds}
    return assess_draft(body, source, CATALOG, variety='EXAMPLE', actor='researcher',
                        written_at=date(2030, 1, 2), **kwargs)


def test_missing_model_kind_keeps_literal_condition_and_other_verified_rules_nonexecuting():
    raw = document()
    before = deepcopy(raw)
    assessment = assess(raw)
    item = assessment.result.payload.unstructured[0]
    assert QUOTE in item.text and 'Independent observation' in item.text
    assert item.business_condition is None and item.rule_kind is None
    assert [rule.id for rule in assessment.result.payload.thresholds] == ['verified']
    assert assessment.result.payload.falsifiers == []
    assert assessment.candidate_document == before and assessment.candidate_history == [before]
    assert raw == before
    assert any(issue['code'] == 'condition_type_pending' for issue in assessment.issues)
    validate_analysis(assessment.document, SOURCE, CATALOG, variety='EXAMPLE', actor='researcher')
    with pytest.raises(AnalysisError) as caught:
        validate_analysis(raw, SOURCE, CATALOG, variety='EXAMPLE', actor='researcher')
    assert caught.value.issues[0]['code'] == 'business_condition_kind'


@pytest.mark.parametrize('kind', ['threshold', 'falsifier'])
def test_unique_independent_model_classification_only_completes_metadata_not_numbers(kind):
    from tin.judgments.analysis import unique_condition_kinds

    raw = document()
    kinds = unique_condition_kinds([{'quote': QUOTE, 'kind': kind}])
    assessment = assess(raw, kinds=kinds)
    item = assessment.result.payload.unstructured[0]
    assert item.business_condition == QUOTE and item.rule_kind == kind
    assert item.text == raw['unresolved'][0]['text']
    assert [rule.id for rule in assessment.result.payload.thresholds] == ['verified']
    assert assessment.result.payload.falsifiers == []
    assert assessment.candidate_document == raw


@pytest.mark.parametrize('items', [
    [], [{'quote': QUOTE, 'kind': 'other'}],
    [{'quote': QUOTE, 'kind': 'threshold'}, {'quote': QUOTE, 'kind': 'falsifier'}],
    [{'quote': QUOTE, 'kind': 'threshold'}, {'quote': QUOTE, 'kind': 'other'}],
    [{'quote': QUOTE, 'kind': 'threshold'}, {'quote': QUOTE, 'kind': None}],
    [{'quote': QUOTE, 'kind': ['threshold']}],
    [{'quote': 'Different source wording', 'kind': 'threshold'}],
])
def test_conflicting_unknown_or_different_quote_classification_never_guesses_a_type(items):
    from tin.judgments.analysis import unique_condition_kinds

    assessment = assess(document(), kinds=unique_condition_kinds(items))
    item = assessment.result.payload.unstructured[0]
    assert item.business_condition is None and item.rule_kind is None and QUOTE in item.text


def test_identical_inventory_duplicates_do_not_manufacture_a_classification_conflict():
    from tin.judgments.analysis import unique_condition_kinds

    items = [{'quote': QUOTE, 'kind': 'threshold'}] * 2
    assert unique_condition_kinds(items) == {QUOTE: 'threshold'}


@pytest.mark.parametrize('kind', ['', 'thresholds', 'falsifiers', 'unknown', 3, False, []])
def test_non_null_invalid_kind_is_not_erased_or_inferred(kind):
    with pytest.raises(AnalysisError):
        assess(document(kind=kind), kinds={QUOTE: 'threshold'})


@pytest.mark.parametrize('condition', ['', ' ', 'A fabricated condition', 3, []])
def test_nonliteral_or_invalid_condition_stays_strict(condition):
    with pytest.raises(AnalysisError):
        assess(document(condition=condition))


def test_missing_kind_does_not_hide_foreign_binding_or_forged_actual_observation():
    raw = document()
    raw['unresolved'][0]['series_id'] = 'not-authorized'
    with pytest.raises(AnalysisError) as caught:
        assess(raw)
    assert any(issue['code'] == 'unknown_series_id' for issue in caught.value.issues)
    raw = document()
    raw['judgment']['contradiction'] = {'bull': {'evidence': [
        {'desc': 'Claim', 'series_id': SID, 'value': 15}]}}
    with pytest.raises(AnalysisError):
        assess(raw)


def test_large_combined_source_text_is_rejected_explicitly_without_truncation():
    raw = document(text='A' * 3990)
    before = deepcopy(raw)
    with pytest.raises(AnalysisError) as caught:
        assess(raw)
    assert caught.value.issues[0]['code'] == 'condition_text_capacity'
    assert raw == before


def test_already_included_condition_is_preserved_once_and_normalization_is_idempotent():
    raw = document(text='Context. ' + QUOTE)
    first = assess(raw)
    second = assess(first.document)
    assert first.document == second.document
    assert first.result.payload.unstructured[0].text.count(QUOTE) == 1


def test_review_reassesses_raw_candidate_but_uses_normalized_pending_baseline():
    raw = document()
    assessment = assess(raw)
    preview = {'text': SOURCE, 'variety': 'EXAMPLE', 'written_at': '2030-01-02',
               'source_coverage': {'items': []}}
    initialize_review(preview, assessment, CATALOG)
    assert preview['review']['candidate'] == raw
    assert preview['review']['candidate_history'] == [raw]
    item = next(item for item in preview['review']['items'] if item['kind'] == 'unresolved_binding')
    assert item['status'] == 'pending' and QUOTE in item['value']['text']
    assert item['value']['business_condition'] is None and item['value']['rule_kind'] is None
    scope = SimpleNamespace(identity=SimpleNamespace(user_id='researcher'))
    response = review_response(scope, 'a' * 32, preview, CATALOG)
    row = next(row for section in response['sections'] for row in section['items']
               if row['id'] == item['id'])
    assert row['requires_attention'] is True and row['requires_binding'] is False
    assert row['caliber']['can_declare'] is False and row['can_confirm'] is True
    item['status'] = 'kept_text'
    final = build_document(preview, CATALOG)
    checked(preview, final, CATALOG, scope)
    assert final['unresolved'][0]['business_condition'] is None
    assert QUOTE in final['unresolved'][0]['text']
    assert [rule['id'] for rule in final['judgment']['thresholds']] == ['verified']


@pytest.mark.parametrize('kind', ['threshold', 'falsifier'])
def test_review_reuses_only_exact_independent_coverage_kind_and_preserves_raw_audit(kind):
    from tin.judgments.analysis import unique_condition_kinds

    raw = document()
    source_items = [{'quote': QUOTE, 'kind': kind}]
    assessment = assess(raw, kinds=unique_condition_kinds(source_items))
    preview = {'text': SOURCE, 'variety': 'EXAMPLE', 'written_at': '2030-01-02',
               'source_coverage': {'items': source_items}}
    initialize_review(preview, assessment, CATALOG)
    baseline = preview['review']['baseline']['unresolved'][0]
    assert baseline['business_condition'] == QUOTE and baseline['rule_kind'] == kind
    assert preview['review']['candidate'] == raw


def test_model_untyped_item_can_be_reviewed_kept_as_text_and_explicitly_saved(model_generator):
    client, headers, _, response, calls = model_generator
    literal = 'A supply contraction'
    response['unresolved'][0].update(business_condition=literal, rule_kind=None)
    original = deepcopy(response)
    generated = generate(client, headers)
    assert generated.status_code == 200, generated.text
    preview = generated.json()
    assert rows() == [] and response == original
    item = next(item for section in preview['review']['sections'] for item in section['items']
                if item['kind'] == 'unresolved_binding')
    assert item['status'] == 'pending' and literal in item['description']
    assert item['requires_binding'] is False and item['caliber']['can_declare'] is False
    count = len(calls)
    kept = client.post(page(FIRST, '/api/sn/judgment/review/update'), headers=headers, json={
        'draft_id': preview['draft_id'], 'revision': preview['review']['revision'],
        'item_id': item['id'], 'action': 'keep_text'})
    assert kept.status_code == 200, kept.text
    changed = kept.json()
    reviewed = next(row for section in changed['review']['sections'] for row in section['items']
                    if row['id'] == item['id'])
    assert reviewed['status'] == 'kept_text' and reviewed['requires_attention'] is False
    assert len(calls) == count and rows() == []
    saved = apply(client, headers, changed)
    assert saved.status_code == 200, saved.text
    row = rows()[0]
    assert row.status == '草稿' and [rule['id'] for rule in row.thresholds] == ['watch']
    assert row.falsifiers == []
    pending = row.unstructured[0]
    assert literal in pending['text'] and pending['business_condition'] is None and pending['rule_kind'] is None
