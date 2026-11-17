"""Dimension draft decisions belong to the model and never change configuration."""
import asyncio
from contextlib import nullcontext
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
from tests.test_chart_api import chart_client  # noqa: F401


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


def axes():
    return [{'id': 'supply', 'label': 'Synthetic supply', 'category': '供给', 'description': 'Model context',
             'sections': [{'label': 'Synthetic upstream'}, {'label': 'Synthetic downstream'}]},
            {'id': 'inventory', 'label': 'Synthetic inventory', 'category': '库存', 'description': 'Stored goods'}]


def groups_of(prompt):
    return json.loads(prompt.split('\n研究条目：', 1)[1].split('\n候选指标：', 1)[0])


def fields_of(prompt):
    return json.loads(prompt.split('\n候选指标：', 1)[1])


def response_for(groups, items=None):
    items = items or []
    selected = {(row['dimension'], row.get('section', '')) for row in items}
    return {'items': items, 'empty_reasons': [
        {'dimension': row['dimension'], 'section': row['section'], 'reason': 'No representative evidence'}
        for row in groups if (row['dimension'], row['section']) not in selected]}


def validation_groups(dimensions=None, data=None):
    return [(item, list(data or catalog()))
            for item in suggestions.section_inventory(dimensions or axes())['items']]


@pytest.fixture(autouse=True)
def latest_reads(monkeypatch):
    def facts(active, identifiers, module, **kwargs):
        assert module == 'indicators' and kwargs['points'] == 1 and kwargs['direct_only'] is True
        assert len(identifiers) <= 200
        return {'rows': [{'series_id': identifier, 'date': '2024-03-21'} for identifier in identifiers],
                'source_versions': {}}
    monkeypatch.setattr(suggestions, 'bounded_facts', facts)
    monkeypatch.setattr(suggestions, 'page_read_scope', lambda *args, **kwargs: nullcontext())


def test_model_prompts_use_current_dimensions_and_representativeness_requirements():
    from datetime import date
    dimensions = axes()
    inventory = suggestions.section_inventory(dimensions)
    assert len(inventory['items']) == 3
    groups = validation_groups(dimensions)
    data = {identifier: {**field, 'latest_date': '2024-03-21'} for identifier, field in catalog().items()}
    query = suggestions.query_prompt(dimensions, inventory)
    prompt = suggestions.pick_prompt(groups, data, dimensions, date(2024, 3, 29))
    assert dimension_prompt(dimensions) in query and dimension_prompt(dimensions) in prompt
    assert 'Model context' in query and 'Synthetic downstream' in query
    assert '1–4' in prompt and '合约月份' in prompt and '长期停更' in prompt and 'empty_reasons' in prompt
    assert all({'unit', 'frequency', 'sheet', 'latest_date'} <= set(field) for field in fields_of(prompt).values())


def test_unsaved_template_structure_drives_recall_without_saving(monkeypatch, session):
    dimensions = [{'id': 'd-template', 'label': 'Draft axis', 'category': '供给', 'description': 'Draft structure',
                   'sections': [{'id': 's-draft', 'label': 'Draft stage',
                       'items': [{'kind': 'indicator', 'id': 'draft-forged-item'}]}]}]
    before, calls = deepcopy(dimensions), []
    monkeypatch.setattr(suggestions, 'dimensions_for_scope', lambda active: pytest.fail('must use unsaved draft'))
    monkeypatch.setattr(suggestions, 'catalog_snapshot', lambda active: {'catalog': catalog()})
    active = scope()
    monkeypatch.setattr(suggestions, 'writable_scope', lambda: active)
    async def complete(active, model_id, prompt, **kwargs):
        calls.append(kwargs['purpose'])
        assert 'Draft stage' in prompt and 'Draft structure' in prompt
        assert 'draft-forged-item' not in prompt and 's-draft' not in prompt
        if kwargs['purpose'] == 'dimension_queries':
            return {'queries': {'i1': ['Synthetic']}}
        return response_for(groups_of(prompt), [choice(dimension='d-template', section='Draft stage')])
    monkeypatch.setattr(suggestions.strategy_text, 'completion', complete)
    app = FastAPI()
    app.include_router(suggestions.router)
    async def check():
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url='http://test') as client:
            response = await client.post('/api/sn/dimensions/suggestions', json={
                'model_id': 'model', 'dimensions': dimensions, 'date': '2024-03-29'})
        assert response.status_code == 200
        assert response.json()['items'][0]['section'] == 'Draft stage'
        assert response.json()['as_of'] == '2024-03-29'
    asyncio.run(check())
    assert calls == ['dimension_queries', 'dimension_fields'] and dimensions == before
    assert list(session.scalars(select(SourceMapping))) == []


