"""One research entry keeps an explicit, bounded group of authorized bindings."""
from jsonschema import Draft202012Validator
from pydantic import ValidationError
import pytest

from tin.judgments.analysis import AnalysisError, assess_draft, validate_analysis
from tin.judgments.anchor_binding import apply_binding_plan
from tin.judgments.markdown import dump_markdown
from tin.judgments.output_schema import OUTPUT_SCHEMA
from tin.judgments.service import _refs
from tin.schemas.judgment import Evidence, JudgmentIn, binding_ids
from tests.test_knowledge_facts import canonical, SERIES, OTHER_KB
from tests.test_judgment_generation import generator, generate, apply, rows
from tests.test_judgment_review import pick, update, reuse


EXTRA = 'kb-' + 'c' * 48
PRIMARY = 'synthetic-primary'
RELATED = 'synthetic-related'
SOURCE = 'Synthetic stock above 12 kg. Synthetic stock and capacity matter together.'
CATALOG = [{'id': PRIMARY, 'name': 'Synthetic stock', 'unit': 'kg', 'frequency': '日'},
           {'id': RELATED, 'name': 'Synthetic capacity', 'unit': '%', 'frequency': '月'}]


def document():
    return {'judgment': {'contradiction': {'bull': {'claim': 'Synthetic supply',
        'evidence': [{'desc': 'Synthetic shared observation', 'series_id': PRIMARY,
                      'related_series_ids': [RELATED]}]}},
        'marginal_focus': [{'desc': 'Synthetic shared observation', 'series_id': None,
                            'related_series_ids': [RELATED]}],
        'thresholds': [{'id': 'boundary', 'name': 'Synthetic stock boundary', 'series_id': PRIMARY,
                        'related_series_ids': [RELATED], 'operator': '>', 'value': 12,
                        'type': '观察位', 'horizon': '短期'}]},
        'binding_units': {PRIMARY: 'kg', RELATED: '%'},
        'rule_quotes': {'boundary': 'Synthetic stock above 12 kg'}, 'unresolved': []}


def test_domain_group_roundtrip_keeps_primary_and_all_context_without_changing_rule():
    result = validate_analysis(document(), SOURCE, CATALOG, variety='XY', actor='tester')
    payload = result.payload
    assert binding_ids(payload.contradiction.bull.evidence[0]) == [PRIMARY, RELATED]
    assert binding_ids(payload.marginal_focus[0]) == [RELATED]
    assert payload.thresholds[0].series_id == PRIMARY and payload.thresholds[0].value == 12
    encoded = payload.model_dump_json()
    assert JudgmentIn.model_validate_json(encoded) == payload
    assert {sid for _, _, sid in _refs(payload)} == {PRIMARY, RELATED}
    assert RELATED in dump_markdown(payload)
    Draft202012Validator(OUTPUT_SCHEMA).validate(document())


def test_empty_extra_bindings_preserve_legacy_output_shape():
    value = Evidence(desc='Synthetic original', series_id=PRIMARY)
    assert 'related_series_ids' not in value.model_dump()
    assert 'related_series_ids' not in Evidence.model_validate(value.model_dump()).model_dump()


@pytest.mark.parametrize('related', [[PRIMARY], [RELATED, RELATED], [''], [True], ['s' * 257], ['x'] * 8])
def test_repeated_invalid_or_excessive_extra_ids_are_rejected(related):
    with pytest.raises(ValidationError):
        Evidence(desc='Synthetic original', series_id=PRIMARY, related_series_ids=related)


@pytest.mark.parametrize('mode', ['unknown', 'missing_unit', 'wrong_unit'])
def test_all_extra_ids_and_units_are_validated_at_the_analysis_boundary(mode):
    value = document()
    if mode == 'unknown':
        value['judgment']['contradiction']['bull']['evidence'][0]['related_series_ids'] = ['outside-catalog']
    elif mode == 'missing_unit':
        value['binding_units'].pop(RELATED)
    else:
        value['binding_units'][RELATED] = 'kg'
    with pytest.raises(AnalysisError):
        validate_analysis(value, SOURCE, CATALOG, variety='XY', actor='tester')


def test_model_assessment_keeps_group_and_rule_related_units():
    assessed = assess_draft(document(), SOURCE, CATALOG, variety='XY', actor='tester')
    assert assessed.document['judgment']['thresholds'][0]['related_series_ids'] == [RELATED]
    assert set(assessed.document['binding_units']) == {PRIMARY, RELATED}


