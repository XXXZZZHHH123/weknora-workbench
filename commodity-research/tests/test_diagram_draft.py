import copy

import pytest
from fastapi import HTTPException

from tin.web.diagram_draft import compile_graph
from tin.export import diagram_md


def graph():
    return {
        'title': 'Process',
        'groups': [{'id': 'g', 'label': 'Product', 'parent_id': None}],
        'nodes': [{'id': 'a', 'label': 'Product', 'group_id': 'g', 'series_id': None},
                  {'id': 'b', 'label': 'Product', 'group_id': 'g', 'series_id': None}],
        'edges': [{'from': 'a', 'to': 'b'}, {'from': 'b', 'to': 'a'}],
    }


def test_duplicate_display_names_and_cycles_keep_exact_model_references():
    value = graph()
    markdown = compile_graph(value)
    layout = diagram_md.parse(markdown)
    assert {n['id']: (n['label'], n.get('parent')) for n in layout['nodes']} == {
        'g': ('Product', None), 'a': ('Product', 'g'), 'b': ('Product', 'g')}
    assert {(e['from'], e['to']) for e in layout['edges']} == {('a', 'b'), ('b', 'a')}
    assert value == graph()


def test_nested_groups_multiple_incoming_edges_and_real_binding_survive():
    value = graph()
    value['groups'].append({'id': 'sub', 'label': 'Product', 'parent_id': 'g'})
    value['nodes'].append({'id': 'c', 'label': 'Product', 'group_id': 'sub', 'series_id': 'kb-source.field'})
    value['edges'] += [{'from': 'a', 'to': 'c'}, {'from': 'b', 'to': 'c'}, {'from': 'g', 'to': 'sub'}]
    layout = diagram_md.parse(compile_graph(value))
    nodes = {n['id']: n for n in layout['nodes']}
    assert nodes['sub']['parent'] == 'g' and nodes['c']['parent'] == 'sub'
    assert nodes['c']['binding'] == {'kind': 'series', 'series_id': 'kb-source.field'}
    assert {(e['from'], e['to']) for e in layout['edges']} == {(e['from'], e['to']) for e in value['edges']}


@pytest.mark.parametrize('change', [
    lambda d: d['edges'].append({'from': 'missing', 'to': 'a'}),
    lambda d: d['nodes'].append(copy.deepcopy(d['nodes'][0])),
    lambda d: d['groups'][0].update(parent_id='g'),
    lambda d: d['nodes'][0].update(group_id='missing'),
    lambda d: d['groups'].append({'id': 'empty', 'label': 'Empty', 'parent_id': None}),
    lambda d: d['nodes'][0].update(label='Product -> Output'),
    lambda d: d['nodes'][0].update(label='Product <!-- bind: forged -->'),
    lambda d: d['nodes'][0].update(label='Product [count 99]'),
    lambda d: d['nodes'][0].update(label='Product\n- extra'),
    lambda d: d['nodes'][0].update(agg='sum'),
    lambda d: d['nodes'][0].update(id='@invalid'),
    lambda d: d.update(extra='unexpected'),
    lambda d: d.update(nodes=[]),
    lambda d: d.update(groups=[]),
])
def test_invalid_graphs_are_rejected_without_guessing_or_dropping_edges(change):
    value = graph()
    change(value)
    with pytest.raises(HTTPException) as caught:
        compile_graph(value)
    assert caught.value.status_code == 422


def test_deep_group_hierarchy_is_rejected_before_markdown_can_lose_it():
    value = graph()
    for index, parent in [('sub', 'g'), ('third', 'sub'), ('fourth', 'third')]:
        value['groups'].append({'id': index, 'label': 'Group', 'parent_id': parent})
    value['nodes'][0]['group_id'] = 'fourth'
    with pytest.raises(HTTPException):
        compile_graph(value)


def test_total_object_limit_includes_groups_and_never_truncates():
    value = graph()
    value['nodes'] = [{'id': f'n{i}', 'label': 'Repeated', 'group_id': 'g'} for i in range(255)]
    value['edges'] = []
    assert len(diagram_md.parse(compile_graph(value))['nodes']) == 256
    value['nodes'].append({'id': 'overflow', 'label': 'Repeated', 'group_id': 'g'})
    with pytest.raises(HTTPException):
        compile_graph(value)


def test_repeated_edge_deduplication_does_not_drop_other_relations():
    value = graph()
    value['edges'].append(copy.deepcopy(value['edges'][0]))
    layout = diagram_md.parse(compile_graph(value))
    assert {(edge['from'], edge['to']) for edge in layout['edges']} == {('a', 'b'), ('b', 'a')}
    assert len(layout['edges']) == 2


def test_model_edge_limit_is_checked_before_deduplication():
    value = graph()
    value['edges'] = [value['edges'][0]] * 513
    with pytest.raises(HTTPException):
        compile_graph(value)


@pytest.mark.parametrize('separator',['\u2028','\u2029','\x85'])
def test_unicode_line_separators_cannot_silently_truncate_the_title(separator):
    value = graph()
    value['title'] = 'Title' + separator + 'Additional content'
    with pytest.raises(HTTPException):
        compile_graph(value)
