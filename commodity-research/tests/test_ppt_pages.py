"""PPT HTML permissions, navigation and registration without running a service."""
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
from tin.web.ppt_pages import build_router


def endpoints(routes):
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


@pytest.mark.parametrize('path,marker,identifier', [('/sn/ppt', 'ppt-upload-fields', None),
    ('/sn/ppt/template-1', 'ppt-save', 'template-1'),
    ('/sn/ppt/template-1/export', 'ppt-export-fields', 'template-1')])
def test_shell_uses_only_bounded_metadata_projection(pages, path, marker, identifier):
    get, sessions = pages
    response = get(path)
    assert response.status_code == 200, response.text
    html = fromstring(response.text)
    assert html.get_element_by_id(marker) is not None
    assert 'disabled' in html.get_element_by_id(marker).attrib
    assert config(response) == {'enabled': True, 'can_write': True, 'id': identifier, 'write_reason': ''}
    assert sessions == [('indicators', {'series_ids': [], 'points_per_series': 1})]
    assert html.xpath('//nav//a[@href="/workbenches/fixture-workbench/sn/ppt" and @aria-current="page"]')
    assert 'PPT 模板' in response.text
    if path.endswith('/export'):
        assert html.xpath('//a[@href="/workbenches/fixture-workbench/sn/ppt/template-1"]')
    if marker == 'ppt-save':
        assert html.get_element_by_id('ppt-export-link').get('href') == '/workbenches/fixture-workbench/sn/ppt/template-1/export'


@pytest.mark.parametrize('path', ['/sn/ppt', '/sn/ppt/template-1', '/sn/ppt/template-1/export'])
@pytest.mark.parametrize('headers', [{'role': 'viewer'}, {'edit': False}])
def test_readonly_shell_explains_disabled_writes(pages, path, headers):
    get, _ = pages
    response = get(path, **headers)
    assert response.status_code == 200
    assert config(response)['enabled'] and not config(response)['can_write']
    html = fromstring(response.text)
    assert '没有研究工作台编辑权限' in html.get_element_by_id('ppt-write-reason').text_content()
    assert all('disabled' in node.attrib for node in html.xpath('//fieldset'))


@pytest.mark.parametrize('path', ['/sn/ppt', '/sn/ppt/template-1', '/sn/ppt/template-1/export'])
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
    response = get('/sn/ppt/' + identifier.replace('/', ''))
    assert response.status_code == 200
    html = fromstring(response.text)
    assert config(response)['id'] == identifier.replace('/', '')
    assert not html.xpath('//script[contains(text(),"alert(1)") and not(@type="application/json")]')
    assert '\\u003cscript\\u003e' in response.text


def test_ppt_pages_are_registered_in_real_app():
    paths = {route.path for route in endpoints(web.app.routes) if getattr(route, 'endpoint', None)
             and route.endpoint.__module__ == 'tin.web.ppt_pages'}
    assert paths == {'/sn/ppt', '/sn/ppt/{template_id}', '/sn/ppt/{template_id}/export'}
