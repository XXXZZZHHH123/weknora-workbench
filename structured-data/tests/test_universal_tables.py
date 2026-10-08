"""Business meaning survives parse -> publish -> query for non-cotton tables."""
from copy import deepcopy
import os
from pathlib import Path
import pytest
from openpyxl import Workbook
from structured_data.scanner import scan_workbook
from structured_data.generic_parser import parse_mapped_workbook
from structured_data.store import DataStore
from structured_data.imports import ImportManager


def make(tmp_path,rows):
    p=tmp_path/'business.xlsx';w=Workbook();w.active.title='业务数据'
    for row in rows:w.active.append(row)
    w.save(p);return p


def mapping(p,mode,**extra):
    sh=scan_workbook(p)['sheets'][0]
    return {'sheet':sh['name'],'fingerprint':sh['fingerprint'],'blocks':[dict(date_mode=mode,date_column='A',header_rows=[1],data_start_row=2,data_end_row=3,columns=[{'column':'B','metric':'数量','unit':'件'}],**extra)]}


def publish(tmp_path,p,m):
    parsed=parse_mapped_workbook(p,[m]);s=DataStore(tmp_path/'store');r=s.publish(parsed)
    return s,r['dataset_id'],parsed


@pytest.mark.parametrize('text,fmt,expected',[
    ('22/09/2023','DD/MM/YYYY','2023-09-22'),('09/22/2023','MM/DD/YYYY','2023-09-22'),
    (20230922,'YYYYMMDD','2023-09-22'),('2023.9.2','YYYY.MM.DD','2023-09-02'),
    ('02/03/2023','DD/MM/YYYY','2023-03-02')])
def test_manual_date_formats(tmp_path,text,fmt,expected):
    p=make(tmp_path,[['日期','数量'],[text,9],[text,10]])
    m=mapping(p,'custom',date_format=fmt);m['blocks'][0]['data_end_row']=2
    s,i,r=publish(tmp_path,p,m);q=s.query_series(i,r['series'][0]['series_id'],expected,expected)
    assert q['rows'][0]['date']==expected and q['rows'][0]['value']==9
    assert q['rows'][0]['date_cell']=='A2'


@pytest.mark.parametrize('text,fmt',[('31/02/2023','DD/MM/YYYY'),('2023-09','YYYY-MM'),('09/22/23','MM/DD/YY')])
def test_invalid_or_partial_date_is_not_invented(tmp_path,text,fmt):
    p=make(tmp_path,[['日期','数量'],[text,9],[text,10]])
    with pytest.raises(ValueError):parse_mapped_workbook(p,[mapping(p,'custom',date_format=fmt)])


def test_undated_categories_and_numeric_ids(tmp_path):
    p=make(tmp_path,[['产品编号','数量'],[1001,9],[1002,12]])
    m=mapping(p,'none',category_column='A',category_name='产品编号')
    s,i,r=publish(tmp_path,p,m);series=s.list_series(i,search='1001')['series'][0]
    q=s.query_series(i,series['series_id'])
    assert q['rows'][0]['date'] is None and q['rows'][0]['value']==9 and q['rows'][0]['category_cell']=='A2'
    with pytest.raises(ValueError,match='具体日期'):s.query_series(i,series['series_id'],'2023-01-01')
    with pytest.raises(ValueError,match='最新'):s.query_series(i,series['series_id'],operation='latest')
    # No category is also valid; retain separate rows instead of fabricating dates.
    m=mapping(p,'none');s,i,r=publish(tmp_path/'other',p,m)
    assert s.query_series(i,r['series'][0]['series_id'],operation='sum')['aggregate']==21


def test_vertical_period_labels(tmp_path):
    p=make(tmp_path,[['月份','数量'],['2023-09',9],['2023-10',12]])
    s,i,r=publish(tmp_path,p,mapping(p,'period'));sid=r['series'][0]['series_id']
    q=s.query_series(i,sid,period='2023-09');assert q['rows'][0]['value']==9 and q['rows'][0]['period_cell']=='A2'
    assert s.list_series(i)['series'][0]['periods']==['2023-09','2023-10']
    with pytest.raises(ValueError,match='先指定期间'):s.query_series(i,sid,operation='sum')


