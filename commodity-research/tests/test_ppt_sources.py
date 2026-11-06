"""Synthetic KB inventories exercise the same bearer and original-download ACL."""
from contextlib import contextmanager
from dataclasses import replace
import hashlib

from fastapi import HTTPException
import httpx
import pytest

from tin.config import settings
from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope
from tin.integration import ppt_sources


def binding(kb, **changes):
    return {'id': 'binding-' + kb, 'knowledge_base_id': kb, 'source_type': 'knowledge_base',
            'delegated': False, 'tenant_id': 17, 'view_modules': ['reports'], **changes}


def document(kb, identifier, filename='周报.pptx', raw=b'synthetic-ppt', **changes):
    return {'id': identifier, 'knowledge_base_id': kb, 'file_name': filename, 'file_type': 'pptx',
            'file_hash': hashlib.md5(raw).hexdigest(), 'file_size': len(raw),
            'created_at': '2026-01-02T10:00:00Z', 'parse_status': 'completed', **changes}


@pytest.fixture
def upstream(monkeypatch):
    state = {'libraries': {'first': [document('first', 'weekly')], 'second': []},
             'calls': [], 'downloads': [], 'raw': b'synthetic-ppt',
             'inventory_status': 200, 'download_status': 200, 'page_size': 2,
             'change_envelope': lambda doc: doc}
    scope = WorkbenchScope(Identity('researcher', '', 'Synthetic', 17, 'owner'),
        '70707070-7070-4070-8070-707070707070', 'first', 17,
        {'can_read_source': True, 'can_edit': True}, 'Bearer synthetic-caller',
        scope_version=2, sources_revision=1, sources=(binding('first'), binding('second')))
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://weknora.test')

    def request(method, url, **kwargs):
        state['calls'].append((method, url, kwargs))
        req = httpx.Request(method, url)
        assert kwargs['headers'] == {'Authorization': 'Bearer synthetic-caller', 'X-Tenant-ID': '17'}
        assert kwargs['trust_env'] is False and kwargs['follow_redirects'] is False
        kb = url.split('/knowledge-bases/')[1].split('/')[0]
        rows = state['libraries'][kb]
        page, size = kwargs['params']['page'], state['page_size']
        doc = {'success': True, 'data': rows[(page - 1) * size:page * size],
               'total': len(rows), 'page': page, 'page_size': size}
        return httpx.Response(state['inventory_status'], json=state['change_envelope'](doc), request=req)

    @contextmanager
    def stream(method, url, **kwargs):
        state['downloads'].append((method, url, kwargs))
        assert kwargs['headers'] == {'Authorization': 'Bearer synthetic-caller', 'X-Tenant-ID': '17'}
        assert kwargs['trust_env'] is False and kwargs['follow_redirects'] is False
        yield httpx.Response(state['download_status'], content=state['raw'], request=httpx.Request(method, url))

    monkeypatch.setattr(httpx, 'request', request)
    monkeypatch.setattr(httpx, 'stream', stream)
    return scope, state


def reference():
    return {'knowledge_base_id': 'first', 'knowledge_id': 'weekly',
            'file_hash': hashlib.md5(b'synthetic-ppt').hexdigest()}


def test_lists_only_workbench_knowledge_libraries_with_search_and_global_pagination(upstream):
    scope, state = upstream
    state['libraries']['first'] += [document('first', 'readme', 'notes.md', file_type='md'),
        document('first', 'old', parse_status='deleting'),
        document('first', 'weekly-2', '第二期周报.PPTX', metadata={'page_count': 15})]
    state['libraries']['second'] = [document('second', 'monthly', '月报.pptx'),
        document('second', 'weekly-3', '第三期周报.pptx')]
    scope = replace(scope, sources=(*scope.sources, binding('dataset', source_type='central_dataset')))
    result = ppt_sources.list_ppt_sources(scope, '周报', offset=1, limit=1)
    assert result['total'] == 3 and len(result['items']) == 1 and result['next_offset'] == 2
    assert result['items'][0]['filename'] == '第二期周报.PPTX'
    assert result['items'][0]['slide_count'] == 15
    assert result['items'][0]['size'] == len(state['raw'])
    assert len(state['calls']) == 3  # Two pages in first, one in second; no other tenant libraries.
    assert not state['downloads']


@pytest.mark.parametrize('changes,status', [
    ({'authorization': ''}, 401),
    ({'scope_version': 1}, 409),
    ({'sources': (binding('first', delegated=True),)}, 403),
])
def test_requires_fresh_direct_workbench_scope_before_inventory(upstream, changes, status):
    scope, state = upstream
    with pytest.raises(HTTPException) as error:
        ppt_sources.list_ppt_sources(replace(scope, **changes))
    assert error.value.status_code == status
    assert not state['calls']


def test_unbound_or_dataset_library_cannot_be_downloaded_even_with_known_id(upstream):
    scope, state = upstream
    for kb in ('other', 'dataset'):
        scope = replace(scope, sources=(*scope.sources, binding('dataset', source_type='central_dataset')))
        with pytest.raises(HTTPException) as error:
            ppt_sources.read_ppt_source(scope, {**reference(), 'knowledge_base_id': kb})
        assert error.value.status_code == 403
    assert not state['calls'] and not state['downloads']


