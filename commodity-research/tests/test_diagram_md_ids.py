import pytest

from tin.export import diagram_md as md
from tin.export.diagram import _clean


def topology(layout):
    return ({n['id']:(n['label'],n.get('parent'),n.get('kind'),n.get('binding')) for n in layout['nodes']},
            {(e['from'],e['to']) for e in layout['edges']})


def test_explicit_ids_disambiguate_duplicate_group_and_node_labels():
    result=md.parse('# Synthetic\n## Same <!-- id: g1 -->\n- Same <!-- id: n1 -->\n'
                    '## Same <!-- id: g2 -->\n- Same <!-- id: n2 -->\n@n1 -> @n2\n@g1 -> @g2')
    assert result['reference_mode']=='id'
    assert {n['id'] for n in result['nodes']}=={'g1','g2','n1','n2'}
    assert all(n['reference_id']==n['id'] for n in result['nodes'])
    assert {(e['from'],e['to']) for e in result['edges']}=={('n1','n2'),('g1','g2')}


@pytest.mark.parametrize('text',[
    '# T\n## Group <!-- id: dup -->\n- Node <!-- id: dup -->',
    '# T\n## Group\n- One <!-- id: dup -->\n- Two <!-- id: dup -->',
    '# T\n## Group\n- One <!-- id: a --> <!-- id: b -->',
])
def test_duplicate_explicit_ids_are_rejected(text):
    with pytest.raises(md.MarkdownError):md.parse(text)


@pytest.mark.parametrize('token',['bad id','@bad','bad->id','bad/id','x'*129,''])
def test_invalid_explicit_ids_are_rejected(token):
    with pytest.raises(md.MarkdownError):md.parse('# T\n## Group\n- Node <!-- id: '+token+' -->')


def test_real_layout_id_alphabet_is_preserved():
    token='节点_123.a-b'
    result=md.parse('# T\n## Group\n- Node <!-- id: '+token+' -->')
    assert any(n['id']==token for n in result['nodes'])


@pytest.mark.parametrize('flow',['@missing -> @absent','@present -> @absent','@bad/id -> @present',
                                '@present ->','@ -> @present','@present -> -> @present'])
def test_unknown_or_invalid_id_endpoints_never_silently_disappear(flow):
    with pytest.raises(md.MarkdownError):md.parse('# T\n## Group\n- Node <!-- id: present -->\n'+flow)


def test_mixed_legacy_name_and_id_references_are_exact():
    result=md.parse('# T\n## Group <!-- id: group -->\n- Alpha <!-- id: a -->\n- Beta\n@a -> Beta\nGroup -> @a')
    assert {(e['from'],e['to']) for e in result['edges']}=={('a','Beta'),('group','a')}
    with pytest.raises(md.MarkdownError):
        md.parse('# T\n## Group\n- Same <!-- id: a -->\n- Same <!-- id: b -->\nSame -> Group')


def test_explicit_ids_are_reserved_before_auto_slug_generation():
    result=md.parse('# T\n## Group\n- Reserved\n- Other <!-- id: Reserved -->')
    assert len({n['id'] for n in result['nodes']})==len(result['nodes'])
    by_label={n['label']:n['id'] for n in result['nodes']}
    assert by_label['Other']=='Reserved' and by_label['Reserved']!='Reserved'


def test_id_dump_preserves_cycles_multi_parent_edges_and_group_tree_after_storage():
    text=('# T\n## Same <!-- id: g -->\n### Same <!-- id: sub -->\n'
          '- Same <!-- id: a --> <!-- bind: kb-real-source -->\n- Same <!-- id: b -->\n- Third <!-- id: c -->\n'
          '@a -> @b\n@b -> @a\n@a -> @c\n@b -> @c\n@g -> @sub')
    first=md.parse(text)
    # The existing backend and browser keep only these root keys.
    stored=_clean(first)
    assert 'reference_mode' not in stored
    dumped=md.dump(stored,'T')
    assert '@a -> @b' in dumped and '<!-- id: a -->' in dumped
    assert topology(md.parse(dumped))==topology(first)
    stored['nodes'].append({'id':'new-node','label':'Same','parent':'sub','binding':{'kind':'none'},'statics':[],'x':0,'y':0})
    assert '<!-- id: new-node -->' in md.dump(stored,'T')


