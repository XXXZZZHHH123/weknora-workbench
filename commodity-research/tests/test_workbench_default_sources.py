"""The explicit Default KB joins facts without reviving old research records."""
import json
from urllib.parse import parse_qs, urlsplit

import pytest
from lxml.html import fromstring
from tests.test_knowledge_facts import canonical, api, page, signed_read, FIRST, SECOND, KB, OTHER_KB, SERIES

MACRO_SERIES = 'kb-' + 'c' * 48


def enable_macro(state):
    state['default_macro'] = {'knowledge_base_id': OTHER_KB, 'tenant_id': 17,
                              'kind': 'macro', 'name': 'Any arbitrary display name'}


def test_default_macro_shares_the_indicator_directory_without_separate_navigation(canonical):
    client, headers, state, _ = canonical
    enable_macro(state)
    response = client.get(page(FIRST, '/sn/indicators'), headers=headers)
    assert response.status_code == 200
    html = fromstring(response.text)
    assert set(html.xpath('//tr[@data-indicator-row]/@data-series-id')) == {SERIES, MACRO_SERIES}
    assert html.xpath('//tr[@data-series-id=$series]/@data-macro', series=MACRO_SERIES) == ['true']
    assert html.xpath('//button[@data-scope="all"]/@aria-pressed') == ['true']
    navigation = html.xpath('//*[contains(concat(" ",normalize-space(@class)," ")," research-nav-bottom ")]')[0]
    labels = [item.text_content().strip() for item in navigation.xpath('.//a')]
    assert labels.count('指标') == 1
    assert '宏观数据' not in labels and '宏观知识库' not in labels
    assert navigation.xpath('.//a[normalize-space()="指标"]/@aria-current') == ['page']
    sources = html.get_element_by_id('indicator-sources')
    assert sources.xpath('.//a/@href') == ['/platform/knowledge-bases/' + OTHER_KB]
    assert 'open' not in sources.attrib


@pytest.mark.parametrize('day', [None, '2026-01-02'])
def test_old_macro_bookmark_opens_combined_indicators_in_the_same_workbench(canonical, day):
    client, headers, state, _ = canonical
    enable_macro(state)
    response = client.get(page(FIRST, '/sn/macro'), headers=headers,
                          params={'date': day, 'next': 'https://outside.invalid'} if day else {},
                          follow_redirects=False)
    assert response.status_code == 307
    target = urlsplit(response.headers['location'])
    assert not target.scheme and not target.netloc
    assert target.path == page(FIRST, '/sn/indicators')
    assert parse_qs(target.query) == ({'date': [day]} if day else {})
    assert client.get(response.headers['location'], headers=headers).status_code == 200


def test_indicator_filter_keeps_one_active_indicator_navigation(canonical):
    client, headers, state, _ = canonical
    enable_macro(state)
    response = client.get(page(FIRST, '/sn/indicators') + '?scope=macro', headers=headers)
    assert response.status_code == 200
    html = fromstring(response.text)
    active = html.xpath('//*[contains(concat(" ",normalize-space(@class)," ")," research-nav-links ")]//a[@aria-current="page"]')
    assert [item.text_content().strip() for item in active] == ['指标']


def test_no_default_binding_invents_neither_macro_rows_nor_a_source_link(canonical):
    client, headers, _, _ = canonical
    response = client.get(page(FIRST, '/sn/indicators'), headers=headers)
    assert response.status_code == 200
    html = fromstring(response.text)
    assert html.xpath('//tr[@data-indicator-row]/@data-series-id') == [SERIES]
    assert not html.get_element_by_id('indicator-sources').xpath('.//a')


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
