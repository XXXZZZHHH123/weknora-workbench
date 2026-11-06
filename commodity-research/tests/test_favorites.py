"""Account watchlists against real scoped result stores; external reads are mocked."""
import json
from types import SimpleNamespace
from urllib.parse import parse_qs, urlsplit

from fastapi import HTTPException
from fastapi.encoders import jsonable_encoder
from pydantic import ValidationError
import pytest
from sqlalchemy import select

from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, current_workbench, isolated_session_factory
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision
from tin.schemas.chart import ChartDefinition
from tin.web import charts, favorites
from tests.chart_fixtures import FIELDS, OTHER, SID, definition
from tests.test_chart_api import SECOND_WB, chart_client
from tests.test_workbench_sources_v2 import WB, source


class DirectClient:
    """Invoke real route functions without TestClient's background socket wakeups."""
    def request(self, method, path, *, json=None, headers=None):
        headers = headers or {}
        identity = Identity(headers.get('x-actor', 'first'), 'fixture@test', 'Fixture', int(headers.get('x-tenant', '17')),
                            headers.get('x-role', 'owner'))
        scope = WorkbenchScope(identity, headers.get('x-workbench', WB), '', 0,
            {'workspace_mode': headers.get('x-workspace-mode', 'isolated'),
             'can_edit': headers.get('x-can-edit', 'true') == 'true'},
            'Bearer fixture', scope_version=int(headers.get('x-scope-version', '2')), sources_revision=7,
            sources=(source(1, headers.get('x-restricted') == 'true'),))
        actor = current_identity.set(identity)
        marker = current_workbench.set(None if headers.get('x-local') == 'true' else scope)
        parsed = urlsplit(path)
        try:
            if parsed.path == '/api/sn/favorites':
                result = favorites.get_favorites() if method == 'GET' else favorites.put_favorites(favorites.FavoritesInput.model_validate(json))
            elif method == 'POST' and parsed.path == '/api/sn/charts':
                result = charts.create_chart(ChartDefinition.model_validate(json))
            elif method == 'DELETE' and parsed.path.startswith('/api/sn/charts/'):
                result = charts.delete_chart(parsed.path.rsplit('/', 1)[1], int(parse_qs(parsed.query)['revision'][0]), None)
            else:
                raise AssertionError((method, path))
            status = 200
        except HTTPException as error:
            result, status = {'detail': error.detail}, error.status_code
        except ValidationError as error:
            result, status = {'detail': str(error)}, 422
        finally:
            current_workbench.reset(marker)
            current_identity.reset(actor)
        value = jsonable_encoder(result)
        return SimpleNamespace(status_code=status, json=lambda: value, text=str(value))

    def get(self, path, **kwargs):
        return self.request('GET', path, **kwargs)

    def put(self, path, **kwargs):
        return self.request('PUT', path, **kwargs)

    def post(self, path, **kwargs):
        return self.request('POST', path, **kwargs)

    def delete(self, path, **kwargs):
        return self.request('DELETE', path, **kwargs)


@pytest.fixture
def client(chart_client):
    _, state = chart_client
    state.app.include_router(favorites.router)
    return DirectClient(), state


def put(client, items, revision=0, **kwargs):
    return client.put('/api/sn/favorites', json={'revision': revision, 'items': items}, **kwargs)


def indicator(identifier=SID, **extra):
    return {'kind': 'indicator', 'id': identifier, **extra}


def test_favorites_methods_registered_in_real_application():
    from tin.web import app as web
    from tests.test_chart_pages import endpoints
    routes = [route for route in endpoints(web.app.routes) if getattr(route, 'path', None) == '/api/sn/favorites']
    assert {method for route in routes for method in route.methods} == {'GET', 'PUT'}


def test_account_workbench_isolation_revision_order_and_stored_format(client):
    client, state = client
    assert client.get('/api/sn/favorites').json() == {'format': 'favorites.v1', 'revision': 0, 'items': []}
    response = put(client, [indicator(), indicator(OTHER)], headers={'x-can-edit': 'false'})
    assert response.status_code == 200, response.text
    saved = response.json()
    assert saved['revision'] == 1 and [item['id'] for item in saved['items']] == [SID, OTHER]
    assert all(item['added_at'] and item['status'] == 'active' for item in saved['items'])
    assert saved['items'][0]['title'] == FIELDS[SID]['metric']
    assert client.get('/api/sn/favorites', headers={'x-actor': 'second'}).json()['items'] == []
    assert client.get('/api/sn/favorites', headers={'x-workbench': SECOND_WB}).json()['items'] == []
    assert put(client, [indicator(OTHER)]).status_code == 409
    reordered = put(client, [indicator(OTHER), indicator(added_at='2000-01-01T00:00:00Z')], 1)
    assert reordered.status_code == 200 and reordered.json()['revision'] == 2
    assert reordered.json()['items'][1]['added_at'] == saved['items'][0]['added_at']
    with isolated_session_factory(17, WB)() as session:
        rows = session.scalars(select(WorkbenchArtifact).where(WorkbenchArtifact.module == 'favorites')).all()
        assert len(rows) == 1 and rows[0].actor == 'first'
        stored = json.loads(rows[0].body)
        assert stored['format'] == 'favorites.v1'
        assert set(stored['items'][0]) == {'kind', 'id', 'added_at'}
        assert session.get(WorkbenchArtifactRevision, (rows[0].id, 2)) is not None
    assert all(body.get('catalog_only') for _, body in state.requests)


