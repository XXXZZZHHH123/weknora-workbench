"""The explicit Default KB joins facts without reviving old research records."""
import json
from tests.test_knowledge_facts import canonical, api, page, signed_read, FIRST, SECOND, KB, OTHER_KB, SERIES

MACRO_SERIES = 'kb-' + 'c' * 48


def enable_macro(state):
    state['default_macro'] = {'knowledge_base_id': OTHER_KB, 'tenant_id': 17,
                              'kind': 'macro', 'name': 'Any arbitrary display name'}


def test_default_macro_is_available_to_all_workbenches_from_current_kb_only(canonical):
    client, headers, state, _ = canonical
    enable_macro(state)
    for identifier in (FIRST, SECOND):
        response = client.get(api(identifier), headers=headers)
        assert response.status_code == 200, response.text
        assert {row['series_id'] for row in response.json()['indicators']} == {SERIES, MACRO_SERIES}
    assert state['pages'] == state['macro_pages'] == 1
    response = client.get(page(FIRST) + '?scope=macro', headers=headers)
    assert response.status_code == 200, response.text
    assert [row['series_id'] for row in response.json()['indicators']] == [MACRO_SERIES]


def test_default_version_changes_only_rebuild_its_own_cache(canonical):
    client, headers, state, _ = canonical
    enable_macro(state)
    assert client.get(api(), headers=headers).status_code == 200
    primary_pages = state['pages']
    state['macro_value'] = 58.75
    response = client.get(api(FIRST, 'indicators/' + MACRO_SERIES + '/observations'), headers=headers)
    assert response.status_code == 200, response.text
    assert response.json()['observations'][0]['value'] == 58.75
    assert state['pages'] == primary_pages
    assert state['macro_pages'] == 2
    evidence = response.json()['observations'][0]['sources'][0]
    assert evidence['knowledge_base_id'] == OTHER_KB
    assert evidence['locator']['knowledge_base_id'] == OTHER_KB


def test_macro_permission_revocation_never_uses_warm_values(canonical):
    client, headers, state, _ = canonical
    enable_macro(state)
    assert client.get(api(), headers=headers).status_code == 200
    state['macro_revoked'] = True
    assert client.get(api(), headers=headers).status_code == 403


def test_default_binding_from_another_tenant_is_rejected(canonical):
    client, headers, state, _ = canonical
    enable_macro(state)
    state['default_macro']['tenant_id'] = 99
    assert client.get(api(), headers=headers).status_code == 403


def test_default_binding_removal_does_not_keep_cached_macro_fields(canonical):
    client, headers, state, _ = canonical
    enable_macro(state)
    assert client.get(api(), headers=headers).json()['total'] == 2
    state['default_macro'] = None
    assert [row['series_id'] for row in client.get(api(), headers=headers).json()['indicators']] == [SERIES]


def test_model_read_api_describes_each_actual_source_and_its_field_scope(canonical):
    client, _, state, _ = canonical
    enable_macro(state)
    body = json.dumps({'operation': 'indicators'}).encode()
    response = client.post('/research/api/tools/read', content=body, headers=signed_read(body))
    assert response.status_code == 200, response.text
    document = response.json()
    assert {(item['knowledge_base_id'], item['source_kind']) for item in document['knowledge_base_sources']} == {
        (KB, 'primary'), (OTHER_KB, 'macro')}
    fields = {item['series_id']: item for item in document['indicators']}
    assert fields[SERIES]['knowledge_base_id'] == KB
    assert fields[MACRO_SERIES]['knowledge_base_id'] == OTHER_KB
    assert fields[MACRO_SERIES]['source_kind'] == 'macro'
