"""Actual scoped ASGI routes with a temporary verified KB projection, no real service."""
import pytest
from lxml.html import fromstring

from tests.test_knowledge_facts import canonical, FIRST, LEGACY, SECOND, SERIES, page


def market(identifier=FIRST):
    return page(identifier, '/api/sn/market/core')


@pytest.mark.parametrize('identifier', [FIRST, LEGACY])
def test_market_selector_uses_current_catalog_in_both_workbench_modes(canonical, identifier):
    client, headers, state, _ = canonical
    response = client.get(page(identifier, '/sn'), headers=headers)
    assert response.status_code == 200, response.text
    tree = fromstring(response.text)
    assert tree.xpath('//*[@id="market-core-open"]')
    assert tree.xpath('//*[@id="market-core-catalog"]')
    assert tree.xpath('//*[@data-market-card-series]/@data-market-card-series') == [SERIES]
    assert 'SHFE.SN.main.settle' not in tree.get_element_by_id('market-core-catalog').text
    assert tree.xpath('//*[@id="market-contract-source"]//a[contains(@href,"shfe.com.cn")]')
    assert '当前通用字段确认尚未提供这些语义绑定' in tree.get_element_by_id('market-contract-source').text_content()


@pytest.mark.parametrize('identifier', [FIRST, SECOND, LEGACY])
def test_core_cards_read_only_current_knowledge_facts_in_explicit_order(canonical, identifier):
    client, headers, state, _ = canonical
    state['value'] = 27
    response = client.get(market(identifier), params={'series_id': SERIES}, headers=headers)
    assert response.status_code == 200, response.text
    data = response.json()
    assert data['series_ids'] == [SERIES] and data['ready_count'] == 1
    tree = fromstring(data['html'])
    assert tree.get('data-market-card-series') == SERIES
    assert 'Measured quantity' in tree.text_content()
    assert tree.xpath('.//span[contains(@class, "text-3xl")]/text()') == ['27']
    state['value'] = 0
    updated = client.get(market(identifier), params={'series_id': SERIES}, headers=headers).json()
    assert updated['ready_count'] == 1
    assert fromstring(updated['html']).xpath('.//span[contains(@class, "text-3xl")]/text()') == ['0']
    assert not any(path.startswith('/api/sn/refresh') for _, path in state['calls'])


def test_cutoff_and_withdrawn_sources_never_show_future_or_stored_old_quotes(canonical):
    client, headers, state, _ = canonical
    response = client.get(market(), params={'series_id': SERIES, 'date': '2026-01-01'}, headers=headers)
    assert response.status_code == 404
    assert '所选日期之前尚无已确认观测' in response.json()['detail']
    state['ready'] = False
    assert client.get(market(), params={'series_id': SERIES}, headers=headers).status_code == 404
    assert client.get(market(), params={'series_id': 'SHFE.SN.main.settle'}, headers=headers).status_code == 404


@pytest.mark.parametrize('query,status', [
    ('', 422), ('series_id=', 422), ('series_id=unknown', 404),
    ('series_id=' + SERIES + '&series_id=' + SERIES, 422),
    ('&'.join('series_id=field-' + str(i) for i in range(13)), 422),
    ('series_id=' + SERIES + '&date=invalid', 400),
    ('series_id=' + SERIES + '&date=20260102', 400),
    ('series_id=' + SERIES + '&date=', 400),
    ('series_id=' + SERIES + '&date=2026-01-01&date=2026-01-02', 422),
    ('series_id=' + SERIES + '&knowledge_base_id=other', 422),
    ('series_id=' + SERIES + '&page=2', 422),
])
def test_explicit_market_parameters_and_size_are_validated(canonical, query, status):
    client, headers, _, _ = canonical
    response = client.get(market() + '?' + query, headers=headers)
    assert response.status_code == status, response.text


def test_viewer_can_read_but_revoke_or_wrong_field_cannot_fall_back(canonical):
    client, headers, state, _ = canonical
    viewer = {**headers, 'Authorization': 'Bearer source-viewer'}
    assert client.get(market(), params={'series_id': SERIES}, headers=viewer).status_code == 200
    assert client.post(market(), headers=headers, json={'series_id': [SERIES]}).status_code == 404
    state['revoked'] = True
    assert client.get(market(), params={'series_id': SERIES}, headers=headers).status_code == 403


def test_existing_curve_does_not_claim_the_knowledge_base_has_no_contract_semantics():
    from tin.web.app import templates
    template = templates.get_template('_market_contract_guide.html')
    html = template.render(research_workbench=lambda: True, curve={'rows': [{}]})
    assert '本交易日已有可核对的具体合约行情' in html
    assert '当前通用字段确认尚未提供' not in html


def test_market_endpoint_is_readonly_and_does_not_schedule_source_preparation():
    from tin.integration.workbench_paths import allowed_workbench_route, source_preparation_required
    assert allowed_workbench_route('/api/sn/market/core', 'GET')
    assert not allowed_workbench_route('/api/sn/market/core', 'POST')
    assert not source_preparation_required('/api/sn/market/core')


def test_market_endpoint_rejects_unprojected_research_storage_even_in_a_verified_workbench(canonical, monkeypatch):
    from contextlib import contextmanager
    from sqlalchemy.orm import sessionmaker
    from sqlalchemy.pool import StaticPool
    from tin.db import Base, make_engine
    from tin.web import app as module
    client, headers, _, _ = canonical
    engine = make_engine('sqlite://', poolclass=StaticPool, connect_args={'check_same_thread': False})
    Base.metadata.create_all(engine)
    factory = sessionmaker(engine)

    @contextmanager
    def old_storage():
        with factory() as session:
            session.info['workbench_id'] = FIRST
            yield session

    monkeypatch.setattr(module, 'SessionLocal', old_storage)
    response = client.get(market(), params={'series_id': SERIES}, headers=headers)
    assert response.status_code == 409
    assert '未使用旧研究库事实' in response.json()['detail']
