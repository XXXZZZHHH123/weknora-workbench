"""The market page and export consume the same explicitly bound KB quotes."""
import csv
import io

from lxml.html import fromstring
import pytest

from tests.test_knowledge_facts import canonical, FIRST, SECOND, SERIES, page


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
