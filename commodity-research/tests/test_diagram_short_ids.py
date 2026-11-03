"""The model sees short catalog IDs; every answer is still checked against the shown fields."""
from copy import deepcopy

import pytest

from tests.test_diagram_consistency import bound_graph, request, stable_generator, structure  # noqa: F401
from tests.test_diagram_generation import catalog_bytes, catalog_fields, endpoint, generator, is_outline, model_graph, model_outline, shown  # noqa: F401
from tests.test_knowledge_facts import SERIES, canonical  # noqa: F401
from tin.web import diagram_generation as generation


def test_short_ids_map_back_to_exactly_the_shown_fields():
    fields = [{'id': 'kb-' + 'a' * 48, 'name': 'First'}, {'id': 'kb-' + 'b' * 48, 'name': 'Second'}]
    visible, mapping = generation.short_ids(fields)
    assert [item['id'] for item in visible] == ['c1', 'c2']
    assert [item['name'] for item in visible] == ['First', 'Second'] and fields[0]['id'].startswith('kb-')
    assert mapping == {'c1': fields[0]['id'], 'c2': fields[1]['id'],
                       fields[0]['id']: fields[0]['id'], fields[1]['id']: fields[1]['id']}
    assert generation.selection_ids({'series_ids': ['c2', fields[0]['id']]}, mapping) == ['c2', fields[0]['id']]
    with pytest.raises(generation.SelectionPlanError) as foreign:
        generation.selection_ids({'series_ids': ['c3']}, mapping)
    assert foreign.value.code == 'selection_foreign_id'


def test_real_ids_that_look_like_short_ids_are_shown_unchanged():
    fields = [{'id': 'c2', 'name': 'First'}, {'id': 'other', 'name': 'Second'}]
    visible, mapping = generation.short_ids(fields)
    assert visible == fields and mapping == {'c2': 'c2', 'other': 'other'}


def test_resolution_only_translates_shown_ids_and_leaves_the_rest_to_binding_checks():
    graph = {'title': 't', 'groups': [], 'edges': [], 'nodes': [
        {'id': 'a', 'series_id': 'c1'}, {'id': 'b', 'series_id': 'c9'}, {'id': 'c', 'series_id': None}]}
    original = deepcopy(graph)
    resolved = generation.resolve_series(graph, {'c1': 'real-one'})
    assert [node['series_id'] for node in resolved['nodes']] == ['real-one', 'c9', None]
    assert graph == original
    assert generation.resolve_series(['not', 'a', 'graph'], {'c1': 'x'}) == ['not', 'a', 'graph']


def test_binding_with_a_short_id_is_saved_with_the_real_id(stable_generator):
    client, headers, state = stable_generator
    state['graph']['nodes'][0]['series_id'] = 'c1'
    result = request(client, headers)
    assert result.status_code == 200, result.text
    assert [item['series_id'] for item in result.json()['bindings']] == [SERIES]
    assert '<!-- bind: ' + SERIES + ' -->' in result.json()['markdown']


def test_unknown_short_id_is_rejected_with_the_id_returned_to_the_model(stable_generator):
    client, headers, state = stable_generator
    state['graph']['nodes'][0]['series_id'] = 'c99'
    result = request(client, headers)
    assert result.status_code == 422
    repair = state['completions'][-1]['prompt']
    assert repair.startswith('阶段：覆盖修正') and 'c99' in repair and SERIES not in repair


def test_selection_repair_names_the_unknown_ids_and_keeps_valid_choices(generator, monkeypatch):
    client, headers, _, _, calls = generator
    fields = catalog_fields(3)
    monkeypatch.setattr(generation, 'CATALOG_BYTES', catalog_bytes(fields[:2]), raising=False)
    monkeypatch.setattr(generation, 'catalog_snapshot', lambda scope=None: ('d' * 64, fields))
    responses = [{'series_ids': ['c1', 'c7']}, {'series_ids': ['c1']}, {'series_ids': []}, model_graph(fields[0]['id'])]

    async def complete(scope, model, prompt, image=None):
        calls.append({'prompt': prompt})
        if is_outline(prompt):
            return model_outline()
        return responses.pop(0)

    monkeypatch.setattr(generation, 'completion', complete)
    result = client.post(endpoint(), headers=headers, json={'model_id': 'configured-model', 'instructions': 'Research outline'})
    assert result.status_code == 200, result.text
    repair = calls[2]['prompt']
    assert '"unknown_ids":["c7"]' in repair and '保留上次结果中有效的id' in repair
    assert result.json()['candidate_count'] == 1
    assert shown(calls[-1]['prompt']) == [{key: value for key, value in fields[0].items() if key != 'id'}]


