"""Monitor reads pass the actual structured-service bounded-window contract."""
from copy import deepcopy
from datetime import date, timedelta
from pathlib import Path
import re
from types import SimpleNamespace

from fastapi import FastAPI
from fastapi.testclient import TestClient
import httpx
import pytest

from tin.config import settings
from tin.integration.identity import current_identity
from tin.integration.workbench_scope import WorkbenchScope, artifact_session, current_workbench
from tin.web import monitors
from tin.web.monitor_data import backtest_monitor
from tests.chart_fixtures import SID, OTHER
from tests.test_workbench_sources_v2 import IDENTITY, WB, source


END = date(2026, 1, 31)
URL = '/api/sn/monitors'


def rule(identifier=SID, **changes):
    return {'series_id': identifier, 'metric': 'change', 'operator': '>', 'value': 0,
            'consecutive_periods': 2, 'label': '隔离协议样例', 'definition': '相邻观测增加', **changes}


def assert_protocol(body):
    """Always-run equivalent of research_facts_v2.parameters' monitor subset.

    In particular, include_invalid is *not* the ordinary 2,000-point indicator
    grant: research_facts._bounded_parameters caps it at 121 in judgments.
    """
    assert isinstance(body, dict)
    assert set(body) <= {'source_bindings', 'catalog_only', 'offset', 'limit', 'search', 'series_ids',
                         'version', 'view_module', 'as_of', 'points_per_series', 'allow_unavailable',
                         'include_invalid'}
    bindings = body['source_bindings']
    assert isinstance(bindings, list) and len(bindings) <= 8 and len(bindings) == len(set(bindings))
    assert all(isinstance(value, str) and 1 <= len(value) <= 128 for value in bindings)
    assert type(body.get('catalog_only', False)) is bool
    assert type(body.get('allow_unavailable', False)) is bool
    if 'version' in body:
        assert isinstance(body['version'], str) and re.fullmatch('[a-f0-9]{64}', body['version'])
    ids = body.get('series_ids')
    if ids is not None:
        assert isinstance(ids, list) and len(ids) <= 256 and len(ids) == len(set(ids))
        assert all(isinstance(sid, str) and re.fullmatch('kb-[0-9a-f]{48}', sid) for sid in ids)
    if body.get('catalog_only'):
        assert not {'view_module', 'as_of', 'points_per_series', 'include_invalid'} & body.keys()
        assert type(body.get('offset', 0)) is int and body.get('offset', 0) >= 0
        assert type(body.get('limit', 200)) is int and 1 <= body.get('limit', 200) <= 200
        assert isinstance(body.get('search', ''), str) and len(body.get('search', '')) <= 200
        return
    assert ids is not None
    assert not {'offset', 'limit', 'search'} & body.keys()
    module = body.get('view_module')
    assert module in {'home', 'market', 'indicators', 'judgments', 'strategies', 'diagram', 'reports'}
    invalid = body.get('include_invalid', False)
    assert type(invalid) is bool and (not invalid or module == 'judgments')
    points = body.get('points_per_series', 2)
    maximum = (121 if invalid else 180 if module == 'market' and type(points) is int and points > 2
               else {'home': 121, 'reports': 121, 'judgments': 121, 'strategies': 121,
                     'diagram': 121 if len(ids) == 1 else 24, 'indicators': 2000}.get(module, 2))
    assert type(points) is int and 1 <= points <= maximum
    assert len(ids) * points <= 20000
    assert not (module == 'market' and points > 2) or len(ids) <= 12
    if 'as_of' in body:
        assert isinstance(body['as_of'], str) and re.fullmatch(r'\d{4}-\d{2}-\d{2}', body['as_of'])
        assert date.fromisoformat(body['as_of']).isoformat() == body['as_of']


