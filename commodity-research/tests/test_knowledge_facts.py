"""All integrated research reads the bound KB, never stored research facts."""
from datetime import date, datetime, timezone
import base64
import hashlib
import hmac
import json
import time

import httpx
import pytest
from fastapi.testclient import TestClient
from sqlalchemy import func, select, text
from sqlalchemy.exc import DatabaseError

from tin.config import settings
from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, workbench_session
from tin.models import Indicator, Observation
from tin.web.app import app


FIRST = '10000000-0000-4000-8000-000000000011'
SECOND = '10000000-0000-4000-8000-000000000012'
LEGACY = '10000000-0000-4000-8000-000000000013'
KB = '20000000-0000-4000-8000-000000000011'
OTHER_KB = '20000000-0000-4000-8000-000000000012'
SERIES = 'kb-' + 'a' * 48


@pytest.fixture
def canonical(monkeypatch, tmp_path):
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://native.test')
    monkeypatch.setattr(settings, 'structured_data_url', 'http://structured-data:19082')
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    key = tmp_path / 'service-key'; key.write_bytes(b'k' * 48)
    monkeypatch.setattr(settings, 'service_token_file', key)
    source = {'id': 'source-one', 'knowledge_base_id': KB, 'tenant_id': 17,
              'file_hash': 'b' * 32, 'file_name': 'measurements.xlsx', 'file_type': 'xlsx',
              'parse_status': 'stored', 'enable_status': 'disabled'}
    state = {'sources': [source], 'ready': True, 'value': 17.5, 'revoked': False,
             'down': False, 'calls': [], 'version_change': False, 'pages': 0,
             'second_other_kb': False, 'paginate': False}
    spec = {'series_id': SERIES, 'source_knowledge_id': source['id'], 'dataset_id': 'd' * 24,
            'source_series_id': 'raw-column', 'metric': 'Measured quantity', 'unit': '吨',
            'frequency': '日', 'sheet': 'Measurements', 'date_mode': 'date', 'header_cells': ['B1'],
            'source_sha256': 'e' * 64, 'source_file': 'measurements.xlsx'}

    def upstream(request):
        state['calls'].append((request.method, request.url.path))
        path = request.url.path
        if request.url.host == 'native.test':
            if path == '/api/v1/auth/me':
                viewer = request.headers.get('authorization') == 'Bearer source-viewer'
                return httpx.Response(200, json={'success': True, 'data': {'user': {
                    'id': 'viewer' if viewer else 'researcher', 'email': 'researcher@test', 'username': 'Researcher'},
                    'tenant': {'id': 17}, 'memberships': [{'tenant_id': 17, 'role': 'viewer' if viewer else 'owner'}]}})
            if path.startswith('/api/v1/workspaces/research/'):
                identifier = path.rsplit('/', 1)[1]
                if identifier not in {FIRST, SECOND, LEGACY}:
                    return httpx.Response(404, json={'success': False})
                return httpx.Response(200, json={'success': True, 'data': {
                    'id': identifier, 'name': 'Research', 'scope_tenant_id': 17,
                    'knowledge_base_id': OTHER_KB if identifier == SECOND and state['second_other_kb'] else KB,
                    'workspace_mode': 'legacy' if identifier == LEGACY else 'isolated'}})
            if path in {'/api/v1/knowledge-bases/' + KB, '/api/v1/knowledge-bases/' + OTHER_KB}:
                return httpx.Response(403 if state['revoked'] else 200, json={'success': True,
                    'data': {'id': path.rsplit('/', 1)[1], 'tenant_id': 17, 'creator_id': 'researcher'}})
            if path == '/api/v1/knowledge-bases/' + KB + '/knowledge':
                return httpx.Response(200, json={'success': True, 'data': state['sources'], 'total': len(state['sources'])})
            if path == '/api/v1/knowledge-bases/' + OTHER_KB + '/knowledge':
                return httpx.Response(200, json={'success': True, 'data': [], 'total': 0})
        if request.url.host == 'structured-data' and path == '/api/research/facts':
            if state['down']:
                return httpx.Response(503, json={'detail': 'unavailable'})
            body = json.loads(request.content)
            encoded = request.headers['x-research-identity']
            caller = json.loads(base64.urlsafe_b64decode(encoded + '=' * (-len(encoded) % 4)))
            assert set(body) <= {'sources', 'offset', 'limit', 'version', 'metadata_only'}
            active = state['ready'] and any(item == {'knowledge_id': 'source-one', 'file_hash': 'b' * 32}
                                          for item in body['sources'])
            version = hashlib.sha256(json.dumps([active, state['value'], state['paginate'], state['version_change']], sort_keys=True).encode()).hexdigest()
            if body.get('metadata_only'):
                return httpx.Response(200,json={'knowledge_base_id':caller['knowledge_base_id'],
                    'knowledge_base_tenant_id':17,'version':version,'metadata_only':True})
            state['pages'] += 1
            if state['version_change'] and body.get('offset', 0):
                return httpx.Response(409, json={'detail': 'version changed'})
            rows = [{'series_id': SERIES, 'source_series_id': 'raw-column', 'source_knowledge_id': 'source-one',
                     'dataset_id': 'd' * 24, 'date': '2026-01-02', 'value': state['value'],
                     'value_status': 'valid', 'source_cell': 'B2', 'date_cell': 'A2', 'raw_value': state['value']}]
            if state['paginate']:
                rows.append({**rows[0], 'date': '2026-01-03', 'source_cell': 'B3', 'date_cell': 'A3'})
            total = len(rows) if active else 0
            offset = body.get('offset', 0)
            rows = rows[offset:offset + 1] if state['paginate'] else rows
            next_offset = offset + len(rows) if active and offset + len(rows) < total else None
            return httpx.Response(200, json={'knowledge_base_id': caller['knowledge_base_id'], 'knowledge_base_tenant_id': 17,
                'version': version, 'datasets': [{'dataset_id': 'd' * 24, 'source_knowledge_id': 'source-one',
                    'import_id': 'confirmed-import', 'source_file': 'measurements.xlsx'}] if active else [],
                'indicators': [spec] if active else [],
                'rows': rows if active else [], 'total': total,
                'offset': offset, 'next_offset': next_offset, 'truncated': next_offset is not None})
        if request.url.host == 'structured-data' and path == '/api/tools/read':
            return httpx.Response(200, json={'datasets': []})
        if request.url.host == 'structured-data' and path == '/api/imports':
            return httpx.Response(200, json={'imports': []})
        return httpx.Response(404, json={'detail': 'fixture endpoint absent'})

    transport = httpx.MockTransport(upstream)
    original_async, original_client = httpx.AsyncClient, httpx.Client
    monkeypatch.setattr(httpx, 'AsyncClient', lambda **kw: original_async(transport=transport, **kw))
    def sync_request(method, url, **kwargs):
        kwargs.pop('trust_env', None)
        with original_client(transport=transport) as client:
            return client.request(method, url, **kwargs)
    monkeypatch.setattr(httpx, 'request', sync_request)
    headers = {'Authorization': 'Bearer source-user', 'X-Tenant-ID': '17', 'Origin': 'http://testserver'}
    return TestClient(app), headers, state, tmp_path


