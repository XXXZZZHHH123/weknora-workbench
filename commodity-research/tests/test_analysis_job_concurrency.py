"""Synthetic blocked model calls prove detached tasks actually run concurrently."""
import asyncio

from fastapi import HTTPException
import pytest
from sqlalchemy import func, select

from tests.test_diagram_background_jobs import (
    case, active, run, OTHER_WB as DIAGRAM_OTHER_WB,
)
from tests.test_judgment_analysis_jobs_isolation import (
    job_case, run as run_judgment, SOURCE, OTHER_WB as JUDGMENT_OTHER_WB,
)
from tin.integration.identity import current_identity
from tin.integration.workbench_scope import artifact_session, current_workbench
from tin.models import DiagramTemplate, Judgment, SourceMapping
from tin.web import diagram_generation, judgment_generation, judgment_jobs
from tin.judgments.analysis import assess_draft
from tin.web.judgment_review import initialize_review


def test_duplicate_diagram_input_is_explained_without_blocking_or_cancelling_original(case):
    async def scenario():
        case.state.update(block=asyncio.Event(), entered=asyncio.Event())
        body = diagram_generation.GenerateRequest(model_id='synthetic-vision',
            instructions='Synthetic exact duplicate input')
        with active(case.first):
            first = await case.jobs.start_job(case.first, body, 'synthetic-duplicate-first')
            await asyncio.wait_for(case.state['entered'].wait(), 5)
            first_task = case.jobs.runtime.tasks[first['id']]
            duplicate = await case.jobs.start_job(case.first, body, 'synthetic-duplicate-second')
            await asyncio.wait_for(case.jobs.runtime.tasks[duplicate['id']], 5)
            rejected = await case.jobs.job_status(case.first, duplicate['id'])
            assert rejected['state'] == 'failed'
            assert rejected['message'] == '相同资料正在生成产业图，请等待完成'
            assert not first_task.done()
            assert (await case.jobs.job_status(case.first, first['id']))['state'] == 'running'
            case.state['block'].set()
            await asyncio.wait_for(first_task, 5)
            assert (await case.jobs.job_status(case.first, first['id']))['state'] == 'succeeded'
            assert (await case.jobs.latest_job(case.first))['job']['id'] == first['id']
        assert len(case.state['calls']) == 2
    run(case, scenario)


