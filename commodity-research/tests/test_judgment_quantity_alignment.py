"""The reviewer, rather than the source unit, determines any rule scaling."""
from copy import deepcopy
from datetime import date, datetime
from decimal import Decimal
from types import SimpleNamespace

import pytest
from pydantic import ValidationError
from sqlalchemy import select

from tests.test_judgment_generation import apply, endpoint, generator, rows
from tests.test_judgment_review import pick, update
from tests.test_knowledge_facts import FIRST, SERIES, canonical
from tin.integration.workbench_scope import isolated_session_factory
from tin.judgments.quantity_alignment import magnitude_warning, scaled_values
from tin.models import AuditLog
from tin.schemas.judgment import Falsifier, Threshold
from tin.web import judgment_generation as generation
from tin.web import judgment_quantity as quantity


THRESHOLD = {'id': 'band', 'name': 'Synthetic range', 'series_id': SERIES,
    'operator': 'between', 'value': [158, 163], 'type': '观察位', 'horizon': '短期'}
FALSIFIER = {'id': 'limit', 'desc': 'Synthetic invalidation condition', 'series_id': SERIES,
    'expression': 'value >= 158.09', 'unit_confirmed': '吨', 'consecutive_periods': 1,
    'side': 'bull'}


@pytest.fixture(params=['thresholds', 'falsifiers'])
def proposal(generator, monkeypatch, request):
    client, headers, state, _, _ = generator
    state.update(value=18035, bounded_previous=15800)
    kind = request.param
    rule = THRESHOLD if kind == 'thresholds' else FALSIFIER
    source = 'Stocks between [158, 163] 吨' if kind == 'thresholds' else 'Stocks at 158.09 吨 invalidate the claim.'

    async def complete(scope, model, prompt):
        if prompt.startswith('research-source-inventory.v1'):
            return {'blocks': [{'id': 's0001', 'disposition': 'research', 'reason': 'Rule'}],
                'items': [{'id': 'i_rule', 'block_ids': ['s0001'], 'quote': source,
                    'kind': kind.removesuffix('s'), 'source_unit': None}]}
        return {'judgment': {'contradiction': {}, kind: [deepcopy(rule)]},
            'binding_units': {SERIES: '吨'}, 'rule_quotes': {rule['id']: source},
            'unresolved': [], 'source_coverage': {'i_rule': '/judgment/' + kind + '/0'}}

    monkeypatch.setattr(generation, 'completion', complete)
    response = client.post(endpoint('/generate'), headers=headers,
        json={'model_id': 'synthetic-chat', 'text': source, 'base_version': 0})
    assert response.status_code == 200, response.text
    preview = response.json()
    return client, headers, preview, pick(preview, kind), kind


def test_warn_and_require_explicit_decimal_conversion(proposal):
    client, headers, preview, item, kind = proposal
    params = {'draft_id': preview['draft_id'], 'item_id': item['id'], 'series_id': SERIES, 'exponent': 2}
    response = client.get(endpoint('/review/quantity'), headers=headers, params=params)
    assert response.status_code == 200, response.text
    data = response.json()
    assert data['latest'] == {'value': '18035', 'date': '2026-01-02'}
    assert data['range'] == {'min': '15800', 'max': '18035'}
    assert data['range_start'] == '2023-01-02' and data['range_end'] == '2026-01-02'
    assert '相差约 100 倍' in data['warning']
    assert data['converted_value'] == (['15800', '16300'] if kind == 'thresholds' else '15809')
    assert data['converted_overlaps'] and data['converted_warning'] is None
    forbidden = client.get(endpoint('/review/quantity'), headers=headers,
        params={**params, 'series_id': 'not-in-preview'})
    assert forbidden.status_code == 422
    assert apply(client, headers, preview).status_code == 422
    unconfirmed = update(client, headers, preview, item)
    assert unconfirmed.status_code == 422
    assert unconfirmed.json()['detail']['code'] == 'quantity_alignment'

    choice = {'mode': 'convert', 'exponent': 2}
    converted = update(client, headers, preview, item, quantity_alignment=choice)
    assert converted.status_code == 200, converted.text
    revision = converted.json()
    assert '（原文 ' in revision['markdown']
    assert '按 ×100 换算）' in pick(revision, kind)['rule']['conversion_text']
    saved = apply(client, headers, revision)
    assert saved.status_code == 200, saved.text
    rule = getattr(rows()[0], kind)[0]
    assert rule['scale'] == 100
    if kind == 'thresholds':
        assert rule['source_value'] == [158, 163] and rule['value'] == [15800, 16300]
    else:
        assert rule['source_value'] == 158.09 and rule['value'] == 15809
        assert rule['expression'] == 'value >= 15809'
    with isolated_session_factory(17, FIRST)() as session:
        event = session.scalar(select(AuditLog).where(AuditLog.action == '研判逐项确认'))
        assert event.detail['quantity_alignment']['execution']['value'] == rule['value']
        assert event.detail['quantity_alignment']['alignment'] == choice


