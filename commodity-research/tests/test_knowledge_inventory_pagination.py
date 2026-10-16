"""Fresh native inventory is complete across document pages, never a partial fact scope."""
from types import SimpleNamespace

import httpx
import pytest
from fastapi import HTTPException

from tin.config import settings
from tin.integration.kb_facts import current_sources

KB = '20000000-0000-4000-8000-000000000099'


def documents(count=201, structured=1):
    rows = [{'id': f'report-{i}', 'knowledge_base_id': KB, 'file_type': 'md',
             'file_hash': 'e' * 32} for i in range(count)]
    rows += [{'id': f'original-{i}', 'knowledge_base_id': KB, 'file_type': 'xlsx',
              'file_hash': 'b' * 32, 'parse_status': 'stored', 'enable_status': 'disabled'}
             for i in range(structured)]
    return rows


@pytest.fixture
def native(monkeypatch):
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://native.test')
    scope = SimpleNamespace(authorization='Bearer synthetic-user', knowledge_base_id=KB,
                            identity=SimpleNamespace(tenant_id=17))
    calls = []

    def install(rows, *, cap=200, alter=None, response=None):
        def endpoint(request):
            assert request.url.path == f'/api/v1/knowledge-bases/{KB}/knowledge'
            assert request.headers['authorization'] == 'Bearer synthetic-user'
            assert request.headers['x-tenant-id'] == '17'
            page = int(request.url.params['page'])
            requested = int(request.url.params['page_size'])
            calls.append((page, requested))
            if response:
                result = response(request, page)
                if result is not None:
                    return result
            size = min(requested, cap)
            doc = {'success': True, 'data': [dict(row) for row in rows[(page - 1) * size:page * size]],
                   'total': len(rows), 'page': page, 'page_size': size}
            if alter:
                alter(doc, page)
            return httpx.Response(200, json=doc)

        transport = httpx.MockTransport(endpoint)

        def request(method, url, **kwargs):
            assert kwargs.pop('trust_env') is False
            assert kwargs['follow_redirects'] is False
            with httpx.Client(transport=transport, trust_env=False) as client:
                return client.request(method, url, **kwargs)

        monkeypatch.setattr(httpx, 'request', request)

    return scope, calls, install


def test_201_reports_and_one_excel_are_read_completely(native):
    scope, calls, install = native
    install(documents())
    assert current_sources(scope) == [{'knowledge_id': 'original-0', 'file_hash': 'b' * 32}]
    assert calls == [(1, 200), (2, 200)]


def test_native_actual_page_size_is_used_for_following_pages(native):
    scope, calls, install = native
    install(documents(), cap=100)
    assert len(current_sources(scope)) == 1
    assert calls == [(1, 200), (2, 100), (3, 100)]


def test_128_structured_originals_are_allowed_despite_reports(native):
    scope, _, install = native
    install(documents(200, 128))
    assert len(current_sources(scope)) == 128


def test_129_structured_originals_still_fail_closed(native):
    scope, _, install = native
    install(documents(200, 129))
    with pytest.raises(HTTPException, match='128') as error:
        current_sources(scope)
    assert error.value.status_code == 503


def test_empty_inventory_is_valid(native):
    scope, calls, install = native
    install([])
    assert current_sources(scope) == []
    assert calls == [(1, 200)]


def test_single_complete_legacy_envelope_without_page_fields(native):
    scope, _, install = native
    def alter(doc, _):
        doc.pop('page'); doc.pop('page_size')
    install(documents(0), alter=alter)
    assert len(current_sources(scope)) == 1


@pytest.mark.parametrize('field,value', [('page', 2), ('page', True), ('page_size', 0),
    ('page_size', True), ('page_size', 201), ('total', -1), ('total', True), ('success', False)])
def test_invalid_first_page_metadata_fails(native, field, value):
    scope, _, install = native
    install(documents(), alter=lambda doc, _: doc.update({field: value}))
    with pytest.raises(HTTPException) as error:
        current_sources(scope)
    assert error.value.status_code == 503


