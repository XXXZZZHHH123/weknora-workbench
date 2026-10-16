"""Page-range optimization must retain the canonical full-sort result."""
import copy
import json
from contextlib import contextmanager

import pytest

from structured_data.research_facts import _page_series
from structured_data.store import DataStore
from test_knowledge_research_facts import PATH, facts, signed


@pytest.fixture
def paged_facts(facts):
    client,store,parsed,path,review,body=facts
    parsed=copy.deepcopy(parsed)
    parsed['rule_version']='synthetic-page-ranges'
    parsed['series']=[];parsed['observations']=[]
    # Deliberately unordered insertion, a zero-row catalog field, a withdrawn
    # field, null dates, zero values, and missing/error observations.
    for field_index,identifier in enumerate(('field-z','field-a','field-空','field-b','field-empty','hidden')):
        parsed['series'].append({'series_id':identifier,'metric':identifier,'unit':'kg',
            'frequency':'日','date_mode':'date','sheet':'Hidden' if identifier=='hidden' else 'Records',
            'header_cells':['B1'],'dimensions':{}})
        if identifier=='field-empty':
            continue
        for row_index in reversed(range(field_index+2)):
            parsed['observations'].append({'series_id':identifier,
                'date':f'2026-01-{row_index+1:02}' if row_index else None,
                'value':None if row_index==1 else (0 if row_index==0 else row_index),
                'value_status':'excel_error' if row_index==1 else 'valid',
                'source_cell':f'B{field_index*20+row_index+2}',
                'date_cell':f'A{field_index*20+row_index+2}',
                'raw_value':'#N/A' if row_index==1 else str(0 if row_index==0 else row_index)})
    dataset=store.publish(parsed)['dataset_id']
    review['dataset_id']=dataset;path.write_text(json.dumps(review))
    return client,store,parsed,path,review,body


@pytest.mark.parametrize('offset,limit',[(0,1),(0,5),(1,1),(1,7),(2,3),(8,2),(12,7),(14,1),(99,4)])
def test_page_series_ranges_match_global_order_with_empty_fields(offset,limit):
    identifiers=['a','b','empty','z'];counts={'a':3,'b':7,'z':4}
    global_rows=[(name,row) for name in identifiers for row in range(counts.get(name,0))]
    selected,local_offset=_page_series(identifiers,counts,offset,limit)
    local_rows=[(name,row) for name in selected for row in range(counts.get(name,0))]
    assert local_rows[local_offset:local_offset+limit]==global_rows[offset:offset+limit]


def test_all_http_pages_match_canonical_full_sql_sort_and_keep_provenance(paged_facts,monkeypatch):
    client,store,parsed,path,review,body=paged_facts
    confirmed_ids=sorted(field['series_id'] for field in parsed['series'] if field['sheet']=='Records')
    with store._connect(review['dataset_id']) as connection:
        cursor=connection.execute('SELECT * FROM observations WHERE series_id IN ('+
            ','.join('?' for _ in confirmed_ids)+') ORDER BY series_id,date NULLS LAST,source_cell',confirmed_ids)
        names=[column[0] for column in cursor.description]
        expected=[dict(zip(names,row)) for row in cursor.fetchall()]
    queries=[];original_connect=DataStore._connect
    class LoggingConnection:
        def __init__(self,connection):
            self.connection=connection
        def execute(self,sql,parameters=None):
            queries.append((sql,list(parameters or [])))
            return self.connection.execute(sql,parameters or [])
    @contextmanager
    def tracked_connect(self,dataset_id):
        with original_connect(self,dataset_id) as connection:
            yield LoggingConnection(connection)
    monkeypatch.setattr(DataStore,'_connect',tracked_connect)
    actual=[];offset=0;version=None
    while True:
        request={**body,'offset':offset,'limit':3}
        if version is not None:
            request['version']=version
        response=client.post(PATH,**signed(request))
        assert response.status_code==200,response.text
        page=response.json();version=page['version']
        assert page['total']==len(expected)
        actual.extend(page['rows'])
        if page['next_offset'] is None:
            break
        offset=page['next_offset']
    assert len(actual)==len(expected)
    for actual_row,expected_row in zip(actual,expected):
        assert actual_row['source_series_id']==expected_row['series_id']
        assert actual_row['source_knowledge_id']=='original-a'
        assert actual_row['dataset_id']==review['dataset_id']
        for name in names:
            if name=='series_id':
                continue
            expected_value=expected_row[name]
            if name=='date' and expected_value is not None:
                expected_value=expected_value.isoformat()
            assert actual_row[name]==expected_value
    assert any(row['value']==0 for row in actual)
    assert any(row['value'] is None and row['raw_value']=='#N/A' for row in actual)
    assert any(row['date'] is None for row in actual)
    row_queries=[parameters for sql,parameters in queries if sql.startswith('SELECT *')]
    assert row_queries and all(len(parameters[:-2])<len(confirmed_ids) for parameters in row_queries)
    assert all('hidden' not in parameters for _,parameters in queries)
    beyond=client.post(PATH,**signed({**body,'offset':999,'limit':3,'version':version})).json()
    assert beyond['rows']==[] and beyond['next_offset'] is None and not beyond['truncated']


