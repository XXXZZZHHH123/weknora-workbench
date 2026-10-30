from copy import deepcopy

from fastapi import HTTPException
import pytest

from tin.export import diagram_md
from tin.web.diagram_draft import compile_graph


def research_graph():
    roles = ['demand', 'inventory', 'target', 'macro', 'balance', 'supply']
    return {'title': 'Synthetic research',
        'groups': [{'id': role, 'label': 'Same name', 'parent_id': None, 'role': role} for role in roles],
        'nodes': [{'id': 'n_' + role, 'label': 'Observed variable', 'group_id': role,
                   'series_id': None} for role in roles], 'edges': []}


def test_model_declared_roles_put_target_between_supply_and_demand_without_name_matching():
    graph = research_graph()
    original = deepcopy(graph)
    markdown = compile_graph(graph)
    layout = diagram_md.parse(markdown)
    groups = {node['id']: node for node in layout['nodes'] if node.get('kind') == 'group'}
    assert groups['supply']['x'] < groups['target']['x'] < groups['demand']['x']
    assert groups['macro']['y'] < groups['target']['y']
    assert groups['inventory']['y'] > groups['target']['y']
    assert groups['balance']['y'] > groups['target']['y']
    assert {key: value['role'] for key, value in groups.items()} == {role: role for role in groups}
    assert graph == original


def test_roles_survive_markdown_export_import_without_static_percentage_placeholders():
    layout = diagram_md.parse(compile_graph(research_graph()))
    restored = diagram_md.parse(diagram_md.dump(layout, layout['name']))
    assert restored == layout
    assert not any(node.get('statics') for node in layout['nodes'])
    assert '[占比' not in diagram_md.dump(layout)


@pytest.mark.parametrize('change', [
    lambda graph: graph['groups'][0].update(role='unknown'),
    lambda graph: graph['groups'][0].update(role='target'),
    lambda graph: graph['groups'][2].update(role='reference'),
    lambda graph: graph['groups'][0].update(parent_id='target'),
])
def test_invalid_or_ambiguous_role_assignments_fail_before_layout(change):
    graph = research_graph()
    change(graph)
    with pytest.raises(HTTPException) as error:
        compile_graph(graph)
    assert error.value.status_code == 422


@pytest.mark.parametrize('text', [
    '# Research\n## Group <!-- role: unknown -->\n- Field',
    '# Research\n## Group <!-- role: supply -->\n- Field',
    '# Research\n## Group <!-- role: target -->\n### Nested <!-- role: inventory -->\n- Field',
    '# Research\n## Group <!-- role: target -->\n- Field <!-- role: target -->',
])
def test_manual_role_metadata_is_explicit_and_never_guessed(text):
    with pytest.raises(diagram_md.MarkdownError):
        diagram_md.parse(text)


def test_old_name_only_diagrams_keep_their_original_linear_layout():
    layout = diagram_md.parse('# Old\n## target\n- A\n## supply\n- B\n## demand\n- C')
    groups = [node for node in layout['nodes'] if node.get('kind') == 'group']
    assert [node['x'] for node in groups] == sorted(node['x'] for node in groups)
    assert not any('role' in node for node in layout['nodes'])


def test_influence_edges_round_trip_separately_from_default_material_flows():
    graph = research_graph()
    graph['edges'] = [{'from': 'macro', 'to': 'target', 'kind': 'influence'},
                      {'from': 'supply', 'to': 'target'}]
    markdown = compile_graph(graph)
    assert '@macro -> @target <!-- relation: influence -->' in markdown
    layout = diagram_md.parse(markdown)
    assert layout['edges'] == [{'from': 'macro', 'to': 'target', 'kind': 'influence'},
                               {'from': 'supply', 'to': 'target'}]
    assert diagram_md.parse(diagram_md.dump(layout, layout['name'])) == layout


@pytest.mark.parametrize('relation', ['unknown', 'influence --> <!-- relation: flow'])
def test_invalid_or_duplicated_edge_relations_are_rejected(relation):
    markdown = '# Graph\n## Group\n- A <!-- id: a -->\n- B <!-- id: b -->\n@a -> @b <!-- relation: ' + relation + ' -->'
    with pytest.raises(diagram_md.MarkdownError):
        diagram_md.parse(markdown)


def test_same_endpoints_cannot_silently_change_material_flow_into_an_influence():
    graph = research_graph()
    graph['edges'] = [{'from': 'supply', 'to': 'target'},
                      {'from': 'supply', 'to': 'target', 'kind': 'influence'}]
    with pytest.raises(HTTPException):
        compile_graph(graph)


def test_shared_diagram_keeps_only_valid_presentation_roles_and_edge_relations():
    from tin.web.restricted_views import readonly_diagram_layout
    graph = research_graph()
    graph['edges'] = [{'from': 'macro', 'to': 'target', 'kind': 'influence'}]
    layout = diagram_md.parse(compile_graph(graph))
    layout['nodes'][0]['private_alias_history'] = 'DO_NOT_SHARE'
    layout['edges'][0]['private_evidence'] = 'DO_NOT_SHARE'
    shared = readonly_diagram_layout(layout)
    assert next(node for node in shared['nodes'] if node['id'] == 'target')['role'] == 'target'
    assert shared['edges'] == [{'from': 'macro', 'to': 'target', 'kind': 'influence'}]
    assert 'DO_NOT_SHARE' not in repr(shared)


def test_shared_diagram_does_not_echo_unknown_roles_or_relation_fields():
    from tin.web.restricted_views import readonly_diagram_layout
    layout = {'nodes': [{'id': 'g', 'label': 'Group', 'kind': 'group', 'role': 'private-meaning'},
                        {'id': 'n', 'label': 'Node', 'role': 'target'}],
              'edges': [{'from': 'g', 'to': 'n', 'kind': 'private-relation'}]}
    shared = readonly_diagram_layout(layout)
    assert not any('role' in node for node in shared['nodes'])
    assert shared['edges'] == [{'from': 'g', 'to': 'n'}]
