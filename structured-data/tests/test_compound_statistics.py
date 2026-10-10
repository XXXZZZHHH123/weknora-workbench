import pytest
from structured_data.store import DataStore
from structured_data.field_resolver import FieldResolver


@pytest.fixture
def series(tmp_path):
    store = DataStore(tmp_path)
    rows = [dict(series_id='price', date=f'2023-01-{i+1:02}', value=v,
                 raw_value=v, source_cell=f'B{i+2}', date_cell=f'A{i+2}',
                 value_status='valid' if v is not None else 'missing', formula=None)
            for i, v in enumerate([12.5, 8, 20, 8, None, 20])]
    parsed = dict(source_file='prices.xlsx', sha256='b'*64, rule_version='v1',
                  series=[dict(series_id='price', sheet='报价', metric='收盘价', unit=None,
                               dimensions={}, header_cells=['B1'])],
                  observations=rows, sheets=[], excluded_regions=[], issues=[])
    return store, store.publish(parsed)['dataset_id']


def compound():
    return dict(intent='calculation', decision='resolved', question='已确认收盘价',
                fields=[dict(role='v1', label='最高价', candidate_ids=['price'], operation='max'),
                        dict(role='v2', label='最低价', candidate_ids=['price'], operation='min')],
                query=dict(operation='statistics', start_date=None, end_date=None, period=None),
                calculation=dict(expression='v1-v2', unit='原表未注明单位', explanation='最高价减最低价'))


def test_compound_preserves_prior_scope_and_all_tied_dates(series):
    store, ds = series
    requests = []
    def model(payload):
        requests.append(payload)
        # Actual opaque catalogue IDs, not business aliases.
        sid = next(c['id'] for g in payload['catalog'] for c in g['columns'] if c['name']=='收盘价')
        plan = compound()
        for f in plan['fields']: f['candidate_ids']=[sid]
        return plan
    resolver = FieldResolver(store, model)
    plan = resolver.answer('最高价和最低价差值多少？分别是哪天？', ds,
                           prior_series_ids=['price'], date='2023-01-06')
    assert plan['status']=='resolved'
    assert len(requests[0]['prior_series'])==1
    assert requests[0]['prior_series'][0]['name']=='收盘价'
    answer = plan['answer']
    assert answer['date'] is None
    assert answer['start_date'] is None
    assert answer['value']==12
    assert answer['sources']['v1']['aggregate']==20
    assert [r['date'] for r in answer['sources']['v1']['rows']]==['2023-01-03','2023-01-06']
    assert [r['source_cell'] for r in answer['sources']['v2']['rows']]==['B3','B5']
    assert answer['sources']['v1']['valid_count']==5
    assert answer['sources']['v1']['missing_count']==1


def test_compound_respects_explicit_bounds_and_empty_results(series):
    store, ds = series
    raw=compound()
    raw['query'].update(start_date='2023-01-01',end_date='2023-01-02')
    resolver=FieldResolver(store,lambda _:raw)
    plan=resolver.answer('2023/1/1至2023/1/2最高和最低差值多少？',ds)
    assert plan['answer']['value']==4.5
    raw['query'].update(start_date='2024-01-01',end_date='2024-01-02')
    plan=resolver.answer('2024/1/1至2024/1/2最高和最低差值多少？',ds)
    assert 'value' not in plan['answer']
    assert '不按零' in plan['answer']['notice']


def test_partial_extremum_plan_is_completed_from_explicit_task_requirements(series):
    store, ds=series
    requests=[]
    def model(payload):
        requests.append(payload)
        sid=payload['catalog'][0]['columns'][0]['id']
        if len(requests)==1:
            return dict(intent='lookup',decision='resolved',fields=[dict(role='v1',candidate_ids=[sid])],query={'operation':'max'})
        assert '完整返回' in payload['repair_instruction']
        raw=compound()
        for f in raw['fields']: f['candidate_ids']=[sid]
        return raw
    plan=FieldResolver(store,model).answer('最高价和最低价差值多少？分别是哪天？',ds)
    assert len(requests)==1  # Fields selected by model; operations supplied by task.
    assert plan['status']=='resolved'
    assert plan['answer']['value']==12


def test_statistics_require_explicit_role_operations(series):
    store, ds=series
    cat=store.list_series(ds)['series']
    for bad in [None,'rows','median']:
        raw=compound()
        if bad is None: raw['fields'][0].pop('operation')
        else: raw['fields'][0]['operation']=bad
        with pytest.raises(ValueError): FieldResolver.validate(raw,cat)


def test_model_response_preserves_statistical_role_operations(monkeypatch, tmp_path):
    from structured_data.field_resolver import ask_model
    from structured_data import model_settings
    import httpx
    monkeypatch.setattr(model_settings,'read_model',lambda _: {'api_key':'test-only','base_url':'https://example.invalid/v1','model':'test'})
    class Reply:
        status_code=200
        def json(self): return {'choices':[{'finish_reason':'stop','message':{'content':__import__('json').dumps(compound())}}]}
    monkeypatch.setattr(httpx.Client,'post',lambda *a,**k:Reply())
    value=ask_model({'question':'最高价和最低价差值多少？'},tmp_path)
    assert [f['operation'] for f in value['fields']]==['max','min']


def test_statistical_difference_uses_source_unit_not_model_invention(series):
    store, ds=series
    raw=compound()
    raw['calculation']['unit']='元'
    plan=FieldResolver(store,lambda _:raw).answer('最高价和最低价差值多少？',ds)
    assert plan['answer']['unit']=='原表未注明单位'


def test_real_choice_keeps_statistics_and_expression_without_replanning(series):
    store,ds=series
    cat=store.list_series(ds)['series']
    second=dict(cat[0],series_id='alternative')
    raw=compound()
    for f in raw['fields']: f['candidate_ids']=['price','alternative']
    raw['calculation']['unit']='元'
    plan=FieldResolver.validate(raw,[*cat,second])
    plan.update(resolution_id='a'*24,dataset_id=ds,original_question='最高价和最低价差值多少？',resolver_version=__import__('structured_data.field_resolver',fromlist=['VERSION']).VERSION,date=None)
    resolver=FieldResolver(store,lambda _:pytest.fail('no model rewrite after a valid human choice'))
    resolver._save(plan)
    chosen=resolver.confirm(plan['resolution_id'],{'v1':'price','v2':'price'})
    answer=resolver.execute(chosen['resolution_id'])
    assert chosen['query']['operation']=='statistics'
    assert answer['value']==12
    assert answer['unit']=='原表未注明单位'


def test_named_sheet_recalls_real_columns_ahead_of_other_sheet_headers(tmp_path):
    from structured_data.field_index import FieldIndex
    catalog=[dict(series_id=f'misleading-{i}',sheet='其他表',metric=f'真实报价合约{i}',dimensions={}) for i in range(40)]
    catalog += [dict(series_id='actual-close',sheet='真实报价',metric='收盘价',dimensions={}),dict(series_id='actual-settle',sheet='真实报价',metric='结算价',dimensions={})]
    found,_=FieldIndex(tmp_path/'index',catalog).search(['最近一天真实报价价格'])
    assert {s['series_id'] for s in found} >= {'actual-close','actual-settle'}
    assert found[0]['series_id']=='actual-close'
