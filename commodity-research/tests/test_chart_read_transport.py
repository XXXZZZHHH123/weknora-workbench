"""Request-local connection/metadata reuse retains fresh identity and source checks."""
import base64
from dataclasses import replace
import hashlib
import hmac
import json
from types import SimpleNamespace

import httpx
import pytest
from fastapi import HTTPException

from tin.integration import page_facts
from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, current_workbench
from tin.provenance import bridge
from tests.chart_fixtures import FIELDS, SID, OTHER, THIRD
from tests.test_chart_api import chart_client, create
from tests.test_chart_anomalies import cycle_input, anomalies
from tests.test_dimension_data import bind, dimensions
from tests.test_workbench_sources_v2 import IDENTITY, WB, source


def test_signed_transport_pools_connections_without_sharing_headers_or_cookies(tmp_path, monkeypatch):
    key = b'synthetic-signing-key-for-test-only-' * 2
    token = tmp_path / 'service-key'
    token.write_bytes(key)
    monkeypatch.setattr(bridge.settings, 'service_token_file', token)
    monkeypatch.setattr(bridge.settings, 'structured_data_url', 'http://fixture.invalid')
    constructors, requests, timeouts, clients = [], [], [], []
    original = httpx.Client

    def respond(request):
        requests.append(request)
        assert 'cookie' not in request.headers
        caller = request.headers['X-Research-Identity']
        message = '\n'.join((request.headers['X-Research-Timestamp'], request.method,
            request.url.path, caller, hashlib.sha256(request.content).hexdigest()))
        assert request.headers['X-Research-Signature'] == hmac.new(key, message.encode(), hashlib.sha256).hexdigest()
        return httpx.Response(200, json={}, headers={'set-cookie': 'ignored=private'})

    def factory(**options):
        constructors.append(options)
        client = original(**options, transport=httpx.MockTransport(respond))
        request = client.request
        def measured(*args, **kwargs):
            timeouts.append(kwargs['timeout'])
            return request(*args, **kwargs)
        client.request = measured
        clients.append(client)
        return client

    monkeypatch.setattr(bridge.httpx, 'Client', factory)
    actor = Identity('first', 'fixture@test', 'Fixture', 17, 'owner')
    for name in ('first', 'second'):
        identity = replace(actor, user_id=name)
        scope = SimpleNamespace(identity=identity, workbench_id='synthetic-workbench',
            authorization='Bearer ' + name, scope_version=2, sources_revision=7)
        identity_marker, scope_marker = current_identity.set(identity), current_workbench.set(scope)
        try:
            with bridge.structured_read_scope():
                bridge.structured_request('POST', '/api/research/facts', {'series_ids': []})
                bridge.structured_request('POST', '/api/research/facts', {}, timeout=2)
        finally:
            current_identity.reset(identity_marker)
            current_workbench.reset(scope_marker)
    assert constructors == [{'follow_redirects': False, 'trust_env': False}] * 2
    assert all(client.is_closed for client in clients)
    assert timeouts == [120, 2, 120, 2]
    assert [request.headers['Authorization'] for request in requests] == ['Bearer first'] * 2 + ['Bearer second'] * 2
    assert [json.loads(base64.urlsafe_b64decode(request.headers['X-Research-Identity'] + '=='))['user_id']
        for request in requests] == ['first'] * 2 + ['second'] * 2


@pytest.mark.parametrize('base', ['file:///tmp/fixture', 'http://user@fixture.invalid', 'http://fixture.invalid/path'])
def test_pooled_transport_still_rejects_untrusted_origin(base, monkeypatch):
    monkeypatch.setattr(bridge.settings, 'structured_data_url', base)
    marker = current_identity.set(Identity('first', 'fixture@test', 'Fixture', 17, 'owner'))
    try:
        with bridge.structured_read_scope(), pytest.raises(ValueError, match='地址配置无效'):
            bridge.structured_request('POST', '/api/research/facts', {})
    finally:
        current_identity.reset(marker)


def test_anomaly_directory_is_read_once_per_batch_and_never_reused_by_next_actor(chart_client):
    client, state = chart_client
    time, day = cycle_input(state, current=(99, 99))
    create(client, time=time)
    state.requests.clear()
    anomalies(client, day)
    catalog = [body for _, body in state.requests if body.get('catalog_only')]
    bounded = [body for _, body in state.requests if body.get('points_per_series')]
    assert len(catalog) == len(bounded) == 1
    state.requests.clear()
    response = client.get('/api/sn/charts/anomalies', params={'date': day}, headers={'x-actor': 'second'})
    assert response.status_code == 200
    assert len([body for _, body in state.requests if body.get('catalog_only')]) == 1
    assert len([body for _, body in state.requests if body.get('points_per_series')]) == 1


