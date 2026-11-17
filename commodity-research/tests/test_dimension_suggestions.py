"""Dimension draft decisions belong to the model and never change configuration."""
import asyncio
from copy import deepcopy
import json
from types import SimpleNamespace

from fastapi import FastAPI, HTTPException
import httpx
import pytest
from sqlalchemy import select

from tin.models import SourceMapping
from tin.web import dimension_suggestions as suggestions
from tin.web.research_dimensions import default_dimensions, dimension_prompt


def scope(**changes):
    values = {'workbench_id': 'bench', 'restricted': False, 'can_edit': True,
              'authorization': 'Bearer synthetic', 'metadata': {}, 'view_modules': ['indicators'], 'scope_version': 2,
              'identity': SimpleNamespace(can_write=True)}
    values.update(changes)
    return SimpleNamespace(**values)


def catalog(size=4):
    return {f'field-{i}': {'series_id': f'field-{i}', 'metric': f'Synthetic {i}', 'unit': 'unit',
                          'frequency': 'weekly', 'sheet': 'Synthetic worksheet',
                          'source_file': 'Synthetic document', 'dataset_id': 'dataset'} for i in range(size)}


def choice(identifier='field-0', dimension='supply', **changes):
    return {'series_id': identifier, 'dimension': dimension, 'reason': 'Model authored reason', **changes}


def source(identifier='source'):
    return {'kind': 'knowledge_file', 'source_binding_id': 'binding', 'knowledge_id': identifier,
            'file_version': 'a' * 64}


@pytest.fixture(autouse=True)
def direct_reads(monkeypatch):
    async def thread(function, *args, **kwargs):
        return function(*args, **kwargs)
    monkeypatch.setattr(suggestions, 'run_in_threadpool', thread)
    monkeypatch.setattr(suggestions, 'dimensions_for_scope', lambda active: default_dimensions())


def test_model_prompts_use_current_custom_dimensions_at_both_steps():
    dimensions = [{'id': 'd-test', 'label': 'Custom research axis', 'category': '其他', 'description': 'Source authored'}]
    sheets = suggestions.focus_suggestions.worksheets(catalog())
    assert dimension_prompt(dimensions) in suggestions.worksheet_prompt(sheets, dimensions)
    assert dimension_prompt(dimensions) in suggestions.indicator_prompt([], [], 0, dimensions)
    assert '核心关注' not in suggestions.indicator_prompt([], [], 0, dimensions)


def test_model_decisions_preserve_order_without_name_based_grouping_or_completion():
    data, dimensions = catalog(), default_dimensions()
    renamed = deepcopy(data)
    for field in renamed.values():
        field.update(metric='价格供给需求库存宏观', sheet='unrelated')
    document = {'items': [choice('field-2', 'macro', section='Model section'), choice('field-0', 'demand')]}
    first, _ = suggestions.validate_suggestions(document, data, dimensions)
    second, _ = suggestions.validate_suggestions(document, renamed, dimensions)
    assert [(row['series_id'], row['dimension'], row['section']) for row in first] == [
        (row['series_id'], row['dimension'], row['section']) for row in second]
    assert suggestions.validate_suggestions({'items': []}, data, dimensions) == ([], 0)


@pytest.mark.parametrize('document', [None, {'items': 'invalid'}, {'items': [choice('invented')]},
    {'items': [choice(dimension='deleted')]}, {'items': [choice(dimension=['supply'])]},
    {'items': [choice(section='s' * 41)]}, {'items': [choice(reason=None)]}])
def test_invalid_ids_dimension_or_format_fail_without_fallback(document):
    with pytest.raises(HTTPException) as caught:
        suggestions.validate_suggestions(document, catalog(), default_dimensions())
    assert caught.value.status_code == 422


@pytest.mark.parametrize('document', [None, {'worksheets': 'invalid'},
    {'worksheets': [{'worksheet_id': 'invented', 'dimension': 'supply'}]},
    {'worksheets': [{'worksheet_id': 'w1', 'dimension': 'deleted'}]}])
