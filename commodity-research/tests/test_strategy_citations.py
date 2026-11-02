"""Explicit prose references use synthetic authorized metadata, never models or facts."""
import asyncio
from contextlib import contextmanager
from copy import deepcopy
from dataclasses import replace
import hashlib
import json

from fastapi import FastAPI, HTTPException
from fastapi.testclient import TestClient
import httpx
import pytest

from tin.config import settings
from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope, current_workbench
from tin.web import strategy_citations as citations
from tin.web import research_strategy
from tin.web import research_references

WB = '10000000-0000-4000-8000-000000000731'
SID = 'kb-' + 'a' * 48
FIELD_VERSION = 'field-version'
CATALOG_VERSION = 'c' * 64


@pytest.fixture
def case(monkeypatch):
    identity = Identity('synthetic-owner', 'owner@synthetic.invalid', 'Owner', 73, 'owner')
    sources = tuple({'id': binding, 'source_type': 'knowledge_base', 'source_id': kb,
        'knowledge_base_id': kb, 'tenant_id': 73, 'permission': 'editor', 'delegated': False,
        'view_modules': ['strategies', 'judgments', 'indicators']} for binding, kb in (
            ('binding-a', 'library-a'), ('binding-default', 'library-default')))
    scope = WorkbenchScope(identity, WB, 'library-a', 73,
        {'can_edit': True, 'can_read_source': True, 'workspace_mode': 'isolated'},
        'Bearer synthetic-source-access', scope_version=2, sources_revision=3, sources=sources)
    state = {'metadata_reads': [], 'catalog_reads': [], 'downloads': [], 'rule_reads': [],
        'fields': [{'series_id': SID, 'metric': 'Synthetic quantity', 'unit': 'kg', 'frequency': 'weekly',
            'source_file': 'source.xlsx', 'knowledge_base_id': 'library-default',
            'source_binding_id': 'binding-default', 'source_version': FIELD_VERSION}],
        'files': {}, 'raw': {}, 'after_download': None, 'chunks': {}, 'chunk_reads': [], 'chunk_hook': None}
    def add_file(identifier='file-a', kb='library-a', raw=b'Synthetic source text', kind='md'):
        state['raw'][identifier] = raw
        state['files'][identifier] = {'id': identifier, 'knowledge_base_id': kb,
            'file_name': identifier + '.' + kind, 'title': 'Synthetic file ' + identifier,
            'file_type': kind, 'file_size': len(raw), 'file_hash': hashlib.sha256(raw).hexdigest(),
            'updated_at': '2030-01-02T03:04:05Z', 'type': 'file', 'parse_status': 'completed'}
        return state['files'][identifier]
    add_file(); add_file('default-file', 'library-default', b'{"original":"source"}', 'json')
    state['add_file'] = add_file
    async def go(bound, path, *, params=None):
        state['metadata_reads'].append((bound.knowledge_base_id, path, deepcopy(params)))
        if path.startswith('/api/v1/chunks/'):
            identifier = path.rsplit('/', 1)[-1]
            page, size = params['page'], params['page_size']
            rows = state['chunks'].get(identifier, [])
            state['chunk_reads'].append((bound.knowledge_base_id, identifier, page, size))
            result = {'success': True, 'data': deepcopy(rows[(page - 1) * size:page * size]),
                'total': len(rows), 'page': page, 'page_size': size}
            if state['chunk_hook']: state['chunk_hook'](identifier, page, result)
            return result
        if path.endswith('/knowledge'):
            rows = [row for row in state['files'].values() if row['knowledge_base_id'] == bound.knowledge_base_id]
            search = (params or {}).get('keyword', '').casefold()
            rows = [row for row in rows if search in (row['file_name'] + ' ' + row['title']).casefold()]
            page, size = (params or {}).get('page', 1), (params or {}).get('page_size', 100)
            return {'data': deepcopy(rows[(page - 1) * size:page * size]), 'total': len(rows)}
        identifier = path.rsplit('/', 1)[-1]
        row = state['files'].get(identifier)
        if row is None:
            raise HTTPException(404, 'Synthetic missing source')
        return {'data': deepcopy(row)}
    def catalog(bound, *, offset=0, limit=200, search='', series_ids=None):
        state['catalog_reads'].append((bound.workbench_id, offset, limit, search, series_ids))
        rows = [row for row in state['fields'] if (series_ids is None or row['series_id'] in series_ids)
            and search.casefold() in row['metric'].casefold()]
        return {'series': deepcopy(rows[offset:offset + limit]), 'total': len(rows), 'version': CATALOG_VERSION,
            'next_offset': offset + limit if offset + limit < len(rows) else None}
    async def thread(function, *args, **kwargs): return function(*args, **kwargs)
    @contextmanager
    def session(_): yield object()
    condition = {'id': 'rule-a', 'name': 'Synthetic boundary', 'series_id': SID,
        'operator': '>=', 'value': 12345.67, 'type': '观察位', 'horizon': '短期'}
    parent = {'kind': 'judgment', 'id': '17', 'revision': 4, 'title': 'Synthetic judgment',
        'written_at': '2030-01-01', 'payload': {'thresholds': [condition], 'falsifiers': [
            {'id': 'rule-f', 'desc': 'Synthetic accumulation', 'series_id': SID,
             'expression': 'change > 0', 'unit_confirmed': 'kg', 'consecutive_periods': 2}]}}
    def resolve(session, current, kind, identifier, revision=None):
        state['rule_reads'].append((current.identity.user_id, current.workbench_id, identifier, revision))
        if kind != 'judgment' or identifier != '17' or revision != 4:
            raise HTTPException(404, '研判不存在或不属于当前工作台')
        return deepcopy(parent)
    def condition_resolve(session, current, reference):
        actual = resolve(session, current, 'judgment', reference['id'], reference['revision'])
        field = 'thresholds' if reference['rule_kind'] == 'threshold' else 'falsifiers'
        found = [row for row in actual['payload'][field] if row['id'] == reference['rule_id']]
        if len(found) != 1: raise HTTPException(404, '所选研判条件不存在')
        return {**actual, 'condition': found[0], 'rule_kind': reference['rule_kind'], 'rule_id': reference['rule_id']}
    monkeypatch.setattr(citations, 'go_request', go)
    monkeypatch.setattr(citations, 'catalog_page', catalog)
    monkeypatch.setattr(citations, 'artifact_session', session)
    monkeypatch.setattr(citations, 'run_in_threadpool', thread)
    monkeypatch.setattr(research_references, 'resolve_reference', resolve)
    monkeypatch.setattr(research_references, 'resolve_judgment_condition', condition_resolve)
    monkeypatch.setattr(research_references, 'list_references', lambda *args, **kwargs:
        {'items': [{key: parent[key] for key in ('id', 'revision', 'title', 'written_at')}], 'total': 1})
    original = httpx.AsyncClient
    def download(request):
        identifier = request.url.path.split('/')[-2]
        state['downloads'].append((str(request.url), dict(request.headers)))
        response = httpx.Response(200, content=state['raw'][identifier])
        if state['after_download']: state['after_download'](identifier)
        return response
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://synthetic.invalid')
    monkeypatch.setattr(citations.httpx, 'AsyncClient', lambda *args, **kwargs:
        original(*args, **{**kwargs, 'transport': httpx.MockTransport(download)}))
    yield scope, state