@pytest.fixture(params=['equivalent', 'structured-service'])
def validate_request(request, monkeypatch):
    if request.param == 'equivalent':
        return assert_protocol
    monkeypatch.syspath_prepend(str(Path(__file__).resolve().parents[2] / 'structured-data' / 'src'))
    facts = pytest.importorskip('structured_data.research_facts')
    v2 = pytest.importorskip('structured_data.research_facts_v2')

    def validate(body):
        assert_protocol(body)
        v2.parameters(body)
        if not body.get('catalog_only'):
            # Exactly the adaptation made by research_facts_v2.parameters.
            clean = {key: value for key, value in body.items() if key not in {
                'source_bindings', 'version', 'catalog_only', 'allow_unavailable'}}
            facts._bounded_parameters(clean, extended=True)
    return validate


@pytest.fixture
def protocol_client(tmp_path, monkeypatch, validate_request):
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    direct = source(1)
    scope = WorkbenchScope(IDENTITY, WB, '', 0,
        {'workspace_mode': 'isolated', 'can_edit': True, 'can_read_source': True},
        authorization='synthetic-protocol-fixture', scope_version=2, sources_revision=7,
        sources=(direct,), view_modules=('home',))
    state = SimpleNamespace(requests=[], observations={SID: [], OTHER: []},
                            frequencies={SID: '日', OTHER: '日'}, on_read=None, reads=0, scope=scope)

    def transport(method, endpoint, body, **kwargs):
        assert method == 'POST' and endpoint == '/api/research/facts'
        assert kwargs == {'return_error_response': True}
        assert current_workbench.get() is scope and current_identity.get() == IDENTITY
        state.requests.append(deepcopy(body))
        request = httpx.Request(method, 'http://fixture.invalid' + endpoint)
        try:
            validate_request(body)
        except (AssertionError, ValueError):
            return httpx.Response(400, json={'detail': 'structured facts contract rejected'}, request=request)
        version = 'a' * 64
        if not body.get('catalog_only'):
            state.reads += 1
            if state.on_read:
                version = state.on_read(body, state.reads) or version
        ids = body.get('series_ids', list(state.observations))
        fields = [{'series_id': sid, 'metric': '隔离协议字段', 'name': '隔离协议字段', 'unit': '件',
                   'frequency': state.frequencies[sid], 'source_binding_id': direct['id'],
                   'knowledge_base_id': direct['knowledge_base_id'], 'knowledge_base_tenant_id': 17,
                   'source_type': direct['source_type'], 'source_id': direct['source_id'],
                   'source_knowledge_id': 'fixture-original', 'dataset_id': 'fixture-dataset',
                   'source_series_id': sid, 'source_version': version}
                  for sid in ids if sid in state.observations]
        envelope = {'scope_version': 2, 'workbench_id': WB, 'sources_revision': 7, 'version': version}
        if body.get('catalog_only'):
            payload = {**envelope, 'catalog_only': True, 'series': fields, 'datasets': [],
                       'total': len(fields), 'offset': 0, 'next_offset': None, 'truncated': False}
        else:
            rows = []
            for field in fields:
                sid = field['series_id']
                selected = sorted((stamp, value) for stamp, value in state.observations[sid]
                                  if stamp <= body['as_of'])[-body['points_per_series']:]
                rows.extend({'series_id': sid, 'date': stamp, 'value': value,
                             'value_status': 'missing' if value is None else 'valid'}
                            for stamp, value in selected)
            payload = {**envelope, 'bounded': True, 'indicators': fields, 'rows': rows,
                       'source_versions': {direct['id']: version}, 'include_invalid': True,
                       'unavailable_series_ids': [], 'datasets': []}
        return httpx.Response(200, json=payload, request=request)

    monkeypatch.setattr('tin.provenance.bridge.structured_request', transport)
    app = FastAPI()
    app.include_router(monitors.router)

    @app.middleware('http')
    async def scoped(request, call_next):
        marker, actor = current_workbench.set(scope), current_identity.set(IDENTITY)
        try:
            return await call_next(request)
        finally:
            current_workbench.reset(marker)
            current_identity.reset(actor)

    @app.get('/protocol-backtest')
    def read(start: str | None = None):
        return backtest_monitor(scope, state.rules, 'all', start=start, end=END)

    @app.post('/protocol-check')
    def check():
        return monitors.check_monitors(scope, END, END.isoformat() + 'T12:00:00+08:00')

    state.rules = [rule()]
    with TestClient(app) as client:
        yield client, state


