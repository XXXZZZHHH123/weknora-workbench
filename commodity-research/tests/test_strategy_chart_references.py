"""A strategy publishes explicit chart identities and preserves their historical pins."""
from copy import deepcopy

from tests.test_chart_api import chart_client, create  # noqa: F401
from tests.test_research_strategy_cards import URL, entry
from tin.web import chart_references, research_strategy
from tests.test_strategy_workspace import add_plan_routes, plan


def setup(chart_client):
    client, state = chart_client
    state.app.include_router(research_strategy.router)
    state.app.include_router(chart_references.router)
    return client, state


def body(chart, **changes):
    value = entry(source_ids=[])
    value['card']['citations'] = [{'kind': 'chart', 'field': 'trigger',
        'id': chart['id'], 'revision': chart['revision'], 'title': 'Untrusted title',
        'as_of': '1900-01-01', 'source_version': 'untrusted',
        'definition': {'forged': True}, 'snapshot': {'title': 'Forged title'}}]
    return {**value, **changes}


def test_strategy_chart_publication_rebuilds_frozen_fields_and_ignores_prose(chart_client):
    client, state = setup(chart_client)
    chart = create(client)
    request = body(chart)
    response = client.post(URL, json=request)
    assert response.status_code == 201, response.text
    [reference] = response.json()['card']['citations']
    assert reference['id'] == chart['id'] and reference['revision'] == 1
    assert reference['source_version'] == state.facts_version
    assert reference['source_versions'] and reference['as_of'] != '1900-01-01'
    assert reference['title'] == chart['definition']['title']
    assert reference['definition']['format'] == 'chart.v1'
    assert reference['definition_summary'] and reference['snapshot']['title'] == reference['title']
    assert response.json()['card']['trigger'] == request['card']['trigger']
    assert 'forged' not in response.text.lower() and 'untrusted' not in response.text.lower()
    before = len(state.requests)
    prose = entry(source_ids=[])
    prose['card']['trigger'] = 'Mention ' + chart['id'] + ' in prose without explicitly citing it.'
    plain = client.post(URL, json=prose)
    assert plain.status_code == 201 and not plain.json()['card'].get('citations')
    assert not any(request.get('series_ids') for _, request in state.requests[before:])


def test_strategy_chart_history_keeps_exact_revision_and_checks_owner(chart_client):
    client, state = setup(chart_client)
    chart = create(client)
    saved = client.post(URL, json=body(chart))
    assert saved.status_code == 201, saved.text
    saved = saved.json()
    first = deepcopy(saved['card']['citations'][0])
    changed_definition = deepcopy(chart['definition'])
    changed_definition['title'] = 'New chart revision'
    changed = client.patch('/api/sn/charts/' + chart['id'], json={**changed_definition, 'revision': 1})
    assert changed.status_code == 200, changed.text
    assert client.patch(URL + '/' + saved['id'], json=body(chart, revision=1)).status_code == 409
    second = client.patch(URL + '/' + saved['id'], json=body(changed.json(), revision=1))
    assert second.status_code == 200, second.text
    history = client.get(URL + '/' + saved['id'] + '?revision=1')
    assert history.status_code == 200, history.text
    assert history.json()['card']['citations'][0] == first
    assert history.json()['historical'] is True and history.json()['can_edit'] is False
    versions = client.get(URL + '/' + saved['id'] + '/revisions').json()
    assert [row['revision'] for row in versions['items']] == [2, 1]
    assert client.get(URL + '/' + saved['id'] + '?revision=999').status_code == 404
    assert client.get(URL + '/' + saved['id'] + '?revision=1', headers={'x-actor': 'second'}).status_code == 404
    assert client.get(URL + '/' + saved['id'] + '/revisions', headers={'x-actor': 'second'}).status_code == 404
    assert client.get(URL + '/' + saved['id'] + '?revision=1', headers={'x-restricted': 'true'}).status_code == 403


def test_strategy_monitor_renders_only_explicit_chart_pins_and_keeps_deleted_title(chart_client):
    client, _ = setup(chart_client)
    chart = create(client)
    saved = client.post(URL, json=body(chart))
    assert saved.status_code == 201, saved.text
    saved = saved.json()
    response = client.get(URL + '/' + saved['id'] + '/monitor')
    assert response.status_code == 200, response.text
    [rendered] = response.json()['charts']
    reference = saved['card']['citations'][0]
    assert rendered['reference']['id'] == chart['id'] and rendered['as_of'] == reference['as_of']
    assert rendered['payload'] and rendered['field_label'] == '适用 / 触发'
    dated = client.get(URL + '/' + saved['id'] + '/monitor?date=2023-01-02').json()['charts'][0]
    assert dated['as_of'] == dated['monitor_as_of'] == '2023-01-02'
    assert dated['frozen_as_of'] == reference['as_of']
    assert dated['reference']['revision'] == reference['revision']
    deleted = client.delete('/api/sn/charts/' + chart['id'], params={'revision': 1})
    assert deleted.status_code == 200, deleted.text
    response = client.get(URL + '/' + saved['id'] + '/monitor')
    assert response.status_code == 200, response.text
    [rendered] = response.json()['charts']
    assert rendered['status'] == 'deleted' and rendered['notice'] == '图表已删除'
    assert rendered['title'] == reference['title'] and rendered['payload']
    assert client.post(URL, json=body(chart)).status_code == 404


