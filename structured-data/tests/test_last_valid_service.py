from copy import deepcopy
import json
from shutil import copytree

import httpx
import pytest
from fastapi.testclient import TestClient

from structured_data.field_resolver import FieldResolver
from structured_data.server import create_app
from structured_data.store import DataStore
from test_caller_source_binding import GROUP
from test_resource_permissions import KEY, signed_projection
from test_result_contract import assert_binding
from test_scoped_service import signed
from test_source_context import duplicate_source  # noqa: F401 (pytest fixture)


def binding(*fields):
    return [{'considered_ids': list(reversed(GROUP)), 'selected_ids': list(fields),
             'reason': 'The complete request selects the columns for these actual observations.'}]


def stub_review(monkeypatch):
    calls = []

    def clarify(request):
        calls.append(deepcopy(request))
        return {'decision': 'clarify', 'question': 'Which source is required?',
                'bindings': [{'considered_ids': [field['series_id'] for field in group],
                              'selected_ids': [], 'reason': 'Several source definitions are possible.'}
                             for group in request['source_groups']]}

    monkeypatch.setattr(FieldResolver, '__init__', lambda self, store: self.__dict__.update(
        store=store, model_fn=clarify))
    return calls


def app_with_key(store, monkeypatch):
    key_file = store.root / 'signed-reads-key'
    key_file.write_bytes(KEY)
    monkeypatch.setenv('STRUCTURED_SERVICE_TOKEN_FILE', str(key_file))
    monkeypatch.setenv('STRUCTURED_TENANT_ID', '10000')
    monkeypatch.delenv('WEKNORA_STRUCTURED_LAST_VALID', raising=False)
    return create_app(store.root)


@pytest.mark.parametrize('entry', ['native', 'mcp'])
def test_bound_queries_reuse_current_selection_and_legacy_calls_keep_old_shape(duplicate_source, monkeypatch, entry):
    store, dataset = duplicate_source
    calls = stub_review(monkeypatch)
    if entry == 'native':
        scoped = DataStore(store.root / 'workspaces' / '9' / 'knowledge-bases' / 'library-a')
        copytree(store.datasets, scoped.datasets, dirs_exist_ok=True)
    with TestClient(app_with_key(store, monkeypatch)) as client:
        def call(operation, arguments):
            if entry == 'native':
                path = '/api/tools/read'
                body = {'operation': operation, 'arguments': arguments}
                if operation == 'describe_tools':
                    raw, headers = signed(KEY, body=body)
                    response = client.post(path, content=raw, headers=headers)
                else:
                    response = client.post(path, **signed_projection(body, path=path, workbench=False,
                        machine=True, authorization='', api_key_scope_sha256='a' * 64))
                assert response.status_code == 200, response.text
                return response.json()
            headers = {'Host': 'localhost:18082', 'Accept': 'application/json, text/event-stream',
                       'Authorization': 'Bearer ' + (store.root / 'service-token').read_text()}
            method, params = ('tools/list', {}) if operation == 'describe_tools' else (
                'tools/call', {'name': operation, 'arguments': arguments})
            response = client.post('/mcp', headers=headers, json={'jsonrpc': '2.0', 'id': 1,
                'method': method, 'params': params})
            assert response.status_code == 200, response.text
            result = response.json()['result']
            assert not result.get('isError'), result
            if operation == 'describe_tools':
                return result
            return result.get('structuredContent') or json.loads(result['content'][0]['text'])

        operations = ['query_series', 'analyze_table']
        described = call('describe_tools', {'operations': operations})
        for tool in described['tools']:
            if tool['name'] in operations:
                option = tool['inputSchema']['properties']['include_last_valid']
                assert option['type'] == 'boolean' and option['default'] is False
                assert 'coverage.last_valid' in option['description']

        scope = {'dataset_id': dataset, 'source_question': 'Compare which available observations cover the period.',
                 'start_date': '2023-02-01', 'end_date': '2023-02-28', 'include_last_valid': True}
        scalar = call('query_series', {**scope, 'series_id': 'G', 'source_binding': binding('G')})
        assert scalar['status'] == 'no_data' and scalar['valid_count'] == scalar['scope_record_count'] == 0
        assert scalar['rows'] == [] and scalar['requested_range']['end_date'] == '2023-02-28'
        latest = scalar['coverage']['last_valid']
        assert latest['status'] == 'observed' and latest['direction'] == 'before_range'
        assert latest['rows'][0]['value'] == 200 and latest['rows'][0]['source_cell'] == 'G3'
        assert_binding(scalar)
        assert not calls

        args = {**scope, 'series_ids': ['G'], 'sql': 'SELECT count(value) AS available FROM data'}
        result = call('analyze_table', args)
        assert result['rows'] == [[0]] and result['inputs']['G']['coverage']['last_valid'] == latest
        assert_binding(result)
        assert not calls, 'use the same validated group selection across both real entry points'

        bad = call('analyze_table', {**args, 'source_binding': binding('C')})
        assert bad['status'] == 'plan_mismatch' and bad['code'] == 'source_binding_invalid'
        assert 'rows' not in bad and not calls
        unresolved = call('query_series', {**scope, 'series_id': 'C'})
        assert unresolved['status'] == 'needs_clarification' and 'rows' not in unresolved
        assert len(calls) == 1
        missing = call('query_series', {**scope, 'series_id': 'G', 'source_question': None})
        assert missing['status'] == 'error' and 'rows' not in missing
        assert len(calls) == 1

        original = call('query_series', {**scope, 'series_id': 'G', 'source_binding': binding('G'),
                                         'include_last_valid': False})
        assert 'coverage' not in original and 'requested_range' not in original
        assert_binding(original)

        monkeypatch.setenv('WEKNORA_STRUCTURED_LAST_VALID', 'off')
        assert call('query_series', {**scope, 'series_id': 'G', 'source_binding': binding('G')}) == original
        disabled = call('analyze_table', {**args, 'source_binding': binding('G')})
        assert disabled['rows'] == [[0]] and 'coverage' not in disabled['inputs']['G']
        assert_binding(disabled)
        assert call('analyze_table', args)['status'] == 'needs_clarification'
        assert len(calls) == 2, 'disabled requests still use the preexisting independent review path'


