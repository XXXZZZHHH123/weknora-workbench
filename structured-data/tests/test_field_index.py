import json
import pytest
from structured_data.field_index import FieldIndex, literal_fields
from structured_data.field_resolver import FieldResolver
from test_catalog_aliases import catalogue
from test_field_resolver import proposal


def test_literal_unique_lookup_still_uses_semantic_model(catalogue):
    store,ds=catalogue;calls=[]
    def model(p):
        calls.append(p)
        return proposal([['price']])
    resolver=FieldResolver(store,model)
    present=resolver.answer('美国 EMOT M 1-1/8价格是多少',dataset_id=ds,date='2023-09-07')
    assert len(calls)==1
    assert present['answer']['sources']['v1']['rows'][0]['value']==99.7
    missing=resolver.answer('美国 EMOT M 1-1/8价格是多少',dataset_id=ds,date='2023-09-08')
    assert len(calls)==2 and missing['answer']['sources']['v1']['record_count']==0
    assert missing['fields'][0]['candidates'][0]['series_id']=='price'


def test_literal_header_recall_keeps_both_definitions_for_model():
    fields=[dict(series_id='a',metric='商品收盘价',sheet='表'),dict(series_id='b',metric='商品结算价',sheet='表')]
    assert {s['series_id'] for s in literal_fields(fields,'不要商品收盘价，只要商品结算价')}=={'a','b'}
    # Recall is not interpretation: negation and choosing a field belong to the model.


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


def test_alias_field_binding_sees_full_model_catalogue_without_hardcoded_pair(catalogue):
    store,ds=catalogue;path=store._path(ds)/'series.json'
    data=json.loads(path.read_text())
    data.extend(dict(series_id=f'noise-{i}',metric=f'无关销售{i}',sheet='别的表') for i in range(80))
    path.write_text(json.dumps(data));calls=[]
    def expand(p):
        assert '_expand_query' in p and 'catalog' not in p
        return {'search_terms':['欧元兑人民币']}
    def model(p):
        calls.append(p)
        assert sum(len(g['columns']) for g in p['catalog'])==86
        return proposal([['eur']])
    result=FieldResolver(store,model,expand).answer('2023/9/7欧币汇价',dataset_id=ds,date='2023-09-07')
    assert result['answer']['sources']['v1']['series']['series_id']=='eur'
    assert not result['retrieval']['full_catalogue_fallback']


def test_planner_cannot_choose_nonexistent_id_from_complete_model_catalogue(catalogue):
    store,ds=catalogue;path=store._path(ds)/'series.json'
    data=json.loads(path.read_text())
    data.extend(dict(series_id=f'noise-{i}',metric=f'独立库存{i}',sheet='库存表') for i in range(80))
    path.write_text(json.dumps(data))
    resolver=FieldResolver(store,lambda p:proposal([['not-imported-id']]))
    with pytest.raises(ValueError,match='实际候选目录'):
        resolver.resolve(ds,'2023/9/7美国 EMOT M 1-1/8趋势')


def test_named_header_survives_crowded_sheet_context_and_keeps_duplicate_definitions(tmp_path):
    cat=[dict(series_id=f'noise-{i}',metric=f'累计公检量{i}',sheet='新疆棉入库和公检量') for i in range(90)]
    cat.extend([dict(series_id='inventory',metric='在库量',sheet='新疆棉入库和公检量'),
                dict(series_id='inventory-other',metric='在库量',sheet='另一表')])
    recalled,_=FieldIndex(tmp_path,cat).search(['新疆棉入库和公检量的在库量哪天最多'],prior_ids=['noise-0'],limit=1)
    assert {'inventory','inventory-other','noise-0'}.issubset({s['series_id'] for s in recalled})


def test_parallel_index_initialization_does_not_duplicate_fields(tmp_path):
    from concurrent.futures import ThreadPoolExecutor
    import sqlite3
    from structured_data.field_index import FieldIndex
    catalog=[{'series_id':str(i),'sheet':'来源','metric':'价格'+str(i)} for i in range(20)]
    with ThreadPoolExecutor(max_workers=6) as pool:
        indexes=list(pool.map(lambda _:FieldIndex(tmp_path,catalog),range(12)))
    with sqlite3.connect(indexes[0].path) as con:
        assert con.execute('SELECT count(*) FROM fields').fetchone()[0]==20
        assert con.execute('SELECT count(DISTINCT id) FROM fields').fetchone()[0]==20
