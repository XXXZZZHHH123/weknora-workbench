"""Related context must preserve the renderer's explicit quantitative records."""
from copy import deepcopy
from datetime import date

import pytest

from tin.judgments.analysis import ALLOWED_FIELDS, AnalysisError, validate_analysis
from tin.judgments.markdown import dump_markdown
from tin.judgments.markdown_protocol import exported_rules, quoted_rule_records, threshold_signature, falsifier_signature
from tin.schemas.judgment import JudgmentIn


PRIMARY, EXTRA = 'synthetic-primary', 'synthetic-context'
CATALOG = [{'id': PRIMARY, 'name': 'Synthetic main', 'unit': 'kg', 'frequency': None},
           {'id': EXTRA, 'name': 'Synthetic context', 'unit': '%', 'frequency': None}]


def payload():
    return JudgmentIn.model_validate({'variety': 'UNIT', 'author': 'tester', 'written_at': date(2030, 1, 1),
        'contradiction': {}, 'thresholds': [{
            'id': 'level', 'name': 'Synthetic level', 'series_id': PRIMARY, 'related_series_ids': [EXTRA],
            'operator': '>', 'value': 12, 'type': '观察位', 'horizon': '短期'}],
        'falsifiers': [{'id': 'change', 'desc': 'Synthetic consecutive change', 'series_id': PRIMARY,
            'related_series_ids': [EXTRA], 'expression': 'change > 0', 'unit_confirmed': 'kg',
            'consecutive_periods': 2, 'side': 'bull'}]})


def exported():
    value = payload()
    text = dump_markdown(value)
    row = next(line for line in text.splitlines() if line.startswith('| Synthetic level |'))
    condition = next(line for line in text.splitlines() if line.startswith('- Synthetic consecutive change'))
    body = value.model_dump(mode='json', by_alias=True)
    document = {'judgment': {key: value for key, value in body.items() if key in ALLOWED_FIELDS},
        'binding_units': {PRIMARY: 'kg', EXTRA: '%'}, 'unresolved': [],
        'rule_quotes': {'level': row, 'change': condition}}
    return value, text, row, condition, document


def test_extra_context_is_separate_from_the_original_six_field_falsifier_record():
    value, text, row, condition, _ = exported()
    assert len(condition[2:].rsplit(' · ', 5)) == 6
    assert EXTRA not in condition and '关联说明：关联指标 `' + EXTRA + '`' in text
    t = threshold_signature(value.thresholds[0].model_dump())
    f = falsifier_signature(value.falsifiers[0].model_dump())
    assert exported_rules(text) == {'thresholds': {PRIMARY: [(t, None)]}, 'falsifiers': {PRIMARY: [f]}}
    assert quoted_rule_records(text, row) == frozenset({('thresholds', PRIMARY, (t, None))})
    assert quoted_rule_records(text, condition) == frozenset({('falsifiers', PRIMARY, f)})


def test_exported_group_rules_still_pass_full_source_and_number_validation():
    _, text, _, _, document = exported()
    result = validate_analysis(document, text, CATALOG, variety='UNIT', actor='tester')
    assert result.payload.falsifiers[0].related_series_ids == [EXTRA]


def test_related_context_does_not_hide_a_changed_falsifier_direction():
    _, text, _, _, document = exported()
    damaged = deepcopy(document)
    damaged['judgment']['falsifiers'][0]['expression'] = 'change < 0'
    with pytest.raises(AnalysisError) as error:
        validate_analysis(damaged, text, CATALOG, variety='UNIT', actor='tester')
    assert error.value.issues[0]['code'] == 'falsifier_source_mismatch'


def test_related_threshold_notes_do_not_interrupt_the_following_explicit_table_rows():
    value = payload()
    second = value.thresholds[0].model_copy(update={'id': 'second-level', 'name': 'Synthetic second level', 'value': 24.0})
    value.thresholds.append(second)
    records = exported_rules(dump_markdown(value))
    assert records['thresholds'][PRIMARY] == [
        (threshold_signature(rule.model_dump()), None) for rule in value.thresholds]