def test_strategy_chart_cannot_publish_foreign_or_restricted_reference(chart_client):
    client, _ = setup(chart_client)
    chart = create(client)
    assert client.post(URL, json=body(chart), headers={'x-workbench': '10000000-0000-4000-8000-000000000092'}).status_code == 404
    assert client.post(URL, json=body(chart), headers={'x-restricted': 'true'}).status_code == 403
    assert client.post(URL, json=body(chart), headers={'x-can-edit': 'false'}).status_code == 403


def test_trade_plan_keeps_explicit_chart_pin_on_its_own_research_date_and_detail(chart_client):
    from pathlib import Path
    from jinja2 import ChoiceLoader, DictLoader, Environment, FileSystemLoader
    client, _ = setup(chart_client)
    add_plan_routes(client)
    chart = create(client)
    document = {**plan(), 'chart_references': [{'kind': 'chart', 'id': chart['id'], 'revision': 1}]}
    response = client.post('/api/sn/strategies', json=document)
    assert response.status_code == 201, response.text
    saved = response.json()
    [reference] = saved['plan']['chart_references']
    assert reference['as_of'] == '2024-01-01' and reference['revision'] == 1
    template_root = Path(__file__).resolve().parents[1] / 'src/tin/web/templates'
    environment = Environment(loader=ChoiceLoader([
        DictLoader({'base.html': '{% block main %}{% endblock %}'}), FileSystemLoader(template_root)]))
    environment.globals.update(research_workbench=lambda: None, research_prefix=lambda: '/scoped',
        research_static_prefix=lambda: '', static_version=lambda: 'test',
        human_text=lambda value, *_: value or '', indicator_name=lambda *_: 'Fixture')
    context = dict(strategy=saved, strategy_can_write=False, strategy_scope_status='current',
        status_labels={'draft': '草稿'}, kind_labels={'outright': '方向策略'}, mode_labels={'simulation': '模拟跟踪'}, names={})
    html = environment.get_template('strategy_detail.html').render(**context, restricted_view=False)
    assert 'strategy-detail-charts' in html and 'ChartReferences.mount' in html and chart['id'] in html
    restricted = environment.get_template('strategy_detail.html').render(**context, restricted_view=True)
    assert '图表引用暂不对受限视角开放' in restricted and chart['id'] not in restricted
    copied = client.post('/api/sn/strategies/' + str(saved['id']) + '/draft',
        json={'base_version': 1, 'plan': saved['plan']})
    assert copied.status_code == 201, copied.text
    assert copied.json()['plan']['chart_references'] == saved['plan']['chart_references']


def test_restricted_shared_strategy_text_omits_chart_definitions_but_keeps_authored_prose(chart_client, monkeypatch):
    import json
    from tin.integration.workbench_scope import artifact_session, current_workbench
    from tin.models.workbench import WorkbenchArtifact
    from tin.web import restricted_views
    client, state = setup(chart_client)
    scopes = []
    state.on_catalog = lambda _page: scopes.append(current_workbench.get())
    chart = create(client)
    saved = client.post(URL, json=body(chart))
    assert saved.status_code == 201, saved.text
    with artifact_session(scopes[0]) as session:
        row = session.get(WorkbenchArtifact, saved.json()['id'])
        value = json.loads(row.body)
        reference = value['citations'][0]
        value['references'] = [{'kind': 'chart', 'id': chart['id'], 'title': 'Legacy chart title'}]
        value['source_context'] = {'retained_note': 'literal authored note', 'legacy_snapshot': {
            'chart_references': [reference], 'reference': reference,
            'items': [{'kind': 'chart', 'id': chart['id'], 'definition': chart['definition']}],
            'authored_text': 'The word chart in prose remains literal.'}}
        row.body = json.dumps(value)
        session.commit()
    monkeypatch.setattr(restricted_views, 'shared_owner_id', lambda scope: scope.identity.user_id)
    @state.app.get('/fixture/shared-strategies')
    def shared():
        scope = current_workbench.get()
        with artifact_session(scope) as session:
            return restricted_views.shared_artifacts(session, scope, 'strategies')
    restricted = client.get('/fixture/shared-strategies', headers={'x-restricted': 'true'})
    assert restricted.status_code == 200, restricted.text
    [row] = restricted.json()
    parsed = json.loads(row['text'])
    assert parsed['trigger'] == saved.json()['card']['trigger']
    assert not parsed['citations'] and chart['id'] not in restricted.text
    assert 'definition_summary' not in restricted.text and 'source_versions' not in restricted.text
    assert not parsed['references'] and parsed['source_context']['retained_note'] == 'literal authored note'
    assert parsed['source_context']['legacy_snapshot'] == {
        'items': [], 'authored_text': 'The word chart in prose remains literal.'}