def test_same_account_diagrams_run_together_and_reverse_completion_keeps_latest_submission(case, monkeypatch):
    async def scenario():
        entered = [asyncio.Event(), asyncio.Event()]
        release = [asyncio.Event(), asyncio.Event()]
        starts = []
        original = diagram_generation.completion

        async def blocked(scope, model_id, prompt, image=None):
            if prompt.startswith('阶段：研究骨架提取\n'):
                index = len(starts)
                assert index < 2
                starts.append((current_identity.get(), current_workbench.get()))
                entered[index].set()
                await release[index].wait()
            return await original(scope, model_id, prompt, image)

        monkeypatch.setattr(diagram_generation, 'completion', blocked)
        rows = []
        for index in range(2):
            body = diagram_generation.GenerateRequest(model_id='synthetic-vision',
                instructions=f'Synthetic independent structure {index}')
            with active(case.first):
                rows.append(await case.jobs.start_job(case.first, body,
                    f'synthetic-concurrent-diagram-{index}'))
            await asyncio.wait_for(entered[index].wait(), 5)
        older, newer = rows
        tasks = {row['id']: case.jobs.runtime.tasks[row['id']] for row in rows}
        assert len({row['id'] for row in rows}) == 2
        assert starts == [(case.first.identity, case.first)] * 2
        assert all(not task.done() for task in tasks.values())
        with active(case.first):
            statuses = [await case.jobs.job_status(case.first, row['id']) for row in rows]
            assert all(status['state'] == 'running' for status in statuses)
            with artifact_session(case.first) as session:
                stored = [session.get(SourceMapping, case.jobs.job_key(case.first, row['id'])).config
                    for row in rows]
                assert [row['request']['instructions'] for row in stored] == [
                    'Synthetic independent structure 0', 'Synthetic independent structure 1']
                assert all(row['result'] is None for row in stored)
        release[1].set()
        await asyncio.wait_for(tasks[newer['id']], 5)
        with active(case.first):
            latest = await case.jobs.job_result(case.first, newer['id'])
            assert latest['request']['instructions'] == 'Synthetic independent structure 1'
            assert (await case.jobs.job_status(case.first, older['id']))['state'] == 'running'
        release[0].set()
        await asyncio.wait_for(tasks[older['id']], 5)
        with active(case.first):
            old = await case.jobs.job_status(case.first, older['id'])
            assert old['state'] == 'superseded' and not old['result_available']
            assert old['superseded_by'] == newer['id']
            assert (await case.jobs.latest_job(case.first))['job']['id'] == newer['id']
            assert (await case.jobs.job_result(case.first, newer['id']))['markdown'] == latest['markdown']
            with pytest.raises(HTTPException) as gone:
                await case.jobs.job_result(case.first, older['id'])
            assert gone.value.status_code == 410
            notices = {row['id']: row for row in judgment_jobs.list_jobs(case.first.identity)['items']}
            assert notices[older['id']]['dismissed'] is True
            assert notices[newer['id']]['dismissed'] is False
            with artifact_session(case.first) as session:
                assert session.scalar(select(func.count()).select_from(DiagramTemplate)) == 0
                old_record = session.get(SourceMapping, case.jobs.job_key(case.first, older['id']))
                assert old_record.config['state'] == 'superseded' and old_record.config['result'] is None
                new_record = session.get(SourceMapping, case.jobs.job_key(case.first, newer['id']))
                assert new_record.config['result']['markdown'] == latest['markdown']
        assert len(case.state['calls']) == 4
    run(case, scenario)


def test_cancelling_one_workbench_does_not_cancel_the_same_accounts_other_diagram(case, monkeypatch):
    async def scenario():
        scopes = [case.first, case.scope(workbench=DIAGRAM_OTHER_WB)]
        entered = {scope.workbench_id: asyncio.Event() for scope in scopes}
        release = {scope.workbench_id: asyncio.Event() for scope in scopes}
        original = diagram_generation.completion

        async def blocked(scope, model_id, prompt, image=None):
            assert current_identity.get() == scope.identity and current_workbench.get() == scope
            if prompt.startswith('阶段：研究骨架提取\n'):
                entered[scope.workbench_id].set()
                await release[scope.workbench_id].wait()
            return await original(scope, model_id, prompt, image)

        monkeypatch.setattr(diagram_generation, 'completion', blocked)
        rows = []
        for index, scope in enumerate(scopes):
            with active(scope):
                rows.append(await case.jobs.start_job(scope,
                    diagram_generation.GenerateRequest(model_id='synthetic-vision',
                        instructions='Synthetic common input in separate workbenches'),
                    f'synthetic-workbench-diagram-{index}'))
            await asyncio.wait_for(entered[scope.workbench_id].wait(), 5)
        first_task, second_task = [case.jobs.runtime.tasks[row['id']] for row in rows]
        assert not first_task.done() and not second_task.done()
        first_task.cancel()
        await asyncio.wait_for(first_task, 5)
        with active(scopes[0]):
            assert (await case.jobs.job_status(scopes[0], rows[0]['id']))['state'] == 'interrupted'
        with active(scopes[1]):
            assert (await case.jobs.job_status(scopes[1], rows[1]['id']))['state'] == 'running'
        assert not second_task.done()
        release[scopes[1].workbench_id].set()
        await asyncio.wait_for(second_task, 5)
        with active(scopes[1]):
            assert (await case.jobs.job_status(scopes[1], rows[1]['id']))['state'] == 'succeeded'
            assert (await case.jobs.job_result(scopes[1], rows[1]['id']))['nodes'] == 1
            assert (await case.jobs.latest_job(scopes[1]))['job']['id'] == rows[1]['id']
        with active(scopes[0]):
            assert (await case.jobs.latest_job(scopes[0]))['job'] is None
        notices = judgment_jobs.list_jobs(case.first.identity)['items']
        assert {row['workbench_id'] for row in notices} == {scope.workbench_id for scope in scopes}
        assert {row['id']: row['state'] for row in notices} == {
            rows[0]['id']: 'interrupted', rows[1]['id']: 'succeeded'}
        for scope in scopes:
            with active(scope), artifact_session(scope) as session:
                assert session.scalar(select(func.count()).select_from(DiagramTemplate)) == 0
    run(case, scenario)


