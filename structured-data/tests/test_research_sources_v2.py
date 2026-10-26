"""Synthetic two-source fixtures; no model calls, real files or accounts."""
import base64
import copy
import hashlib
import hmac
import json
import time
import shutil
from pathlib import Path
from datetime import date

import httpx
import duckdb
import pytest

from structured_data.store import DataStore
from structured_data.workspaces import Identity, WorkspaceRegistry, current_identity
from structured_data.research_facts_v2 import read_current
from test_knowledge_research_facts import facts, KEY, PATH

WB = '10000000-0000-4000-8000-000000000091'


@pytest.fixture
def multi(facts):
    client,store,parsed,path,review,body = facts
    copied = copy.deepcopy(parsed)
    copied['rule_version'] = 'synthetic-source-two'
    copied['observations'] = [
        {'series_id':'quantity','date':stamp,'value':value,'value_status':'valid','source_cell':f'B{index+2}'}
        for index,(stamp,value) in enumerate([('2026-01-02',20),('2026-01-03',30),('2026-01-04',40)])]
    other_store = DataStore(store.root.parent/'library-b')
    dataset = other_store.publish(copied)['dataset_id']
    other_review = {**copy.deepcopy(review),'import_id':'2'*24,'dataset_id':dataset,
        'native_source_bindings':[{**review['native_source_bindings'][0],
            'source_knowledge_id':'original-b','knowledge_base_id':'library-b'}]}
    other_path = other_store.root/'imports'/('2'*24)/'review.json'
    other_path.parent.mkdir(parents=True);other_path.write_text(json.dumps(other_review))
    updated = copy.deepcopy(parsed);updated['rule_version']='synthetic-source-one'
    updated['observations'] = [
        {'series_id':'quantity','date':stamp,'value':value,'value_status':'valid','source_cell':f'B{index+2}'}
        for index,(stamp,value) in enumerate([('2026-01-01',1),('2026-01-03',3),('2026-01-05',5)])]
    review['dataset_id']=store.publish(updated)['dataset_id'];path.write_text(json.dumps(review))
    sources = [
        {'id':f'binding-{suffix}','source_type':'central_dataset' if suffix=='b' else 'knowledge_base',
         'source_id':f'dataset-{suffix}' if suffix=='b' else f'library-{suffix}',
         'knowledge_base_id':f'library-{suffix}','tenant_id':9,'kind':'macro' if suffix=='b' else 'primary',
         'permission':'viewer','delegated':False,'view_modules':['home','market','indicators','judgments','strategies','diagram','reports'],
         'knowledge':[{'id':f'original-{suffix}','file_type':'xlsx','file_hash':body['sources'][0]['file_hash'],'parse_status':'stored'}]}
        for suffix in ('a','b')]
    registry = WorkspaceRegistry(store.root.parent.parent,9)
    identity = Identity('reader','reader@example.test','Reader',9,'viewer',workbench_id=WB,scope_version=2,sources_revision=7)
    def read(arguments,selected=None):
        marker=current_identity.set(identity)
        try: return read_current(registry,arguments,sources if selected is None else selected)
        finally: current_identity.reset(marker)
    return client,read,sources,store,review,path


def catalog(read, ids=('binding-a','binding-b'), **options):
    return read({'source_bindings':list(ids),'catalog_only':True,**options})


def test_catalog_is_real_pagination_across_sources_without_observations_or_chart_xml(multi,monkeypatch):
    _,read,_,_,_,_=multi
    def forbidden(*args,**kwargs): raise AssertionError('metadata browsing touched observations/charts')
    monkeypatch.setattr(DataStore,'_connect',forbidden)
    from structured_data import native_charts
    monkeypatch.setattr(native_charts,'index',forbidden)
    first=catalog(read,limit=1)
    second=catalog(read,offset=1,limit=1,version=first['version'])
    assert first['total']==2 and first['next_offset']==1 and first['truncated']
    assert second['next_offset'] is None and not second['truncated']
    assert first['series'][0]['series_id'] != second['series'][0]['series_id']
    assert first['series'][0]['knowledge_base_id']=='library-a'
    assert second['series'][0]['source_type']=='central_dataset'
    assert 'rows' not in first and 'source_cell' not in json.dumps(first)
    assert catalog(read,source_kind='macro')['total']==1
    assert catalog(read,search='library-b')['total']==0
    assert catalog(read,search='kg')['total']==2


