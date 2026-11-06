"""Tool reads exercise signed identities, shared chart stores and page projections."""
from dataclasses import replace
from datetime import date, datetime, timedelta
import hashlib
import json

from fastapi import FastAPI, HTTPException
from fastapi.testclient import TestClient
import pytest

from tin.config import SHANGHAI, settings
from tin.integration import auth, read_api, workbench_scope
from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, current_workbench, isolated_session_factory
from tin.models.workbench import WorkbenchArtifact
from tests.chart_fixtures import FIELDS, OTHER, SID
from tests.test_chart_api import SECOND_WB, chart_client, create
from tests.test_research_reads import signed_headers
from tests.test_workbench_sources_v2 import WB, source


@pytest.fixture
def chart_reads(chart_client, monkeypatch, tmp_path):
    charts, state = chart_client
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://fixture.invalid')
    monkeypatch.setattr(settings, 'public_prefix', '')
    key = 'chart-read-test-key-' * 3
    token_file = tmp_path / 'read-key'
    token_file.write_text(key)
    monkeypatch.setattr(settings, 'service_token_file', token_file)
    state.scope_version, state.sources_revision, state.restricted = 2, 7, False
    state.login_user, state.revoked = 'first', False

    async def verify(token, tenant):
        assert token == 'fixture' and tenant == 17
        if state.revoked:
            raise HTTPException(403, '当前登录授权已失效')
        return Identity(state.login_user, 'fixture@test', 'Fixture', tenant, 'viewer')

    async def resolve(identity, identifier, authorization):
        return WorkbenchScope(identity, identifier, source(1)['knowledge_base_id'], 17,
            {'workspace_mode': 'isolated', 'can_edit': False}, authorization,
            scope_version=state.scope_version, sources_revision=state.sources_revision,
            sources=(source(1, state.restricted),))

    monkeypatch.setattr(auth, 'verify_bearer', verify)
    monkeypatch.setattr(workbench_scope, 'resolve_workbench', resolve)
    app = FastAPI()
    app.include_router(read_api.router)
    app.add_middleware(auth.ResearchAuthMiddleware)
    client = TestClient(app)

    def read(body, *, identity=None, headers=None, scoped=True):
        caller = {'user_id': 'first', 'email': 'fixture@test', 'display_name': 'Fixture',
            'tenant_id': 17, 'role': 'viewer', 'workbench_id': WB,
            'knowledge_base_id': source(1)['knowledge_base_id'], 'knowledge_base_tenant_id': 17,
            'scope_version': 2, 'sources_revision': 7,
            'authorization_sha256': hashlib.sha256(b'Bearer fixture').hexdigest()}
        caller.update(identity or {})
        if not scoped:
            caller = {key: value for key, value in caller.items() if key in {
                'user_id', 'email', 'display_name', 'tenant_id', 'role'}}
        if caller.get('scope_version') == 1:
            caller.pop('scope_version')
            caller.pop('sources_revision')
        raw = json.dumps(body, ensure_ascii=False).encode()
        signed = {**signed_headers(raw, key, caller), 'X-Research-Authorization': 'Bearer fixture'}
        signed.update(headers or {})
        return client.post('/api/tools/read', content=raw, headers=signed)

    return charts, read, state


@pytest.mark.parametrize('body', [
    {'operation': 'unknown'}, {'operation': 'cycle', 'template': '字' * 81},
    {'operation': 'cycle', 'template': 123}, {'operation': 'cycle', 'template': {}},
    {'operation': 'cycle', 'date': '2024-02-30'}, {'operation': 'charts', 'search': 'a' * 201},
    {'operation': 'charts', 'limit': 201}, {'operation': 'charts', 'limit': 0},
    {'operation': 'charts', 'offset': -1}, {'operation': 'charts', 'offset': 100001},
    {'operation': 'charts', 'series_id': 'a' * 81}, {'operation': 'charts', 'workbench_id': SECOND_WB},
    {'operation': 'cycle', 'tenant_id': 18},
])
def test_chart_read_request_validation(chart_reads, body):
    _, read, state = chart_reads
    assert read(body).status_code == 422
    assert state.requests == []


