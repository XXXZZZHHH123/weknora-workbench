"""The market page and export consume the same explicitly bound KB quotes."""
import csv
import io

from lxml.html import fromstring
import pytest

from tests.test_knowledge_facts import canonical, FIRST, SECOND, SERIES, page
from tests.test_restricted_views import delegated


def bindings(label="SYN's contract"):
    return {'contracts': [{'contract': label, 'delivery_month': '2026-03',
        'settle': SERIES, 'price_unit': '吨'}]}


def test_current_kb_contract_configuration_drives_page_and_csv_without_invented_units(canonical):
    client, headers, state, _ = canonical
    target = page(FIRST, '/api/sn/market/contracts/config')
    response = client.post(target, headers=headers, json=bindings())
    assert response.status_code == 200, response.text
    response = client.get(page(FIRST, '/sn'), headers=headers)
    assert response.status_code == 200, response.text
    tree = fromstring(response.text)
    rows = tree.xpath('//tr[@data-contract]')
    assert len(rows) == 1 and rows[0].get('data-contract') == "SYN's contract"
    assert rows[0].get('onmouseenter') is None
    assert '2026-01-02 · 历史日行情' in tree.text_content()
    assert '未判定' in rows[0].text_content() and '成交量（手）' not in tree.text_content()
    exported = client.get(page(FIRST, '/sn/contracts.csv'), headers=headers)
    assert exported.status_code == 200
    cells = list(csv.reader(io.StringIO(exported.content.decode('utf-8-sig'))))
    assert cells[1][0] == '2026-01-02' and cells[1][1] == "SYN's contract"
    assert float(cells[1][2]) == state['value'] and cells[1][5] == cells[1][7] == ''
    assert cells[1][8] == '吨' and cells[1][-1] == '未判定'
    other = client.get(page(SECOND, '/sn'), headers=headers)
    assert not fromstring(other.text).xpath('//tr[@data-contract]')


def test_contract_export_writes_untrusted_labels_as_text_and_never_carries_to_another_day(canonical):
    client, headers, _, _ = canonical
    target = page(FIRST, '/api/sn/market/contracts/config')
    assert client.post(target, headers=headers, json=bindings('=synthetic-label')).status_code == 200
    exported = client.get(page(FIRST, '/sn/contracts.csv'), headers=headers)
    cells = list(csv.reader(io.StringIO(exported.content.decode('utf-8-sig'))))
    assert cells[1][1] == "'=synthetic-label"
    response = client.get(page(FIRST, '/sn'), params={'date': '2026-01-03'}, headers=headers)
    assert response.status_code == 200
    assert not fromstring(response.text).xpath('//tr[@data-contract]')


@pytest.mark.parametrize('mode', ['viewer', 'readonly', 'delegated', 'unknown', 'unit', 'extra'])
def test_contract_configuration_requires_current_edit_and_verified_source_binding(canonical, mode):
    client, headers, state, _ = canonical
    body = bindings()
    if mode == 'viewer': headers = {**headers, 'Authorization': 'Bearer source-viewer'}
    if mode == 'readonly': state['wb_edit'] = False
    if mode == 'delegated': state['source_delegated'] = True
    if mode == 'unknown': body['contracts'][0]['settle'] = 'kb-' + 'f' * 48
    if mode == 'unit': body['contracts'][0]['price_unit'] = '美元'
    if mode == 'extra': body['contracts'][0]['guess_by_name'] = True
    response = client.post(page(FIRST, '/api/sn/market/contracts/config'), headers=headers, json=body)
    assert response.status_code in {403, 404, 409, 422}, response.text


CUSTOM_LINK = {'label': '交易所公开页 <行情>', 'url': 'https://example.org/quotes?type=futures&mode=daily'}


def contract_guide(client, headers, identifier=FIRST, **params):
    response = client.get(page(identifier, '/sn'), params=params, headers=headers)
    assert response.status_code == 200, response.text
    tree = fromstring(response.text)
    guide = tree.get_element_by_id('market-contract-source')
    links = [(a.text_content(), a.get('href')) for a in guide.xpath('.//a')]
    return tree, links