def test_same_account_judgment_and_diagram_use_both_running_slots_without_replacing_each_other(case, monkeypatch):
    async def scenario():
        diagram_entered, judgment_entered = asyncio.Event(), asyncio.Event()
        diagram_release, judgment_release = asyncio.Event(), asyncio.Event()
        original = diagram_generation.completion
        monkeypatch.setattr(judgment_jobs, 'runtime', judgment_jobs.Runtime())

        async def blocked_diagram(scope, model_id, prompt, image=None):
            if prompt.startswith('阶段：研究骨架提取\n'):
                diagram_entered.set()
                await diagram_release.wait()
            return await original(scope, model_id, prompt, image)

        async def blocked_judgment(scope, body, progress=None):
            assert current_identity.get() == scope.identity and current_workbench.get() == scope
            judgment_entered.set()
            await judgment_release.wait()
            field = case.catalog[0]['id']
            assessment = assess_draft({'judgment': {'thresholds': [{
                'id': 'boundary', 'name': 'Synthetic stock boundary', 'series_id': field,
                'operator': '>', 'value': 12, 'type': '观察位', 'horizon': '短期'}]},
                'binding_units': {field: 'kg'},
                'rule_quotes': {'boundary': 'Synthetic stock above 12 kg'}, 'unresolved': []},
                body.text, [{'id': field, 'name': 'Synthetic stock', 'unit': 'kg'}],
                variety='XY', actor=scope.identity.user_id)
            preview = {'variety': 'XY', 'text': body.text, 'written_at': '2030-01-02',
                'fact_version': case.state['version'], 'base_version': 0,
                'source_name': 'Synthetic source', 'panel_title': body.panel_title,
                'model_id': body.model_id, 'validation_issues': [], 'repair_attempts': 0}
            initialize_review(preview, assessment, [{'id': field, 'name': 'Synthetic stock', 'unit': 'kg'}])
            return preview, None

        monkeypatch.setattr(diagram_generation, 'completion', blocked_diagram)
        monkeypatch.setattr(judgment_generation, '_analyze', blocked_judgment)
        try:
            with active(case.first):
                diagram = await case.jobs.start_job(case.first,
                    diagram_generation.GenerateRequest(model_id='synthetic-vision',
                        instructions='Synthetic independent diagram input'), 'synthetic-cross-purpose-diagram')
                await asyncio.wait_for(diagram_entered.wait(), 5)
                judgment = await judgment_jobs.start_job(case.first,
                    judgment_generation.GenerateRequest(model_id='synthetic-model', text=SOURCE,
                        variety='XY', panel_title='Synthetic independent judgment', base_version=0),
                    'synthetic-cross-purpose-judgment')
                await asyncio.wait_for(judgment_entered.wait(), 5)
                assert (await case.jobs.job_status(case.first, diagram['id']))['state'] == 'running'
                assert (await judgment_jobs.job_status(case.first, judgment['id']))['state'] == 'running'
            diagram_task = case.jobs.runtime.tasks[diagram['id']]
            judgment_task = judgment_jobs.runtime.tasks[judgment['id']]
            assert not diagram_task.done() and not judgment_task.done()
            judgment_release.set()
            await asyncio.wait_for(judgment_task, 5)
            with active(case.first):
                assert (await judgment_jobs.job_status(case.first, judgment['id']))['state'] == 'succeeded'
                assert (await case.jobs.job_status(case.first, diagram['id']))['state'] == 'running'
            diagram_release.set()
            await asyncio.wait_for(diagram_task, 5)
            with active(case.first):
                assert (await case.jobs.latest_job(case.first))['job']['id'] == diagram['id']
                assert (await judgment_jobs.latest_job(case.first))['job']['id'] == judgment['id']
                assert (await case.jobs.job_result(case.first, diagram['id']))['nodes'] == 1
                with artifact_session(case.first) as session:
                    row = session.get(SourceMapping, judgment_jobs.job_key(case.first, judgment['id']))
                    token = row.config['draft_id']
                    assert session.scalar(select(func.count()).select_from(DiagramTemplate)) == 0
                    assert session.scalar(select(func.count()).select_from(Judgment)) == 0
                assert judgment_jobs.load_preview(case.first, token)['panel_title'] == 'Synthetic independent judgment'
                notices = judgment_jobs.list_jobs(case.first.identity)['items']
                assert {row['purpose'] for row in notices} == {'diagram', 'judgments'}
                assert all(row['state'] == 'succeeded' and not row['dismissed'] for row in notices)
        finally:
            await judgment_jobs.runtime.shutdown()
    run(case, scenario)


