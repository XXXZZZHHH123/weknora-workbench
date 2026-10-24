"""Delegated page grants never become arbitrary facts or raw-source access."""
from datetime import datetime, timezone

import pytest

from tin.config import SHANGHAI
from tests.test_knowledge_facts import canonical, api, page, FIRST, SECOND, SERIES, KB, OTHER_KB


def delegated(state, modules=('market',)):
    state['source_delegated'] = True
    state['view_modules'] = list(modules)


def save_core():
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import SourceMapping
    with isolated_session_factory(17, FIRST)() as session:
        session.add(SourceMapping(id='workbench-market-core:' + FIRST, config={'series_ids': [SERIES]},
            confirmed_by='researcher', confirmed_at=datetime.now(timezone.utc)))
        session.add(SourceMapping(id='workbench-series-origins:' + FIRST, config={'origins': {
            SERIES: {'knowledge_base_id': KB, 'tenant_id': 17},
            'kb-' + 'f' * 48: {'knowledge_base_id': KB, 'tenant_id': 17}}},
            confirmed_by='researcher', confirmed_at=datetime.now(timezone.utc)))
        session.commit()


def test_no_source_consent_still_renders_restricted_workbench_without_facts(canonical):
    client, headers, state, _ = canonical
    delegated(state, ())
    state['source_unavailable'] = True
    response = client.get(page(FIRST, '/'), headers=headers)
    assert response.status_code == 200, response.text
    assert '受限视角' in response.text and '来源未授权' in response.text
    assert '"detail"' not in response.text and '17.5' not in response.text
    assert not state.get('bounded_requests') and state['pages'] == 0


def test_navigation_hides_unselected_modules_without_granting_direct_url_access(canonical):
    from lxml.html import fromstring
    client, headers, state, _ = canonical
    delegated(state, ('market', 'diagram'))
    response = client.get(page(FIRST, '/sn'), headers=headers)
    assert response.status_code == 200, response.text
    links = fromstring(response.text).xpath('//nav[@aria-label="商品研究导航"]//a/@href')
    assert any('/sn/diagram' in link for link in links)
    for suffix in ('/sn/indicators', '/sn/judgment', '/sn/strategies', '/sn/reports', '/sn/entry'):
        assert not any(suffix in link for link in links), suffix
    locked = client.get(page(FIRST, '/sn/indicators'), headers=headers)
    assert locked.status_code == 200 and '来源未授权' in locked.text
    assert not state.get('bounded_requests')


def test_market_uses_only_server_core_and_rejects_browser_scope_expansion(canonical):
    client, headers, state, _ = canonical
    delegated(state)
    save_core()
    response = client.get(page(FIRST, '/sn'), headers=headers)
    assert response.status_code == 200, response.text
    from lxml.html import fromstring
    assert fromstring(response.text).xpath('//*[contains(@class,"restricted-value")]')[0].text_content().strip().startswith('18')
    requests = state['bounded_requests']
    # Existing core-card consent must not implicitly publish a longer history.
    assert len(requests) == 1
    assert [request['points_per_series'] for request in requests] == [2]
    for request in requests:
        assert request['series_ids'] == [SERIES]
        assert request['view_module'] == 'market'
        assert not {'offset', 'limit', 'encoding', 'metadata_only'} & request.keys()
    assert requests[0]['as_of'] == datetime.now(SHANGHAI).date().isoformat()
    assert 'source_cell' not in response.text and 'source_file' not in response.text
    before = len(state['bounded_requests'])
    for suffix in ['/sn?series_id=hidden', '/sn?date=2000-01-01',
                   '/api/sn/market/core?series_id=hidden']:
        assert client.get(page(FIRST, suffix), headers=headers).status_code in {403, 422}
    assert len(state['bounded_requests']) == before


