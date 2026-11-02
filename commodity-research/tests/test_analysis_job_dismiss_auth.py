"""Dismissing a personal notice requires authentication and same-origin, not source access."""
from types import SimpleNamespace

import pytest

from tests.test_knowledge_facts import canonical, FIRST
from tin.web import judgment_jobs as jobs
from tin.web.judgment_job_registry import Registry


@pytest.fixture
def notice(canonical, monkeypatch):
    client, headers, state, root = canonical
    index = Registry(root / 'dismiss-index.sqlite')
    monkeypatch.setattr(jobs, 'registry', index)

    def create(user='researcher', status='failed'):
        scope = SimpleNamespace(identity=SimpleNamespace(user_id=user, tenant_id=17),
            workbench_id=FIRST, metadata={})
        row, _ = index.create(scope, user + status, user + status, 'synthetic-worker')
        if status != 'queued':
            index.finish(row['id'], row['worker_id'], row['fence'], status)
        return '/research/api/research/analysis-jobs/' + row['id'] + '/dismiss'

    return client, headers, state, index, create


def test_bearer_dismiss_is_owner_only_and_does_not_read_revoked_workbench(notice):
    client, headers, state, _, create = notice
    path = create()
    state.update(revoked=True, workbench_revoked=True)
    state['calls'].clear()
    result = client.post(path, headers=headers)
    assert result.status_code == 200, result.text
    assert result.json()['dismissed'] is True
    assert all('/workspaces/research/' not in endpoint for _, endpoint in state['calls'])
    first = result.json()['dismissed_at']
    assert client.post(path, headers=headers).json()['dismissed_at'] == first
    other = {**headers, 'Authorization': 'Bearer source-viewer'}
    assert client.post(path, headers=other).status_code == 404


@pytest.mark.parametrize('origin', [None, 'https://different.invalid'])
def test_bearer_dismiss_rejects_missing_or_cross_origin_requests(notice, origin):
    client, headers, _, _, create = notice
    path = create()
    headers = {key: value for key, value in headers.items() if key != 'Origin'}
    if origin:
        headers['Origin'] = origin
    assert client.post(path, headers=headers).status_code == 403


def test_viewer_can_ignore_own_terminal_cookie_notice_without_resource_edit_rights(notice):
    client, headers, state, _, create = notice
    path = create(user='viewer')
    viewer = {**headers, 'Authorization': 'Bearer source-viewer'}
    assert client.post('/research/auth/session', headers=viewer).status_code == 200
    state.update(revoked=True, workbench_revoked=True)
    cookie_headers = {'Origin': 'http://testserver', 'X-Tenant-ID': '17', 'X-Research-User': 'viewer'}
    result = client.post(path, headers=cookie_headers)
    assert result.status_code == 200, result.text
    assert result.json()['dismissed'] is True
    assert client.post(path, headers={**cookie_headers, 'X-Research-User': 'researcher'}).status_code == 403
    assert client.post(path, headers={**cookie_headers, 'X-Tenant-ID': '18'}).status_code == 403
    assert client.post(path, headers={**cookie_headers, 'Origin': 'https://different.invalid'}).status_code == 403


def test_active_dismiss_and_broader_unscoped_routes_remain_closed(notice):
    client, headers, _, _, create = notice
    path = create(status='queued')
    assert client.post(path, headers=headers).status_code == 409
    assert client.get(path, headers=headers).status_code == 401
    assert client.post(path + '/extra', headers=headers).status_code == 401
    assert client.post(path.replace('/dismiss', '/result'), headers=headers).status_code == 401
    assert client.post(path.replace('/dismiss', 'x/dismiss'), headers=headers).status_code == 401