def indicator(**changes):
    return {'kind': 'indicator', 'field': 'trigger', 'series_id': SID,
        'source_version': FIELD_VERSION, 'source_binding_id': 'binding-default', **changes}


def condition(**changes):
    return {'kind': 'judgment_condition', 'field': 'invalidation', 'id': '17',
        'revision': 4, 'rule_kind': 'threshold', 'rule_id': 'rule-a', **changes}


def file_ref(case, identifier='file-a', **changes):
    _, state = case
    row = state['files'][identifier]
    return {'kind': 'knowledge_file', 'field': 'original_text', 'knowledge_id': identifier,
        'source_binding_id': 'binding-a' if row['knowledge_base_id'] == 'library-a' else 'binding-default',
        'file_version': citations.file_version(row), **changes}


def run(case, operation):
    marker = current_workbench.set(case[0])
    try: return asyncio.run(operation())
    finally: current_workbench.reset(marker)


def options(case, kind, **changes):
    arguments = {'kind': kind, 'search': '', 'offset': 0, 'limit': 30, 'judgment_id': None, 'revision': None, **changes}
    return run(case, lambda: citations.citation_options(**arguments))


def test_indicator_options_use_search_pages_and_actual_secondary_source_version(case):
    scope, state = case
    result = options(case, 'indicator', search='quantity')
    assert result['total'] == 1 and result['has_more'] is False
    assert result['items'][0]['citation'] == indicator(field='thesis')
    assert state['catalog_reads'] == [(scope.workbench_id, 0, 30, 'quantity', None)]
    assert state['metadata_reads'] == [] and state['downloads'] == []


