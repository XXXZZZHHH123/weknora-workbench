"""Synthetic Markdown provenance only; no network, model, database or business data."""
from copy import deepcopy
from datetime import date
from decimal import Decimal

import pytest

from tin.judgments.analysis import AnalysisError, assess_draft, validate_analysis
from tin.judgments.markdown import dump_markdown
from tin.judgments.markdown_protocol import exported_rules
from tin.schemas.judgment import JudgmentIn


SID = 'kb-' + '3' * 48
OTHER_SID = 'kb-' + '4' * 48
CATALOG = [
    {'id': sid, 'name': 'Synthetic quantity ' + str(index), 'unit': 'kg',
     'frequency': 'day', 'knowledge_base_id': 'synthetic-library', 'source_kind': 'structured'}
    for index, sid in enumerate((SID, OTHER_SID))
]
WRITTEN_AT = date(2030, 1, 2)


def _fields():
    return {
        'contradiction': {
            'bull': {'claim': 'First untested hypothesis', 'evidence': [
                {'desc': 'Synthetic measured quantity', 'series_id': SID}]},
            'bear': {'claim': 'Second untested hypothesis', 'evidence': []},
        },
        'thresholds': [{
            'id': 'interval_watch', 'name': 'Synthetic interval', 'series_id': SID,
            'operator': 'between', 'value': [13.5, 28], 'type': '观察位', 'horizon': '短期',
            'action_hint': 'Keep the supplied monitoring action',
            'note': 'Annotation 27 is not an interval boundary',
        }],
        'falsifiers': [
            {'id': 'first_hypothesis', 'desc': '上涨、供应收缩；注记数字28与3不改变明确条件',
             'series_id': SID, 'expression': 'value > 13.5', 'unit_confirmed': 'kg',
             'consecutive_periods': 2, 'side': 'bull', 'note': 'Preserve the first rule note'},
            {'id': 'second_hypothesis', 'desc': '下跌、供应宽松；仅采用明确的证伪对象',
             'series_id': OTHER_SID, 'expression': 'change < 28', 'unit_confirmed': 'kg',
             'consecutive_periods': 3, 'side': 'bear', 'note': 'Preserve the second rule note'},
        ],
    }


def _markdown(fields):
    return dump_markdown(JudgmentIn.model_validate({
        **fields, 'variety': 'TEST', 'author': 'synthetic-researcher',
        'written_at': WRITTEN_AT,
    }))


def _line(source, marker):
    return next(line for line in source.splitlines() if marker in line)


def _roundtrip():
    fields = _fields()
    source = _markdown(fields)
    body = {'judgment': fields, 'binding_units': {SID: 'kg', OTHER_SID: 'kg'},
            'rule_quotes': {
                'interval_watch': _line(source, '| Synthetic interval |'),
                'first_hypothesis': _line(source, '- 上涨、供应收缩'),
                'second_hypothesis': _line(source, '- 下跌、供应宽松'),
            }, 'unresolved': []}
    return body, source


def _validate(body, source, catalog=CATALOG):
    return validate_analysis(body, source, catalog, variety='TEST',
                             actor='synthetic-researcher', written_at=WRITTEN_AT)


def _assess(body, source, catalog=CATALOG):
    return assess_draft(body, source, catalog, variety='TEST',
                        actor='synthetic-researcher', written_at=WRITTEN_AT)


def test_markdown_protocol_preserves_both_interval_endpoints_and_explicit_hypothesis_targets():
    body, source = _roundtrip()
    records = exported_rules(source)

    assert records['thresholds'][SID] == [
        (('between', (Decimal('13.5'), Decimal('28')), '观察位', '短期'), None)]
    assert records['falsifiers'][SID] == [
        ('value', '>', Decimal('13.5'), 2, 'bull', 'kg')]
    assert records['falsifiers'][OTHER_SID] == [
        ('change', '<', Decimal('28'), 3, 'bear', 'kg')]
    assert '证伪对象：多方原假设' in body['rule_quotes']['first_hypothesis']
    assert '证伪对象：空方原假设' in body['rule_quotes']['second_hypothesis']


