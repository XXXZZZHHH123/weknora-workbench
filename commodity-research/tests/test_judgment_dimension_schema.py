"""Explicit dimension tags keep the old JSON contract intact when absent."""
from copy import deepcopy
import hashlib
import json

import pytest
from pydantic import ValidationError

from tin.schemas.judgment import JudgmentIn


def legacy_fields():
    return dict(variety='TEST', author='synthetic', written_at='2030-01-02',
        contradiction={'bull': {'evidence': [{'desc': 'Synthetic evidence'}]},
                       'bear': {'evidence': [{'desc': 'Other evidence'}]}},
        thresholds=[{'id': 't', 'name': 'Synthetic limit', 'series_id': 'synthetic',
                     'operator': '>', 'value': 10, 'type': '观察位', 'horizon': '短期'}],
        falsifiers=[{'id': 'f', 'desc': 'Synthetic falsifier', 'series_id': 'synthetic',
                     'expression': 'value > 10', 'unit_confirmed': 'kg', 'side': 'bull'}])


def items(payload):
    return [payload.contradiction.bull.evidence[0], payload.contradiction.bear.evidence[0],
            payload.thresholds[0], payload.falsifiers[0]]


@pytest.mark.parametrize('identifier', ['price', 'supply', 'desk-custom_7', 'aa', 'a' + '0' * 31])
def test_dimension_is_an_open_stable_identifier_for_evidence_and_rules(identifier):
    payload = JudgmentIn(**legacy_fields())
    for item in items(payload):
        tagged = type(item).model_validate({**item.model_dump(), 'dimension': identifier})
        assert tagged.dimension == identifier
        assert tagged.model_dump(mode='json')['dimension'] == identifier


@pytest.mark.parametrize('identifier', ['', 'a', 'Price', 'price space', '中文', 'a/b', 'a' * 33, 12, [], {}])
def test_dimension_rejects_invalid_format_and_non_strings(identifier):
    for item in items(JudgmentIn(**legacy_fields())):
        with pytest.raises(ValidationError):
            type(item).model_validate({**item.model_dump(), 'dimension': identifier})


def test_absent_or_explicit_null_dimension_preserves_legacy_json_roundtrip_and_hash():
    payload = JudgmentIn(**legacy_fields())
    raw = payload.model_dump(mode='json', by_alias=True)
    # Golden digest was obtained from the original HEAD schema, before dimension existed.
    canonical = json.dumps(raw, ensure_ascii=False, sort_keys=True, separators=(',', ':'))
    assert hashlib.sha256(canonical.encode()).hexdigest() == '604f0e68adc86523e9024154e629133c9ab98c8522d4345e91256ceefc2971a0'
    assert JudgmentIn.model_validate(raw).model_dump(mode='json', by_alias=True) == raw
    explicit = deepcopy(raw)
    for item in [explicit['contradiction'][side]['evidence'][0] for side in ('bull', 'bear')] + explicit['thresholds'] + explicit['falsifiers']:
        item['dimension'] = None
    assert JudgmentIn.model_validate(explicit).model_dump(mode='json', by_alias=True) == raw
    assert all('dimension' not in item.model_dump() for item in items(payload))
    from tin.judgments.markdown import dump_markdown
    assert hashlib.sha256(dump_markdown(payload).encode()).hexdigest() == '986e81061ec9ece75d10bc191d9a0f9f780c774b864c24803bba48ad0a2669b3'
