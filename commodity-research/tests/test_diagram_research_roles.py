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
    assert groups['macro']['y'] > groups['target']['y'] + groups['target']['h']
    assert groups['inventory']['y'] + groups['inventory']['h'] < groups['target']['y']
    assert groups['balance']['y'] + groups['balance']['h'] < groups['target']['y']
    assert groups['inventory']['y'] == groups['balance']['y']
    assert groups['macro']['x'] + groups['macro']['w'] / 2 == groups['target']['x'] + groups['target']['w'] / 2
    assert {key: value['role'] for key, value in groups.items()} == {role: role for role in groups}
    assert graph == original


def test_role_slots_align_unequal_nested_groups_and_preserve_topology_and_bindings():
    graph = research_graph()
    graph['groups'].extend([
        {'id': 'supply_nested', 'label': 'Same name', 'parent_id': 'supply'},
        {'id': 'supply_second', 'label': 'Same name', 'parent_id': None, 'role': 'supply'},
        {'id': 'reference', 'label': 'Same name', 'parent_id': None, 'role': 'reference'},
    ])
    graph['nodes'].extend([
        {'id': 'supply_detail_' + str(index), 'label': 'Observed variable',
         'group_id': 'supply_nested', 'series_id': 'synthetic-measurement-' + str(index)}
        for index in range(4)
    ] + [
        {'id': 'second_supply_measure', 'label': 'Observed variable', 'group_id': 'supply_second'},
        {'id': 'reference_measure', 'label': 'Observed variable', 'group_id': 'reference'},
    ])
    graph['edges'] = [{'from': 'supply', 'to': 'target'}, {'from': 'target', 'to': 'demand'},
        {'from': 'macro', 'to': 'target', 'kind': 'influence'},
        {'from': 'inventory', 'to': 'target', 'kind': 'influence'},
        {'from': 'balance', 'to': 'target', 'kind': 'influence'}]
    layout = diagram_md.parse(compile_graph(graph))
    by = {node['id']: node for node in layout['nodes']}
    center = lambda node: (node['x'] + node['w'] / 2, node['y'] + node['h'] / 2)
    supply_height = by['supply_second']['y'] + by['supply_second']['h'] - by['supply']['y']
    supply_center_y = by['supply']['y'] + supply_height / 2
    assert supply_center_y == center(by['target'])[1] == center(by['demand'])[1]
    assert center(by['inventory'])[1] == center(by['balance'])[1]
    assert center(by['macro'])[0] == center(by['target'])[0]
    assert by['reference']['y'] > by['macro']['y'] + by['macro']['h']
    assert by['supply']['x'] + by['supply']['w'] < by['target']['x']
    assert by['target']['x'] + by['target']['w'] < by['demand']['x']
    for node in layout['nodes']:
        assert node['x'] >= diagram_md.PAD and node['y'] >= diagram_md.PAD
        if node.get('parent'):
            parent = by[node['parent']]
            assert parent['x'] <= node['x'] and parent['y'] <= node['y']
            assert parent['x'] + parent['w'] >= node['x'] + node['w']
            assert parent['y'] + parent['h'] >= node['y'] + node['h']
    assert {node['id']: node.get('parent') for node in layout['nodes']} == {
        **{group['id']: group['parent_id'] for group in graph['groups']},
        **{node['id']: node['group_id'] for node in graph['nodes']}}
    assert layout['edges'] == graph['edges']
    assert by['supply_detail_2']['binding'] == {'kind': 'series', 'series_id': 'synthetic-measurement-2'}
    assert diagram_md.parse(diagram_md.dump(layout, layout['name'])) == layout
    original = deepcopy(layout)
    diagram_md._position_research_roles(layout['nodes'])
    assert layout == original, 'Default role positioning is deterministic and idempotent'


def test_role_placement_does_not_create_missing_inventory_macro_or_influence_links():
    graph = research_graph()
    graph['groups'] = [group for group in graph['groups'] if group['role'] in {'supply', 'target', 'demand'}]
    graph['nodes'] = [node for node in graph['nodes'] if node['group_id'] in {'supply', 'target', 'demand'}]
    layout = diagram_md.parse(compile_graph(graph))
    assert layout['edges'] == []
    assert {node.get('role') for node in layout['nodes'] if node.get('kind') == 'group'} == {'supply', 'target', 'demand'}


