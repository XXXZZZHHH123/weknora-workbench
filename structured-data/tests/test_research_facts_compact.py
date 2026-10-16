"""Optional transport keeps the same canonical values, identities and evidence."""
import copy
import json
from contextlib import contextmanager

import pytest

from structured_data.store import DataStore
from test_knowledge_research_facts import PATH, facts, signed
from test_research_facts_page_ranges import paged_facts


ENCODING='research-rows-v1'
COLUMNS=['series_id','date','value','source_cell','formula','value_status','date_cell','raw_value']
SOURCE_KEYS=['source_knowledge_id','dataset_id','source_series_id']


def _projected(rows):
    return [row for row in rows if row['value_status'] in {'valid','ok','正常'}
            and row['value'] is not None and row['date'] is not None]


def _decoded(document):
    assert document['encoding']==ENCODING and document['row_columns']==COLUMNS
    catalog={spec['series_id']:spec for spec in document['indicators']}
    decoded=[]
    for values in document['rows']:
        assert isinstance(values,list) and len(values)==len(COLUMNS)
        row=dict(zip(COLUMNS,values));spec=catalog[row['series_id']]
        decoded.append({**row,**{key:spec[key] for key in SOURCE_KEYS}})
    return decoded


def _equivalent_rows(document,legacy):
    expected=[{key:row[key] for key in COLUMNS+SOURCE_KEYS} for row in _projected(legacy['rows'])]
    assert _decoded(document)==expected
    assert document['total']==len(expected)
    assert document['version']==legacy['version']
    assert document['indicators']==legacy['indicators']
    for dataset,original in zip(document['datasets'],legacy['datasets']):
        assert {key:value for key,value in dataset.items() if key!='raw_observation_count'}==original
        assert dataset['raw_observation_count']==sum(
            row['source_knowledge_id']==dataset['source_knowledge_id'] and row['dataset_id']==dataset['dataset_id']
            for row in legacy['rows'])


@pytest.fixture
def mixed_facts(facts):
    client,store,parsed,path,review,body=facts
    parsed=copy.deepcopy(parsed);parsed['rule_version']='synthetic-compact-mixed'
    extra=[
        ('2026-01-03',None,'valid',None),
        ('2026-01-04',1.25,'ok','=B2+1.25'),
        ('2026-01-05',-2.5,'正常',None),
        (None,0,'valid',None),
        ('2026-01-06',42,'invalid',None),
        ('2026-01-07',0,'valid','=B2-B2'),
        (None,None,'missing',None),
        ('2026-01-08',5,'VALID',None),
        ('2026-01-09',7.25,'valid',None),
    ]
    for index,(when,value,status,formula) in enumerate(extra,start=4):
        parsed['observations'].append({'series_id':'quantity','date':when,'value':value,
            'value_status':status,'source_cell':f'B{index}','date_cell':f'A{index}',
            'raw_value':'原文缺失' if value is None else str(value),'formula':formula})
    review['dataset_id']=store.publish(parsed)['dataset_id'];path.write_text(json.dumps(review))
    return client,store,parsed,path,review,body


def test_optional_compact_rows_preserve_all_current_research_values_and_default_bytes(mixed_facts):
    client,_,_,_,_,body=mixed_facts
    legacy=client.post(PATH,**signed(body))
    assert legacy.status_code==200,legacy.text
    response=client.post(PATH,**signed({**body,'encoding':ENCODING}))
    assert response.status_code==200,response.text
    compact=response.json();_equivalent_rows(compact,legacy.json())
    assert compact['total']==5 and compact['datasets'][0]['raw_observation_count']==11
    assert [row[2] for row in compact['rows']]==[0,1.25,-2.5,0,7.25]
    assert any(row[4]=='=B2-B2' and row[7]=='0' for row in compact['rows'])
    repeated=client.post(PATH,**signed(body))
    assert repeated.content==legacy.content
    assert 'encoding' not in repeated.json() and 'row_columns' not in repeated.json()
    assert 'raw_observation_count' not in repeated.json()['datasets'][0]


