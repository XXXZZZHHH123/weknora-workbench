"""The original indicator layout has a bounded, read-only shared variant."""
import os
from pathlib import Path

from lxml.html import fromstring

from tests.test_knowledge_facts import canonical, FIRST, SERIES, page
from tests.test_restricted_views import delegated, save_core


def test_shared_indicators_reuse_catalog_with_only_bound_latest_values(canonical):
    client, headers, state, _ = canonical
    save_core()
    delegated(state, ('indicators',))
    state['bounded_previous'] = 15
    response = client.get(page(FIRST, '/sn/indicators'), headers=headers)
    assert response.status_code == 200, response.text
    html = fromstring(response.text)
    root = html.get_element_by_id('indicator-workspace')
    assert root.get('data-readonly') == 'true'
    assert root.xpath('.//tr[@data-indicator-row]/@data-series-id') == [SERIES]
    assert root.xpath('.//details[@data-indicator-group]')
    row = root.xpath('.//tr[@data-indicator-row]')[0]
    assert 'Measured quantity' in row.text_content()
    assert '17.50' in row.text_content() and '2026-01-02' in row.text_content()
    assert '最新值' in root.text_content() and '较上一观测' in root.text_content()
    scripts = html.xpath('//script[@src]/@src')
    assert any('/static/readonly-indicators.js?' in script for script in scripts)
    assert not any('/static/indicators.js?' in script or '/static/indicator-preferences.js?' in script
                   for script in scripts)
    assert not root.xpath('.//*[@data-favorite or @data-compare or @data-series-chart]')
    assert not root.xpath('.//a[not(starts-with(@href,"#"))]')
    for element in ('indicator-favorites', 'indicator-compare-dialog', 'indicator-sources',
                    'indicator-dictionary', 'indicator-checks'):
        assert not root.xpath(f'.//*[@id="{element}"]')
    assert state['bounded_requests'][-1]['series_ids'] == [SERIES]
    assert state['bounded_requests'][-1]['points_per_series'] == 2
    assert state['pages'] == 0
    assert 'source_cell' not in response.text and 'source_file' not in response.text
    output = os.environ.get('RESTRICTED_INDICATOR_ARTIFACTS')
    if output:
        folder = Path(output)
        folder.mkdir(parents=True, exist_ok=True)
        (folder / 'indicators.html').write_text(response.text)


def test_shared_indicator_empty_state_stays_in_original_layout_without_fact_scan(canonical):
    client, headers, state, _ = canonical
    delegated(state, ('indicators',))
    response = client.get(page(FIRST, '/sn/indicators'), headers=headers)
    assert response.status_code == 200, response.text
    html = fromstring(response.text)
    root = html.get_element_by_id('indicator-workspace')
    assert root.get('data-readonly') == 'true'
    assert '尚无已绑定的指标' in root.text_content()
    assert not root.xpath('.//tr[@data-indicator-row]')
    assert not state.get('bounded_requests') and state['pages'] == 0


def test_shared_indicator_missing_owner_does_not_reuse_visitor_records(canonical):
    client, headers, state, _ = canonical
    save_core()
    delegated(state, ('indicators',))
    state['owner_missing'] = True
    response = client.get(page(FIRST, '/sn/indicators'), headers=headers)
    assert response.status_code == 200, response.text
    html = fromstring(response.text)
    root = html.get_element_by_id('indicator-workspace')
    assert '主人归属尚未核验' in root.text_content()
    assert not root.xpath('.//tr[@data-indicator-row]')
    assert 'Measured quantity' not in root.text_content()