def test_indicator_preview_rebuilds_snapshot_from_authoritative_metadata(case):
    scope, state = case
    value = indicator(snapshot={'title': 'Forged', 'preview': {'url': 'https://private.invalid'}})
    normalized = run(case, lambda: citations.resolve_citations(scope, [value]))[0]
    assert normalized['snapshot']['title'] == 'Synthetic quantity'
    assert normalized['snapshot']['preview']['type'] == 'indicator'
    assert 'Forged' not in json.dumps(normalized) and 'private.invalid' not in json.dumps(normalized)
    assert state['catalog_reads'][0][-1] == [SID]
    assert state['downloads'] == []


@pytest.mark.parametrize('change,status', [
    ({'series_id': 'missing'}, 404), ({'source_version': 'old'}, 409),
    ({'source_binding_id': 'binding-a'}, 403), ({'source_binding_id': 'foreign'}, 403),
])
def test_indicator_identity_version_and_binding_are_not_client_claims(case, change, status):
    with pytest.raises(HTTPException) as error:
        run(case, lambda: citations.resolve_citations(case[0], [indicator(**change)]))
    assert error.value.status_code == status


def test_condition_preview_retains_exact_rule_and_version_without_reading_observations(case):
    scope, state = case
    row = run(case, lambda: citations.resolve_citations(scope, [condition()]))[0]
    preview = row['snapshot']['preview']
    assert preview['type'] == 'condition' and preview['text'] == '指标数值不低于 12345.67'
    assert preview['origin_url'].endswith('judgment_id=17')
    assert state['rule_reads'] == [('synthetic-owner', WB, '17', 4)]
    assert state['catalog_reads'] == [] and state['downloads'] == []


def test_condition_directory_supports_threshold_falsifier_and_literal_search(case):
    result = options(case, 'judgment_condition', search='accumulation')
    assert result['total'] == 1 and result['items'][0]['citation']['rule_kind'] == 'falsifier'
    assert '连续 2 期' in result['items'][0]['subtitle']
    selected = result['items'][0]['citation']
    row = run(case, lambda: citations.resolve_citations(case[0], [selected]))[0]
    assert row['snapshot']['preview']['text'] == '较上一期变化高于 0 kg，连续 2 期'


@pytest.mark.parametrize('changes', [{'id': '18'}, {'revision': 5}, {'rule_id': 'missing'}, {'rule_kind': 'falsifier'}])
def test_unknown_or_wrong_revision_condition_is_not_replaced_with_another_rule(case, changes):
    with pytest.raises(HTTPException) as error:
        run(case, lambda: citations.resolve_citations(case[0], [condition(**changes)]))
    assert error.value.status_code == 404


def test_file_options_include_primary_and_default_but_fetch_no_file_bytes(case):
    result = options(case, 'knowledge_file')
    assert result['total'] == 2 and result['has_more'] is False
    assert {row['citation']['source_binding_id'] for row in result['items']} == {'binding-a', 'binding-default'}
    assert case[1]['downloads'] == []


def test_file_directory_pages_native_metadata_and_advances_past_deleting_rows(case):
    scope, state = case
    state['files'].clear()
    for index in range(120): state['add_file']('file-' + str(index))
    for index in range(10): state['add_file']('default-' + str(index), 'library-default')
    state['files']['file-111']['parse_status'] = 'deleting'
    result = options(case, 'knowledge_file', offset=110, limit=20)
    assert result['total'] == 130 and len(result['items']) == 19
    assert result['next_offset'] is None and result['has_more'] is False
    assert len(state['metadata_reads']) == 3 and state['downloads'] == []


@pytest.mark.parametrize('kind', ['xlsx', 'pdf'])
def test_large_binary_original_is_valid_as_a_reference_without_downloading(case, kind):
    scope, state = case
    source = state['add_file']('large', raw=b'not downloaded', kind=kind)
    source['file_size'] = 310 * 1024 * 1024
    row = run(case, lambda: citations.resolve_citations(scope, [file_ref(case, 'large')]))[0]
    preview = row['snapshot']['preview']
    assert preview['type'] == 'file' and preview['content_supported'] is True
    assert preview['content_kind'] == 'parsed_fragments'
    assert preview['url'].startswith('/platform/knowledge-bases/library-a?knowledge_id=large')
    assert state['downloads'] == []


