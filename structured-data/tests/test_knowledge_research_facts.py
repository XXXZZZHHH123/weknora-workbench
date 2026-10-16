"""Canonical research reads use the KB's current native imports, never research copies."""
import copy
import base64
import hashlib
import hmac
import json
import time

import pytest
from fastapi.testclient import TestClient
from openpyxl import Workbook

from structured_data.server import create_app
from structured_data.store import DataStore

PATH = '/api/research/facts'
KEY = b'synthetic-research-facts-service-key-123456789'


def signed(body, *, kb='library-a', tenant=9, owner=9):
    payload = {'user_id':'reader', 'email':'reader@example.test', 'display_name':'Reader',
               'tenant_id':tenant, 'role':'viewer'}
    if kb is not None:
        payload.update(knowledge_base_id=kb,knowledge_base_tenant_id=owner)
    raw=json.dumps(body,separators=(',',':')).encode()
    encoded=base64.urlsafe_b64encode(json.dumps(payload).encode()).decode().rstrip('=')
    stamp=str(int(time.time()))
    digest=hmac.new(KEY,'\n'.join((stamp,'POST',PATH,encoded,hashlib.sha256(raw).hexdigest())).encode(),hashlib.sha256).hexdigest()
    return {'content':raw,'headers':{'Content-Type':'application/json','X-Research-Identity':encoded,
            'X-Research-Timestamp':stamp,'X-Research-Signature':digest}}


@pytest.fixture
def facts(tmp_path,monkeypatch):
    monkeypatch.setenv('STRUCTURED_TENANT_ID','9')
    secret=tmp_path/'key'; secret.write_bytes(KEY)
    monkeypatch.setenv('STRUCTURED_SERVICE_TOKEN_FILE',str(secret))
    root=tmp_path/'data'; directory=root/'knowledge-bases/library-a'
    original=tmp_path/'values.xlsx'
    wb=Workbook(); ws=wb.active;ws.title='Records'
    ws.append(['date','quantity']);ws.append(['2026-01-01',0]);ws.append(['2026-01-02','#N/A']);wb.save(original)
    file_hash=hashlib.md5(original.read_bytes()).hexdigest()
    parsed={'source_file':str(original),'sha256':hashlib.sha256(original.read_bytes()).hexdigest(),
      'rule_version':'synthetic-1','dataset_kind':'business','use_reviewed_mappings':False,
      'series':[{'series_id':'quantity','metric':'Quantity','unit':'kg','frequency':'日',
                 'date_mode':'date','sheet':'Records','header_cells':['B1'],'dimensions':{}}],
      'observations':[{'series_id':'quantity','date':'2026-01-01','value':0,'value_status':'valid','source_cell':'B2','date_cell':'A2','raw_value':0},
                      {'series_id':'quantity','date':'2026-01-02','value':None,'value_status':'excel_error','source_cell':'B3','date_cell':'A3','raw_value':'#N/A'}],
      'sheets':[],'excluded_regions':[],'issues':[]}
    store=DataStore(directory); dataset=store.publish(parsed)['dataset_id']
    review={'import_id':'1'*24,'dataset_id':dataset,'dataset_kind':'business','source_file':'values.xlsx',
      'use_reviewed_mappings':False,'updated_at':'2026-01-03T00:00:00+00:00','summary':{'confirmed':1},
      'sheets':[{'name':'Records','status':'confirmed','mapping':{'blocks':[]}}],
      'native_source_bindings':[{'source_knowledge_id':'original-a','file_hash':file_hash,
        'knowledge_base_id':'library-a','tenant_id':9,'file_name':'values.xlsx','file_size':original.stat().st_size}]}
    review_path=directory/'imports'/('1'*24)/'review.json';review_path.parent.mkdir(parents=True);review_path.write_text(json.dumps(review))
    with TestClient(create_app(root)) as client:
        yield client,store,parsed,review_path,review,{'sources':[{'knowledge_id':'original-a','file_hash':file_hash}]}


def test_only_current_native_kb_data_is_visible_and_keeps_zero_and_missing(facts):
    client,store,parsed,path,review,body=facts
    response=client.post(PATH,**signed(body))
    assert response.status_code==200,response.text
    data=response.json()
    assert data['knowledge_base_id']=='library-a' and data['knowledge_base_tenant_id']==9
    assert len(data['version'])==64 and data['total']==2
    assert data['indicators'][0]['source_series_id']=='quantity'
    assert data['indicators'][0]['series_id'].startswith('kb-')
    assert [row['value'] for row in data['rows']]==[0,None]
    assert data['rows'][1]['raw_value']=='#N/A' and data['rows'][1]['source_cell']=='B3'
    assert all(row['source_knowledge_id']=='original-a' for row in data['rows'])
    assert client.post(PATH,**signed({'sources':[]})).json()['total']==0
    assert client.post(PATH,**signed(body,kb='library-b')).json()['total']==0
    assert client.post(PATH,**signed(body,owner=10)).json()['total']==0
    shared=client.post(PATH,**signed(body,tenant=10,owner=9)).json()
    assert shared['version']==data['version'] and shared['indicators']==data['indicators']


