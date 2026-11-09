"""Chart HTML and permission branches; no HTTP transport or live service needed."""
from contextlib import contextmanager
import json
from types import SimpleNamespace
from urllib.parse import parse_qs, urlsplit

from fastapi import FastAPI, Request
from lxml.html import fromstring
import pytest
from starlette.routing import Match

from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, current_workbench
from tin.web import app as web
from tin.web.chart_pages import build_router, page_config


def endpoints(routes):
    # FastAPI can keep included routers as nested objects instead of flattening.
    for route in routes:
        if hasattr(route, 'original_router'):
            yield from endpoints(route.original_router.routes)
        else:
            yield route


@pytest.fixture
def pages():
    sessions = []

    @contextmanager
    def page_session(module, **kwargs):
        sessions.append((module, kwargs))
        yield SimpleNamespace(info={})

    def shell(session, nav, **extra):
        return {'nav': nav, 'ticker': [], 'd': None, 'names': {}, **extra}

    app = FastAPI()
    app.include_router(build_router(page_session, shell, web.templates))

    @app.get('/sn/indicators')
    def indicator_tabs(request: Request):
        return web.templates.TemplateResponse(request, 'indicators.html', shell(None, 'indicators',
            rows=[], groups=[], catalog_remote=True, catalog_total=0, catalog_offset=0, catalog_next=None,
            dimensions=[], checks=[], sources={'rows': []}, chart_page=page_config(request)))

    @app.get('/fixture-indicator')
    def indicator(request: Request):
        detail = {'series_id': "kb-fixture'<>\"", 'name': '合成指标 <script>bad()</script>', 'history': [],
                  'unit': '吨', 'frequency': '日', 'caliber': [], 'revisions': [], 'fetch_mode': 'manual'}
        return web.templates.TemplateResponse(request, 'indicator.html', shell(None, 'indicators', detail=detail,
            usage={'judgment': [], 'nodes': [], 'crosscheck': []}))

    @app.get('/fixture-reports')
    def reports(request: Request):
        scope = current_workbench.get()
        return web.templates.TemplateResponse(request, 'reports.html', shell(None, 'reports',
            restricted_view=bool(scope and scope.restricted), restricted_data={'briefs': [], 'entries': []},
            period_kind=request.query_params.get('period', 'daily'), live_report=True,
            files=[], selected={'date': request.query_params.get('date')}, report=None))

    def get(url, headers=None):
        # Invoke the actual registered endpoint without a TestClient background
        # loop: the restricted runner disallows its local socket wakeups.
        headers = headers or {}
        parsed = urlsplit(url)
        request_scope = {'type': 'http', 'method': 'GET', 'scheme': 'http', 'path': parsed.path,
                         'query_string': parsed.query.encode(), 'headers': [(b'host', b'fixture.invalid')],
                         'server': ('fixture.invalid', 80)}
        identity = Identity('fixture', 'fixture@example.invalid', '测试', 1, headers.get('x-role', 'owner'))
        scope = WorkbenchScope(identity, 'fixture-workbench', '', 0,
            {'name': '测试研究台 <安全>', 'can_edit': headers.get('x-edit', 'true') == 'true',
             'can_read_source': headers.get('x-restricted') != 'true'},
            scope_version=int(headers.get('x-version', '2')),
            sources=({'id': 'fixture', 'delegated': headers.get('x-restricted') == 'true'},))
        a = current_identity.set(identity)
        b = current_workbench.set(None if headers.get('x-local') == 'true' else scope)
        try:
            for route in endpoints(app.routes):
                match, child_scope = route.matches(request_scope)
                if match == Match.FULL:
                    request_scope.update(child_scope)
                    response = route.endpoint(Request(request_scope), **child_scope['path_params'])
                    return SimpleNamespace(status_code=response.status_code, text=response.body.decode(), headers=response.headers)
            raise AssertionError('Missing HTML route: ' + parsed.path)
        finally:
            current_identity.reset(a)
            current_workbench.reset(b)

    return SimpleNamespace(get=get), sessions


def config(response, element='chart-page-config'):
    return json.loads(fromstring(response.text).get_element_by_id(element).text)


@pytest.mark.parametrize('path, marker', [('/sn/charts/import', 'chart-import-sheets'),
                                        ('/sn/charts/chart-1', 'chart-editor')])