@pytest.mark.parametrize('limit',[1,2,5,8])
def test_filtered_pagination_has_no_skips_gaps_or_raw_offsets(mixed_facts,limit):
    client,_,_,_,_,body=mixed_facts
    complete=client.post(PATH,**signed({**body,'encoding':ENCODING})).json()
    rows=[];offset=0
    while True:
        response=client.post(PATH,**signed({**body,'encoding':ENCODING,'offset':offset,
            'limit':limit,'version':complete['version']}))
        assert response.status_code==200,response.text
        page=response.json()
        assert page['total']==complete['total'] and page['datasets']==complete['datasets']
        assert page['offset']==offset and len(page['rows'])<=limit
        rows.extend(page['rows'])
        if page['next_offset'] is None:
            assert not page['truncated']
            break
        assert page['next_offset']==offset+len(page['rows']) and page['truncated']
        offset=page['next_offset']
    assert rows==complete['rows']
    empty=client.post(PATH,**signed({**body,'encoding':ENCODING,'offset':99,'version':complete['version']})).json()
    assert empty['rows']==[] and empty['total']==5 and empty['next_offset'] is None and not empty['truncated']


def test_compact_pagination_crosses_datasets_and_retains_each_raw_count(paged_facts):
    client,store,parsed,path,review,body=paged_facts
    extra=copy.deepcopy(parsed);extra['rule_version']='synthetic-compact-extra-source'
    extra['series']=extra['series'][:1];identifier=extra['series'][0]['series_id']
    extra['observations']=[
        {'series_id':identifier,'date':'2026-02-01','value':0,'value_status':'valid','source_cell':'B2','date_cell':'A2','raw_value':'0'},
        {'series_id':identifier,'date':'2026-02-02','value':None,'value_status':'invalid','source_cell':'B3','date_cell':'A3','raw_value':'#N/A'},
        {'series_id':identifier,'date':'2026-02-03','value':8,'value_status':'ok','source_cell':'B4','date_cell':'A4','raw_value':'8'},
        {'series_id':identifier,'date':None,'value':7,'value_status':'valid','source_cell':'B5','date_cell':'A5','raw_value':'7'},
    ]
    extra_review=copy.deepcopy(review)
    extra_review.update(import_id='2'*24,dataset_id=store.publish(extra)['dataset_id'])
    extra_review['native_source_bindings'][0]['source_knowledge_id']='original-b'
    extra_path=path.parent.parent/('2'*24)/'review.json';extra_path.parent.mkdir()
    extra_path.write_text(json.dumps(extra_review))
    body['sources'].append({**body['sources'][0],'knowledge_id':'original-b'})
    legacy=client.post(PATH,**signed(body)).json()
    compact=client.post(PATH,**signed({**body,'encoding':ENCODING})).json()
    _equivalent_rows(compact,legacy)
    assert [row['raw_observation_count'] for row in compact['datasets']]==[14,4]
    rows=[]
    for offset in range(0,compact['total'],3):
        page=client.post(PATH,**signed({**body,'encoding':ENCODING,'offset':offset,
            'limit':3,'version':compact['version']}))
        assert page.status_code==200,page.text
        rows.extend(page.json()['rows'])
    assert rows==compact['rows']
    assert {row['source_knowledge_id'] for row in _decoded(compact)}=={'original-a','original-b'}


def test_same_dataset_bound_to_two_real_originals_keeps_distinct_ids_and_raw_counts(facts):
    client,_,_,path,review,body=facts
    binding=copy.deepcopy(review['native_source_bindings'][0]);binding['source_knowledge_id']='original-b'
    review['native_source_bindings'].append(binding);path.write_text(json.dumps(review))
    body['sources'].append({**body['sources'][0],'knowledge_id':'original-b'})
    legacy=client.post(PATH,**signed(body)).json()
    compact=client.post(PATH,**signed({**body,'encoding':ENCODING})).json()
    _equivalent_rows(compact,legacy)
    assert compact['total']==2 and len({row[0] for row in compact['rows']})==2
    assert len({dataset['dataset_id'] for dataset in compact['datasets']})==1
    assert [dataset['raw_observation_count'] for dataset in compact['datasets']]==[2,2]