def test_explicit_empty_groups_remain_real_reference_endpoints():
    first=md.parse('# T\n## Empty <!-- id: empty -->\n## Owner <!-- id: owner -->\n- Node <!-- id: n -->\n@empty -> @n')
    assert any(n['id']=='empty' for n in first['nodes'])
    assert topology(md.parse(md.dump(_clean(first),'T')))==topology(first)


def test_id_references_to_existing_auto_ids_enable_persistent_reference_mode():
    first=md.parse('# T\n## Group\n- Alpha\n- Beta\n@Alpha -> @Beta')
    assert first['reference_mode']=='id'
    assert topology(md.parse(md.dump(_clean(first),'T')))==topology(first)


@pytest.mark.parametrize('mutate',[
    lambda v:v['edges'].append({'from':'missing','to':'n'}),
    lambda v:v['nodes'][1].update(id='bad/id'),
    lambda v:v['nodes'][1].update(reference_id='other'),
    lambda v:v['nodes'][1].update(parent='missing'),
    lambda v:v['nodes'][0].update(parent='g'),
])
def test_id_dump_rejects_invalid_stored_identity_or_containment(mutate):
    first=md.parse('# T\n## Group <!-- id: g -->\n- Node <!-- id: n -->')
    mutate(first)
    with pytest.raises(md.MarkdownError):md.dump(first,'T')


def test_legacy_name_mode_and_dump_remain_unchanged():
    text='# T\n\n## Group\n- Alpha\n  - Beta\n'
    first=md.parse(text)
    assert 'reference_mode' not in first and all('reference_id' not in n for n in first['nodes'])
    assert md.dump(first,'T')==text
    assert topology(md.parse(md.dump(first,'T')))==topology(first)
    with pytest.raises(md.MarkdownError):md.parse('# T\n## Group\n- Alpha\nAlpha -> Missing')
    with pytest.raises(md.MarkdownError):md.parse('# T\n## Group\n- Same\n- Same\nSame -> Group')


@pytest.mark.parametrize('all_root', [False, True])
def test_id_mode_root_nodes_preserve_parent_after_actual_storage(all_root):
    first = md.parse('# T\n## Group <!-- id: g -->\n- Grouped <!-- id: a -->')
    if all_root:
        first['nodes'] = [n for n in first['nodes'] if n['id'] != 'g']
        first['nodes'][0]['parent'] = None
    first['nodes'].append({'id': 'root-node', 'label': 'Root', 'parent': None,
                           'binding': {'kind': 'none'}, 'statics': [], 'x': 0, 'y': 0})
    first['edges'] = [{'from': 'root-node', 'to': 'a'}, {'from': 'a', 'to': 'root-node'}]
    stored = _clean(first)
    assert 'reference_mode' not in stored
    exported = md.dump(stored, 'T')
    assert '<!-- parent: none -->' in exported
    restored = md.parse(exported)
    assert topology(restored) == topology(first)
    assert all(n['label'] != '未分组' for n in restored['nodes'])


def test_parent_none_marker_enables_persistent_id_mode_without_explicit_id():
    first = md.parse('# T\n- Root <!-- parent: none -->')
    assert len(first['nodes']) == 1 and first['nodes'][0]['parent'] is None
    assert first['reference_mode'] == 'id'
    assert first['nodes'][0]['reference_id'] == first['nodes'][0]['id']
    assert topology(md.parse(md.dump(_clean(first), 'T'))) == topology(first)


@pytest.mark.parametrize('text', [
    '# T\n## Group <!-- parent: none -->\n- Node',
    '# T\n- Node <!-- parent: other -->',
    '# T\n- Node <!-- parent: none --> <!-- parent: none -->',
    '# T\n- Node <!-- parent: none --> <!-- parent: none',
    '# T\n- Node <!-- parent: none',
    '# T\n- <!-- parent: none -->',
])
def test_invalid_root_parent_markers_are_rejected(text):
    with pytest.raises(md.MarkdownError):
        md.parse(text)


def test_explicit_root_nodes_do_not_change_following_group_membership():
    first = md.parse('# T\n- First Root <!-- id: r1 --> <!-- parent: none -->\n'
                     '## Group <!-- id: g -->\n- Inside <!-- id: a -->\n'
                     '- Another Root <!-- parent: none --> <!-- id: r2 -->\n'
                     '- Also Inside <!-- id: b -->\n@r1 -> @r2\n@r2 -> @a')
    assert {n['id']: n['parent'] for n in first['nodes']} == {
        'g': None, 'r1': None, 'r2': None, 'a': 'g', 'b': 'g'}
    assert topology(md.parse(md.dump(_clean(first), 'T'))) == topology(first)


