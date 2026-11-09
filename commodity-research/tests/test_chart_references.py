"""Chart references freeze real shared revisions, never text or client snapshots."""
from copy import deepcopy

from fastapi import Body
import pytest

from tin.integration.workbench_scope import artifact_session, current_workbench
from tin.web import chart_references as refs
from tin.web.reference_writes import fence_reference
from tests.chart_fixtures import SID, OTHER, definition
from tests.test_chart_api import chart_client, SECOND_WB


@pytest.fixture
def reference_client(chart_client):
    client, state = chart_client
    state.app.include_router(refs.router)

    @state.app.post('/freeze')
    def freeze(body: dict = Body()):
        scope = current_workbench.get()
        with artifact_session(scope) as session:
            values = refs.freeze_chart_references(session, scope, body['references'], body.get('as_of', '2023-01-02'))
            session.commit()
            return values

    @state.app.post('/fence')
    def fence(body: dict = Body()):
        scope = current_workbench.get()
        with artifact_session(scope) as session:
            fence_reference(session, scope, body)
            session.commit()
        return {'ok': True}

    return client, state


def create(client, **changes):
    response = client.post('/api/sn/charts', json=definition(**changes))
    assert response.status_code == 201, response.text
    return response.json()


def frozen(client, row, **changes):
    value = {'kind': 'chart', 'id': row['id'], 'revision': row['revision'], **changes}
    response = client.post('/freeze', json={'references': [value]})
    assert response.status_code == 200, response.text
    return response.json()[0]


def render(client, pin, **changes):
    return client.post('/api/sn/chart-references/render', json={'reference': pin, **changes})


def test_freeze_server_fields_and_attachment_targets(reference_client):
    client, state = reference_client
    row = create(client)
    target = {'kind': 'threshold', 'id': 'rule-one'}
    pin = frozen(client, row, title='forged', definition={'title': 'forged'}, source_version='forged', target=target)
    assert pin['title'] == '测试图' and pin['definition']['curves'][0]['series_id'] == SID
    assert pin['definition_summary'] == '走势 · 测试曲线'
    assert pin['source_version'] == state.facts_version
    assert pin['source_versions'] == {SID: state.facts_version}
    assert pin['as_of'] == '2023-01-02' and pin['target'] == target
    second = client.post('/freeze', json={'references': [pin, {**pin, 'target': {'kind': 'judgment'}}]})
    assert second.status_code == 200 and len(second.json()) == 2


def test_cutoff_old_revision_current_toggle_and_deleted_status(reference_client):
    client, state = reference_client
    row = create(client)
    pin = frozen(client, row)
    assert client.patch('/api/sn/charts/' + row['id'], json={'revision': 1, 'title': '修订后的标题'}).status_code == 200
    old = render(client, {**pin, 'definition': definition(title='forged')})
    assert old.status_code == 200, old.text
    value = old.json()
    assert value['status'] == 'updated' and value['notice'] == '已有新修订'
    assert value['title'] == value['definition']['title'] == '测试图'
    assert value['payload']['revision'] == 1 and value['as_of'] == pin['as_of']
    assert [p['date'] for p in value['payload']['curves'][0]['points']] == ['2023-01-01', '2023-01-02']
    current = render(client, pin, current=True).json()
    assert current['current'] is True and current['as_of'] > pin['as_of']
    assert len(current['payload']['curves'][0]['points']) == 4
    assert current['definition'] == value['definition']
    assert client.delete('/api/sn/charts/' + row['id'] + '?revision=2').status_code == 200
    tombstone = render(client, pin)
    assert tombstone.status_code == 200 and tombstone.json()['notice'] == '图表已删除'
    assert tombstone.json()['title'] == pin['title']


