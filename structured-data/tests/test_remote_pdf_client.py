"""Remote queue bridge: authenticated scope, immutable source and lease isolation."""
import asyncio
from copy import deepcopy
import hashlib
import json

import httpx
import pytest

from structured_data.pdf_parser_bridge import ParserConfig, parse_pdf
from structured_data.remote_pdf_client import RemotePDFError, parse_remote_pdf
from structured_data.workspaces import Identity, current_identity
from test_pdf_tables import bundle


@pytest.fixture
def remote_fixture(tmp_path, monkeypatch):
    path = tmp_path / 'source.pdf'
    path.write_bytes(b'%PDF-1.7\n isolated remote transport test')
    sha = hashlib.sha256(path.read_bytes()).hexdigest()
    raw = bundle()
    raw['source'].update(sha256=sha, page_count=1)
    raw['parser'] = dict(engine='remote_vlm', version='fixture', model='vision-fixture',
                         profile_sha256='b' * 64, independent_verification=True)
    raw['continuation_proposals'] = []
    artifact = dict(schema_version='remote_pdf_v1', source=dict(sha256=sha, page_count=1),
                    pages=[dict(page=1, text='original text')], bundle=raw)
    state = dict(artifact=artifact, initial=dict(id='job-actual', lease_id='lease-consumer-a', status='completed'),
                 polls=[], post_status=200, result_status=200, requests=[])
    original_client = httpx.AsyncClient

    async def transport(request):
        await request.aread()
        state['requests'].append(request)
        if request.method == 'POST' and request.url.path == '/v1/jobs':
            if state['post_status'] != 200:
                return httpx.Response(state['post_status'], text='private key and provider detail must not escape')
            return httpx.Response(200, json=state['initial'])
        if request.method == 'POST' and request.url.path == '/v1/jobs/job-actual/retry':
            return httpx.Response(202, json=state.get('retry_reply', dict(id='job-actual',status='completed',lease_id='retry-lease')))
        if request.method == 'GET' and request.url.path == '/v1/jobs/job-actual/result':
            return httpx.Response(state['result_status'], json=state['artifact'])
        if request.method == 'GET' and request.url.path == '/v1/jobs/job-actual':
            value = state['polls'].pop(0) if state['polls'] else dict(id='job-actual', status='running')
            return httpx.Response(200, json=value)
        if request.method == 'DELETE' and request.url.path == '/v1/jobs/job-actual':
            return httpx.Response(200, json={'status': 'detached'})
        raise AssertionError(f'Unexpected request {request.method} {request.url.path}')

    def client(**kwargs):
        assert kwargs['trust_env'] is False and kwargs['follow_redirects'] is False
        return original_client(transport=httpx.MockTransport(transport), **kwargs)

    monkeypatch.setattr(httpx, 'AsyncClient', client)
    cfg = ParserConfig('remote_vlm', 'http://remote-pdf.test', api_key='server-fixture-token',
                       poll_interval_seconds=.05, timeout_seconds=2)
    identity = Identity(user_id='verified-user', email='', display_name='', tenant_id=11,
                        role='owner', knowledge_base_id='verified-kb', knowledge_base_tenant_id=22,
                        knowledge_base_can_write=True)
    token = current_identity.set(identity)
    yield path, sha, cfg, state
    current_identity.reset(token)


def run(remote_fixture, source=None, progress=None):
    path, sha, cfg, _ = remote_fixture
    return asyncio.run(parse_remote_pdf(path, source or {'sha256': sha, 'file_name': 'authorized-name.pdf'}, cfg, progress))


def test_completed_bundle_preserves_profile_provenance_and_verified_scope(remote_fixture):
    path, sha, cfg, state = remote_fixture
    original = deepcopy(state['artifact']['bundle'])
    progress = []
    source = dict(sha256=sha, file_name='authorized-name.pdf', knowledge_id='authorized-document',
                  knowledge_base_id='verified-kb', tenant_id='forged-tenant', user_id='forged-user')
    result = run(remote_fixture, source, progress.append)
    assert result['parser'] == original['parser']
    assert result['fragments'] == original['fragments']
    assert result['continuation_proposals'] == original['continuation_proposals']
    assert result['source'] == dict(original['source'], file_name='authorized-name.pdf',
                                    knowledge_id='authorized-document', knowledge_base_id='verified-kb')
    assert result['remote_job_id'] == 'job-actual'
    assert 'parse_duration_ms' not in result
    assert progress[0]['queue_status'] == 'completed'
    assert [r.method for r in state['requests']] == ['POST', 'GET']
    for request in state['requests']:
        assert request.headers['authorization'] == 'Bearer server-fixture-token'
        assert request.headers['x-parse-scope'] == 'tenant:22'
        assert request.headers['x-parse-owner'] == 'verified-user'
        assert 'forged-' not in str(request.headers)
    assert path.read_bytes() in state['requests'][0].content


