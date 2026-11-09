"""Source-status metadata is optional and never broadens source access."""
from copy import deepcopy
from dataclasses import replace

import httpx
from lxml.html import fromstring
import pytest

from tin.integration.source_status import source_status_hint
from tin.integration.workbench_scope import WorkbenchScope, current_workbench
from tests.test_workbench_sources_v2 import IDENTITY, WB, source


@pytest.fixture
def status_scope():
    first, second = source(1), source(2)
    first['knowledge'] = [{'id': 'original-1', 'parse_status': 'completed'}]
    second['knowledge'] = [{'id': 'original-2', 'parse_status': 'completed'}]
    return WorkbenchScope(IDENTITY, WB, first['knowledge_base_id'], 17,
                          {'scope_version': 2}, 'Bearer fixture', scope_version=2,
                          sources_revision=7, sources=(first, second))


@pytest.fixture
def status_document():
    return {'sources': [
        {'knowledge_id': 'original-1', 'source_file': 'One workbook',
         'sheets': [{'name': 'Checked', 'status': 'confirmed', 'field_count': 2},
                    {'name': 'Automatic', 'status': 'auto_ready', 'field_count': 7},
                    {'name': 'Review', 'status': 'needs_review', 'field_count': 3}],
         'summary': {'confirmed': 1, 'pending': 2, 'pending_fields': 10, 'pending_without_fields': 0}},
        {'knowledge_id': 'original-2', 'source_file': 'Another workbook',
         'sheets': [{'name': 'Untitled', 'status': 'template_ready', 'field_count': 4}],
         'summary': {'confirmed': 0, 'pending': 1, 'pending_fields': 4, 'pending_without_fields': 0}}],
        'summary': {'confirmed': 1, 'pending': 3, 'pending_fields': 14, 'pending_without_fields': 0}}


def response(document, code=200):
    return httpx.Response(code, json=document,
                          request=httpx.Request('POST', 'http://fixture.invalid/api/research/source-status'))


def test_reads_current_scope_using_empty_signed_service_contract_and_real_review_links(
        monkeypatch, status_scope, status_document):
    calls = []

    def transport(method, path, body, **options):
        assert current_workbench.get() is status_scope
        assert current_workbench.get().sources_revision == 7
        assert options == {'return_error_response': True, 'timeout': 2}
        calls.append((method, path, body))
        return response(status_document)

    monkeypatch.setattr('tin.provenance.bridge.structured_request', transport)
    previous = current_workbench.get()
    hint = source_status_hint(status_scope)
    assert current_workbench.get() is previous
    assert calls == [('POST', '/api/research/source-status', {})]
    assert hint['message'] == '知识库还有 3 张工作表待确认（含 14 个字段），确认后才能在这里使用'
    assert hint['pending'] == 3 and hint['pending_fields'] == 14
    assert [link['url'] for link in hint['links']] == [
        f"/platform/knowledge-bases/{item['knowledge_base_id']}/structured-data?mode=import&source_knowledge_id=original-{number}"
        for number, item in enumerate(status_scope.sources, start=1)]


@pytest.mark.parametrize('code', [401, 403, 409, 500, 503])
def test_http_failures_are_optional_without_stale_hints(monkeypatch, status_scope, code):
    monkeypatch.setattr('tin.provenance.bridge.structured_request',
                        lambda *args, **kwargs: response({'detail': 'unavailable'}, code))
    assert source_status_hint(status_scope) is None


@pytest.mark.parametrize('error', [httpx.ConnectError('offline'), PermissionError('no key'),
                                  ValueError('bad JSON'), OSError('missing signing key')])
def test_transport_and_configuration_failures_do_not_block_pages(monkeypatch, status_scope, error):
    def unavailable(*args, **kwargs):
        raise error
    monkeypatch.setattr('tin.provenance.bridge.structured_request', unavailable)
    assert source_status_hint(status_scope) is None


