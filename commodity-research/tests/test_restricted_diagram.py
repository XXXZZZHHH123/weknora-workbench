"""The authorized diagram uses the complete normal renderer, never a tenant fallback."""
import json
import re
from datetime import datetime, timezone

import pytest

from tests.test_knowledge_facts import canonical, api, page, FIRST, SECOND, SERIES, KB
from tests.test_restricted_views import delegated


def layout():
    return {'nodes': [
        {'id': 'upstream', 'kind': 'group', 'label': 'Supply group', 'x': 10, 'y': 20, 'w': 320, 'h': 220,
         'binding': {'kind': 'agg', 'op': 'sum', 'members': ['measured']}},
        {'id': 'measured', 'parent': 'upstream', 'label': 'Measured component', 'x': 25, 'y': 60,
         'w': 180, 'h': 100, 'binding': {'kind': 'series', 'series_id': SERIES, 'proxy': True},
         'statics': [{'label': 'survey', 'value': '32%'}]},
        {'id': 'downstream', 'kind': 'group', 'label': 'Demand group', 'x': 400, 'y': 20, 'w': 260, 'h': 220},
        {'id': 'annotation', 'parent': 'downstream', 'label': 'Historical reference', 'x': 420, 'y': 60,
         'w': 180, 'h': 100, 'binding': {'kind': 'series', 'series_id': 'legacy.retired'},
         'statics': [{'label': 'note', 'value': '<script>doNotRun()</script>'}]},
    ], 'edges': [{'from': 'upstream', 'to': 'downstream', 'kind': 'flow'}]}


def existing_diagram(legacy=False):
    from tin.integration.workbench_scope import isolated_session_factory, result_session_factory
    from tin.db import tenant_database_path
    from tin.models import DiagramTemplate
    factory = result_session_factory(tenant_database_path(17)) if legacy else isolated_session_factory(17, FIRST)
    with factory() as session:
        now = datetime.now(timezone.utc)
        row = DiagramTemplate(variety='SN', name='Full authorized diagram', layout=layout(),
                              is_default=True, created_at=now, updated_at=now)
        session.add(row)
        session.commit()
        return row.id


def embedded(response, variable):
    match = re.search(r'window\.' + re.escape(variable) + r'\s*=\s*(.*?);\s*\n', response.text)
    assert match, variable
    return json.loads(match.group(1))


def test_existing_isolated_diagram_renders_complete_graph_without_manual_resave(canonical):
    from tin.integration.workbench_scope import isolated_session_factory
    client, headers, state, _ = canonical
    existing_diagram()
    # The prior editor recorded origins before this release's snapshot feature.
    from tin.models import SourceMapping
    with isolated_session_factory(17, FIRST)() as session:
        session.add(SourceMapping(id='workbench-series-origins:' + FIRST,
            config={'origins': {SERIES: {'knowledge_base_id': KB, 'tenant_id': 17}}},
            confirmed_by='researcher', confirmed_at=datetime.now(timezone.utc)))
        session.commit()
    state['bounded_previous'] = 10
    delegated(state, ('diagram',))
    response = client.get(page(FIRST, '/sn/diagram'), headers=headers)
    assert response.status_code == 200, response.text
    template = embedded(response, 'DG_TEMPLATE')
    assert template['layout'] == layout()
    assert 'id="dg-canvas"' in response.text and '/static/diagram.js?' in response.text
    assert 'id="dg-md-drawer"' not in response.text and 'id="dg-drawer"' not in response.text
    assert 'onclick="dgToggleEdit()"' not in response.text
    assert '<script>doNotRun()</script>' not in response.text
    values = {item['node_id']: item for item in embedded(response, 'DG_RESTRICTED_DATA')['nodes']}
    assert values['measured']['value'] == 17.5 and values['measured']['delta'] == 7.5
    assert values['measured']['proxy'] is True
    assert values['upstream']['value'] == 17.5
    assert values['annotation']['value'] is None
    assert state['bounded_requests'][-1]['series_ids'] == [SERIES]
    assert state['pages'] == 0


