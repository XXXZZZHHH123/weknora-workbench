"""Service inventory proofs bind route, current bearer, tenant and time."""
import asyncio
import hashlib
import hmac

import httpx
import pytest
from fastapi import HTTPException

from tin.integration import source_scope_auth,workbench_scope,kb_facts
from tin.integration.identity import Identity

WB='10000000-0000-4000-8000-000000000091'
KB='20000000-0000-4000-8000-000000000091'
PATH='/api/v1/workspaces/research/'+WB+'/source-scope'
KEY=b'synthetic-source-inventory-service-key-123456'


@pytest.fixture
def configured(tmp_path,monkeypatch):
    key=tmp_path/'service-key';key.write_bytes(KEY)
    monkeypatch.setattr(source_scope_auth.settings,'service_token_file',key)
    monkeypatch.setattr(source_scope_auth.settings,'weknora_api_url','http://native-proof.test')
    monkeypatch.setattr(source_scope_auth.time,'time',lambda:1700000000)
    identity=Identity('reader','reader@example.test','Reader',17,'viewer')
    return workbench_scope.WorkbenchScope(identity,WB,KB,17,{'knowledge_base_id':KB},'Bearer synthetic-current-user')


def verify(headers,path=PATH,authorization='Bearer synthetic-current-user',tenant=17):
    stamp=headers['X-Research-Scope-Timestamp']
    expected=hmac.new(KEY,'\n'.join(('weknora.research-scope.v1','GET',path,stamp,
        hashlib.sha256(authorization.encode()).hexdigest(),str(tenant))).encode(),hashlib.sha256).hexdigest()
    assert headers['X-Research-Scope-Signature']==expected


def test_proof_binds_every_authorization_component(configured):
    headers=source_scope_auth.proof_headers(PATH,configured.authorization,17)
    verify(headers)
    assert headers['X-Research-Scope-Timestamp']=='1700000000'
    assert source_scope_auth.proof_headers(PATH,configured.authorization,18)!=headers
    assert source_scope_auth.proof_headers(PATH,'Bearer another-user',17)!=headers
    other=PATH.replace(WB,'10000000-0000-4000-8000-000000000092')
    assert source_scope_auth.proof_headers(other,configured.authorization,17)!=headers


def test_async_scope_inventory_sends_proof(configured,monkeypatch):
    def native(request):
        verify({key:request.headers[key] for key in ('X-Research-Scope-Timestamp','X-Research-Scope-Signature')})
        return httpx.Response(200,json={'success':True,'data':{'inventory_included':True}})
    original=httpx.AsyncClient
    monkeypatch.setattr(httpx,'AsyncClient',lambda **kwargs:original(transport=httpx.MockTransport(native),**kwargs))
    assert asyncio.run(workbench_scope.go_request(configured,PATH))['success']


def test_sync_delegated_inventory_sends_same_proof(configured,monkeypatch):
    scope=workbench_scope.WorkbenchScope(configured.identity,WB,KB,17,{'knowledge_base_id':KB,'source_sharing_modules':['diagram']},
        configured.authorization,source_delegations=(KB,))
    def native(method,url,**kwargs):
        verify(kwargs['headers'])
        return httpx.Response(200,json={'success':True,'data':{'id':WB,'user_id':'reader','scope_tenant_id':17,
            'inventory_included':True,
            'knowledge_base_id':KB,'view_modules':['diagram'],'sources':[{'knowledge_base_id':KB,
                'tenant_id':17,'kind':'primary','permission':'viewer','delegated':True,'knowledge':[]}]}}
            ,request=httpx.Request(method,url))
    monkeypatch.setattr(httpx,'request',native)
    assert kb_facts.current_sources(scope)==[]


def test_missing_proof_key_never_sends_bare_inventory_request(configured,monkeypatch,tmp_path):
    monkeypatch.setattr(source_scope_auth.settings,'service_token_file',tmp_path/'missing')
    def forbidden(*args,**kwargs):raise AssertionError('missing service proof fell back to bare inventory')
    monkeypatch.setattr(httpx,'AsyncClient',forbidden)
    with pytest.raises(HTTPException) as error:asyncio.run(workbench_scope.go_request(configured,PATH))
    assert error.value.status_code==503


def test_public_summary_is_not_accepted_as_an_empty_inventory(configured,monkeypatch):
    original=httpx.AsyncClient
    transport=httpx.MockTransport(lambda request:httpx.Response(200,json={'success':True,'data':{'inventory_included':False,'sources':[]}}))
    monkeypatch.setattr(httpx,'AsyncClient',lambda **kwargs:original(transport=transport,**kwargs))
    with pytest.raises(HTTPException) as error:asyncio.run(workbench_scope.go_request(configured,PATH))
    assert error.value.status_code==503
