from datetime import datetime
from hashlib import sha256
import json

from openpyxl import Workbook
import pytest

from structured_data.store import DataStore
from structured_data.source_context import formula_references, protect_duplicate_bindings, verify_selection
from structured_data.field_resolver import FieldResolver


@pytest.fixture
def duplicate_source(tmp_path):
    book=Workbook();sheet=book.active;sheet.title='原料'
    sheet.append(['日期','3128级棉花','20天原料','双29棉花','20天原料','CC Index 3128B','20天原料','双29棉花','20天原料'])
    sheet.append([None,*['元/吨']*8]);sheet.merge_cells('B1:B1')
    sheet.append([datetime(2023,1,1),100,'=B3',130,'=D3',200,'=F3',230,'=H3'])
    path=tmp_path/'input.xlsx';book.save(path)
    series=[];observations=[]
    for col,metric,value,formula in [('B','3128级棉花',100,None),('C','20天原料',100,'=B3'),('D','双29棉花',130,'=B3+30'),('E','20天原料',130,'=D3'),('F','CC Index 3128B',200,None),('G','20天原料',200,'=F3'),('H','双29棉花',230,'=F3+30'),('I','20天原料',230,'=H3')]:
        series.append(dict(series_id=col,sheet='原料',metric=metric,unit='元/吨',dimensions={'原表列':col},header_cells=[col+'1',col+'2'],date_mode='date'))
        observations.append(dict(series_id=col,date='2023-01-01',value=value,formula=formula,source_cell=col+'3',date_cell='A3',raw_value=value,value_status='valid'))
    store=DataStore(tmp_path/'data')
    published=store.publish(dict(source_file=str(path),sha256=sha256(path.read_bytes()).hexdigest(),rule_version='fixture',series=series,observations=observations,sheets=[],excluded_regions=[],issues=[]))
    return store,published['dataset_id']


def test_context_keeps_actual_headers_recursive_formula_and_original(duplicate_source):
    store,dataset=duplicate_source;before=store.manifest(dataset);catalog=store.catalog(dataset)
    context=next(s['source_context'] for s in catalog if s['series_id']=='E')
    parent=context['formula_source']['references'][0]['fields'][0]
    assert parent['metric']=='双29棉花'
    assert parent['source_context']['formula_source']['references'][0]['fields'][0]['metric']=='3128级棉花'
    assert context['headers'][0]=={'cell':'E1','value':'20天原料'}
    assert store.catalog(dataset)==catalog
    assert store.manifest(dataset)==before
    assert len(store.list_datasets())==1


def test_formula_reference_tokens_do_not_follow_external_or_text():
    refs=formula_references('=SUM($B$2:C9)+\'另一张表\'!D3+"E4"+\'[other.xlsx]Sheet\'!F1','本表')
    assert [(r['sheet'],r['columns']) for r in refs]==[('本表',['B','C']),('另一张表',['D'])]
    assert formula_references('=INDIRECT("B3")','本表')==[]


@pytest.mark.parametrize('question,expected',[
    ('20天原料',['C','E','G','I']),('双29的20天原料',['E','I']),
    ('3128级棉花的20天原料',['C']),('CC Index 3128B的20天原料',['G']),
    ('CC Index 3128B那套双29棉花的20天原料',['I']),('20天原料，原表C列',['C']),
])
def test_model_singleton_cannot_erase_source_ambiguity(duplicate_source,question,expected):
    store,dataset=duplicate_source;catalog=store.catalog(dataset)
    result=protect_duplicate_bindings({'decision':'resolved','fields':[{'role':'v1','candidate_ids':['C']}]},catalog,question)
    assert result['fields'][0]['candidate_ids']==expected
    blocked=verify_selection(store,dataset,['C'],question)
    assert bool(blocked)==(expected!=['C'])
    if blocked:assert blocked['status']=='needs_clarification' and 'rows' not in blocked