def api(identifier=FIRST, suffix='indicators'):
    return '/research/api/workbenches/' + identifier + '/' + suffix


def page(identifier=FIRST, suffix='/api/sn/indicators/catalog'):
    return '/research/workbenches/' + identifier + suffix


@pytest.mark.parametrize('identifier', [FIRST, LEGACY])
def test_unconfirmed_kb_has_no_indicators_in_either_mode(canonical, identifier):
    client, headers, state, root = canonical
    state['ready'] = False
    result = client.get(api(identifier), headers=headers)
    assert result.status_code == 200, result.text
    assert result.json()['indicators'] == []
    assert result.json()['total'] == 0


def test_same_kb_two_workbenches_read_same_facts_without_persisting(canonical):
    client, headers, state, root = canonical
    first = client.get(api(FIRST), headers=headers)
    second = client.get(api(SECOND), headers=headers)
    assert first.status_code == second.status_code == 200
    assert first.json()['indicators'] == second.json()['indicators']
    assert [row['series_id'] for row in first.json()['indicators']] == [SERIES]
    assert client.post('/research/auth/session', headers=headers).status_code == 200
    values = []
    for identifier in (FIRST, SECOND):
        result = client.get(api(identifier, 'indicators/' + SERIES + '/observations'), headers=headers)
        assert result.status_code == 200, result.text
        values.append(result.json()['observations'][0]['value'])
        assert client.get(page(identifier)).json()['indicators'][0]['series_id'] == SERIES
    assert values == [17.5, 17.5]
    import sqlite3
    for path in root.rglob('*.db'):
        with sqlite3.connect(path) as db:
            assert db.execute('select count(*) from indicators').fetchone()[0] == 0
            assert db.execute('select count(*) from observations').fetchone()[0] == 0