@pytest.mark.parametrize('change,status', [
    ({'knowledge_id': 'missing'}, 404), ({'source_binding_id': 'binding-default'}, 404),
    ({'source_binding_id': 'foreign'}, 403), ({'file_version': 'a' * 64}, 409),
])
def test_file_save_rejects_missing_wrong_source_or_changed_version(case, change, status):
    with pytest.raises(HTTPException) as error:
        run(case, lambda: citations.resolve_citations(case[0], [file_ref(case, **change)]))
    assert error.value.status_code == status and case[1]['downloads'] == []


def test_file_preview_reports_updated_version_without_silently_changing_selected_identity(case):
    row = run(case, lambda: citations._resolve_file(case[0], file_ref(case, file_version='a' * 64), strict_version=False))
    assert row['preview']['version_status'] == 'updated'
    assert row['citation']['file_version'] == 'a' * 64
    assert row['preview']['current_version'] != row['citation']['file_version']


@pytest.mark.parametrize('raw,kind', [(b'Safe UTF-8 original', 'txt'), ('中文材料'.encode(), 'md'), (b'{"source":"value"}', 'json')])
def test_only_explicit_content_action_downloads_small_utf8_original(case, raw, kind):
    scope, state = case
    state['add_file']('content', 'library-default', raw, kind)
    ref = file_ref(case, 'content')
    result = run(case, lambda: citations.citation_file_content(**{key: ref[key] for key in (
        'source_binding_id', 'knowledge_id', 'file_version')}))
    assert result['supported'] is True and result['text'] == raw.decode()
    assert result['sha256'] == hashlib.sha256(raw).hexdigest()
    assert len(state['downloads']) == 1
    assert state['downloads'][0][1]['authorization'] == 'Bearer synthetic-source-access'
    assert state['downloads'][0][0].startswith('http://synthetic.invalid/api/v1/knowledge/content/download')


def test_binary_content_request_returns_clear_unsupported_result_without_download(case):
    case[1]['add_file']('pdf', kind='pdf')
    ref = file_ref(case, 'pdf')
    result = run(case, lambda: citations.citation_file_content(**{key: ref[key] for key in (
        'source_binding_id', 'knowledge_id', 'file_version')}))
    assert result['supported'] is False and result['text'] == ''
    assert case[1]['downloads'] == []


@pytest.mark.parametrize('mode,status', [('oversized', 413), ('invalid_utf8', 422), ('hash_mismatch', 409),
    ('size_mismatch', 409), ('changed_during_read', 409), ('old_version', 409), ('deleted', 404)])
def test_file_content_never_uses_truncated_changed_or_unverified_replacement(case, mode, status):
    scope, state = case
    source = state['files']['file-a']
    ref = file_ref(case)
    if mode == 'oversized':
        source['file_size'] = citations.MAX_CONTENT_BYTES + 1
        ref = file_ref(case)
    if mode == 'invalid_utf8':
        state['add_file']('file-a', raw=b'\xff\xfe')
        ref = file_ref(case)
    if mode == 'hash_mismatch': state['raw']['file-a'] = b'changed text'
    if mode == 'size_mismatch':
        source['file_hash'] = ''; source['file_size'] += 1; ref = file_ref(case)
    if mode == 'changed_during_read':
        state['after_download'] = lambda identifier: state['files'][identifier].update(updated_at='later')
    if mode == 'old_version': ref['file_version'] = 'a' * 64
    if mode == 'deleted': source['parse_status'] = 'deleted'
    with pytest.raises(HTTPException) as error:
        run(case, lambda: citations.citation_file_content(**{key: ref[key] for key in (
            'source_binding_id', 'knowledge_id', 'file_version')}))
    assert error.value.status_code == status
    if mode in {'oversized', 'old_version', 'deleted'}: assert state['downloads'] == []


@pytest.mark.parametrize('change', ['restricted', 'source-hidden', 'module', 'delegated'])
def test_citations_do_not_expand_workbench_or_delegated_source_permissions(case, change):
    scope, state = case
    if change == 'restricted':
        scope = replace(scope, metadata={**scope.metadata, 'can_read_source': False})
    if change == 'source-hidden':
        scope = replace(scope, metadata={**scope.metadata, 'can_read_source': False})
    if change == 'module': scope = replace(scope, view_modules=('home',))
    if change == 'delegated': scope = replace(scope, sources=({**scope.sources[0], 'delegated': True},))
    with pytest.raises(HTTPException) as error:
        asyncio.run(citations.resolve_citations(scope, [indicator()]))
    assert error.value.status_code == 403 and state['downloads'] == [] and state['catalog_reads'] == []