def test_no_unbound_tenant_or_unpublished_import_can_supply_facts(facts):
    client,store,parsed,path,review,body=facts
    assert client.post(PATH,**signed(body,kb=None)).status_code==400
    review['dataset_id']=None;path.write_text(json.dumps(review))
    data=client.post(PATH,**signed(body)).json()
    assert data['total']==0 and data['indicators']==[]


def test_source_deletion_or_hash_change_immediately_hides_previous_facts(facts):
    client,store,parsed,path,review,body=facts
    old=client.post(PATH,**signed(body)).json()
    changed={'sources':[{'knowledge_id':'original-a','file_hash':'e'*32}]}
    current=client.post(PATH,**signed(changed)).json()
    assert current['total']==0 and current['version']!=old['version']
    stale=client.post(PATH,**signed({**changed,'version':old['version']}))
    assert stale.status_code==409


@pytest.mark.parametrize('status',['auto_ready','template_ready','trusted_existing','needs_review'])
def test_scanned_or_withdrawn_sheet_is_not_a_confirmed_research_source(facts,status):
    client,store,parsed,path,review,body=facts
    previous=client.post(PATH,**signed(body)).json()
    review['sheets'][0]['status']=status;path.write_text(json.dumps(review))
    current=client.post(PATH,**signed(body)).json()
    assert current['total']==0 and current['indicators']==[]
    assert current['version']!=previous['version']


def test_pagination_pins_version_and_reconfirmation_keeps_source_identity(facts):
    client,store,parsed,path,review,body=facts
    first=client.post(PATH,**signed({**body,'limit':1})).json()
    second=client.post(PATH,**signed({**body,'offset':1,'limit':1,'version':first['version']})).json()
    assert second['version']==first['version'] and second['rows'][0]['value'] is None
    assert first['next_offset']==1 and second['next_offset'] is None
    updated=copy.deepcopy(parsed);updated['rule_version']='synthetic-2';updated['observations'][0]['value']=12
    dataset=store.publish(updated)['dataset_id'];review['dataset_id']=dataset;path.write_text(json.dumps(review))
    assert client.post(PATH,**signed({**body,'offset':1,'version':first['version']})).status_code==409
    current=client.post(PATH,**signed(body)).json()
    assert current['rows'][0]['value']==12
    assert current['indicators'][0]['series_id']==first['indicators'][0]['series_id']


@pytest.mark.parametrize('changes',[{'offset':True},{'offset':-1},{'limit':0},{'limit':10001},{'version':'invalid'},
    {'sources':[{'knowledge_id':'original-a','file_hash':'invalid'}]}, {'sources':[{'knowledge_id':'../outside','file_hash':'a'*32}]},
    {'tenant_id':10}])
def test_facts_reject_invalid_protocol_parameters(facts,changes):
    client,_,_,_,_,body=facts
    assert client.post(PATH,**signed({**body,**changes})).status_code==400


def test_tampered_original_fails_closed(facts):
    client,store,parsed,path,review,body=facts
    original=store._path(review['dataset_id'])/'source.xlsx'
    original.write_bytes(b'changed source')
    assert client.post(PATH,**signed(body)).status_code==409


def test_ambiguous_current_bindings_fail_instead_of_choosing_a_source(facts):
    client,store,parsed,path,review,body=facts
    updated=copy.deepcopy(parsed);updated['rule_version']='alternative'
    alternate=store.publish(updated)['dataset_id']
    other={**review,'import_id':'2'*24,'dataset_id':alternate}
    location=path.parent.parent/('2'*24)/'review.json'
    location.parent.mkdir();location.write_text(json.dumps(other))
    assert client.post(PATH,**signed(body)).status_code==409


def test_service_auth_signature_is_required_for_facts(facts):
    client,_,_,_,_,body=facts
    assert client.post(PATH,json=body).status_code==401
    parameters=signed(body);parameters['headers']['X-Research-Signature']='0'*64
    assert client.post(PATH,**parameters).status_code==401


def test_metadata_probe_uses_full_version_checks_without_scanning_rows(facts,monkeypatch):
    client,store,parsed,path,review,body=facts
    full=client.post(PATH,**signed(body)).json()
    def forbidden(*args,**kwargs):
        raise AssertionError('version-only probe scanned observations')
    monkeypatch.setattr(DataStore,'_connect',forbidden)
    probe=client.post(PATH,**signed({**body,'metadata_only':True}))
    assert probe.status_code==200,probe.text
    assert probe.json()=={'knowledge_base_id':'library-a','knowledge_base_tenant_id':9,
        'version':full['version'],'metadata_only':True}
    review['sheets'][0]['status']='needs_review';path.write_text(json.dumps(review))
    assert client.post(PATH,**signed({**body,'metadata_only':True})).json()['version']!=full['version']
    original=store._path(review['dataset_id'])/'source.xlsx';original.write_bytes(b'tampered')
    assert client.post(PATH,**signed({**body,'metadata_only':True})).status_code==409


@pytest.mark.parametrize('invalid',[1,None,'true',{},[]])
def test_metadata_probe_type_is_strict(facts,invalid):
    client,_,_,_,_,body=facts
    assert client.post(PATH,**signed({**body,'metadata_only':invalid})).status_code==400
