"""The original indicator layout has a bounded, read-only shared variant."""
import os
from pathlib import Path

from lxml.html import fromstring
import pytest

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
    # No precision is supplied by this source; use the shared magnitude format.
    assert row.xpath('./td[3]')[0].text_content().strip() == '17.5'
    assert '2026-01-02' in row.text_content()
    assert '最新值' in root.text_content() and '较上一观测' in root.text_content()
    scripts = html.xpath('//script[@src]/@src')
    assert any('/static/readonly-indicators.js?' in script for script in scripts)
    assert not any('/static/indicators.js?' in script or '/static/indicator-preferences.js?' in script
                   or '/static/chart-hover.js?' in script or '/static/favorites.js?' in script
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


@pytest.mark.parametrize('field_metadata,row_metadata,value,expected,change', [
    ({'display_decimals': 2}, {}, 17.5, '17.50', '+2.50'),
    ({'caliber': {'decimals': 3}}, {}, 17.5, '17.500', '+2.500'),
    ({'display_decimals': 4}, {'display_decimals': 2}, 17.5, '17.50', '+2.50'),
    ({'display_decimals': 2}, {'decimals': 0}, 17.5, '18', '+2'),
    ({'display_decimals': 0}, {'display_decimals': 3}, 0, '0.000', '−15.000'),
])
def test_shared_indicator_preserves_source_precision_and_observation_override(
        canonical, field_metadata, row_metadata, value, expected, change):
    client, headers, state, _ = canonical
    save_core()
    delegated(state, ('indicators',))
    state.update(bounded_spec_metadata=field_metadata, bounded_row_metadata=row_metadata,
                 bounded_previous=15, value=value)
    response = client.get(page(FIRST, '/sn/indicators'), headers=headers)
    assert response.status_code == 200, response.text
    row = fromstring(response.text).xpath('//tr[@data-indicator-row]')[0]
    assert row.xpath('./td[3]')[0].text_content().strip() == expected
    assert row.xpath('./td[4]')[0].text_content().strip().startswith(change)
    assert state['pages'] == 0


@pytest.mark.parametrize('field_metadata,row_metadata,expected', [
    ({'display_decimals': 2}, {}, 2),
    ({'display_decimals': 3}, {'decimals': 0}, 0),
])
def test_multi_source_restricted_values_keep_verified_precision(
        session, monkeypatch, field_metadata, row_metadata, expected):
    from tin.integration import page_facts
    from tin.integration.workbench_scope import WorkbenchScope
    from tin.web.restricted_views import necessary_values
    from tests.test_workbench_sources_v2 import IDENTITY, WB, source
    session.info['workbench_id'] = WB
    scope = WorkbenchScope(IDENTITY, WB, '', 0, {'workspace_mode': 'isolated'}, 'Bearer fake',
        scope_version=2, sources_revision=7, sources=(source(1),))
    monkeypatch.setattr(page_facts, 'bounded_facts', lambda *args, **kwargs: {
        'indicators': [{'series_id': SERIES, **field_metadata}],
        'rows': [{'series_id': SERIES, 'date': '2026-01-02', 'value': 17.5, **row_metadata}]})
    values = necessary_values(session, scope, [SERIES], 'indicators')[SERIES]
    assert values['value'] == 17.5 and values['display_decimals'] == expected


@pytest.mark.parametrize('field_metadata,row_metadata,expected,change', [
    ({'display_decimals': 2}, {}, '17.50', '+2.50'),
    ({'display_decimals': 2}, {'decimals': 0}, '18', '+2'),
    ({'display_decimals': 0}, {'display_decimals': 2}, '17.50', '+2.50'),
])
def test_shared_market_preserves_source_precision_through_compact_cards(
        canonical, field_metadata, row_metadata, expected, change):
    client, headers, state, _ = canonical
    save_core()
    delegated(state, ('market',))
    state.update(bounded_spec_metadata=field_metadata, bounded_row_metadata=row_metadata,
                 bounded_previous=15)
    response = client.get(page(FIRST, '/sn'), headers=headers)
    assert response.status_code == 200, response.text
    html = fromstring(response.text)
    assert html.xpath('//*[contains(@class,"restricted-value")]')[0].text_content().strip() == expected
    assert html.xpath(f'//*[@aria-label="较上期 {change}"]')
    assert state['pages'] == 0


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
    assert '暂时无法确认这份研究台的共享范围' in root.text_content()
    assert not root.xpath('.//tr[@data-indicator-row]')
    assert 'Measured quantity' not in root.text_content()