def test_default_role_positioner_leaves_untyped_manual_coordinates_untouched():
    nodes = [{'id': 'group', 'kind': 'group', 'label': 'macro', 'parent': None,
        'x': 907, 'y': 113, 'w': 520, 'h': 470},
        {'id': 'leaf', 'label': 'supply', 'parent': 'group', 'x': 953, 'y': 252, 'w': 170, 'h': 80}]
    original = deepcopy(nodes)
    diagram_md._position_research_roles(nodes)
    assert nodes == original


def test_default_role_geometry_keeps_explicit_aggregation_static_notes_and_standalone_nodes():
    markdown = ('# Synthetic graph\n'
        '- Other observed variable <!-- id: loose --> <!-- parent: none -->\n'
        '## Same <!-- id: supply --> <!-- role: supply --> <!-- agg: sum(@a, @b) -->\n'
        '- Same [Coverage 23%] <!-- id: a --> <!-- bind: kb-synthetic-a -->\n'
        '- Same <!-- id: b --> <!-- bind: kb-synthetic-b -->\n'
        '### Same <!-- id: nested -->\n- Same <!-- id: child -->\n'
        '## Same <!-- id: target --> <!-- role: target -->\n- Same <!-- id: t -->\n'
        '## Same <!-- id: macro --> <!-- role: macro -->\n- Same <!-- id: m -->\n'
        '@supply -> @target\n@macro -> @target <!-- relation: influence -->\n')
    layout = diagram_md.parse(markdown)
    by = {node['id']: node for node in layout['nodes']}
    assert by['supply']['binding'] == {'kind': 'agg', 'op': 'sum', 'members': ['a', 'b']}
    assert by['a']['statics'] == [{'label': 'Coverage', 'value': '23%'}]
    assert by['child']['parent'] == 'nested' and by['nested']['parent'] == 'supply'
    assert by['loose']['parent'] is None
    assert by['loose']['y'] > by['macro']['y'] + by['macro']['h']
    assert layout['edges'] == [{'from': 'supply', 'to': 'target'},
        {'from': 'macro', 'to': 'target', 'kind': 'influence'}]
    assert diagram_md.parse(diagram_md.dump(layout, layout['name'])) == layout


def test_compact_center_keeps_both_gaps_at_64_with_tall_supply_and_demand():
    nodes = []
    for index, role in enumerate(('supply', 'target', 'demand', 'inventory', 'balance', 'macro')):
        nodes.append({'id': role, 'kind': 'group', 'role': role, 'label': 'Same', 'parent': None,
            'x': index * 300, 'y': 22, 'w': 186, 'h': 1200 if role in {'supply', 'demand'} else 140,
            'binding': {'kind': 'agg', 'op': 'sum', 'members': ['n_' + role]}})
        nodes.append({'id': 'n_' + role, 'label': 'Same', 'parent': role, 'x': index * 300 + 14,
            'y': 68, 'w': 140, 'h': 66, 'binding': {'kind': 'series', 'series_id': 'synthetic-' + role},
            'statics': [{'label': 'Scope', 'value': 'Original'}]})
    original = deepcopy(nodes)
    diagram_md._position_research_roles(nodes)
    groups = {node['id']: node for node in nodes if node.get('kind') == 'group'}
    target, macro = groups['target'], groups['macro']
    stock_bottom = max(groups[role]['y'] + groups[role]['h'] for role in ('inventory', 'balance')) + 18
    assert target['y'] - stock_bottom == 64
    assert macro['y'] - (target['y'] + target['h']) == 64
    assert groups['supply']['y'] + groups['supply']['h'] / 2 == target['y'] + target['h'] / 2
    assert groups['demand']['y'] + groups['demand']['h'] / 2 == target['y'] + target['h'] / 2
    assert min(node['y'] for node in nodes) >= diagram_md.PAD
    for index, first in enumerate(groups.values()):
        for second in list(groups.values())[index + 1:]:
            assert not (first['x'] < second['x'] + second['w'] and second['x'] < first['x'] + first['w']
                and first['y'] < second['y'] + second['h'] and second['y'] < first['y'] + first['h'])
    fields = lambda values: [{key: value for key, value in node.items() if key not in {'x', 'y', 'w', 'h'}} for node in values]
    assert fields(nodes) == fields(original), 'Presentation geometry cannot rewrite hierarchy, aggregation or bindings'


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
