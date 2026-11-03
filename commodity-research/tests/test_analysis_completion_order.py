"""Overlapping accepted tasks retain the latest submitted successful result."""
from copy import deepcopy
import secrets

import pytest

from tin.integration.workbench_scope import artifact_session
from tin.models import SourceMapping
from tin.web import judgment_analysis_cache, diagram_analysis_cache
from tin.web.judgment_job_registry import LEASE

from tests.test_judgment_analysis_jobs_isolation import job_case, active, SOURCE
from tests.test_diagram_background_jobs import case, body as diagram_body


def overlapping_records(jobs, scope, body, purpose):
    """Seed two admitted jobs; admission policy is tested independently."""
    index = jobs.registry
    first, created = index.create(scope, 'first-request', 'first-key', 'worker', purpose)
    assert created
    second = {**deepcopy(first), 'id': secrets.token_hex(16),
        'request_hash': 'second-request', 'idempotency_hash': 'second-key',
        'created_order': first['created_order'] + 1, 'fence': secrets.token_hex(16),
        'lease_until': first['lease_until'] + LEASE}
    with index.transaction() as connection:
        index.write(connection, second)
    for meta in (first, second):
        jobs._create_private(scope, body, meta)
    return first, second


@pytest.mark.parametrize('job_case', ['legacy', 'isolated'], indirect=True)
@pytest.mark.parametrize('reverse', [False, True], ids=['submission-order', 'newest-finishes-first'])
def test_judgment_completion_order_keeps_latest_success_and_its_snapshot(job_case, monkeypatch, reverse):
    jobs, scope = job_case.jobs, job_case.first
    from tin.web.judgment_generation import GenerateRequest
    body = GenerateRequest(model_id='synthetic-model', text=SOURCE, base_version=0, variety='XY')
    delivered = []
    monkeypatch.setattr(judgment_analysis_cache, 'prune_snapshots_after_success',
        lambda current, key: delivered.append(key))
    with active(scope):
        first, second = overlapping_records(jobs, scope, body, 'judgments')
        values = {first['id']: {'review': {'revision': 1}, 'text': 'Older synthetic result',
                               'analysis_snapshot': 'older-snapshot'},
                  second['id']: {'review': {'revision': 1}, 'text': 'Latest synthetic result',
                                'analysis_snapshot': 'latest-snapshot'}}
        for meta in ((second, first) if reverse else (first, second)):
            jobs._complete(scope, meta, values[meta['id']])
        assert jobs.registry.get(scope.identity, first['id'])['state'] == 'superseded'
        assert jobs.registry.get(scope.identity, second['id'])['state'] == 'succeeded'
        assert jobs.registry.latest(scope.identity, scope.workbench_id, 'judgments')['id'] == second['id']
        assert delivered == (['latest-snapshot'] if reverse else ['older-snapshot', 'latest-snapshot'])
        with artifact_session(scope) as session:
            older = session.get(SourceMapping, jobs.job_key(scope, first['id']))
            newest = session.get(SourceMapping, jobs.job_key(scope, second['id']))
            assert older.config['state'] == 'superseded'
            assert older.config.get('input') == {} and older.config.get('preview') is None
            assert older.version == (2 if reverse else 3)
            assert newest.config['preview']['text'] == 'Latest synthetic result'
            assert newest.version == 2
            token = newest.config['draft_id']
        assert jobs.load_preview(scope, token)['text'] == 'Latest synthetic result'


@pytest.mark.parametrize('reverse', [False, True], ids=['submission-order', 'newest-finishes-first'])
def test_diagram_completion_order_keeps_latest_success_and_its_snapshot(case, monkeypatch, reverse):
    jobs, scope = case.jobs, case.first
    delivered = []
    monkeypatch.setattr(diagram_analysis_cache, 'mark_delivered',
        lambda current, key: delivered.append(key))
    with active(scope):
        first, second = overlapping_records(jobs, scope, diagram_body(), 'diagram')
        values = {first['id']: {'markdown': 'Older synthetic graph', 'analysis_snapshot': 'older-snapshot'},
                  second['id']: {'markdown': 'Latest synthetic graph', 'analysis_snapshot': 'latest-snapshot'}}
        for meta in ((second, first) if reverse else (first, second)):
            jobs._complete(scope, meta, values[meta['id']], None)
        assert jobs.registry.get(scope.identity, first['id'])['state'] == 'superseded'
        assert jobs.registry.get(scope.identity, second['id'])['state'] == 'succeeded'
        assert jobs.registry.latest(scope.identity, scope.workbench_id, 'diagram')['id'] == second['id']
        assert delivered == (['latest-snapshot'] if reverse else ['older-snapshot', 'latest-snapshot'])
        with artifact_session(scope) as session:
            older = session.get(SourceMapping, jobs.job_key(scope, first['id']))
            newest = jobs.owned_record(session, scope, second['id'])
            assert older.config['state'] == 'superseded'
            assert older.config.get('request') == {} and older.config.get('result') is None
            assert older.version == (2 if reverse else 3)
            assert newest.config['result']['markdown'] == 'Latest synthetic graph'
            assert newest.version == 2