def test_unbound_market_does_not_fetch_catalog_or_any_facts(canonical):
    client, headers, state, _ = canonical
    delegated(state)
    response = client.get(page(FIRST, '/sn'), headers=headers)
    assert response.status_code == 200, response.text
    assert '尚未配置' in response.text and '17.5' not in response.text
    assert not state.get('bounded_requests') and state['pages'] == 0


@pytest.mark.parametrize('suffix', [
    '/api/sn/indicators/catalog', '/api/sn/indicators/' + SERIES + '/history',
    '/api/sn/diagram/templates', '/api/sn/diagram/markdown', '/api/sn/diagram/values',
    '/api/sn/export', '/api/sn/snapshot', '/sn/entry', '/api/sn/diagram/suggest?label=x',
])
def test_delegated_cannot_bypass_page_grants_via_general_endpoints(canonical, suffix):
    client, headers, state, _ = canonical
    delegated(state, ('market', 'diagram'))
    response = client.get(page(FIRST, suffix), headers=headers)
    assert response.status_code in {403, 404}, response.text
    assert '17.5' not in response.text and state['pages'] == 0


@pytest.mark.parametrize('suffix', ['indicators', 'indicators/' + SERIES + '/observations',
                                     'overview', 'sources', 'judgments', 'structure'])
def test_workbench_json_apis_cannot_bypass_restricted_routes(canonical, suffix):
    client, headers, state, _ = canonical
    delegated(state, ('market', 'diagram'))
    response = client.get(api(FIRST, suffix), headers=headers)
    assert response.status_code == 403, response.text
    assert '17.5' not in response.text and state['pages'] == 0


def test_revocation_is_not_downgraded_into_successful_shell(canonical):
    client, headers, state, _ = canonical
    delegated(state)
    save_core()
    assert client.get(page(FIRST, '/sn'), headers=headers).status_code == 200
    state['source_consent_revoked'] = True
    denied = client.get(page(FIRST, '/sn'), headers=headers)
    assert denied.status_code == 403 and '17.5' not in denied.text


def test_indicator_page_is_locked_unless_its_own_checkbox_is_approved(canonical):
    client, headers, state, _ = canonical
    delegated(state)
    save_core()
    response = client.get(page(FIRST, '/sn/indicators'), headers=headers)
    assert response.status_code == 200
    assert '来源未授权' in response.text and '17.5' not in response.text
    assert not state.get('bounded_requests')


def test_overbroad_upstream_response_fails_closed(canonical):
    client, headers, state, _ = canonical
    delegated(state)
    save_core()
    state['bounded_bad_response'] = True
    from unittest.mock import patch
    with patch('tin.web.restricted_views.bound_series_ids', return_value=['kb-' + 'f' * 48]):
        denied = client.get(page(FIRST, '/sn'), headers=headers)
    assert denied.status_code == 503 and '17.5' not in denied.text


def test_source_readable_editor_saves_verified_core_for_restricted_members(canonical):
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import SourceMapping
    client, headers, state, _ = canonical
    target = page(FIRST, '/api/sn/market/core/config')
    assert client.post(target, headers=headers, json={'series_ids': [SERIES]}).status_code == 200
    with isolated_session_factory(17, FIRST)() as session:
        config = session.get(SourceMapping, 'workbench-market-core:' + FIRST)
        origins = session.get(SourceMapping, 'workbench-series-origins:' + FIRST)
        assert config.config['series_ids'] == [SERIES]
        assert origins.config['origins'][SERIES] == {'knowledge_base_id': KB, 'tenant_id': 17}
    delegated(state)
    assert client.get(page(FIRST, '/sn'), headers=headers).status_code == 200
    assert state['bounded_requests'][-1]['series_ids'] == [SERIES]
    assert '尚未配置' in client.get(page(SECOND, '/sn'), headers=headers).text


