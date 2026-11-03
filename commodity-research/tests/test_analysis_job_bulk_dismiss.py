"""Ignore terminal notices atomically; never cancel work or delete a preview."""
from types import SimpleNamespace

import pytest

from tests.test_knowledge_facts import canonical, FIRST
from tests.test_judgment_analysis_jobs_isolation import job_case, completed, run, active
from tin.web import judgment_jobs as jobs
from tin.web.judgment_job_registry import Registry


PATH = '/research/api/research/analysis-jobs/dismiss-all'


def scope(user='synthetic-owner', tenant=71, board='synthetic-board'):
    return SimpleNamespace(identity=SimpleNamespace(user_id=user, tenant_id=tenant),
        workbench_id=board, metadata={})


def create(index, owner, key, state='failed', purpose='judgments'):
    row, _ = index.create(owner, key, key, 'worker-' + key, purpose)
    if state == 'running':
        assert index.claim(row['id'], row['worker_id'], row['fence'])
    elif state != 'queued':
        index.finish(row['id'], row['worker_id'], row['fence'], state)
    return index.get(owner.identity, row['id'])


def test_bulk_dismiss_covers_unlisted_terminal_notices_and_is_idempotent_owner_scoped(tmp_path):
    index, owner = Registry(tmp_path / 'index.sqlite'), scope()
    terminal = [create(index, scope(board='board-' + str(number)), 'finished-' + str(number),
        ('succeeded', 'failed', 'interrupted')[number % 3], 'diagram' if number % 2 else 'judgments')
        for number in range(24)]
    other_owner = scope(user='other-owner')
    other_tenant = scope(tenant=72)
    foreign = [(other_owner, create(index, other_owner, 'foreign-owner')),
               (other_tenant, create(index, other_tenant, 'foreign-tenant'))]
    running = create(index, owner, 'running', 'running')
    queued = create(index, owner, 'queued', 'queued', 'diagram')
    before = {row['id']: row for _, row in foreign}
    ignored = index.dismiss_all(owner.identity)
    assert len(ignored) == 24 and set(ignored) == {row['id'] for row in terminal}
    assert len(index.listing(owner.identity)) == 20
    assert set(Registry(index.path).dismiss_all(owner.identity)) == set(ignored)
    for row in terminal:
        current = index.get(owner.identity, row['id'])
        assert current['dismissed'] is True and current['state'] == row['state']
        assert current['finished_at'] == row['finished_at']
    assert index.get(owner.identity, running['id']) == running
    assert index.get(owner.identity, queued['id']) == queued
    assert all(index.get(other.identity, row['id']) == before[row['id']] for other, row in foreign)
    later = create(index, scope(board='new-board'), 'finished-after-request')
    assert later['id'] not in ignored
    assert not index.get(owner.identity, later['id']).get('dismissed')
    assert later['id'] in index.dismiss_all(owner.identity)


@pytest.fixture
def bulk_notice(canonical, monkeypatch):
    client, headers, state, root = canonical
    index = Registry(root / 'bulk-index.sqlite')
    monkeypatch.setattr(jobs, 'registry', index)
    owner = scope(user='researcher', tenant=17, board=FIRST)
    finished = create(index, owner, 'synthetic-finished')
    running = create(index, owner, 'synthetic-running', 'running')
    return client, headers, state, index, owner, finished, running


def test_bulk_endpoint_requires_no_source_access_and_leaves_running_work(bulk_notice):
    client, headers, state, index, owner, finished, running = bulk_notice
    state.update(revoked=True, workbench_revoked=True)
    state['calls'].clear()
    result = client.post(PATH, headers=headers)
    assert result.status_code == 200, result.text
    assert result.json() == {'dismissed_ids': [finished['id']]}
    assert client.post(PATH, headers=headers).json() == result.json()
    assert index.get(owner.identity, running['id']) == running
    assert all('/workspaces/research/' not in endpoint for _, endpoint in state['calls'])


@pytest.mark.parametrize('origin', [None, 'https://different.invalid'])
def test_bulk_endpoint_rejects_missing_or_cross_origin(bulk_notice, origin):
    client, headers, _, index, owner, finished, _ = bulk_notice
    headers = {key: value for key, value in headers.items() if key != 'Origin'}
    if origin:
        headers['Origin'] = origin
    assert client.post(PATH, headers=headers).status_code == 403
    assert not index.get(owner.identity, finished['id']).get('dismissed')


def test_bulk_endpoint_never_accepts_user_or_task_selectors(bulk_notice):
    client, headers, _, index, owner, finished, _ = bulk_notice
    assert client.post(PATH + '?user_id=other', headers=headers).status_code == 422
    assert client.post(PATH, headers=headers, json={'ids': [finished['id']]}).status_code == 422
    assert client.get(PATH, headers=headers).status_code == 401
    assert client.post(PATH + '/extra', headers=headers).status_code == 401
    assert not index.get(owner.identity, finished['id']).get('dismissed')


def test_viewer_cookie_can_ignore_only_its_own_terminal_notices(bulk_notice):
    client, headers, state, index, owner, finished, _ = bulk_notice
    viewer = scope(user='viewer', tenant=17, board=FIRST)
    own = create(index, viewer, 'viewer-finished')
    assert client.post('/research/auth/session', headers={**headers,
        'Authorization': 'Bearer source-viewer'}).status_code == 200
    state.update(revoked=True, workbench_revoked=True)
    cookie = {'Origin': 'http://testserver', 'X-Tenant-ID': '17', 'X-Research-User': 'viewer'}
    result = client.post(PATH, headers=cookie)
    assert result.status_code == 200, result.text
    assert result.json()['dismissed_ids'] == [own['id']]
    assert not index.get(owner.identity, finished['id']).get('dismissed')
    assert client.post(PATH, headers={**cookie, 'X-Research-User': 'researcher'}).status_code == 403
    assert client.post(PATH, headers={**cookie, 'X-Tenant-ID': '18'}).status_code == 403


@pytest.mark.parametrize('job_case', ['legacy', 'isolated'], indirect=True)
def test_ignoring_all_retains_the_completed_private_result(job_case):
    async def scenario():
        identifier, result = await completed(job_case)
        before_calls = len(job_case.state['calls'])
        assert job_case.jobs.registry.dismiss_all(job_case.first.identity) == [identifier]
        with active(job_case.first):
            restored = await job_case.jobs.job_result(job_case.first, identifier)
        assert restored['draft_id'] == result['draft_id']
        assert restored['source_text'] == result['source_text']
        assert len(job_case.state['calls']) == before_calls
    run(job_case, scenario)