def test_current_source_update_delete_confirmation_withdrawal_and_failure_never_return_old_values(canonical):
    client, headers, state, _ = canonical
    path = api(suffix='indicators/' + SERIES + '/observations')
    assert client.get(path, headers=headers).json()['observations'][0]['value'] == 17.5
    state['value'] = 23.25
    assert client.get(path, headers=headers).json()['observations'][0]['value'] == 23.25
    state['down'] = True
    assert client.get(path, headers=headers).status_code == 503
    state['down'] = False
    source = state['sources'][0]
    # Excel originals normally have RAG embeddings disabled. This switch is not
    # an authorization or structural confirmation flag.
    source['enable_status'] = 'disabled'
    assert client.get(api(), headers=headers).json()['total'] == 1
    state['ready'] = False
    assert client.get(api(), headers=headers).json()['total'] == 0
    state['ready'] = True
    source['enable_status'] = 'enabled'
    source['file_hash'] = 'c' * 32
    assert client.get(api(), headers=headers).json()['total'] == 0
    state['sources'] = []
    assert client.get(api(), headers=headers).json()['total'] == 0
    assert client.get(path, headers=headers).status_code == 404
    state['revoked'] = True
    assert client.get(api(), headers=headers).status_code == 403


def test_warm_projection_skips_row_transfer_but_checks_current_inventory_and_version(canonical):
    client,headers,state,_=canonical
    assert client.get(api(),headers=headers).json()['total']==1
    pages=state['pages'];calls=len(state['calls'])
    assert client.get(api(),headers=headers).json()['total']==1
    assert state['pages']==pages
    current=state['calls'][calls:]
    assert ('GET','/api/v1/knowledge-bases/'+KB+'/knowledge') in current
    assert ('POST','/api/research/facts') in current
    state['down']=True
    assert client.get(api(),headers=headers).status_code==503
    state['down']=False
    # A failed verification invalidates the physical connection, including TEMP
    # state; recovery must reload rather than reuse an unverified snapshot.
    assert client.get(api(),headers=headers).json()['total']==1
    assert state['pages']==pages+1


def test_exported_documents_do_not_consume_structured_original_limit(canonical):
    client,headers,state,_=canonical
    state['sources'] += [{'id':'report-'+str(index),'knowledge_base_id':KB,
        'file_type':'md','file_hash':'e'*32,'parse_status':'completed'} for index in range(150)]
    result=client.get(api(),headers=headers)
    assert result.status_code==200,result.text
    assert result.json()['total']==1


def test_projection_expiry_and_memory_budget_force_rebuild(canonical,monkeypatch):
    from tin.integration import kb_facts
    client,headers,state,_=canonical
    monkeypatch.setattr(kb_facts,'PROJECTION_SECONDS',0)
    assert client.get(api(),headers=headers).status_code==200
    pages=state['pages']
    assert client.get(api(),headers=headers).status_code==200
    assert state['pages']==pages+1
    monkeypatch.setattr(kb_facts,'PROJECTION_SECONDS',60)
    monkeypatch.setattr(kb_facts,'MAX_RETAINED_BYTES',0)
    assert client.get(api(),headers=headers).status_code==200
    pages=state['pages']
    assert client.get(api(),headers=headers).status_code==200
    assert state['pages']==pages+1


