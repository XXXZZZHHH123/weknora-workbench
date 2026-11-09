"""PPT HTML permissions, navigation and registration without running a service."""
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
from tin.web.ppt_pages import build_router


def endpoints(routes):
    for route in routes:
        if hasattr(route, 'original_router'):
            yield from endpoints(route.original_router.routes)
        else:
            yield route


@pytest.fixture(autouse=True)
def ppt_feature_enabled(monkeypatch):
    # The feature is off by default in this release; these tests cover the kept code.
    monkeypatch.setattr(web.settings, 'ppt_enabled', True)
    monkeypatch.setitem(web.templates.env.globals, 'ppt_enabled', True)


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

    @app.get('/fixture-reports')
    def reports(request: Request):
        scope = current_workbench.get()
        return web.templates.TemplateResponse(request, 'reports.html', shell(None, 'reports',
            restricted_view=bool(scope and scope.restricted), restricted_data={'briefs': [], 'entries': []},
            period_kind=request.query_params.get('period', 'daily'), live_report=True,
            files=[], selected={'date': request.query_params.get('date')}, report=None))

    def get(url, **headers):
        parsed = urlsplit(url)
        request_scope = {'type': 'http', 'method': 'GET', 'scheme': 'http', 'path': parsed.path,
                         'query_string': parsed.query.encode(), 'headers': [(b'host', b'fixture.invalid')],
                         'server': ('fixture.invalid', 80)}
        identity = Identity('fixture', 'fixture@example.invalid', '测试', 1, headers.get('role', 'owner'))
        scope = WorkbenchScope(identity, 'fixture-workbench', '', 0,
            {'name': '合成研究台', 'can_edit': headers.get('edit', True),
             'can_read_source': not headers.get('restricted', False)},
            scope_version=headers.get('version', 2),
            sources=({'id': 'fixture', 'delegated': headers.get('restricted', False)},))
        a = current_identity.set(identity)
        b = current_workbench.set(None if headers.get('local') else scope)
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

    return get, sessions


def config(response):
    return json.loads(fromstring(response.text).get_element_by_id('ppt-page-config').text)


@pytest.mark.parametrize('path,marker,identifier', [
    ('/sn/reports/ppt/template-1', 'ppt-save', 'template-1'),
    ('/sn/reports/ppt/template-1/export', 'ppt-export-fields', 'template-1')])
def test_shell_uses_only_bounded_metadata_projection(pages, path, marker, identifier):
    get, sessions = pages
    response = get(path)
    assert response.status_code == 200, response.text
    html = fromstring(response.text)
    assert html.get_element_by_id(marker) is not None
    assert 'disabled' in html.get_element_by_id(marker).attrib
    assert config(response) == {'enabled': True, 'can_write': True, 'id': identifier,
                                'date': None, 'write_reason': ''}
    assert sessions == [('indicators', {'series_ids': [], 'points_per_series': 1})]
    assert html.xpath('//nav[@aria-label="商品研究导航"]//a[@href="/workbenches/fixture-workbench/sn/reports" and @aria-current="page"]')
    assert not html.xpath('//nav[@aria-label="商品研究导航"]//a[contains(@href,"/sn/ppt")]')
    breadcrumb = html.xpath('//nav[@aria-label="面包屑"]')[0]
    assert '日报周报' in breadcrumb.text_content() and 'PPT 版式' in breadcrumb.text_content()
    assert breadcrumb.xpath('.//*[@id="ppt-breadcrumb-title"]')
    if path.endswith('/export'):
        assert html.xpath('//a[@href="/workbenches/fixture-workbench/sn/reports/ppt/template-1"]')
    if marker == 'ppt-save':
        assert html.get_element_by_id('ppt-export-link').get('href') == '/workbenches/fixture-workbench/sn/reports/ppt/template-1/export'


@pytest.mark.parametrize('path', ['/fixture-reports', '/sn/reports/ppt/template-1', '/sn/reports/ppt/template-1/export'])
@pytest.mark.parametrize('headers', [{'role': 'viewer'}, {'edit': False}])
def test_readonly_shell_explains_disabled_writes(pages, path, headers):
    get, _ = pages
    response = get(path, **headers)
    assert response.status_code == 200
    assert config(response)['enabled'] and not config(response)['can_write']
    html = fromstring(response.text)
    assert '没有研究工作台编辑权限' in html.get_element_by_id('ppt-write-reason').text_content()
    assert all('disabled' in node.attrib for node in html.xpath('//fieldset'))