def test_restricted_diagram_lists_and_switches_all_workbench_layouts(canonical):
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import DiagramTemplate
    from lxml.html import fromstring
    client, headers, state, _ = canonical
    first_id = existing_diagram()
    with isolated_session_factory(17, FIRST)() as session:
        now = datetime.now(timezone.utc)
        row = DiagramTemplate(variety='SN', name='Second authorized layout',
            layout={'nodes': [{'id': 'second-only', 'label': 'Second layout node',
                              'x': 20, 'y': 20, 'w': 160, 'h': 80}], 'edges': []},
            is_default=False, created_at=now, updated_at=now)
        session.add(row)
        session.commit()
        second_id = row.id
    delegated(state, ('diagram',))
    first = client.get(page(FIRST, '/sn/diagram'), headers=headers)
    assert first.status_code == 200, first.text
    options = fromstring(first.text).xpath('//select[@id="dg-template"]/option')
    assert [(option.get('value'), option.text_content()) for option in options] == [
        (str(first_id), 'Full authorized diagram（默认）'),
        (str(second_id), 'Second authorized layout')]
    assert 'Second layout node' not in first.text
    shown = client.get(page(FIRST, f'/sn/diagram?template={second_id}'), headers=headers)
    assert shown.status_code == 200, shown.text
    assert embedded(shown, 'DG_TEMPLATE')['id'] == second_id
    assert embedded(shown, 'DG_TEMPLATE')['layout']['nodes'][0]['id'] == 'second-only'
    assert 'onclick="dgToggleEdit()"' not in shown.text
    assert 'value="__new"' not in shown.text
    assert not state.get('bounded_requests') and state['pages'] == 0
    import os
    from pathlib import Path
    if output := os.environ.get('RESTRICTED_DIAGRAM_ARTIFACTS'):
        folder = Path(output)
        folder.mkdir(parents=True, exist_ok=True)
        (folder / 'diagram-first.html').write_text(first.text)
        (folder / 'diagram-second.html').write_text(shown.text)


@pytest.mark.parametrize('query', ['template=999', 'template=-1', 'template=abc',
                                   'template=1&template=2', 'template=1&date=2000-01-01'])
def test_restricted_diagram_rejects_invalid_or_unshared_layout_selection(canonical, query):
    client, headers, state, _ = canonical
    existing_diagram()
    delegated(state, ('diagram',))
    response = client.get(page(FIRST, '/sn/diagram?' + query), headers=headers)
    assert response.status_code in {404, 422}, response.text
    assert 'Measured component' not in response.text
    assert not state.get('bounded_requests') and state['pages'] == 0


def test_legacy_layout_collection_retains_each_saved_snapshot_without_tenant_fallback(canonical):
    from tin.db import tenant_database_path
    from tin.integration.workbench_scope import result_session_factory
    from tin.models import DiagramTemplate
    from tin.web.restricted_views import remember_diagram
    client, headers, state, _ = canonical
    state['force_legacy'] = True
    with result_session_factory(tenant_database_path(17))() as session:
        session.info.update(workbench_id=FIRST, weknora_user_id='researcher', source_storage_legacy=True)
        now = datetime.now(timezone.utc)
        rows = []
        for index in range(3):
            row = DiagramTemplate(variety='SN', name=f'Legacy layout {index}',
                layout={'nodes': [{'id': f'legacy-{index}', 'label': f'Saved legacy structure {index}',
                                  'x': 0, 'y': 0, 'w': 160, 'h': 80}], 'edges': []},
                is_default=index == 0, created_at=now, updated_at=now)
            session.add(row)
            session.flush()
            rows.append(row)
            if index < 2:
                remember_diagram(session, row)
        identifiers = [row.id for row in rows]
        # A different workbench can mutate tenant-shared rows. Its state must
        # never replace the snapshot explicitly saved in FIRST's namespace.
        rows[0].layout = {'nodes': [{'id': 'foreign', 'label': 'OTHER_WORKBENCH_PRIVATE_CHANGE'}], 'edges': []}
        session.commit()
    delegated(state, ('diagram',))
    for index, identifier in enumerate(identifiers[:2]):
        shown = client.get(page(FIRST, f'/sn/diagram?template={identifier}'), headers=headers)
        assert shown.status_code == 200, shown.text
        assert embedded(shown, 'DG_TEMPLATE')['layout']['nodes'][0]['label'] == f'Saved legacy structure {index}'
        assert 'OTHER_WORKBENCH_PRIVATE_CHANGE' not in shown.text and 'Legacy layout 2' not in shown.text
    assert client.get(page(FIRST, f'/sn/diagram?template={identifiers[2]}'), headers=headers).status_code == 404
    other = client.get(page(SECOND, '/sn/diagram'), headers=headers)
    assert 'Legacy layout' not in other.text and '尚未保存产业图' in other.text
    assert not state.get('bounded_requests') and state['pages'] == 0


