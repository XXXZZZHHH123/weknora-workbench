"""The anomaly's verified row window equals the existing SQL projection path."""
from contextlib import nullcontext
from copy import deepcopy
import json

import httpx
import pytest

from tin.integration import kb_facts, page_facts
from tin.provenance import bridge
from tin.web import chart_data, charts
from tests.chart_fixtures import SID, OTHER, THIRD
from tests.test_chart_api import chart_client

DAY = '2023-01-04'


def read_routes(state, *, identifiers=None, limit=3):
    identifiers = identifiers or [SID, OTHER, THIRD]
    def endpoint(direct):
        def read():
            charts.require_scope()
            with chart_data.chart_read_scope(use_bounded=direct) if direct else nullcontext():
                return chart_data.read_series(identifiers, chart_data.cutoff(DAY), limit=limit)
        return read
    state.app.add_api_route('/synthetic-series/direct', endpoint(True), methods=['GET'])
    state.app.add_api_route('/synthetic-series/sql', endpoint(False), methods=['GET'])


def mutate_response(monkeypatch, mutate):
    transport = bridge.structured_request
    def response(*args, **kwargs):
        result = transport(*args, **kwargs)
        if not args[2].get('catalog_only'):
            payload = result.json()
            mutate(payload)
            return httpx.Response(result.status_code, json=payload, request=result.request)
        return result
    monkeypatch.setattr(bridge, 'structured_request', response)


@pytest.mark.parametrize('limit', [1, 3, 2000])
@pytest.mark.parametrize('totals', [True, False])
def test_direct_series_equal_every_sql_field_and_skip_projection(chart_client, monkeypatch, limit, totals):
    client, state = chart_client
    state.fields[SID]['metric'] = ' 字段名称保留空白 '
    state.fields[SID]['unit'] = ''
    state.fields[OTHER]['unit'] = None
    state.observations[SID] = [('2023-01-01', -2), ('2023-01-02', -0.0), ('2023-01-04', 7)]
    state.observations[OTHER] = [('2023-01-01', 4), ('2023-01-03', 2)]
    state.observations[THIRD] = []
    read_routes(state, limit=limit)
    original_saved, original_projection = page_facts._saved_plan, kb_facts.install_projection
    saved, projections = [], []
    def saved_plan(*args):
        saved.append(deepcopy(args[1]))
        return original_saved(*args)
    def projection(*args, **kwargs):
        projections.append(True)
        return original_projection(*args, **kwargs)
    monkeypatch.setattr(page_facts, '_saved_plan', saved_plan)
    monkeypatch.setattr(kb_facts, 'install_projection', projection)
    def reorder(payload):
        payload['rows'].reverse()
        if not totals:
            payload.pop('series_totals')
    mutate_response(monkeypatch, reorder)
    projected = client.get('/synthetic-series/sql')
    direct = client.get('/synthetic-series/direct')
    assert projected.status_code == direct.status_code == 200
    assert direct.json() == projected.json()
    assert json.dumps(direct.json(), sort_keys=True) == json.dumps(projected.json(), sort_keys=True)
    result = direct.json()
    assert set(result[SID]) == {'series_id', 'name', 'unit', 'points', 'freq_days', 'limited', 'point_limit', 'source_version'}
    assert result[SID]['name'] == ' 字段名称保留空白 '
    assert result[SID]['unit'] == result[OTHER]['unit'] == '未注明'
    assert result[THIRD]['points'] == []
    assert projections == [True], 'the ordinary path projects once; the direct path never installs SQL facts'
    assert len(saved) == 2 and saved[0] == saved[1], 'both paths retain the saved plan and explicit point window'
    assert chart_data._direct_chart_read.get() is False


@pytest.mark.parametrize('kind', ['identity', 'name', 'value', 'status', 'date', 'duplicate',
    'source_version', 'calendar_scope', 'calendar_type', 'calendar_date'])
