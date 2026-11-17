"""Fifty-card request timings over synthetic daily history, without sockets.

Run with ``PYTHONPATH=src python -m pytest -q -s
tests/test_dimension_data_performance.py``. Only upstream HTTP transmission is
mocked: routing, client/TLS initialization, signing, JSON, verified bounded
reads and cycle calculations execute normally. Three independent requests are
reported per latency case; timing is not asserted because host load varies.
"""
import base64
from collections import Counter
from datetime import date, timedelta
import json
from statistics import median
from time import perf_counter, sleep

import httpx
import pytest

from tin.config import settings
from tin.provenance import bridge
from tests.test_chart_api import chart_client
from tests.test_dimension_data import bind


REAL_STRUCTURED_REQUEST = bridge.structured_request
REAL_CLIENT = httpx.Client


def _seed(state):
    day = date(2024, 3, 29)
    start = date(2014, 1, 1)
    dates = [(start + timedelta(days=index)).isoformat()
             for index in range((day - start).days + 1)]
    identifiers = ['kb-' + f'{index + 1:048x}' for index in range(50)]
    state.fields = {sid: {'series_id': sid, 'metric': f'合成日度指标 {index + 1}',
                          'unit': '件', 'frequency': '日', 'source_version': 'e' * 64}
                    for index, sid in enumerate(identifiers)}
    state.observations = {sid: [(stamp, 100.0 + index + offset / 1000)
                               for offset, stamp in enumerate(dates)]
                          for index, sid in enumerate(identifiers)}
    # Stay within the real configuration's section/card limits as well.
    bind(state, [{'id': 'supply', 'label': '供给', 'category': '供给',
                  'description': '', 'sections': [
                      {'id': f'section-{group}', 'label': f'合成环节 {group + 1}',
                       'items': [{'kind': 'indicator', 'id': sid}
                                 for sid in identifiers[group * 10:(group + 1) * 10]]}
                      for group in range(5)]}])
    return day, identifiers


@pytest.mark.parametrize('latency_ms', [0, 5])
def test_fifty_daily_cards_keep_five_years_and_reuse_request_resources(
        chart_client, monkeypatch, tmp_path, latency_ms):
    client, state = chart_client
    day, identifiers = _seed(state)
    token = tmp_path / 'synthetic-service-token'
    token.write_bytes(b'synthetic-dimension-fixture-key-not-a-real-secret')
    monkeypatch.setattr(settings, 'service_token_file', token)
    monkeypatch.setattr(settings, 'structured_data_url', 'http://synthetic.invalid')
    synthetic_service = bridge.structured_request
    assert synthetic_service is not REAL_STRUCTURED_REQUEST
    created_clients, closed_transports, rounds = [], [], []

    class MeasuredTransport(httpx.MockTransport):
        def close(self):
            closed_transports.append(self)
            super().close()

    for round_index in range(3):
        state.requests.clear()
        upstream_points = {}
        metrics = {'round': round_index + 1, 'latency_ms': latency_ms,
                   'client_seconds': 0.0, 'upstream_seconds': 0.0}
        request_clients, request_transports = [], []

        def handle(request):
            assert request.url.host == 'synthetic.invalid'
            assert request.headers['Authorization'] == 'Bearer fixture'
            caller = json.loads(base64.urlsafe_b64decode(
                request.headers['X-Research-Identity'] + '==='))
            assert caller['user_id'] == 'first' and caller['tenant_id'] == 17
            assert caller['scope_version'] == 2 and caller['workbench_id']
            assert request.headers['X-Research-Signature']
            body = json.loads(request.content)
            started = perf_counter()
            if latency_ms:
                sleep(latency_ms / 1000)
            response = synthetic_service(request.method, request.url.path, body,
                                         return_error_response=True)
            metrics['upstream_seconds'] += perf_counter() - started
            response.request = request
            if not body.get('catalog_only'):
                for row in response.json()['rows']:
                    upstream_points.setdefault(row['series_id'], []).append(row['date'])
            return response

        def make_client(*args, **kwargs):
            started = perf_counter()
            result = REAL_CLIENT(*args, **kwargs)
            # Include the real TLS initialization, but never send or resolve
            # anything through its original transport.
            result._transport.close()
            transport = MeasuredTransport(handle)
            result._transport = transport
            metrics['client_seconds'] += perf_counter() - started
            created_clients.append(result)
            request_clients.append(result)
            request_transports.append(transport)
            return result

        with monkeypatch.context() as patch:
            patch.setattr(httpx, 'Client', make_client)
            patch.setattr(bridge, 'structured_request', REAL_STRUCTURED_REQUEST)
            started = perf_counter()
            response = client.get('/dimension-data-test', params={'day': day.isoformat()})
            metrics['seconds'] = perf_counter() - started
            assert response.status_code == 200, response.text

        payload = response.json()
        assert payload['failed'] == []
        cards = [item for dimension in payload['dimensions']
                 for section in dimension['sections'] for item in section['items']]
        assert [item['id'] for item in cards] == identifiers
        assert all(item['point_limit'] == 2000 for item in cards)
        assert all(item['position']['n'] == 5 and item['position']['rank'] is not None
                   for item in cards)
        assert set(upstream_points) == set(identifiers)
        assert all(len(points) == 2000 and (day - date.fromisoformat(min(points))).days > 5 * 365
                   for points in upstream_points.values())
        reads = [body for _, body in state.requests if 'points_per_series' in body]
        assert len(reads) == 5
        assert all(len(body['series_ids']) <= 256 and
                   len(body['series_ids']) * body['points_per_series'] <= 20000
                   for body in reads)
        catalog_groups = Counter(tuple(body['series_ids']) for _, body in state.requests
                                 if body.get('catalog_only'))
        assert catalog_groups and max(catalog_groups.values()) == 1
        assert len(request_clients) == len(request_transports) == 1
        assert request_clients[0].is_closed
        assert closed_transports.count(request_transports[0]) == 1
        # Keep all clients alive, so object identity reliably proves that no
        # connection pool survives from one independent request to the next.
        assert len({id(value) for value in created_clients}) == round_index + 1
        metrics.update(clients=len(request_clients), catalog_requests=sum(catalog_groups.values()),
                       bounded_requests=len(reads), points_read=sum(map(len, upstream_points.values())))
        rounds.append(metrics)
        print('\nDIMENSION_FIFTY_REQUEST ' + json.dumps(metrics, sort_keys=True), flush=True)

    assert len(closed_transports) == 3
    print('\nDIMENSION_FIFTY_SYNTHETIC ' + json.dumps({
        'indicators': 50, 'history_start': '2014-01-01', 'as_of': day.isoformat(),
        'points_each': 2000, 'latency_ms': latency_ms,
        'median_seconds_3_runs': median(item['seconds'] for item in rounds),
        'range_seconds': [min(item['seconds'] for item in rounds), max(item['seconds'] for item in rounds)]
    }, sort_keys=True), flush=True)
