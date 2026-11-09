"""Builder permissions, explicit template metadata and atomic chart collections."""
from datetime import date, timedelta

import pytest

from tests.chart_fixtures import FIELDS, OTHER, SID, THIRD, definition
from tests.test_chart_api import chart_client, create


def cycle_time(start='01-01', **extra):
    return {'mode': 'cycle', 'template': {'kind': 'repeat', 'name': '研究员模板',
            'start': start, 'period': 'year'}, **extra}


def native_definition(client, start, *, title='标题不能决定默认值', sheet='测试表', source_file='fixture.xlsx', period='year'):
    time = cycle_time(start)
    time['template']['period'] = period
    return create(client, title=title, origin={'kind': 'native', 'ref': f'nc-{start}-{title}'},
        curves=[{'series_id': SID, 'label': title, 'descriptor': {'source_file': source_file, 'sheet': sheet}}],
        time=time)


def test_builder_template_counts_explicit_imported_sheet_metadata_only(chart_client):
    client, state = chart_client
    create(client, group='已有分组', time=cycle_time('10-01'))
    native_definition(client, '01-01')
    native_definition(client, '09-01', title='一')
    native_definition(client, '09-01', title='二')
    for index in range(3):
        native_definition(client, '10-01', title=str(index), sheet='另一个明确工作表')
        native_definition(client, '11-01', title=str(index), source_file='另一个明确来源.xlsx')
    state.requests.clear()
    result = client.get('/api/sn/charts/builder-options', params={'series_id': OTHER})
    assert result.status_code == 200, result.text
    assert result.json()['template']['start'] == '09-01'
    assert result.json()['template_source'] == 'native_sheet'
    assert '已有分组' in result.json()['groups']
    assert all(body.get('catalog_only') for _, body in state.requests)
    # A manual title, descriptor metric or missing imported binding is never a fallback.
    del state.fields[SID]
    result = client.get('/api/sn/charts/builder-options', params={'series_id': OTHER}).json()
    assert result['template']['start'] == '01-01' and result['template_source'] == 'natural_year'


def test_builder_default_tie_is_stable_and_missing_id_never_inferred(chart_client):
    client, _ = chart_client
    native_definition(client, '09-01')
    native_definition(client, '01-01')
    result = client.get('/api/sn/charts/builder-options', params={'series_id': SID}).json()
    assert result['template']['start'] == '09-01'
    assert client.get('/api/sn/charts/builder-options?series_id=invented').status_code == 422
    assert client.get('/api/sn/charts/builder-options').json()['template_source'] == 'natural_year'


def test_builder_counts_start_across_periods_and_checks_real_document_identity(chart_client):
    client, state = chart_client
    for period in ('year', 'half', 'quarter'):
        native_definition(client, '09-01', title=period, period=period)
    for index in range(2):
        native_definition(client, '01-01', title=str(index))
    selected = client.get('/api/sn/charts/builder-options', params={'series_id': OTHER}).json()
    assert selected['template']['start'] == '09-01' and selected['template']['period'] == 'year'

    def different_dataset(payload):
        for field in payload['series']:
            if field['series_id'] == OTHER:
                field['dataset_id'] = 'a-different-real-dataset'

    state.on_catalog = different_dataset
    fallback = client.get('/api/sn/charts/builder-options', params={'series_id': OTHER}).json()
    assert fallback['template_source'] == 'natural_year' and fallback['template']['start'] == '01-01'


def test_batch_preview_does_not_save_and_confirmation_creates_one_chart_per_indicator(chart_client):
    client, _ = chart_client
    native_definition(client, '09-01')
    body = {'series_ids': [SID, OTHER, THIRD], 'group': '研究员新图集'}
    preview = client.post('/api/sn/charts/batch/preview', json=body)
    assert preview.status_code == 200, preview.text
    items = preview.json()['items']
    assert len(items) == 3 and client.get('/api/sn/charts').json()['total'] == 1
    assert all(item['definition']['time']['template']['start'] == '09-01' for item in items)
    saved = client.post('/api/sn/charts/batch', json=body)
    assert saved.status_code == 201, saved.text
    charts = saved.json()['created']
    assert len(charts) == 3
    assert [chart['definition']['curves'][0]['series_id'] for chart in charts] == body['series_ids']
    assert all(chart['definition']['group'] == body['group'] and chart['revision'] == 1 for chart in charts)
    assert client.get('/api/sn/charts', params={'group': body['group']}).json()['total'] == 3