def test_research_cannot_register_or_manually_write_another_fact_copy(canonical):
    client, headers, state, _ = canonical
    assert client.post('/research/auth/session', headers=headers).status_code == 200
    for path in (api(suffix='imports/confirm'), api(suffix='imports/confirm-batch'), api(suffix='indicators'),
                 page(suffix='/sn/entry'), page(suffix='/api/sn/import/commit')):
        response = client.post(path, headers=headers, json={})
        assert response.status_code == 409, (path, response.text)
        assert '知识库' in response.text


def test_unscoped_integrated_old_urls_require_explicit_workbench(canonical):
    client, headers, state, _ = canonical
    assert client.post('/research/auth/session', headers=headers).status_code == 200
    for path in ('/research/', '/research/sn/indicators', '/research/api/sn/indicators/catalog'):
        response = client.get(path, headers=headers)
        assert response.status_code == 409, (path, response.text)
        assert '工作台' in response.text


def test_projection_is_read_only_even_for_direct_sql_and_does_not_read_main(canonical):
    client, headers, state, _ = canonical
    identity = Identity('researcher', 'researcher@test', 'Researcher', 17, 'owner')
    scope = WorkbenchScope(identity, FIRST, KB, 17, {'workspace_mode': 'isolated'}, 'Bearer source-user')
    marker = current_identity.set(identity)
    try:
        with workbench_session(scope) as session:
            assert session.scalar(select(func.count()).select_from(Indicator)) == 1
            assert session.scalar(select(Observation.value)) == 17.5
            for statement in ('INSERT INTO observations (id) VALUES (91)',
                              'UPDATE indicators SET name=\'overwritten\'',
                              'DELETE FROM temp._kb_evidence',
                              'SELECT * FROM main.observations'):
                with pytest.raises(DatabaseError):
                    session.execute(text(statement))
                session.rollback()
    finally:
        current_identity.reset(marker)


def test_other_kb_has_no_inherited_facts_and_page_versions_cannot_mix(canonical):
    client, headers, state, _ = canonical
    assert client.get(api(), headers=headers).json()['total'] == 1
    state['second_other_kb'] = True
    assert client.get(api(SECOND), headers=headers).json()['total'] == 0
    state['paginate'] = True
    path = api(suffix='indicators/' + SERIES + '/observations')
    assert client.get(path, headers=headers).json()['total'] == 2
    state['version_change'] = True
    response = client.get(path, headers=headers)
    assert response.status_code == 409
    assert '17.5' not in response.text


def test_legacy_fact_archive_and_graph_are_preserved_but_not_used_as_current_values(canonical):
    from tin.db import tenant_database_path
    from tin.integration.workbench_scope import result_session_factory
    from tin.models import DiagramTemplate
    client, headers, state, root = canonical
    factory = result_session_factory(tenant_database_path(17))
    stamp = datetime(2025, 1, 1, tzinfo=timezone.utc)
    layout = {'name': 'Authored graph', 'nodes': [{'id': 'historical', 'label': 'Historical binding',
              'binding': {'kind': 'series', 'series_id': 'old-archive-only'}}], 'edges': []}
    with factory() as session:
        session.add(Indicator(series_id='old-archive-only', name='Archived private measurement', variety='SN',
            category='Archive', caliber={'time_type': '原表日期'}, unit='吨', source='fixture', frequency='日', fetch_mode='manual'))
        session.flush()
        session.add(Observation(series_id='old-archive-only', value=9876543, as_of=stamp,
            fetched_at=stamp, caliber_snapshot={'time_type': '原表日期'}, source='fixture', revision=0, status='正常'))
        session.add(DiagramTemplate(variety='SN', name='Authored graph', is_default=True, layout=layout,
                                   created_at=stamp, updated_at=stamp))
        session.commit()
    assert client.post('/research/auth/session', headers=headers).status_code == 200
    result = client.get(page(LEGACY, '/sn/diagram'))
    assert result.status_code == 200 and 'Authored graph' in result.text
    assert '9876543' not in result.text
    result = client.get(page(LEGACY, '/api/sn/diagram/values'))
    assert result.status_code == 200, result.text
    node = next(row for row in result.json()['nodes'] if row['node_id'] == 'historical')
    assert node['value'] is None
    assert node['state'] == 'retired'
    state['ready'] = False
    assert client.get(api(LEGACY), headers=headers).json()['total'] == 0
    with factory() as session:
        assert session.scalar(select(Observation.value)) == 9876543
        assert session.scalar(select(DiagramTemplate.name)) == 'Authored graph'