def test_templates_and_page_projection(pages, path, marker):
    client, sessions = pages
    response = client.get(path + '?date=2030-01-02')
    assert response.status_code == 200, response.text
    html = fromstring(response.text)
    assert html.get_element_by_id(marker) is not None
    assert config(response)['enabled'] and config(response)['can_write']
    assert config(response)['date'] == '2030-01-02'
    assert sessions == [('indicators', {'series_ids': [], 'points_per_series': 1})]
    assert html.xpath('//nav//a[@href="/workbenches/fixture-workbench/sn/indicators" and @aria-current="page"]')
    assert not html.xpath('//nav//a[contains(@href,"/sn/charts")]')
    assert html.xpath('//a[@href="/workbenches/fixture-workbench/sn/indicators?tab=charts&date=2030-01-02" and text()="返回图表库"]')
    if path.endswith('/import'):
        assert config(response)['id'] is None, '静态导入路由优先于动态ID'
    if path.endswith('chart-1'):
        assert config(response)['id'] == 'chart-1'
        assert 'disabled' in html.get_element_by_id('chart-editor-fields').attrib
        assert html.get_element_by_id('chart-reconvert').text_content() == '按原表重新转换'
        assert html.xpath('//input[@name="visible_cycles" and @min="1" and @max="40"]')


@pytest.mark.parametrize('headers', [{'x-role': 'viewer'}, {'x-edit': 'false'}])
def test_readonly_controls_and_reason(pages, headers):
    client, _ = pages
    response = client.get('/sn/charts/chart-1', headers=headers)
    assert response.status_code == 200
    assert not config(response)['can_write']
    html = fromstring(response.text)
    assert '没有研究工作台编辑权限' in html.get_element_by_id('chart-write-reason').text_content()
    assert 'disabled' in html.get_element_by_id('chart-editor-fields').attrib
    assert not html.xpath('//*[@id="chart-reconvert"]')


@pytest.mark.parametrize('path', ['/sn/charts/import', '/sn/charts/chart-1'])
@pytest.mark.parametrize('headers, status, reason', [({'x-restricted': 'true'}, 403, '受限视角'),
    ({'x-version': '1'}, 409, '重新打开研究台'), ({'x-local': 'true'}, 409, '来源版本2')])
def test_unavailable_pages_explain_without_reading_sources(pages, path, headers, status, reason):
    client, sessions = pages
    response = client.get(path, headers=headers)
    assert response.status_code == status
    assert response.headers['content-type'].startswith('text/html')
    assert reason in response.text and not config(response)['enabled']
    assert not sessions
    if status == 403:
        assert not fromstring(response.text).xpath('//nav//a[contains(@href,"/sn/charts")]')


def test_indicator_keeps_existing_panels_and_escapes_text(pages):
    client, _ = pages
    response = client.get('/fixture-indicator?date=2030-01-02')
    assert response.status_code == 200, response.text
    html = fromstring(response.text)
    for marker in ('ind-chart', 'indicator-chart-usage', 'indicator-cycle-panel', 'indicator-related-panel'):
        assert html.get_element_by_id(marker) is not None
    assert '口径' in html.text_content() and 'WHO DEPENDS ON THIS' in html.text_content()
    assert not html.xpath('//script[contains(text(),"bad()") and not(@type="application/json") and not(contains(text(),"window.IND"))]')
    assert '&lt;script&gt;bad()&lt;/script&gt;' in response.text
    data = config(response, 'indicator-cycle-config')
    assert data['enabled'] is True and data['can_write'] is True
    assert data['date'] == '2030-01-02'
    assert data['series_id'] == "kb-fixture'<>\""
    readonly = client.get('/fixture-indicator', headers={'x-role': 'viewer'})
    assert not config(readonly, 'indicator-cycle-config')['can_write']
    assert '没有研究工作台编辑权限' in readonly.text


def test_page_router_is_registered_in_real_app():
    paths = {route.path for route in endpoints(web.app.routes) if getattr(route, 'endpoint', None)
             and route.endpoint.__module__ == 'tin.web.chart_pages'}
    assert paths == {'/sn/charts', '/sn/charts/import', '/sn/charts/{chart_id}'}


