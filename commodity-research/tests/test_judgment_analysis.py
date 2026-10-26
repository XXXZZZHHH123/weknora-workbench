"""Synthetic research logic only; no desktop methods or commodity examples."""
from datetime import date
import copy
import math
import pytest
from tin.judgments.analysis import AnalysisError, assess_draft, validate_analysis
from tin.judgments.markdown import dump_markdown, version_diff

SID = 'kb-' + '1' * 48
CATALOG = [{'id': SID, 'name': 'Measured quantity', 'unit': 'kg', 'frequency': 'day',
            'knowledge_base_id': 'library', 'source_kind': 'structured'}]
TEXT = 'Production contraction supports prices; weak orders constrain them. Monitor stocks above 12 kg.'


def document():
    return {'judgment': {'contradiction': {'bull': {'claim': 'Production contraction', 'evidence': [
                {'desc': 'Monitor available quantity', 'series_id': SID}]},
                'bear': {'claim': 'Weak orders', 'evidence': []}},
            'marginal_focus': [{'desc': 'Stocks', 'series_id': SID}],
            'thresholds': [{'id': 'watch', 'name': 'Stock boundary', 'series_id': SID,
                'operator': '>', 'value': 12, 'type': '观察位', 'horizon': '短期'}]},
            'binding_units': {SID: 'kg'}, 'rule_quotes': {'watch': 'stocks above 12 kg'},
            'unresolved': [{'section': 'demand', 'text': 'Orders', 'reason': 'No comparable indicator'}]}


def validate(value=None, catalog=CATALOG):
    return validate_analysis(document() if value is None else value, TEXT, catalog,
                             variety='TEST', actor='researcher', written_at=date(2030, 1, 2))


def test_independent_draft_uses_real_ids_units_and_preserves_full_original():
    result = validate()
    assert result.payload.author == 'researcher' and result.payload.source_note == TEXT
    assert result.payload.thresholds[0].value == 12
    assert not result.payload.falsifiers and result.payload.priority_rule is None
    assert result.unresolved[0]['section'] == 'demand'


@pytest.mark.parametrize('change', [
    lambda d: d['judgment']['marginal_focus'][0].update(series_id='invented'),
    lambda d: d['binding_units'].update({SID: 't'}),
    lambda d: d['judgment']['thresholds'][0].update(value=float('inf')),
    lambda d: d['judgment']['thresholds'][0].update(value=True),
    lambda d: d['judgment']['contradiction']['bull']['evidence'][0].update(value=99),
    lambda d: d['rule_quotes'].update(watch='not present in the original'),
    lambda d: d['judgment'].update(author='forged'),
    lambda d: d.update(extra='unexpected'),
])
def test_invalid_binding_unit_number_fact_or_provenance_is_rejected(change):
    value = document(); change(value)
    with pytest.raises(AnalysisError): validate(value)


def test_missing_source_rules_are_not_carried_or_invented():
    result = validate({'judgment': {'contradiction': {}}, 'binding_units': {},
                       'rule_quotes': {}, 'unresolved': []})
    assert result.payload.thresholds == [] and result.payload.falsifiers == []
    assert result.payload.whitelist == [] and result.payload.priority_rule is None


def test_cost_line_references_receive_same_id_and_unit_validation():
    value = document()
    value['judgment']['cost_line'] = {'type': 'cash', 'items': [{'desc': 'Cost basis', 'series_id': 'foreign'}]}
    with pytest.raises(AnalysisError): validate(value)


