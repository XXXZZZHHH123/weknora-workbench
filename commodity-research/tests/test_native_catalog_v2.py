"""Real native catalog/detail handlers with an isolated structured-service transport."""
from datetime import datetime
import json
import re

import httpx
from fastapi import FastAPI
from fastapi.testclient import TestClient
from lxml.html import fromstring
import pytest

from tin.config import SHANGHAI
from tin.integration.identity import current_identity
from tin.integration.workbench_scope import WorkbenchScope, current_workbench
from tin.models import Indicator
from tin.ingest.record import record
from tin.schemas.caliber import Caliber
from tin.schemas.observation import ObservationIn
from tin.web import app as web
from tests.test_workbench_sources_v2 import IDENTITY, WB, source


@pytest.fixture
def native_catalog_client(monkeypatch):
    direct = source(1)
    scope = WorkbenchScope(IDENTITY, WB, '', 0, {'scope_version': 2}, 'Bearer fixture',
                           scope_version=2, sources_revision=7, sources=(direct,))
    captured = []
    identifier = 'kb-' + 'a' * 48

    def transport(method, endpoint, body, **kwargs):
        assert method == 'POST' and endpoint == '/api/research/facts'
        assert body['catalog_only'] is True and body['limit'] <= 200
        assert body['source_bindings'] == [direct['id']]
        assert 'points_per_series' not in body and 'observations' not in body
        captured.append(body)
        field = {'series_id': identifier, 'metric': 'Fixture metric', 'name': 'Fixture metric',
                 'dimensions': {}, 'unit': '件', 'frequency': '日', 'source_file': 'Fixture workbook',
                 'source_kind': 'industry', 'source_binding_id': direct['id'],
                 'knowledge_base_id': direct['knowledge_base_id'], 'knowledge_base_tenant_id': 17}
        payload = {'scope_version': 2, 'workbench_id': WB, 'sources_revision': 7,
                   'version': 'f' * 64, 'catalog_only': True, 'series': [field], 'datasets': [],
                   'offset': body['offset'], 'total': 201, 'next_offset': None, 'truncated': False}
        return httpx.Response(200, json=payload, request=httpx.Request(method, 'http://fixture.invalid/api/research/facts'))

    monkeypatch.setattr('tin.provenance.bridge.structured_request', transport)
    app = FastAPI()
    app.add_api_route('/api/sn/indicators/catalog', web.indicator_catalog, methods=['GET'])

    @app.middleware('http')
    async def fixture_scope(request, call_next):
        identity_token = current_identity.set(IDENTITY)
        scope_token = current_workbench.set(scope)
        try:
            return await call_next(request)
        finally:
            current_workbench.reset(scope_token)
            current_identity.reset(identity_token)

    return TestClient(app), captured, identifier


def test_historical_catalog_page_renders_date_for_charts_and_detail_links(native_catalog_client):
    client, requests, identifier = native_catalog_client
    response = client.get('/api/sn/indicators/catalog', params={
        'date': '2026-01-02', 'offset': 200, 'limit': 200, 'search': '[literal] %'})
    assert response.status_code == 200
    data = response.json()
    assert data['offset'] == 200 and data['total'] == 201
    assert requests[0]['search'] == '[literal] %', 'Literal metadata evidence must reach the real catalog protocol unchanged'
    html = fromstring(data['html'])
    assert html.xpath('//button[@data-series-chart]/@data-chart-date') == ['2026-01-02']
    links = html.xpath('//a/@href')
    assert links and all(link.endswith('/sn/indicators/' + identifier + '?date=2026-01-02') for link in links)
    assert fromstring(data['nav_html']).xpath('//*[@data-jump-total]/text()') == ['1']


def test_invalid_catalog_date_does_not_fetch_any_metadata(native_catalog_client):
    client, requests, _ = native_catalog_client
    assert client.get('/api/sn/indicators/catalog?date=not-a-date').status_code == 400
    assert requests == []


def test_native_indicator_detail_respects_explicit_history_cutoff(loaded, monkeypatch):
    identifier = 'FIXTURE.CATALOG.HISTORY'
    loaded.add(Indicator(series_id=identifier, name='Fixture history metric', variety='SN', category='Fixture',
                         caliber={'time_type': '发布时点'}, unit='件', source='Fixture', frequency='日',
                         fetch_mode='manual', status='可用'))
    loaded.flush()
    for day, value in ((17, 10), (18, 20), (21, 900)):
        record(loaded, ObservationIn(series_id=identifier, value=value,
               as_of=datetime(2026, 9, day, 15, tzinfo=SHANGHAI),
               caliber=Caliber(time_type='发布时点'), source='Fixture',
               entered_by='fixture-researcher', note='Isolated historical cutoff fixture'))
    loaded.commit()
    monkeypatch.setattr(web, 'SessionLocal', lambda: loaded)
    app = FastAPI()
    app.add_api_route('/sn/indicators/{series_id}', web.indicator_page, methods=['GET'])
    response = TestClient(app).get('/sn/indicators/' + identifier + '?date=2026-09-18')
    assert response.status_code == 200
    detail = json.loads(re.search(r'window\.IND = (.*?);', response.text).group(1))
    assert [point['value'] for point in detail['history']] == [10, 20]
    assert all(point['as_of'] <= '2026-09-18' for point in detail['history'])