def test_keep_requires_reason_and_preserves_old_rule_shape(proposal):
    client, headers, preview, item, kind = proposal
    bulk = client.post(endpoint('/review/confirm-ready'), headers=headers,
        json={'draft_id': preview['draft_id'], 'revision': preview['review']['revision']})
    assert bulk.status_code == 200, bulk.text
    preview = bulk.json()
    assert pick(preview, kind)['requires_attention']
    assert pick(preview, kind)['quantity_reference'] == {
        'range_start': '2023-01-02', 'range_end': '2026-01-02'}
    denied = update(client, headers, preview, item, quantity_alignment={'mode': 'keep', 'reason': '  '})
    assert denied.status_code == 422
    assert '必须填写原因' in denied.json()['detail']['message']
    reason = '原阈值用于极端条件观察。'
    confirmed = update(client, headers, preview, item, quantity_alignment={'mode': 'keep', 'reason': reason})
    assert confirmed.status_code == 200, confirmed.text
    saved = apply(client, headers, confirmed.json())
    assert saved.status_code == 200, saved.text
    rule = getattr(rows()[0], kind)[0]
    assert 'source_value' not in rule and 'scale' not in rule
    if kind == 'thresholds':
        assert rule['value'] == [158, 163]
    else:
        assert rule['expression'] == FALSIFIER['expression']
    with isolated_session_factory(17, FIRST)() as session:
        events = session.scalars(select(AuditLog).where(AuditLog.action == '研判逐项确认')).all()
        assert any(event.detail.get('quantity_alignment', {}).get('alignment', {}).get('reason') == reason
            for event in events)


def test_schema_validates_pair_equality_and_falsifier_execution():
    before = Threshold.model_validate(THRESHOLD).model_dump(mode='json')
    assert 'source_value' not in before and 'scale' not in before
    checked = Threshold.model_validate({**THRESHOLD, 'source_value': [158, 163],
        'value': [15800, 16300], 'scale': 100})
    assert checked.source_value == [158, 163]
    for change in ({'scale': 20}, {'scale': None}, {'source_value': [158, 164]}):
        with pytest.raises(ValidationError):
            Threshold.model_validate({**checked.model_dump(), **change})
    with pytest.raises(ValidationError):
        Falsifier.model_validate({**FALSIFIER, 'source_value': 158.09, 'value': 15809, 'scale': 100})
    assert 'value' not in Falsifier.model_validate(FALSIFIER).model_dump(mode='json')


def test_arithmetic_is_decimal_and_warning_requires_disjoint_ranges():
    assert scaled_values([158.09, 163.077], 100) == [Decimal('15809'), Decimal('16307.7')]
    assert scaled_values(0.000123, 0.0001) == [Decimal('0.0000000123')]
    assert magnitude_warning([100, 1001], {'min': '1000', 'max': '12000'}) is None
    assert magnitude_warning([158, 163], {'min': '168', 'max': '171'}) is None
    assert magnitude_warning([158, 163], {'min': '200', 'max': '260'}) is not None


