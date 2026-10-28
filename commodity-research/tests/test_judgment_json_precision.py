"""Actual JSON decode boundaries reject rounding before typed/model validation."""
import asyncio
import json
from types import SimpleNamespace

from fastapi import HTTPException
import httpx
import pytest

from tin.web import judgment_generation as generation
from tin.web.diagram_generation import strict_json
from tests.test_knowledge_facts import canonical, FIRST, KB, SERIES
from tests.test_structured_judgment_import import payload, path, saved


@pytest.mark.parametrize('literal,expected', [
    ('0.1', 0.1), ('-0.125', -0.125), ('1.25e-3', 0.00125), ('1.25e+3', 1250),
    ('9007199254740992.0', 9007199254740992), ('0.000', 0), ('5e-324', 5e-324),
])
def test_decoded_numeric_intent_round_trips_without_changing_value(literal, expected):
    assert generation.research_json('{"value":' + literal + '}') == {'value': expected}


@pytest.mark.parametrize('literal', [
    '0.10000000000000001', '-0.10000000000000001', '9007199254740993.0',
    '1e23', '1e-400', '1e400', '1.2345678901234567890123456789',
])
def test_lossy_numbers_are_rejected_before_the_json_token_disappears(literal):
    with pytest.raises(HTTPException) as caught:
        generation.research_json('{"value":' + literal + '}')
    assert caught.value.status_code == 422
    assert caught.value.detail['code'] == 'number_precision'


@pytest.mark.parametrize('raw', ['{"value":NaN}', '{"value":Infinity}',
                               '{"value":1,"value":2}', '{"value":1} trailing'])
def test_new_precision_parser_keeps_strict_json_guards(raw):
    with pytest.raises(HTTPException) as caught:
        generation.research_json(raw)
    assert caught.value.status_code == 422


def test_string_literals_stay_verbatim_and_other_feature_parsers_keep_their_default():
    value = '0.10000000000000001'
    assert generation.research_json(json.dumps({'quote': value})) == {'quote': value}
    assert strict_json('{"value":0.10000000000000001}') == {'value': 0.1}


def test_native_completion_cannot_round_a_model_number_before_source_verification(monkeypatch):
    monkeypatch.setattr(generation.settings, 'weknora_api_url', 'http://native.test')
    original = httpx.AsyncClient
    def upstream(request):
        return httpx.Response(200, json={'success': True, 'data': {'model_id': 'm',
            'finish_reason': 'stop', 'content': '{"value":0.10000000000000001}'}})
    monkeypatch.setattr(generation.httpx, 'AsyncClient', lambda *args, **kwargs: original(*args, **{
        **kwargs, 'transport': httpx.MockTransport(upstream)}))
    scope = SimpleNamespace(workbench_id=FIRST, knowledge_base_id=KB, authorization='Bearer synthetic',
                            identity=SimpleNamespace(tenant_id=17))
    with pytest.raises(HTTPException) as caught:
        asyncio.run(generation.completion(scope, 'm', 'synthetic numeric protocol'))
    assert caught.value.status_code == 422 and caught.value.detail['code'] == 'number_precision'


@pytest.mark.parametrize('literal', ['0.10000000000000001', '1e-400', '9007199254740993'])
def test_typed_import_cannot_save_rounded_or_underflowed_rule_values(canonical, literal):
    client, headers, state, _ = canonical
    document = payload()
    document['thresholds'] = [{'id': 'boundary', 'name': 'Explicit manual observation',
        'series_id': SERIES, 'operator': '>', 'value': 'NUMERIC_TOKEN', 'type': '观察位', 'horizon': '短期'}]
    raw = json.dumps({'payload': document}).replace('"NUMERIC_TOKEN"', literal)
    response = client.post(path('/structured/preview'),
        headers={**headers, 'Content-Type': 'application/json'}, content=raw)
    assert response.status_code == 422, response.text
    assert saved() == []
    assert not any(endpoint == '/api/research/facts' for _, endpoint in state['calls'])