@pytest.mark.parametrize('mutation', ['empty', 'count', 'duplicate', 'id', 'label', 'category',
    'description', 'sections_type', 'sections_count', 'section_label'])
def test_invalid_draft_structure_fails_before_catalog_or_model(monkeypatch, mutation):
    dimensions = default_dimensions()
    if mutation == 'empty': dimensions = []
    elif mutation == 'count': dimensions *= 3
    elif mutation == 'duplicate': dimensions[1]['id'] = dimensions[0]['id']
    elif mutation == 'id': dimensions[0]['id'] = 'bad id'
    elif mutation == 'label': dimensions[0]['label'] = 'x' * 25
    elif mutation == 'category': dimensions[0]['category'] = 'invented'
    elif mutation == 'description': dimensions[0]['description'] = 'x' * 201
    elif mutation == 'sections_type': dimensions[0]['sections'] = 'invented'
    elif mutation == 'sections_count': dimensions[0]['sections'] = [{'label': 'Stage'}] * 9
    elif mutation == 'section_label': dimensions[0]['sections'] = [{'label': ' '}]
    monkeypatch.setattr(suggestions, 'catalog_snapshot', lambda active: pytest.fail('invalid structure must fail first'))
    with pytest.raises(HTTPException) as caught:
        asyncio.run(suggestions.generate_suggestions(scope(), 'model', dimensions))
    assert caught.value.status_code == 422


def test_every_section_recalled_from_whole_catalog_with_partial_queries(monkeypatch):
    data = catalog(1800)
    data['field-1700'].update(metric='Synthetic upstream target', name='Readable upstream target')
    data['field-1798'].update(metric='Synthetic downstream target')
    data['field-1799'].update(metric='Synthetic inventory target')
    monkeypatch.setattr(suggestions, 'catalog_snapshot', lambda active: {'catalog': data})
    calls = []
    async def complete(active, model_id, prompt, **kwargs):
        calls.append(kwargs['purpose'])
        if kwargs['purpose'] == 'dimension_queries':
            return {'queries': {'i1': ['Readable upstream target']}}
        groups = groups_of(prompt)
        assert len(groups) == 3
        assert 'field-1700' in groups[0]['candidates'] and 'field-1798' in groups[1]['candidates']
        assert 'field-1799' in groups[2]['candidates']
        assert fields_of(prompt)['field-1700']['latest_date'] == '2024-03-21'
        return response_for(groups, [choice('field-1700', section='Synthetic upstream'),
            choice('field-1798', section='Synthetic downstream'), choice('field-1799', 'inventory', section='')])
    monkeypatch.setattr(suggestions.strategy_text, 'completion', complete)
    result = asyncio.run(suggestions.generate_suggestions(scope(), 'model', axes(), '2024-03-29'))
    assert [row['series_id'] for row in result['items']] == ['field-1700', 'field-1798', 'field-1799']
    assert result['field_count'] == 1800 and result['selected_field_count'] <= 3 * 20
    assert result['truncated'] is False and all(row['selected_count'] == 1 for row in result['sections'])
    assert result['items'][0]['name'] == 'Readable upstream target' and len(calls) == 2


