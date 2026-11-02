"""Item feedback is independent; the final document remains strictly checked."""
from copy import deepcopy
from types import SimpleNamespace

import pytest
from fastapi import HTTPException

from tin.judgments.analysis import AnalysisError, assess_draft
from tin.web import judgment_review as review


CATALOG = [
    {'id': 'synthetic-amount', 'name': 'Synthetic measured amount', 'unit': 'kg'},
    {'id': 'synthetic-other', 'name': 'Independent measurement', 'unit': 'kg'},
]
SOURCE = 'First amount > 12 kg. Second amount > 21 kg.'
SCOPE = SimpleNamespace(identity=SimpleNamespace(user_id='synthetic-reviewer', tenant_id=23),
    workbench_id='10000000-0000-4000-8000-000000000123', knowledge_base_id='synthetic-kb',
    knowledge_base_tenant_id=23)


@pytest.fixture
def preview():
    document = {'judgment': {'contradiction': {'bull': {'claim': 'Supply research',
        'evidence': [{'desc': 'First measured amount', 'series_id': CATALOG[0]['id']}]}},
        'marginal_focus': [{'desc': 'First measured amount', 'series_id': CATALOG[0]['id']}],
        'thresholds': [{
            'id': identifier, 'name': 'Measured boundary', 'series_id': CATALOG[index]['id'],
            'operator': '>', 'value': number, 'type': '观察位', 'horizon': '短期'}
            for index, (identifier, number) in enumerate([('first', 12), ('second', 21)])]},
        'binding_units': {row['id']: row['unit'] for row in CATALOG},
        'rule_quotes': {'first': 'First amount > 12 kg', 'second': 'Second amount > 21 kg'},
        'unresolved': []}
    result = assess_draft(document, SOURCE, CATALOG, variety='UNIT', actor=SCOPE.identity.user_id)
    value = {'text': SOURCE, 'variety': 'UNIT', 'written_at': '2033-01-02', 'fact_version': 'synthetic-source-version'}
    review.initialize_review(value, result, CATALOG)
    for item in value['review']['items']:
        item['status'] = 'confirmed'
    return value


def item_of(preview, kind, identifier=None):
    return next(item for item in preview['review']['items'] if item['kind'] == kind
        and (identifier is None or item['value']['id'] == identifier))


def test_unrelated_numeric_error_is_not_attached_to_confirmed_binding(preview):
    item_of(preview, 'thresholds', 'first')['value']['value'] = 999
    binding = item_of(preview, 'binding')
    assert review.confirmation_issues(preview, binding, CATALOG, SCOPE) == []
    response = review.review_response(SCOPE, 'synthetic-private-preview', preview, CATALOG)
    shown = next(row for section in response['sections'] for row in section['items'] if row['id'] == binding['id'])
    assert shown['status'] == 'confirmed' and shown['can_confirm']
    assert shown['warnings'] == [] and shown['attention_fields'] == []


def test_current_invalid_number_is_not_masked_by_an_earlier_invalid_rule(preview):
    first = item_of(preview, 'thresholds', 'first')
    second = item_of(preview, 'thresholds', 'second')
    first['value']['value'] = 999
    second['value']['value'] = 888
    issues = review.confirmation_issues(preview, second, CATALOG, SCOPE)
    assert len(issues) == 1
    assert issues[0]['code'] == 'number_not_in_source'
    assert issues[0]['path'] == '/judgment/thresholds/1/value'


def test_current_binding_and_unit_errors_are_still_rejected(preview):
    item = item_of(preview, 'thresholds', 'second')
    item['series_id'] = None
    assert review.confirmation_issues(preview, item, CATALOG, SCOPE)[0]['code'] == 'missing_binding'
    item['series_id'] = CATALOG[1]['id']
    changed = deepcopy(CATALOG)
    changed[1]['unit'] = None
    assert review.confirmation_issues(preview, item, changed, SCOPE)[0]['code'] == 'unit_metadata_missing'


def test_current_numeric_error_does_not_keep_an_old_unit_red_box(preview):
    item = item_of(preview, 'thresholds', 'first')
    item.update(status='pending', initial_attention_fields=['binding'])
    item['value']['value'] = 999
    response = review.review_response(SCOPE, 'synthetic-private-preview', preview, CATALOG)
    shown = next(row for section in response['sections'] for row in section['items'] if row['id'] == item['id'])
    assert shown['attention_fields'] == ['condition']
    assert shown['requires_attention'] and not shown['can_confirm']
    assert not shown['caliber']['confirmation_required']


def test_independent_item_check_does_not_make_an_invalid_document_saveable(preview):
    item_of(preview, 'thresholds', 'first')['value']['value'] = 999
    with pytest.raises(AnalysisError) as caught:
        review.checked(preview, review.build_document(preview, CATALOG), CATALOG, SCOPE)
    assert caught.value.issues[0]['code'] == 'number_not_in_source'