def test_all_forbidden_cost_facts_are_reported_together_without_mutating_or_accepting_them():
    value = document()
    value['judgment']['cost_line'] = {'type': 'cash', 'value': 41, 'source': 'untrusted text',
        'items': [{'desc': 'First cost', 'series_id': SID, 'value': 11},
                  {'desc': 'Second cost', 'series_id': SID, 'value': 30}]}
    original = copy.deepcopy(value)
    expected = {'/judgment/cost_line/value', '/judgment/cost_line/source',
                '/judgment/cost_line/items/0/value', '/judgment/cost_line/items/1/value'}
    for check in (validate, lambda body: assess_draft(body, TEXT, CATALOG,
                                                   variety='TEST', actor='researcher')):
        with pytest.raises(AnalysisError, match='实际成本值须从知识库读取，不能由模型填造') as caught:
            check(value)
        assert {issue['path'] for issue in caught.value.issues} == expected
        assert all(issue['code'] == 'cost_fact_not_allowed' and 'JSON null' in issue['message']
                   for issue in caught.value.issues)
        assert value == original


def test_markdown_and_diff_are_deterministic_and_show_binding_changes():
    result = validate(); before = dump_markdown(result.payload)
    assert SID in before and '12' in before
    changed = result.payload.model_copy(deep=True)
    changed.thresholds[0].value = 13.0
    diff = version_diff(result.payload, changed)
    assert any(item['field'] == 'thresholds' for item in diff['fields'])
    assert '-| ' in diff['markdown'] and '+| ' in diff['markdown']


@pytest.mark.parametrize('value', [99999, '12', 'NaN', float('nan'), False])
def test_rule_values_must_be_explicit_finite_numeric_source_quantities(value):
    body = document(); body['judgment']['thresholds'][0]['value'] = value
    with pytest.raises(AnalysisError): validate(body)


def test_unrelated_quote_cannot_license_a_fabricated_threshold():
    body = document(); body['rule_quotes']['watch'] = 'weak orders constrain them'
    body['judgment']['thresholds'][0]['value'] = 99999
    with pytest.raises(AnalysisError, match='数字'): validate(body)


def test_falsifier_rhs_and_periods_require_explicit_quoted_numbers():
    body = document(); body['judgment']['falsifiers'] = [{'id':'invalidate','desc':'Invalidate',
        'series_id':SID,'expression':'value > 99999','unit_confirmed':'kg','side':'bull'}]
    body['rule_quotes']['invalidate'] = 'stocks above 12 kg'
    with pytest.raises(AnalysisError, match='证伪数字'): validate(body)


@pytest.mark.parametrize(('quote','value'), [('12,000 kg',12000),('1.2万 kg',12000),('0.12亿 kg',12000000),('１.２万 kg',12000)])
def test_generic_quantity_normalization_has_no_commodity_rules(quote,value):
    body = document(); body['rule_quotes']['watch'] = quote
    body['judgment']['thresholds'][0]['value'] = value
    result = validate_analysis(body, TEXT+' '+quote, CATALOG, variety='TEST', actor='r')
    assert result.payload.thresholds[0].value == value


@pytest.mark.parametrize('change', [
    lambda d:d['judgment']['contradiction']['bull'].update(evidence=None),
    lambda d:d['judgment'].update(cost_line={'items':None}),
    lambda d:d['judgment'].update(thresholds=[None]),
    lambda d:d.update(binding_units=[]), lambda d:d['judgment'].update(whitelist=[SID,SID]),
    lambda d:d['judgment'].update(contradiction=[]),
])
def test_malformed_nested_model_shapes_are_clean_validation_errors(change):
    body=document(); change(body)
    with pytest.raises(AnalysisError):validate(body)


def test_unpaired_unicode_in_model_output_is_a_clean_error():
    body = document(); body['judgment']['tone_note'] = '\ud800'
    with pytest.raises(AnalysisError): validate(body)


def test_extreme_source_exponent_does_not_escape_validation():
    body = document(); body['rule_quotes']['watch'] = '1e999999999999999999999999999999999'
    with pytest.raises(AnalysisError):
        validate_analysis(body, TEXT + ' ' + body['rule_quotes']['watch'], CATALOG,
                          variety='TEST', actor='r')


@pytest.mark.parametrize('section', ['evidence', 'marginal', 'cost'])
def test_optional_binding_must_be_null_or_a_real_id(section):
    body = document()
    if section == 'evidence': body['judgment']['contradiction']['bull']['evidence'][0]['series_id'] = ''
    elif section == 'marginal': body['judgment']['marginal_focus'][0]['series_id'] = ''
    else: body['judgment']['cost_line'] = {'items': [{'desc': 'Unmatched', 'series_id': ''}]}
    with pytest.raises(AnalysisError): validate(body)