@pytest.mark.parametrize('status', [401, 403, 404, 500])
def test_native_inventory_acl_and_service_failure_are_not_empty_lists(upstream, status):
    scope, state = upstream
    state['inventory_status'] = status
    with pytest.raises(HTTPException) as error:
        ppt_sources.list_ppt_sources(scope)
    assert error.value.status_code == (503 if status == 500 else status)


@pytest.mark.parametrize('change', [
    lambda doc: {**doc, 'data': [{**doc['data'][0], 'knowledge_base_id': 'another'}]},
    lambda doc: {**doc, 'total': 5},
    lambda doc: {**doc, 'page': 7},
    lambda doc: {**doc, 'success': False},
])
def test_inconsistent_or_cross_library_inventory_is_rejected(upstream, change):
    scope, state = upstream
    state['change_envelope'] = change
    with pytest.raises(HTTPException) as error:
        ppt_sources.list_ppt_sources(scope)
    assert error.value.status_code == 503


def test_selected_reference_checks_current_hash_and_downloads_with_same_identity(upstream):
    scope, state = upstream
    source = ppt_sources.get_ppt_source(scope, 'first', 'weekly')
    assert source['knowledge_base_id'] == 'first' and source['knowledge_id'] == 'weekly'
    assert source['file_hash'] == reference()['file_hash']
    assert ppt_sources.read_ppt_source(scope, source) == state['raw']
    assert state['downloads'][0][1] == 'http://weknora.test/api/v1/knowledge/weekly/download'


@pytest.mark.parametrize('mode,expected', [('changed', 'changed'), ('deleted', 'deleted'), ('retyped', 'changed')])
def test_changed_and_deleted_reference_states_block_before_download(upstream, mode, expected):
    scope, state = upstream
    if mode == 'deleted':
        state['libraries']['first'] = []
    elif mode == 'retyped':
        state['libraries']['first'][0]['file_type'] = 'pdf'
    else:
        state['libraries']['first'][0]['file_hash'] = 'b' * 32
    result = ppt_sources.source_state(scope, reference())
    assert result['status'] == expected
    assert '已变化' in result['message'] if expected == 'changed' else '已删除' in result['message']
    with pytest.raises(HTTPException) as error:
        ppt_sources.read_ppt_source(scope, reference())
    assert error.value.status_code == 409
    assert error.value.detail['code'] == 'source_' + expected
    assert not state['downloads']


@pytest.mark.parametrize('algorithm', ['md5', 'sha256'])
def test_original_bytes_must_match_registered_hash_even_when_inventory_unchanged(upstream, algorithm):
    scope, state = upstream
    digest = getattr(hashlib, algorithm)(state['raw']).hexdigest()
    state['libraries']['first'][0]['file_hash'] = digest
    source = {**reference(), 'file_hash': digest}
    assert ppt_sources.read_ppt_source(scope, source) == state['raw']
    state['raw'] += b'replaced'
    with pytest.raises(HTTPException) as error:
        ppt_sources.read_ppt_source(scope, source)
    assert error.value.status_code == 409 and error.value.detail['code'] == 'source_changed'


def test_batch_states_reuse_only_request_inventory(upstream):
    scope, state = upstream
    inventory = ppt_sources.source_inventory(scope)
    calls = len(state['calls'])
    assert ppt_sources.source_state(scope, reference(), inventory=inventory)['status'] == 'ready'
    assert ppt_sources.source_state(scope, {**reference(), 'knowledge_id': 'absent'}, inventory=inventory)['status'] == 'deleted'
    assert len(state['calls']) == calls
    # A request-local inventory cannot authorize a library removed from the current grant.
    revoked = replace(scope, sources=())
    assert ppt_sources.source_state(revoked, reference(), inventory=inventory)['status'] == 'forbidden'


@pytest.mark.parametrize('status,expected', [(403, 403), (401, 401), (404, 409), (500, 503), (302, 503)])
def test_download_reuses_upstream_original_acl_and_never_follows_redirects(upstream, status, expected):
    scope, state = upstream
    state['download_status'] = status
    with pytest.raises(HTTPException) as error:
        ppt_sources.read_ppt_source(scope, reference())
    assert error.value.status_code == expected


def test_download_size_is_bounded_even_when_inventory_is_stale(upstream, monkeypatch):
    scope, state = upstream
    monkeypatch.setattr(ppt_sources, 'MAX_UPLOAD', 4)
    state['libraries']['first'][0]['file_size'] = 1
    with pytest.raises(HTTPException) as error:
        ppt_sources.read_ppt_source(scope, reference())
    assert error.value.status_code == 413


def test_missing_hash_fails_explicitly_instead_of_showing_partial_catalog(upstream):
    scope, state = upstream
    state['libraries']['first'][0]['file_hash'] = ''
    with pytest.raises(HTTPException) as error:
        ppt_sources.list_ppt_sources(scope)
    assert error.value.status_code == 503 and '文件哈希' in error.value.detail


def test_download_transport_failure_is_unavailable_and_keeps_reference(upstream, monkeypatch):
    scope, state = upstream
    source = reference()

    @contextmanager
    def failure(*args, **kwargs):
        raise httpx.ConnectError('synthetic transport failure')
        yield  # pragma: no cover - preserve context-manager protocol

    monkeypatch.setattr(httpx, 'stream', failure)
    with pytest.raises(HTTPException) as error:
        ppt_sources.read_ppt_source(scope, source)
    assert error.value.status_code == 503
    assert source == reference()