@pytest.mark.parametrize('value',[float('inf'),float('-inf'),float('nan')])
@pytest.mark.parametrize('status',['valid','invalid'])
@pytest.mark.parametrize('when',['2026-01-03',None])
def test_compact_never_silently_discards_nonfinite_raw_values(facts,value,status,when):
    client,store,parsed,path,review,body=facts
    parsed=copy.deepcopy(parsed);parsed['rule_version']='synthetic-compact-nonfinite'
    parsed['observations'].append({'series_id':'quantity','date':when,'value':value,
        'value_status':status,'source_cell':'B4','date_cell':'A4','raw_value':str(value)})
    review['dataset_id']=store.publish(parsed)['dataset_id'];path.write_text(json.dumps(review))
    assert client.post(PATH,**signed(body)).status_code==400
    response=client.post(PATH,**signed({**body,'encoding':ENCODING}))
    assert response.status_code==503 and '非有限' in response.json()['detail']
    assert client.post(PATH,**signed({**body,'metadata_only':True})).status_code==200


@pytest.mark.parametrize('encoding',[None,False,True,0,'','research-rows-v2',[],{}])
def test_compact_encoding_is_explicit_and_strict(facts,encoding):
    client,_,_,_,_,body=facts
    assert client.post(PATH,**signed({**body,'encoding':encoding})).status_code==400


def test_metadata_probe_keeps_legacy_version_and_never_scans_rows(facts,monkeypatch):
    client,_,_,_,_,body=facts
    compact=client.post(PATH,**signed({**body,'encoding':ENCODING})).json()
    def forbidden(*args,**kwargs):
        raise AssertionError('metadata-only probe scanned observations')
    monkeypatch.setattr(DataStore,'_connect',forbidden)
    probe=client.post(PATH,**signed({**body,'metadata_only':True}))
    assert probe.status_code==200 and probe.json()['version']==compact['version']
    assert set(probe.json())=={'knowledge_base_id','knowledge_base_tenant_id','version','metadata_only'}
    assert client.post(PATH,**signed({**body,'metadata_only':True,'encoding':ENCODING})).status_code==400


def test_filtered_protocol_still_enforces_limit_against_all_raw_rows(facts,monkeypatch):
    client,_,_,_,_,body=facts
    @contextmanager
    def over_limit(self,dataset_id):
        class Connection:
            def execute(self,sql,parameters):
                assert sql.startswith('SELECT series_id,count(*)') and 'isfinite' in sql
                return self
            def fetchall(self):
                return [('quantity',2_000_001,1,0)]
        yield Connection()
    monkeypatch.setattr(DataStore,'_connect',over_limit)
    response=client.post(PATH,**signed({**body,'encoding':ENCODING}))
    assert response.status_code==413 and '200万' in response.json()['detail']


def test_compact_requests_keep_fresh_source_confirmation_and_version_checks(facts):
    client,store,parsed,path,review,body=facts
    compact=client.post(PATH,**signed({**body,'encoding':ENCODING})).json()
    request={**body,'encoding':ENCODING,'version':compact['version']}
    review['sheets'][0]['status']='needs_review';path.write_text(json.dumps(review))
    assert client.post(PATH,**signed(request)).status_code==409
    withdrawn=client.post(PATH,**signed({**body,'encoding':ENCODING})).json()
    assert withdrawn['rows']==[] and withdrawn['total']==0
    review['sheets'][0]['status']='confirmed';path.write_text(json.dumps(review))
    original=store._path(review['dataset_id'])/'source.xlsx';original.write_bytes(b'tampered')
    assert client.post(PATH,**signed(request)).status_code==409
