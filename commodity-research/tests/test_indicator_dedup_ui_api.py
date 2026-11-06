"""Real routes/templates and space decisions, with only source transport isolated."""
from dataclasses import replace

from fastapi import FastAPI
from fastapi.testclient import TestClient

from tin.integration.identity import current_identity
from tin.integration.workbench_scope import current_workbench
from tin.integration import page_facts
from tin.web import app as web, indicator_dedup as api
from tests.test_indicator_dedup import setup, OWN, DEFAULT  # noqa: F401


def client_for(scope, state, monkeypatch):
    def raw(actual, offset=0, limit=200, search=None, series_ids=None, *, version=None, source_kind=None):
        rows = [field for field in state['fields'] if series_ids is None or field['series_id'] in series_ids]
        return {'series': rows[offset:offset+limit], 'version': 'a' * 64, 'datasets': [],
                'total': len(rows), 'offset': offset, 'next_offset': None, 'truncated': False,
                'unavailable_series_ids': []}
    monkeypatch.setattr(page_facts, '_raw_catalog_page', raw)
    app = FastAPI()
    app.include_router(api.router)
    app.add_api_route('/sn/indicator-dedup', web.indicator_dedup_page)
    app.add_api_route('/api/sn/indicators/catalog', web.indicator_catalog)
    @app.middleware('http')
    async def context(request, call_next):
        identity = current_identity.set(scope.identity)
        marker = current_workbench.set(scope)
        try:
            return await call_next(request)
        finally:
            current_workbench.reset(marker)
            current_identity.reset(identity)
    return TestClient(app)


def test_real_review_routes_confirm_filter_and_undo_without_automatic_rewrite(setup, monkeypatch):
    scope, state = setup
    scope = replace(scope, metadata={**scope.metadata, 'name': 'Synthetic review'})
    client = client_for(scope, state, monkeypatch)
    page = client.get('/sn/indicator-dedup')
    assert page.status_code == 200 and '指标对应确认' in page.text and 'indicator-dedup.js' in page.text
    item = client.get('/api/sn/indicator-dedup').json()['items'][0]
    catalog = client.get('/api/sn/indicators/catalog?format=metadata').json()
    assert len(catalog['indicators']) == 2 and all(row['dedup_status'] == 'pending' for row in catalog['indicators'])
    assert not state.get('value_reads'), 'opening a catalog/review shell reads metadata only'
    verified = client.post(f"/api/sn/indicator-dedup/{item['id']}/verify", json={'sources_revision': 7}).json()
    saved = client.post('/api/sn/indicator-dedup/decisions', json={'ids': [item['id']],
        'revisions': {item['id']: verified['revision']}, 'state': 'confirmed_default', 'batch': True, 'sources_revision': 7})
    assert saved.status_code == 200
    catalog = client.get('/api/sn/indicators/catalog').json()
    assert [row['series_id'] for row in catalog['indicators']] == [DEFAULT]
    assert '已与个人库对应' in catalog['html'] and catalog['total'] == 1
    assert state['fields'][0]['series_id'] == OWN, 'the personal copy is retained unchanged'
    revision = saved.json()['items'][0]['revision']
    assert client.post(f"/api/sn/indicator-dedup/{item['id']}/undo", json={'sources_revision': 7, 'revision': revision}).status_code == 200
    assert client.get('/api/sn/indicators/catalog?format=metadata').json()['total'] == 2


def test_read_only_and_restricted_review_routes_enforce_current_scope(setup, monkeypatch):
    scope, state = setup
    scope = replace(scope, metadata={**scope.metadata, 'name': 'Synthetic review'}, identity=replace(scope.identity, role='viewer'))
    client = client_for(scope, state, monkeypatch)
    listing = client.get('/api/sn/indicator-dedup').json()
    assert listing['can_confirm'] is False
    item = listing['items'][0]
    denied = client.post('/api/sn/indicator-dedup/decisions', json={'ids': [item['id']],
        'revisions': {item['id']: item['revision']}, 'state': 'keep_both', 'batch': False, 'sources_revision': 7})
    assert denied.status_code == 403
    assert client.post(f"/api/sn/indicator-dedup/{item['id']}/verify", json={'sources_revision': 7}).status_code == 200
    restricted = replace(scope, sources=({**scope.sources[0], 'delegated': True}, scope.sources[1]))
    client = client_for(restricted, state, monkeypatch)
    assert client.get('/sn/indicator-dedup').status_code == 403
    assert client.get('/api/sn/indicator-dedup').status_code == 403