@pytest.mark.parametrize('body', [
    {'series_ids': [SID, OTHER, 'kb-' + 'd' * 48], 'group': '整组拒绝'},
    {'series_ids': [SID, SID], 'group': '重复'},
    {'series_ids': [SID] * 17, 'group': '超限'},
    {'series_ids': [SID], 'group': '   '},
    {'series_ids': [SID], 'group': '图集', 'cycles': True},
    {'series_ids': [SID], 'group': '图集', 'template': {'kind': 'repeat', 'name': '坏', 'start': '02-30', 'period': 'year'}},
])
def test_invalid_batch_never_partially_writes(chart_client, body):
    client, _ = chart_client
    assert client.post('/api/sn/charts/batch', json=body).status_code == 422
    assert client.get('/api/sn/charts').json()['total'] == 0


def test_preview_and_batch_preview_readers_and_shared_write_permission(chart_client):
    client, _ = chart_client
    viewer = {'x-role': 'viewer', 'x-can-edit': 'false'}
    assert client.post('/api/sn/charts/preview', json=definition(), headers=viewer).status_code == 200
    body = {'series_ids': [SID], 'group': '图集'}
    assert client.post('/api/sn/charts/batch/preview', json=body, headers=viewer).status_code == 200
    assert client.post('/api/sn/charts/batch', json=body, headers=viewer).status_code == 403
    assert client.get('/api/sn/charts').json()['total'] == 0


def test_preview_and_saved_data_share_derived_date_alignment_and_transforms(chart_client):
    client, state = chart_client
    state.observations[OTHER] = [('2023-01-02', 2), ('2023-01-04', 4)]
    value = definition(curves=[{'derived': {'op': 'sub', 'a': SID, 'b': OTHER}, 'label': '自定义差值'}],
                       time={'mode': 'since', 'start': '2023-01-03', 'transform': 'rebase'})
    preview = client.post('/api/sn/charts/preview?date=2023-01-04', json=value)
    assert preview.status_code == 200, preview.text
    curve = preview.json()['curves'][0]
    assert curve['points'] == [{'date': '2023-01-04', 'raw': 9, 'value': 100}]
    saved = client.post('/api/sn/charts', json=value).json()
    data = client.get('/api/sn/charts/' + saved['id'] + '/data?date=2023-01-04').json()
    assert data['curves'] == preview.json()['curves']
    assert client.get('/api/sn/charts', params={'series_id': OTHER}).json()['total'] == 1
    assert client.get('/api/sn/charts/' + saved['id']).json()['curves'][0]['status'] == 'linked'
    # Persist formulas only; no observations or computed values enter definitions.
    assert 'points' not in saved['definition']['curves'][0]


def test_trend_transform_overflow_is_a_missing_point(chart_client):
    client, state = chart_client
    state.observations[SID] = [('2023-01-01', 1e-300), ('2023-01-02', 1e300)]
    result = client.post('/api/sn/charts/preview?date=2023-01-02', json=definition(
        time={'mode': 'since', 'start': '2023-01-01', 'transform': 'rebase'}))
    assert result.status_code == 200, result.text
    assert result.json()['curves'][0]['points'][-1]['value'] is None
    assert result.json()['warnings'][-1]['code'] == 'transform_non_finite'


def test_multi_cycle_preview_returns_per_curve_statistics_and_suppresses_band(chart_client):
    client, state = chart_client
    for sid, offset in ((SID, 0), (OTHER, 10)):
        state.observations[sid] = [((date(year, 1, 1) + timedelta(days=day)).isoformat(), year - 2010 + day + offset)
                                  for year in range(2020, 2024)
                                  for day in range(3 if year == 2023 else (date(year + 1, 1, 1) - date(year, 1, 1)).days)]
    value = definition(curves=[{'series_id': SID, 'label': '甲', 'curve_cycles': 'current'},
                               {'series_id': OTHER, 'label': '乙', 'curve_cycles': 'current_and_previous'}],
                       time=cycle_time())
    response = client.post('/api/sn/charts/preview?date=2023-01-03', json=value)
    assert response.status_code == 200, response.text
    data = response.json()
    assert data['band'] == [] and len(data['curves']) == 2
    assert all(curve['cycle']['position']['n'] == 3 for curve in data['curves'])
    assert all(curve['cycle']['position']['rank'] == 1 for curve in data['curves'])


