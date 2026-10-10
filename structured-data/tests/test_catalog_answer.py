import pytest
from structured_data.store import DataStore
from structured_data.field_resolver import FieldResolver


@pytest.fixture
def fields_store(tmp_path):
    store=DataStore(tmp_path/'data');series=[];rows=[]
    for metric,col in [('收盘价','B'),('结算价','K'),('盘中价','T')]:
        for i in range(7):
            sid=f'{col}-{i}'
            series.append(dict(series_id=sid,sheet='期货报价',metric=metric,unit='元/吨',dimensions={'contract':f'C{i}'},header_cells=[f'{col}1'],date_mode='date'))
            rows.append(dict(series_id=sid,date='2023-09-07',value=i+10,raw_value=i+10,source_cell=f'{col}2',date_cell='A2',value_status='valid'))
    ds=store.publish(dict(source_file='test.xlsx',sha256='a'*64,rule_version='test',series=series,observations=rows,sheets=[],issues=[],excluded_regions=[]))['dataset_id']
    return store,ds


def metadata(names):
    return dict(intent='metadata',decision='resolved',fields=[],metadata={'sheets':['期货报价'],'metrics':names})


def test_metadata_counts_types_from_all_contracts_without_observation_query(fields_store,monkeypatch):
    store,ds=fields_store
    monkeypatch.setattr(store,'query_series',lambda *a,**kw:pytest.fail('Metadata must never query observations'))
    def model(p):
        assert p['catalog_summary']==[{'sheet':'期货报价','metrics':['收盘价','盘中价','结算价']}]
        return metadata(['收盘价','结算价','盘中价'])
    plan=FieldResolver(store,model).answer('期货报价有几种价格',dataset_id=ds,date='2023-09-07')
    assert plan['status']=='resolved' and plan['date'] is None and not plan['requires_note']
    assert plan['answer']['count']==3 and plan['answer']['complete']
    assert all(g['source_count']==7 and len(g['sources'])==7 for g in plan['answer']['groups'])
    assert plan['answer']['groups'][0]['sources'][0]['header_cells']==['B1']


def test_catalog_summary_does_not_depend_on_fts_subset(fields_store,monkeypatch):
    from structured_data.field_index import FieldIndex
    store,ds=fields_store
    # Force catalogue over the small-catalogue fallback threshold.
    import json
    path=store._path(ds)/'series.json';cols=json.loads(path.read_text())
    cols.extend(dict(series_id=f'n{i}',sheet='其他表',metric=f'别的指标{i}') for i in range(30))
    path.write_text(json.dumps(cols))
    monkeypatch.setattr(FieldIndex,'search',lambda self,*a,**kw:([cols[0]],{'method':'test_partial'}))
    def model(p):
        assert sum(len(g['columns']) for g in p['catalog'])==1
        assert next(g for g in p['catalog_summary'] if g['sheet']=='期货报价')['metrics']==['收盘价','盘中价','结算价']
        return metadata(['收盘价','结算价','盘中价'])
    result=FieldResolver(store,model).answer('有几种价格',dataset_id=ds)
    assert result['answer']['count']==3


def test_metadata_rejects_invented_names_and_does_not_open_field_choice(fields_store):
    store,ds=fields_store
    result=FieldResolver(store,lambda p:metadata(['最高价'])).tool_answer('有哪些价格',dataset_id=ds)
    assert result['kind']=='structured_tool_notice' and result['status']=='query_unavailable'
    assert 'answer' not in result


def test_single_field_model_clarify_is_not_itself_business_ambiguity(fields_store):
    store,ds=fields_store
    raw=dict(intent='lookup',decision='clarify',question='请确认是否查询这个字段',fields=[{'role':'v1','label':'收盘价','candidate_ids':['B-0']}])
    result=FieldResolver(store,lambda p:raw).answer('2023/9/7 C0收盘价',dataset_id=ds)
    assert result['status']=='resolved' and result['answer']['sources']['v1']['rows'][0]['value']==10


def test_two_requested_metrics_return_two_sources_not_choice(fields_store):
    store,ds=fields_store
    raw=dict(intent='lookup',decision='clarify',fields=[{'role':'v1','label':'收盘价','candidate_ids':['B-0']},
                                                    {'role':'v2','label':'结算价','candidate_ids':['K-0']}])
    result=FieldResolver(store,lambda p:raw).answer('2023/9/7 C0收盘价和结算价都给我',dataset_id=ds)
    assert result['status']=='resolved' and len(result['answer']['sources'])==2


def test_real_alternatives_and_missing_date_still_require_confirmation(fields_store):
    store,ds=fields_store
    raw=dict(intent='lookup',decision='clarify',fields=[{'role':'v1','label':'价格','candidate_ids':['B-0','K-0']}])
    result=FieldResolver(store,lambda p:raw).answer('2023/9/7 C0价格',dataset_id=ds)
    assert result['status']=='needs_clarification' and 'answer' not in result
    raw['fields'][0]['candidate_ids']=['B-0']
    result=FieldResolver(store,lambda p:raw).answer('C0价格',dataset_id=ds)
    assert result['status']=='needs_clarification' and result['requires_note']


def test_real_missing_definition_can_be_requested_for_single_field(fields_store):
    store,ds=fields_store
    raw=dict(intent='lookup',decision='clarify',question='需要确认是否含税',clarification={'missing_input':'definition','reason':'含税要求影响答案但原字段没有注明'},fields=[{'role':'v1','label':'价格','candidate_ids':['B-0']}])
    result=FieldResolver(store,lambda p:raw).answer('2023/9/7 C0含税价格',dataset_id=ds)
    assert result['status']=='needs_clarification' and result['requires_note'] and 'answer' not in result


def test_note_can_change_pending_lookup_to_metadata_without_discarding_scope(fields_store):
    store,ds=fields_store
    def model(p):
        if '用户补充' in p['question']:return metadata(['收盘价','结算价','盘中价'])
        return dict(intent='lookup',decision='clarify',fields=[{'role':'v1','label':'价格','candidate_ids':['B-0','K-0']}])
    resolver=FieldResolver(store,model)
    first=resolver.resolve(ds,'有哪些价格')
    result=resolver.attach_answer(resolver.confirm(first['resolution_id'],{'v1':'B-0'},'问价格种类，不需要具体报价'))
    assert result['status']=='resolved' and result['answer']['count']==3


def test_repeated_same_id_does_not_create_truncation_or_ambiguity(fields_store):
    store,ds=fields_store
    raw=dict(intent='lookup',decision='clarify',fields=[{'role':'v1','label':'收盘价','candidate_ids':['B-0']*7}])
    result=FieldResolver(store,lambda p:raw).answer('2023/9/7 C0收盘价',dataset_id=ds)
    assert result['status']=='resolved' and not result['candidates_truncated']


def test_metadata_scope_ambiguity_requires_specific_missing_information(fields_store):
    store,ds=fields_store
    raw=metadata(['收盘价','结算价']);raw.update(decision='clarify',question='要查哪个范围？')
    result=FieldResolver(store,lambda p:raw).tool_answer('有哪些价格',dataset_id=ds)
    assert result['kind']=='structured_tool_notice'
    raw['clarification']={'missing_input':'scope','reason':'不同范围会列出不同种类'}
    result=FieldResolver(store,lambda p:raw).answer('有哪些价格',dataset_id=ds)
    assert result['status']=='needs_clarification' and result['requires_note'] and 'answer' not in result