def test_parser_dispatches_remote_engine(remote_fixture):
    path, sha, cfg, state = remote_fixture
    result = asyncio.run(parse_pdf(path, {'sha256': sha, 'file_name': 'source.pdf'}, config=cfg))
    assert result['remote_job_id'] == 'job-actual'
    assert result['parser']['engine'] == 'remote_vlm'


def test_missing_identity_rejects_before_network(remote_fixture):
    _, _, _, state = remote_fixture
    token = current_identity.set(None)
    try:
        with pytest.raises(RemotePDFError, match='missing_scope'):
            run(remote_fixture)
    finally:
        current_identity.reset(token)
    assert not state['requests']


def test_tenant_scope_without_kb_uses_authenticated_tenant(remote_fixture):
    _, _, _, state = remote_fixture
    token = current_identity.set(Identity(user_id='user-b', email='', display_name='', tenant_id=44, role='owner'))
    try:
        run(remote_fixture)
    finally:
        current_identity.reset(token)
    assert state['requests'][0].headers['x-parse-scope'] == 'tenant:44'


def test_input_source_digest_mismatch_rejects_before_upload(remote_fixture):
    _, _, _, state = remote_fixture
    with pytest.raises(RemotePDFError, match='source_mismatch'):
        run(remote_fixture, {'sha256': 'f' * 64})
    assert not state['requests']


@pytest.mark.parametrize('bad', ['outer_sha', 'bundle_sha', 'outer_schema', 'page_count', 'page_bool',
                                'duplicate_page', 'page_missing', 'bundle_count', 'fragment_page',
                                'source_shape', 'page_shape', 'bundle_invalid'])
def test_result_rejects_invalid_source_coverage_and_detaches_only_own_lease(remote_fixture, bad):
    _, _, _, state = remote_fixture
    artifact = state['artifact']
    if bad == 'outer_sha': artifact['source']['sha256'] = 'c' * 64
    elif bad == 'bundle_sha': artifact['bundle']['source']['sha256'] = 'c' * 64
    elif bad == 'outer_schema': artifact['schema_version'] = 'different'
    elif bad == 'page_count': artifact['source']['page_count'] = 2
    elif bad == 'page_bool': artifact['source']['page_count'] = True
    elif bad == 'duplicate_page':
        artifact['pages'].append({'page': 1})
        artifact['source']['page_count'] = 2
    elif bad == 'page_missing': artifact['pages'] = []
    elif bad == 'bundle_count': artifact['bundle']['source']['page_count'] = 2
    elif bad == 'fragment_page': artifact['bundle']['fragments'][0]['page'] = 2
    elif bad == 'source_shape': artifact['source'] = []
    elif bad == 'page_shape': artifact['pages'] = ['not-page-object']
    else: artifact['bundle']['fragments'][0]['rows'][0][0]['text'] = None
    with pytest.raises((RemotePDFError, ValueError)):
        run(remote_fixture)
    canceled = [r for r in state['requests'] if r.method == 'DELETE']
    assert len(canceled) == 1
    assert canceled[0].headers['x-parse-lease'] == 'lease-consumer-a'
    assert canceled[0].headers['x-parse-scope'] == 'tenant:22'


def test_queue_429_does_not_expose_provider_body_or_create_cancel(remote_fixture):
    _, _, _, state = remote_fixture
    state['post_status'] = 429
    with pytest.raises(RemotePDFError) as captured:
        run(remote_fixture)
    assert captured.value.code == 'http_429'
    assert 'private' not in str(captured.value) and 'provider' not in str(captured.value)
    assert [r.method for r in state['requests']] == ['POST']


def test_queued_progress_and_completion_preserve_creation_lease(remote_fixture):
    _, _, _, state = remote_fixture
    state['initial']['status'] = 'queued'
    state['polls'] = [dict(id='job-actual', status='running', progress={'page': 1}),
                      dict(id='job-actual', status='completed')]
    progress = []
    run(remote_fixture, progress=progress.append)
    assert [p['queue_status'] for p in progress] == ['queued', 'running', 'completed']
    assert not any(r.method == 'DELETE' for r in state['requests'])