def test_chart_read_template_length_counts_characters_and_null_is_optional():
    assert read_api.ReadRequest(operation='cycle', template='字' * 80).template == '字' * 80
    assert read_api.ReadRequest(operation='cycle', template=None).template is None
    assert read_api.ReadRequest(operation='charts', limit=200, offset=100000).limit == 200


def test_charts_search_shared_definitions_filters_and_pagination(chart_reads):
    client, read, state = chart_reads
    first = create(client, title='Literal_100%', group='共享分组', curves=[{'series_id': SID, 'label': '显示名'}])
    cycle = create(client, title='另一张图', group='共享分组', time={'mode': 'cycle',
        'template': {'kind': 'repeat', 'name': '年', 'start': '01-01', 'period': 'year'}})
    create(client, title='其他图', curves=[{'series_id': OTHER, 'label': '其他指标'}])
    deleted = create(client, title='删除图', group='共享分组')
    assert client.delete('/api/sn/charts/' + deleted['id'] + '?revision=1').status_code == 200
    state.requests.clear()
    response = read({'operation': 'charts', 'search': '共享分组', 'series_id': SID, 'limit': 1})
    assert response.status_code == 200, response.text
    page = response.json()
    assert page['operation'] == 'charts' and page['workbench_id'] == WB
    assert (page['total'], page['offset'], page['limit'], page['next_offset'], page['truncated']) == (2, 0, 1, 1, True)
    second = read({'operation': 'charts', 'search': '共享分组', 'series_id': SID, 'limit': 1, 'offset': 1}).json()
    assert second['next_offset'] is None and not second['truncated']
    items = {item['id']: item for item in page['charts'] + second['charts']}
    assert set(items) == {first['id'], cycle['id']}
    assert items[first['id']] == {'id': first['id'], 'title': 'Literal_100%', 'group': '共享分组', 'mode': 'trend',
        'curves': [{'series_id': SID, 'label': '显示名', 'name': FIELDS[SID]['metric'], 'status': 'linked'}],
        'url': '/sn/charts/' + first['id']}
    assert items[cycle['id']]['mode'] == 'cycle'
    for search in ('literal_', '100%'):
        found = read({'operation': 'charts', 'search': search}).json()
        assert [item['id'] for item in found['charts']] == [first['id']]
    assert read({'operation': 'charts', 'search': '%_'}).json()['total'] == 0
    assert read({'operation': 'charts', 'series_id': 'kb-' + 'd' * 48}).json()['total'] == 0
    empty = read({'operation': 'charts', 'offset': 100}).json()
    assert empty['charts'] == [] and empty['next_offset'] is None
    foreign = read({'operation': 'charts'}, identity={'workbench_id': SECOND_WB}).json()
    assert foreign['charts'] == [] and foreign['total'] == 0
    state.login_user = 'second'
    shared = read({'operation': 'charts', 'series_id': SID}, identity={'user_id': 'second'})
    assert shared.status_code == 200 and shared.json()['total'] == 2
    assert all(body.get('catalog_only') for _, body in state.requests)


def test_charts_preserve_missing_curves_without_descriptor_rebinding(chart_reads):
    client, read, state = chart_reads
    saved = create(client, curves=[{'series_id': SID, 'label': '保留名称'}])
    del state.fields[SID]
    data = read({'operation': 'charts', 'series_id': SID}).json()
    assert data['charts'][0]['id'] == saved['id']
    assert data['charts'][0]['curves'] == [{'series_id': SID, 'label': '保留名称', 'name': '保留名称', 'status': 'missing'}]


