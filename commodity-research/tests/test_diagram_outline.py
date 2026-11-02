import copy

import pytest
from fastapi import HTTPException

from tin.web.diagram_outline import (
    final_constraint, outline_graph, outline_prompt, selection_context,
    validate_coverage, validate_final, validate_outline,
)


def outline():
    return {
        'title': 'Research structure',
        'groups': [
            {'id': 'up', 'label': 'Inputs', 'role': 'supply', 'parent_id': None},
            {'id': 'core', 'label': 'Core', 'role': 'target', 'parent_id': None},
            {'id': 'down', 'label': 'Applications', 'role': 'demand', 'parent_id': None},
        ],
        'nodes': [
            {'id': 'a', 'label': 'Material availability', 'group_id': 'up'},
            {'id': 'b', 'label': 'Reference price', 'group_id': 'core'},
            {'id': 'c', 'label': 'Consumption', 'group_id': 'down'},
        ],
        'edges': [
            {'from': 'up', 'to': 'core', 'kind': 'flow'},
            {'from': 'core', 'to': 'down', 'kind': 'flow'},
        ],
        'coverage': [
            {'id': 'v1', 'label': 'Material conditions', 'node_ids': ['a']},
            {'id': 'v2', 'label': 'Pricing', 'node_ids': ['b']},
            {'id': 'v3', 'label': 'Applications', 'node_ids': ['c']},
        ],
    }


def final_graph():
    result = outline_graph(outline())
    result['nodes'][0]['series_id'] = 'authorized-field-a'
    return result


def test_outline_normalization_retains_explicit_coverage_without_binding_or_mutating_input():
    raw = outline()
    initial = copy.deepcopy(raw)
    parsed = validate_outline(raw)
    assert raw == initial
    assert parsed == initial
    projected = outline_graph(parsed)
    assert projected['nodes'][0]['series_id'] is None
    assert 'coverage' not in projected
    assert all('series_id' not in node for node in parsed['nodes'])


def test_sparse_evidence_does_not_require_template_roles_or_a_minimum_of_twelve_nodes():
    raw = outline()
    raw['groups'] = [raw['groups'][1]]
    raw['nodes'] = [raw['nodes'][1]]
    raw['coverage'] = [raw['coverage'][1]]
    raw['edges'] = []
    assert len(validate_outline(raw)['nodes']) == 1


def test_source_supported_subgroups_are_kept_and_not_promoted_to_new_research_roles():
    raw = outline()
    raw['groups'].append({'id': 'sub', 'label': 'Source detail', 'parent_id': 'up', 'role': None})
    raw['nodes'][0]['group_id'] = 'sub'
    normalized = validate_outline(raw)
    draft = outline_graph(normalized)
    assert validate_final(normalized, draft)['nodes'][0]['group_id'] == 'sub'
    raw['groups'][-1]['role'] = 'reference'
    with pytest.raises(HTTPException):
        validate_outline(raw)


@pytest.mark.parametrize('change', [
    lambda d: d['nodes'][0].update(series_id='forged'),
    lambda d: d['nodes'][0].update(value=17),
    lambda d: d['coverage'][0].update(quote='not a source quote'),
    lambda d: d['coverage'][0].update(node_ids=[]),
    lambda d: d['coverage'][0].update(node_ids=['absent']),
    lambda d: d['coverage'][0].update(node_ids=['a', 'a']),
    lambda d: d['coverage'].pop(),
    lambda d: d['coverage'][1].update(id='v1'),
    lambda d: d['groups'][0].update(role='target'),
    lambda d: d['groups'][1].update(role='reference'),
    lambda d: d['groups'][0].update(role=None),
    lambda d: d['groups'][1].update(parent_id='up'),
    lambda d: d['edges'][0].update(to='absent'),
    lambda d: d['nodes'][0].update(group_id='absent'),
    lambda d: d['nodes'][0].update(label='Supply -> Product'),
])
def test_invalid_or_incomplete_outline_is_rejected_instead_of_filled_or_guessed(change):
    raw = outline()
    change(raw)
    with pytest.raises(HTTPException) as caught:
        validate_outline(raw)
    assert caught.value.status_code == 422


