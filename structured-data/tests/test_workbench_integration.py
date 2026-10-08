from datetime import datetime
from pathlib import Path
import json
import httpx
from fastapi.testclient import TestClient
from openpyxl import Workbook
from structured_data.server import create_app
from structured_data.scanner import scan_workbook
from structured_data.generic_parser import parse_mapped_workbook
from structured_data.model_settings import read_model
from structured_data.native_charts import calendar_year


def test_merged_headers_keep_category_measure_and_evidence(tmp_path):
    w=Workbook();s=w.active;s.title='通用零售'
    s.append(['日期','销售额(亿元)',None,'销量(万件)',None,'均价(元/件)',None,'合计'])
    s.append([None,'鞋','衣','鞋','衣','鞋','衣','亿元'])
    for cell in ('B1:C1','D1:E1','F1:G1'):s.merge_cells(cell)
    s.append([datetime(2023,1,1),10,20,30,40,50,60,30])
    s.append([datetime(2023,2,1),11,21,31,41,51,61,32])
    p=tmp_path/'retail.xlsx';w.save(p)
    scan=scan_workbook(p);m=scan['sheets'][0]['mapping'];cols=m['blocks'][0]['columns']
    assert [(c['metric'],c['unit']) for c in cols[:6]]==[(a+' / '+b,u) for a,u in [('销售额(亿元)','亿元'),('销量(万件)','万件'),('均价(元/件)','元/件')] for b in ('鞋','衣')]
    assert cols[1]['header_cells']==['B1','C2']
    assert cols[-1]['metric']=='合计' and cols[-1]['unit']=='亿元'
    result=parse_mapped_workbook(p,[m],scan=scan)
    assert len(result['series'])==7
    assert next(o for o in result['observations'] if o['source_cell']=='C3')['value']==20


def test_calendar_family_not_contract_codes():
    contracts=[{'label':s} for s in ['1601','1701','1801','1901','2001','2101','2201']]
    assert all(calendar_year(c,contracts) is None for c in contracts)
    years=[{'label':s} for s in ['2021','2022','2023']]
    assert [calendar_year(c,years) for c in years]==[2021,2022,2023]
    assert calendar_year({'label':'2023年'},[])==2023
    assert calendar_year({'label':'1901'},[{'label':'1901'}]) is None


def test_shared_login_checks_identity_and_model_settings_redact_key(tmp_path,monkeypatch):
    class AuthClient:
        def __init__(self,**kw):pass
        async def __aenter__(self):return self
        async def __aexit__(self,*args):pass
        async def get(self,url,headers):
            token=headers['Authorization']
            if token=='Bearer expired':return httpx.Response(401,json={})
            return httpx.Response(200,json={'data':{'user':{'tenant_id':10001 if token=='Bearer other' else 10000}}})
    monkeypatch.setattr('httpx.AsyncClient',AuthClient)
    monkeypatch.setenv('STRUCTURED_TENANT_ID','10000')
    monkeypatch.setenv('STRUCTURED_MODEL_CONFIG',str(tmp_path/'absent'))
    with TestClient(create_app(tmp_path)) as c:
        assert c.get('/api/datasets').status_code==401
        assert c.get('/api/datasets',headers={'Authorization':'Bearer expired'}).status_code==401
        assert c.get('/api/datasets',headers={'Authorization':'Bearer other'}).status_code==403
        good={'Authorization':'Bearer valid','Origin':'http://testserver','X-Tenant-ID':'10000'}
        assert c.get('/api/datasets',headers=good).status_code==200
        assert c.get('/api/datasets',headers=good|{'X-Tenant-ID':'10001'}).status_code==403
        payload={'model':'custom-chat','base_url':'https://example.com/v1','api_key':'test-private-key'}
        assert c.post('/api/model-settings',json=payload,headers=good|{'Origin':'https://evil.invalid'}).status_code==403
        saved=c.post('/api/model-settings',json=payload,headers=good)
        assert saved.status_code==200 and 'test-private-key' not in saved.text
        assert 'api_key' not in c.get('/api/model-settings',headers=good).json()
        assert read_model(tmp_path)['api_key']=='test-private-key'
        assert (tmp_path/'model-settings.json').stat().st_mode & 0o777==0o600
        payload.update(api_key='',model='changed-model')
        assert c.post('/api/model-settings',json=payload,headers=good).status_code==200
        assert read_model(tmp_path)['model']=='changed-model' and read_model(tmp_path)['api_key']=='test-private-key'