def test_catalog_warm_index_does_not_rehash_or_reparse_the_workbook(multi,monkeypatch):
    _,read,_,store,review,_=multi
    first=catalog(read)
    original_open=Path.open
    def no_workbook(self,*args,**kwargs):
        if self.name in {'source.xlsx','series.json'}: raise AssertionError('warm read reread workbook/whole metadata bytes')
        return original_open(self,*args,**kwargs)
    with monkeypatch.context() as warm:
        warm.setattr(Path,'open',no_workbook)
        from structured_data import research_catalog,research_facts_v2
        def no_full_catalog(*args,**kwargs):raise AssertionError('warm selected facts reconstructed the full source catalog')
        warm.setattr(research_catalog,'read_facts',no_full_catalog)
        warm.setattr(research_facts_v2,'read_facts',no_full_catalog,raising=False)
        assert catalog(read)['version']==first['version']
        bounded=read({'source_bindings':['binding-a','binding-b'],'series_ids':[field['series_id'] for field in first['series']],
            'view_module':'diagram','points_per_series':2})
        assert sorted(row['value'] for row in bounded['rows'])==[3,5,30,40]
    (store._path(review['dataset_id'])/'source.xlsx').write_bytes(b'tampered source')
    from fastapi import HTTPException
    with pytest.raises(HTTPException) as error: catalog(read)
    assert error.value.status_code==409


def test_cross_source_contract_values_share_one_actual_common_day(multi):
    _,read,_,_,_,_=multi
    fields=catalog(read)['series'];ids=[field['series_id'] for field in fields]
    result=read({'source_bindings':['binding-a','binding-b'],'series_ids':ids,'view_module':'market',
        'as_of':'2026-01-05','points_per_series':1,'same_day_series_ids':ids})
    assert result['selected_date']=='2026-01-03'
    assert {row['date'] for row in result['rows']}=={'2026-01-03'}
    assert sorted(row['value'] for row in result['rows'])==[3,30]
    assert all('source_cell' not in row and 'raw_value' not in row for row in result['rows'])


def test_past_page_window_keeps_real_next_date_without_loading_future_values(multi):
    _,read,_,_,_,_=multi
    ids=[field['series_id'] for field in catalog(read)['series']]
    result=read({'source_bindings':['binding-a','binding-b'],'series_ids':ids,'view_module':'home',
        'as_of':'2026-01-03','points_per_series':2})
    assert max(row['date'] for row in result['rows'])==date(2026,1,3)
    assert result['calendar']=={'as_of':'2026-01-03','min_date':'2026-01-01','max_date':'2026-01-05',
        'previous_date':'2026-01-02','next_date':'2026-01-04','latest_previous_date':'2026-01-04'}
    assert 'value' not in json.dumps(result['calendar'])


def test_regular_bounded_values_deduplicate_equal_dates_and_reject_conflicts(multi):
    _,read,_,store,review,_=multi
    field=next(field for field in catalog(read)['series'] if field['knowledge_base_id']=='library-a')
    body={'source_bindings':['binding-a'],'series_ids':[field['series_id']],
        'view_module':'home','as_of':'2026-01-05','points_per_series':2}
    database=store._path(review['dataset_id'])/'data.duckdb'
    with duckdb.connect(str(database),config={'enable_external_access':'false','threads':'1','memory_limit':'128MB'}) as connection:
        connection.execute("INSERT INTO observations SELECT * REPLACE ('Z999' AS source_cell) FROM observations WHERE date='2026-01-05'")
    result=read(body,multi[2][:1])
    assert [row['value'] for row in result['rows']]==[3,5]
    with duckdb.connect(str(database),config={'enable_external_access':'false','threads':'1','memory_limit':'128MB'}) as connection:
        connection.execute("UPDATE observations SET value=6 WHERE source_cell='Z999'")
    from fastapi import HTTPException
    with pytest.raises(HTTPException) as error:read(body,multi[2][:1])
    assert error.value.status_code==409


def test_removed_saved_binding_is_reported_missing_without_legacy_fallback(multi):
    _,read,sources,_,_,_=multi
    ids=[field['series_id'] for field in catalog(read)['series']]
    result=read({'source_bindings':['binding-a'],'series_ids':ids,'view_module':'diagram',
        'points_per_series':2,'allow_unavailable':True},sources[:1])
    assert len(result['unavailable_series_ids'])==1 and len(result['indicators'])==1
    assert result['unavailable_series_ids'][0] not in [row['series_id'] for row in result['rows']]
    from fastapi import HTTPException
    with pytest.raises(HTTPException) as error:
        read({'source_bindings':['binding-a'],'series_ids':ids,'view_module':'indicators'},sources[:1])
    assert error.value.status_code==403