def test_resolver_does_not_execute_first_duplicate(duplicate_source):
    store,dataset=duplicate_source
    resolver=FieldResolver(store,lambda p:{'intent':'lookup','decision':'resolved','fields':[{'role':'v1','label':'原料','candidate_ids':['C']}],'query':{'operation':'rows'}})
    plan=resolver.resolve(dataset,'20天原料',date='2023-01-01')
    assert plan['status']=='needs_clarification'
    assert {c['series_id'] for c in plan['fields'][0]['candidates']}=={'C','E','G','I'}
    with pytest.raises(ValueError,match='尚未确认'):resolver.execute(plan['resolution_id'])


def test_signed_query_and_sql_cannot_choose_a_duplicate(duplicate_source, monkeypatch):
    from fastapi.testclient import TestClient
    from structured_data.server import create_app
    from test_scoped_service import signed
    store,dataset=duplicate_source
    key=b'actual-source-selection-guard-1234567890'
    path=store.root/'read-key';path.write_bytes(key)
    monkeypatch.setenv('STRUCTURED_SERVICE_TOKEN_FILE',str(path));monkeypatch.setenv('STRUCTURED_TENANT_ID','10000')
    with TestClient(create_app(store.root)) as client:
        for operation,args in [('query_series',{'series_id':'C'}),('analyze_table',{'series_ids':['C'],'sql':'SELECT sum(value) FROM data'}),('compare_series',{'left_series_id':'C','right_series_id':'G','date':'2023-01-01'}),('convert_price',{'price_series_id':'C','fx_series_id':'B','date':'2023-01-01'})]:
            raw,headers=signed(key,body={'operation':operation,'arguments':{'dataset_id':dataset,'source_question':'20天原料',**args}})
            response=client.post('/api/tools/read',content=raw,headers=headers)
            assert response.status_code==200,response.text
            assert response.json()['status']=='needs_clarification'
            assert 'rows' not in response.json() and 'aggregate' not in response.json()
        raw,headers=signed(key,body={'operation':'query_series','arguments':{'dataset_id':dataset,'series_id':'G','source_question':'CC Index 3128B的20天原料'}})
        assert client.post('/api/tools/read',content=raw,headers=headers).json()['rows'][0]['value']==200


def test_merged_header_anchor_and_scope_cache(duplicate_source,tmp_path):
    from structured_data.source_context import _headers
    import zipfile,time
    book=Workbook();w=book.active;w.title='分组'
    w.append([None,'来源组',None]);w.merge_cells('B1:C1');w.append(['日期','原料','20天原料'])
    path=tmp_path/'merged.xlsx';book.save(path)
    with zipfile.ZipFile(path) as archive:
        result=_headers(archive,{'分组'},{'分组':{1,2}},time.monotonic()+10)
    assert result['分组']['merged'][0]['anchor']=='B1'
    assert result['分组']['merged'][0]['last_column']==3
    assert result['分组']['cells']['B1']=='来源组'
    store,dataset=duplicate_source
    assert len(store.catalog(dataset,sheet='原料'))==len(store.catalog(dataset))


def test_initial_catalog_recalls_all_literal_duplicate_headers(duplicate_source,monkeypatch):
    from fastapi.testclient import TestClient
    from structured_data.server import create_app
    from test_scoped_service import signed
    store,dataset=duplicate_source;key=b'literal-field-discovery-key-1234567890'
    path=store.root/'read-key';path.write_bytes(key)
    monkeypatch.setenv('STRUCTURED_SERVICE_TOKEN_FILE',str(path));monkeypatch.setenv('STRUCTURED_TENANT_ID','10000')
    with TestClient(create_app(store.root)) as client:
        raw,headers=signed(key,body={'operation':'list_datasets','arguments':{'question':'20天原料'}})
        result=client.post('/api/tools/read',content=raw,headers=headers).json()
        assert {f['series_id'] for f in result['literal_fields']}=={'C','E','G','I'}
        assert result['literal_field_count']==4
        assert all(f['source_context']['formula_source'] for f in result['literal_fields'])
        assert not result['literal_fields_truncated']
        assert 'rows' not in result