def test_next_page_revalidates_replacement_withdrawal_catalog_and_original(paged_facts):
    client,store,parsed,path,review,body=paged_facts
    initial=client.post(PATH,**signed({**body,'limit':3})).json()
    request={**body,'offset':3,'limit':3,'version':initial['version']}
    # Replacing the published import cannot combine old and new pages.
    replacement=copy.deepcopy(parsed);replacement['rule_version']='synthetic-replacement'
    replacement['observations'][0]['value']=732
    review['dataset_id']=store.publish(replacement)['dataset_id'];path.write_text(json.dumps(review))
    assert client.post(PATH,**signed(request)).status_code==409
    review['dataset_id']=initial['datasets'][0]['dataset_id'];path.write_text(json.dumps(review))
    assert client.post(PATH,**signed(request)).status_code==200
    # The confirmation check occurs before any page-range count optimization.
    review['sheets'][0]['status']='needs_review';path.write_text(json.dumps(review))
    assert client.post(PATH,**signed(request)).status_code==409
    assert client.post(PATH,**signed(body)).json()['total']==0
    review['sheets'][0]['status']='confirmed';path.write_text(json.dumps(review))
    catalog_path=store._path(review['dataset_id'])/'series.json'
    catalog=json.loads(catalog_path.read_text());catalog[0]['unit']='g';catalog_path.write_text(json.dumps(catalog))
    assert client.post(PATH,**signed(request)).status_code==409
    original=store._path(review['dataset_id'])/'source.xlsx';original.write_bytes(b'tampered')
    assert client.post(PATH,**signed(body)).status_code==409


def test_page_crosses_datasets_and_skips_confirmed_zero_row_catalog(paged_facts):
    client,store,parsed,path,review,body=paged_facts
    initial=client.post(PATH,**signed(body)).json()
    for index,(source_name,count) in enumerate((('original-b',0),('original-c',2)),start=2):
        extra=copy.deepcopy(parsed);extra['rule_version']=f'synthetic-page-dataset-{index}'
        extra['series']=extra['series'][:1]
        extra['observations']=extra['observations'][:count]
        dataset=store.publish(extra)['dataset_id']
        extra_review=copy.deepcopy(review)
        extra_review.update(import_id=str(index)*24,dataset_id=dataset)
        extra_review['native_source_bindings'][0]['source_knowledge_id']=source_name
        extra_path=path.parent.parent/(str(index)*24)/'review.json'
        extra_path.parent.mkdir();extra_path.write_text(json.dumps(extra_review))
        body['sources'].append({**body['sources'][0],'knowledge_id':source_name})
    response=client.post(PATH,**signed({**body,'offset':initial['total']-1,'limit':4}))
    assert response.status_code==200,response.text
    page=response.json()
    assert page['total']==initial['total']+2
    assert [row['source_knowledge_id'] for row in page['rows']]==['original-a','original-c','original-c']
    assert page['next_offset'] is None and not page['truncated']


def test_row_counts_come_from_snapshot_not_catalog_summaries(paged_facts):
    client,store,parsed,path,review,body=paged_facts
    initial=client.post(PATH,**signed(body)).json()
    catalog_path=store._path(review['dataset_id'])/'series.json'
    catalog=json.loads(catalog_path.read_text())
    for field in catalog:
        field['record_count']=2_000_001
    catalog_path.write_text(json.dumps(catalog))
    response=client.post(PATH,**signed({**body,'offset':3,'limit':4}))
    assert response.status_code==200,response.text
    assert response.json()['total']==initial['total']
    assert response.json()['rows']==initial['rows'][3:7]
    assert response.json()['version']!=initial['version']