def test_native_binding_revalidates_grant_principal_and_snapshot(duplicate_source, monkeypatch):
    store, dataset = duplicate_source
    calls = stub_review(monkeypatch)
    kb = 'library-a'
    scoped = DataStore(store.root / 'workspaces' / '9' / 'knowledge-bases' / kb)
    copytree(store.datasets, scoped.datasets, dirs_exist_ok=True)
    series = json.loads((store._path(dataset) / 'series.json').read_text())
    with store._connect(dataset) as connection:
        result = connection.execute('SELECT * FROM observations')
        columns = [column[0] for column in result.description]
        observations = [dict(zip(columns, item)) for item in result.fetchall()]
    # Use the same workbook and real field identities with a fresh import rule.
    source = store._path(dataset) / 'source.xlsx'
    replacement = scoped.publish(dict(source_file=str(source), sha256=store.manifest(dataset)['sha256'],
        rule_version='fixture-v2', series=series, observations=observations,
        sheets=[], excluded_regions=[], issues=[]))['dataset_id']
    assert replacement != dataset

    permission = {'status': 200, 'role': 'viewer', 'checks': 0}
    original_client = httpx.AsyncClient

    def upstream(request):
        assert request.url.path == '/api/v1/knowledge-bases/library-a'
        permission['checks'] += 1
        return httpx.Response(permission['status'], json={'success': True, 'data': {
            'id': kb, 'tenant_id': 9, 'my_permission': permission['role']}})

    monkeypatch.setattr(httpx, 'AsyncClient', lambda **options: original_client(
        transport=httpx.MockTransport(upstream), **options))
    with TestClient(app_with_key(store, monkeypatch)) as client:
        path = '/api/tools/read'
        query = {'dataset_id': dataset, 'series_id': 'G', 'source_question': 'An exact observation is required.',
                 'include_last_valid': True}

        def post(arguments, **identity):
            body = {'operation': 'query_series', 'arguments': arguments}
            return client.post(path, **signed_projection(body, path=path, workbench=False, **identity))

        planned = {**query, 'source_binding': binding('G')}
        assert post(planned).json()['coverage']['last_valid']['status'] == 'observed'
        assert post(query).json()['coverage']['last_valid']['status'] == 'observed'
        assert not calls

        permission['status'] = 403
        denied = post(query)
        assert denied.status_code == 403 and 'coverage' not in denied.json()
        assert not calls

        permission.update(status=200, role='editor')
        assert post(query).json()['status'] == 'needs_clarification'
        assert len(calls) == 1
        permission['role'] = 'viewer'
        assert post(query, authorization='Bearer newer').json()['status'] == 'needs_clarification'
        assert post(query, user='other').json()['status'] == 'needs_clarification'
        assert post({**query, 'dataset_id': replacement}).json()['status'] == 'needs_clarification'
        assert len(calls) == 4 and permission['checks'] == 7

        # The older envelope can share a mapped identity across multiple keys.
        machine = {'machine': True, 'authorization': ''}
        assert post(planned, **machine).json()['coverage']['last_valid']['status'] == 'observed'
        assert post(query, **machine).json()['status'] == 'needs_clarification'
        assert len(calls) == 5 and permission['checks'] == 7

        bound_machine = {**machine, 'api_key_scope_sha256': 'a' * 64}
        assert post(planned, **bound_machine).json()['coverage']['last_valid']['status'] == 'observed'
        assert post(query, **bound_machine).json()['coverage']['last_valid']['status'] == 'observed'
        assert len(calls) == 5
        for digest in ('b' * 64, 'c' * 64):
            assert post(query, **{**machine, 'api_key_scope_sha256': digest}).json()['status'] == 'needs_clarification'
        assert post(query, **machine).json()['status'] == 'needs_clarification'
        assert len(calls) == 8

        # A digest is private signed authentication context and cannot bind a
        # human principal or arrive as a public tool argument.
        for invalid in ('a' * 63, 'A' * 64, 'g' * 64, 42):
            assert post(planned, **{**machine, 'api_key_scope_sha256': invalid}).status_code == 401
        assert post(planned, api_key_scope_sha256='a' * 64).status_code == 401
        assert post({**planned, 'api_key_scope_sha256': 'a' * 64}, **machine).status_code == 400
        assert permission['checks'] == 7

        def post_unscoped(arguments):
            raw, headers = signed(KEY, body={'operation': 'query_series', 'arguments': arguments})
            return client.post(path, content=raw, headers=headers).json()

        assert post_unscoped(planned)['coverage']['last_valid']['status'] == 'observed'
        assert post_unscoped(query)['status'] == 'needs_clarification'
        assert len(calls) == 9

        monkeypatch.setenv('WEKNORA_STRUCTURED_LAST_VALID', 'false')
        assert 'coverage' not in post(planned, **bound_machine).json()
        assert post(query, **machine).json()['status'] == 'needs_clarification'
        assert len(calls) == 10 and permission['checks'] == 7