def valid_card(**changes):
    return {'format': 'research-strategy-card.v1', 'title': 'Synthetic strategy', 'direction': 'observe',
        'thesis': 'Research thesis', 'watch_for': 'Watch conditions', 'trigger': 'Explicit trigger',
        'invalidation': 'Explicit invalidation', 'review_date': '2030-01-02',
        'original_text': '', **changes}


def test_old_card_defaults_empty_citations_and_never_loads_sources(case):
    value = run(case, lambda: citations.normalize_card_citations(case[0], valid_card()))
    assert 'citations' not in value and case[1]['metadata_reads'] == [] and case[1]['catalog_reads'] == []


def test_server_rebuilds_all_client_snapshots_and_preserves_manual_prose(case):
    card = valid_card(citations=[indicator(snapshot={'title': 'Forged'}), condition(), file_ref(case)])
    result = run(case, lambda: citations.normalize_card_citations(case[0], card))
    assert result['thesis'] == card['thesis'] and result['trigger'] == card['trigger']
    assert all('snapshot' in row for row in result['citations'])
    assert 'Forged' not in json.dumps(result) and case[1]['downloads'] == []


@pytest.mark.parametrize('values', [[indicator()] * 33, [indicator(field='entry')],
    [condition(revision=True)], [file_ref], [{'kind': 'unknown', 'field': 'trigger'}]])
def test_schema_and_capacity_reject_malformed_references_without_model_or_source_reads(case, values):
    with pytest.raises(HTTPException) as error:
        run(case, lambda: citations.resolve_citations(case[0], values))
    assert error.value.status_code == 422
    assert case[1]['catalog_reads'] == [] and case[1]['metadata_reads'] == []


@pytest.fixture
def client(case):
    app = FastAPI()
    app.include_router(research_strategy.router)
    @app.middleware('http')
    async def context(request, call_next):
        marker = current_workbench.set(case[0])
        try: return await call_next(request)
        finally: current_workbench.reset(marker)
    with TestClient(app) as value: yield value


def test_new_static_routes_precede_card_id_route_and_return_exact_contract(client, case):
    path = '/api/sn/research-strategies'
    result = client.get(path + '/citation-options', params={'kind': 'indicator', 'limit': 30})
    assert result.status_code == 200, result.text
    item = result.json()['items'][0]
    assert set(item) == {'citation', 'title', 'subtitle'}
    preview = client.post(path + '/citation-preview', json={'citation': item['citation']})
    assert preview.status_code == 200, preview.text
    assert set(preview.json()) == {'citation', 'title', 'subtitle', 'preview'}
    assert preview.json()['citation']['snapshot']['title'] == 'Synthetic quantity'
    raw_ref = file_ref(case)
    content = client.get(path + '/citation-file-content', params={key: raw_ref[key] for key in (
        'source_binding_id', 'knowledge_id', 'file_version')})
    assert content.status_code == 200 and content.json()['supported'] is True


def test_preview_request_rejects_duplicate_json_keys_without_guessing_identity(client):
    response = client.post('/api/sn/research-strategies/citation-preview',
        content='{"citation":' + json.dumps(indicator()) + ',"citation":' + json.dumps(condition()) + '}',
        headers={'Content-Type': 'application/json'})
    assert response.status_code == 422


def parsed(case, texts, identifier='pdf'):
    _, state = case
    if identifier not in state['files']:
        state['add_file'](identifier, raw=b'not downloaded binary', kind='pdf')
    source = state['files'][identifier]
    source['file_size'] = 310 * 1024 * 1024
    state['chunks'][identifier] = [{'id': 'chunk-' + str(index), 'knowledge_id': identifier,
        'knowledge_base_id': source['knowledge_base_id'], 'tenant_id': 73, 'chunk_type': 'text',
        'status': 2, 'chunk_index': index, 'content_revision': 0,
        'updated_at': '2030-01-02T03:04:05Z', 'content': text} for index, text in enumerate(texts)]
    return file_ref(case, identifier)


def load_content(case, ref):
    return run(case, lambda: citations.citation_file_content(**{key: ref[key] for key in (
        'source_binding_id', 'knowledge_id', 'file_version')}))


