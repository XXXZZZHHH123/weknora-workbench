"""The bounded directory distinguishes scope totals, current rows and page groups."""
from pathlib import Path
import shutil
import subprocess

from jinja2 import ChoiceLoader, DictLoader, Environment, FileSystemLoader
from lxml.html import fromstring
from tin.web.app import _num


def render_page(*, total=1003, offset=200, count=2, restricted=False):
    env = Environment(loader=ChoiceLoader([
        DictLoader({'base.html': '{% block main %}{% endblock %}'}),
        FileSystemLoader(Path(__file__).parents[1] / 'src/tin/web/templates'),
    ]), autoescape=True)
    env.globals.update(research_workbench=lambda: None, research_prefix=lambda: '',
                       research_static_prefix=lambda: '', static_version=lambda: 'fixture')
    env.filters['num'] = _num
    rows = [{'series_id': f'fixture.{index}', 'name': f'Fixture field {index}', 'unit': '件',
             'category': 'Fixture category', 'macro': False, 'vendor_code': '', 'source': 'Fixture source',
             'caliber': {}, 'registered': False, 'frequency': '日', 'fetch_mode': 'manual',
             'value': None, 'change': None, 'as_of': None} for index in range(count)]
    result = env.get_template('indicators.html').render(
        rows=rows, groups=[{'name': 'Fixture category', 'rows': rows}] if rows else [],
        restricted_view=restricted, restricted_data={}, d=None, catalog_remote=True,
        catalog_offset=offset, catalog_total=total,
        catalog_next=offset + count if offset + count < total else None,
        checks=[], dimensions=[], sources={'rows': []})
    return fromstring(result)


def test_directory_header_shows_total_and_both_compact_pagers_are_unambiguous():
    html = render_page()
    assert '浏览 1003 项指标' in html.get_element_by_id('indicator-top').text_content()
    assert html.get_element_by_id('indicator-result-count').text_content() == '本页 2 项 · 范围共 1003 项'
    for suffix in ('-top', ''):
        assert html.get_element_by_id('indicator-page-status' + suffix).text_content() == '201–202 / 1003 项'
        assert 'disabled' not in html.get_element_by_id('indicator-prev-page' + suffix).attrib
        assert 'disabled' not in html.get_element_by_id('indicator-next-page' + suffix).attrib
    ordered_ids = html.xpath('//*[@id]/@id')
    assert len(ordered_ids) == len(set(ordered_ids))
    assert ordered_ids.index('indicator-page-status-top') < ordered_ids.index('indicator-catalog-groups')
    assert ordered_ids.index('indicator-page-status') > ordered_ids.index('indicator-catalog-groups')
    nav = html.xpath('//nav[@aria-label="指标分类"]')[0]
    assert '本页分类' in nav.text_content()
    assert '本页全部 2' in ' '.join(nav.text_content().split())
    assert nav.xpath('.//*[@data-jump-total]/text()') == ['2']


def test_empty_directory_disables_both_pagers_and_does_not_invent_a_first_row():
    html = render_page(total=0, offset=0, count=0)
    for suffix in ('-top', ''):
        assert html.get_element_by_id('indicator-page-status' + suffix).text_content() == '0–0 / 0 项'
        assert 'disabled' in html.get_element_by_id('indicator-prev-page' + suffix).attrib
        assert 'disabled' in html.get_element_by_id('indicator-next-page' + suffix).attrib
    assert not html.xpath('//*[@data-indicator-row]')


def test_restricted_directory_never_exposes_full_catalog_totals_or_pagers():
    html = render_page(restricted=True)
    assert html.get_element_by_id('indicator-workspace').get('data-catalog-total') == '2'
    assert html.get_element_by_id('indicator-workspace').get('data-catalog-offset') == '0'
    assert '浏览 2 项指标' in html.get_element_by_id('indicator-top').text_content()
    assert '1003' not in html.text_content()
    assert not html.xpath('//*[@data-catalog-page-prev or @data-catalog-page-next]')
    assert not html.xpath('//*[@id="indicator-next-page" or @id="indicator-next-page-top"]')
    assert not html.xpath('//*[@data-favorite or @data-compare or @data-series-chart]')


def test_actual_catalog_scripts_keep_pagers_synced_without_preloading_the_directory():
    node = shutil.which('node')
    assert node, 'Node is required for the directory interaction checks'
    result = subprocess.run([node, '--test', str(Path(__file__).with_name('native-catalog-v2.test.cjs'))],
                            capture_output=True, text=True, timeout=15)
    assert result.returncode == 0, result.stdout + result.stderr