def test_partial_draft_preserves_valid_rule_and_explicitly_quarantines_invalid_rule():
    body = document()
    body['judgment']['thresholds'].append({**body['judgment']['thresholds'][0], 'id': 'unsupported', 'value': 99999})
    body['rule_quotes']['unsupported'] = 'stocks above 12 kg'
    result = assess_draft(body, TEXT, CATALOG, variety='TEST', actor='r')
    assert [rule.id for rule in result.result.payload.thresholds] == ['watch']
    assert any(item.get('rule_id') == 'unsupported' and item['code'] == 'number_not_in_source' for item in result.issues)
    assert any('unsupported' in item['text'] and '不执行' in item['text'] for item in result.result.unresolved)
    assert result.result.payload.source_note == TEXT
    assert validate(result.document).payload.thresholds[0].value == 12


def test_null_unit_does_not_block_description_but_cannot_license_a_quantitative_rule():
    catalog = [{**CATALOG[0], 'unit': None}]
    body = document()
    result = assess_draft(body, TEXT, catalog, variety='TEST', actor='r')
    assert result.result.payload.marginal_focus[0].series_id == SID
    assert result.result.binding_units == {SID: None}
    assert result.result.payload.thresholds == []
    assert any(item['code'] == 'unit_metadata_missing' for item in result.issues)
    assert any('目录单位' in item['reason'] or '单位尚未确认' in item['reason'] for item in result.result.unresolved)
    with pytest.raises(AnalysisError, match='单位'):
        validate_analysis(body, TEXT, catalog, variety='TEST', actor='r')


def test_redundant_real_unit_key_is_protocol_cleanup_without_inventing_a_binding():
    extra = 'kb-' + '2' * 48
    body = document(); body['binding_units'][extra] = 'kg'
    result = assess_draft(body, TEXT, [*CATALOG, {**CATALOG[0], 'id': extra}], variety='TEST', actor='r')
    assert result.document['binding_units'] == {SID: 'kg'}
    assert not any(item.get('severity') == 'error' for item in result.issues)
    with pytest.raises(AnalysisError): validate(body)


@pytest.mark.parametrize('change', [
    lambda d: d['judgment']['thresholds'][0].update(series_id='unknown'),
    lambda d: d['binding_units'].update(unknown='kg'),
    lambda d: d['judgment']['contradiction']['bull']['evidence'][0].update(value=999),
    lambda d: d['judgment'].update(extra='unsupported'),
    lambda d: d.pop('unresolved'),
])
def test_partial_draft_does_not_swallow_unknown_identity_facts_or_broken_protocol(change):
    body = document(); change(body)
    with pytest.raises(AnalysisError): assess_draft(body, TEXT, CATALOG, variety='TEST', actor='r')


def test_partial_priority_preserves_original_order_of_surviving_rules_only():
    body = document()
    body['judgment']['thresholds'].append({**body['judgment']['thresholds'][0], 'id': 'unsupported', 'value': 99999})
    body['rule_quotes']['unsupported'] = 'stocks above 12 kg'
    body['judgment']['priority_rule'] = {'order': ['unsupported', 'watch'], 'note': 'Explicit original precedence'}
    result = assess_draft(body, TEXT, CATALOG, variety='TEST', actor='r')
    assert result.result.payload.priority_rule.order == ['watch']
    assert result.result.payload.priority_rule.note == 'Explicit original precedence'


@pytest.mark.parametrize('identifier', [None, [], {}, 7, 'bad id'])
def test_partial_draft_rejects_malformed_rule_identity_without_server_errors(identifier):
    body = document(); body['judgment']['thresholds'][0]['id'] = identifier
    with pytest.raises(AnalysisError, match='ID'):
        assess_draft(body, TEXT, CATALOG, variety='TEST', actor='r')
