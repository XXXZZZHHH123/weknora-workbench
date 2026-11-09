"""Invalid SQL is correctable tool feedback, never an HTTP 500 or result."""
import json

import pytest

from structured_data.table_analysis import ANALYSIS_INPUT_COLUMNS, AnalysisSQLError, analyze_table
from test_scoped_service import service, signed
from test_store import snapshot


SQL_FAILURES = [
    ('WITH pivot AS (SELECT value FROM data) SELECT * FROM pivot', 'parse'),
    ('SELECT ( FROM data', 'parse'),
    ('SELECT date, avg(value) FROM data', 'execute'),
    ('WITH selected AS (SELECT value FROM data) SELECT s.value FROM selected AS actual', 'execute'),
    ('SELECT value FROM unavailable_table', 'execute'),
]


@pytest.mark.parametrize('sql,stage', SQL_FAILURES)
def test_sql_parse_and_binding_failures_never_persist_analysis(snapshot, sql, stage):
    store, dataset, _ = snapshot
    with pytest.raises(AnalysisSQLError) as captured:
        analyze_table(store, dataset, ['price'], sql)
    failure = captured.value
    assert failure.stage == stage
    assert failure.diagnostic()['query_completed'] is False
    assert 'data' in str(failure)
    assert not list((store.root / 'analysis-results').glob('*.json'))
    assert store.query_series(dataset, 'price', operation='sum')['aggregate'] == 30


@pytest.mark.parametrize('sql,stage', SQL_FAILURES)
def test_native_sql_errors_are_structured_and_correction_can_run(service, sql, stage):
    client, key, dataset = service

    def post(query):
        raw, headers = signed(key, body={'operation': 'analyze_table', 'arguments': {
            'dataset_id': dataset, 'series_ids': ['price'], 'sql': query}})
        return client.post('/api/tools/read', content=raw, headers=headers)

    response = post(sql)
    assert response.status_code == 200
    failure = response.json()
    assert failure['status'] == 'error' and failure['retryable'] is True
    assert failure['code'] == 'analysis_sql_' + stage + '_error'
    assert failure['stage'] == 'sql_' + stage
    assert failure['query_completed'] is False
    assert failure['sql_contract'] == {'dialect': 'DuckDB', 'table': 'data', 'read_only': True,
        'columns': [{'name': name, 'type': dtype} for name, dtype in ANALYSIS_INPUT_COLUMNS]}
    assert 'rows' not in failure and 'result_id' not in failure and 'result_contract' not in failure
    # The caller supplies the corrected plan. The failed SQL was not rewritten,
    # run successfully, cached as a result or used to fabricate any rows.
    corrected = 'WITH selected AS (SELECT value FROM data WHERE value IS NOT NULL) SELECT sum(value) AS total FROM selected'
    result = post(corrected)
    assert result.status_code == 200
    assert result.json()['status'] == 'ok'
    assert result.json()['rows'] == [[30]]
    assert result.json()['sql'] == corrected


def test_actual_manifest_exposes_duckdb_grouping_and_alias_contract(service):
    client, key, _ = service
    raw, headers = signed(key, body={'operation': 'describe_tools', 'arguments': {'operations': ['analyze_table']}})
    response = client.post('/api/tools/read', content=raw, headers=headers)
    assert response.status_code == 200
    description = response.json()['tools'][0]['description']
    prefix = description.encode('utf-8')[:1024].decode('utf-8', errors='ignore')
    assert all(token in prefix for token in ('DuckDB', 'data', 'date DATE', 'value DOUBLE', 'GROUP BY'))
    assert '别名' in description and 'CTE' in description and '保留字' in description


def test_legacy_mcp_parser_failure_uses_the_same_error_contract(snapshot):
    from fastapi.testclient import TestClient
    from structured_data.server import create_app

    store, dataset, _ = snapshot
    with TestClient(create_app(store.root)) as client:
        headers = {'Host': 'localhost:18082', 'Accept': 'application/json, text/event-stream',
                   'Authorization': 'Bearer ' + (store.root / 'service-token').read_text()}
        response = client.post('/mcp', headers=headers, json={'jsonrpc': '2.0', 'id': 1,
            'method': 'tools/call', 'params': {'name': 'analyze_table', 'arguments': {
                'dataset_id': dataset, 'series_ids': ['price'], 'sql': 'SELECT ( FROM data'}}})
        assert response.status_code == 200
        result = response.json()['result']
        assert not result.get('isError'), result
        payload = result.get('structuredContent') or json.loads(result['content'][0]['text'])
        assert payload['status'] == 'error' and payload['code'] == 'analysis_sql_parse_error'
        assert payload['query_completed'] is False and 'rows' not in payload