def test_canonical_judgment_references_save_and_unknown_ids_remain_rejected(canonical):
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.judgments.service import save_version, JudgmentError
    from tin.models import JudgmentSeriesRef
    from tin.schemas.judgment import JudgmentIn
    client, headers, state, _ = canonical
    identity = Identity('researcher', 'researcher@test', 'Researcher', 17, 'owner')
    scope = WorkbenchScope(identity, FIRST, KB, 17, {'workspace_mode': 'isolated'}, 'Bearer source-user')
    marker = current_identity.set(identity)
    try:
        with workbench_session(scope) as session:
            payload = JudgmentIn(variety='SN', author='researcher', written_at=date(2026, 1, 2),
                contradiction={'bull': {'claim': 'Authored thesis'}, 'bear': {'claim': 'Authored counterclaim'}},
                whitelist=[SERIES])
            row = save_version(session, payload, actor='researcher')
            session.commit()
            assert session.scalar(select(JudgmentSeriesRef.series_id).where(JudgmentSeriesRef.judgment_id == row.id)) == SERIES
            with pytest.raises(JudgmentError, match='未登记'):
                save_version(session, payload.model_copy(update={'whitelist': ['not-in-current-kb']}), actor='researcher')
        with isolated_session_factory(17, FIRST)() as session:
            assert session.scalar(select(func.count()).select_from(Indicator)) == 0
            foreign_keys = session.execute(text('PRAGMA foreign_key_list(judgment_series_refs)')).all()
            assert [row[2] for row in foreign_keys] == ['judgments']
            assert session.execute(text('PRAGMA foreign_key_check')).all() == []
    finally:
        current_identity.reset(marker)


def signed_read(body, *, authorization='Bearer source-user', identity_updates=None, scoped=True):
    identity = {'user_id': 'researcher', 'email': 'researcher@test', 'display_name': 'Researcher',
                'tenant_id': 17, 'role': 'owner'}
    if scoped:
        identity.update(workbench_id=FIRST, knowledge_base_id=KB, knowledge_base_tenant_id=17,
                        authorization_sha256=hashlib.sha256(authorization.encode()).hexdigest())
    identity.update(identity_updates or {})
    encoded = base64.urlsafe_b64encode(json.dumps(identity).encode()).decode().rstrip('=')
    stamp = str(int(time.time()))
    message = '\n'.join((stamp, 'POST', '/api/tools/read', encoded, hashlib.sha256(body).hexdigest()))
    return {'Content-Type': 'application/json', 'X-Research-Identity': encoded,
        'X-Research-Timestamp': stamp, 'X-Research-Signature': hmac.new(b'k' * 48, message.encode(), hashlib.sha256).hexdigest(),
        'X-Research-Authorization': authorization}