@pytest.mark.parametrize('op', ['sum', 'share'])
def test_id_aggregate_duplicate_label_selection_preserves_exact_member(op):
    first = md.parse('# T\n## Group <!-- id: g -->\n- Same <!-- id: a -->\n- Same <!-- id: b -->')
    next(n for n in first['nodes'] if n['id'] == 'g')['binding'] = {
        'kind': 'agg', 'op': op, 'members': ['b']}
    exported = md.dump(_clean(first), 'T')
    assert '<!-- agg: ' + op + '(@b) -->' in exported
    restored = md.parse(exported)
    assert topology(restored) == topology(first)


@pytest.mark.parametrize('op', ['sum', 'share'])
def test_id_aggregate_all_direct_members_stays_implicit(op):
    first = md.parse('# T\n## Group <!-- id: g -->\n- Same <!-- id: a -->\n- Same <!-- id: b -->')
    next(n for n in first['nodes'] if n['id'] == 'g')['binding'] = {'kind': 'agg', 'op': op}
    exported = md.dump(_clean(first), 'T')
    assert '<!-- agg: ' + op + ' -->' in exported
    assert topology(md.parse(exported)) == topology(first)


@pytest.mark.parametrize('member', ['@missing', '@other', '@sub', '@g', '@bad/id', 'Same'])
def test_id_aggregate_unknown_outside_group_and_ambiguous_members_are_rejected(member):
    text = ('# T\n## Group <!-- id: g --> <!-- agg: sum(' + member + ') -->\n'
            '- Same <!-- id: a -->\n- Same <!-- id: b -->\n'
            '### Child <!-- id: sub -->\n- Outside <!-- id: other -->')
    with pytest.raises(md.MarkdownError):
        md.parse(text)


def test_id_aggregate_mixed_unique_name_and_id_members_resolve_within_group():
    first = md.parse('# T\n## Group <!-- id: g --> <!-- agg: sum(Unique, @b) -->\n'
                     '- Unique <!-- id: a -->\n- Same <!-- id: b -->\n'
                     '## Other <!-- id: h -->\n- Unique <!-- id: c -->')
    assert next(n for n in first['nodes'] if n['id'] == 'g')['binding']['members'] == ['a', 'b']
    assert topology(md.parse(md.dump(_clean(first), 'T'))) == topology(first)


def test_explicit_aggregate_reference_to_auto_id_enables_persistent_mode():
    first = md.parse('# T\n## Group <!-- agg: sum(@Second) -->\n- First\n- Second')
    assert first['reference_mode'] == 'id'
    assert next(n for n in first['nodes'] if n['id'] == 'Group')['binding']['members'] == ['Second']
    assert topology(md.parse(md.dump(_clean(first), 'T'))) == topology(first)


@pytest.mark.parametrize('member', ['missing', 'outside', 'g'])
def test_id_dump_rejects_invalid_stored_aggregate_member(member):
    first = md.parse('# T\n## Group <!-- id: g -->\n- Node <!-- id: n -->\n'
                     '## Other <!-- id: h -->\n- Outside <!-- id: outside -->')
    next(n for n in first['nodes'] if n['id'] == 'g')['binding'] = {
        'kind': 'agg', 'op': 'sum', 'members': [member]}
    with pytest.raises(md.MarkdownError):
        md.dump(first, 'T')


def test_legacy_aggregate_name_and_all_members_syntax_remain_unchanged():
    for annotation, binding in [
        ('sum(Beta)', {'kind': 'agg', 'op': 'sum', 'members': ['Beta']}),
        ('sum', {'kind': 'agg', 'op': 'sum'}),
        ('share', {'kind': 'agg', 'op': 'share'}),
    ]:
        text = '# T\n\n## Group <!-- agg: ' + annotation + ' -->\n- Alpha\n- Beta\n'
        first = md.parse(text)
        assert next(n for n in first['nodes'] if n['id'] == 'Group')['binding'] == binding
        assert 'reference_mode' not in first
        assert md.dump(first, 'T') == text
