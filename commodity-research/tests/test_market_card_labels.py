"""Compact names never rewrite source evidence or guess a ratio's meaning."""
from copy import deepcopy
from dataclasses import replace
from datetime import date
import os
from pathlib import Path
import re

from lxml.html import fromstring
import pytest

from tin.export.board import Card, _obs_card
from tin.web.app import templates
from tin.web.market_card_labels import compact_market_card, compact_market_cards
from tin.web.restricted_views import _market_cards
from tests.test_knowledge_facts import canonical, FIRST, SERIES, page


@pytest.fixture
def source_card():
    return Card(key='selected-field', series_id='kb-' + 'a' * 48,
        label='Source price / code-42 / 日 / Long original coverage', unit='元/吨', status='ok',
        value=100, as_of='2026-01-02', change=2, badge='知识库结构化', source='知识库结构化',
        caliber='Original caliber', detail={'series_id': 'kb-' + 'a' * 48,
            'caliber': {'time_type': '原表日期'}, 'source_url': 'https://source.test/evidence',
            'revision': 2, 'note': 'Original provenance note'})


def render(card, compact=True):
    return templates.get_template('_market_cards.html').render(cards=[card], restricted_view=False) if compact else (
        templates.env.from_string("{% from '_card.html' import card with context %}{{ card(c) }}")
        .render(c=card, restricted_view=False))


def test_explicit_display_name_has_priority_and_original_evidence_is_unchanged(source_card):
    detail = deepcopy(source_card.detail)
    card = compact_market_card(source_card, {'metric': source_card.label, 'display_name': 'Visible price',
        'name': 'Alternate source name', 'code': 'code-42', 'frequency': '日', 'coverage': 'Long original coverage'})
    assert card.label == 'Visible price' and card.series_id == source_card.series_id and card.value == source_card.value
    assert source_card.detail == detail and source_card.label.startswith('Source price /')
    assert all(card.detail[key] == value for key, value in detail.items())
    assert card.detail['display']['full_name'] == source_card.label
    tree = fromstring(render(card))
    title = tree.xpath('//*[@data-market-card-title]')[0]
    assert title.text_content() == 'Visible price' and 'truncate' in title.get('class')
    info = tree.xpath('//*[@data-popover]')[0]
    body = deepcopy(tree)
    for node in body.xpath('//*[@data-popover]'): node.getparent().remove(node)
    assert '知识库结构化' not in body.text_content() and 'Original caliber' not in body.text_content()
    assert 'code-42' not in body.text_content() and 'Long original coverage' not in body.text_content()
    assert all(text in info.text_content() for text in [source_card.label, 'code-42', 'Long original coverage',
        'Original caliber', 'Original provenance note', '知识库结构化'])
    assert info.xpath('.//a/@href') == ['https://source.test/evidence']
    assert tree.xpath('//*[@data-popover-trigger]')[0].text_content().strip() == '口径 ⓘ'


@pytest.mark.parametrize('metadata', [{'unit': 'code-42'}, {'code': 'code-42'},
                                       {'frequency': 'code-42'}, {'vendor_code': 'code-42'}])
def test_decorated_header_shortens_only_with_literal_metadata_evidence(source_card, metadata):
    card = compact_market_card(source_card, {'metric': source_card.label, **metadata})
    assert card.label == 'Source price'
    assert card.detail['display']['full_name'] == source_card.label


@pytest.mark.parametrize('label', ['Exports / Imports', 'Brent / WTI', '库存 / 需求 / 周'])
def test_ratio_and_comparison_names_are_not_cut_at_an_unverified_slash(source_card, label):
    card = compact_market_card(replace(source_card, label=label),
        {'metric': label, 'frequency': '周', 'unit': '吨'})
    assert card.label == label
    assert fromstring(render(card)).xpath('//*[@data-market-card-title]')[0].text_content() == label


def test_unproven_compound_name_keeps_full_text_with_single_line_ellipsis(source_card):
    card = compact_market_card(source_card, {'metric': source_card.label})
    assert card.label == source_card.label
    title = fromstring(render(card)).xpath('//*[@data-market-card-title]')[0]
    assert 'truncate' in title.get('class') and title.get('title') == source_card.label


def test_plain_metadata_reading_is_not_a_raw_json_block_and_templates_escape_it(source_card):
    card = compact_market_card(source_card, {'display_name': '<script>bad()</script>',
        'coverage': {'start': '2020-01-01', 'end': '2026-01-02'}})
    html = render(card)
    assert '<script>bad' not in html and '&lt;script&gt;' in html
    assert 'start：2020-01-01；end：2026-01-02' in fromstring(html).text_content()
    assert '{"start"' not in html


