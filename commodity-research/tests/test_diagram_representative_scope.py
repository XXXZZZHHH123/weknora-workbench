"""Representative scope is shared without weakening frozen source coverage."""
from copy import deepcopy

from fastapi import HTTPException
import pytest

from tests.test_diagram_outline import outline
from tests.test_diagram_background_jobs import case, completed, run
from tin.export import diagram_md
from tin.web import diagram_generation as generation
from tin.web import diagram_outline as structure


def test_source_stage_can_cover_multiple_source_questions_with_one_representative_concept():
    raw = outline()
    raw['coverage'].append({'id': 'further_question', 'label': 'Another related source observation',
        'node_ids': ['c']})
    checked = structure.validate_outline(raw)
    assert len(checked['coverage']) == 4 and len(checked['nodes']) == 3
    assert checked['coverage'][-1]['node_ids'] == checked['coverage'][-2]['node_ids']
    assert all('series_id' not in item for item in checked['nodes'])


def test_all_stages_share_the_same_representative_scope_and_detail_gate():
    shared = structure.REPRESENTATIVE_SCOPE_PROTOCOL
    assert '真实目录' in shared and '统计范围' in shared
    assert '覆盖不足' in shared and '十分必要的独立研究差异' in shared
    assert '用户明确要求' in shared and '数量上限不作为' in shared
    protocols = [structure.outline_prompt(),
        generation.selection_protocol() + structure.selection_context(),
        generation.graph_protocol([], [], False) + structure.final_constraint()]
    assert all(shared in protocol for protocol in protocols)
    assert all(protocol.count(shared) == 1 for protocol in protocols)
    assert all('少量代表' not in protocol and '12–20' not in protocol for protocol in protocols)
    assert '不逐个复制' in protocols[0]
    assert '不能把局部指标或代理指标冒充整体' in shared
    assert '同口径' in generation.graph_protocol([], [], False)


def test_representative_guidance_does_not_allow_dropping_a_frozen_axis_or_changing_geometry_roles():
    raw = outline()
    graph = structure.outline_graph(raw)
    graph['nodes'][0]['series_id'] = 'authorized-real-id'
    checked = structure.validate_final(raw, graph)
    layout = diagram_md.parse(generation.compile_graph(checked))
    groups = {item['id']: item for item in layout['nodes'] if item.get('kind') == 'group'}
    assert {key: value['role'] for key, value in groups.items()} == {
        'up': 'supply', 'core': 'target', 'down': 'demand'}
    assert groups['up']['x'] < groups['core']['x'] < groups['down']['x']
    changed = deepcopy(graph)
    changed['nodes'].pop()
    with pytest.raises(HTTPException) as missing:
        structure.validate_final(raw, changed)
    assert missing.value.status_code == 422
    changed = deepcopy(graph)
    changed['groups'][0]['role'] = 'reference'
    with pytest.raises(HTTPException):
        structure.validate_final(raw, changed)


def test_prompt_change_invalidates_reuse_without_adding_a_model_stage(case, monkeypatch):
    async def scenario():
        prompts = []
        original = generation.completion

        async def capture(scope, model_id, prompt, image=None):
            prompts.append(prompt)
            return await original(scope, model_id, prompt, image)

        monkeypatch.setattr(generation, 'completion', capture)
        assert generation.PROMPT_VERSION == 'diagram-outline-v2'
        _, first = await completed(case, key='synthetic-representative-first')
        assert first['analysis_reused'] is False and len(prompts) == 2
        assert all(structure.REPRESENTATIVE_SCOPE_PROTOCOL in prompt for prompt in prompts)
        _, reused = await completed(case, key='synthetic-representative-repeat')
        assert reused['analysis_reused'] is True and len(prompts) == 2
        monkeypatch.setattr(structure, 'REPRESENTATIVE_SCOPE_PROTOCOL',
            structure.REPRESENTATIVE_SCOPE_PROTOCOL + ' Synthetic protocol revision.')
        _, changed = await completed(case, key='synthetic-representative-revision')
        assert changed['analysis_reused'] is False and len(prompts) == 4
        assert [prompt.split('\n', 1)[0] for prompt in prompts] == [
            '阶段：研究骨架提取', '阶段：绑定与有界细化',
            '阶段：研究骨架提取', '阶段：绑定与有界细化']
    run(case, scenario)