def test_legacy_single_snapshot_is_compatible_and_deleted_layout_is_not_resurrected(canonical):
    from tin.db import tenant_database_path
    from tin.integration.workbench_scope import result_session_factory
    from tin.models import DiagramTemplate, SourceMapping
    client, headers, state, _ = canonical
    state['force_legacy'] = True
    identifier = existing_diagram(legacy=True)
    with result_session_factory(tenant_database_path(17))() as session:
        session.add(SourceMapping(id='workbench-diagram:' + FIRST,
            config={'template_id': identifier, 'snapshot': {'name': 'Old saved layout', 'layout': layout()}},
            confirmed_by='researcher', confirmed_at=datetime.now(timezone.utc)))
        session.commit()
    delegated(state, ('diagram',))
    shown = client.get(page(FIRST, f'/sn/diagram?template={identifier}'), headers=headers)
    assert shown.status_code == 200 and embedded(shown, 'DG_TEMPLATE')['name'] == 'Old saved layout'
    with result_session_factory(tenant_database_path(17))() as session:
        session.delete(session.get(DiagramTemplate, identifier))
        session.commit()
    assert client.get(page(FIRST, f'/sn/diagram?template={identifier}'), headers=headers).status_code == 404
    assert not state.get('bounded_requests') and state['pages'] == 0


def test_selecting_unbound_layout_does_not_load_another_layouts_metrics(canonical):
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import DiagramTemplate, SourceMapping
    client, headers, state, _ = canonical
    existing_diagram()
    with isolated_session_factory(17, FIRST)() as session:
        now = datetime.now(timezone.utc)
        row = DiagramTemplate(variety='SN', name='No measurements', layout={'nodes': [], 'edges': []},
            is_default=False, created_at=now, updated_at=now)
        session.add(row)
        session.add(SourceMapping(id='workbench-series-origins:' + FIRST,
            config={'origins': {SERIES: {'knowledge_base_id': KB, 'tenant_id': 17}}},
            confirmed_by='researcher', confirmed_at=now))
        session.commit()
        identifier = row.id
    delegated(state, ('diagram',))
    shown = client.get(page(FIRST, f'/sn/diagram?template={identifier}'), headers=headers)
    assert shown.status_code == 200, shown.text
    assert not state.get('bounded_requests') and state['pages'] == 0
    assert embedded(shown, 'DG_RESTRICTED_DATA')['nodes'] == []


def test_legacy_authorization_can_publish_current_display_without_editing_graph(canonical):
    client, headers, state, _ = canonical
    state['force_legacy'] = True
    identifier = existing_diagram(legacy=True)
    delegated(state, ('diagram',))
    before = client.get(page(FIRST, '/sn/diagram'), headers=headers)
    assert '尚未保存产业图' in before.text
    state['source_delegated'] = False
    published = client.post(api(FIRST, 'diagram/publish'), headers=headers)
    assert published.status_code == 200, published.text
    assert published.json() == {'published': True, 'template_id': identifier}
    pages = state['pages']
    delegated(state, ('diagram',))
    shown = client.get(page(FIRST, '/sn/diagram'), headers=headers)
    assert embedded(shown, 'DG_TEMPLATE')['layout'] == layout()
    assert embedded(shown, 'DG_RESTRICTED_DATA')['nodes'][1]['value'] == 17.5
    assert state['pages'] == pages
    other = client.get(page(SECOND, '/sn/diagram'), headers=headers)
    assert 'Full authorized diagram' not in other.text and '尚未保存产业图' in other.text