@pytest.mark.parametrize('problem', ['unknown_original', 'duplicate_original', 'deleted_original',
                                     'wrong_total', 'negative_fields', 'invalid_shape', 'ambiguous_kb'])
def test_invalid_or_unauthorized_metadata_cannot_produce_kb_links(
        monkeypatch, status_scope, status_document, problem):
    doc = deepcopy(status_document)
    if problem == 'unknown_original':
        doc['sources'][0]['knowledge_id'] = 'someone-elses-original'
    elif problem == 'duplicate_original':
        doc['sources'][1]['knowledge_id'] = 'original-1'
    elif problem == 'deleted_original':
        status_scope.sources[0]['knowledge'][0]['parse_status'] = 'deleted'
    elif problem == 'wrong_total':
        doc['summary']['pending'] += 1
    elif problem == 'negative_fields':
        doc['sources'][0]['sheets'][0]['field_count'] = -1
    elif problem == 'ambiguous_kb':
        status_scope.sources[1]['knowledge'].append({'id': 'original-1'})
    else:
        doc = {'sources': [], 'summary': None}
    monkeypatch.setattr('tin.provenance.bridge.structured_request', lambda *args, **kwargs: response(doc))
    assert source_status_hint(status_scope) is None


def test_restricted_legacy_and_empty_context_do_not_call_service(monkeypatch, status_scope):
    def forbidden(*args, **kwargs):
        pytest.fail('No source-status read is authorized for this context')
    monkeypatch.setattr('tin.provenance.bridge.structured_request', forbidden)
    token = current_workbench.set(None)
    try:
        assert source_status_hint() is None
        assert source_status_hint(replace(status_scope, scope_version=1)) is None
        assert source_status_hint(replace(status_scope, sources_revision=0)) is None
        private = deepcopy(status_scope.sources[0])
        private['delegated'] = True
        assert source_status_hint(replace(status_scope, sources=(private,))) is None
    finally:
        current_workbench.reset(token)


def test_zero_pending_and_drawing_sheets_do_not_invent_pending_reviews(monkeypatch, status_scope):
    # The structured service excludes drawing-only sheets from pending. Its
    # review status remains raw; the client must not turn it into another task.
    doc = {'sources': [{'knowledge_id': 'original-1', 'source_file': 'Drawings',
                       'sheets': [{'name': 'Images', 'status': 'needs_review', 'field_count': 0}],
                       'summary': {'confirmed': 0, 'pending': 0, 'pending_fields': 0, 'pending_without_fields': 0}}],
           'summary': {'confirmed': 0, 'pending': 0, 'pending_fields': 0, 'pending_without_fields': 0}}
    monkeypatch.setattr('tin.provenance.bridge.structured_request', lambda *args, **kwargs: response(doc))
    assert source_status_hint(status_scope) is None


@pytest.mark.parametrize('fields,without,message', [
    (0, 2, None),
    (3, 1, '知识库还有 1 张工作表待确认（含 3 个字段），确认后才能在这里使用')])
def test_sheets_without_fields_are_left_to_the_import_page(monkeypatch, status_scope, fields, without, message):
    sheets = [{'name': 'Notes', 'status': 'needs_review', 'field_count': 0},
              {'name': 'Other', 'status': 'needs_review', 'field_count': fields}]
    summary = {'confirmed': 0, 'pending': 2, 'pending_fields': fields, 'pending_without_fields': without}
    doc = {'sources': [{'knowledge_id': 'original-1', 'source_file': 'Workbook', 'sheets': sheets,
                        'summary': summary}], 'summary': dict(summary)}
    monkeypatch.setattr('tin.provenance.bridge.structured_request', lambda *args, **kwargs: response(doc))
    hint = source_status_hint(status_scope)
    # Field-less sheets (notes, empty tabs) are reported where the workbook is imported.
    if message is None:
        assert hint is None
    else:
        assert hint['message'] == message and hint['quiet'] is False and '没有识别出字段' not in hint['message']
        assert [link['pending'] for link in hint['links']] == [2]