@pytest.mark.parametrize('document', [None, [], {'queries': []}, {'queries': {'unknown': ['Synthetic']}},
    {'queries': {'i1': ['']}}, {'queries': {'i1': 'Synthetic'}}, {'queries': {'i1': [5]}},
    {'queries': {'i1': ['x' * 61]}}, {'queries': {'i1': ['a'] * 4}}])
def test_invalid_query_format_fails_without_fixed_fallback(monkeypatch, document):
    monkeypatch.setattr(suggestions, 'catalog_snapshot', lambda active: {'catalog': catalog()})
    async def complete(*args, **kwargs): return document
    monkeypatch.setattr(suggestions.strategy_text, 'completion', complete)
    with pytest.raises(HTTPException) as caught:
        asyncio.run(suggestions.generate_suggestions(scope(), 'model', axes()))
    assert caught.value.status_code == 422


def test_picks_cannot_authorize_unshown_id_or_another_sections_candidate():
    dimensions, data = axes(), catalog()
    groups = validation_groups(dimensions, data)
    groups[0] = (groups[0][0], ['field-0'])
    for row in (choice('invented', section='Synthetic upstream'), choice('field-1', section='Synthetic upstream'),
                choice(dimension='deleted'), choice(section='invented'), choice(dimension=['supply']),
                choice(section='Synthetic upstream', reason=None)):
        document = {'items': [row], 'empty_reasons': []}
        with pytest.raises(HTTPException) as caught:
            suggestions.validate_suggestions(document, data, dimensions, groups)
        assert caught.value.status_code == 422


def test_picks_preserve_model_order_without_name_or_date_filtering():
    dimensions, data = axes(), catalog()
    groups = validation_groups(dimensions, data)
    document = response_for([item for item, _ in groups], [choice('field-2', 'inventory', section=''),
                                                        choice('field-0', section='Synthetic upstream')])
    renamed = {identifier: {**field, 'metric': 'unrelated', 'latest_date': '1990-01-01'} for identifier, field in data.items()}
    before, sections = suggestions.validate_suggestions(document, data, dimensions, groups)
    after, _ = suggestions.validate_suggestions(document, renamed, dimensions, groups)
    assert [(row['series_id'], row['dimension'], row['section']) for row in before] == [
        (row['series_id'], row['dimension'], row['section']) for row in after]
    assert sections[1]['empty_reason'] == 'No representative evidence'


@pytest.mark.parametrize('document', [None, {'items': []}, {'items': 'invalid', 'empty_reasons': []},
    {'items': [], 'empty_reasons': []}, {'items': [], 'empty_reasons': [
        {'dimension': 'supply', 'section': 'unknown', 'reason': 'None'}]}])
def test_invalid_pick_format_or_missing_empty_reasons_fail(document):
    with pytest.raises(HTTPException) as caught:
        suggestions.validate_suggestions(document, catalog(), axes(), validation_groups())
    assert caught.value.status_code == 422