def test_existing_pdf_parsed_fragments_are_bounded_read_only_and_explicit_about_overlap(case):
    ref = parsed(case, ['Alpha overlap', 'overlap beta'])
    result = load_content(case, ref)
    assert result['supported'] is True and result['content_kind'] == 'parsed_fragments'
    assert result['text'] == 'Alpha overlap\n\noverlap beta'
    assert '不等同原件全文' in result['notice']
    assert result['provenance']['chunk_count'] == 2
    assert result['provenance']['chunks'][1]['revision'] == 0
    assert len(case[1]['chunk_reads']) == 2 and case[1]['downloads'] == []


def test_parsed_fragments_require_completed_state_and_existing_data(case):
    ref = parsed(case, [])
    assert load_content(case, ref)['supported'] is False
    case[1]['files']['pdf']['parse_status'] = 'processing'
    ref = file_ref(case, 'pdf')
    case[1]['chunk_reads'].clear()
    assert load_content(case, ref)['supported'] is False
    assert case[1]['chunk_reads'] == [] and case[1]['downloads'] == []


def test_parsed_fragment_pagination_reads_and_rechecks_every_chunk_without_deduplication(case):
    ref = parsed(case, ['Repeated source text'] * 23)
    result = load_content(case, ref)
    assert result['text'].count('Repeated source text') == 23
    assert result['provenance']['chunk_count'] == 23
    assert [row[2] for row in case[1]['chunk_reads']] == [1, 2, 1, 2]
    assert case[1]['downloads'] == []


@pytest.mark.parametrize('change,status', [
    ('foreign_knowledge', 409), ('foreign_kb', 409), ('foreign_tenant', 409), ('duplicate_id', 409),
    ('bad_order', 409), ('bad_revision', 409), ('wrong_type', 409), ('bad_pagination', 409),
    ('changed_during_read', 409), ('too_many_chunks', 413), ('too_many_characters', 413),
    ('too_many_bytes', 413),
])
def test_parsed_fragments_never_silently_truncate_or_mix_sources(case, change, status):
    ref = parsed(case, ['First', 'Second'])
    rows = case[1]['chunks']['pdf']
    if change == 'foreign_knowledge': rows[0]['knowledge_id'] = 'other'
    if change == 'foreign_kb': rows[0]['knowledge_base_id'] = 'other'
    if change == 'foreign_tenant': rows[0]['tenant_id'] = 74
    if change == 'duplicate_id': rows[1]['id'] = rows[0]['id']
    if change == 'bad_order': rows[1]['chunk_index'] = rows[0]['chunk_index']
    if change == 'bad_revision': rows[0]['content_revision'] = -1
    if change == 'wrong_type': rows[0]['chunk_type'] = 'summary'
    if change == 'bad_pagination':
        case[1]['chunk_hook'] = lambda identifier, page, result: result.update(total=3)
    if change == 'changed_during_read':
        def mutate(identifier, page, result):
            case[1]['chunks'][identifier][0]['content_revision'] += 1
        case[1]['chunk_hook'] = mutate
    if change == 'too_many_chunks': ref = parsed(case, ['Chunk'] * (citations.MAX_PARSED_CHUNKS + 1))
    if change == 'too_many_characters': rows[0]['content'] = 'a' * (citations.MAX_CONTENT_CHARACTERS + 1)
    if change == 'too_many_bytes': rows[0]['content'] = '漢' * (citations.MAX_CONTENT_BYTES // 3 + 1)
    with pytest.raises(HTTPException) as error:
        load_content(case, ref)
    assert error.value.status_code == status
    assert case[1]['downloads'] == []


def test_raw_utf8_content_also_respects_strategy_character_limit(case):
    case[1]['add_file']('long', raw=b'a' * (citations.MAX_CONTENT_CHARACTERS + 1), kind='txt')
    with pytest.raises(HTTPException) as error:
        load_content(case, file_ref(case, 'long'))
    assert error.value.status_code == 413


def test_current_raw_text_is_distinguished_from_parsed_fragments(case):
    result = load_content(case, file_ref(case))
    assert result['content_kind'] == 'original_text' and result['notice'] == ''
    assert 'provenance' not in result


def test_explicitly_removing_last_citation_keeps_legacy_shape_and_avoids_source_reads(case):
    before = valid_card()
    result = run(case, lambda: citations.normalize_card_citations(case[0], {**before, 'citations': []}))
    assert result == before
    assert 'citations' not in result
    assert case[1]['catalog_reads'] == [] and case[1]['metadata_reads'] == []