@pytest.mark.parametrize('without', [2, 3])
def test_impossible_field_less_counts_are_rejected(monkeypatch, status_scope, without):
    # 2: only one open sheet has no field; 3: more field-less sheets than pending ones.
    sheets = [{'name': 'Notes', 'status': 'needs_review', 'field_count': 0},
              {'name': 'Other', 'status': 'needs_review', 'field_count': 1}]
    summary = {'confirmed': 0, 'pending': 2, 'pending_fields': 1, 'pending_without_fields': without}
    doc = {'sources': [{'knowledge_id': 'original-1', 'source_file': 'Workbook', 'sheets': sheets,
                        'summary': summary}], 'summary': dict(summary)}
    monkeypatch.setattr('tin.provenance.bridge.structured_request', lambda *args, **kwargs: response(doc))
    assert source_status_hint(status_scope) is None


@pytest.mark.parametrize('timeout', [None, 2])
def test_shared_signed_transport_keeps_default_timeout_and_allows_short_optional_reads(
        tmp_path, monkeypatch, status_scope, timeout):
    from tin.integration.identity import current_identity
    from tin.provenance import bridge

    key = tmp_path / 'service-key'
    key.write_bytes(b'isolated-fixture-signing-key-only-' * 2)
    monkeypatch.setattr(bridge.settings, 'service_token_file', key)
    monkeypatch.setattr(bridge.settings, 'structured_data_url', 'http://fixture.invalid')
    calls = []

    def transport(method, url, **options):
        calls.append((method, url, options))
        return response({'sources': [], 'summary': {'confirmed': 0, 'pending': 0, 'pending_fields': 0, 'pending_without_fields': 0}})

    monkeypatch.setattr(bridge.httpx, 'request', transport)
    token = current_identity.set(status_scope.identity)
    scope_token = current_workbench.set(status_scope)
    try:
        options = {} if timeout is None else {'timeout': timeout}
        bridge.structured_request('POST', '/api/research/source-status', {}, **options)
    finally:
        current_workbench.reset(scope_token)
        current_identity.reset(token)
    method, url, options = calls[0]
    assert method == 'POST' and url == 'http://fixture.invalid/api/research/source-status'
    assert options['timeout'] == (120 if timeout is None else timeout)
    assert options['follow_redirects'] is False and options['trust_env'] is False
    assert options['content'] == b'{}'
    assert options['headers']['Authorization'] == 'Bearer fixture'
    assert options['headers']['X-Research-Signature']


@pytest.mark.parametrize('available,restricted', [(True, False), (False, False), (True, True)])
def test_indicator_catalog_renders_optional_review_hint_only_in_direct_view(
        monkeypatch, status_scope, status_document, available, restricted):
    from starlette.requests import Request
    from tin.web import app as web

    monkeypatch.setattr('tin.provenance.bridge.structured_request',
                        lambda *args, **kwargs: response(status_document, 200 if available else 503))
    hint = source_status_hint(status_scope)
    request = Request({'type': 'http', 'method': 'GET', 'scheme': 'http',
                       'path': '/sn/indicators', 'query_string': b'',
                       'headers': [(b'host', b'fixture.invalid')], 'server': ('fixture.invalid', 80)})
    html = web.templates.env.get_template('indicators.html').render(
        request=request, nav='indicators', d=None, ticker=[], names={}, rows=[], groups=[],
        catalog_remote=False, dimensions=[], checks=[], sources={'rows': []},
        restricted_view=restricted, restricted_data={'owner_unavailable': False}, source_status_hint=hint)
    document = fromstring(html)
    hints = document.xpath('//*[@id="indicator-catalog"]//*[@data-source-status-hint]')
    assert bool(hints) is (available and not restricted)
    if hints:
        assert hint['message'] in hints[0].text_content()
        links = hints[0].xpath('.//a')
        assert [link.get('href') for link in links] == [link['url'] for link in hint['links']]
        assert all(link.get('target') == '_top' for link in links)
