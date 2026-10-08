from datetime import datetime
from copy import deepcopy
from pathlib import Path
import os
import pytest
from openpyxl import Workbook, load_workbook
from structured_data.scanner import scan_workbook
from structured_data.generic_parser import parse_mapped_workbook
from structured_data.store import DataStore


def source(tmp_path):
    p=tmp_path/'mixed.xlsx';w=Workbook();s=w.active;s.title='混排表'
    s.append(['日期','主表价格',None,None,'2018年05月11日'])
    s.append([datetime(2018,3,28),900,None,None,'类别','成交量（吨）'])
    s.append([datetime(2018,3,29),901,None,None,'新疆棉',9345.68])
    s.append([datetime(2018,3,30),902,None,None,'地产棉',15816.81])
    s.append([datetime(2018,4,1),903,None,None,'总计',25162.48])
    w.save(p)
    scan=scan_workbook(p);fp=scan['sheets'][0]['fingerprint']
    mapping={'sheet':s.title,'fingerprint':fp,'blocks':[
        {'name':'主表','date_mode':'date','date_column':'A','header_rows':[1],'data_start_row':2,'data_end_row':5,
         'columns':[{'column':'B','metric':'价格','unit':None,'header_cells':['B1']}]},
        {'name':'成交小表','date_mode':'shared_cell','date_cell':'E1','category_column':'E','category_name':'类别',
         'header_rows':[2],'data_start_row':3,'data_end_row':5,
         'columns':[{'column':'F','metric':'成交量','unit':'吨','header_cells':['F2']}]}]}
    return p,mapping


def test_shared_date_categories_and_published_provenance(tmp_path):
    p,m=source(tmp_path);parsed=parse_mapped_workbook(p,[m]);store=DataStore(tmp_path/'data')
    r=store.publish(parsed);rows=store.list_series(r['dataset_id'],search='新疆棉')['series']
    assert len(rows)==1
    q=store.query_series(r['dataset_id'],rows[0]['series_id'],'2018-05-11','2018-05-11')
    assert q['rows'][0]['value']==9345.68
    assert q['rows'][0]['source_cell']=='F3'
    assert q['rows'][0]['date_cell']=='E1'
    assert q['rows'][0]['category_cell']=='E3'
    assert store.query_series(r['dataset_id'],rows[0]['series_id'],'2018-03-29','2018-03-29')['status']=='no_data'
    assert len(store.list_series(r['dataset_id'],search='成交量')['series'])==3


@pytest.mark.parametrize('field,value,match',[
    ('date_cell','', '日期单元格'),('date_cell','E99','日期单元格'),
    ('date_cell','E2','不是完整日期'),('data_end_row',None,'结束行'),
    ('category_column','F','类别列')])
def test_invalid_shared_mapping_is_blocked(tmp_path,field,value,match):
    p,m=source(tmp_path);m['blocks'][1][field]=value
    with pytest.raises(ValueError,match=match):parse_mapped_workbook(p,[m])


def test_duplicate_categories_block_instead_of_adding(tmp_path):
    p,m=source(tmp_path);w=load_workbook(p);w.active['E4']='新疆棉';w.save(p)
    with pytest.raises(ValueError,match='重复日期'):parse_mapped_workbook(p,[m])


def test_nested_header_drift_rejects_template(tmp_path):
    p,m=source(tmp_path);m['blocks'][1]['header_expectations']={'F2':'成交量（吨）'}
    w=load_workbook(p);w.active['F2']='库存（吨）';w.save(p)
    with pytest.raises(ValueError,match='区块表头已变化'):parse_mapped_workbook(p,[m])


def test_shared_template_requires_review_when_source_changes(tmp_path):
    from structured_data.imports import ImportManager
    p,m=source(tmp_path);manager=ImportManager(tmp_path/'data')
    initial=manager.ingest(p)
    preview=manager.preview(initial['import_id'],m)
    assert len(preview['blocks'][1]['sample'])==3
    confirmed=manager.confirm(initial['import_id'],m)
    assert manager.ingest(p)['dataset_id']==confirmed['dataset_id']
    changed=tmp_path/'changed.xlsx';w=load_workbook(p)
    w.active['E1']='2018年05月12日';w.save(changed)
    fresh=manager.ingest(changed)
    sheet=manager.sheet(fresh['import_id'],'混排表')
    assert sheet['status']=='needs_review'
    assert sheet['template_status'] in ('source_changed','layout_changed')


@pytest.mark.skipif(os.getenv('COTTON_ACCEPTANCE')!='1',reason='Original workbook opt-in')
def test_real_auction_mixed_regions_source_checked(tmp_path):
    p=Path('/Users/mac/Documents/中哲物产/棉花测试集/棉花/棉花数据库(2).xlsx')
    scan=scan_workbook(p);sheet=next(s for s in scan['sheets'] if s['name']=='2018年抛储')
    assert sheet['region_profile']=='cotton-auction-regions-v1'
    parsed=parse_mapped_workbook(p,[sheet['mapping']],scan=scan)
    assert len(parsed['observations'])==4509
    bycell={x['source_cell']:x for x in parsed['observations']}
    assert bycell['AH17']['value']==9345.68
    assert bycell['AH17']['date']=='2018-05-11' and bycell['AH17']['date_cell']=='AG15'
    assert bycell['AH17']['category_cell']=='AG17' and bycell['AH17']['category']=='新疆棉'
    assert bycell['AH18']['value']==15816.81 and bycell['AH18']['date']=='2018-05-11'
    assert bycell['AH19']['value']==25162.48 and bycell['AH19']['category']=='总计'
    assert bycell['AG147']['value']==pytest.approx(2749431.3816999993)
    assert bycell['AG147']['date_cell']=='A147'
    for excluded in ['AG10','AG22','AG28','AG58','AH107','D154']:
        assert excluded not in bycell
    unsafe=deepcopy(sheet['mapping']);unsafe['blocks'][1]['data_start_row']=5
    with pytest.raises(ValueError,match='独立小表'):parse_mapped_workbook(p,[unsafe])