def test_cycle_uses_chart_calculation_and_bounded_projection_without_curve_points(chart_reads):
    client, read, state = chart_reads
    observations = []
    for year in (2020, 2021, 2023, 2024):
        start = date(year, 1, 1)
        length = (date(year + 1, 1, 1) - start).days if year < 2024 else 3
        observations.extend(((start + timedelta(days=offset)).isoformat(), 100 + (year - 2020) * 10 + offset)
                            for offset in range(length))
    state.observations[SID] = observations
    expected = client.get('/api/sn/indicators/' + SID + '/cycle?date=2024-01-02').json()
    state.requests.clear()
    response = read({'operation': 'cycle', 'series_id': SID, 'date': '2024-01-02'})
    assert response.status_code == 200, response.text
    result = response.json()
    assert result['notice'] == '往期后续是历史参照，不是预测'
    data = result['data']
    assert set(data) == {'series_id', 'unit', 'template', 'freq_days', 'tolerance_days', 'as_of',
                         'position', 'forward', 'omitted_segments', 'warnings'}
    assert data['position'] == {key: expected['position'].get(key) for key in (
        'x', 'date', 'value', 'rank', 'n', 'share_below', 'min', 'max', 'median', 'mean', 'vs_recent_mean', 'status')}
    assert data['position']['status'] == 'ok' and data['position']['n'] == 3
    assert data['position']['value'] == 141 and data['position']['rank'] == 1
    assert data['forward'] == [{key: item[key] for key in ('h', 'days', 'n', 'up', 'median', 'min', 'max')}
                               for item in expected['forward']]
    assert [item['median'] for item in data['forward']] == [4, 8, 12]
    assert data['omitted_segments'] == ['2022'] and 'empty_segments_omitted' in data['warnings']
    assert data['template']['name'] == '自然年' and data['freq_days'] == 1 and data['as_of'] == '2024-01-02'
    assert all(key not in json.dumps(data) for key in ('"points"', '"band"', '"segments"', '"peers"', '"items"'))
    assert all(endpoint == '/api/research/facts' for endpoint, _ in state.requests)
    assert any(body.get('catalog_only') for _, body in state.requests)
    bounded = [body for _, body in state.requests if not body.get('catalog_only')]
    assert len(bounded) == 1
    body = bounded[0]
    assert body['series_ids'] == [SID] and body['view_module'] == 'indicators'
    assert body['points_per_series'] == 2000 and body['as_of'] == '2024-01-02'


def test_cycle_default_date_insufficient_samples_empty_data_and_window_warning(chart_reads):
    _, read, state = chart_reads
    response = read({'operation': 'cycle', 'series_id': SID, 'template': None})
    assert response.status_code == 200, response.text
    data = response.json()['data']
    assert data['position']['status'] == 'insufficient_cycles' and data['position']['rank'] is None
    assert data['forward'] == [] and data['template']['name'] == '自然年'
    assert data['as_of'] == datetime.now(SHANGHAI).date().isoformat()
    event = read({'operation': 'cycle', 'series_id': SID, 'template': '春节前后8周'}).json()['data']
    assert event['template']['calendar'] == 'spring_festival'
    state.observations[SID] = []
    data = read({'operation': 'cycle', 'series_id': SID}).json()['data']
    assert data['position'] is None and 'no_observations' in data['warnings']
    start = date(2010, 1, 1)
    state.observations[SID] = [((start + timedelta(days=index)).isoformat(), index) for index in range(2001)]
    data = read({'operation': 'cycle', 'series_id': SID}).json()['data']
    assert 'observation_window_limited' in data['warnings']