def test_catalog_and_bounded_source_version_change_rejects_the_read(chart_client):
    client, state = chart_client
    time, day = cycle_input(state, current=(99, 99))
    create(client, time=time)
    for field in state.fields.values():
        field['source_version'] = 'a' * 64
    # The fixture's bounded response advertises e*64: no stale metadata may be
    # silently combined with that newer source, even within one request.
    response = client.get('/api/sn/charts/anomalies', params={'date': day})
    assert response.status_code == 409
    assert '来源快照' in response.json()['detail']


@pytest.fixture
def subset_catalog(monkeypatch):
    binding = source(1)
    scope = WorkbenchScope(IDENTITY, WB, '', 0, {}, 'Bearer synthetic', scope_version=2,
                           sources_revision=7, sources=(binding,))
    state = SimpleNamespace(calls=[], first_override={})

    def request(active_scope, body):
        state.calls.append((active_scope, body))
        identifiers = body.get('series_ids', list(FIELDS))
        fields = [{**field, 'source_binding_id': binding['id'],
                   'knowledge_base_id': binding['knowledge_base_id'],
                   'knowledge_base_tenant_id': binding['tenant_id'], 'source_version': 'e' * 64}
                  for sid, field in FIELDS.items() if sid in identifiers]
        offset, limit = body['offset'], body['limit']
        selected = fields[offset:offset + limit]
        following = offset + len(selected) if offset + len(selected) < len(fields) else None
        result = {'catalog_only': True, 'series': selected, 'total': len(fields), 'datasets': [],
                  'offset': offset, 'next_offset': following, 'truncated': following is not None,
                  'version': 'f' * 64}
        if len(state.calls) == 1:
            result.update(state.first_override)
        return result

    monkeypatch.setattr(page_facts, '_request', request)
    return scope, state


def test_complete_explicit_catalog_projects_subsets_and_missing_ids_with_same_version(subset_catalog):
    scope, state = subset_catalog
    missing = 'kb-' + 'd' * 48
    with page_facts.catalog_read_scope():
        parent = page_facts.catalog_page(scope, series_ids=[SID, OTHER, missing], apply_dedup=False)
        child = page_facts.catalog_page(scope, series_ids=[missing, OTHER], apply_dedup=False,
                                        version=parent['version'])
        assert [field['series_id'] for field in child['series']] == [OTHER]
        assert child['total'] == 1 and child['offset'] == 0
        assert child['unavailable_series_ids'] == [missing]
        assert child['next_offset'] is None and not child['truncated']
        assert child['version'] == parent['version'] and child['datasets'] == parent['datasets']
        assert len(state.calls) == 1
        with pytest.raises(HTTPException) as changed:
            page_facts.catalog_page(scope, series_ids=[SID], version='a' * 64, apply_dedup=False)
        assert changed.value.status_code == 409
        assert len(state.calls) == 1
    with page_facts.catalog_read_scope():
        page_facts.catalog_page(scope, series_ids=[OTHER], apply_dedup=False)
    assert len(state.calls) == 2, 'Metadata may not survive into another request'


@pytest.mark.parametrize('change', ['bearer', 'actor'])
def test_subset_catalog_never_crosses_scope_object_identity(subset_catalog, change):
    scope, state = subset_catalog
    other_scope = (replace(scope, authorization='Bearer another-synthetic') if change == 'bearer' else
                   replace(scope, identity=replace(scope.identity, user_id='another-actor')))
    if change == 'bearer':
        assert scope == other_scope, 'Dataclass equality intentionally ignores authorization'
    with page_facts.catalog_read_scope():
        page_facts.catalog_page(scope, series_ids=[SID, OTHER], apply_dedup=False)
        page_facts.catalog_page(other_scope, series_ids=[SID], apply_dedup=False)
    assert len(state.calls) == 2
    assert state.calls[-1][0] is other_scope