def signed_v2(body,revision=7):
    authorization='Bearer synthetic-current-login'
    identity={'user_id':'reader','email':'reader@example.test','display_name':'Reader','tenant_id':9,'role':'viewer',
        'workbench_id':WB,'scope_version':2,'sources_revision':revision,'authorization_sha256':hashlib.sha256(authorization.encode()).hexdigest()}
    raw=json.dumps(body,separators=(',',':')).encode()
    encoded=base64.urlsafe_b64encode(json.dumps(identity).encode()).decode().rstrip('=')
    stamp=str(int(time.time()))
    signature=hmac.new(KEY,'\n'.join((stamp,'POST',PATH,encoded,hashlib.sha256(raw).hexdigest())).encode(),hashlib.sha256).hexdigest()
    return {'content':raw,'headers':{'Content-Type':'application/json','Authorization':authorization,
        'X-Research-Identity':encoded,'X-Research-Timestamp':stamp,'X-Research-Signature':signature}}


def test_signed_multi_source_requests_recheck_revision_and_each_source_module(multi,monkeypatch):
    client,_,sources,_,_,_=multi
    state={'revision':7,'sources':sources,'inventory':True}
    def upstream(request):
        assert request.url.path==f'/api/v1/workspaces/research/{WB}/source-scope'
        stamp=request.headers['X-Research-Scope-Timestamp']
        proof='\n'.join(('weknora.research-scope.v1','GET',request.url.path,stamp,
            hashlib.sha256(request.headers['authorization'].encode()).hexdigest(),'9')).encode()
        assert request.headers['X-Research-Scope-Signature']==hmac.new(KEY,proof,hashlib.sha256).hexdigest()
        return httpx.Response(200,json={'success':True,'data':{'scope_version':2,'sources_revision':state['revision'],
            'inventory_included':state['inventory'],
            'id':WB,'user_id':'reader','scope_tenant_id':9,'sources':state['sources']}})
    original=httpx.AsyncClient
    monkeypatch.setattr(httpx,'AsyncClient',lambda **kwargs:original(transport=httpx.MockTransport(upstream),**kwargs))
    body={'source_bindings':['binding-a','binding-b'],'catalog_only':True,'limit':1}
    response=client.post(PATH,**signed_v2(body));assert response.status_code==200,response.text
    state['inventory']=False
    assert client.post(PATH,**signed_v2(body)).status_code==503
    state['inventory']=True
    state['revision']=8
    assert client.post(PATH,**signed_v2(body)).status_code==409
    state['revision']=7;sources[1]['delegated']=True;sources[1]['view_modules']=['diagram']
    assert client.post(PATH,**signed_v2(body)).status_code==403
    direct=client.post(PATH,**signed_v2({**body,'source_bindings':['binding-a']}))
    assert direct.status_code==200 and direct.json()['total']==1


def test_authorized_zero_source_request_returns_empty_without_store_reads(multi,monkeypatch):
    _,read,_,_,_,_=multi
    def forbidden(*args,**kwargs):raise AssertionError('empty source scope read a store')
    monkeypatch.setattr(DataStore,'_connect',forbidden)
    result=read({'source_bindings':[],'series_ids':[],'view_module':'home'},[])
    assert result['rows']==[] and result['indicators']==[] and result['total']==0


@pytest.mark.parametrize('change',[
    {'points_per_series':2001},
    {'series_ids':['kb-'+f'{number:048x}' for number in range(40)],'points_per_series':600},
    {'source_kind':{}},
])
def test_v2_window_and_catalog_limits_reject_invalid_or_oversized_reads(change):
    from structured_data.research_facts_v2 import parameters
    body={'source_bindings':['binding-a'],'series_ids':['kb-'+'a'*48],'view_module':'indicators',**change}
    with pytest.raises(ValueError):parameters(body)


def test_shared_source_uses_the_real_owner_storage_namespace(multi):
    _,read,sources,store,_,_=multi
    root=store.root.parent.parent
    target=root/'workspaces'/'10'/'knowledge-bases'/'library-b'
    shutil.copytree(root/'knowledge-bases'/'library-b',target)
    shutil.rmtree(root/'knowledge-bases'/'library-b')
    review_path=target/'imports'/('2'*24)/'review.json'
    review=json.loads(review_path.read_text())
    review['native_source_bindings'][0]['tenant_id']=10
    review_path.write_text(json.dumps(review))
    sources[1]['tenant_id']=10
    result=catalog(read)
    shared=next(field for field in result['series'] if field['knowledge_base_id']=='library-b')
    assert shared['knowledge_base_tenant_id']==10
    bounded=read({'source_bindings':['binding-b'],'series_ids':[shared['series_id']],
        'view_module':'diagram','points_per_series':2},sources[1:])
    assert sorted(row['value'] for row in bounded['rows'])==[30,40]