def test_final_can_bind_or_leave_nodes_empty_without_dropping_evidence():
    original = outline()
    draft = final_graph()
    initial = copy.deepcopy(draft)
    normalized = validate_final(original, draft)
    assert normalized == initial
    assert draft == initial
    assert validate_coverage(original, draft) == normalized


def test_final_can_add_distinct_subgroup_details_with_own_relationships():
    draft = final_graph()
    draft['groups'].append({'id': 'detail', 'label': 'Comparison', 'parent_id': 'up', 'role': None})
    draft['nodes'].append({'id': 'detail_a', 'label': 'Independent dimension',
                           'group_id': 'detail', 'series_id': None})
    draft['edges'].append({'from': 'detail_a', 'to': 'a', 'kind': 'influence'})
    assert len(validate_final(outline(), draft)['nodes']) == 4
    with pytest.raises(HTTPException):
        validate_final(outline(), draft, detail_limit=0)


@pytest.mark.parametrize('change', [
    lambda d: d.update(title='Different target'),
    lambda d: d['nodes'].pop(),
    lambda d: d['nodes'][0].update(label='Different concept'),
    lambda d: d['nodes'][0].update(group_id='core'),
    lambda d: d['groups'][0].update(label='Different meaning'),
    lambda d: d['groups'][0].update(role='reference'),
    lambda d: d['groups'][0].update(parent_id='down', role=None),
    lambda d: d['edges'].pop(),
    lambda d: d['edges'][0].update(kind='influence'),
    lambda d: d['edges'].append({'from': 'a', 'to': 'b', 'kind': 'influence'}),
    lambda d: d['nodes'].append({'id': 'd', 'label': 'Detail', 'group_id': 'up', 'series_id': None}),
    lambda d: (
        d['groups'].append({'id': 'more', 'label': 'New axis', 'parent_id': None, 'role': 'reference'}),
        d['nodes'].append({'id': 'd', 'label': 'Detail', 'group_id': 'more', 'series_id': None}),
    ),
])
def test_final_cannot_silently_replace_skeleton_semantics_or_expand_top_level_roles(change):
    draft = final_graph()
    change(draft)
    with pytest.raises(HTTPException) as caught:
        validate_final(outline(), draft)
    assert caught.value.status_code == 422


def test_final_topology_order_is_not_semantic_and_normalized_edges_are_preserved():
    draft = final_graph()
    draft['groups'].reverse()
    draft['nodes'].reverse()
    draft['edges'].reverse()
    assert validate_final(outline(), draft)['nodes'] == draft['nodes']


def test_protocols_expose_source_outline_and_breadth_priority_without_fake_source_quotes():
    context = selection_context(outline())
    constraints = final_constraint(outline())
    first = outline_prompt('Source description')
    assert 'Source description' in first
    assert 'coverage' in first and 'series_id' in first
    assert 'Reference price' in context and 'v3' in context
    assert 'Reference price' in constraints and 'series_id' in constraints
    assert '广度' in first and '逐字' in first
    assert '真实' in context


def test_outline_hard_capacity_rejects_instead_of_truncating_representative_concepts():
    raw = outline()
    raw['nodes'] = [{'id': f'n{i}', 'label': f'Concept {i}', 'group_id': 'core'} for i in range(33)]
    raw['groups'] = [raw['groups'][1]]
    raw['coverage'] = [{'id': 'full', 'label': 'Coverage', 'node_ids': [n['id'] for n in raw['nodes']]}]
    raw['edges'] = []
    with pytest.raises(HTTPException):
        validate_outline(raw)


@pytest.mark.parametrize('limit', [-1, 65, True, '12', 2.0])
def test_invalid_detail_limit_is_a_caller_error_and_never_silently_coerced(limit):
    with pytest.raises(ValueError):
        final_constraint(outline(), detail_limit=limit)
    with pytest.raises(ValueError):
        validate_final(outline(), final_graph(), detail_limit=limit)


def test_model_instances_and_default_edge_kind_are_revalidated_and_normalized():
    from tin.web.diagram_outline import Outline

    raw = outline()
    for edge in raw['edges']:
        del edge['kind']
    source = Outline.model_validate(raw)
    normalized = validate_outline(source)
    assert all(edge['kind'] == 'flow' for edge in normalized['edges'])
    assert validate_final(source, outline_graph(source))['edges'] == normalized['edges']