def test_legacy_shfe_guide_requires_explicit_spec_and_preserves_original_links(canonical):
    client, headers, _, _ = canonical
    target = page(FIRST, '/api/sn/market/contracts/config')
    body = bindings('SN2603')
    assert client.post(target, headers=headers, json=body).status_code == 200
    assert contract_guide(client, headers)[1] == []

    body['contracts'][0].update(variety='SN', multiplier=1, tick_size=10, spec_source='legacy-shfe-sn')
    assert client.post(target, headers=headers, json=body).status_code == 200
    for date in ('2026-01-02', '2026-01-03'):
        tree, links = contract_guide(client, headers, date=date)
        assert tree.get_element_by_id('market-contract-source').tag == 'details'
        assert links == [
            ('上海期货交易所 · 日交易快讯', 'https://www.shfe.com.cn/reports/tradedata/dailyandweeklydata/?query_params=kx'),
            ('数据下载', 'https://www.shfe.com.cn/reports/tradedata/datadownload/'),
        ]

    response = client.post(target, headers=headers, json={'source_links': [CUSTOM_LINK]})
    assert response.status_code == 200, response.text
    assert response.json()['contracts'][0]['spec_source'] == 'legacy-shfe-sn'
    assert contract_guide(client, headers)[1] == [(CUSTOM_LINK['label'], CUSTOM_LINK['url'])]
    response = client.post(target, headers=headers, json=bindings('Updated contract'))
    assert response.json()['source_links'] == [CUSTOM_LINK]
    assert contract_guide(client, headers, SECOND)[1] == []
    assert client.post(target, headers=headers, json={'source_links': []}).status_code == 200
    assert contract_guide(client, headers)[1] == []


@pytest.mark.parametrize('source_links', [
    [dict(CUSTOM_LINK, url='http://example.org/quotes')],
    [dict(CUSTOM_LINK, url='javascript:alert(1)')],
    [dict(CUSTOM_LINK, url='https:///missing-host')],
    [dict(CUSTOM_LINK, url='https://example.org\\foo/bar')],
    [dict(CUSTOM_LINK, url='https://example.org:invalid/quotes')],
    [dict(CUSTOM_LINK, label=' ')],
    [dict(CUSTOM_LINK, label='长' * 41)],
    [dict(CUSTOM_LINK, url='https://example.org/' + 'a' * 281)],
    [CUSTOM_LINK] * 5,
    [{'url': 'https://example.org'}],
    None,
])
def test_invalid_contract_source_links_do_not_change_saved_links(canonical, source_links):
    client, headers, _, _ = canonical
    target = page(FIRST, '/api/sn/market/contracts/config')
    assert client.post(target, headers=headers, json={'source_links': [CUSTOM_LINK]}).status_code == 200
    response = client.post(target, headers=headers, json={'source_links': source_links})
    assert response.status_code in (409, 422)
    assert contract_guide(client, headers)[1] == [(CUSTOM_LINK['label'], CUSTOM_LINK['url'])]


def test_link_limits_presets_and_mapping_audit(canonical):
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import SourceMapping
    from tin.web.market_contracts import CONTRACTS_KEY

    client, headers, _, _ = canonical
    tree, _ = contract_guide(client, headers)
    groups = tree.get_element_by_id('market-contract-settings-preset').xpath('./optgroup')
    assert len(groups) == 6 and all(len(group.xpath('./option[starts-with(@value,"https://")]')) == 2 for group in groups)
    links = [{'label': '字' * 39 + str(index), 'url': 'https://example.org/' + 'a' * 279 + str(index)}
             for index in range(4)]
    target = page(FIRST, '/api/sn/market/contracts/config')
    response = client.post(target, headers=headers, json={'source_links': links})
    assert response.status_code == 200, response.text
    with isolated_session_factory(17, FIRST)() as session:
        mapping = session.get(SourceMapping, f'{CONTRACTS_KEY}:{FIRST}')
        assert mapping.config == {'contracts': [], 'source_links': links}
        assert mapping.confirmed_by == 'researcher' and mapping.confirmed_at is not None
    assert contract_guide(client, headers)[1] == [(link['label'], link['url']) for link in links]


@pytest.mark.parametrize('mode', ['readonly', 'delegated'])
def test_readonly_can_see_configured_links_without_editor_or_write_api(canonical, mode):
    client, headers, state, _ = canonical
    target = page(FIRST, '/api/sn/market/contracts/config')
    assert client.post(target, headers=headers, json={'source_links': [CUSTOM_LINK]}).status_code == 200
    if mode == 'readonly':
        state['wb_edit'] = False
    else:
        delegated(state, ('market',))
    tree, links = contract_guide(client, headers)
    assert links == [(CUSTOM_LINK['label'], CUSTOM_LINK['url'])]
    assert not tree.xpath('//*[@id="market-contract-settings-open"] | //script[contains(@src,"market-contract-settings.js")]')
    assert client.post(target, headers=headers, json={'source_links': []}).status_code == 403