def test_explicit_legacy_publication_shares_the_original_pages_entire_layout_list(canonical):
    from tin.db import tenant_database_path
    from tin.integration.workbench_scope import result_session_factory
    from tin.models import DiagramTemplate
    from lxml.html import fromstring
    client, headers, state, _ = canonical
    state['force_legacy'] = True
    first_id = existing_diagram(legacy=True)
    with result_session_factory(tenant_database_path(17))() as session:
        now = datetime.now(timezone.utc)
        row = DiagramTemplate(variety='SN', name='Another original-page diagram',
            layout={'nodes': [{'id': 'another', 'label': 'Another published structure',
                              'x': 0, 'y': 0, 'w': 160, 'h': 80}], 'edges': []},
            is_default=False, created_at=now, updated_at=now)
        session.add(row)
        session.commit()
        second_id = row.id
    original = client.get(page(FIRST, '/sn/diagram'), headers=headers)
    assert original.status_code == 200, original.text
    original_options = fromstring(original.text).xpath('//select[@id="dg-template"]/option[not(@value="__new")]/@value')
    assert set(original_options) == {str(first_id), str(second_id)}
    published = client.post(api(FIRST, 'diagram/publish'), headers=headers)
    assert published.status_code == 200, published.text
    assert published.json() == {'published': True, 'template_id': first_id}
    pages = state['pages']
    delegated(state, ('diagram',))
    for identifier in (first_id, second_id):
        shown = client.get(page(FIRST, f'/sn/diagram?template={identifier}'), headers=headers)
        assert shown.status_code == 200, shown.text
        options = fromstring(shown.text).xpath('//select[@id="dg-template"]/option/@value')
        assert set(options) == set(original_options)
        assert embedded(shown, 'DG_TEMPLATE')['id'] == identifier
    other = client.get(page(SECOND, '/sn/diagram'), headers=headers)
    assert 'Another published structure' not in other.text and '尚未保存产业图' in other.text
    assert state['pages'] == pages


@pytest.mark.parametrize('mode', ['viewer', 'wb_readonly', 'delegated'])
def test_diagram_publication_requires_source_readable_workbench_editor(canonical, mode):
    client, headers, state, _ = canonical
    if mode == 'viewer':
        headers = {**headers, 'Authorization': 'Bearer source-viewer'}
    elif mode == 'wb_readonly':
        state['wb_edit'] = False
    else:
        delegated(state, ('diagram',))
    response = client.post(api(FIRST, 'diagram/publish'), headers=headers)
    assert response.status_code == 403, response.text


def test_publication_does_not_create_or_seed_a_missing_graph(canonical):
    client, headers, state, _ = canonical
    response = client.post(api(FIRST, 'diagram/publish'), headers=headers)
    assert response.status_code == 200 and response.json() == {'published': False, 'template_id': None}
    delegated(state, ('diagram',))
    shown = client.get(page(FIRST, '/sn/diagram'), headers=headers)
    assert '尚未保存产业图' in shown.text
    assert not state.get('bounded_requests')


