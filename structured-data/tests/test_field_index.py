import json
import pytest
from structured_data.field_index import FieldIndex, exact_lookup
from structured_data.field_resolver import FieldResolver
from test_catalog_aliases import catalogue
from test_field_resolver import proposal


def test_literal_unique_lookup_skips_model_and_keeps_missing_date(catalogue):
    store,ds=catalogue
    def forbidden(_):raise AssertionError('Literal unique field must not call model')
    resolver=FieldResolver(store,forbidden)
    present=resolver.answer('2023/9/7美国 EMOT M 1-1/8价格是多少',dataset_id=ds)
    assert present['retrieval']['model_calls']==0
    assert present['answer']['sources']['v1']['rows'][0]['value']==99.7
    missing=resolver.answer('2023/9/8美国 EMOT M 1-1/8价格是多少',dataset_id=ds)
    assert missing['answer']['sources']['v1']['record_count']==0
    assert missing['fields'][0]['candidates'][0]['series_id']=='price'


def test_literal_does_not_resolve_duplicate_definition_or_conversion():
    a=dict(series_id='a',metric='商品报价',sheet='表1',dimensions={})
    assert exact_lookup([a,dict(a,series_id='b',sheet='表2')],'2023/9/7商品报价是多少','2023-09-07') is None
    assert exact_lookup([a,dict(a,series_id='b',metric='商品报价结算价')],'商品报价是多少','2023-09-07') is None
    assert exact_lookup([a],'商品报价换算成美元','2023-09-07') is None
    assert exact_lookup([dict(a,dimensions={'产地':'美国'})],'商品报价是多少','2023-09-07') is None
    siblings=[dict(a,series_id=str(i),metric=m) for i,m in enumerate(['合约甲01','合约甲01收盘价','合约甲01结算价'])]
    assert exact_lookup(siblings,'合约甲01价格是多少','2023-09-07') is None
    siblings=[dict(a,series_id=str(i),metric=m) for i,m in enumerate(['郑棉01合约','郑棉01收盘价','郑棉01结算价'])]
    assert exact_lookup(siblings,'郑棉01合约价格是多少','2023-09-07') is None


def test_index_rebuilds_for_new_metadata_and_ignores_observation_availability(tmp_path):
    cat=[dict(series_id='p',metric='3128B现货价格',sheet='表',dimensions={}),
         dict(series_id='c',metric='CF01结算价',sheet='表',dimensions={}),
         dict(series_id='d',metric='CF01收盘价',sheet='表',dimensions={})]
    idx=FieldIndex(tmp_path,cat)
    assert idx.search(['2023/9/3 3128价格'])[0][0]['series_id']=='p'
    assert {s['series_id'] for s in idx.search(['CF01价格'])[0]}=={'c','d','p'}
    updated=FieldIndex(tmp_path,cat+[dict(series_id='x',metric='新增字段',sheet='新表')])
    assert updated.path!=idx.path
    assert updated.search(['新增字段'])[0][0]['series_id']=='x'


def test_expansion_searches_alias_without_full_catalogue_or_hardcoded_pair(catalogue):
    store,ds=catalogue;path=store._path(ds)/'series.json'
    data=json.loads(path.read_text())
    data.extend(dict(series_id=f'noise-{i}',metric=f'无关销售{i}',sheet='别的表') for i in range(80))
    path.write_text(json.dumps(data));calls=[]
    def expand(p):
        assert '_expand_query' in p and 'catalog' not in p
        return {'search_terms':['欧元兑人民币']}
    def model(p):
        calls.append(p)
        assert sum(len(g['columns']) for g in p['catalog'])<24
        return proposal([['eur']])
    result=FieldResolver(store,model,expand).answer('2023/9/7欧币汇价',dataset_id=ds)
    assert result['answer']['sources']['v1']['series']['series_id']=='eur'
    assert not result['retrieval']['full_catalogue_fallback']


def test_planner_cannot_choose_real_but_not_retrieved_id(catalogue):
    store,ds=catalogue;path=store._path(ds)/'series.json'
    data=json.loads(path.read_text())
    data.extend(dict(series_id=f'noise-{i}',metric=f'独立库存{i}',sheet='库存表') for i in range(80))
    path.write_text(json.dumps(data))
    resolver=FieldResolver(store,lambda p:proposal([['noise-0']]))
    with pytest.raises(ValueError,match='实际候选目录'):
        resolver.resolve(ds,'2023/9/7美国 EMOT M 1-1/8趋势')
