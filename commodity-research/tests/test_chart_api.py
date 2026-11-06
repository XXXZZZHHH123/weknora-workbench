"""Real shared stores and page projections; every external transport is mocked."""
from copy import deepcopy
from dataclasses import replace
from datetime import date, datetime, timedelta
from types import SimpleNamespace

from fastapi import FastAPI
from fastapi.testclient import TestClient
import httpx
import pytest
from sqlalchemy import select
from sqlalchemy.orm import sessionmaker

from tin.config import SHANGHAI, settings
from tin.integration.identity import Identity, current_identity
from tin.integration.page_facts import _page
from tin.integration.workbench_scope import WorkbenchScope, current_workbench, isolated_session_factory
from tin.integration import workbench_scope
from tin.models import Indicator, Observation
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision
from tin.schemas.chart import ChartDefinition
from tin.web import app as web
from tin.web import charts, chart_import
from tests.chart_fixtures import FIELDS, KEY, OTHER, SID, THIRD, definition, native_chart, native_curve, yearly_native
from tests.test_workbench_sources_v2 import WB, source

SECOND_WB = '10000000-0000-4000-8000-000000000092'


@pytest.fixture
def chart_client(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    monkeypatch.setattr(settings, 'weknora_api_url', '')
    state = SimpleNamespace(fields=deepcopy(FIELDS), observations={SID: [('2023-01-01', 10), ('2023-01-02', 11),
        ('2023-01-03', 12), ('2023-01-04', 13)], OTHER: [('2023-01-01', 20)], THIRD: [('2023-01-01', 30)]},
        requests=[], native=[native_chart()], native_status=200, native_version=None, facts_version='f' * 64,
        on_catalog=None, on_native=None)
    direct = source(1)

    def transport(method, endpoint, body, **kwargs):
        assert method == 'POST' and kwargs == {'return_error_response': True}
        scope = current_workbench.get()
        assert scope and current_identity.get() == scope.identity and scope.scope_version == 2
        assert not scope.restricted
        state.requests.append((endpoint, deepcopy(body)))
        envelope = {'scope_version': 2, 'workbench_id': scope.workbench_id,
                    'sources_revision': 7, 'version': state.facts_version}
        if endpoint == '/api/research/native-charts':
            assert set(body) == {'offset', 'limit'} and 1 <= body['limit'] <= 200
            offset, limit = body['offset'], body['limit']
            payload = {'charts': state.native[offset:offset + limit], 'offset': offset, 'total': len(state.native),
                       'next_offset': offset + limit if offset + limit < len(state.native) else None}
            if state.native_version is not None:
                payload['version'] = state.native_version(offset)
            if state.on_native:
                state.on_native(payload)
            return httpx.Response(state.native_status, json=payload, request=httpx.Request(method, 'http://fixture.invalid' + endpoint))
        assert endpoint == '/api/research/facts'
        assert body['source_bindings'] == [direct['id']]
        ids = body.get('series_ids', list(state.fields))
        fields = [{**value, 'name': value['metric'], 'source_binding_id': direct['id'],
                   'knowledge_base_id': direct['knowledge_base_id'], 'knowledge_base_tenant_id': 17,
                   'source_type': direct['source_type'], 'source_id': direct['source_id'],
                   'source_knowledge_id': 'fixture-document', 'dataset_id': 'fixture-dataset',
                   'source_series_id': sid, 'source_file': 'fixture.xlsx', 'sheet': '测试表'}
                  for sid, value in state.fields.items() if sid in ids]
        if body.get('catalog_only'):
            assert body['offset'] == 0 and body['limit'] <= 200
            assert 'points_per_series' not in body
            payload = {**envelope, 'catalog_only': True, 'series': fields, 'datasets': [], 'total': len(fields),
                       'offset': 0, 'next_offset': None, 'truncated': False}
            if state.on_catalog:
                state.on_catalog(payload)
        else:
            request = _page.get()
            assert request['module'] == body['view_module'] == 'indicators'
            assert request['series_ids'] == body['series_ids']
            assert request['points'] == body['points_per_series'] <= 2000
            assert request['day'] == body['as_of']
            rows = []
            totals = {}
            for field in fields:
                sid = field['series_id']
                points = [(stamp, value) for stamp, value in state.observations.get(sid, []) if stamp <= body['as_of']]
                totals[sid] = len(points)
                rows.extend({'series_id': sid, 'date': stamp, 'value': value, 'value_status': 'valid',
                             'source_cell': 'M' + str(index), 'source_binding_id': direct['id']}
                            for index, (stamp, value) in enumerate(points[-body['points_per_series']:]))
            payload = {**envelope, 'bounded': True, 'indicators': fields, 'rows': rows, 'datasets': [],
                       'source_versions': {direct['id']: 'e' * 64}, 'series_totals': totals,
                       'unavailable_series_ids': list(set(ids) - set(state.fields)), 'offset': 0,
                       'total': len(rows), 'next_offset': None, 'truncated': False}
        return httpx.Response(200, json=payload, request=httpx.Request(method, 'http://fixture.invalid' + endpoint))

    monkeypatch.setattr('tin.provenance.bridge.structured_request', transport)
    app = FastAPI()
    app.include_router(charts.router)
    app.include_router(chart_import.router)

    @app.middleware('http')
    async def scope_fixture(request, call_next):
        identity = Identity(request.headers.get('x-actor', 'first'), 'fixture@test', 'Fixture', 17,
                            request.headers.get('x-role', 'owner'))
        scope = WorkbenchScope(identity, request.headers.get('x-workbench', WB), '', 0,
            {'workspace_mode': request.headers.get('x-workspace-mode', 'isolated'),
             'can_edit': request.headers.get('x-can-edit', 'true') == 'true'},
            'Bearer fixture', scope_version=int(request.headers.get('x-scope-version', '2')), sources_revision=7,
            sources=(source(1, request.headers.get('x-restricted') == 'true'),))
        marker = current_workbench.set(None if request.headers.get('x-local') == 'true' else scope)
        identity_marker = current_identity.set(identity)
        try:
            return await call_next(request)
        finally:
            current_workbench.reset(marker)
            current_identity.reset(identity_marker)

    state.app = app
    return TestClient(app), state


def create(client, **changes):
    response = client.post('/api/sn/charts', json=definition(**changes))
    assert response.status_code == 201, response.text
    return response.json()


def test_chart_shared_read_write_revisions_and_workbench_isolation(chart_client):
    client, state = chart_client
    saved = create(client)
    identifier = saved['id']
    other = {'x-actor': 'second', 'x-role': 'viewer', 'x-can-edit': 'false'}
    read = client.get('/api/sn/charts/' + identifier, headers=other)
    assert read.status_code == 200 and read.json()['revision'] == 1
    assert client.get('/api/sn/charts', headers=other).json()['total'] == 1
    assert client.get('/api/sn/charts', headers={'x-workbench': SECOND_WB}).json()['total'] == 0
    assert client.get('/api/sn/charts/' + identifier, headers={'x-workbench': SECOND_WB}).status_code == 404
    changed = client.patch('/api/sn/charts/' + identifier, json={'revision': 1, 'title': '成员共同修改'}, headers={'x-actor': 'second'})
    assert changed.status_code == 200 and changed.json()['revision'] == 2
    history = client.get('/api/sn/charts/' + identifier + '/revisions').json()
    assert history['total'] == 2
    assert [row['revision'] for row in history['items']] == [2, 1]
    assert history['items'][0]['edited_by'] == 'second' and history['items'][1]['definition']['title'] == '测试图'
    with isolated_session_factory(17, WB)() as session:
        row = session.get(WorkbenchArtifact, identifier)
        assert row.module == 'charts' and row.actor == 'first'
    assert all(body.get('catalog_only') for endpoint, body in state.requests), 'Definition operations only need directory metadata'


def test_revision_conflict_delete_tombstone_and_history_survive(chart_client):
    client, _ = chart_client
    identifier = create(client)['id']
    assert client.patch('/api/sn/charts/' + identifier, json={'revision': 1, 'note': '新修订'}).status_code == 200
    assert client.patch('/api/sn/charts/' + identifier, json={'revision': 1, 'title': '旧版覆盖'}).status_code == 409
    assert client.delete('/api/sn/charts/' + identifier + '?revision=1').status_code == 409
    assert client.delete('/api/sn/charts/' + identifier).status_code == 422
    deleted = client.request('DELETE', '/api/sn/charts/' + identifier, json={'revision': 2})
    assert deleted.status_code == 200 and deleted.json()['revision'] == 3 and deleted.json()['status'] == 'deleted'
    assert client.get('/api/sn/charts/' + identifier).status_code == 404
    assert client.get('/api/sn/charts/' + identifier + '/data').status_code == 404
    assert client.get('/api/sn/charts').json()['total'] == 0
    history = client.get('/api/sn/charts/' + identifier + '/revisions').json()
    assert [row['status'] for row in history['items']] == ['deleted', 'draft', 'draft']
    assert history['items'][0]['definition']['note'] == '新修订'


@pytest.mark.parametrize('status', ['draft', 'deleted'])
def test_revision_compare_and_swap_rechecks_after_directory_validation(chart_client, monkeypatch, status):
    client, _ = chart_client
    identifier = create(client)['id']
    original = charts.validate_definition

    def racing_validation(scope, value):
        notices = original(scope, value)
        with isolated_session_factory(17, WB)() as session:
            row = session.get(WorkbenchArtifact, identifier)
            row.revision = 2
            row.title = '并发修改'
            row.status = status
            session.commit()
        return notices

    monkeypatch.setattr(charts, 'validate_definition', racing_validation)
    response = client.patch('/api/sn/charts/' + identifier, json={'revision': 1, 'title': '被拒绝的覆盖'})
    assert response.status_code == 409
    with isolated_session_factory(17, WB)() as session:
        assert session.get(WorkbenchArtifact, identifier).title == '并发修改'
        assert session.scalar(select(WorkbenchArtifactRevision).where(WorkbenchArtifactRevision.revision == 2)) is None


@pytest.mark.parametrize('headers,status', [({'x-restricted': 'true'}, 403), ({'x-scope-version': '1'}, 409)])
@pytest.mark.parametrize('method,path,body', [
    ('GET', '/api/sn/charts', None), ('POST', '/api/sn/charts', definition()),
    ('GET', '/api/sn/charts/absent', None), ('PATCH', '/api/sn/charts/absent', {'revision': 1, 'title': '修改'}),
    ('DELETE', '/api/sn/charts/absent?revision=1', None), ('GET', '/api/sn/charts/absent/revisions', None),
    ('GET', '/api/sn/charts/absent/data', None), ('GET', '/api/sn/cycle-templates', None),
    ('POST', '/api/sn/cycle-templates', {'kind': 'repeat', 'name': '模板', 'start': '01-01', 'period': 'year'}),
    ('POST', '/api/sn/charts/import/native/preview', {}),
    ('POST', '/api/sn/charts/import/native/apply', {'sheets': [{'knowledge_id': 'fixture-document', 'sheet': '测试表'}]}),
])
def test_all_library_routes_reject_restricted_and_old_grants_without_reads(chart_client, headers, status, method, path, body):
    client, state = chart_client
    response = client.request(method, path, json=body, headers=headers)
    assert response.status_code == status, response.text
    assert state.requests == []


@pytest.mark.parametrize('headers', [{'x-can-edit': 'false'}, {'x-role': 'viewer'}, {'x-role': 'contributor', 'x-can-edit': 'false'}])
def test_every_write_requires_shared_write_even_for_members_who_can_create_personal_notes(chart_client, headers):
    client, state = chart_client
    identifier = create(client)['id']
    state.requests.clear()
    assert client.post('/api/sn/charts', json=definition(), headers=headers).status_code == 403
    assert client.patch('/api/sn/charts/' + identifier, json={'revision': 1, 'note': '拒绝'}, headers=headers).status_code == 403
    assert client.delete('/api/sn/charts/' + identifier + '?revision=1', headers=headers).status_code == 403
    assert client.post('/api/sn/cycle-templates', json={'kind': 'repeat', 'name': '模板', 'start': '01-01', 'period': 'year'}, headers=headers).status_code == 403
    assert client.post('/api/sn/charts/import/native/apply', json={'sheets': [{'knowledge_id': 'fixture-document', 'sheet': '测试表'}]}, headers=headers).status_code == 403
    assert state.requests == []


@pytest.mark.parametrize('identifier', ['kb-' + 'd' * 48, 'invented-from-label'])
def test_missing_series_id_cannot_be_saved_or_auto_rebound_from_descriptor(chart_client, identifier):
    client, _ = chart_client
    value = definition()
    value['curves'][0].update(series_id=identifier, descriptor={'metric': '字段甲', 'sheet': '测试表'})
    assert client.post('/api/sn/charts', json=value).status_code == 422
    assert client.get('/api/sn/charts').json()['total'] == 0


def test_same_axis_unit_mismatch_rejected_and_unknown_units_only_warn(chart_client):
    client, state = chart_client
    value = definition(curves=[{'series_id': SID, 'label': '件'}, {'series_id': THIRD, 'label': '元'}])
    assert client.post('/api/sn/charts', json=value).status_code == 422
    value['curves'][1]['axis'] = 'right'
    assert client.post('/api/sn/charts', json=value).status_code == 201
    state.fields[THIRD]['unit'] = ''
    value['curves'][1]['axis'] = 'left'
    response = client.post('/api/sn/charts', json=value)
    assert response.status_code == 201
    assert response.json()['warnings'][0]['code'] == 'unit_missing'


def test_cycle_multiple_curves_and_unknown_nested_fields_are_rejected(chart_client):
    client, _ = chart_client
    value = definition(curves=[{'series_id': SID, 'label': '一'}, {'series_id': OTHER, 'label': '二'}],
                       time={'mode': 'cycle', 'template': {'kind': 'repeat', 'name': '年', 'start': '01-01', 'period': 'year'}})
    assert client.post('/api/sn/charts', json=value).status_code == 422
    for path in ('top', 'curve', 'template', 'style', 'axes'):
        value = definition()
        if path == 'top':
            value['unrecognized'] = 1
        elif path == 'curve':
            value['curves'][0]['unit'] = '伪造单位'
        elif path == 'template':
            value['time'] = {'mode': 'cycle', 'template': {'kind': 'repeat', 'name': '年', 'start': '01-01', 'period': 'year', 'guess': True}}
        elif path == 'style':
            value['curves'][0]['style'] = {'opacity': 0.5}
        else:
            value['axes'] = {'left': {'autoscale': True}}
        assert client.post('/api/sn/charts', json=value).status_code == 422


def test_sixteen_curve_limit_applies_to_both_create_and_update(chart_client):
    client, _ = chart_client
    curves = [{'series_id': SID, 'label': f'曲线{index}'} for index in range(17)]
    saved = create(client, curves=curves[:16])
    assert len(saved['definition']['curves']) == 16
    assert client.post('/api/sn/charts', json=definition(curves=curves)).status_code == 422
    identifier = create(client)['id']
    changed = client.patch('/api/sn/charts/' + identifier, json={'revision': 1, 'curves': curves[:16]})
    assert changed.status_code == 200, changed.text
    assert len(changed.json()['definition']['curves']) == 16 and changed.json()['revision'] == 2
    assert client.patch('/api/sn/charts/' + identifier, json={'revision': 2, 'curves': curves}).status_code == 422
    assert client.get('/api/sn/charts/' + identifier).json()['revision'] == 2


def test_saved_missing_curve_stays_visible_in_definition_and_data(chart_client):
    client, state = chart_client
    saved = create(client, curves=[{'series_id': SID, 'label': '仍可用'}, {'series_id': OTHER, 'label': '来源失效'}])
    del state.fields[OTHER]
    read = client.get('/api/sn/charts/' + saved['id']).json()
    assert len(read['definition']['curves']) == 2 and read['curves'][1]['status'] == 'missing'
    data = client.get('/api/sn/charts/' + saved['id'] + '/data?date=2023-01-03').json()
    assert data['curves'][1]['status'] == 'missing' and data['curves'][1]['points'] == []
    assert data['curves'][0]['points'][-1] == {'date': '2023-01-03', 'value': 12}
    assert any(w['code'] == 'missing' for w in data['warnings'])


def test_data_modes_limits_cutoffs_and_projection_mask_archived_facts(chart_client):
    client, state = chart_client
    saved = create(client, time={'mode': 'range', 'start': '2023-01-02', 'end': '2023-01-03'})
    with isolated_session_factory(17, WB)() as session:
        session.add(Indicator(series_id=SID, name='历史副本', variety='', category='', caliber={}, unit='错误单位', source='旧库', frequency='日', fetch_mode='readonly'))
        session.flush()
        stamp = datetime(2023, 1, 2, tzinfo=SHANGHAI)
        session.add(Observation(series_id=SID, value=9999, as_of=stamp, fetched_at=stamp, caliber_snapshot={}, source='旧库'))
        session.commit()
    response = client.get('/api/sn/charts/' + saved['id'] + '/data?date=2023-12-31')
    assert response.status_code == 200, response.text
    data = response.json()
    assert data['as_of'] == '2023-01-03' and data['curves'][0]['unit'] == '件'
    assert data['curves'][0]['points'] == [{'date': '2023-01-02', 'value': 11}, {'date': '2023-01-03', 'value': 12}]
    bounded = [body for _, body in state.requests if 'points_per_series' in body]
    assert bounded[-1]['points_per_series'] == 2000 and bounded[-1]['series_ids'] == [SID]
    recent = create(client, time={'mode': 'recent', 'points': 2})
    data = client.get('/api/sn/charts/' + recent['id'] + '/data?date=2023-01-04').json()
    assert [p['value'] for p in data['curves'][0]['points']] == [12, 13]
    assert data['curves'][0]['limited'] is True


@pytest.mark.parametrize('count,time,expected', [
    (1, {'mode': 'since', 'start': '2023-01-01'}, 2000),
    (10, {'mode': 'since', 'start': '2023-01-01'}, 2000),
    (11, {'mode': 'since', 'start': '2023-01-01'}, 1818),
    (12, {'mode': 'since', 'start': '2023-01-01'}, 1666),
    (16, {'mode': 'since', 'start': '2023-01-01'}, 1250),
    (12, {'mode': 'recent', 'points': 2000}, 1666),
    (16, {'mode': 'recent', 'points': 120}, 120),
    (16, {'mode': 'range', 'start': '2023-01-01', 'end': '2023-01-04'}, 1250),
])
def test_chart_point_budget_counts_curves_and_respects_smaller_recent_requests(chart_client, count, time, expected):
    client, state = chart_client
    identifiers = ['kb-' + f'{index:048x}' for index in range(count)]
    for sid in identifiers:
        state.fields[sid] = {**FIELDS[SID], 'series_id': sid}
        state.observations[sid] = state.observations[SID]
    saved = create(client, curves=[{'series_id': sid, 'label': f'曲线{index}'} for index, sid in enumerate(identifiers)], time=time)
    response = client.get('/api/sn/charts/' + saved['id'] + '/data?date=2023-01-04')
    assert response.status_code == 200, response.text
    data = response.json()
    assert data['point_limit'] == expected and len(data['curves']) == count
    bounded = [body for _, body in state.requests if 'points_per_series' in body]
    assert len(bounded) == 1 and bounded[0]['series_ids'] == identifiers
    assert bounded[0]['points_per_series'] == expected
    assert len(identifiers) * expected <= 20000


def test_point_budget_counts_duplicate_and_missing_curves_in_the_definition(chart_client):
    client, state = chart_client
    curves = [{'series_id': SID if index < 12 else OTHER, 'label': f'曲线{index}'} for index in range(16)]
    saved = create(client, curves=curves, time={'mode': 'since', 'start': '2023-01-01'})
    del state.fields[OTHER]
    response = client.get('/api/sn/charts/' + saved['id'] + '/data?date=2023-01-04')
    assert response.status_code == 200, response.text
    data = response.json()
    assert data['point_limit'] == 1250
    assert len(data['curves']) == 16 and sum(curve['status'] == 'missing' for curve in data['curves']) == 4
    bounded = [body for _, body in state.requests if 'points_per_series' in body]
    assert len(bounded) == 1 and bounded[0]['series_ids'] == [SID] and bounded[0]['points_per_series'] == 1250


def test_twelve_curve_data_read_stays_within_twenty_thousand_points(chart_client):
    client, state = chart_client
    identifiers = ['kb-' + f'{index:048x}' for index in range(12)]
    points = [((date(2017, 1, 1) + timedelta(days=offset)).isoformat(), offset) for offset in range(2001)]
    state.fields = {sid: {**FIELDS[SID], 'series_id': sid} for sid in identifiers}
    state.observations = {sid: points for sid in identifiers}
    saved = create(client, curves=[{'series_id': sid, 'label': f'曲线{index}'} for index, sid in enumerate(identifiers)],
                   time={'mode': 'since', 'start': points[0][0]})
    response = client.get('/api/sn/charts/' + saved['id'] + '/data', params={'date': points[-1][0]})
    assert response.status_code == 200, response.text
    data = response.json()
    assert data['point_limit'] == 1666
    assert [len(curve['points']) for curve in data['curves']] == [1666] * 12
    assert sum(len(curve['points']) for curve in data['curves']) == 19992
    assert all(curve['limited'] for curve in data['curves'])
    assert all(curve['points'][0]['date'] == points[-1666][0] for curve in data['curves'])
    bounded = [body for _, body in state.requests if 'points_per_series' in body]
    assert len(bounded) == 1 and bounded[0]['points_per_series'] * len(bounded[0]['series_ids']) == 19992


def test_unit_change_after_save_requires_axis_repair_before_rendering(chart_client):
    client, state = chart_client
    identifier = create(client, curves=[{'series_id': SID, 'label': '甲'}, {'series_id': OTHER, 'label': '乙'}])['id']
    state.fields[OTHER]['unit'] = '美元'
    assert client.get('/api/sn/charts/' + identifier + '/data').status_code == 409
    assert any(w['code'] == 'axis_unit_mismatch' for w in client.get('/api/sn/charts/' + identifier).json()['warnings'])


def test_templates_are_shared_and_cycle_data_uses_embedded_transform(chart_client):
    client, _ = chart_client
    body = {'kind': 'segments', 'name': '共享段', 'segments': [{'label': '不解释图例', 'start': '2023-01-01', 'end': '2023-01-10'}]}
    saved = client.post('/api/sn/cycle-templates', json=body)
    assert saved.status_code == 201, saved.text
    template_id = saved.json()['id']
    listed = client.get('/api/sn/cycle-templates', headers={'x-actor': 'second', 'x-role': 'viewer'}).json()['items']
    assert [row['id'] for row in listed[:2]] == ['自然年', '春节前后8周']
    assert listed[2]['id'] == template_id
    instant = client.get('/api/sn/indicators/' + SID + '/cycle', params={'template': template_id, 'date': '2023-01-03'})
    assert instant.status_code == 200 and instant.json()['position']['value'] == 12
    chart = create(client, time={'mode': 'cycle', 'template': body, 'transform': 'change'})
    result = client.get('/api/sn/charts/' + chart['id'] + '/data?date=2023-01-03').json()
    assert result['template']['transform'] == 'change' and result['segments'][0]['points'][-1]['value'] == 2
    with isolated_session_factory(17, WB)() as session:
        assert session.get(WorkbenchArtifact, template_id).module == 'cycle_templates'
        assert session.get(WorkbenchArtifactRevision, (template_id, 1)) is not None


@pytest.mark.parametrize('headers', [{'x-local': 'true'}, {'x-scope-version': '1'}])
def test_instant_cycle_legacy_observations_use_current_session_and_latest_revision(chart_client, session, monkeypatch, headers):
    client, state = chart_client
    sid = 'FIXTURE.LEGACY.CYCLE'
    session.add(Indicator(series_id=sid, name='旧来源指标', variety='', category='', caliber={}, unit='原单位', source='本地', frequency='日', fetch_mode='manual'))
    session.flush()
    for day, revision, value, status in [(1, 0, 5, '正常'), (2, 0, 10, '正常'), (2, 1, 20, '正常'),
                                          (3, 0, 30, '正常'), (3, 1, 40, '无效'), (4, 0, 50, '正常')]:
        stamp = datetime(2023, 1, day, tzinfo=SHANGHAI)
        session.add(Observation(series_id=sid, value=value, as_of=stamp, fetched_at=stamp, caliber_snapshot={}, source='本地', revision=revision, status=status))
    session.commit()
    monkeypatch.setattr(web, 'SessionLocal', sessionmaker(session.get_bind(), expire_on_commit=False))
    response = client.get('/api/sn/indicators/' + sid + '/cycle?date=2023-01-03', headers=headers)
    assert response.status_code == 200, response.text
    result = response.json()
    assert result['unit'] == '原单位'
    assert result['position']['date'] == '2023-01-02' and result['position']['value'] == 20
    assert [point['raw'] for point in result['segments'][0]['points']] == [5, 20]
    assert state.requests == [], 'Legacy current-session observations must not open another data channel'


def test_instant_cycle_restricted_missing_id_and_unknown_template(chart_client):
    client, _ = chart_client
    base = '/api/sn/indicators/' + SID + '/cycle'
    assert client.get(base, headers={'x-restricted': 'true'}).status_code == 403
    assert client.get('/api/sn/indicators/kb-' + 'd' * 48 + '/cycle').status_code == 404
    assert client.get(base + '?template=absent').status_code == 404
    assert client.get(base + '?date=2023-1-1').status_code == 400


def test_list_search_group_series_filters_and_pagination(chart_client):
    client, _ = chart_client
    create(client, title='literal%_title')
    create(client, title='other', group='第二组', curves=[{'series_id': OTHER, 'label': '乙'}])
    assert client.get('/api/sn/charts', params={'q': '%_'}).json()['total'] == 1
    assert client.get('/api/sn/charts', params={'group': '第二组'}).json()['items'][0]['title'] == 'other'
    assert client.get('/api/sn/charts', params={'series_id': SID}).json()['total'] == 1
    first = client.get('/api/sn/charts?limit=1').json()
    assert first['total'] == 2 and first['next_offset'] == 1
    assert client.get('/api/sn/charts?offset=1&limit=1').json()['next_offset'] is None
    assert client.get('/api/sn/charts?limit=101').status_code == 422


def test_native_preview_groups_missing_curves_and_latest_period_notice_for_readers(chart_client):
    client, state = chart_client
    state.native = [yearly_native(), native_chart(chart_key='nc-' + '2' * 32, curves=[native_curve(status='pending_source')])]
    state.observations[SID] = [('2024-01-01', 88)]
    response = client.post('/api/sn/charts/import/native/preview', json={}, headers={'x-role': 'viewer', 'x-can-edit': 'false'})
    assert response.status_code == 200, response.text
    data = response.json()
    assert data['total'] == 2 and data['next_offset'] is None
    sheet = data['sheets'][0]
    assert (sheet['cycles'], sheet['trends'], sheet['unconvertible']) == (1, 0, 1)
    assert sheet['charts'][0]['warnings'][0]['code'] == 'latest_cycle_missing'
    assert sheet['charts'][1]['missing_curves'][0]['reason'] == 'pending_source'
    assert any(endpoint == '/api/research/native-charts' for endpoint, _ in state.requests)
    assert all(body['points_per_series'] == 1 for _, body in state.requests if 'points_per_series' in body)


def test_native_apply_selected_sheet_override_dedup_deleted_origin_and_style_snapshot(chart_client):
    client, state = chart_client
    state.native = [yearly_native(), native_chart(chart_key='nc-' + '2' * 32, sheet='未勾选')]
    body = {'sheets': [{'knowledge_id': 'fixture-document', 'sheet': '测试表', 'start': '09-01'}]}
    response = client.post('/api/sn/charts/import/native/apply', json=body)
    assert response.status_code == 200, response.text
    result = response.json()
    assert len(result['created']) == 1 and result['skipped'] == []
    saved = result['created'][0]
    assert saved['definition']['time']['template']['start'] == '09-01'
    assert len(saved['source_snapshots'][0]['curves']) == 3
    again = client.post('/api/sn/charts/import/native/apply', json=body).json()
    assert again['created'] == [] and again['skipped'] == [{'chart_key': KEY, 'id': saved['id'], 'reason': 'already_imported'}]
    assert client.delete('/api/sn/charts/' + saved['id'] + '?revision=1').status_code == 200
    # A researcher who deleted an imported chart can import that sheet again.
    preview = client.post('/api/sn/charts/import/native/preview', json={}).json()
    assert preview['sheets'][0]['charts'][0]['already_imported'] is False
    recreated = client.post('/api/sn/charts/import/native/apply', json=body).json()
    assert len(recreated['created']) == 1 and recreated['created'][0]['id'] != saved['id']
    preview = client.post('/api/sn/charts/import/native/preview', json={}).json()
    assert preview['sheets'][0]['charts'][0]['already_imported'] is True


def test_native_apply_pages_through_current_sources_and_only_creates_selected_chart_keys(chart_client):
    client, state = chart_client
    state.native = [native_chart(chart_key='nc-' + f'{index:032x}') for index in range(201)]
    selected_key = state.native[-1]['chart_key']
    body = {'sheets': [{'knowledge_id': 'fixture-document', 'sheet': '测试表', 'chart_keys': [selected_key]}]}
    response = client.post('/api/sn/charts/import/native/apply', json=body)
    assert response.status_code == 200, response.text
    assert len(response.json()['created']) == 1
    native_requests = [body for endpoint, body in state.requests if endpoint.endswith('/native-charts')]
    assert native_requests == [{'offset': 0, 'limit': 200}, {'offset': 200, 'limit': 200}]


def test_native_chart_key_duplicates_create_once_within_an_apply(chart_client):
    client, state = chart_client
    state.native = [native_chart(), native_chart()]
    response = client.post('/api/sn/charts/import/native/apply', json={'sheets': [{'knowledge_id': 'fixture-document', 'sheet': '测试表'}]})
    assert response.status_code == 200 and len(response.json()['created']) == 1
    assert client.get('/api/sn/charts').json()['total'] == 1


def test_native_apply_changed_source_unknown_selection_and_invalid_pagination_never_write(chart_client):
    client, state = chart_client
    body = {'sheets': [{'knowledge_id': 'fixture-document', 'sheet': '测试表'}]}
    state.native_status = 409
    assert client.post('/api/sn/charts/import/native/apply', json=body).status_code == 409
    state.native_status = 200
    body['sheets'][0]['chart_keys'] = ['nc-' + 'f' * 32]
    assert client.post('/api/sn/charts/import/native/apply', json=body).status_code == 409
    state.on_native = lambda page: page.update(next_offset=200)
    assert client.post('/api/sn/charts/import/native/preview', json={}).status_code == 503
    assert client.get('/api/sn/charts').json()['total'] == 0


def test_native_apply_rejects_changed_snapshot_between_pages(chart_client):
    client, state = chart_client
    state.native = [native_chart(chart_key='nc-' + f'{index:032x}') for index in range(201)]
    state.native_version = lambda offset: 'a' * 64 if offset == 0 else 'b' * 64
    response = client.post('/api/sn/charts/import/native/apply', json={'sheets': [{'knowledge_id': 'fixture-document', 'sheet': '测试表'}]})
    assert response.status_code == 409
    assert client.get('/api/sn/charts').json()['total'] == 0


def test_reconvert_reads_current_original_and_only_confirmation_creates_revision(chart_client):
    client, state = chart_client
    old = {'mode': 'cycle', 'template': {'kind': 'segments', 'name': '原有分段', 'segments': [
        {'label': '旧段', 'start': '2020-01-01', 'end': '2020-12-31'}]}}
    saved = create(client, title='研究员旧标题', time=old, origin={'kind': 'native', 'ref': KEY})
    path = '/api/sn/charts/' + saved['id']
    state.native = [yearly_native()]
    state.requests.clear()
    preview = client.get(path + '/reconvert')
    assert preview.status_code == 200, preview.text
    proposal = preview.json()
    assert proposal['id'] == saved['id'] and proposal['revision'] == 1
    fresh = proposal['suggestion']['definition']
    assert fresh['time']['template']['kind'] == 'repeat'
    assert fresh['origin'] == {'kind': 'native', 'ref': KEY}
    assert fresh['title'] == state.native[0]['title']
    assert any(endpoint.endswith('/native-charts') for endpoint, _ in state.requests)
    # A preview (including repeated previews) must not rewrite old definitions.
    assert client.get(path).json()['definition']['time']['template']['kind'] == 'segments'
    assert len(client.get(path + '/revisions').json()['items']) == 1
    assert client.get(path + '/reconvert').json()['revision'] == 1
    changed = client.patch(path, json={'revision': proposal['revision'], **fresh})
    assert changed.status_code == 200, changed.text
    assert changed.json()['revision'] == 2
    history = client.get(path + '/revisions').json()['items']
    assert [item['revision'] for item in history] == [2, 1]
    assert history[1]['definition']['title'] == '研究员旧标题'
    assert history[1]['definition']['time']['template']['kind'] == 'segments'
    assert client.patch(path, json={'revision': 1, **fresh}).status_code == 409


def test_reconvert_permission_scope_and_origin_guards(chart_client):
    client, state = chart_client
    manual = create(client)
    assert client.get('/api/sn/charts/' + manual['id'] + '/reconvert').status_code == 422
    saved = create(client, origin={'kind': 'native', 'ref': KEY})
    path = '/api/sn/charts/' + saved['id']
    viewer = {'x-role': 'viewer', 'x-can-edit': 'false'}
    preview = client.get(path + '/reconvert', headers=viewer)
    assert preview.status_code == 200
    assert client.patch(path, json={'revision': 1, **preview.json()['suggestion']['definition']}, headers=viewer).status_code == 403
    for headers, status in [({'x-restricted': 'true'}, 403), ({'x-scope-version': '1'}, 409),
                            ({'x-local': 'true'}, 409), ({'x-workbench': SECOND_WB}, 404)]:
        state.requests.clear()
        assert client.get(path + '/reconvert', headers=headers).status_code == status
        assert state.requests == []
    assert client.delete(path + '?revision=1').status_code == 200
    assert client.get(path + '/reconvert').status_code == 404


def test_reconvert_missing_or_changed_source_never_uses_saved_snapshot(chart_client):
    client, state = chart_client
    imported = client.post('/api/sn/charts/import/native/apply', json={
        'sheets': [{'knowledge_id': 'fixture-document', 'sheet': '测试表'}]}).json()['created'][0]
    path = '/api/sn/charts/' + imported['id']
    assert imported['source_snapshots']
    state.native = []
    assert client.get(path + '/reconvert').status_code == 409
    state.native = [native_chart(curves=[native_curve(status='pending_source')])]
    unavailable = client.get(path + '/reconvert')
    assert unavailable.status_code == 200 and unavailable.json()['suggestion']['convertible'] is False
    state.native_status = 403
    assert client.get(path + '/reconvert').status_code == 403
    state.native_status = 200
    state.native = [native_chart(chart_key='nc-' + f'{index:032x}') for index in range(201)]
    state.native_version = lambda offset: 'a' * 64 if offset == 0 else 'b' * 64
    assert client.get(path + '/reconvert').status_code == 409
    assert client.get(path).json()['revision'] == 1
    assert len(client.get(path + '/revisions').json()['items']) == 1


def test_reconvert_locates_original_beyond_first_page_and_rejects_changed_duplicate(chart_client):
    client, state = chart_client
    state.native = [native_chart(chart_key='nc-' + f'{index:032x}') for index in range(201)]
    ref = state.native[-1]['chart_key']
    saved = create(client, origin={'kind': 'native', 'ref': ref})
    path = '/api/sn/charts/' + saved['id'] + '/reconvert'
    response = client.get(path)
    assert response.status_code == 200
    assert response.json()['suggestion']['chart_key'] == ref
    state.native.append(native_chart(chart_key=ref, title='目录在读取时改变'))
    assert client.get(path).status_code == 409


def test_cycle_visible_cycles_round_trip_and_strict_limits(chart_client):
    client, _ = chart_client
    time = {'mode': 'cycle', 'template': {'kind': 'repeat', 'name': '年', 'start': '01-01', 'period': 'year'}}
    saved = create(client, time={**time, 'visible_cycles': 2})
    path = '/api/sn/charts/' + saved['id']
    assert saved['definition']['time']['visible_cycles'] == 2
    assert client.get(path).json()['definition']['time']['visible_cycles'] == 2
    changed = client.patch(path, json={'revision': 1, 'time': {**time, 'visible_cycles': 40}})
    assert changed.status_code == 200 and changed.json()['definition']['time']['visible_cycles'] == 40
    for invalid in (0, 41, 1.5, True, '3'):
        assert client.post('/api/sn/charts', json=definition(time={**time, 'visible_cycles': invalid})).status_code == 422
        assert client.patch(path, json={'revision': 2, 'time': {**time, 'visible_cycles': invalid}}).status_code == 422
    reset = client.patch(path, json={'revision': 2, 'time': time})
    assert reset.status_code == 200 and reset.json()['definition']['time']['visible_cycles'] is None
    assert client.get(path + '/revisions').json()['items'][1]['definition']['time']['visible_cycles'] == 40


def test_preview_pagination_and_template_validation_are_bounded(chart_client):
    client, state = chart_client
    state.native.append(native_chart(chart_key='nc-' + '2' * 32))
    data = client.post('/api/sn/charts/import/native/preview', json={'offset': 0, 'limit': 1}).json()
    assert data['next_offset'] == 1 and data['page_count'] == 1
    assert client.post('/api/sn/charts/import/native/preview', json={'limit': 201}).status_code == 422
    assert client.post('/api/sn/cycle-templates', json={'kind': 'repeat', 'name': '坏起点', 'start': '02-30', 'period': 'year'}).status_code == 422
    assert client.post('/api/sn/cycle-templates', json={'kind': 'event', 'name': '双日历', 'calendar': 'spring_festival',
        'anchors': [{'label': '混用', 'date': '2024-01-01'}], 'before_days': 1, 'after_days': 1}).status_code == 422


def test_shared_charts_in_legacy_result_storage_remain_inside_their_workbench(chart_client):
    client, _ = chart_client
    headers = {'x-workspace-mode': 'legacy'}
    created = client.post('/api/sn/charts', json=definition(), headers=headers)
    assert created.status_code == 201, created.text
    identifier = created.json()['id']
    assert client.get('/api/sn/charts/' + identifier, headers={**headers, 'x-actor': 'second'}).status_code == 200
    foreign = {**headers, 'x-workbench': SECOND_WB}
    assert client.get('/api/sn/charts', headers=foreign).json()['total'] == 0
    assert client.get('/api/sn/charts/' + identifier, headers=foreign).status_code == 404
    assert client.get('/api/sn/charts/' + identifier + '/revisions', headers=foreign).status_code == 404
    assert client.delete('/api/sn/charts/' + identifier + '?revision=1', headers=foreign).status_code == 404


def test_viewer_native_preview_passes_the_actual_scoped_auth_middleware(chart_client, monkeypatch):
    from tin.integration import auth
    client, state = chart_client
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://fixture.invalid')
    monkeypatch.setattr(settings, 'public_prefix', '')
    viewer = Identity('first', 'fixture@test', 'Fixture', 17, 'viewer')

    async def verify(*args, **kwargs):
        return viewer

    async def resolve(identity, identifier, authorization):
        return WorkbenchScope(identity, identifier, '', 0, {'workspace_mode': 'isolated', 'can_edit': False},
                              authorization, scope_version=2, sources_revision=7, sources=(source(1),))

    monkeypatch.setattr(auth, 'verify_bearer', verify)
    monkeypatch.setattr(workbench_scope, 'resolve_workbench', resolve)
    state.app.add_middleware(auth.ResearchAuthMiddleware)
    headers = {'authorization': 'Bearer fixture', 'origin': 'http://testserver', 'x-role': 'viewer', 'x-can-edit': 'false'}
    root = '/workbenches/' + WB
    response = client.post(root + '/api/sn/charts/import/native/preview', json={}, headers=headers)
    assert response.status_code == 200, response.text
    assert client.post(root + '/api/sn/charts/import/native/apply',
        json={'sheets': [{'knowledge_id': 'fixture-document', 'sheet': '测试表'}]}, headers=headers).status_code == 403


def test_dates_cannot_be_coerced_from_timestamps_or_noncanonical_strings(chart_client):
    client, _ = chart_client
    for value in (123, '2023-1-1', '2023-01-01T00:00:00'):
        assert client.post('/api/sn/charts', json=definition(time={'mode': 'since', 'start': value})).status_code == 422


def test_cycle_time_defaults_to_the_templates_transform(chart_client):
    client, _ = chart_client
    template = {'kind': 'repeat', 'name': '起点变化', 'start': '01-01', 'period': 'year', 'transform': 'change'}
    saved = create(client, time={'mode': 'cycle', 'template': template})
    assert saved['definition']['time']['transform'] == 'change'
    result = client.get('/api/sn/charts/' + saved['id'] + '/data?date=2023-01-03').json()
    assert result['segments'][0]['points'][-1]['value'] == 2


def test_segment_styles_round_trip_through_create_update_reads_and_revisions(chart_client):
    client, _ = chart_client
    template = {'kind': 'repeat', 'name': '年度', 'start': '01-01', 'period': 'year'}
    styles = [{'label': '原样标签' + str(index), 'color': '#Ab12Cd' if index % 2 else None,
               'width': 1.25 + index, 'dash': ['solid', 'dash', 'dot'][index % 3]} for index in range(40)]
    saved = create(client, time={'mode': 'cycle', 'template': template, 'segment_styles': styles})
    path = '/api/sn/charts/' + saved['id']
    assert saved['definition']['time']['segment_styles'] == styles
    assert client.get(path).json()['definition']['time']['segment_styles'] == styles
    changed_styles = [{'label': ' 2023 ', 'color': None, 'width': None, 'dash': None}]
    changed = client.patch(path, json={'revision': 1, 'time': {**saved['definition']['time'], 'segment_styles': changed_styles}})
    assert changed.status_code == 200, changed.text
    assert changed.json()['definition']['time']['segment_styles'] == changed_styles
    history = [ChartDefinition.model_validate(row['definition']).model_dump(mode='json')
               for row in client.get(path + '/revisions').json()['items']]
    # Artifact history omits null fields; the schema restores their defaults.
    assert history[0]['time']['segment_styles'] == changed_styles
    assert history[1]['time']['segment_styles'] == styles


@pytest.mark.parametrize('styles', [
    [{'label': str(index)} for index in range(41)], [{'label': ''}], [{'label': '长' * 41}],
    [{'label': '段', 'color': 'red'}],
    [{'label': '段', 'color': '#12345'}], [{'label': '段', 'color': '#1234567'}],
    [{'label': '段', 'color': '#12zz56'}], [{'label': '段', 'width': 0}],
    [{'label': '段', 'width': 101}], [{'label': '段', 'dash': 'unknown'}],
    [{'label': '段', 'opacity': 0.5}],
])
def test_segment_style_limits_and_values_are_validated_on_create_and_update(chart_client, styles):
    client, _ = chart_client
    time = {'mode': 'cycle', 'template': {'kind': 'repeat', 'name': '年度', 'start': '01-01', 'period': 'year'},
            'segment_styles': styles}
    assert client.post('/api/sn/charts', json=definition(time=time)).status_code == 422
    saved = create(client)
    assert client.patch('/api/sn/charts/' + saved['id'], json={'revision': 1, 'time': time}).status_code == 422
    assert client.get('/api/sn/charts/' + saved['id']).json()['revision'] == 1


def test_imported_cycle_keeps_metric_warning_names_segment_styles_and_empty_segment_diagnostics(chart_client):
    client, state = chart_client
    state.fields[SID]['unit'] = ''
    state.native = [yearly_native()]
    for index, curve in enumerate(state.native[0]['curves']):
        curve['label'] = str(2020 + index)
        curve['style'] = {'color': ['#112233', '#445566', '#778899'][index],
                          'width_pt': index + 1.5, 'dash': ['solid', 'dot', 'dash'][index]}
    preview = client.post('/api/sn/charts/import/native/preview', json={}).json()['sheets'][0]
    assert preview['summary'] == '本表 1 张：1 张周期图（起点 01-01 1 张），0 张走势图，0 张无法转换'
    assert next(w for w in preview['charts'][0]['warnings'] if w['code'] == 'unit_missing')['label'] == FIELDS[SID]['metric']
    response = client.post('/api/sn/charts/import/native/apply',
                           json={'sheets': [{'knowledge_id': 'fixture-document', 'sheet': '测试表'}]})
    assert response.status_code == 200, response.text
    saved = response.json()['created'][0]
    assert saved['definition']['curves'][0]['label'] == FIELDS[SID]['metric']
    assert saved['definition']['time']['segment_styles'] == [
        {'label': c['label'], 'color': c['style']['color'], 'width': c['style']['width_pt'], 'dash': c['style']['dash']}
        for c in state.native[0]['curves']]
    record = client.get('/api/sn/charts/' + saved['id']).json()
    assert record['warnings'][0]['label'] == FIELDS[SID]['metric']
    data = client.get('/api/sn/charts/' + saved['id'] + '/data?date=2026-11-06').json()
    assert [s['label'] for s in data['segments']] == ['2023']
    assert data['omitted_segments'] == ['2024', '2025', '2026']
    assert 'empty_segments_omitted' in data['warnings']
    assert data['notices'][0]['label'] == FIELDS[SID]['metric']