@pytest.mark.parametrize('path', ['/sn/reports/ppt/template-1', '/sn/reports/ppt/template-1/export'])
@pytest.mark.parametrize('headers,status,reason', [({'restricted': True}, 403, '受限视角'),
    ({'version': 1}, 409, '重新打开研究台'), ({'local': True}, 409, '来源版本2')])
def test_denied_shells_explain_without_opening_sources(pages, path, headers, status, reason):
    get, sessions = pages
    response = get(path, **headers)
    assert response.status_code == status
    assert response.headers['content-type'].startswith('text/html')
    assert not config(response)['enabled'] and reason in response.text
    assert not sessions
    html = fromstring(response.text)
    assert not html.xpath('//*[@id="ppt-export-form" or @id="ppt-upload-form" or @id="ppt-save"]')
    if headers.get('restricted'):
        assert not html.xpath('//nav//a[contains(@href,"/sn/ppt")]')


def test_unsafe_identifier_is_json_and_html_escaped(pages):
    get, _ = pages
    identifier = 'template-"<script>alert(1)</script>'
    # Render directly so a slash in the attack text does not affect routing.
    response = get('/sn/reports/ppt/' + identifier.replace('/', ''))
    assert response.status_code == 200
    html = fromstring(response.text)
    assert config(response)['id'] == identifier.replace('/', '')
    assert not html.xpath('//script[contains(text(),"alert(1)") and not(@type="application/json")]')
    assert '\\u003cscript\\u003e' in response.text


def test_ppt_pages_are_registered_in_real_app():
    paths = {route.path for route in endpoints(web.app.routes) if getattr(route, 'endpoint', None)
             and route.endpoint.__module__ == 'tin.web.ppt_pages'}
    assert paths == {'/sn/ppt', '/sn/ppt/{template_id}', '/sn/ppt/{template_id}/export',
                     '/sn/reports/ppt/{template_id}', '/sn/reports/ppt/{template_id}/export'}


@pytest.mark.parametrize('path,target,fragment', [
    ('/sn/ppt', '/sn/reports', 'report-ppt'),
    ('/sn/ppt/template-1', '/sn/reports/ppt/template-1', ''),
    ('/sn/ppt/template-1/export', '/sn/reports/ppt/template-1/export', '')])
@pytest.mark.parametrize('headers', [{}, {'restricted': True}, {'version': 1}, {'local': True}])
def test_legacy_addresses_redirect_with_all_query_parameters_without_reading_sources(pages, path, target, fragment, headers):
    get, sessions = pages
    query = 'date=2030-01-02&period=weekly&q=a%26b&tag=a&tag=b'
    response = get(path + '?' + query, **headers)
    assert response.status_code == 307
    location = urlsplit(response.headers['location'])
    prefix = '' if headers.get('local') else '/workbenches/fixture-workbench'
    assert location.path == prefix + target
    assert location.fragment == fragment
    assert parse_qs(location.query) == parse_qs(query)
    assert not sessions


@pytest.mark.parametrize('date', ['2030-01-02', 'not-a-date', '<script>bad()</script>'])
def test_export_page_passes_cutoff_query_as_escaped_configuration(pages, date):
    get, _ = pages
    from urllib.parse import urlencode
    response = get('/sn/reports/ppt/template-1/export?' + urlencode({'date': date}))
    assert response.status_code == 200
    assert config(response)['date'] == date
    assert not fromstring(response.text).xpath('//script[contains(text(),"bad()") and not(@type="application/json")]')


