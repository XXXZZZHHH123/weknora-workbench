"""A failed skeleton check is returned to the model once; checks are never relaxed."""
from copy import deepcopy
from datetime import datetime, timedelta

from fastapi import HTTPException
import pytest

from tests.test_diagram_consistency import bound_graph, request, stable_generator, structure  # noqa: F401
from tests.test_knowledge_facts import SERIES, canonical  # noqa: F401
from tin.web import diagram_generation as generation


def scripted(monkeypatch, state, outlines):
    """Outline replies are consumed in order; binding replies use the bound graph."""
    async def complete(scope, model_id, prompt, image=None):
        state['completions'].append({'prompt': prompt, 'image': image})
        if prompt.startswith('阶段：研究骨架提取'):
            reply = outlines.pop(0)
            if isinstance(reply, Exception):
                raise reply
            return deepcopy(reply)
        return deepcopy(state['graph'])

    monkeypatch.setattr(generation, 'completion', complete)


def unlabeled():
    document = structure()
    document['groups'][2]['role'] = None
    return document


def outline_prompts(state):
    return [call for call in state['completions'] if call['prompt'].startswith('阶段：研究骨架提取')]


def test_failed_skeleton_check_is_returned_once_with_the_specific_problem(stable_generator, monkeypatch):
    client, headers, state = stable_generator
    scripted(monkeypatch, state, [unlabeled(), structure()])
    image = 'data:image/png;base64,eA=='
    result = request(client, headers, image=image)
    assert result.status_code == 200, result.text
    assert result.json()['outline_repairs'] == 1
    first, repair = outline_prompts(state)
    assert '程序校验反馈' not in first['prompt']
    assert repair['prompt'].startswith(first['prompt'])
    assert '程序校验反馈' in repair['prompt'] and "['demand']" in repair['prompt']
    assert '\n上次输出：' in repair['prompt']
    assert first['image'] == repair['image'] == image
    assert len(state['completions']) == 3


def test_skeleton_repair_happens_once_and_a_failure_is_not_reused(stable_generator, monkeypatch):
    client, headers, state = stable_generator
    scripted(monkeypatch, state, [unlabeled(), unlabeled()])
    failed = request(client, headers)
    assert failed.status_code == 422, failed.text
    assert len(state['completions']) == 2 and len(outline_prompts(state)) == 2
    scripted(monkeypatch, state, [structure()])
    recovered = request(client, headers)
    assert recovered.status_code == 200, recovered.text
    assert recovered.json()['analysis_reused'] is False and recovered.json()['outline_repairs'] == 0


def test_invalid_json_skeleton_gets_the_same_single_repair(stable_generator, monkeypatch):
    client, headers, state = stable_generator
    scripted(monkeypatch, state, [HTTPException(422, generation.INVALID_OUTPUT_MESSAGE), structure()])
    result = request(client, headers)
    assert result.status_code == 200, result.text
    repair = outline_prompts(state)[1]['prompt']
    assert generation.INVALID_OUTPUT_MESSAGE in repair and '\n上次输出：' not in repair


@pytest.mark.parametrize('error', [HTTPException(503, '模型服务暂时不可用'),
                                   HTTPException(422, '生成配置无效，请核对资料与模型能力')])
def test_provider_or_configuration_failure_is_not_retried(stable_generator, monkeypatch, error):
    client, headers, state = stable_generator
    scripted(monkeypatch, state, [error, structure()])
    result = request(client, headers)
    assert result.status_code == error.status_code, result.text
    assert len(state['completions']) == 1


def test_binding_rejection_returns_the_specific_problem_to_the_model(stable_generator):
    client, headers, state = stable_generator
    state['graph']['nodes'][2]['label'] = 'Renamed concept'
    result = request(client, headers)
    assert result.status_code == 422
    repair = state['completions'][-1]['prompt']
    assert repair.startswith('阶段：覆盖修正') and "['demand_concept']" in repair
    state['graph'] = bound_graph()
    state['graph']['nodes'].pop()
    assert request(client, headers).status_code == 422
    assert "['demand']" in state['completions'][-1]['prompt']


def dates(start, step, count):
    return [datetime(2026, 1, 1) + timedelta(days=start + step * index) for index in range(count)]


@pytest.mark.parametrize('step,expected', [(1, '日'), (7, '周'), (30, '月'), (91, '季'), (182, '半年'), (365, '年')])
def test_frequency_is_inferred_from_observation_spacing(step, expected):
    assert generation.observed_frequency(dates(0, step, 6)) == expected


def test_too_few_observations_do_not_claim_a_frequency():
    assert generation.observed_frequency(dates(0, 30, 2)) is None


def test_binding_prompt_shows_candidate_recency_from_stored_observations(stable_generator):
    client, headers, state = stable_generator
    result = request(client, headers)
    assert result.status_code == 200, result.text
    binding = state['completions'][-1]['prompt']
    assert binding.startswith('阶段：绑定与有界细化')
    assert '"latest_date":"' in binding and '"months_behind":0' in binding
    assert SERIES not in binding and '"id":"c1"' in binding


def test_recency_is_omitted_rather_than_exceeding_prompt_capacity(monkeypatch):
    candidates = [{'id': 'synthetic-a', 'name': 'Synthetic quantity'}]
    status = {'synthetic-a': {'latest_date': '2026-07-31', 'observed_frequency': '月'}}
    plain = generation.catalog_prompt('protocol', candidates, 'text')
    assert '2026-07-31' in generation.annotated_prompt('protocol', candidates, status, 'text')
    monkeypatch.setattr(generation, 'MODEL_PROMPT_BYTES', len(plain.encode('utf-8')))
    assert generation.annotated_prompt('protocol', candidates, status, 'text') == plain


def test_local_history_status_reads_only_the_newest_date(monkeypatch):
    from types import SimpleNamespace
    from tin.models.facts import Observation

    executed = []

    class Session:
        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def execute(self, statement):
            executed.append(str(statement))
            return [('synthetic-a', datetime(2026, 7, 31))]

    monkeypatch.setattr(generation, 'SessionLocal', Session)
    status = generation.candidate_status(SimpleNamespace(scope_version=1), ['synthetic-a'])
    assert status == {'synthetic-a': {'latest_date': '2026-07-31', 'months_behind': 0}}
    assert 'max(' in executed[0].lower() and 'group by' in executed[0].lower()
    assert Observation.__tablename__ in executed[0]