def test_first_step_validates_real_worksheets_and_dimension_ids(document):
    with pytest.raises(HTTPException) as caught:
        suggestions.selected_fields(document, suggestions.focus_suggestions.worksheets(catalog()), default_dimensions())
    assert caught.value.status_code == 422


def test_two_step_flow_uses_only_model_selected_candidates_and_400_limit(monkeypatch):
    data = catalog(450)
    for i, row in enumerate(data.values()):
        row['sheet'] = 'second' if i >= 250 else 'first'
    monkeypatch.setattr(suggestions, 'catalog_snapshot', lambda active: {'catalog': data})
    replies = [{'worksheets': [{'worksheet_id': 'w2', 'dimension': 'supply'},
                               {'worksheet_id': 'w1', 'dimension': 'price'}]},
               {'items': [choice('field-300'), choice('field-1', 'price')]}]
    calls = []

    async def complete(active, model_id, prompt, **kwargs):
        calls.append((prompt, kwargs['purpose']))
        return replies.pop(0)
    monkeypatch.setattr(suggestions.strategy_text, 'completion', complete)
    result = asyncio.run(suggestions.generate_suggestions(scope(), 'model'))
    assert [row['series_id'] for row in result['items']] == ['field-300', 'field-1']
    assert result['field_count'] == 450 and result['selected_field_count'] == 400 and result['truncated']
    assert [purpose for _, purpose in calls] == ['dimension_sheets', 'dimension_fields']
    fields = json.loads(calls[1][0].split('\n当前真实字段：')[1])
    assert [row['series_id'] for row in fields] == [*[f'field-{i}' for i in range(250, 450)],
                                                    *[f'field-{i}' for i in range(200)]]


def test_empty_first_step_makes_no_second_call_or_fixed_suggestions(monkeypatch):
    monkeypatch.setattr(suggestions, 'catalog_snapshot', lambda active: {'catalog': catalog()})
    calls = []

    async def complete(*args, **kwargs):
        calls.append(args)
        return {'worksheets': []}
    monkeypatch.setattr(suggestions.strategy_text, 'completion', complete)
    assert asyncio.run(suggestions.generate_suggestions(scope(), 'model'))['items'] == []
    assert len(calls) == 1


def test_suggestions_deduplicate_and_apply_total_limit():
    rows, total = suggestions.validate_suggestions({'items': [choice('field-4'), choice('field-4', 'demand'),
        *[choice(f'field-{i}') for i in range(105)]]}, catalog(105), default_dimensions())
    assert total == 105 and len(rows) == 96 and rows[0]['dimension'] == 'supply'


def test_inference_generates_fresh_ids_and_unbound_sections():
    raw = {'dimensions': [{'id': 'model-id', 'label': ' Custom ', 'category': '其他', 'description': 'From source',
                           'sections': [{'id': 'invented', 'label': 'Step', 'items': [choice()]}]}]}
    first, second = suggestions.validate_inference(raw), suggestions.validate_inference(raw)
    assert first[0]['id'].startswith('d-') and len(first[0]['id']) == 10
    assert first[0]['id'] != second[0]['id'] and first[0]['label'] == 'Custom'
    assert first[0]['sections'][0]['items'] == []
    assert first[0]['sections'][0]['id'].startswith('s-')


@pytest.mark.parametrize('document', [None, {'dimensions': []},
    {'dimensions': [{'label': 'Custom', 'category': 'invented'}]},
    {'dimensions': [{'label': 'Custom', 'category': '其他', 'sections': ['x'] * 9}]},
    {'dimensions': [{'label': 'Custom', 'category': '其他', 'sections': ['x', 'x']}]},
    {'dimensions': [{'label': 'Custom', 'category': '其他', 'sections': ['x' * 41]}]}])
def test_invalid_inferred_shared_fields_and_sections_fail(document):
    with pytest.raises(HTTPException) as caught:
        suggestions.validate_inference(document)
    assert caught.value.status_code == 422