def test_legacy_shared_database_also_isolates_each_account_and_workbench(client):
    client, _ = client
    headers = {'x-workspace-mode': 'legacy'}
    assert put(client, [indicator()], headers=headers).status_code == 200
    assert client.get('/api/sn/favorites', headers=headers).json()['items'][0]['id'] == SID
    for other in ({'x-actor': 'second'}, {'x-workbench': SECOND_WB}):
        alternate = {**headers, **other}
        assert client.get('/api/sn/favorites', headers=alternate).json()['revision'] == 0
        assert put(client, [indicator(OTHER)], headers=alternate).status_code == 200
    assert client.get('/api/sn/favorites', headers=headers).json()['items'][0]['id'] == SID


def test_same_actor_identity_in_another_tenant_has_an_independent_watchlist(client):
    client, _ = client
    assert put(client, [indicator()]).status_code == 200
    other = {'x-tenant': '18'}
    assert client.get('/api/sn/favorites', headers=other).json()['revision'] == 0
    assert put(client, [indicator(OTHER)], headers=other).status_code == 200
    assert client.get('/api/sn/favorites').json()['items'][0]['id'] == SID
    assert client.get('/api/sn/favorites', headers=other).json()['items'][0]['id'] == OTHER


def test_validates_chart_scope_and_retains_missing_items_until_explicit_removal(client):
    client, state = client
    saved = client.post('/api/sn/charts', json=definition()).json()
    chart = {'kind': 'chart', 'id': saved['id']}
    assert put(client, [chart], headers={'x-workbench': SECOND_WB}).status_code == 422
    assert put(client, [indicator(), chart]).status_code == 200
    state.fields.pop(SID)
    assert client.delete('/api/sn/charts/' + saved['id'] + '?revision=1').status_code == 200
    invalid = client.get('/api/sn/favorites').json()
    assert [item['status'] for item in invalid['items']] == ['invalid', 'invalid']
    assert put(client, [chart, indicator()], 1).status_code == 200, 'Existing invalid entries can be reordered'
    assert put(client, [chart], 2).status_code == 200, 'One invalid entry can be removed without discarding the other'
    assert put(client, [], 3).status_code == 200
    assert put(client, [indicator()], 4).status_code == 422, 'Invalid entries cannot be newly inserted'
    assert put(client, [chart], 4).status_code == 422
    assert client.get('/api/sn/favorites').json()['items'] == []


def test_full_watchlist_is_100_unique_items_and_never_truncates(client):
    client, state = client
    state.fields = {('kb-' + f'{index:048x}'): {**FIELDS[SID], 'series_id': 'kb-' + f'{index:048x}'} for index in range(101)}
    items = [indicator(identifier) for identifier in state.fields]
    response = put(client, items[:100])
    assert response.status_code == 200 and len(response.json()['items']) == 100
    assert put(client, items, 1).status_code == 422
    assert put(client, [items[0], items[0]], 1).status_code == 422
    assert len(client.get('/api/sn/favorites').json()['items']) == 100


@pytest.mark.parametrize('headers,status', [({'x-restricted': 'true'}, 403), ({'x-scope-version': '1'}, 409),
                                         ({'x-local': 'true'}, 409)])
def test_scope_rejections_do_not_read_or_write_sources(client, headers, status):
    client, state = client
    assert client.get('/api/sn/favorites', headers=headers).status_code == status
    assert put(client, [indicator()], headers=headers).status_code == status
    assert not state.requests


def test_viewer_reads_but_cannot_write_personal_research_and_non_editor_can_write(client):
    client, _ = client
    assert client.get('/api/sn/favorites', headers={'x-role': 'viewer'}).status_code == 200
    assert put(client, [], headers={'x-role': 'viewer'}).status_code == 403
    assert put(client, [indicator()], headers={'x-can-edit': 'false'}).status_code == 200


@pytest.mark.parametrize('body', [
    {'items': []}, {'revision': -1, 'items': []}, {'revision': True, 'items': []},
    {'revision': 0, 'items': [], 'actor': 'second'},
    {'revision': 0, 'items': [{'kind': 'unknown', 'id': SID}]},
    {'revision': 0, 'items': [indicator('../unsafe')]},
    {'revision': 0, 'items': [indicator('missing')]},
    {'format': 'unknown', 'revision': 0, 'items': []},
])
def test_schema_validation(client, body):
    client, _ = client
    assert client.put('/api/sn/favorites', json=body).status_code == 422
    assert client.get('/api/sn/favorites').json()['revision'] == 0


def test_unverified_catalog_response_is_rejected_without_saving_or_marking_invalid(client):
    client, state = client
    assert put(client, [indicator()]).status_code == 200
    state.on_catalog = lambda payload: payload.update(sources_revision=8)
    assert client.get('/api/sn/favorites').status_code == 503
    assert put(client, [indicator(OTHER)], 1).status_code == 503
    state.on_catalog = None
    saved = client.get('/api/sn/favorites').json()
    assert saved['revision'] == 1 and saved['items'][0]['status'] == 'active'
