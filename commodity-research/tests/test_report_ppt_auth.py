"""The real authentication middleware permits reader downloads and enforces CSRF."""
from copy import deepcopy
import json

from fastapi import FastAPI, HTTPException
from fastapi.testclient import TestClient
import pytest

from tin.config import settings
from tin.integration import auth, workbench_scope
from tin.integration.identity import Identity
from tin.integration.workbench_paths import (
    allowed_workbench_route, parse_workbench_path, source_preparation_required,
)
from tin.web import report_ppt
from tin.web.report_view import present_snapshot


WB = '67175bd2-32c2-42df-8ba6-d8c2e2f1fe67'
DAY = '2023-01-03'
SUFFIX = f'/api/sn/reports/{DAY}/weekly/ppt'
URL = f'/research/workbenches/{WB}{SUFFIX}'


@pytest.fixture
def auth_ppt_client(monkeypatch):
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://native.fixture')
    monkeypatch.setattr(settings, 'auth_cache_seconds', 0)
    auth._sessions.clear()
    identity = Identity('reader', 'reader@example.test', 'Reader', 17, 'viewer')
    state = {'restricted': False, 'view_calls': 0}

    async def verify(token, tenant=None):
        if token != 'reader' or tenant is not None and str(tenant) != '17':
            raise HTTPException(403, '凭据不匹配')
        return identity

    async def resolve(actual, identifier, authorization):
        assert actual == identity and identifier == WB and authorization == 'Bearer reader'
        return workbench_scope.WorkbenchScope(identity, WB, 'fixture-kb', 17,
            {'name': '只读权限测试台', 'can_edit': False,
             'can_read_source': not state['restricted']}, authorization)

    def view(*args, **kwargs):
        state['view_calls'] += 1
        result = present_snapshot({'observations': [], 'as_of': DAY,
            'period': {'kind': 'weekly', 'start': '2023-01-02', 'end': DAY}})
        result.update(workbench_name='只读权限测试台', report_title='只读账号周报',
                      as_of=DAY, generated_at='2023-01-03T16:00:00+08:00', frozen=False)
        return deepcopy(result)

    monkeypatch.setattr(auth, 'verify_bearer', verify)
    monkeypatch.setattr(workbench_scope, 'resolve_workbench', resolve)
    monkeypatch.setattr(report_ppt, 'read_report_view', view)
    app = FastAPI()
    app.add_middleware(auth.ResearchAuthMiddleware)
    app.include_router(report_ppt.router)
    return TestClient(app), state


def request(client, *, origin=True, bearer=True):
    headers = {'Origin': 'http://testserver'} if origin else {}
    if bearer:
        headers.update(Authorization='Bearer reader', **{'X-Tenant-ID': '17'})
    return client.post(URL, headers=headers, files=[
        ('options', (None, json.dumps({'sections': dict.fromkeys(
            ('summary', 'research', 'dashboard', 'watch'), False), 'figures_per_slide': 2}))),
        ('revision', (None, 'null')), ('keys', (None, '[]')),
    ])


@pytest.mark.parametrize('bearer', [True, False])
def test_real_auth_reader_download_and_origin_checks(auth_ppt_client, bearer):
    client, state = auth_ppt_client
    if not bearer:
        login = client.post('/research/auth/session', headers={
            'Authorization': 'Bearer reader', 'X-Tenant-ID': '17', 'Origin': 'http://testserver'})
        assert login.status_code == 200, login.text
    rejected = request(client, origin=False, bearer=bearer)
    assert rejected.status_code == 403, rejected.text
    assert state['view_calls'] == 0
    accepted = request(client, bearer=bearer)
    assert accepted.status_code == 200, accepted.text
    assert accepted.content.startswith(b'PK')
    assert state['view_calls'] == 1
    state['restricted'] = True
    restricted = request(client, bearer=bearer)
    assert restricted.status_code == 403, restricted.text
    assert state['view_calls'] == 1


def test_ppt_scoped_route_is_read_only_and_does_not_prepare_sources():
    assert parse_workbench_path(f'/workbenches/{WB}{SUFFIX}') == (WB, SUFFIX)
    assert allowed_workbench_route(SUFFIX, 'POST')
    assert not source_preparation_required(SUFFIX)
    assert auth.is_read_only_post(SUFFIX)
    assert auth.is_read_only_post(SUFFIX.replace('/weekly/', '/daily/'))
    for path in (SUFFIX + '/save', SUFFIX.replace('/weekly/', '/monthly/'),
                 '/api/sn/reports/2023-01-03/export', '/api/sn/report-drafts',
                 SUFFIX.replace(DAY, 'arbitrary'), '/api/sn/strategies'):
        assert not auth.is_read_only_post(path), path