def test_detail_notices_are_small_text_directly_below_the_chart(pages):
    client, _ = pages
    html = fromstring(client.get('/sn/charts/chart-1').text)
    plot = html.get_element_by_id('chart-plot')
    notice = html.get_element_by_id('chart-notices')
    assert plot.getnext() == notice
    assert notice.tag == 'p' and notice.get('class') == 'charts-footnote'
    assert notice.get('role') == 'status'


def test_library_anomalies_are_expanded_before_the_chart_list(pages):
    client, _ = pages
    html = fromstring(client.get('/sn/indicators?tab=charts').text)
    panel = html.get_element_by_id('chart-anomalies')
    assert panel.tag == 'details' and 'open' in panel.attrib
    assert '本周同期异动' in panel.xpath('./summary')[0].text_content()
    assert panel.getnext().get('class') == 'charts-layout'
    more = html.get_element_by_id('chart-anomalies-more')
    assert more.get('aria-expanded') == 'false' and more.get('aria-controls') == 'chart-anomalies-rows'


def test_chart_index_redirect_preserves_query_and_overrides_tab_without_reading_sources(pages):
    client, sessions = pages
    for headers in ({}, {'x-restricted': 'true'}, {'x-version': '1'}):
        response = client.get('/sn/charts?date=2030-01-02&q=a%26b&tab=catalog&tag=a&tag=b', headers)
        assert response.status_code == 307
        location = urlsplit(response.headers['location'])
        assert location.path == '/workbenches/fixture-workbench/sn/indicators'
        assert parse_qs(location.query) == {'tab': ['charts'], 'date': ['2030-01-02'], 'q': ['a&b'], 'tag': ['a', 'b']}
    assert not sessions


def test_indicator_three_tabs_share_chart_library_content_and_accessible_stars(pages):
    client, _ = pages
    response = client.get('/sn/indicators?tab=charts&date=2030-01-02')
    html = fromstring(response.text)
    assert html.xpath('//*[@data-indicator-tab]/@data-indicator-tab') == ['favorites', 'catalog', 'charts']
    assert len(html.xpath('//*[@id="chart-cards"]')) == 1
    assert len(html.xpath('//script[contains(@src,"/static/charts.js")]')) == 1
    assert len(html.xpath('//script[contains(@src,"/static/chart-hover.js?v=") and @defer]')) == 1
    assert html.xpath('//a[contains(@href,"/sn/charts/import")]')
    assert config(response)['date'] == '2030-01-02'
    for path in ('/fixture-indicator', '/sn/charts/chart-1'):
        page = fromstring(client.get(path).text)
        assert page.xpath('//button[@data-favorite and @aria-pressed="false" and @type="button"]')


@pytest.mark.parametrize('period', ['daily', 'weekly'])
def test_daily_and_weekly_report_entry_keeps_date_and_workbench_prefix(pages, period):
    client, _ = pages
    html = fromstring(client.get('/fixture-reports?period=' + period + '&date=2030-01-02').text)
    link = html.get_element_by_id('report-chart-anomalies')
    assert link.text_content() == '本周同期异动'
    assert link.get('href') == '/workbenches/fixture-workbench/sn/charts?date=2030-01-02#chart-anomalies'


@pytest.mark.parametrize('headers', [{'x-restricted': 'true'}, {'x-version': '1'}, {'x-local': 'true'}])
def test_report_does_not_offer_an_unavailable_chart_library(pages, headers):
    client, _ = pages
    html = fromstring(client.get('/fixture-reports', headers=headers).text)
    assert not html.xpath('//*[@id="report-chart-anomalies"]')


@pytest.mark.parametrize('headers', [{'x-restricted': 'true'}, {'x-version': '1'}, {'x-local': 'true'}])
def test_indicator_unavailable_cycle_has_reason_and_no_writable_config(pages, headers):
    client, _ = pages
    response = client.get('/fixture-indicator', headers=headers)
    assert response.status_code == 200
    data = config(response, 'indicator-cycle-config')
    assert data['enabled'] is False and data['can_write'] is False
    assert '受限视角' in data['reason'] or '重新打开研究台' in data['reason']
    html = fromstring(response.text)
    assert 'disabled' in html.get_element_by_id('indicator-cycle-save').attrib
    if headers.get('x-restricted'):
        assert not html.xpath('//nav//a[contains(@href,"/sn/charts")]')
