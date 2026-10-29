"""Synthetic source-unit boundaries; no unit aliases, conversions, or model calls."""
from copy import deepcopy
from decimal import Decimal
from types import SimpleNamespace

import pytest
from tests.test_judgment_generation import generator
from tests.test_knowledge_facts import canonical

from tin.judgments.analysis import AnalysisError, assess_draft, validate_analysis
from tin.judgments.numbers import quoted_quantities
from tin.web import judgment_review as review


CASES = [
    ('库存超过12吨时复核。', '吨', 'kg', 12),
    ('库存超过12吨。', '吨', 'kg', 12),
    ('库存超过1千克时复核。', '千克', '吨', 1),
    ('库存超过1公斤时复核。', '公斤', '吨', 1),
    ('运量超过12吨公里时复核。', '吨公里', '公斤', 12),
    ('运量超过12吨公里时复核。', '吨公里', '吨', 12),
    ('价格超过12元/千克时复核。', '元/千克', '元/吨', 12),
    ('价格超过12元/千克。', '元/千克', '元/吨', 12),
    ('价格超过12元/kg时复核。', '元/kg', '元/吨', 12),
    ('速度超过12米/秒时复核。', '米/秒', '米/小时', 12),
    ('库存区间为[12, 18]吨时复核。', '吨', 'kg', 12),
]


def catalog(source_unit, selected_unit):
    return [{'id': 'synthetic-original', 'name': 'Synthetic source field', 'unit': source_unit},
        {'id': 'synthetic-selected', 'name': 'Synthetic selected field', 'unit': selected_unit}]


def envelope(quote, unit, value, *, kind='thresholds', selected='synthetic-selected'):
    if kind == 'thresholds':
        rule = {'id': 'boundary', 'name': 'Synthetic condition', 'series_id': selected,
            'operator': '>', 'value': value, 'type': '观察位', 'horizon': '短期'}
    else:
        rule = {'id': 'boundary', 'desc': 'Synthetic condition', 'series_id': selected,
            'expression': 'value > ' + str(value), 'unit_confirmed': unit, 'side': 'bull'}
    return {'judgment': {'contradiction': {}, kind: [rule]},
        'binding_units': {selected: unit}, 'rule_quotes': {'boundary': quote}, 'unresolved': []}


@pytest.mark.parametrize('quote,source_unit,selected_unit,value', CASES)
def test_full_catalog_does_not_turn_source_quantity_into_another_unit(quote, source_unit, selected_unit, value):
    metadata = catalog(source_unit, selected_unit)
    values = quoted_quantities(quote, unit=selected_unit, units=[field['unit'] for field in metadata])
    assert Decimal(str(value)) not in values


@pytest.mark.parametrize('kind', ['thresholds', 'falsifiers'])
@pytest.mark.parametrize('quote,source_unit,selected_unit,value', CASES)
def test_analysis_rejects_same_digits_bound_to_another_real_unit(kind, quote, source_unit, selected_unit, value):
    document = envelope(quote, selected_unit, value, kind=kind)
    unchanged = deepcopy(document)
    with pytest.raises(AnalysisError):
        validate_analysis(document, quote, catalog(source_unit, selected_unit),
            variety='TEST', actor='synthetic')
    assert document == unchanged


def test_reselecting_another_real_field_cannot_reinterpret_source_tonnes_as_kg():
    quote = '库存超过12吨时复核。'
    metadata = catalog('吨', 'kg')
    original = envelope(quote, '吨', 12, selected='synthetic-original')
    assessment = assess_draft(original, quote, metadata, variety='TEST', actor='synthetic')
    preview = {'text': quote, 'variety': 'TEST', 'written_at': '2030-01-02'}
    review.initialize_review(preview, assessment, metadata)
    item = next(item for item in preview['review']['items'] if item['kind'] == 'thresholds')
    item.update(series_id='synthetic-selected', binding_explicit=True)

    assert review.confirmation_error(preview, item, metadata,
        SimpleNamespace(identity=SimpleNamespace(user_id='synthetic')))


@pytest.mark.parametrize('quote', [
    '数量超过12未知单位。', '数量超过12 未知单位。',
    '数量超过12未知单位时复核。',
])
def test_unknown_explicit_source_unit_cannot_become_bare_selected_unit_evidence(quote):
    # The only real catalog unit is kg. Unknown source text must not silently
    # acquire it merely because no other unit literal is present in metadata.
    assert Decimal('12') not in quoted_quantities(quote, unit='kg', units=['kg'])