def test_signed_tool_reads_recheck_live_scope_and_bind_bearer_hash(canonical):
    client, _, state, _ = canonical
    body = b'{"operation":"indicators"}'
    headers = signed_read(body)
    result = client.post('/research/api/tools/read', content=body, headers=headers)
    assert result.status_code == 200, result.text
    assert result.json()['indicators'][0]['series_id'] == SERIES
    assert client.post('/research/api/tools/read', content=body, headers=signed_read(body, scoped=False)).status_code == 409
    for changed in ({'X-Research-Authorization': ''}, {'X-Research-Authorization': 'Bearer swapped'}):
        assert client.post('/research/api/tools/read', content=body, headers={**headers, **changed}).status_code == 401
    assert client.post('/research/api/tools/read', content=body,
        headers=signed_read(body, identity_updates={'user_id': 'forged-user'})).status_code == 403
    state['revoked'] = True
    assert client.post('/research/api/tools/read', content=body, headers=headers).status_code == 403
    identity = Identity('researcher', 'email', 'Researcher', 17, 'owner', authorization='Bearer secret-value')
    assert 'secret-value' not in repr(identity)
    assert 'authorization' not in identity.session_info()


def test_original_diagram_and_indicator_history_resolve_live_canonical_values(canonical):
    client, headers, state, _ = canonical
    assert client.post('/research/auth/session', headers=headers).status_code == 200
    layout = {'nodes': [{'id': 'real', 'label': 'Measured quantity',
                        'binding': {'kind': 'series', 'series_id': SERIES}}], 'edges': []}
    response = client.post(page(suffix='/api/sn/diagram/templates'), headers=headers,
                           json={'name': 'Authored structure', 'layout': layout})
    assert response.status_code == 200, response.text
    for method in ('get', 'post'):
        request = getattr(client, method)
        kwargs = {'json': {'layout': layout}} if method == 'post' else {}
        response = request(page(suffix='/api/sn/diagram/values'), headers=headers, **kwargs)
        assert response.status_code == 200, response.text
        node = next(row for row in response.json()['nodes'] if row['node_id'] == 'real')
        assert node['value'] == 17.5 and node['state'] == 'ok'
    response = client.get(page(suffix='/sn/indicators/' + SERIES))
    assert response.status_code == 200 and 'Measured quantity' in response.text
    history = client.get(page(suffix='/api/sn/series/' + SERIES + '/history'))
    assert history.status_code == 200
    assert history.json()['points'] == [{'date': '2026-01-02', 'value': 17.5}]
    state['sources'] = []
    response = client.post(page(suffix='/api/sn/diagram/values'), headers=headers, json={'layout': layout})
    assert response.status_code == 200
    assert response.json()['as_of'] is None
    assert response.json()['nodes'][0]['value'] is None


def test_inventory_and_overview_use_same_confirmed_catalog_without_fresh_only_filter(canonical):
    client, headers, state, _ = canonical
    overview = client.get(api(suffix='overview'), headers=headers)
    sources = client.get(api(suffix='sources'), headers=headers)
    assert overview.status_code == sources.status_code == 200
    assert overview.json()['counts']['datasets'] == 1
    assert overview.json()['counts']['indicators'] == 1
    assert sources.json()['datasets'][0]['dataset_id'] == 'd' * 24
    assert sources.json()['datasets'][0]['observation_count'] == 1
    assert sources.json()['datasets'][0]['series_count'] == 1
    state['ready'] = False
    overview = client.get(api(suffix='overview'), headers=headers)
    assert overview.json()['counts']['datasets'] == overview.json()['counts']['indicators'] == 0


def test_production_scoped_origin_actor_pin_and_viewer_are_enforced(canonical):
    client, headers, state, _ = canonical
    assert client.get(page(), headers={**headers, 'X-Research-User': 'forged'}).status_code == 403
    target = page(suffix='/api/sn/diagram/templates')
    body = {'name': 'Authored structure', 'layout': {'nodes': [], 'edges': []}}
    assert client.post(target, headers={**headers, 'Origin': 'http://evil.invalid'}, json=body).status_code == 403
    assert client.post(target, headers={key: value for key, value in headers.items() if key != 'Origin'}, json=body).status_code == 403
    viewer = {**headers, 'Authorization': 'Bearer source-viewer'}
    assert client.get(page(), headers=viewer).status_code == 200
    assert client.post(target, headers=viewer, json=body).status_code == 403
    assert client.post(target, headers=headers, json=body).status_code == 200