@pytest.mark.parametrize('job_case', ['isolated'], indirect=True)
def test_same_account_judgments_in_different_workbenches_keep_independent_previews(job_case, monkeypatch):
    async def scenario():
        scopes = [job_case.first, job_case.scope(workbench=JUDGMENT_OTHER_WB)]
        entered = [asyncio.Event(), asyncio.Event()]
        release = [asyncio.Event(), asyncio.Event()]
        original = judgment_generation._analyze

        async def blocked(scope, body, progress=None):
            index = int(body.panel_title[-1])
            assert current_identity.get() == scope.identity and current_workbench.get() == scope
            entered[index].set()
            await release[index].wait()
            return await original(scope, body, progress)

        monkeypatch.setattr(judgment_generation, '_analyze', blocked)
        rows = []
        for index, scope in enumerate(scopes):
            with active(scope):
                rows.append(await job_case.jobs.start_job(scope,
                    judgment_generation.GenerateRequest(model_id='synthetic-model', text=SOURCE,
                        variety='XY', panel_title=f'Synthetic panel {index}', base_version=0),
                    f'synthetic-concurrent-judgment-{index}'))
            await asyncio.wait_for(entered[index].wait(), 5)
        tasks = [job_case.jobs.runtime.tasks[row['id']] for row in rows]
        assert all(not task.done() for task in tasks)
        for index in (1, 0):
            release[index].set()
            await asyncio.wait_for(tasks[index], 5)
        tokens = []
        for index, scope in enumerate(scopes):
            with active(scope):
                assert (await job_case.jobs.job_status(scope, rows[index]['id']))['state'] == 'succeeded'
                result = await job_case.jobs.job_result(scope, rows[index]['id'])
                assert result['panel_title'] == f'Synthetic panel {index}'
                assert (await job_case.jobs.latest_job(scope))['job']['id'] == rows[index]['id']
                tokens.append(result['draft_id'])
                with artifact_session(scope) as session:
                    assert session.scalar(select(func.count()).select_from(Judgment)) == 0
        assert tokens[0] != tokens[1]
        notices = job_case.jobs.list_jobs(scopes[0].identity)['items']
        assert len(notices) == 2 and all(row['state'] == 'succeeded' for row in notices)
        assert all(row['dismissed'] is False for row in notices)
        assert len(job_case.state['calls']) == 2
    run_judgment(job_case, scenario)