def test_concept_named_exactly_like_a_catalog_field_keeps_it_as_a_candidate(generator, monkeypatch):
    client, headers, _, _, calls = generator
    fields = catalog_fields(3)
    monkeypatch.setattr(generation, 'CATALOG_BYTES', catalog_bytes(fields[:2]), raising=False)
    monkeypatch.setattr(generation, 'catalog_snapshot', lambda scope=None: ('d' * 64, fields))
    graph = model_graph(None)
    graph['nodes'][1]['label'] = fields[2]['name']
    bound = {**graph, 'nodes': [graph['nodes'][0], {**graph['nodes'][1], 'series_id': 'c1'}]}
    responses = [{'series_ids': []}, {'series_ids': []}, bound]

    async def complete(scope, model, prompt, image=None):
        calls.append({'prompt': prompt})
        if is_outline(prompt):
            return model_outline(graph)
        return responses.pop(0)

    monkeypatch.setattr(generation, 'completion', complete)
    result = client.post(endpoint(), headers=headers, json={'model_id': 'configured-model', 'instructions': 'Research outline'})
    assert result.status_code == 200, result.text
    # Recall only adds the exact-name field; the model still decided the binding.
    assert result.json()['candidate_count'] == 1
    assert [item['series_id'] for item in result.json()['bindings']] == [fields[2]['id']]


def concept_setup(monkeypatch, generator, responses):
    client, headers, _, _, calls = generator
    fields = catalog_fields(3)
    monkeypatch.setattr(generation, 'CATALOG_BYTES', catalog_bytes(fields[:2]), raising=False)
    monkeypatch.setattr(generation, 'catalog_snapshot', lambda scope=None: ('d' * 64, fields))

    async def complete(scope, model, prompt, image=None):
        calls.append({'prompt': prompt})
        if is_outline(prompt):
            return model_outline()
        return responses.pop(0)

    monkeypatch.setattr(generation, 'completion', complete)
    result = client.post(endpoint(), headers=headers, json={'model_id': 'configured-model', 'instructions': 'Research outline'})
    return fields, calls, result


def test_shortlist_grouped_by_concept_unions_ranked_candidates(generator, monkeypatch):
    responses = [{'concepts': [{'id': 'feed', 'series_ids': ['c2', 'c1']}, {'id': 'output', 'series_ids': ['c1']}]},
                 {'concepts': []}, None]
    fields, calls, _ = concept_setup(monkeypatch, generator, responses[:2] + [model_graph(None)])
    assert shown(calls[-1]['prompt']) == [{k: v for k, v in field.items() if k != 'id'} for field in fields[:2]]
    assert '每个概念最多3个候选' in calls[1]['prompt']


@pytest.mark.parametrize('answer,issue', [
    ({'concepts': [{'id': 'invented', 'series_ids': ['c1']}]}, 'selection_concept'),
    ({'concepts': [{'id': 'feed', 'series_ids': ['c1', 'c2', 'c1', 'c2']}]}, 'selection_concept_capacity'),
    ({'concepts': [{'id': 'feed', 'series_ids': ['c9']}]}, 'selection_foreign_id'),
    ({'concepts': [{'id': 'feed'}]}, 'selection_shape'),
])
def test_grouped_shortlist_is_checked_per_concept_and_repaired_once(generator, monkeypatch, answer, issue):
    fields, calls, result = concept_setup(monkeypatch, generator, [answer, answer])
    assert result.status_code == 422, result.text
    assert len(calls) == 3 and issue in calls[2]['prompt']