def test_inventory_over_10000_has_explicit_limit_error(native):
    scope, _, install = native
    install(documents(), alter=lambda doc, _: doc.update(total=10001))
    with pytest.raises(HTTPException, match='10000') as error:
        current_sources(scope)
    assert error.value.status_code == 503


@pytest.mark.parametrize('mutation', ['total_deleted', 'total_deleted_shortened', 'total_added', 'page', 'page_size',
                                     'duplicate', 'foreign_scope', 'invalid_hash', 'empty', 'short'])
def test_later_page_must_preserve_inventory_and_scope(native, mutation):
    scope, _, install = native
    rows = documents()
    def alter(doc, page):
        if page != 2:
            return
        if mutation == 'total_deleted': doc['total'] -= 1
        elif mutation == 'total_deleted_shortened':
            doc['total'] -= 1; doc['data'].pop()
        elif mutation == 'total_added': doc['total'] += 1
        elif mutation == 'page': doc['page'] = 1
        elif mutation == 'page_size': doc['page_size'] = 199
        elif mutation == 'duplicate': doc['data'][0]['id'] = rows[0]['id']
        elif mutation == 'foreign_scope': doc['data'][0]['knowledge_base_id'] = 'another-kb'
        elif mutation == 'invalid_hash': doc['data'][-1]['file_hash'] = 'invalid'
        elif mutation == 'empty': doc['data'] = []
        elif mutation == 'short': doc['data'].pop()
    install(rows, alter=alter)
    with pytest.raises(HTTPException) as error:
        current_sources(scope)
    assert error.value.status_code == 503


def test_short_nonfinal_page_is_not_silently_skipped(native):
    scope, _, install = native
    install(documents(), alter=lambda doc, page: doc.update(data=doc['data'][:100]) if page == 1 else None)
    with pytest.raises(HTTPException):
        current_sources(scope)


@pytest.mark.parametrize('field', ['page', 'page_size'])
def test_partial_envelope_requires_native_pagination_metadata(native, field):
    scope, _, install = native
    install(documents(), alter=lambda doc, _: doc.pop(field))
    with pytest.raises(HTTPException):
        current_sources(scope)


@pytest.mark.parametrize('status', [401, 403, 404, 500])
def test_later_permission_or_upstream_failure_never_returns_first_page(native, status):
    scope, _, install = native
    rows = documents(201, 0); rows[0].update(file_type='xlsx', file_hash='b' * 32)
    install(rows, response=lambda _, page: httpx.Response(status) if page == 2 else None)
    with pytest.raises(HTTPException) as error:
        current_sources(scope)
    assert error.value.status_code == (status if status in {401, 403, 404} else 503)


@pytest.mark.parametrize('kind', ['timeout', 'invalid_json'])
def test_later_transport_failure_is_fail_closed(native, kind):
    scope, _, install = native
    def response(request, page):
        if page == 2:
            if kind == 'timeout':
                raise httpx.ReadTimeout('synthetic failure', request=request)
            return httpx.Response(200, text='not-json')
    install(documents(), response=response)
    with pytest.raises(HTTPException) as error:
        current_sources(scope)
    assert error.value.status_code == 503


def test_explicit_file_type_and_current_hash_decide_eligibility(native):
    scope, _, install = native
    rows = documents(1, 1)
    rows[0]['file_name'] = 'misleading.xlsx'
    rows[1].update(file_type='.XLSX', file_hash='B' * 32)
    rows += [{**rows[1], 'id': 'deleting', 'parse_status': 'deleting'},
             {**rows[1], 'id': 'deleted', 'deleted_at': '2026-01-01'},
             {**rows[1], 'id': 'no-fingerprint', 'file_hash': ''}]
    install(rows)
    assert current_sources(scope) == [{'knowledge_id': 'original-0', 'file_hash': 'b' * 32}]