def test_cycle_resolves_shared_saved_template_and_rejects_missing_or_invalid_templates(chart_reads):
    client, read, state = chart_reads
    saved = client.post('/api/sn/cycle-templates', json={'kind': 'repeat', 'name': '相对起点',
        'start': '01-01', 'period': 'year', 'transform': 'change'}).json()
    body = {'operation': 'cycle', 'series_id': SID, 'template': saved['id'], 'date': '2023-01-03'}
    response = read(body)
    assert response.status_code == 200, response.text
    data = response.json()['data']
    assert data['template']['name'] == '相对起点' and data['template']['transform'] == 'change'
    assert data['position']['value'] == 2
    state.login_user = 'second'
    shared = read(body, identity={'user_id': 'second'})
    assert shared.status_code == 200 and shared.json()['data']['template'] == data['template']
    state.login_user = 'first'
    assert read(body, identity={'workbench_id': SECOND_WB}).status_code == 404
    for value in ('absent', '', create(client)['id']):
        assert read({**body, 'template': value}).status_code == 404
    for invalid in ('{', '{"kind":"repeat","name":"坏模板","start":"02-30","period":"year"}'):
        with isolated_session_factory(17, WB)() as session:
            session.get(WorkbenchArtifact, saved['id']).body = invalid
            session.commit()
        broken = read(body)
        assert broken.status_code == 409 and '模板格式无效' in broken.text
    with isolated_session_factory(17, WB)() as session:
        session.get(WorkbenchArtifact, saved['id']).status = 'deleted'
        session.commit()
    assert read(body).status_code == 404


@pytest.mark.parametrize('series_id,status', [(None, 400), ('', 400), ('not-a-real-id', 422), ('kb-' + 'd' * 48, 404)])
def test_cycle_requires_existing_real_series(chart_reads, series_id, status):
    _, read, _ = chart_reads
    response = read({'operation': 'cycle', 'series_id': series_id})
    assert response.status_code == status
    assert '指标' in response.text


@pytest.mark.parametrize('operation', ['charts', 'cycle'])
@pytest.mark.parametrize('problem,status', [('restricted', 403), ('legacy', 409), ('stale', 409),
    ('revoked', 403), ('login_changed', 403), ('tampered', 401), ('authorization', 401), ('no_workbench', 409)])
def test_chart_operations_share_signed_authorization_and_never_fall_back(chart_reads, operation, problem, status):
    _, read, state = chart_reads
    identity, headers = {}, {}
    if problem == 'restricted': state.restricted = True
    elif problem == 'legacy':
        state.scope_version = 1
        identity['scope_version'] = 1
    elif problem == 'stale': identity['sources_revision'] = 6
    elif problem == 'revoked': state.revoked = True
    elif problem == 'login_changed': state.login_user = 'other'
    elif problem == 'tampered': headers['X-Research-Signature'] = '0' * 64
    elif problem == 'authorization': headers['X-Research-Authorization'] = 'Bearer unsigned'
    response = read({'operation': operation, 'series_id': SID}, identity=identity, headers=headers,
                    scoped=problem != 'no_workbench')
    assert response.status_code == status, response.text
    assert state.requests == []


@pytest.mark.parametrize('operation', ['charts', 'cycle'])
def test_direct_read_helpers_reject_missing_or_mismatched_workbench_identity(session, operation):
    body = read_api.ReadRequest(operation=operation, series_id=SID)
    with pytest.raises(HTTPException) as old:
        read_api.read_research(session, body)
    assert old.value.status_code == 409
    identity = Identity('first', 'fixture@test', 'Fixture', 17, 'viewer', workbench_id=WB)
    scope = WorkbenchScope(identity, WB, '', 0, {'workspace_mode': 'isolated'},
        scope_version=2, sources_revision=7, sources=(source(1),))
    session.info.update(workbench_id=WB, tenant_id=17)
    for caller, active in ((None, scope), (identity, None), (identity, replace(scope, workbench_id=SECOND_WB)),
        (identity, replace(scope, identity=replace(identity, user_id='other'))),
        (identity, replace(scope, identity=replace(identity, tenant_id=18)))):
        marker, identity_marker = current_workbench.set(active), current_identity.set(caller)
        try:
            with pytest.raises(HTTPException) as denied:
                read_api.research_read(body)
            assert denied.value.status_code == (401 if caller is None else 403)
            with pytest.raises(HTTPException):
                read_api.read_isolated_research(session, body, WB)
        finally:
            current_workbench.reset(marker)
            current_identity.reset(identity_marker)