def test_only_bound_tools_expose_endpoint_and_separate_truncation_from_read_failure(duplicate_source, monkeypatch):
    store, dataset = duplicate_source
    calls = stub_review(monkeypatch)
    original_client = httpx.AsyncClient

    def upstream(request):
        assert request.url.path == '/api/v1/auth/me'
        return httpx.Response(200, json={'data': {'user': {'id': 'browser', 'tenant_id': 10000},
            'memberships': [{'tenant_id': 10000, 'role': 'viewer'}]}})

    monkeypatch.setattr(httpx, 'AsyncClient', lambda **options: original_client(
        transport=httpx.MockTransport(upstream), **options))
    with TestClient(app_with_key(store, monkeypatch)) as client:
        browser = {'Authorization': 'Bearer browser'}
        params = {'dataset_id': dataset, 'series_id': 'G', 'include_last_valid': 'true'}
        response = client.get('/api/query', params=params, headers=browser)
        assert response.status_code == 200
        assert response.json()['rows'][0]['source_cell'] == 'G3' and 'coverage' not in response.json()

        def post(operation, **arguments):
            raw, headers = signed(KEY, body={'operation': operation, 'arguments': {
                'dataset_id': dataset, 'include_last_valid': True,
                'source_question': 'Compare the two source observations.', **arguments}})
            return client.post('/api/tools/read', content=raw, headers=headers).json()

        sql_input = dict(series_ids=['C', 'G'], sql='SELECT value FROM data ORDER BY value',
                         source_binding=binding('C', 'G'), limit=1)
        limited = post('analyze_table', **sql_input)
        assert limited['status'] == 'ok' and limited['output_truncated'] and limited['input_complete']
        assert all(field['coverage']['last_valid']['status'] == 'observed' for field in limited['inputs'].values())
        assert_binding(limited)

        def failing_read(self, dataset_id):
            raise OSError('a disk read failed')

        monkeypatch.setattr(DataStore, '_connect', failing_read)
        for failure in (post('query_series', series_id='G', source_binding=binding('G')),
                        post('analyze_table', **sql_input)):
            assert failure['status'] == 'error' and failure['code'] == 'observation_read_failed'
            assert failure['query_completed'] is False and 'rows' not in failure and 'coverage' not in failure
        assert not calls