@pytest.mark.parametrize('mode', ['viewer', 'wb_readonly', 'delegated', 'unknown_id', 'duplicate', 'extra'])
def test_untrusted_core_configuration_cannot_expand_shared_bindings(canonical, mode):
    client, headers, state, _ = canonical
    body = {'series_ids': [SERIES]}
    if mode == 'viewer':
        headers = {**headers, 'Authorization': 'Bearer source-viewer'}
    elif mode == 'wb_readonly':
        state['wb_edit'] = False
    elif mode == 'delegated':
        delegated(state)
    elif mode == 'unknown_id':
        body['series_ids'] = ['kb-' + 'f' * 48]
    elif mode == 'duplicate':
        body['series_ids'] = [SERIES, SERIES]
    else:
        body['knowledge_base_id'] = 'other'
    result = client.post(page(FIRST, '/api/sn/market/core/config'), headers=headers, json=body)
    assert result.status_code in {403, 404, 409, 422}, result.text


def test_legacy_configuration_and_diagram_do_not_cross_workbench_boundaries(canonical):
    client, headers, state, _ = canonical
    state['force_legacy'] = True
    assert client.post(page(FIRST, '/api/sn/market/core/config'), headers=headers,
                       json={'series_ids': [SERIES]}).status_code == 200
    payload = {'name': 'First private layout', 'layout': {'nodes': [
        {'id': 'first-node', 'label': 'PRIVATE_LAYOUT_SENTINEL', 'x': 10, 'y': 10,
         'binding': {'kind': 'series', 'series_id': SERIES}, 'statics': [{'label': 'private', 'value': '91234567'}]}], 'edges': []}}
    assert client.post(page(FIRST, '/api/sn/diagram/templates'), headers=headers, json=payload).status_code == 200
    delegated(state, ('market', 'diagram'))
    first = client.get(page(FIRST, '/sn/diagram'), headers=headers)
    assert first.status_code == 200 and 'PRIVATE_LAYOUT_SENTINEL' in first.text
    second = client.get(page(SECOND, '/sn/diagram'), headers=headers)
    assert second.status_code == 200 and 'PRIVATE_LAYOUT_SENTINEL' not in second.text and '91234567' not in second.text
    assert '尚未配置' in client.get(page(SECOND, '/sn'), headers=headers).text


def test_legacy_template_edits_in_another_workbench_cannot_change_approved_snapshot(canonical):
    client, headers, state, _ = canonical
    state['force_legacy'] = True
    target = '/api/sn/diagram/templates'
    payload = {'name': 'Shared old row', 'layout': {'nodes': [
        {'id': 'node', 'label': 'APPROVED_LAYOUT', 'x': 0, 'y': 0,
         'binding': {'kind': 'series', 'series_id': SERIES}, 'statics': []}], 'edges': []}}
    first = client.post(page(FIRST, target), headers=headers, json=payload)
    assert first.status_code == 200
    payload['id'] = first.json()['saved']['id']
    assert client.post(page(SECOND, target), headers=headers, json=payload).status_code == 200
    payload['layout']['nodes'][0]['label'] = 'OTHER_WORKBENCH_SECRET_SENTINEL'
    assert client.post(page(FIRST, target), headers=headers, json=payload).status_code == 200
    delegated(state, ('diagram',))
    protected = client.get(page(SECOND, '/sn/diagram'), headers=headers)
    assert protected.status_code == 200 and 'APPROVED_LAYOUT' in protected.text
    assert 'OTHER_WORKBENCH_SECRET_SENTINEL' not in protected.text


def test_indicator_checkbox_grants_only_current_workbench_bound_values(canonical):
    client, headers, state, _ = canonical
    save_core()
    delegated(state, ('indicators',))
    response = client.get(page(FIRST, '/sn/indicators'), headers=headers)
    assert response.status_code == 200 and state['bounded_requests'][-1]['view_module'] == 'indicators'
    assert state['bounded_requests'][-1]['series_ids'] == [SERIES]
    assert client.get(page(FIRST, '/api/sn/indicators/catalog'), headers=headers).status_code == 403