def test_old_temporary_unit_metadata_can_be_confirmed_once_without_new_analysis(preview):
    item = item_of(preview, 'thresholds', 'second')
    item.update(status='pending', requires_explicit_binding=True,
                binding_explicit=False, initial_attention_fields=['binding'])
    preview['review']['baseline']['judgment']['thresholds'] = []
    original = deepcopy(preview)
    normalized = review.adopt_catalog_choices(preview, CATALOG)
    selected = item_of(normalized, 'thresholds', 'second')
    assert selected['status'] == 'pending', 'A compatibility read cannot confirm a business decision'
    assert not selected['requires_explicit_binding']
    assert review.confirmation_issues(normalized, selected, CATALOG, SCOPE) == []
    assert preview == original, 'Restoring a temporary result does not write back business state'
    assert review.adopt_catalog_choices(normalized, CATALOG) == normalized
    selected['status'] = 'confirmed'
    review.checked(normalized, review.build_document(normalized, CATALOG), CATALOG, SCOPE)
    response = review.review_response(SCOPE, 'synthetic-private-preview', normalized, CATALOG)
    shown = next(row for section in response['sections'] for row in section['items'] if row['id'] == selected['id'])
    assert shown['status'] == 'confirmed' and shown['warnings'] == []
    assert shown['attention_fields'] == [] and not shown['caliber']['confirmation_required']


def test_remembered_cross_unit_binding_does_not_change_the_unconfirmed_falsifier_draft():
    source = 'Measurement > 12'
    catalog = [{'id': 'synthetic-a', 'name': 'AI measurement', 'unit': 'kg'},
               {'id': 'synthetic-b', 'name': 'Remembered measurement', 'unit': '吨'}]
    document = {'judgment': {'contradiction': {}, 'falsifiers': [{
        'id': 'boundary', 'desc': 'Synthetic rule', 'series_id': 'synthetic-a',
        'expression': 'value > 12', 'unit_confirmed': 'kg', 'side': 'bull'}]},
        'binding_units': {'synthetic-a': 'kg'}, 'rule_quotes': {'boundary': source}, 'unresolved': []}
    assessment = assess_draft(document, source, catalog, variety='UNIT', actor=SCOPE.identity.user_id)
    preview = {'text': source, 'variety': 'UNIT', 'written_at': '2033-01-02',
               'fact_version': 'synthetic-source-version'}
    review.initialize_review(preview, assessment, catalog)
    original_draft = deepcopy(preview['document'])
    item = item_of(preview, 'falsifiers', 'boundary')
    item.update(status='pending', remembered=True, series_id='synthetic-b')
    restored = review.adopt_catalog_choices(preview, catalog)
    result = review.checked(restored, restored['document'], catalog, SCOPE)
    assert restored['document'] == original_draft
    assert result.payload.falsifiers[0].series_id == 'synthetic-a'
    assert result.payload.falsifiers[0].unit_confirmed == 'kg'
    selected = item_of(restored, 'falsifiers', 'boundary')
    assert selected['series_id'] == 'synthetic-b' and selected['status'] == 'pending'
    assert selected['value']['unit_confirmed'] == 'kg'
    assert review.confirmation_issues(restored, selected, catalog, SCOPE) == []
    selected['status'] = 'confirmed'
    explicit = review.checked(restored, review.build_document(restored, catalog), catalog, SCOPE)
    assert explicit.payload.falsifiers[0].series_id == 'synthetic-b'
    assert explicit.payload.falsifiers[0].unit_confirmed == '吨'


@pytest.mark.parametrize('status', ['confirmed', 'kept_text', 'excluded'])
def test_catalog_compatibility_keeps_existing_user_decisions_unchanged(preview, status):
    item = item_of(preview, 'thresholds', 'first')
    item['status'] = status
    item['requires_explicit_binding'] = True
    before = deepcopy(item)
    normalized = review.adopt_catalog_choices(preview, CATALOG)
    assert item_of(normalized, 'thresholds', 'first') == before


def test_selecting_a_registered_unit_does_not_need_an_unknown_source_unit_popup(preview):
    item = {'id': 'item-manual', 'path': ['unresolved', 0], 'section': 'unresolved',
            'series_id': None, 'ai_series_id': None}
    body = review.UpdateRequest(draft_id='synthetic-private-preview', revision=1,
        item_id=item['id'], action='confirm', series_id=CATALOG[0]['id'])
    assert review.acknowledge_binding(SCOPE, body, preview, item, {row['id']: row for row in CATALOG}) is None


@pytest.mark.parametrize('known_from', ['original_source', 'previous_binding'])
def test_real_known_unit_mismatch_still_needs_exact_warning_acknowledgement(preview, known_from):
    item = {'id': 'item-manual', 'path': ['unresolved', 0], 'section': 'unresolved',
            'series_id': None, 'ai_series_id': None}
    if known_from == 'original_source':
        preview['source_coverage'] = {'items': [{'candidate_path': '/unresolved/0',
            'quote': 'First amount > 12 kg', 'source_unit': 'kg'}]}
    else:
        item['series_id'] = CATALOG[0]['id']
    other = {'id': 'synthetic-distinct-unit', 'unit': 'lb', 'name': 'Independent raw measurement'}
    known = {row['id']: row for row in [*CATALOG, other]}
    body = review.UpdateRequest(draft_id='synthetic-private-preview', revision=1,
        item_id=item['id'], action='confirm', series_id=other['id'])
    with pytest.raises(HTTPException) as caught:
        review.acknowledge_binding(SCOPE, body, preview, item, known)
    warning = caught.value.detail
    assert warning['code'] == 'binding_warning' and 'kg' in warning['message'] and 'lb' in warning['message']
    assert '不自动换算' in warning['message']
    accepted = body.model_copy(update={'binding_warning_token': warning['confirmation_token']})
    record = review.acknowledge_binding(SCOPE, accepted, preview, item, known)
    assert record['unit'] == 'lb' and record['series_id'] == other['id']