def test_non_cotton_matrix_confirm_and_versions(tmp_path):
    p=make(tmp_path,[['华东区域','2022','23/24-6月','23/24-7月'],['销售量（吨）',90,110,120],['库存（吨）',50,55,60]])
    sh=scan_workbook(p)['sheets'][0]
    assert sh['layout']=='horizontal_matrix' and sh['status']=='needs_review'
    manager=ImportManager(tmp_path/'app');before=manager.ingest(p)
    assert before['dataset_id'] is None
    preview=manager.preview(before['import_id'],sh['mapping'])
    assert preview['observation_count']==6
    done=manager.confirm(before['import_id'],sh['mapping']);s=manager.store
    series=s.list_series(done['dataset_id'],search='销售量')['series'][0]
    june=s.query_series(done['dataset_id'],series['series_id'],period='23/24-6月')['rows'][0]
    july=s.query_series(done['dataset_id'],series['series_id'],period='23/24-7月')['rows'][0]
    assert (june['value'],july['value'])==(110,120)
    assert june['date'] is None and june['period_cell']=='C1' and june['metric_cell']=='A2'
    assert s.query_series(done['dataset_id'],series['series_id'],period='23/24')['status']=='no_data'
    duplicate=deepcopy(sh['mapping']);duplicate['blocks'][0]['columns'][2]['period']='23/24-6月'
    with pytest.raises(ValueError,match='不能重复'):parse_mapped_workbook(p,[duplicate])


@pytest.mark.skipif(os.getenv('COTTON_ACCEPTANCE')!='1',reason='Original workbook opt-in')
def test_usda_period_versions_remain_separate(tmp_path):
    p=Path('/Users/mac/Documents/中哲物产/棉花测试集/棉花/棉花数据库(2).xlsx')
    scan=scan_workbook(p);sh=next(s for s in scan['sheets'] if s['name']=='USDA棉花平衡表')
    r=parse_mapped_workbook(p,[sh['mapping']],scan=scan);cells={x['source_cell']:x for x in r['observations']}
    assert cells['AG4']['value']==2541.321216 and cells['AG4']['period']=='23/24-6月'
    assert cells['AH4']['value']==pytest.approx(2543.9557248) and cells['AH4']['period']=='23/24-7月'
    assert cells['AH4']['date'] is None and cells['AH4']['period_cell']=='AH2'
    assert cells['P6']['period']=='2023' and cells['P6']['value']==116841
    assert 'AI4' not in cells and 'CW4' not in cells # changes/scenarios must not be guessed as years
    assert 'P80' in cells # a Yield value near 2000 must not end an annual block
    s=DataStore(tmp_path/'real');id=s.publish(r)['dataset_id']
    q=s.query_series(id,cells['AH4']['series_id'],period='23/24-7月')
    assert q['rows'][0]['source_cell']=='AH4' and q['rows'][0]['metric_cell']=='R4'


def test_configurable_branding_is_public_but_not_credentials(tmp_path):
    import json
    from fastapi.testclient import TestClient
    from structured_data.server import create_app
    (tmp_path/'workbench-config.json').write_text(json.dumps({'title':'综合数据平台','workspace_label':'销售部','agent_name':'数据问答','api_key':'not-public','weknora_url':'javascript:alert(1)'}))
    with TestClient(create_app(tmp_path)) as client:
        r=client.get('/api/ui-config');assert r.status_code==200
        assert r.json()['title']=='综合数据平台' and 'api_key' not in r.json()
        assert r.json()['weknora_url'].startswith('http:')


def test_shared_cell_custom_date(tmp_path):
    p=make(tmp_path,[['09/22/2023','数量'],['产品甲',9],['产品乙',10]])
    m=mapping(p,'shared_cell',date_cell='A1',date_format='MM/DD/YYYY',category_column='A')
    result=parse_mapped_workbook(p,[m])
    assert {r['date'] for r in result['observations']}=={'2023-09-22'}