def test_anomalies_include_each_multi_curve_and_formula(chart_client):
    client, state = chart_client
    for sid, offset in ((SID, 0), (OTHER, 10)):
        state.observations[sid] = [((date(year, 1, 1) + timedelta(days=day)).isoformat(), year - 2010 + day + offset)
                                  for year in range(2020, 2024)
                                  for day in range(3 if year == 2023 else (date(year + 1, 1, 1) - date(year, 1, 1)).days)]
    direct = create(client, curves=[{'series_id': SID, 'label': '甲'}, {'series_id': OTHER, 'label': '乙'}], time=cycle_time())
    formula = create(client, curves=[{'derived': {'op': 'sub', 'a': SID, 'b': OTHER}, 'label': '研究员公式'}], time=cycle_time())
    result = client.get('/api/sn/charts/anomalies?date=2023-01-03')
    assert result.status_code == 200, result.text
    data = result.json()
    assert data['failed'] == [] and data['total'] == 3
    assert len([item for item in data['items'] if item['id'] == direct['id']]) == 2
    derived = next(item for item in data['items'] if item['id'] == formula['id'])
    assert derived['series_id'] is None and derived['metric'] == '研究员公式'
    assert derived['derived']['op'] == 'sub' and derived['n'] == 3
    limited = client.get('/api/sn/charts/anomalies?date=2023-01-03&limit=1').json()
    assert limited['total'] == 3 and len(limited['items']) == 1


def test_batch_preview_caps_total_operand_observations(chart_client):
    client, state = chart_client
    ids = ['kb-' + f'{index:048x}' for index in range(16)]
    for sid in ids:
        state.fields[sid] = {**FIELDS[SID], 'series_id': sid}
        state.observations[sid] = state.observations[SID]
    result = client.post('/api/sn/charts/batch/preview', json={'series_ids': ids, 'group': '大图集'})
    assert result.status_code == 200, result.text
    reads = [body for _, body in state.requests if 'points_per_series' in body]
    assert sum(len(body['series_ids']) * body['points_per_series'] for body in reads) <= 20000
    assert result.json()['point_limit'] == 1250


def test_new_readonly_previews_pass_real_scoped_auth_middleware(chart_client, monkeypatch):
    from tin.config import settings
    from tin.integration import auth, workbench_scope
    from tin.integration.identity import Identity
    from tin.integration.workbench_scope import WorkbenchScope
    from tests.test_workbench_sources_v2 import WB, source

    client, state = chart_client
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://fixture.invalid')
    monkeypatch.setattr(settings, 'public_prefix', '')
    viewer = Identity('first', 'fixture@test', 'Fixture', 17, 'viewer')

    async def verify(*args, **kwargs):
        return viewer

    async def resolve(identity, identifier, authorization):
        return WorkbenchScope(identity, identifier, '', 0, {'workspace_mode': 'isolated', 'can_edit': False},
                              authorization, scope_version=2, sources_revision=7, sources=(source(1),))

    monkeypatch.setattr(auth, 'verify_bearer', verify)
    monkeypatch.setattr(workbench_scope, 'resolve_workbench', resolve)
    state.app.add_middleware(auth.ResearchAuthMiddleware)
    headers = {'authorization': 'Bearer fixture', 'origin': 'http://testserver', 'x-role': 'viewer', 'x-can-edit': 'false'}
    root = '/workbenches/' + WB
    assert client.post(root + '/api/sn/charts/preview', json=definition(), headers=headers).status_code == 200
    assert client.post(root + '/api/sn/charts/batch/preview', json={'series_ids': [SID], 'group': '图集'},
                       headers=headers).status_code == 200
    assert client.get(root + '/api/sn/charts', headers=headers).json()['total'] == 0