def test_per_section_and_total_limits_reject_instead_of_silent_choices():
    data = catalog(100)
    dimensions = [{'id': f'd-{i}', 'label': f'Axis {i}', 'category': '其他',
                   'sections': [{'label': f'Stage {j}'} for j in range(8)]} for i in range(4)]
    groups = validation_groups(dimensions, data)
    too_many = [choice(f'field-{i}', section='Synthetic upstream') for i in range(5)]
    with pytest.raises(HTTPException) as caught:
        suggestions.validate_suggestions({'items': too_many, 'empty_reasons': []}, data, axes(), validation_groups(data=data))
    assert caught.value.status_code == 422 and '4个' in caught.value.detail
    picks = [choice(f'field-{i}', dimensions[i // 32]['id'], section=f'Stage {i // 4 % 8}') for i in range(97)]
    with pytest.raises(HTTPException) as caught:
        suggestions.validate_suggestions(response_for([item for item, _ in groups], picks), data, dimensions, groups)
    assert caught.value.status_code == 422 and '96个' in caught.value.detail


def test_empty_candidates_still_get_model_reason_and_per_section_counts(monkeypatch):
    monkeypatch.setattr(suggestions, 'catalog_snapshot', lambda active: {'catalog': catalog()})
    dimensions = [{'id': 'd-empty', 'label': 'Unfindable', 'category': '其他', 'description': 'No overlap'}]
    calls = []
    async def complete(active, model_id, prompt, **kwargs):
        calls.append(kwargs['purpose'])
        if kwargs['purpose'] == 'dimension_queries': return {'queries': {}}
        groups = groups_of(prompt)
        assert groups[0]['candidates'] == [] and fields_of(prompt) == {}
        return response_for(groups)
    monkeypatch.setattr(suggestions.strategy_text, 'completion', complete)
    result = asyncio.run(suggestions.generate_suggestions(scope(), 'model', dimensions))
    assert result['items'] == []
    assert result['sections'] == [{'dimension': 'd-empty', 'section': '', 'item_id': 'i1', 'section_id': '', 'candidate_count': 0,
                                  'selected_count': 0, 'empty_reason': 'No representative evidence'}]
    assert calls == ['dimension_queries', 'dimension_fields']


def test_prompt_budget_retreats_without_skipping_sections(monkeypatch):
    data, dimensions = catalog(80), axes()
    for field in data.values(): field['source_file'] = 'Long provenance ' * 50
    monkeypatch.setattr(suggestions, 'catalog_snapshot', lambda active: {'catalog': data})
    counts = []
    async def complete(active, model_id, prompt, **kwargs):
        if kwargs['purpose'] == 'dimension_queries':
            return {'queries': {item['id']: ['Synthetic'] for item in suggestions.section_inventory(dimensions)['items']}}
        counts.extend(len(row['candidates']) for row in groups_of(prompt))
        assert len(groups_of(prompt)) == 3 and len(prompt.encode()) <= suggestions.PROMPT_BYTES
        return response_for(groups_of(prompt))
    monkeypatch.setattr(suggestions.strategy_text, 'completion', complete)
    monkeypatch.setattr(suggestions, 'PROMPT_BYTES', 16 * 1024)
    result = asyncio.run(suggestions.generate_suggestions(scope(), 'model', dimensions))
    assert result['recall_per_section'] < 20 and max(counts) < 20
    monkeypatch.setattr(suggestions, 'PROMPT_BYTES', 5 * 1024)
    with pytest.raises(HTTPException) as caught:
        asyncio.run(suggestions.generate_suggestions(scope(), 'model', dimensions))
    assert caught.value.status_code == 413


def test_candidate_dates_bounded_and_source_snapshot_changes_rejected(monkeypatch):
    from datetime import date
    data, calls = catalog(450), []
    for field in data.values(): field.update(source_binding_id='binding', source_version='a' * 64)
    def facts(active, identifiers, module, **kwargs):
        calls.append((identifiers, kwargs))
        return {'rows': [{'series_id': identifiers[-1], 'date': '2024-03-22'}],
                'source_versions': {'binding': 'a' * 64}}
    monkeypatch.setattr(suggestions, 'bounded_facts', facts)
    result = suggestions.candidate_fields(scope(), data, list(data), date(2024, 3, 29))
    assert [len(batch) for batch, _ in calls] == [200, 200, 50]
    assert all(kwargs == {'day': '2024-03-29', 'points': 1, 'direct_only': True} for _, kwargs in calls)
    assert result['field-0']['latest_date'] is None and result['field-449']['latest_date'] == '2024-03-22'
    assert 'latest_date' not in data['field-449']
    data['field-449']['source_version'] = 'b' * 64
    with pytest.raises(HTTPException) as caught:
        suggestions.candidate_fields(scope(), data, list(data), date(2024, 3, 29))
    assert caught.value.status_code == 409


def test_model_failure_at_either_stage_propagates_without_fallback(monkeypatch):
    monkeypatch.setattr(suggestions, 'catalog_snapshot', lambda active: {'catalog': catalog()})
    for failing in ('dimension_queries', 'dimension_fields'):
        async def complete(active, model_id, prompt, **kwargs):
            if kwargs['purpose'] == failing: raise HTTPException(503, 'Unavailable model')
            return {'queries': {}}
        monkeypatch.setattr(suggestions.strategy_text, 'completion', complete)
        with pytest.raises(HTTPException) as caught:
            asyncio.run(suggestions.generate_suggestions(scope(), 'model', axes()))
        assert caught.value.status_code == 503 and caught.value.detail == 'Unavailable model'


@pytest.mark.parametrize('with_ids', [True, False])
def test_duplicate_section_labels_keep_each_real_section_identity(with_ids):
    dimensions = [{'id': 'supply', 'label': 'Synthetic', 'category': '供给', 'sections': [
        {'id': 's-first', 'label': 'Same label'}, {'id': 's-second', 'label': 'Same label'}]}]
    if not with_ids:
        for section in dimensions[0]['sections']:
            section.pop('id')
    clean = suggestions.draft_dimensions(dimensions)
    assert [row.get('id', '') for row in clean[0]['sections']] == (['s-first', 's-second'] if with_ids else ['', ''])
    groups = validation_groups(clean)
    document = {'items': [choice('field-0', section='Same label', item_id='i2')],
                'empty_reasons': [{'dimension': 'supply', 'section': 'Same label', 'item_id': 'i1',
                                   'reason': 'No matching evidence'}]}
    items, sections = suggestions.validate_suggestions(document, catalog(), clean, groups)
    assert items[0]['item_id'] == 'i2' and items[0]['section_id'] == ('s-second' if with_ids else '')
    assert sections[0]['empty_reason'] == 'No matching evidence' and sections[1]['selected_count'] == 1
    document['items'][0].pop('item_id')
    with pytest.raises(HTTPException) as caught:
        suggestions.validate_suggestions(document, catalog(), clean, groups)
    assert caught.value.status_code == 422


def test_candidate_latest_date_through_real_bounded_source_projection(chart_client, monkeypatch):
    from tin.integration.page_facts import bounded_facts, page_read_scope
    from tests.chart_fixtures import SID, OTHER
    client, state = chart_client
    state.app.include_router(suggestions.router)
    monkeypatch.setattr(suggestions, 'bounded_facts', bounded_facts)
    monkeypatch.setattr(suggestions, 'page_read_scope', page_read_scope)
    state.observations[SID] = [('2024-03-20', 10), ('2024-03-22', 11), ('2024-04-01', 12)]
    state.observations[OTHER] = [('2024-04-01', 20)]
    for field in state.fields.values():
        field['source_version'] = 'e' * 64
    async def complete(active, model_id, prompt, **kwargs):
        if kwargs['purpose'] == 'dimension_queries':
            return {'queries': {'i1': ['字段']}}
        fields = fields_of(prompt)
        assert fields[SID]['latest_date'] == '2024-03-22'
        assert fields[OTHER]['latest_date'] is None
        return response_for(groups_of(prompt), [choice(SID, 'supply', section='')])
    monkeypatch.setattr(suggestions.strategy_text, 'completion', complete)
    response = client.post('/api/sn/dimensions/suggestions', json={'model_id': 'model', 'date': '2024-03-29',
        'dimensions': [{'id': 'supply', 'label': '字段', 'category': '供给'}]})
    assert response.status_code == 200, response.text
    assert response.json()['items'][0]['latest_date'] == '2024-03-22'
    bounded = [body for _, body in state.requests if not body.get('catalog_only')]
    assert len(bounded) == 1 and bounded[0]['points_per_series'] == 1 and bounded[0]['as_of'] == '2024-03-29'


@pytest.mark.parametrize('day', ['2024-02-30', '20240329', 'not-a-date'])
def test_invalid_recommendation_cutoff_rejected_before_catalog(monkeypatch, day):
    monkeypatch.setattr(suggestions, 'catalog_snapshot', lambda active: pytest.fail('invalid cutoff must fail first'))
    with pytest.raises(HTTPException) as caught:
        asyncio.run(suggestions.generate_suggestions(scope(), 'model', axes(), day))
    assert caught.value.status_code == 400


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
