import base64
import hashlib
import hmac
import json
import time

import httpx
import pytest
from fastapi.testclient import TestClient
from structured_data.server import create_app
from test_store import snapshot


def signed(key, path='/api/tools/read', body=None, tenant=10000, method='POST', timestamp=None):
    raw=json.dumps(body or {},separators=(',',':')).encode() if method=='POST' else b''
    identity={'user_id':'researcher-a','email':'a@example.com','display_name':'A','tenant_id':tenant,'role':'viewer'}
    encoded=base64.urlsafe_b64encode(json.dumps(identity,separators=(',',':')).encode()).decode().rstrip('=')
    stamp=str(timestamp or int(time.time()))
    signature=hmac.new(key, '\n'.join((stamp,method,path,encoded,hashlib.sha256(raw).hexdigest())).encode(), hashlib.sha256).hexdigest()
    return raw, {'Content-Type':'application/json','X-Research-Identity':encoded,'X-Research-Timestamp':stamp,'X-Research-Signature':signature}


@pytest.fixture
def service(snapshot,monkeypatch):
    store,dataset,parsed=snapshot
    key=b'test-read-key-for-workspaces-1234567890'
    path=store.root/'read-key';path.write_bytes(key)
    monkeypatch.setenv('STRUCTURED_SERVICE_TOKEN_FILE',str(path))
    monkeypatch.setenv('STRUCTURED_TENANT_ID','10000')
    with TestClient(create_app(store.root)) as client:
        yield client,key,dataset


def test_native_queries_preserve_cells_and_use_complete_aggregate(service):
    client,key,dataset=service
    body={'operation':'query_series','arguments':{'dataset_id':dataset,'series_id':'price','operation':'max','limit':1}}
    raw,headers=signed(key,body=body)
    response=client.post('/api/tools/read',content=raw,headers=headers)
    assert response.status_code==200
    result=response.json()
    assert result['aggregate']==20
    assert result['extremum_rows'][0]['source_cell']=='B4'
    assert result['extremum_rows'][0]['date_cell']=='A4'
    body={'operation':'analyze_table','arguments':{'dataset_id':dataset,'series_ids':['price'],'sql':'SELECT sum(value) FROM data','limit':1}}
    raw,headers=signed(key,body=body)
    assert client.post('/api/tools/read',content=raw,headers=headers).json()['rows']==[[30]]


def test_other_space_cannot_read_or_download_pilot_snapshot(service):
    client,key,dataset=service
    raw,headers=signed(key,body={'operation':'list_datasets','arguments':{}},tenant=10001)
    assert client.post('/api/tools/read',content=raw,headers=headers).json()=={'datasets':[]}
    raw,headers=signed(key,body={'dataset_id':dataset,'series_id':'price'},tenant=10001,path='/api/research/export')
    assert client.post('/api/research/export',content=raw,headers=headers).status_code==400
    path='/api/research/source/'+dataset
    _,headers=signed(key,tenant=10001,path=path,method='GET')
    assert client.get(path,headers=headers).status_code==400


def test_export_pagination_keeps_error_cells(service):
    client,key,dataset=service
    raw,headers=signed(key,body={'dataset_id':dataset,'series_id':'price','limit':2},path='/api/research/export')
    result=client.post('/api/research/export',content=raw,headers=headers).json()
    assert result['total']==3 and result['next_offset']==2
    assert result['rows'][1]['value'] is None
    assert result['rows'][1]['raw_value']=='#N/A'
    assert result['series']['header_cells']==['B1']


def test_signatures_bind_body_time_and_endpoint(service):
    client,key,dataset=service
    raw,headers=signed(key,body={'operation':'list_datasets','arguments':{}})
    assert client.post('/api/tools/read',content=raw+b' ',headers=headers).status_code==401
    assert client.post('/api/research/export',content=raw,headers=headers).status_code==401
    raw,headers=signed(key,body={},timestamp=int(time.time())-120)
    assert client.post('/api/tools/read',content=raw,headers=headers).status_code==401
    assert client.post('/api/tools/read',content=raw,headers=headers|{'X-Research-Timestamp':'9'*400}).status_code==401
    assert client.post('/api/tools/read',json={'operation':'list_datasets','arguments':{}},headers={'Authorization':'Bearer legacy'}).status_code==401


def test_describe_only_selected_schemas_and_no_arbitrary_operation(service):
    client,key,_=service
    raw,headers=signed(key,body={'operation':'describe_tools','arguments':{'operations':['analyze_table']}})
    result=client.post('/api/tools/read',content=raw,headers=headers)
    assert result.status_code==200
    tools=result.json()['tools']
    assert len(tools)==1 and tools[0]['name']=='analyze_table'
    assert 'sql' in tools[0]['inputSchema']['properties']
    for body in ({'operation':'delete','arguments':{}},{'operation':'list_datasets','arguments':{'tenant_id':10001}}):
        raw,headers=signed(key,body=body)
        assert client.post('/api/tools/read',content=raw,headers=headers).status_code==400


def test_viewer_cannot_write_and_missing_membership_is_rejected(tmp_path,monkeypatch):
    class Auth:
        def __init__(self,**kw):pass
        async def __aenter__(self):return self
        async def __aexit__(self,*a):pass
        async def get(self,url,headers):
            if headers['Authorization']=='Bearer tenantless':
                return httpx.Response(200,json={'data':{'user':{'id':'viewer'},'tenant':None,'tenant_required':True}})
            return httpx.Response(200,json={'data':{'user':{'id':'viewer','tenant_id':10000},
                'memberships':[] if headers['Authorization']=='Bearer missing' else [{'tenant_id':10000,
                    'role':'contributor' if headers['Authorization']=='Bearer contributor' else 'viewer'}]}})
    monkeypatch.setattr(httpx,'AsyncClient',Auth)
    with TestClient(create_app(tmp_path)) as client:
        headers={'Authorization':'Bearer viewer','Origin':'http://testserver'}
        assert client.get('/api/datasets',headers=headers).status_code==200
        assert client.post('/api/import',files={'file':('x.xlsx',b'invalid')},headers=headers).status_code==403
        assert client.get('/api/datasets',headers={'Authorization':'Bearer missing'}).status_code==403
        assert client.get('/api/datasets',headers={'Authorization':'Bearer tenantless'}).status_code==403
        assert client.post('/api/model-settings',json={'model':'x','base_url':'https://x.example/v1','api_key':'x'},
            headers=headers|{'Authorization':'Bearer contributor'}).status_code==403