def test_freeze_rejects_stale_deleted_foreign_and_missing_metrics(reference_client):
    client, state = reference_client
    row = create(client)
    pin = frozen(client, row)
    assert client.post('/freeze', json={'references': [pin]}, headers={'x-workbench': SECOND_WB}).status_code == 404
    assert render(client, pin, current=True).status_code == 200
    assert client.patch('/api/sn/charts/' + row['id'], json={'revision': 1, 'title': '新修订'}).status_code == 200
    assert client.post('/freeze', json={'references': [pin]}).status_code == 409
    pin['revision'] = 2
    del state.fields[SID]
    assert client.post('/freeze', json={'references': [pin]}).status_code == 422
    assert client.delete('/api/sn/charts/' + row['id'] + '?revision=2').status_code == 200
    assert client.post('/freeze', json={'references': [pin]}).status_code == 404


@pytest.mark.parametrize('path', ['/api/sn/chart-references/options', '/api/sn/chart-references/options?favorites=true'])
def test_restricted_and_scope_v1_reject_before_read(reference_client, path):
    client, state = reference_client
    count = len(state.requests)
    response = client.get(path, headers={'x-restricted': 'true'})
    assert response.status_code == 403 and response.json()['detail'] == refs.RESTRICTED_NOTICE
    assert client.get(path, headers={'x-scope-version': '1'}).status_code == 409
    assert len(state.requests) == count


def test_shared_chart_does_not_require_author_ownership_but_remains_workbench_scoped(reference_client):
    client, _ = reference_client
    pin = frozen(client, create(client))
    assert render(client, pin).status_code == 200
    other = client.post('/freeze', json={'references': [pin]}, headers={'x-actor': 'second'})
    assert other.status_code == 200
    assert client.post('/api/sn/chart-references/render', json={'reference': pin}, headers={'x-workbench': SECOND_WB}).status_code == 404
    denied = client.post('/api/sn/chart-references/render', json={'reference': pin}, headers={'x-restricted': 'true'})
    assert denied.status_code == 403 and pin['title'] not in denied.text


def test_options_search_group_favorites_and_related_only_real_ids(reference_client):
    client, state = reference_client
    client.app.include_router(__import__('tin.web.favorites', fromlist=['router']).router)
    first = create(client, title='共同显示名称', group='组甲')
    create(client, title='共同显示名称', group='组乙', curves=[{'series_id': OTHER, 'label': '共同显示名称'}])
    ids = client.get('/api/sn/chart-references/options', params={'series_id': SID}).json()
    assert [item['id'] for item in ids['items']] == [first['id']]
    assert ids['groups'] == ['组乙', '组甲']
    assert client.get('/api/sn/chart-references/options', params={'series_id': '共同显示名称'}).json()['total'] == 0
    assert client.get('/api/sn/chart-references/options?q=共同&group=组甲').json()['total'] == 1
    assert client.get('/api/sn/chart-references/options?favorites=true').json()['total'] == 0
    assert client.put('/api/sn/favorites', json={'revision': 0, 'items': [{'kind': 'chart', 'id': first['id']}]}).status_code == 200
    assert client.get('/api/sn/chart-references/options?favorites=true').json()['items'][0]['id'] == first['id']
    assert client.get('/api/sn/chart-references/options?favorites=true', headers={'x-actor': 'second'}).json()['total'] == 0


def test_fences_and_explicit_references_ignore_prose(reference_client):
    from tin.web.research_references import declared_references
    client, _ = reference_client
    row = create(client)
    pin = frozen(client, row)
    assert declared_references({'thesis': 'chart ' + row['id']}) == []
    assert declared_references({'citations': [pin]}) == [pin]
    assert declared_references({'chart_references': [pin]}) == [pin]
    assert client.post('/fence', json=pin).status_code == 200
    assert client.delete('/api/sn/charts/' + row['id'] + '?revision=1').status_code == 200
    assert client.post('/fence', json=pin).status_code == 404


