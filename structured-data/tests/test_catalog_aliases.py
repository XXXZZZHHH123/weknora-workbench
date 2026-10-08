import json
import pytest
from structured_data.store import DataStore
from structured_data.catalog_search import aliases_for


@pytest.fixture
def catalogue(tmp_path):
    store=DataStore(tmp_path)
    series=[dict(series_id=sid,sheet=sheet,metric=metric,unit=unit,dimensions={},header_cells=['B1'])
        for sid,sheet,metric,unit in [
            ('fx-other','另一张表','美元兑人民币',None),
            ('fx-context','商品报价','美元兑人民币',None),
            ('reverse','商品报价','人民币兑美元',None),
            ('eur','其他报价','欧元兑人民币',None),
            ('price','商品报价','美国 EMOT M 1-1/8','美分/磅'),
            ('usd-price','商品报价','美元价格','美元/吨')]]
    rows=[dict(series_id=s['series_id'],date='2023-09-07',value=7.3285,source_cell='B2',formula=None,value_status='valid',date_cell='A2',raw_value=7.3285) for s in series]
    next(r for r in rows if r['series_id']=='price').update(value=99.7,raw_value=99.7)
    parsed=dict(source_file='test.xlsx',sha256='a'*64,rule_version='v1',series=series,observations=rows,sheets=[],excluded_regions=[],issues=[])
    dataset=store.publish(parsed)['dataset_id']
    path=store._path(dataset)/'manifest.json';manifest=json.loads(path.read_text())
    manifest['coverage']=[{'name':'汇率','status':'needs_review','reasons':['未确认']}, {'name':'USDA全球','status':'needs_review','reasons':[]}]
    path.write_text(json.dumps(manifest))
    return store,dataset


def test_no_hardcoded_currency_aliases(catalogue):
    store,dataset=catalogue
    assert not store.list_series(dataset,'USD/CNY')['series']
    assert not aliases_for({'metric':'美元兑人民币'})
    assert store.list_series(dataset,'汇率')['match_mode']=='pending_scope'
    result=store.list_series(dataset,'美元兑人民币',context_sheet='商品报价')
    assert result['series'][0]['series_id']=='fx-context'


def test_explicit_sheet_filter_still_blocks_pending(catalogue):
    store,dataset=catalogue
    r=store.list_series(dataset,'汇率',sheet='汇率',context_sheet='商品报价')
    assert r['match_mode']=='pending_scope' and not r['series']
    assert not store.list_series(dataset,'汇率',sheet='不存在')['series']
    assert store.list_series(dataset,'USDA 全球 产量')['match_mode']=='pending_scope'


def test_only_explicit_aliases():
    assert aliases_for({'metric':'欧元兑人民币'})==[]
    assert aliases_for({'metric':'期末结存','aliases':['库存余额']})==['库存余额']


def test_alias_pagination_preserves_context_order(catalogue):
    store,dataset=catalogue
    r=store.list_series(dataset,'美元兑人民币',limit=1,context_sheet='商品报价')
    assert r['truncated'] and r['next_offset']==1
    nextpage=store.list_series(dataset,'美元兑人民币',limit=1,offset=1,context_sheet='商品报价')
    assert r['series'][0]['series_id']!=nextpage['series'][0]['series_id']


def test_mcp_catalog_and_alias_search(catalogue):
    from fastapi.testclient import TestClient
    from structured_data.server import create_app
    store,dataset=catalogue
    app=create_app(store.root)
    with TestClient(app) as client:
        headers={'Host':'localhost:18082','Accept':'application/json, text/event-stream',
                 'Authorization':'Bearer '+(store.root/'service-token').read_text().strip()}
        def call(name,args):
            result=client.post('/mcp',headers=headers,json={'jsonrpc':'2.0','id':1,'method':'tools/call',
                'params':{'name':name,'arguments':args}}).json()['result']
            assert not result.get('isError')
            return json.loads(result['content'][0]['text'])
        catalog=call('list_datasets',{})
        assert catalog['datasets'][0]['dataset_id']==dataset
        assert catalog['datasets'][0]['is_latest_for_source']
        result=call('list_series',{'dataset_id':dataset,'search':'美元兑人民币','context_sheet':'商品报价'})
        assert result['series'][0]['series_id']=='fx-context'
        assert call('list_series',{'dataset_id':dataset,'search':'汇率','sheet':'汇率'})['match_mode']=='pending_scope'
        result=call('convert_price',{'dataset_id':dataset,'price_series_id':'price','fx_series_id':'fx-context','date':'2023-09-07'})
        assert result['rounded_value']=='16108.11' and result['unit']=='元/吨'


def test_decimal_conversion_and_source_provenance(catalogue):
    store,dataset=catalogue
    result=store.convert_price(dataset,'price','fx-context','2023-09-07','元/磅')
    assert result['value_decimal']=='7.3065145'
    assert result['price']['series']['metric']=='美国 EMOT M 1-1/8'
    assert result['exchange_rate']['series']['metric']=='美元兑人民币'
    assert result['exchange_rate']['rows'][0]['source_cell']=='B2'
    tonne=store.convert_price(dataset,'price','fx-context','2023-09-07')
    assert tonne['value']==pytest.approx(16108.107153566096)
    assert tonne['rounded_value']=='16108.11'


@pytest.mark.parametrize('price,fx,day,unit',[
    ('price','reverse','2023-09-07','元/吨'),('price','eur','2023-09-07','元/吨'),
    ('fx-context','fx-other','2023-09-07','元/吨'),('price','fx-context','2024-01-01','元/吨'),
    ('price','fx-context','2023-09-07','美元/吨'),('price','fx-context',None,'元/吨'),
])
def test_conversion_refuses_ambiguous_direction_units_or_missing_data(catalogue,price,fx,day,unit):
    store,dataset=catalogue
    with pytest.raises(ValueError):store.convert_price(dataset,price,fx,day,unit)