@pytest.mark.parametrize('extra_points', [False, True])
def test_multiple_bound_indicators_use_one_bounded_request_and_enforce_two_points(canonical, monkeypatch, extra_points):
    import httpx
    from copy import deepcopy
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import DiagramTemplate, SourceMapping
    client, headers, state, _ = canonical
    existing_diagram()
    second_id = 'kb-' + '9' * 48
    with isolated_session_factory(17, FIRST)() as session:
        row = session.get(DiagramTemplate, 1)
        graph = deepcopy(row.layout)
        graph['nodes'][0]['binding']['members'].append('second-measure')
        graph['nodes'].append({'id': 'second-measure', 'parent': 'upstream', 'label': 'Second measured component',
            'x': 25, 'y': 180, 'w': 180, 'h': 100,
            'binding': {'kind': 'series', 'series_id': second_id}})
        row.layout = graph
        session.add(SourceMapping(id='workbench-series-origins:' + FIRST,
            config={'origins': {identifier: {'knowledge_base_id': KB, 'tenant_id': 17}
                                for identifier in [SERIES, second_id]}},
            confirmed_by='researcher', confirmed_at=datetime.now(timezone.utc)))
        session.commit()
    fixture_request = httpx.request

    def upstream_with_second_indicator(method, url, **kwargs):
        response = fixture_request(method, url, **kwargs)
        body = json.loads(kwargs.get('content') or b'{}')
        if str(url).endswith('/api/research/facts') and 'series_ids' in body:
            document = response.json()
            document['indicators'].append({**document['indicators'][0], 'series_id': second_id,
                                           'metric': 'Second measured quantity'})
            document['rows'] += [
                {'series_id': second_id, 'date': '2026-01-02', 'value': 40, 'value_status': 'valid'},
                {'series_id': second_id, 'date': '2026-01-01', 'value': 32, 'value_status': 'valid'}]
            if extra_points:
                document['rows'].append({'series_id': second_id, 'date': '2025-12-31', 'value': 29, 'value_status': 'valid'})
            document['total'] = len(document['rows'])
            return httpx.Response(200, json=document, request=response.request)
        return response

    monkeypatch.setattr(httpx, 'request', upstream_with_second_indicator)
    state['bounded_previous'] = 10
    delegated(state, ('diagram',))
    response = client.get(page(FIRST, '/sn/diagram'), headers=headers)
    assert state['pages'] == 0
    assert len(state['bounded_requests']) == 1
    assert state['bounded_requests'][0]['series_ids'] == [SERIES, second_id]
    assert state['bounded_requests'][0]['points_per_series'] == 2
    assert state['bounded_requests'][0]['view_module'] == 'diagram'
    if extra_points:
        assert response.status_code == 503 and 'Second measured quantity' not in response.text
    else:
        assert response.status_code == 200, response.text
        values = {item['node_id']: item for item in embedded(response, 'DG_RESTRICTED_DATA')['nodes']}
        assert values['second-measure']['value'] == 40 and values['second-measure']['delta'] == 8
        assert values['upstream']['value'] == 57.5


def test_unbound_derived_formula_never_reads_old_cached_result_or_inputs(canonical):
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import Derived, DiagramTemplate
    client, headers, state, _ = canonical
    existing_diagram()
    with isolated_session_factory(17, FIRST)() as session:
        row = session.get(DiagramTemplate, 1)
        row.layout = {'nodes': [{'id': 'derived', 'label': 'Calculated node', 'x': 20, 'y': 20,
            'w': 180, 'h': 100, 'binding': {'kind': 'derived', 'formula_id': 'PRIVATE_FORMULA'}}], 'edges': []}
        session.add(Derived(formula_id='PRIVATE_FORMULA', variety='SN', trade_date='2026-01-02',
            value=9876543, inputs=[{'series_id': 'secret-series', 'value': 1234567}], params={},
            status='ok', computed_at=datetime.now(timezone.utc), note='PRIVATE_CACHED_INPUT_SENTINEL'))
        session.commit()
    delegated(state, ('diagram',))
    response = client.get(page(FIRST, '/sn/diagram'), headers=headers)
    assert response.status_code == 200, response.text
    node = embedded(response, 'DG_RESTRICTED_DATA')['nodes'][0]
    assert node['state'] == 'no_data' and node['value'] is None
    assert '9876543' not in response.text and '1234567' not in response.text
    assert 'PRIVATE_CACHED_INPUT_SENTINEL' not in response.text and 'secret-series' not in response.text
    assert not state.get('bounded_requests') and state['pages'] == 0


