"""A WB grant exposes published research facts, never source APIs or raw files."""
import base64
import hashlib
import hmac
import json
import time

import httpx
import pytest

from test_knowledge_research_facts import facts, KEY, PATH

WB = 'a0000000-0000-4000-8000-000000000001'


def signed_projection(body, *, path=PATH, user='reader', kb='library-a', authorization='Bearer reader',
                      workbench=True, machine=False):
    identity = {'user_id': user, 'email': 'reader@example.test', 'display_name': 'Reader',
                'tenant_id': 9, 'role': 'contributor', 'knowledge_base_id': kb,
                'knowledge_base_tenant_id': 9}
    if machine:
        identity['principal_type'] = 'api_key'
    else:
        identity['authorization_sha256'] = hashlib.sha256(authorization.encode()).hexdigest()
    if workbench:
        identity['workbench_id'] = WB
    raw = json.dumps(body, separators=(',', ':')).encode()
    encoded = base64.urlsafe_b64encode(json.dumps(identity).encode()).decode().rstrip('=')
    stamp = str(int(time.time()))
    message = '\n'.join((stamp, 'POST', path, encoded, hashlib.sha256(raw).hexdigest())).encode()
    digest = hmac.new(KEY, message, hashlib.sha256).hexdigest()
    return {'content': raw, 'headers': {'Authorization': authorization, 'Content-Type': 'application/json',
        'X-Research-Identity': encoded, 'X-Research-Timestamp': stamp, 'X-Research-Signature': digest}}


@pytest.fixture
def delegated(facts, monkeypatch):
    client, store, parsed, review_path, review, body = facts
    state = {'status': 200, 'calls': 0, 'user_id': 'reader', 'delegated': True,
             'direct_status': 200, 'dataset_id': review['dataset_id'], 'view_modules': ['market']}
    original_client = httpx.AsyncClient

    def upstream(request):
        if request.url.path == '/api/v1/knowledge-bases/library-a':
            state['calls'] += 1
            return httpx.Response(state['direct_status'], json={'success': True, 'data': {
                'id': 'library-a', 'tenant_id': 9, 'my_permission': 'viewer'}})
        assert request.url.path == '/api/v1/workspaces/research/' + WB + '/source-scope'
        assert request.headers['authorization'] == 'Bearer reader'
        assert request.headers['x-tenant-id'] == '9'
        state['calls'] += 1
        sources = [{'knowledge_base_id': 'library-a', 'tenant_id': 9, 'kind': 'primary',
                    'permission': 'viewer', 'delegated': state['delegated'], 'knowledge': [
                        {'id': source['knowledge_id'], 'file_hash': source['file_hash'], 'file_type': 'xlsx'}
                        for source in body['sources']]}]
        return httpx.Response(state['status'], json={'success': state['status'] == 200, 'data': {
            'inventory_included':True,
            'id': WB, 'user_id': state['user_id'], 'scope_tenant_id': 9,
            'knowledge_base_id': 'library-a', 'sources': sources, 'view_modules': state['view_modules']}})

    monkeypatch.setattr(httpx, 'AsyncClient', lambda **kw: original_client(transport=httpx.MockTransport(upstream), **kw))
    return client, body, state


def test_delegated_page_facts_revalidate_current_consent_and_reject_full_catalog(delegated):
    client, body, state = delegated
    from test_knowledge_research_facts import signed
    field = client.post(PATH, **signed(body)).json()['indicators'][0]['series_id']
    request = {**body, 'series_ids': [field], 'view_module': 'market'}
    result = client.post(PATH, **signed_projection(request))
    assert result.status_code == 200, result.text
    assert result.json()['rows'][0]['value'] == 0
    assert client.post(PATH, **signed_projection(body)).status_code == 403
    assert client.post(PATH, **signed_projection({**body, 'metadata_only': True})).status_code == 403
    assert state['calls'] == 3
    state['status'] = 403
    denied = client.post(PATH, **signed_projection(request))
    assert denied.status_code == 403 and 'quantity' not in denied.text


@pytest.mark.parametrize('modules', [[], None, ['market', 'market'], ['all'], 'market'])
def test_delegated_facts_require_a_current_valid_module_grant(delegated, modules):
    client, body, state = delegated
    from test_knowledge_research_facts import signed
    field = client.post(PATH, **signed(body)).json()['indicators'][0]['series_id']
    state['view_modules'] = modules
    response = client.post(PATH, **signed_projection({**body, 'series_ids': [field], 'view_module': 'market'}))
    assert response.status_code == 403


def test_delegated_facts_cannot_change_the_approved_page_or_read_unbounded_metadata(delegated):
    client, body, state = delegated
    from test_knowledge_research_facts import signed
    field = client.post(PATH, **signed(body)).json()['indicators'][0]['series_id']
    for changes in ({}, {'view_module': 'diagram'}, {'view_module': 'indicators'},
                    {'view_module': 'market', 'metadata_only': True}):
        response = client.post(PATH, **signed_projection({**body, 'series_ids': [field], **changes}))
        assert response.status_code == 403, response.text
    # A later revocation is checked even when the facts have already been read.
    assert client.post(PATH, **signed_projection({**body, 'series_ids': [field], 'view_module': 'market'})).status_code == 200
    state['view_modules'] = ['diagram']
    assert client.post(PATH, **signed_projection({**body, 'series_ids': [field], 'view_module': 'market'})).status_code == 403


def test_delegation_cannot_read_raw_exports_or_other_sources(delegated):
    client, body, state = delegated
    assert client.post('/api/research/export', **signed_projection(
        {'dataset_id': state['dataset_id'], 'series_id': 'quantity'}, path='/api/research/export')).status_code == 403
    foreign = {'sources': [{'knowledge_id': 'other-file', 'file_hash': 'a' * 32}]}
    assert client.post(PATH, **signed_projection(foreign)).status_code == 403
    assert client.post(PATH, **signed_projection(body, kb='library-b')).status_code == 403


def test_original_bearer_and_signed_identity_must_belong_to_same_user(delegated):
    client, body, state = delegated
    assert client.post(PATH, **signed_projection(body, user='someone-else')).status_code == 403
    assert client.post(PATH, **signed_projection(body, authorization='')).status_code == 401


def test_native_human_signed_reads_revalidate_private_kb_and_bind_bearer(delegated):
    client, _, state = delegated
    body = {'operation': 'list_datasets', 'arguments': {}}
    path = '/api/tools/read'
    signed = signed_projection(body, path=path, workbench=False)
    assert client.post(path, **signed).status_code == 200
    state['direct_status'] = 403
    assert client.post(path, **signed).status_code == 403
    assert state['calls'] == 2
    signed['headers']['Authorization'] = 'Bearer someone-else'
    assert client.post(path, **signed).status_code == 401
    signed['headers'].pop('Authorization')
    assert client.post(path, **signed).status_code == 401


def test_explicit_machine_grant_is_scoped_and_cannot_impersonate_workbench(delegated):
    client, body, state = delegated
    assert client.post(PATH, **signed_projection(body, workbench=False, machine=True,
                                               authorization='')).status_code == 200
    assert state['calls'] == 0
    assert client.post(PATH, **signed_projection(body, machine=True, authorization='')).status_code == 401