def test_exported_interval_roundtrips_through_quote_validation_without_dropping_rules_or_notes():
    body, source = _roundtrip()
    original = deepcopy(body)
    validated = _validate(body, source)
    assessed = _assess(body, source)

    assert validated.payload.thresholds[0].value == [13.5, 28]
    assert [rule.side for rule in validated.payload.falsifiers] == ['bull', 'bear']
    assert assessed.result.payload.model_dump() == validated.payload.model_dump()
    assert assessed.document == body == original
    assert assessed.issues == []
    assert validated.payload.source_note == source
    assert dump_markdown(validated.payload) == source
    for rule in body['judgment']['thresholds']:
        assert rule['action_hint'] in source and rule['note'] in source
    for rule in body['judgment']['falsifiers']:
        assert rule['note'] in source


@pytest.mark.parametrize('quote', ['[13.5,28]', 'between [13.5,28]'])
def test_short_interval_quote_uses_only_its_own_complete_exported_row(quote):
    body, source = _roundtrip()
    source = source.replace('[13.5, 28.0]', '[13.5,28]')
    body['rule_quotes']['interval_watch'] = quote

    assert _validate(body, source).payload.thresholds[0].value == [13.5, 28]
    assessed = _assess(body, source)
    assert assessed.issues == []
    assert assessed.result.payload.thresholds[0].value == [13.5, 28]


@pytest.mark.parametrize(('kind', 'changes', 'expected_code'), [
    ('thresholds', {'operator': '>=', 'value': 13.5}, 'exported_rule_mismatch'),
    ('thresholds', {'value': [13.5, 27]}, 'exported_rule_mismatch'),
    ('thresholds', {'type': '风控线'}, 'exported_rule_mismatch'),
    ('thresholds', {'horizon': '中长期'}, 'exported_rule_mismatch'),
    ('falsifiers', {'expression': 'value < 13.5'}, 'falsifier_source_mismatch'),
    ('falsifiers', {'expression': 'change > 13.5'}, 'falsifier_source_mismatch'),
    ('falsifiers', {'expression': 'value > 28'}, 'falsifier_source_mismatch'),
    ('falsifiers', {'consecutive_periods': 3}, 'falsifier_source_mismatch'),
    ('falsifiers', {'side': 'bear'}, 'falsifier_source_mismatch'),
    ('falsifiers', {'unit_confirmed': 't'}, 'unit_mismatch'),
])
def test_structured_export_metadata_changes_are_rejected_and_only_the_invalid_rule_is_quarantined(
        kind, changes, expected_code):
    body, source = _roundtrip()
    body['judgment'][kind][0].update(changes)
    original = deepcopy(body)
    invalid_id = body['judgment'][kind][0]['id']
    expected_thresholds = [] if kind == 'thresholds' else ['interval_watch']
    expected_falsifiers = ['second_hypothesis'] if kind == 'falsifiers' else [
        'first_hypothesis', 'second_hypothesis']

    with pytest.raises(AnalysisError) as caught:
        _validate(body, source)
    assert caught.value.issues[0]['code'] == expected_code

    assessed = _assess(body, source)
    assert [rule.id for rule in assessed.result.payload.thresholds] == expected_thresholds
    assert [rule.id for rule in assessed.result.payload.falsifiers] == expected_falsifiers
    assert any(issue.get('rule_id') == invalid_id and issue['code'] == expected_code
               and issue['severity'] == 'error' for issue in assessed.issues)
    assert invalid_id not in assessed.document['rule_quotes']
    assert len(assessed.result.unresolved) == 1
    quarantine = assessed.result.unresolved[0]
    assert invalid_id in quarantine['text'] and '不执行' in quarantine['text']
    assert quarantine['business_condition'] == body['rule_quotes'][invalid_id]
    assert quarantine['rule_kind'] == ('threshold' if kind == 'thresholds' else 'falsifier')
    assert assessed.result.payload.source_note == source
    assert _validate(assessed.document, source).payload.model_dump() == assessed.result.payload.model_dump()
    assert body == original


