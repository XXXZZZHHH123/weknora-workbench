"""Local unit declarations do not invent quantities or mutate the source catalog."""
from copy import deepcopy
import hashlib

import httpx
import pytest

from tests.test_judgment_generation import generator, endpoint, apply, rows
from tests.test_judgment_review import pick, update
from tests.test_knowledge_facts import canonical, SERIES, FIRST, api
from tin.web import judgment_generation as generation


SOURCE = 'Quantity >= 万吨.'


@pytest.fixture
def quantity_case(generator, monkeypatch, request):
    client, headers, state, _, calls = generator
    upstream_request = httpx.request
    model_unit = getattr(request, 'param', '吨')

    def missing_source_unit(method, url, **kwargs):
        response = upstream_request(method, url, **kwargs)
        if str(url).startswith('http://structured-data:19082/') and response.status_code == 200:
            value = response.json()
            if isinstance(value, dict) and 'version' in value:
                value['version'] = hashlib.sha256((value['version'] + ':missing-source-unit').encode()).hexdigest()
                for field in value.get('indicators', []):
                    field['unit'] = None
                return httpx.Response(response.status_code, json=value, request=response.request)
        return response

    async def complete(scope, model, prompt):
        calls.append({'model': model, 'prompt': prompt})
        if prompt.startswith('research-source-inventory.v1'):
            return {'blocks': [{'id': 's0001', 'disposition': 'research', 'reason': 'Explicit quantity condition.'}],
                'items': [{'id': 'quantity_condition', 'block_ids': ['s0001'], 'quote': SOURCE,
                    'kind': 'falsifier', 'source_unit': '吨'}]}
        return {'judgment': {'contradiction': {}, 'falsifiers': [{
            'id': 'quantity_review', 'desc': 'Synthetic quantity invalidates the original claim.',
            'series_id': SERIES, 'expression': 'value >= 10000', 'unit_confirmed': model_unit,
            'consecutive_periods': 1, 'side': 'bull'}]},
            'binding_units': {SERIES: None}, 'rule_quotes': {'quantity_review': SOURCE}, 'unresolved': [],
            'source_coverage': {'quantity_condition': '/judgment/falsifiers/0'}}

    monkeypatch.setattr(httpx, 'request', missing_source_unit)
    monkeypatch.setattr(generation, 'completion', complete)
    generated = client.post(endpoint('/generate'), headers=headers, json={
        'model_id': 'synthetic-chat', 'text': SOURCE, 'base_version': 0})
    assert generated.status_code == 200, generated.text
    preview = generated.json()
    item = pick(preview, 'falsifiers')
    assert item['status'] == 'pending' and item['caliber']['catalog_unit'] is None
    assert item['caliber']['can_declare'] and item['binding']['series_id'] == SERIES
    catalog = client.get(api(FIRST), headers=headers)
    assert catalog.status_code == 200, catalog.text
    assert catalog.json()['indicators'][0]['unit'] == '未注明'
    return client, headers, state, preview, item


@pytest.mark.parametrize('quantity_case', ['吨', 'kg'], indirect=True)
def test_confirming_complete_quantity_unit_makes_the_original_falsifier_saveable(quantity_case):
    client, headers, _, preview, item = quantity_case
    assert rows() == []
    confirmed = update(client, headers, preview, item, caliber={
        'mode': 'declared', 'unit': '吨', 'basis': 'Checked the original quantity header.'})
    assert confirmed.status_code == 200, confirmed.text
    data = confirmed.json()
    checked = pick(data, 'falsifiers')
    assert checked['status'] == 'confirmed'
    assert checked['binding']['unit'] == '吨'
    assert checked['caliber']['catalog_unit'] is None, 'A local declaration cannot alter the source unit'
    assert checked['rule']['expression'] == 'value >= 10000'
    assert checked['rule']['consecutive_periods'] == 1
    saved = apply(client, headers, data)
    assert saved.status_code == 200, saved.text
    record = rows()[0]
    assert record.source_note == SOURCE and not record.thresholds
    assert len(record.falsifiers) == 1
    rule = record.falsifiers[0]
    assert rule['expression'] == 'value >= 10000' and rule['consecutive_periods'] == 1
    assert rule['series_id'] == SERIES and rule['unit_confirmed'] == '吨'
    assert rule['caliber_confirmation']['unit'] == '吨'
    catalog = client.get(api(FIRST), headers=headers)
    assert catalog.status_code == 200, catalog.text
    assert catalog.json()['indicators'][0]['unit'] == '未注明'


@pytest.mark.parametrize('unit', ['', 'kg'])
def test_missing_or_wrong_unit_cannot_confirm_a_different_quantity(quantity_case, unit):
    client, headers, _, preview, item = quantity_case
    original = deepcopy(preview['review'])
    rejected = update(client, headers, preview, item, caliber={
        'mode': 'declared', 'unit': unit, 'basis': 'Checked the original quantity header.'})
    assert rejected.status_code == 422, rejected.text
    reread = client.get(endpoint('/review') + '?draft_id=' + preview['draft_id'], headers=headers)
    assert reread.status_code == 200, reread.text
    assert reread.json() == original, 'A failed confirmation cannot partially change the preview or source unit'
    assert rows() == []
