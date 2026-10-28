"""Saved observation conditions require explicit type and literal source proof."""
from copy import deepcopy
import json

import pytest

from tin.judgments.analysis import AnalysisError, assess_draft, validate_analysis
from tin.schemas.judgment import UnstructuredItem


SID = 'kb-' + 'b' * 48
CATALOG = [{'id': SID, 'name': 'Measured quantity', 'unit': 'kg'}]
SOURCE = ('Quantity above 12 kg. Quantity below 5 kg for 2 periods. '
          'Review when production slows.')
QUOTE = 'Review when production slows.'


def document():
    return {'judgment': {'contradiction': {}, 'thresholds': [
        {'id': 'high', 'name': 'Upper boundary', 'series_id': SID,
         'operator': '>', 'value': 12, 'type': '观察位', 'horizon': '短期'}]},
        'binding_units': {SID: 'kg'}, 'rule_quotes': {'high': 'Quantity above 12 kg'},
        'unresolved': []}


def validate(body):
    return validate_analysis(body, SOURCE, CATALOG, variety='EXAMPLE', actor='owner')


def test_explicit_business_condition_accepts_free_audit_title_and_keeps_source_unchanged():
    body = document()
    body['unresolved'] = [{'section': '人工核对项·任意标题', 'text': 'Audit note',
        'reason': 'No numeric rule is executable', 'business_condition': QUOTE,
        'rule_kind': 'falsifier'}]
    original = deepcopy(body)
    result = validate(body)
    item = result.payload.unstructured[0]
    assert item.business_condition == QUOTE and item.rule_kind == 'falsifier'
    assert result.payload.thresholds[0].value == 12
    assert body == original and result.payload.source_note == SOURCE


@pytest.mark.parametrize(('condition', 'kind', 'code', 'field'), [
    ('Production certainly contracts.', 'threshold', 'business_condition_not_in_source', 'business_condition'),
    ('', 'threshold', 'business_condition_not_in_source', 'business_condition'),
    (' ', 'falsifier', 'business_condition_not_in_source', 'business_condition'),
    (QUOTE, None, 'business_condition_kind', 'rule_kind'),
    (None, 'falsifier', 'business_condition_missing', 'business_condition'),
])
def test_model_condition_requires_literal_quote_and_complete_explicit_type(condition, kind, code, field):
    body = document()
    body['unresolved'] = [{'section': 'thresholds', 'text': 'Audit note', 'reason': 'Manual review',
        'business_condition': condition, 'rule_kind': kind}]
    original = deepcopy(body)
    for check in (validate, lambda value: assess_draft(value, SOURCE, CATALOG,
                                                      variety='EXAMPLE', actor='owner')):
        with pytest.raises(AnalysisError) as caught:
            check(body)
        assert any(issue['code'] == code and issue['path'] == '/unresolved/0/' + field
                   for issue in caught.value.issues)
    assert body == original


@pytest.mark.parametrize('kind', ['thresholds', 'falsifiers', 'business guess', 5, True])
def test_invalid_condition_type_is_rejected_instead_of_inferred_from_section(kind):
    body = document()
    body['unresolved'] = [{'section': 'falsifiers', 'text': 'Audit note', 'reason': 'Manual review',
        'business_condition': QUOTE, 'rule_kind': kind}]
    with pytest.raises(AnalysisError):
        validate(body)


@pytest.mark.parametrize(('quote', 'expected'), [
    ('Quantity above 12 kg', 'Quantity above 12 kg'),
    ('Quantity substantially above 12 kg', None),
    ('', None),
    (' ', None),
    (None, None),
])
def test_demoted_threshold_preserves_only_a_verified_quote_as_nonexecuting_condition(quote, expected):
    body = document()
    body['judgment']['thresholds'][0]['value'] = 99999
    body['rule_quotes']['high'] = quote
    original = deepcopy(body)
    assessed = assess_draft(body, SOURCE, CATALOG, variety='EXAMPLE', actor='owner')
    assert assessed.result.payload.thresholds == []
    item = assessed.result.payload.unstructured[0]
    assert item.business_condition == expected
    assert item.rule_kind == ('threshold' if expected else None)
    assert '99999' in item.text and '不执行' in item.text and item.reason
    assert body == original


def test_demoted_falsifier_keeps_verified_condition_and_never_creates_executable_signal():
    body = document()
    body['judgment']['falsifiers'] = [{'id': 'low', 'desc': 'Lower boundary', 'series_id': SID,
        'expression': 'value < 99999', 'unit_confirmed': 'kg', 'side': 'bull', 'consecutive_periods': 2}]
    body['rule_quotes']['low'] = 'Quantity below 5 kg for 2 periods'
    result = assess_draft(body, SOURCE, CATALOG, variety='EXAMPLE', actor='owner')
    assert [rule.id for rule in result.result.payload.thresholds] == ['high']
    assert result.result.payload.falsifiers == []
    item = result.result.payload.unstructured[0]
    assert item.rule_kind == 'falsifier'
    assert item.business_condition == 'Quantity below 5 kg for 2 periods'


def test_legacy_unstructured_json_still_parses_without_new_fields():
    row = UnstructuredItem.model_validate({'section': '目录说明', 'text': 'Original note', 'reason': 'Audit'})
    assert row.business_condition is None and row.rule_kind is None