def test_inference_reads_selected_versioned_sources_and_never_saves(monkeypatch, session):
    active, calls = scope(), []

    async def content(active_scope, binding, identifier, version):
        calls.append((binding, identifier, version))
        return {'knowledge_id': identifier, 'filename': identifier + '.md', 'file_version': version,
                'supported': True, 'text': 'Full original research prose', 'sha256': 'content-digest'}

    async def complete(active_scope, model_id, prompt, **kwargs):
        assert 'Full original research prose' in prompt and kwargs['purpose'] == 'dimension_inference'
        return {'dimensions': [{'label': 'Custom', 'category': '其他', 'sections': ['Stage']}]}
    monkeypatch.setattr(suggestions.strategy_citations, 'read_file_content', content)
    monkeypatch.setattr(suggestions.strategy_text, 'completion', complete)
    body = suggestions.InferenceInput(model_id='model', sources=[source('first'), source('second')])
    result = asyncio.run(suggestions.infer_dimensions(active, body))
    assert len(result['dimensions']) == 1 and len(calls) == 4
    assert calls[:2] == calls[2:]
    assert list(session.scalars(select(SourceMapping))) == []


def test_inference_refuses_unreadable_or_changed_originals(monkeypatch):
    replies = [{'supported': True, 'text': 'original', 'sha256': 'first'},
               {'supported': True, 'text': 'changed', 'sha256': 'second'}]

    async def content(*args):
        return replies.pop(0)
    async def complete(*args, **kwargs):
        return {'dimensions': [{'label': 'Custom', 'category': '其他'}]}
    monkeypatch.setattr(suggestions.strategy_citations, 'read_file_content', content)
    monkeypatch.setattr(suggestions.strategy_text, 'completion', complete)
    body = suggestions.InferenceInput(model_id='model', sources=[source()])
    with pytest.raises(HTTPException) as caught:
        asyncio.run(suggestions.infer_dimensions(scope(), body))
    assert caught.value.status_code == 409
    replies.append({'supported': False, 'notice': 'Unsupported original'})
    with pytest.raises(HTTPException) as caught:
        asyncio.run(suggestions.infer_dimensions(scope(), body))
    assert caught.value.status_code == 422


def test_api_scope_permissions_input_limits_and_model_failures(monkeypatch):
    app = FastAPI()
    app.include_router(suggestions.router)
    active = [scope()]
    monkeypatch.setattr(suggestions, 'current_workbench', SimpleNamespace(get=lambda: active[0]))
    monkeypatch.setattr(suggestions, 'require_shared_write', lambda value: None)

    async def generate(*args):
        raise HTTPException(503, 'Model unavailable; no fixed fallback')
    monkeypatch.setattr(suggestions, 'generate_suggestions', generate)
    async def files(*args):
        return {'items': [{'title': 'Selected file', 'citation': source()}], 'next_offset': None}
    monkeypatch.setattr(suggestions.strategy_citations, '_file_options', files)

    async def check():
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url='http://test') as client:
            assert (await client.post('/api/sn/dimensions/suggestions', json={})).status_code == 422
            assert (await client.post('/api/sn/dimensions/suggestions', json={'model_id': 'model', 'token': 'extra'})).status_code == 422
            response = await client.post('/api/sn/dimensions/suggestions', json={'model_id': 'model'})
            assert response.status_code == 503 and 'fallback' in response.json()['detail']
            for files in ([], [source()] * 2, [source(str(i)) for i in range(4)]):
                assert (await client.post('/api/sn/dimensions/infer', json={'model_id': 'model', 'sources': files})).status_code == 422
            assert (await client.get('/api/sn/dimensions/sources')).json()['items'][0]['title'] == 'Selected file'
            active[0] = scope(scope_version=1)
            assert (await client.post('/api/sn/dimensions/suggestions', json={'model_id': 'model'})).status_code == 409
            for denied in (scope(restricted=True), scope(metadata={'can_read_source': False}), scope(view_modules=[]),
                           scope(identity=SimpleNamespace(can_write=False)), scope(authorization='')):
                active[0] = denied
                for path in ('suggestions', 'infer'):
                    body = {'model_id': 'model', **({'sources': [source()]} if path == 'infer' else {})}
                    assert (await client.post('/api/sn/dimensions/' + path, json=body)).status_code == 403
                assert (await client.get('/api/sn/dimensions/sources')).status_code == 403
    asyncio.run(check())