@pytest.mark.parametrize('parent_options,child_options,override', [
    ({'search': ''}, {}, {}),
    ({'source_kind': 'industry'}, {}, {}),
    ({'offset': 1}, {}, {}),
    ({'series_ids': None}, {}, {}),
    ({}, {'search': ''}, {}),
    ({}, {'source_kind': 'industry'}, {}),
    ({}, {'offset': 1}, {}),
    ({}, {'limit': 1}, {}),
    ({}, {}, {'truncated': True}),
    ({}, {}, {'next_offset': 3}),
    ({}, {}, {'total': 4}),
    ({'series_ids': [SID]}, {'series_ids': [OTHER]}, {}),
])
def test_subset_catalog_requires_complete_unfiltered_explicit_parent_and_fitting_child(
        subset_catalog, parent_options, child_options, override):
    scope, state = subset_catalog
    state.first_override = override
    with page_facts.catalog_read_scope():
        page_facts.catalog_page(scope, **{'series_ids': [SID, OTHER, THIRD], **parent_options},
                                apply_dedup=False)
        page_facts.catalog_page(scope, **{'series_ids': [SID, OTHER], **child_options},
                                apply_dedup=False)
    assert len(state.calls) == 2


def test_mixed_frequency_dimension_reads_share_one_verified_directory(chart_client):
    client, state = chart_client
    for sid, frequency in ((SID, '日'), (OTHER, '周'), (THIRD, '月')):
        state.fields[sid]['frequency'] = frequency
        state.fields[sid]['source_version'] = 'e' * 64
    bind(state, dimensions(*({'kind': 'indicator', 'id': sid} for sid in (SID, OTHER, THIRD))))
    response = client.get('/dimension-data-test?day=2023-01-04')
    assert response.status_code == 200, response.text
    assert response.json()['failed'] == []
    catalogs = [body for _, body in state.requests if body.get('catalog_only')]
    bounded = [body for _, body in state.requests if body.get('points_per_series')]
    assert len(catalogs) == 1 and catalogs[0]['series_ids'] == [SID, OTHER, THIRD]
    assert len(bounded) == 3
    assert {body['points_per_series'] for body in bounded} == {2000, 316, 81}


def test_mixed_frequency_dimension_rejects_source_changed_after_parent_directory(chart_client):
    client, state = chart_client
    for sid, frequency in ((SID, '日'), (OTHER, '周'), (THIRD, '月')):
        state.fields[sid]['frequency'] = frequency
        state.fields[sid]['source_version'] = 'a' * 64

    def change_source_after_catalog(payload):
        # Parent fields advertise the old source; any reread would advertise
        # the new one. Subset reuse must retain the old stamp and reject the
        # fixture's bounded response, whose source stamp is e*64.
        for field in state.fields.values():
            field['source_version'] = 'e' * 64

    state.on_catalog = change_source_after_catalog
    bind(state, dimensions(*({'kind': 'indicator', 'id': sid} for sid in (SID, OTHER, THIRD))))
    response = client.get('/dimension-data-test?day=2023-01-04')
    assert response.status_code == 409, response.text
    assert '来源快照' in response.json()['detail']
    assert len([body for _, body in state.requests if body.get('catalog_only')]) == 1


@pytest.mark.parametrize('value', [None, True, False, 0, -7, 0.0, -0.0, 1e-300, 1e200,
    float('nan'), float('inf'), -float('inf'), '中文"\\\n', [1, '值'], {'列': [None, 1.5]}])
def test_projection_raw_value_json_matches_original_encoder(value):
    from tin.integration.kb_facts import _json_raw_value
    assert _json_raw_value(value) == json.dumps(value, ensure_ascii=False)


@pytest.mark.parametrize('cell', [None, 'M1', '列"\\\n', 23])
def test_projection_observation_ids_keep_original_json_bytes(session, cell):
    from tin.integration.kb_facts import _clear_projection, install_projection
    from tin.models import Observation
    from sqlalchemy import select
    identifier = 'kb-' + '1' * 48
    spec = {'series_id': identifier, 'source_knowledge_id': '原件"\\', 'dataset_id': '数据集',
        'source_series_id': '指标\n', 'metric': '合成指标', 'unit': '件'}
    row = {**spec, 'date': '2026-01-02', 'value': 1.5, 'value_status': 'valid',
        'source_cell': cell, 'raw_value': {'中文': ['原值', -0.0]}}
    page = {'version': 'a' * 64, 'indicators': [spec], 'datasets': [], 'rows': [row], 'total': 1}
    expected = int(hashlib.sha256(json.dumps([spec['source_knowledge_id'], spec['dataset_id'],
        spec['source_series_id'], cell, row['date']], ensure_ascii=False).encode()).hexdigest()[:15], 16)
    with session.get_bind().connect() as connection:
        try:
            install_projection(connection, SimpleNamespace(), pages=[page])
            assert connection.execute(select(Observation.id)).scalar_one() == expected
            raw = connection.exec_driver_sql('SELECT raw_value FROM temp._kb_evidence').scalar_one()
            assert raw == json.dumps(row['raw_value'], ensure_ascii=False)
        finally:
            _clear_projection(connection)