def test_compaction_uses_current_catalog_without_extra_sql_or_mutating_indicator_names(source_card):
    class Session:
        info = {'knowledge_facts_catalog': {source_card.series_id:
            {'metric': source_card.label, 'display_name': 'Catalog title'}}}
    card = compact_market_cards(Session(), [source_card])[0]
    assert card.label == 'Catalog title' and card.series_id == source_card.series_id
    assert source_card.label != card.label


def test_restricted_market_only_reuses_selected_text_metadata_without_source_ids(source_card):
    identifier = source_card.series_id
    values = {identifier: {'name': source_card.label, 'display_name': 'Allowed label',
        'unit': '元/吨', 'frequency': '日', 'date': '2026-01-02', 'value': 100,
        'change': 2, 'change_pct': 2, 'display_metadata': {'code': 'code-42'}}}
    card = _market_cards([identifier], values, compact=True)[0]
    assert card.label == 'Allowed label' and card.detail['display']['full_name'] == source_card.label
    assert set(card.detail) == {'display'}
    assert _market_cards([identifier], values)[0].label == source_card.label
    assert 'source_knowledge_id' not in render(card) and 'dataset_id' not in render(card)


def test_core_refresh_and_saved_card_have_compact_title_but_preserve_catalog_identity(canonical, monkeypatch):
    import httpx
    client, headers, state, _ = canonical
    original = httpx.request
    full = 'Recorded price / source-code / 日 / Original coverage'
    def enriched(method, url, **kwargs):
        response = original(method, url, **kwargs)
        if str(url).endswith('/api/research/facts') and response.status_code == 200:
            document = response.json()
            for spec in document.get('indicators', []):
                spec.update(metric=full, display_name='Recorded price', code='source-code',
                            frequency='日', coverage='Original coverage')
            return httpx.Response(200, json=document, request=response.request)
        return response
    monkeypatch.setattr(httpx, 'request', enriched)
    refresh = client.get(page(FIRST, '/api/sn/market/core'), params={'series_id': SERIES}, headers=headers)
    assert refresh.status_code == 200, refresh.text
    tree = fromstring(refresh.json()['html'])
    assert tree.xpath('//*[@data-market-card-title]')[0].text_content() == 'Recorded price'
    assert full in tree.xpath('//*[@data-popover]')[0].text_content()
    assert tree.xpath('//*[@data-market-card-series]/@data-market-card-series') == [SERIES]
    saved = client.post(page(FIRST, '/api/sn/market/core/config'), headers=headers, json={'series_ids': [SERIES]})
    assert saved.status_code == 200
    response = client.get(page(FIRST, '/sn'), headers=headers)
    assert response.status_code == 200
    assert fromstring(response.text).xpath('//*[@data-market-card-title]')[0].text_content() == 'Recorded price'


def test_other_card_pages_keep_their_existing_badge_and_footer(source_card):
    html = render(source_card, compact=False)
    tree = fromstring(html)
    assert not tree.xpath('//*[@data-market-card-title]')
    body = deepcopy(tree)
    for node in body.xpath('//*[@data-popover]'): node.getparent().remove(node)
    assert '知识库结构化' in body.text_content() and 'Original caliber' in body.text_content()


def test_inline_popover_handlers_use_the_application_window_namespace(source_card):
    directory = Path(__file__).parents[1] / 'src/tin/web/templates'
    for template in directory.glob('*.html'):
        for match in re.finditer(r'''onclick\s*=\s*(["'])(.*?)\1''', template.read_text(), re.S):
            assert not re.search(r'(?<![\w.])togglePopover\s*\(', match[2]), template.name
    card = compact_market_card(source_card, {'display_name': 'Visible price'})
    fragment = render(card)
    handlers = fromstring(fragment).xpath('//*[@onclick]/@onclick')
    assert handlers and all(handler.startswith('window.togglePopover(') for handler in handlers)
    output = os.environ.get('MARKET_POPOVER_ARTIFACTS')
    if output:
        target = Path(output)
        target.mkdir(parents=True, exist_ok=True)
        (target / 'popover.html').write_text('<!doctype html><html><head><meta charset="utf-8">'
            '<style>.hidden{display:none}body{padding:20px}</style><script src="/app.js"></script>'
            '</head><body>' + fragment + '</body></html>')