def test_timeout_detaches_creation_lease_even_without_lease_in_poll(remote_fixture):
    _, _, cfg, state = remote_fixture
    cfg.timeout_seconds = .08
    state['initial']['status'] = 'queued'
    with pytest.raises(RemotePDFError, match='client_timeout'):
        run(remote_fixture)
    assert [r.method for r in state['requests']][-1] == 'DELETE'
    assert state['requests'][-1].headers['x-parse-lease'] == 'lease-consumer-a'


def test_task_cancellation_detaches_only_consumer(remote_fixture):
    path, sha, cfg, state = remote_fixture
    state['initial']['status'] = 'queued'
    async def scenario():
        task = asyncio.create_task(parse_remote_pdf(path, {'sha256': sha}, cfg))
        while not state['requests']:
            await asyncio.sleep(0)
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task
    asyncio.run(scenario())
    assert state['requests'][-1].method == 'DELETE'
    assert state['requests'][-1].headers['x-parse-lease'] == 'lease-consumer-a'


def test_failed_job_code_is_sanitized_without_redundant_cancel(remote_fixture):
    _, _, _, state = remote_fixture
    state['initial'].update(status='failed', error={'code': 'https://private-provider/token=secret'})
    with pytest.raises(RemotePDFError) as captured:
        run(remote_fixture)
    assert captured.value.code == 'failed'
    assert str(captured.value) == 'Remote PDF parser: failed'
    assert [r.method for r in state['requests']] == ['POST']


@pytest.mark.parametrize('lease', ['', None, 'another/job', 'bad\nheader'])
def test_invalid_lease_never_attempts_unscoped_cancel(remote_fixture, lease):
    _, _, _, state = remote_fixture
    state['initial'].update(status='queued', lease_id=lease)
    with pytest.raises(RemotePDFError, match='invalid_lease_id'):
        run(remote_fixture)
    assert [r.method for r in state['requests']] == ['POST']


def test_response_budget_rejects_before_loading_unbounded_result(remote_fixture):
    _, _, cfg, state = remote_fixture
    cfg.max_response_bytes = 1024
    state['artifact']['extra'] = 'x' * 2048
    with pytest.raises(RemotePDFError, match='oversize_result'):
        run(remote_fixture)
    assert state['requests'][-1].headers['x-parse-lease'] == 'lease-consumer-a'


def test_explicit_retry_restarts_terminal_once_with_server_token(remote_fixture):
    path, sha, cfg, state = remote_fixture
    state['initial']['status'] = 'failed'
    token = 'e' * 32
    result = asyncio.run(parse_remote_pdf(path, {'sha256': sha}, cfg, retry_token=token))
    assert result['remote_job_id'] == 'job-actual'
    retried = [r for r in state['requests'] if r.url.path.endswith('/retry')]
    assert len(retried) == 1 and retried[0].headers['x-parse-retry-key'] == token


def test_explicit_retry_never_loops_when_restarted_job_fails(remote_fixture):
    path, sha, cfg, state = remote_fixture
    state['initial']['status'] = 'failed'
    state['retry_reply'] = dict(id='job-actual',status='failed',lease_id='retry-lease',error={'code':'MODEL_UNAVAILABLE'})
    with pytest.raises(RemotePDFError, match='MODEL_UNAVAILABLE'):
        asyncio.run(parse_remote_pdf(path, {'sha256': sha}, cfg, retry_token='e' * 32))
    assert len([r for r in state['requests'] if r.url.path.endswith('/retry')]) == 1


def test_manual_retry_does_not_restart_active_or_completed_work(remote_fixture):
    path, sha, cfg, state = remote_fixture
    asyncio.run(parse_remote_pdf(path, {'sha256': sha}, cfg, retry_token='e' * 32))
    assert not any(r.url.path.endswith('/retry') for r in state['requests'])


def test_same_remote_cache_result_produces_same_immutable_draft(remote_fixture, tmp_path):
    from structured_data.pdf_tables import PDFTableStore
    first = run(remote_fixture)
    second = run(remote_fixture)
    store = PDFTableStore(tmp_path / 'stable-drafts')
    a, b = store.create_draft(first), store.create_draft(second)
    assert a['draft_id'] == b['draft_id']
    assert len(store.list_drafts()['drafts']) == 1
    assert first['parser']['profile_sha256'] == 'b' * 64