def test_source_version_change_is_disclosed_and_default_mapping_is_hint_only(reference_client, monkeypatch):
    client, state = reference_client
    pin = frozen(client, create(client))
    state.facts_version = 'a' * 64
    monkeypatch.setattr('tin.integration.indicator_dedup.confirmed_replacements', lambda scope, ids: {SID: OTHER})
    response = render(client, pin)
    assert response.status_code == 200, response.text
    data = response.json()
    assert data['source_version_changed'] is True and '按原截止日重算' in data['data_notice']
    assert data['dedup_notice'] == '可改为引用 default 库指标'
    assert data['definition']['curves'][0]['series_id'] == SID


def test_historical_requires_date_and_dedup_uses_frozen_revision(reference_client):
    from tin.web.dedup_references import _related_charts
    client, state = reference_client
    row = create(client)
    pin = frozen(client, row)
    missing = {key: value for key, value in pin.items() if key != 'as_of'}
    assert render(client, missing).status_code == 422
    assert client.patch('/api/sn/charts/' + row['id'], json={'revision': 1,
        'curves': [{'series_id': OTHER, 'label': '替换后的指标'}]}).status_code == 200

    @state.app.get('/related-pin')
    def related_pin():
        scope = current_workbench.get()
        with artifact_session(scope) as session:
            return [{'id': record.id, 'title': record.title, 'ids': ids}
                    for record, ids in _related_charts(session, 'strategy', {'citations': [pin]})]
    result = client.get('/related-pin')
    assert result.status_code == 200 and result.json() == [{'id': row['id'], 'title': '测试图', 'ids': [SID]}]


def test_threshold_overlays_exact_id_range_sma_missing_and_change(monkeypatch):
    from tin.schemas.chart import ChartDefinition
    from datetime import date
    samples = [{'series_id': SID, 'date': f'2023-01-0{i}', 'value': value, 'value_status': 'valid' if value is not None else 'missing'}
               for i, value in enumerate([10, 12, None, 16, 18], 1)]
    captured = []
    def facts(scope, identifiers, module, **kwargs):
        captured.append((identifiers, module, kwargs))
        return {'rows': samples}
    monkeypatch.setattr('tin.integration.page_facts.bounded_facts', facts)
    rules = [{'id': 'fixed', 'series_id': SID, 'value': [11, 17], 'operator': 'between'},
             {'id': 'same-name', 'series_id': OTHER, 'value': 99, 'name': '测试曲线'},
             {'id': 'sma', 'series_id': SID, 'value': {'kind': 'sma', 'window': 2}},
             {'id': 'delta', 'series_id': SID, 'expression': 'change >= 3'}]
    overlays = refs.threshold_overlays(None, ChartDefinition.model_validate(definition()), rules, date(2023, 1, 5))
    assert [o['value'] for o in overlays if o['kind'] == 'fixed'] == [11, 17]
    assert all(o['series_id'] == SID for o in overlays)
    assert [p['value'] for p in overlays[2]['points']] == [None, 11, None, None, 17]
    assert [p['value'] for p in overlays[3]['points']] == [None, 13, 15, None, 19]
    assert captured[0][1] == 'judgments' and captured[0][2]['include_invalid'] is True
    formula = ChartDefinition.model_validate(definition(curves=[{'label': '测试曲线', 'derived': {'op': 'sub', 'a': SID, 'b': OTHER}}]))
    assert refs.threshold_overlays(None, formula, rules, date(2023, 1, 5)) == []


@pytest.mark.parametrize('revision', [True, 0, -1, 2**100, '1'])
def test_pin_requires_bounded_real_revision(revision):
    from fastapi import HTTPException
    with pytest.raises(HTTPException) as error:
        refs._pin({'kind': 'chart', 'id': 'synthetic-chart', 'revision': revision})
    assert error.value.status_code == 422