def test_catalog_unit_change_cannot_rewrite_the_unit_explicitly_recorded_in_source_markdown():
    body, source = _roundtrip()
    # The candidate is internally consistent with the current authorized catalog,
    # but the exported source still explicitly says kg for this falsifier.
    body['binding_units'][SID] = 't'
    body['judgment']['falsifiers'][0]['unit_confirmed'] = 't'
    catalog = [{**row, 'unit': 't'} if row['id'] == SID else row for row in CATALOG]

    with pytest.raises(AnalysisError) as caught:
        _validate(body, source, catalog)
    assert caught.value.issues[0]['code'] == 'falsifier_source_mismatch'
    assessed = _assess(body, source, catalog)
    assert [rule.id for rule in assessed.result.payload.thresholds] == ['interval_watch']
    assert [rule.id for rule in assessed.result.payload.falsifiers] == ['second_hypothesis']
    assert any(issue.get('rule_id') == 'first_hypothesis'
               and issue['code'] == 'falsifier_source_mismatch' for issue in assessed.issues)


@pytest.mark.parametrize('side', ['bull', 'bear'])
def test_free_text_business_words_do_not_decide_which_hypothesis_the_model_plan_falsifies(side):
    source = '上涨、下跌、供应收缩、需求回落均是待研究背景；量化条件为13.5 kg，连续2期。'
    body = {'judgment': {'contradiction': {}, 'falsifiers': [{
        'id': 'model_target', 'desc': 'Model-authored interpretation awaiting review',
        'series_id': SID, 'expression': 'value > 13.5', 'unit_confirmed': 'kg',
        'consecutive_periods': 2, 'side': side,
    }]}, 'binding_units': {SID: 'kg'}, 'rule_quotes': {'model_target': source}, 'unresolved': []}

    assert exported_rules(source) == {'thresholds': {}, 'falsifiers': {}}
    assert _validate(body, source).payload.falsifiers[0].side == side
    assessed = _assess(body, source)
    assert assessed.result.payload.falsifiers[0].side == side
    assert assessed.issues == []


@pytest.mark.parametrize('kind', ['thresholds', 'falsifiers'])
def test_a_quote_cannot_borrow_a_different_same_indicator_rule_from_elsewhere_in_markdown(kind):
    body, _ = _roundtrip()
    source_fields = deepcopy(body['judgment'])
    if kind == 'thresholds':
        extra = {**deepcopy(source_fields[kind][0]), 'id': 'different_threshold',
                 'name': 'Different threshold', 'operator': '>', 'value': 13.5}
        candidate_changes = {'operator': '>', 'value': 13.5}
        expected_code = 'exported_rule_mismatch'
        marker = '| Synthetic interval |'
    else:
        extra = {**deepcopy(source_fields[kind][0]), 'id': 'different_falsifier',
                 'desc': 'Different falsifier', 'side': 'bear'}
        candidate_changes = {'side': 'bear'}
        expected_code = 'falsifier_source_mismatch'
        marker = '- 上涨、供应收缩'
    source_fields[kind].append(extra)
    source = _markdown(source_fields)
    identifier = body['judgment'][kind][0]['id']
    body['judgment'][kind][0].update(candidate_changes)
    body['rule_quotes'][identifier] = _line(source, marker)

    # A matching rule exists elsewhere, but this exact quote explicitly records
    # a different condition/target. Matching only series_id loses provenance.
    with pytest.raises(AnalysisError) as caught:
        _validate(body, source)
    assert caught.value.issues[0]['code'] == expected_code
    assessed = _assess(body, source)
    assert all(rule.id != identifier for rule in getattr(assessed.result.payload, kind))
    assert any(issue.get('rule_id') == identifier and issue['code'] == expected_code
               for issue in assessed.issues)