@pytest.mark.parametrize('period', ['daily', 'weekly'])
def test_report_ppt_region_is_collapsed_and_offers_shared_upload_and_list(pages, period):
    get, sessions = pages
    response = get('/fixture-reports?period=' + period + '&date=2030-01-02')
    assert response.status_code == 200
    html = fromstring(response.text)
    panel = html.get_element_by_id('report-ppt')
    assert panel.tag == 'details' and 'open' not in panel.attrib
    assert '导出为 PPT' in panel.xpath('./summary')[0].text_content()
    # A report page offers PPT from the 导出 menu; an empty report page shows the region directly.
    if html.xpath('//*[contains(@class,"report-document")]'):
        assert panel.get('hidden') is not None
        assert html.xpath('//details[contains(@class,"report-export-menu")]//*[@data-open-report-ppt]')
    else:
        assert panel.get('hidden') is None
    assert panel.xpath('.//*[@data-ppt-page="list"]') or panel.get('data-ppt-page') == 'list'
    for marker in ('ppt-upload-form', 'ppt-file', 'ppt-upload-fields', 'ppt-upload-progress',
                   'ppt-upload-status', 'ppt-list-rows', 'ppt-list-conflict', 'ppt-list-more',
                   'ppt-new-template', 'ppt-source-dialog', 'ppt-source-search', 'ppt-source-rows',
                   'ppt-source-prev', 'ppt-source-next', 'ppt-source-create', 'ppt-local-upload'):
        assert panel.xpath('.//*[@id="' + marker + '"]')
    dialog = html.get_element_by_id('ppt-source-dialog')
    assert dialog.tag == 'dialog' and 'open' not in dialog.attrib
    assert '已授权来源知识库' in dialog.text_content()
    assert all(label in dialog.text_content() for label in ('文件名', '上传时间', '大小', '页数', '上传本地文件'))
    assert 'disabled' in html.get_element_by_id('ppt-upload-fields').attrib
    assert '.pptx' in html.get_element_by_id('ppt-file').get('accept')
    assert '最近导出' in panel.text_content()
    assert config(response)['date'] == '2030-01-02'
    assert config(response)['enabled'] and config(response)['can_write']
    assert len(html.xpath('//script[contains(@src,"/static/ppt.js")]')) == 1
    assert not html.xpath('//nav[@aria-label="商品研究导航"]//a[contains(@href,"/sn/ppt")]')
    assert html.get_element_by_id('report-period-switch') is not None
    assert '尚无' in response.text, '暂无报告时仍可上传自己的版式'
    assert not sessions


@pytest.mark.parametrize('headers', [{'restricted': True}, {'version': 1}, {'local': True}])
def test_report_does_not_render_ppt_region_or_assets_without_an_available_personal_scope(pages, headers):
    get, sessions = pages
    response = get('/fixture-reports', **headers)
    assert response.status_code == 200
    html = fromstring(response.text)
    assert not html.xpath('//*[@id="report-ppt" or @id="ppt-upload-form" or @id="ppt-page-config"]')
    assert not html.xpath('//script[contains(@src,"/static/ppt.js")]')
    assert not sessions


@pytest.mark.parametrize('period', ['daily', 'weekly'])
def test_real_report_route_uses_selected_period_date_for_embedded_ppt(session, monkeypatch, period):
    from tin.config import settings
    from tin.web import report_period

    reads = []

    @contextmanager
    def local_report_session(module, **kwargs):
        reads.append((module, kwargs))
        yield session

    def snapshot(session, variety, day, kind):
        assert day.isoformat() == '2030-01-02' and kind == period
        return {'as_of': day.isoformat(), 'observations': [], 'judgment': None,
                'signals': [], 'derived': [], 'gaps': [], 'surveys': []}

    # Exercise the production route and templates while keeping source projection
    # in process; date selection, period selection and shell rendering stay real.
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://offline.fixture')
    monkeypatch.setattr(web, '_page_session', local_report_session)
    monkeypatch.setattr(report_period, 'build_period', snapshot)
    identity = Identity('fixture', 'fixture@test.invalid', '测试', 1, 'owner')
    scope = WorkbenchScope(identity, 'fixture-workbench', '', 0,
                           {'name': '合成研究台', 'can_edit': True}, scope_version=2)
    a = current_identity.set(identity)
    b = current_workbench.set(scope)
    try:
        endpoint = next(route.endpoint for route in endpoints(web.app.routes)
                        if getattr(route, 'path', None) == '/sn/reports')
        request = Request({'type': 'http', 'method': 'GET', 'scheme': 'http', 'path': '/sn/reports',
                           'query_string': ('period=' + period + '&date=2030-01-02').encode(),
                           'headers': [(b'host', b'fixture.invalid')], 'server': ('fixture.invalid', 80)})
        response = endpoint(request, date='2030-01-02', period=period)
    finally:
        current_workbench.reset(b)
        current_identity.reset(a)
    assert response.status_code == 200
    html = fromstring(response.body)
    embedded = json.loads(html.get_element_by_id('ppt-page-config').text)
    assert embedded['date'] == '2030-01-02'
    assert html.get_element_by_id('report-ppt') is not None
    assert html.get_element_by_id('report-readable') is not None, 'PPT 区域保留原报告正文'
    assert reads == [('reports', {'day': '2030-01-02', 'points_per_series': 8 if period == 'weekly' else 2})]
