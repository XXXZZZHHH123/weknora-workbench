"""Navigation can carry a day with no facts; data APIs must keep their cutoff checks."""
import pytest
from lxml.html import fromstring

from tests.test_knowledge_facts import canonical, FIRST, SERIES, page
from tests.test_workbench_default_sources import enable_macro, MACRO_SERIES


@pytest.mark.parametrize('suffix', [
    '/sn', '/sn/indicators', '/sn/indicators/' + SERIES, '/sn/judgment', '/sn/diagram',
])
def test_scoped_pages_render_navigation_before_the_first_observation(canonical, suffix):
    client, headers, _, _ = canonical
    response = client.get(page(FIRST, suffix), params={'date': '2026-01-01'}, headers=headers)
    assert response.status_code == 200, response.text
    tree = fromstring(response.text)
    assert tree.xpath('//nav')
    assert tree.xpath('//input[@name="date"]/@value') == ['2026-01-01']
    # The fixture has an observation only on the following day.
    assert '17.5' not in tree.text_content()


@pytest.mark.parametrize('suffix', ['/sn', '/sn/indicators', '/sn/diagram'])
def test_page_date_fallback_keeps_invalid_dates_and_revoked_sources_rejected(canonical, suffix):
    client, headers, state, _ = canonical
    assert client.get(page(FIRST, suffix), params={'date': 'invalid'}, headers=headers).status_code == 400
    state['revoked'] = True
    assert client.get(page(FIRST, suffix), params={'date': '2026-01-01'}, headers=headers).status_code == 403


@pytest.mark.parametrize('suffix,params', [
    ('/api/sn/market/core', {'series_id': SERIES}),
    ('/api/sn/series/' + SERIES + '/history', {}),
    ('/api/sn/indicators/previews', {'series_id': SERIES}),
    ('/api/sn/indicators/compare', {'series_id': [SERIES, MACRO_SERIES]}),
])
def test_data_api_still_rejects_a_day_before_confirmed_observations(canonical, suffix, params):
    client, headers, state, _ = canonical
    enable_macro(state)
    response = client.get(page(FIRST, suffix), params={**params, 'date': '2026-01-01'}, headers=headers)
    assert response.status_code == 404, response.text
