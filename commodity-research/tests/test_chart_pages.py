"""Chart HTML and permission branches; no HTTP transport or live service needed."""
from contextlib import contextmanager
import json
from types import SimpleNamespace
from urllib.parse import urlsplit

from fastapi import FastAPI, Request
from lxml.html import fromstring
import pytest
from starlette.routing import Match

from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, current_workbench
from tin.web import app as web
from tin.web.chart_pages import build_router


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

    @app.get('/fixture-indicator')
    def indicator(request: Request):
        detail = {'series_id': "kb-fixture'<>\"", 'name': '合成指标 <script>bad()</script>', 'history': [],
                  'unit': '吨', 'frequency': '日', 'caliber': [], 'revisions': [], 'fetch_mode': 'manual'}
        return web.templates.TemplateResponse(request, 'indicator.html', shell(None, 'indicators', detail=detail,
            usage={'judgment': [], 'nodes': [], 'crosscheck': []}))

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


@pytest.mark.parametrize('path, marker', [('/sn/charts', 'chart-cards'), ('/sn/charts/import', 'chart-import-sheets'),
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
    assert html.xpath('//nav//a[@href="/workbenches/fixture-workbench/sn/charts"]')
    if path.endswith('/import'):
        assert config(response)['id'] is None, '静态导入路由优先于动态ID'
    if path.endswith('chart-1'):
        assert config(response)['id'] == 'chart-1'
        assert 'disabled' in html.get_element_by_id('chart-editor-fields').attrib


@pytest.mark.parametrize('headers', [{'x-role': 'viewer'}, {'x-edit': 'false'}])
def test_readonly_controls_and_reason(pages, headers):
    client, _ = pages
    response = client.get('/sn/charts/chart-1', headers=headers)
    assert response.status_code == 200
    assert not config(response)['can_write']
    html = fromstring(response.text)
    assert '没有研究工作台编辑权限' in html.get_element_by_id('chart-write-reason').text_content()
    assert 'disabled' in html.get_element_by_id('chart-editor-fields').attrib


@pytest.mark.parametrize('path', ['/sn/charts', '/sn/charts/import', '/sn/charts/chart-1'])
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