@pytest.mark.parametrize('revoked,status', [('login_revoked', 401), ('workbench_revoked', 404)])
def test_actual_login_or_workbench_revocation_is_never_a_successful_shell(canonical, revoked, status):
    client, headers, state, _ = canonical
    delegated(state)
    state['source_unavailable'] = True
    state[revoked] = True
    assert client.get(page(FIRST, '/'), headers=headers).status_code == status


def test_page_grant_change_during_verification_fails_closed(canonical):
    client, headers, state, _ = canonical
    delegated(state, ('market',))
    state['grant_modules'] = ['diagram']
    denied = client.get(page(FIRST, '/sn'), headers=headers)
    assert denied.status_code == 403 and state['pages'] == 0


def test_missing_origin_metadata_does_not_probe_any_knowledge_base(canonical):
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import SourceMapping
    client, headers, state, _ = canonical
    with isolated_session_factory(17, FIRST)() as session:
        session.add(SourceMapping(id='workbench-market-core:' + FIRST, config={'series_ids': [SERIES]},
            confirmed_by='researcher', confirmed_at=datetime.now(timezone.utc)))
        session.commit()
    delegated(state)
    assert '尚未配置' in client.get(page(FIRST, '/sn'), headers=headers).text
    assert not state.get('bounded_requests')


def test_personal_records_stay_actor_scoped_and_never_return_cached_fact_payloads(canonical):
    from tests.test_canonical_research_results import save_results
    client, headers, state, _ = canonical
    save_results(FIRST)
    save_results(FIRST, 'another-user')
    delegated(state, ('judgments', 'strategies', 'reports'))
    for suffix in ['/sn/judgment', '/sn/strategies', '/sn/reports']:
        response = client.get(page(FIRST, suffix), headers=headers)
        assert response.status_code == 200, response.text
        assert 'another-user' not in response.text
        if suffix == '/sn/judgment':
            assert 'researcher original thesis' in response.text
        if suffix == '/sn/reports':
            assert 'researcher original brief' in response.text
        assert '17.5' not in response.text and 'source_snapshots' not in response.text
    assert not state.get('bounded_requests')


def test_restricted_projection_rules_do_not_block_normal_saving_of_legacy_binding(canonical):
    client, headers, _, _ = canonical
    payload = {'name': 'Historical layout', 'layout': {'nodes': [
        {'id': 'node', 'label': 'Legacy binding', 'x': 0, 'y': 0,
         'binding': {'kind': 'series', 'series_id': 'legacy.retired-series'}, 'statics': []}], 'edges': []}}
    response = client.post(page(FIRST, '/api/sn/diagram/templates'), headers=headers, json=payload)
    assert response.status_code == 200, response.text


def test_core_macro_bindings_use_verified_origin_without_loading_either_full_catalog(canonical):
    client, headers, state, _ = canonical
    state['default_macro'] = {'knowledge_base_id': OTHER_KB, 'tenant_id': 17, 'name': 'Macro'}
    macro_id = 'kb-' + 'c' * 48
    saved = client.post(page(FIRST, '/api/sn/market/core/config'), headers=headers,
        json={'series_ids': [SERIES, macro_id]})
    assert saved.status_code == 200, saved.text
    delegated(state)
    pages, macro_pages = state['pages'], state.get('macro_pages', 0)
    response = client.get(page(FIRST, '/sn'), headers=headers)
    assert response.status_code == 200, response.text
    assert 'Macro quantity' in response.text and 'Measured quantity' in response.text
    assert [body['series_ids'] for body in state['bounded_requests']] == [[SERIES], [macro_id]]
    assert state['pages'] == pages and state.get('macro_pages', 0) == macro_pages


def test_only_latest_value_and_server_calculated_change_are_rendered(canonical):
    client, headers, state, _ = canonical
    state['value'], state['bounded_previous'] = 30, 13
    save_core()
    delegated(state)
    response = client.get(page(FIRST, '/sn'), headers=headers)
    assert response.status_code == 200
    assert '较上期 +17' in response.text
    assert '2026-01-01' not in response.text