@pytest.mark.parametrize('quote,unit,value', [
    ('库存超过12吨。', '吨', 12),
    ('库存超过1千克。', '千克', 1),
    ('价格超过12元/千克。', '元/千克', 12),
    ('价格超过12元/吨。', '元/吨', 12),
])
def test_exact_source_unit_remains_usable_without_conversion(quote, unit, value):
    metadata = catalog(unit, 'unrelated-unit')
    document = envelope(quote, unit, value, selected='synthetic-original')
    result = validate_analysis(document, quote, metadata, variety='TEST', actor='synthetic')
    assert result.payload.thresholds[0].value == value


@pytest.mark.parametrize('quote', ['12 ounces', '12(kg)', '12 (kg)', '12[kg]', '12 未知复合单位'])
def test_unverified_unit_spelling_is_not_bare_quantity_evidence(quote):
    assert Decimal('12') not in quoted_quantities(quote, unit='吨', units=['吨', 'kg'])


@pytest.mark.parametrize('quote,expected', [
    ('between 12 and 13 kg for observation.', {'12', '13'}),
    ('between -0.25 and 1.5 kg.', {'-0.25', '1.5'}),
    ('between 12 and 13 吨 for observation.', set()),
    ('between 12 oranges and 13 吨.', set()),
])
def test_explicit_pair_shares_only_its_verified_final_unit(quote, expected):
    values = quoted_quantities(quote, unit='kg', units=['kg', '吨'])
    assert values == {Decimal(number) for number in expected}


@pytest.mark.parametrize('quote,forbidden', [
    ('²12 and 13 kg', '12'), ('½12 and 13 kg', '12'),
    ('1\u202f000 and 13 kg', '0'), ('1/2 and 13 kg', '2'),
])
def test_shared_unit_pair_cannot_restore_rejected_numeric_fragments(quote, forbidden):
    assert Decimal(forbidden) not in quoted_quantities(quote, unit='kg', units=['kg'])


@pytest.mark.parametrize('quote', ['Quantity exceeds 12 kg for 2 periods.', '数量高于12kg，连续2期。'])
def test_period_count_is_dimensionless_and_does_not_acquire_quantity_unit(quote):
    body = envelope(quote, 'kg', 12, kind='falsifiers')
    body['judgment']['falsifiers'][0]['consecutive_periods'] = 2
    result = validate_analysis(body, quote, catalog('kg', 'kg'), variety='TEST', actor='synthetic')
    assert result.payload.falsifiers[0].consecutive_periods == 2


def test_review_route_rejects_cross_unit_rebind_without_mutating_preview(generator, monkeypatch):
    from tests.test_judgment_generation import endpoint
    from tests.test_knowledge_facts import SERIES
    from tin.web import judgment_generation as generation
    client, headers, _, model, _ = generator
    source = '库存超过12吨时复核。'
    model['rule_quotes']['watch'] = source
    original_snapshot = generation.snapshot

    def with_other_unit(*args, **kwargs):
        state = original_snapshot(*args, **kwargs)
        state['catalog'].append({'id': 'kg-field', 'name': 'Other quantity', 'unit': 'kg'})
        return state

    monkeypatch.setattr(generation, 'snapshot', with_other_unit)
    response = client.post(endpoint('/generate'), headers=headers, json={
        'model_id': 'synthetic-chat', 'text': source, 'base_version': 0})
    assert response.status_code == 200, response.text
    preview = response.json()
    item = next(item for section in preview['review']['sections'] for item in section['items']
                if item['kind'] == 'thresholds')
    changed = client.post(endpoint('/review/update'), headers=headers, json={
        'draft_id': preview['draft_id'], 'revision': preview['review']['revision'],
        'item_id': item['id'], 'action': 'confirm', 'series_id': 'kg-field'})
    assert changed.status_code == 422, changed.text
    message = changed.json()['detail']['message']
    assert '单位' in message and '原文' in message
    current = client.get(endpoint('/review'), headers=headers, params={'draft_id': preview['draft_id']}).json()
    assert current['revision'] == preview['review']['revision']
    same = next(entry for section in current['sections'] for entry in section['items'] if entry['id'] == item['id'])
    assert same['binding']['series_id'] == SERIES and same['status'] == 'pending'