def test_legacy_roundtrip_keeps_absent_keys_but_explicit_nulls_remain_explicit():
    legacy = {'section': 'thresholds', 'text': 'Original legacy observation', 'reason': 'Audit'}
    row = UnstructuredItem.model_validate(legacy)
    assert row.model_dump() == legacy
    assert json.loads(row.model_dump_json()) == legacy
    explicit = {**legacy, 'business_condition': None, 'rule_kind': None}
    assert UnstructuredItem.model_validate(explicit).model_dump() == explicit
    assert json.loads(UnstructuredItem.model_validate(explicit).model_dump_json()) == explicit


def test_nested_legacy_roundtrip_preserves_watch_compatibility_without_hiding_new_nulls():
    from tin.web.report_watch import watch_items
    body = document()
    body['unresolved'] = [
        {'section': 'thresholds', 'text': 'Original legacy observation', 'reason': 'Audit'},
        {'section': 'thresholds', 'text': 'PRIVATE_DIAGNOSTIC', 'reason': 'Audit',
         'business_condition': None, 'rule_kind': None}]
    result = validate(body)
    payload = result.payload.model_dump()
    assert set(payload['unstructured'][0]) == {'section', 'text', 'reason'}
    assert payload['unstructured'][1]['business_condition'] is None
    assert payload['unstructured'][1]['rule_kind'] is None
    assert [row['text'] for row in watch_items(payload)] == ['Upper boundary', 'Original legacy observation']


def test_report_keeps_all_valid_and_demoted_source_conditions_but_omits_diagnostics():
    from tin.web.report_watch import watch_items
    body = document()
    body['judgment']['thresholds'].append({**body['judgment']['thresholds'][0],
        'id': 'unsupported', 'name': 'Unverified boundary', 'value': 99999})
    body['rule_quotes']['unsupported'] = 'Quantity above 12 kg'
    body['judgment']['falsifiers'] = [{'id': 'low', 'desc': 'Lower boundary', 'series_id': SID,
        'expression': 'value < 5', 'unit_confirmed': 'kg', 'side': 'bull', 'consecutive_periods': 2}]
    body['rule_quotes']['low'] = 'Quantity below 5 kg for 2 periods'
    body['unresolved'] = [
        {'section': '自由中文标题', 'text': 'PRIVATE_DIAGNOSTIC_TEXT', 'reason': 'PRIVATE_DIAGNOSTIC_REASON',
         'business_condition': QUOTE, 'rule_kind': 'threshold'},
        {'section': '目录与单位说明', 'text': 'PRIVATE_DIAGNOSTIC_ONLY', 'reason': 'PRIVATE_DIAGNOSTIC_REASON'},
        {'section': 'thresholds', 'text': 'PRIVATE_NEW_DIAGNOSTIC', 'reason': 'PRIVATE_DIAGNOSTIC_REASON',
         'business_condition': None, 'rule_kind': None}]
    result = assess_draft(body, SOURCE, CATALOG, variety='EXAMPLE', actor='owner')
    payload = result.result.payload.model_dump()
    # Persisted legacy JSON has neither new key. A newly assessed diagnostic
    # has explicit null keys and must not inherit that display compatibility.
    payload['unstructured'].append({'section': 'thresholds', 'text': 'Legacy observation',
                                    'reason': 'PRIVATE_LEGACY_REASON'})
    rows = watch_items(payload)
    texts = [row['text'] for row in rows]
    assert len(rows) == 5
    assert {'Upper boundary', 'Lower boundary', QUOTE, 'Quantity above 12 kg', 'Legacy observation'} == set(texts)
    assert all('PRIVATE_' not in text and '99999' not in text for text in texts)


def test_explicitly_null_business_fields_do_not_turn_a_diagnostic_title_into_a_condition():
    from tin.web.report_watch import watch_items
    rows = watch_items({'unstructured': [
        {'section': 'thresholds', 'text': 'PRIVATE_NULL_DIAGNOSTIC', 'reason': 'Audit only',
         'business_condition': None, 'rule_kind': None},
        {'section': 'falsifiers', 'text': 'Original legacy condition', 'reason': 'Old audit'}]})
    assert [row['text'] for row in rows] == ['Original legacy condition']


def test_prompt_declares_explicit_type_and_literal_business_quote_without_section_heuristics():
    from tin.judgments.prompt import OUTPUT_PROTOCOL
    assert 'business_condition' in OUTPUT_PROTOCOL and 'rule_kind' in OUTPUT_PROTOCOL
    assert 'threshold' in OUTPUT_PROTOCOL and 'falsifier' in OUTPUT_PROTOCOL
    assert '逐字' in OUTPUT_PROTOCOL and 'section' in OUTPUT_PROTOCOL


def test_current_model_omission_does_not_claim_legacy_research_condition_semantics():
    from tin.web.report_watch import watch_items
    body = document()
    body['unresolved'] = [{'section': 'thresholds', 'text': 'PRIVATE_MODEL_DIAGNOSTIC', 'reason': 'Audit only'}]
    original = deepcopy(body)
    result = assess_draft(body, SOURCE, CATALOG, variety='EXAMPLE', actor='owner')
    item = result.result.payload.model_dump()['unstructured'][0]
    assert item['business_condition'] is None and item['rule_kind'] is None
    assert all('PRIVATE_' not in row['text'] for row in watch_items(result.result.payload.model_dump()))
    assert body == original