def test_v2_history_uses_the_direct_indicator_window_and_excludes_old_rows(monkeypatch):
    from tin.integration import page_facts

    scope = SimpleNamespace(scope_version=2)
    day = datetime(2026, 11, 19)
    monkeypatch.setattr(quantity, 'datetime', SimpleNamespace(now=lambda _zone: day))
    requests = []

    def bounded(actual_scope, identifiers, module, **parameters):
        assert actual_scope is scope
        requests.append((identifiers, module, parameters))
        return {'rows': [
            {'series_id': SERIES, 'date': '2023-11-18', 'value': 800},
            {'series_id': SERIES, 'date': '2023-11-19', 'value': 15000},
            {'series_id': SERIES, 'date': '2026-11-18', 'value': 19000},
            {'series_id': SERIES, 'date': '2026-11-19', 'value': 18035},
        ]}

    monkeypatch.setattr(page_facts, 'bounded_facts', bounded)
    history = quantity.read_histories(scope, [SERIES], [{'id': SERIES}])[SERIES]
    assert requests == [([SERIES], 'indicators', {'day': '2026-11-19', 'points': 1100, 'direct_only': True})]
    assert history == {'latest': {'date': '2026-11-19', 'value': '18035'},
        'range': {'min': '15000', 'max': '19000'}, 'range_start': '2023-11-19', 'range_end': '2026-11-19'}


@pytest.mark.parametrize(('scope_version', 'trailing_gaps'), [(2, False), (1, False), (1, True)])
def test_stale_observations_allow_the_observation_backed_conversion(monkeypatch, scope_version, trailing_gaps):
    from tin.integration import page_facts
    from tin.web import judgment_radar

    scope = SimpleNamespace(scope_version=scope_version)
    catalog = [{'id': SERIES}]
    today = datetime(2026, 11, 24)
    monkeypatch.setattr(quantity, 'datetime', SimpleNamespace(now=lambda _zone: today))
    monkeypatch.setattr(quantity, 'LEGACY_HISTORY_PAGE', 2)
    observations = {'2020-09-21': 800, '2020-09-22': 15600, '2021-01-01': None,
        '2022-04-01': 16000, '2023-09-22': 16865, '2026-11-25': 1}
    if trailing_gaps:
        observations.update({'2023-10-01': None, '2024-01-01': None, '2025-01-01': None, '2026-01-01': None})

    def visible_points(cutoff, count, include_invalid=False):
        return [{'date': stamp, 'value': value} for stamp, value in sorted(observations.items())
            if stamp <= cutoff and (value is not None or include_invalid)][-count:]

    def bounded(actual_scope, identifiers, module, *, day, points, direct_only):
        assert (actual_scope, identifiers, module, day, direct_only) == (
            scope, [SERIES], 'indicators', today.date().isoformat(), True)
        return {'rows': [{'series_id': SERIES, **point} for point in visible_points(day, points)]}

    def legacy(actual_scope, identifiers, day: date, fields, points, *, include_invalid=False):
        assert (actual_scope, identifiers, fields) == (scope, [SERIES], catalog)
        assert day <= today.date()
        return {SERIES: {'points': visible_points(day.isoformat(), points, include_invalid)}}

    monkeypatch.setattr(page_facts, 'bounded_facts', bounded)
    monkeypatch.setattr(judgment_radar, 'legacy_series', legacy)
    history = quantity.read_histories(scope, [SERIES], catalog)[SERIES]
    assert history == {'latest': {'date': '2023-09-22', 'value': '16865'},
        'range': {'min': '15600', 'max': '16865'}, 'range_start': '2020-09-22', 'range_end': '2023-09-22'}

    item = {'id': 'band', 'kind': 'thresholds', 'value': THRESHOLD, 'series_id': SERIES,
        'source_quote': 'Stocks between [158, 163] 吨'}
    diagnostic = quantity.diagnostic(item, {'series_id': SERIES, **history}, 2)
    assert '相差约 100 倍' in diagnostic['warning']
    assert diagnostic['converted_overlaps'] is True
    assert quantity.conversion_candidates(item, history) == [{'exponent': 2}]
    assert quantity.pending_alignment(scope, {SERIES: catalog[0]}, [item]) == {'band': {
        'warning': diagnostic['warning'], 'range_start': '2020-09-22', 'range_end': '2023-09-22'}}
    choice = quantity.QuantityAlignment(mode='convert', exponent=2)
    quantity.confirm_quantity(item, choice, history, candidates_only=True)
    assert item['quantity_confirmation']['execution']['value'] == [15800, 16300]