def observation_window(end=END, size=900, interval=1):
    return [((end - timedelta(days=index * interval)).isoformat(), size - index)
            for index in reversed(range(size))]


def assert_paged_on_real_dates(state):
    previous = {}
    reads = [body for body in state.requests if not body.get('catalog_only')]
    assert reads
    for body in reads:
        assert body['view_module'] == 'judgments' and body['include_invalid'] is True
        assert 1 <= body['points_per_series'] <= 121
        for sid in body['series_ids']:
            if sid in previous:
                assert body['as_of'] == previous[sid]
            selected = sorted(stamp for stamp, _ in state.observations[sid] if stamp <= body['as_of'])
            page = selected[-body['points_per_series']:]
            if page:
                previous[sid] = (date.fromisoformat(page[0]) - timedelta(days=1)).isoformat()
    return reads


def test_two_year_daily_pages_preserve_missing_and_identical_backtest_semantics(protocol_client):
    client, state = protocol_client
    missing_day = END - timedelta(days=120)  # On the boundary of the first full 121-point page.
    state.observations[SID] = [(stamp, None if stamp == missing_day.isoformat() else value)
                               for stamp, value in observation_window()]
    response = client.get('/protocol-backtest')
    assert response.status_code == 200, response.text
    payload = response.json()
    start = END - timedelta(days=731)
    assert payload['start'] == start.isoformat() and payload['end'] == END.isoformat()
    expected = [{'date': stamp, 'value': value} for stamp, value in state.observations[SID]
                if start.isoformat() <= stamp <= END.isoformat()]
    assert payload['series'][0]['points'] == expected and len(expected) == 732
    states = {item['date']: item['rule_states'][0] for item in payload['daily_states']}
    assert states[start.isoformat()] is True  # Warmup still covers changes before the displayed range.
    for offset in range(3):
        assert states[(missing_day + timedelta(days=offset)).isoformat()] is None
    assert states[(missing_day + timedelta(days=3)).isoformat()] is True
    assert payload['trigger_dates'] == [(missing_day + timedelta(days=3)).isoformat()]
    reads = assert_paged_on_real_dates(state)
    assert len(reads) >= 7


def test_sparse_mixed_series_pages_join_by_actual_cutoffs_without_filling(protocol_client):
    client, state = protocol_client
    state.rules = [rule(), rule(OTHER)]
    state.observations[SID] = observation_window()
    state.observations[OTHER] = observation_window(size=800, interval=3)
    response = client.get('/protocol-backtest')
    assert response.status_code == 200, response.text
    payload = response.json()
    for chart in payload['series']:
        expected = [{'date': stamp, 'value': value} for stamp, value in state.observations[chart['series_id']]
                    if payload['start'] <= stamp <= payload['end']]
        assert chart['points'] == expected
        assert len({point['date'] for point in chart['points']}) == len(expected)
    assert_paged_on_real_dates(state)


def test_source_snapshot_change_between_pages_is_rejected(protocol_client):
    client, state = protocol_client
    state.observations[SID] = observation_window()
    state.on_read = lambda body, count: 'b' * 64 if count == 2 else None
    response = client.get('/protocol-backtest')
    assert response.status_code == 409, response.text
    assert '变化' in response.json()['detail']
    assert state.reads == 2
    assert_paged_on_real_dates(state)


def test_source_change_between_distinct_frequency_windows_is_rejected(protocol_client):
    client, state = protocol_client
    state.rules = [rule(), rule(OTHER)]
    state.frequencies[OTHER] = '月'
    state.observations[SID] = observation_window()
    state.observations[OTHER] = observation_window(size=100, interval=30)
    state.on_read = lambda body, count: 'b' * 64 if OTHER in body['series_ids'] else None
    response = client.get('/protocol-backtest')
    assert response.status_code == 409, response.text
    assert '变化' in response.json()['detail']
    assert state.reads >= 2
    assert_paged_on_real_dates(state)