def test_publication_rechecks_workbench_edit_rights_before_commit(canonical, monkeypatch):
    import httpx
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import SourceMapping
    client, headers, state, _ = canonical
    existing_diagram()
    fixture_request = httpx.request

    def revoke_during_preparation(method, url, **kwargs):
        response = fixture_request(method, url, **kwargs)
        body = json.loads(kwargs.get('content') or b'{}')
        if str(url).endswith('/api/research/facts') and 'offset' in body:
            state['wb_edit'] = False
        return response

    monkeypatch.setattr(httpx, 'request', revoke_during_preparation)
    response = client.post(api(FIRST, 'diagram/publish'), headers=headers)
    assert response.status_code == 403, response.text
    with isolated_session_factory(17, FIRST)() as session:
        assert session.get(SourceMapping, 'workbench-diagram:' + FIRST) is None


def test_publication_rechecks_workbench_owner_before_commit(canonical, monkeypatch):
    import httpx
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import SourceMapping
    client, headers, state, _ = canonical
    existing_diagram()
    fixture_request = httpx.request

    def replace_owner_during_preparation(method, url, **kwargs):
        response = fixture_request(method, url, **kwargs)
        body = json.loads(kwargs.get('content') or b'{}')
        if str(url).endswith('/api/research/facts') and 'offset' in body:
            state['owner_id'] = 'another-owner'
        return response

    monkeypatch.setattr(httpx, 'request', replace_owner_during_preparation)
    response = client.post(api(FIRST, 'diagram/publish'), headers=headers)
    assert response.status_code == 409, response.text
    with isolated_session_factory(17, FIRST)() as session:
        assert session.get(SourceMapping, 'workbench-diagram:' + FIRST) is None


def test_full_diagram_projection_strips_unrendered_state_and_sanitizes_geometry_and_ids(canonical):
    from copy import deepcopy
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import DiagramTemplate
    client, headers, state, _ = canonical
    existing_diagram()
    malicious_id = "node');window.PRIVATE_JS_SENTINEL=1;//"
    with isolated_session_factory(17, FIRST)() as session:
        row = session.get(DiagramTemplate, 1)
        graph = deepcopy(row.layout)
        graph['private_cache'] = {'file_path': '/private/PRIVATE_LAYOUT_CACHE_SENTINEL'}
        graph['nodes'][0]['binding']['members'] = [malicious_id]
        graph['nodes'][1].update(id=malicious_id, x='0)" onload="PRIVATE_GEOMETRY_SENTINEL',
            url='https://private.invalid/PRIVATE_URL_SENTINEL',
            cached_inputs={'secret': 'PRIVATE_NODE_CACHE_SENTINEL'})
        graph['nodes'][1]['binding']['full_catalog'] = ['PRIVATE_BINDING_CACHE_SENTINEL']
        graph['edges'][0]['source_path'] = 'PRIVATE_EDGE_CACHE_SENTINEL'
        row.layout = graph
        session.commit()
    delegated(state, ('diagram',))
    response = client.get(page(FIRST, '/sn/diagram'), headers=headers)
    assert response.status_code == 200, response.text
    for sentinel in ['PRIVATE_LAYOUT_CACHE_SENTINEL', 'PRIVATE_NODE_CACHE_SENTINEL',
                     'PRIVATE_BINDING_CACHE_SENTINEL', 'PRIVATE_EDGE_CACHE_SENTINEL',
                     'PRIVATE_URL_SENTINEL', 'PRIVATE_GEOMETRY_SENTINEL', 'PRIVATE_JS_SENTINEL']:
        assert sentinel not in response.text
    graph = embedded(response, 'DG_TEMPLATE')['layout']
    assert len(graph['nodes']) == 4 and len(graph['edges']) == 1
    assert graph['nodes'][1]['parent'] == 'upstream'
    assert graph['nodes'][0]['binding']['members'] == [graph['nodes'][1]['id']]
    assert type(graph['nodes'][1]['x']) in {float, int}
    assert graph['nodes'][1]['statics'] == [{'label': 'survey', 'value': '32%'}]
    assert not state.get('bounded_requests') and state['pages'] == 0