def test_anchor_recommendation_can_keep_one_description_with_multiple_bindings():
    value = {'judgment': {'contradiction': {'bull': {'evidence': [
        {'desc': 'Synthetic shared observation', 'series_id': None}]}}}, 'binding_units': {}}
    path = '/judgment/contradiction/bull/evidence/0'
    plan = {'bindings': [{'path': path, 'items': [{'desc': 'Synthetic shared observation',
        'quote': 'Synthetic stock and capacity matter together', 'series_id': PRIMARY,
        'related_series_ids': [RELATED], 'reason': 'Synthetic source-supported group'}]}]}
    changed, audit = apply_binding_plan(value, plan, SOURCE, CATALOG)
    evidence = changed['judgment']['contradiction']['bull']['evidence']
    assert len(evidence) == 1 and evidence[0]['desc'] == 'Synthetic shared observation'
    assert binding_ids(evidence[0]) == [PRIMARY, RELATED]
    assert audit['path_remap'][path] == [path]


def setup_two_sources(generator):
    client, headers, state, _, _ = generator
    state['default_macro'] = {'knowledge_base_id': OTHER_KB, 'tenant_id': 17}
    response = generate(client, headers)
    assert response.status_code == 200, response.text
    return client, headers, response.json()


def test_review_group_is_saved_and_omission_preserves_but_empty_array_clears(generator):
    client, headers, preview = setup_two_sources(generator)
    item = pick(preview, 'binding')
    added = update(client, headers, preview, item, related_series_ids=[EXTRA])
    assert added.status_code == 200, added.text
    changed = added.json()
    selected = pick(changed, 'binding')
    assert [entry['series_id'] for entry in selected['bindings']] == [SERIES, EXTRA]
    assert selected['can_add_binding'] and selected['max_bindings'] == 8
    kept = update(client, headers, changed, selected)
    assert kept.status_code == 200 and pick(kept.json(), 'binding')['related_bindings'][0]['series_id'] == EXTRA
    cleared = update(client, headers, kept.json(), pick(kept.json(), 'binding'), related_series_ids=[])
    assert cleared.status_code == 200 and pick(cleared.json(), 'binding')['related_bindings'] == []
    restored = update(client, headers, cleared.json(), pick(cleared.json(), 'binding'), related_series_ids=[EXTRA])
    assert restored.status_code == 200
    assert apply(client, headers, restored.json()).status_code == 200
    assert rows()[0].contradiction['bull']['evidence'][0]['related_series_ids'] == [EXTRA]


def test_primary_none_with_extra_can_be_remembered_and_only_reused_as_full_group(generator):
    client, headers, preview = setup_two_sources(generator)
    item = pick(preview, 'binding')
    response = update(client, headers, preview, item, series_id=None, related_series_ids=[EXTRA], remember=True)
    assert response.status_code == 200, response.text
    other = generate(client, headers).json()
    remembered = reuse(client, headers, other)
    assert remembered.status_code == 200, remembered.text
    selected = pick(remembered.json(), 'binding')
    assert selected['binding'] is None and selected['related_bindings'][0]['series_id'] == EXTRA
    assert selected['remembered'] and selected['status'] == 'pending'
    restored = reuse(client, headers, remembered.json(), False).json()
    assert pick(restored, 'binding')['binding']['series_id'] == SERIES
    assert pick(restored, 'binding')['related_bindings'] == []


@pytest.mark.parametrize('action', ['keep_text', 'exclude'])
def test_text_or_exclusion_clears_all_selected_bindings(generator, action):
    client, headers, preview = setup_two_sources(generator)
    changed = update(client, headers, preview, pick(preview, 'binding'), related_series_ids=[EXTRA]).json()
    response = update(client, headers, changed, pick(changed, 'binding'), action)
    assert response.status_code == 200, response.text
    assert pick(response.json(), 'binding')['binding'] is None
    assert pick(response.json(), 'binding')['related_bindings'] == []


@pytest.mark.parametrize('extras', [[SERIES], [EXTRA, EXTRA], ['foreign-field']])
def test_review_invalid_extra_choices_never_mutate_the_preview(generator, extras):
    client, headers, preview = setup_two_sources(generator)
    response = update(client, headers, preview, pick(preview, 'binding'), related_series_ids=extras)
    assert response.status_code == 422
    assert rows() == []