def test_monthly_window_covers_five_years_and_limited_history_is_disclosed(protocol_client):
    client, state = protocol_client
    state.frequencies[SID] = '月'
    state.observations[SID] = observation_window(size=100, interval=30)
    response = client.get('/protocol-backtest')
    assert response.status_code == 200, response.text
    payload = response.json()
    assert payload['start'] == (END - timedelta(days=5 * 365 + 1)).isoformat()
    assert payload['series'][0]['points'] == [
        {'date': stamp, 'value': value} for stamp, value in state.observations[SID]
        if stamp >= payload['start']]
    assert_paged_on_real_dates(state)
    state.requests.clear()
    state.observations[SID] = state.observations[SID][-8:]
    response = client.get('/protocol-backtest')
    assert response.status_code == 200, response.text
    payload = response.json()
    coverage = payload['series'][0]['coverage']
    assert coverage['start'] == state.observations[SID][0][0]
    assert coverage['end'] == END.isoformat() and coverage['point_count'] == 8
    assert coverage['start'] in payload['series'][0]['notice']
    assert coverage['end'] in payload['series'][0]['notice']
    assert payload['notices']


def test_six_year_daily_request_reports_capped_actual_coverage_instead_of_413(protocol_client):
    client, state = protocol_client
    state.observations[SID] = observation_window(size=2300)
    start = (END - timedelta(days=6 * 365)).isoformat()
    response = client.get('/protocol-backtest', params={'start': start})
    assert response.status_code == 200, response.text
    payload = response.json()
    coverage = payload['series'][0]['coverage']
    assert coverage['point_count'] == coverage['point_limit'] == 2000
    assert coverage['start'] == (END - timedelta(days=1999)).isoformat()
    assert coverage['end'] == END.isoformat()
    assert coverage['start'] in payload['series'][0]['notice']
    assert payload['series'][0]['limited'] and payload['notices']
    assert len(assert_paged_on_real_dates(state)) == 17


def test_current_monitor_check_and_saved_backtest_share_the_protocol_reader(protocol_client):
    client, state = protocol_client
    state.observations[SID] = observation_window()
    sentence = '隔离协议测试提醒'
    with artifact_session(state.scope) as session:
        draft = monitors._new_artifact(state.scope, monitors.DRAFT_MODULE, '隔离草稿',
            {'workbench_id': WB, 'sentence': sentence, 'candidates': [{'series_id': SID}]})
        session.add(draft)
        session.commit()
        draft_id = draft.id
    saved = client.post(URL, json={'draft_id': draft_id, 'title': '隔离协议测试',
        'sentence': sentence, 'logic': 'all', 'rules': state.rules, 'enabled': True})
    assert saved.status_code == 201, saved.text
    state.requests.clear()
    response = client.post('/protocol-check')
    assert response.status_code == 200, response.text
    assert response.json()['checked'] == 1 and response.json()['partial'] is False
    assert client.get(URL).json()['items'][0]['current_state'] == '触发'
    assert_paged_on_real_dates(state)
    state.requests.clear()
    response = client.get(URL + '/' + saved.json()['id'] + '/backtest')
    assert response.status_code == 200, response.text
    assert_paged_on_real_dates(state)


@pytest.mark.parametrize('changes', [
    {'points_per_series': 2000}, {'view_module': 'indicators'}, {'points_per_series': 122},
    {'points_per_series': True}, {'series_ids': [SID] * 2},
    {'series_ids': ['kb-' + format(index, '048x') for index in range(166)]},
    {'series_ids': ['kb-' + format(index, '048x') for index in range(257)], 'points_per_series': 1},
])
def test_equivalent_protocol_guard_rejects_original_failure_and_resource_overruns(changes):
    body = {'source_bindings': [source(1)['id']], 'series_ids': [SID],
            'view_module': 'judgments', 'include_invalid': True, 'points_per_series': 121,
            'as_of': END.isoformat(), **changes}
    with pytest.raises(AssertionError):
        assert_protocol(body)