@pytest.mark.parametrize('kind', ['thresholds', 'falsifiers'])
@pytest.mark.parametrize('same_metadata', [True, False])
def test_repeated_short_quotes_require_identical_explicit_metadata_at_every_location(kind, same_metadata):
    body, _ = _roundtrip()
    fields = deepcopy(body['judgment'])
    extra = {**deepcopy(fields[kind][0]), 'id': 'additional_source_rule'}
    if kind == 'thresholds':
        extra['name'] = 'Additional interval'
        if not same_metadata:
            extra['type'] = '风控线'
        quote = 'between [13.5, 28.0]'
        expected_code = 'exported_rule_mismatch'
    else:
        extra['desc'] = 'Additional explicitly labeled hypothesis'
        if not same_metadata:
            extra['side'] = 'bear'
        quote = '条件：value > 13.5 · 单位：kg · 连续2期'
        expected_code = 'falsifier_source_mismatch'
    fields[kind].append(extra)
    source = _markdown(fields)
    identifier = body['judgment'][kind][0]['id']
    body['rule_quotes'][identifier] = quote
    assert source.count(quote) == 2

    if same_metadata:
        _validate(body, source)
        assert _assess(body, source).issues == []
    else:
        with pytest.raises(AnalysisError) as caught:
            _validate(body, source)
        assert caught.value.issues[0]['code'] == expected_code
        assessed = _assess(body, source)
        assert all(rule.id != identifier for rule in getattr(assessed.result.payload, kind))
        assert any(issue.get('rule_id') == identifier and issue['code'] == expected_code
                   for issue in assessed.issues)


def test_short_quote_repeated_in_unstructured_prose_cannot_choose_the_exported_row_by_guessing():
    body, source = _roundtrip()
    quote = 'between [13.5, 28.0]'
    source += '\nUnstructured annotation: ' + quote + '\n'
    body['rule_quotes']['interval_watch'] = quote

    with pytest.raises(AnalysisError) as caught:
        _validate(body, source)
    assert caught.value.issues[0]['code'] == 'exported_rule_mismatch'
    assessed = _assess(body, source)
    assert assessed.result.payload.thresholds == []
    assert [rule.id for rule in assessed.result.payload.falsifiers] == [
        'first_hypothesis', 'second_hypothesis']


def test_an_exported_quote_cannot_be_rebound_to_another_real_catalog_indicator():
    body, source = _roundtrip()
    body['judgment']['thresholds'][0]['series_id'] = OTHER_SID

    with pytest.raises(AnalysisError) as caught:
        _validate(body, source)
    assert caught.value.issues[0]['code'] == 'exported_rule_mismatch'
    assessed = _assess(body, source)
    assert assessed.result.payload.thresholds == []
    assert [rule.id for rule in assessed.result.payload.falsifiers] == [
        'first_hypothesis', 'second_hypothesis']


def test_an_explicit_falsifier_record_cannot_be_reused_as_a_threshold_record():
    body, _ = _roundtrip()
    fields = deepcopy(body['judgment'])
    fields.pop('thresholds')
    source = _markdown(fields)
    body['judgment']['thresholds'][0].update(operator='>', value=13.5)
    body['rule_quotes']['interval_watch'] = _line(source, '- 上涨、供应收缩')

    with pytest.raises(AnalysisError) as caught:
        _validate(body, source)
    assert caught.value.issues[0]['code'] == 'exported_rule_mismatch'
    assessed = _assess(body, source)
    assert assessed.result.payload.thresholds == []
    assert [rule.id for rule in assessed.result.payload.falsifiers] == [
        'first_hypothesis', 'second_hypothesis']