def test_sma_pages_real_invalid_windows_under_service_limit(monkeypatch):
    from datetime import date, timedelta
    from tin.schemas.chart import ChartDefinition
    start = date(2023, 1, 1)
    source = [{'series_id': SID, 'date': (start + timedelta(days=i)).isoformat(),
        'value': None if i == 140 else i, 'value_status': 'missing' if i == 140 else 'valid'} for i in range(260)]
    calls = []
    def facts(scope, identifiers, module, *, day, points, include_invalid):
        assert points <= 121 and module == 'judgments' and include_invalid
        calls.append((day, points))
        return {'rows': [row for row in source if row['date'] <= day][-points:],
                'source_versions': {'synthetic-binding': 'f' * 64}}
    monkeypatch.setattr('tin.integration.page_facts.bounded_facts', facts)
    value = ChartDefinition.model_validate(definition(time={'mode': 'recent', 'points': 250}))
    [overlay] = refs.threshold_overlays(None, value, [{'series_id': SID, 'value': {'kind': 'sma', 'window': 3}}],
        start + timedelta(days=259))
    assert [count for _, count in calls] == [121, 121, 10]
    values = {point['date']: point['value'] for point in overlay['points']}
    assert len(values) == 252
    assert values[source[140]['date']] is None and values[source[142]['date']] is None
    assert values[source[143]['date']] == 142 and values[source[-1]['date']] == 258


def test_sma_rejects_source_change_between_pages(monkeypatch):
    from datetime import date, timedelta
    from fastapi import HTTPException
    count = 0
    def facts(scope, identifiers, module, *, day, points, include_invalid):
        nonlocal count
        count += 1
        end = date.fromisoformat(day)
        return {'rows': [{'series_id': SID, 'date': (end - timedelta(days=i)).isoformat(),
                         'value': i, 'value_status': 'valid'} for i in range(points)],
                'source_versions': {'synthetic-binding': str(count) * 64}}
    monkeypatch.setattr('tin.integration.page_facts.bounded_facts', facts)
    with pytest.raises(HTTPException) as error:
        refs.moving_observations(None, [SID], date(2023, 12, 1), 250)
    assert error.value.status_code == 409


def test_render_shares_graph_and_sma_budget_and_uses_range_cutoff(reference_client, monkeypatch):
    from datetime import date, timedelta
    from tin.integration import page_facts
    client, state = reference_client
    identifiers = ['kb-' + f'{i:048x}' for i in range(16)]
    for sid in identifiers:
        state.fields[sid] = {'series_id': sid, 'metric': '合成指标', 'unit': '件'}
        state.observations[sid] = [('2023-01-01', 10), ('2023-01-02', 11)]
    row = create(client, curves=[{'series_id': sid, 'label': '合成曲线'} for sid in identifiers],
        time={'mode': 'range', 'start': '2023-01-01', 'end': '2023-01-02'})
    pin = frozen(client, row)
    pin['as_of'] = '2023-01-04'
    original, windows = page_facts.bounded_facts, []
    def facts(scope, ids, module, **kwargs):
        if module != 'judgments':
            return original(scope, ids, module, **kwargs)
        windows.append((list(ids), kwargs['day'], kwargs['points']))
        end = date.fromisoformat(kwargs['day'])
        return {'source_versions': {'fixture-binding': 'f' * 64},
            'rows': [{'series_id': sid, 'date': (end - timedelta(days=i)).isoformat(),
                      'value': i, 'value_status': 'valid'} for sid in ids for i in range(kwargs['points'])]}
    monkeypatch.setattr(page_facts, 'bounded_facts', facts)
    response = render(client, pin, rules=[{'series_id': sid, 'value': {'kind': 'sma', 'window': 3}} for sid in identifiers])
    assert response.status_code == 200, response.text
    result = response.json()
    assert result['as_of'] == '2023-01-02' and result['research_as_of'] == '2023-01-04'
    assert result['payload']['point_limit'] == 625
    assert windows[0][1] == result['as_of']
    graph_budget = len(identifiers) * result['payload']['point_limit']
    window_budget = sum(len(ids) * points for ids, _, points in windows)
    assert graph_budget + window_budget == 20000
    assert all(points <= 121 for _, _, points in windows)