def test_direct_window_and_sql_reject_same_invalid_responses(chart_client, monkeypatch, kind):
    client, state = chart_client
    for field in state.fields.values():
        field['source_version'] = 'e' * 64
    read_routes(state)
    def invalid(payload):
        if kind == 'identity': payload['indicators'][0]['dataset_id'] = None
        elif kind == 'name': payload['indicators'][0]['metric'] = payload['indicators'][0]['name'] = ' '
        elif kind == 'value': payload['rows'][0]['value'] = 'invalid'
        elif kind == 'status': payload['rows'][0]['value_status'] = 'invalid'
        elif kind == 'date': payload['rows'][0]['date'] = '2023-01-05'
        elif kind == 'duplicate': payload['rows'].append(dict(payload['rows'][0]))
        elif kind == 'source_version': payload['source_versions'] = {key: 'f' * 64 for key in payload['source_versions']}
        elif kind == 'calendar_scope': payload['calendar'] = {'as_of': '2023-01-03'}
        elif kind == 'calendar_type': payload['calendar'] = []
        elif kind == 'calendar_date': payload['calendar'] = {'as_of': DAY, 'previous_date': '2023-1-1'}
    mutate_response(monkeypatch, invalid)
    projected = client.get('/synthetic-series/sql')
    direct = client.get('/synthetic-series/direct')
    assert direct.status_code == projected.status_code == (409 if kind == 'source_version' else 503)
    assert direct.json() == projected.json()
    assert chart_data._direct_chart_read.get() is False


@pytest.mark.parametrize('field', ['source_cell', 'date_cell', 'formula'])
def test_nonbinding_evidence_keeps_original_sql_failure(chart_client, monkeypatch, field):
    client, state = chart_client
    read_routes(state)
    mutate_response(monkeypatch, lambda payload: payload['rows'][0].update({field: {'unusable': 'binding'}}))
    # A direct consumer must not accept evidence the original SQL path rejects.
    for mode in ('sql', 'direct'):
        with pytest.raises(Exception, match='binding parameter|not supported'):
            client.get('/synthetic-series/' + mode)
    assert chart_data._direct_chart_read.get() is False


@pytest.mark.parametrize('field', ['value', 'source_cell', 'date_cell', 'formula'])
def test_overflowing_sql_integers_keep_original_failure(chart_client, monkeypatch, field):
    client, state = chart_client
    read_routes(state)
    mutate_response(monkeypatch, lambda payload: payload['rows'][0].update({field: 2**63}))
    for mode in ('sql', 'direct'):
        with pytest.raises(Exception, match='int too large|overflow'):
            client.get('/synthetic-series/' + mode)


def test_nonstandard_unit_preserves_sql_coercion(chart_client):
    client, state = chart_client
    state.fields[SID]['unit'] = 17
    read_routes(state)
    projected = client.get('/synthetic-series/sql')
    direct = client.get('/synthetic-series/direct')
    assert projected.status_code == direct.status_code == 200
    assert direct.json() == projected.json()
    assert direct.json()[SID]['unit'] == '17'


def test_direct_window_stays_inside_page_metric_and_point_budgets(chart_client):
    client, state = chart_client
    identifiers = ['kb-' + f'{index + 1:048x}' for index in range(27)]
    state.fields = {sid: {'series_id': sid, 'metric': '合成字段', 'unit': '件'} for sid in identifiers}
    state.observations = {sid: [(DAY, index)] for index, sid in enumerate(identifiers)}
    read_routes(state, identifiers=identifiers, limit=2000)
    response = client.get('/synthetic-series/direct')
    assert response.status_code == 200 and len(response.json()) == 27
    windows = [body for _, body in state.requests if not body.get('catalog_only')]
    assert [len(body['series_ids']) for body in windows] == [10, 10, 7]
    assert all(len(body['series_ids']) <= 256 and len(body['series_ids']) * body['points_per_series'] <= 20000 for body in windows)


@pytest.mark.parametrize('headers,status', [({'x-restricted': 'true'}, 403), ({'x-scope-version': '1'}, 409)])
def test_direct_window_requires_same_chart_grant(chart_client, headers, status):
    client, state = chart_client
    read_routes(state)
    assert client.get('/synthetic-series/direct', headers=headers).status_code == status
    assert state.requests == []
