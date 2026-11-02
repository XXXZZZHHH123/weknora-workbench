"""A complete item confirmation uses explicit source-local metadata and audit."""
from copy import deepcopy

import httpx
import pytest
from sqlalchemy import select

from tests.test_judgment_generation import generator, generate, endpoint, rows
from tests.test_judgment_review import update, pick
from tests.test_knowledge_facts import canonical, SERIES, FIRST
from tin.integration.workbench_scope import isolated_session_factory
from tin.models import AuditLog
from tin.web import judgment_generation as generation


SOURCE = 'Synthetic observations change continuously, 连续两周。'


def prepare(generator, monkeypatch, *, unit='吨', frequency=None, expression='change > 0'):
    client, headers, _, response, calls = generator
    response.clear()
    response.update({'judgment': {'contradiction': {}, 'falsifiers': [{
        'id': 'continuity', 'desc': 'Synthetic continuous observation condition',
        'series_id': SERIES, 'expression': expression, 'unit_confirmed': '吨',
        'consecutive_periods': 2, 'side': 'bull'}]}, 'binding_units': {SERIES: unit},
        'rule_quotes': {'continuity': SOURCE}, 'unresolved': []})
    original = generation.snapshot

    def metadata(*args, **kwargs):
        value = original(*args, **kwargs)
        value['catalog'] = deepcopy(value['catalog'])
        value['catalog'][0].update(unit=unit, frequency=frequency)
        return value

    async def complete(scope, model, prompt):
        calls.append({'model': model, 'prompt': prompt})
        if prompt.startswith('research-source-inventory.v1'):
            return {'blocks': [{'id': 's0001', 'disposition': 'research', 'reason': 'Synthetic condition'}],
                'items': [{'id': 'i_condition', 'block_ids': ['s0001'], 'quote': SOURCE,
                    'kind': 'falsifier', 'source_unit': None}]}
        return {**deepcopy(response), 'source_coverage': {'i_condition': '/judgment/falsifiers/0'}}

    monkeypatch.setattr(generation, 'snapshot', metadata)
    monkeypatch.setattr(generation, 'completion', complete)
    # The authoritative save reads the live source again. Keep the synthetic
    # upstream metadata consistent with the preview rather than bypassing it.
    original_request = httpx.request

    def source_metadata(method, url, **kwargs):
        result = original_request(method, url, **kwargs)
        if '/api/research/facts' in str(url) and result.status_code == 200:
            body = result.json()
            for field in body.get('indicators', []):
                if field.get('series_id') == SERIES:
                    field.update(unit=unit, frequency=frequency)
            return httpx.Response(200, json=body, request=result.request)
        return result

    monkeypatch.setattr(httpx, 'request', source_metadata)
    result = client.post(endpoint('/generate'), headers=headers,
        json={'model_id': 'synthetic-chat', 'text': SOURCE, 'base_version': 0})
    assert result.status_code == 200, result.text
    return client, headers, result.json()


@pytest.mark.parametrize('unit', ['吨', None])
def test_whole_item_can_explicitly_fill_missing_cadence_with_or_without_source_unit(generator, monkeypatch, unit):
    client, headers, preview = prepare(generator, monkeypatch, unit=unit)
    item = pick(preview, 'falsifiers')
    caliber = item['caliber']
    assert caliber['catalog_frequency'] is None and caliber['can_declare_frequency']
    assert caliber['frequency_required'] is True
    result = update(client, headers, preview, item, caliber={
        'mode': 'catalog' if unit else 'declared', 'unit': '吨', 'frequency': '周',
        'basis': 'User explicitly checked this item metadata'})
    assert result.status_code == 200, result.text
    confirmed = result.json()
    current = pick(confirmed, 'falsifiers')
    assert current['status'] == 'confirmed' and not current['requires_attention']
    assert current['caliber']['frequency'] == '周'
    assert current['caliber']['catalog_frequency'] is None
    saved = client.post(endpoint('/apply'), headers=headers, json={
        'draft_id': confirmed['draft_id'], 'base_version': 0,
        'review_revision': confirmed['review']['revision']})
    assert saved.status_code == 200, saved.text
    declaration = rows()[0].falsifiers[0]['caliber_confirmation']
    assert declaration['frequency'] == '周' and declaration['confirmed_by'] == 'researcher'
    assert declaration['series_id'] == SERIES and declaration['unit'] == '吨'
    with isolated_session_factory(17, FIRST)() as session:
        audit = session.scalar(select(AuditLog).where(AuditLog.action == '研判逐项确认'))
        assert audit.detail['caliber_confirmation']['frequency'] == '周'


def test_manual_metadata_confirmation_does_not_overwrite_a_known_different_cadence(generator, monkeypatch):
    client, headers, preview = prepare(generator, monkeypatch, frequency='日')
    result = update(client, headers, preview, pick(preview, 'falsifiers'), caliber={
        'mode': 'catalog', 'frequency': '周', 'basis': 'User confirmation cannot change a known cadence'})
    assert result.status_code == 422 and rows() == []


def test_manual_metadata_confirmation_does_not_approve_a_fabricated_number(generator, monkeypatch):
    client, headers, preview = prepare(generator, monkeypatch, expression='change > 12345')
    result = update(client, headers, preview, pick(preview, 'falsifiers'), caliber={
        'mode': 'catalog', 'frequency': '周', 'basis': 'Metadata was checked'})
    assert result.status_code == 422 and rows() == []


def test_manual_frequency_confirmation_does_not_skip_current_revision(generator, monkeypatch):
    client, headers, preview = prepare(generator, monkeypatch)
    preview['review']['revision'] += 1
    result = update(client, headers, preview, pick(preview, 'falsifiers'), caliber={
        'mode': 'catalog', 'frequency': '周', 'basis': 'Metadata was checked'})
    assert result.status_code == 409 and rows() == []


def test_fixed_level_without_frequency_does_not_require_an_unnecessary_frequency_declaration(generator, monkeypatch):
    client, headers, _, _, _ = generator
    original = generation.snapshot

    def metadata(*args, **kwargs):
        value = original(*args, **kwargs)
        value['catalog'] = deepcopy(value['catalog'])
        value['catalog'][0]['frequency'] = None
        return value

    monkeypatch.setattr(generation, 'snapshot', metadata)
    generated = generate(client, headers)
    assert generated.status_code == 200, generated.text
    preview = generated.json()
    item = pick(preview, 'thresholds')
    assert item['caliber']['can_declare_frequency']
    assert item['caliber']['frequency_required'] is False
    result = update(client, headers, preview, item)
    assert result.status_code == 200, result.text
    confirmed = pick(result.json(), 'thresholds')
    assert confirmed['status'] == 'confirmed'
    assert confirmed['caliber']['frequency'] is None
