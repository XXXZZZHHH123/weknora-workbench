"""Executor contract tests. Language understanding is evaluated with live cases.

The mock supplies a complete semantic plan: the executor must never turn words
in the question into a different operation or silently repair an invalid plan.
"""
from copy import deepcopy
import pytest
from structured_data.store import DataStore
from structured_data.field_resolver import FieldResolver
from structured_data.task_plan import normalize_proposal, requirements, analyze


@pytest.fixture
def data(tmp_path):
    store=DataStore(tmp_path);rows=[]
    for sid,values,col in [('p',[10,20,None,0,15,30],'B'),('q',[11,21,None,1,16,31],'C')]:
        for i,v in enumerate(values,1):rows.append(dict(series_id=sid,date=f'2023-01-{i:02}',value=v,raw_value=v,source_cell=f'{col}{i+1}',date_cell=f'A{i+1}',value_status='valid' if v is not None else 'missing',formula=None))
    ds=store.publish(dict(source_file='example.xlsx',sha256='d'*64,rule_version='tasks',series=[dict(series_id=sid,sheet='记录',metric=name,dimensions={},header_cells=[col+'1'],unit=None) for sid,name,col in [('p','收盘价','B'),('q','结算价','C')]],observations=rows,sheets=[],excluded_regions=[],issues=[]))['dataset_id']
    return store,ds


def plan(operation='rows', ids=('p',), **query):
    return dict(intent='lookup',decision='resolved',fields=[dict(role=f'v{i+1}',label='指标',candidate_ids=[sid]) for i,sid in enumerate(ids)],query=dict(operation=operation,**query),time_scope=dict(kind='unbounded',evidence=[],explanation='未限定日历范围'))


def answer(data,raw,question='读取指标'):
    store,ds=data
    return FieldResolver(store,lambda _:deepcopy(raw)).answer(question,ds)


def test_window_uses_last_dates_and_returns_evidence(data):
    result=answer(data,plan('mean',last_n=3))['answer']['sources']['v1']
    assert result['aggregate']==15
    assert [r['date'] for r in result['window_rows']]==['2023-01-04','2023-01-05','2023-01-06']


@pytest.mark.parametrize('op,threshold,expected',[('count',15,2),('count',100,0),('min',0,10)])
def test_filters_are_applied_to_actual_rows(data,op,threshold,expected):
    result=answer(data,plan(op,value_filters=[dict(op='gt',value=threshold)]))['answer']['sources']['v1']
    assert result['aggregate']==expected


def test_top_and_range_are_not_rewritten(data):
    raw=plan('top',top_n=3,order='desc')
    raw['time_scope']=dict(kind='range',evidence=['整个一月'],explanation='指定月份',start_date='2023-01-01',end_date='2023-01-31')
    result=answer(data,raw,'整个一月')['answer']['sources']['v1']
    assert [r['value'] for r in result['rows']]==[30,20,15]


@pytest.mark.parametrize('question,raw',[
    ('不要最高，只要最低',plan('min')),
    ('不画图，最新值就行',plan('latest')),
    ('不是问为什么，列最近三条价格',plan('rows',last_n=3)),
])
def test_negated_words_do_not_override_model_plan(data,question,raw):
    result=answer(data,raw,question)
    assert result['query']['operation']==raw['query']['operation']
    assert 'analysis' not in result['answer'] and 'chart' not in result['answer']


def test_maximum_minus_mean_remains_compound_statistics(data):
    raw=plan('statistics',ids=('p','p'));raw['intent']='calculation'
    for field,op in zip(raw['fields'],['max','mean']):field['operation']=op
    raw['calculation']=dict(expression='v1-v2',unit='原表未注明单位')
    result=answer(data,raw,'最高价比均价高多少？')['answer']
    assert result['value']==15 and result['sources']['v2']['aggregate']==15


def test_two_extremes_do_not_imply_subtraction(data):
    raw=plan('statistics',ids=('p','p'))
    for f,op in zip(raw['fields'],['max','min']):f['operation']=op
    result=answer(data,raw,'最高和最低分别多少，不用算差值')['answer']
    assert 'value' not in result and [s['aggregate'] for s in result['sources'].values()]==[30,0]


def test_real_source_choice_only_is_confirmed(data):
    store,ds=data;raw=plan('statistics',ids=('p','p'));raw['intent']='calculation'
    for f,op in zip(raw['fields'],['max','min']):f.update(operation=op,candidate_ids=['p','q'])
    raw.update(same_series_roles=['v1','v2'],calculation=dict(expression='v1-v2',unit='原表未注明单位'))
    f=FieldResolver(store,lambda _:deepcopy(raw));pending=f.answer('两个极值之差',ds)
    assert pending['status']=='needs_clarification' and not pending['requires_note']
    with pytest.raises(ValueError,match='同一'):f.confirm(pending['resolution_id'],{'v1':'p','v2':'q'})
    assert f.attach_answer(f.confirm(pending['resolution_id'],{'v1':'p'}))['answer']['value']==30


def test_multi_turn_receives_complete_task_and_current_words(data):
    store,ds=data;calls=[]
    def model(payload):
        calls.append(deepcopy(payload));raw=plan('mean')
        if len(calls)==1:raw['time_scope']=dict(kind='range',evidence=['整个一月'],explanation='指定月份',start_date='2023-01-01',end_date='2023-01-31')
        else:
            previous=payload['previous_task']
            assert previous['query']['operation']=='mean'
            assert previous['task_requirements']['time_scope']['end_date']=='2023-01-31'
            assert previous['original_question']=='整个一月均价'
            raw['query']['operation']='sum';raw['time_scope']=previous['task_requirements']['time_scope']
        return raw
    f=FieldResolver(store,model);first=f.answer('整个一月均价',ds)
    second=f.answer('改成合计',ds,previous_resolution_id=first['resolution_id'])
    assert calls[-1]['question']=='改成合计'
    assert second['answer']['sources']['v1']['aggregate']==75
    assert second['answer']['end_date']=='2023-01-31'


def test_model_repairs_invalid_formula_not_a_user_popup(data):
    store,ds=data;calls=[]
    def model(payload):
        calls.append(payload);raw=plan('mean',last_n=3)
        if len(calls)==1:raw.update(intent='calculation',calculation=dict(expression='mean(v1)',unit='元'))
        else:assert 'repair_instruction' in payload
        return raw
    result=FieldResolver(store,model).answer('最近三条均价',ds)
    assert len(calls)==2 and result['answer']['sources']['v1']['aggregate']==15
    bad=plan('rows');bad.update(intent='calculation',calculation=dict(expression='mean(v1)',unit='元'))
    result=FieldResolver(store,lambda _:deepcopy(bad)).tool_answer('求均价',ds)
    assert result['kind']=='structured_tool_notice' and 'fields' not in result


@pytest.mark.parametrize('features,ids',[({'analysis':'correlation'},('p','q')),({'analysis':'trend'},('p',)),({'analysis':'causality'},('p',)),({'chart':True},('p',))])
def test_analysis_uses_real_full_inputs(data,features,ids):
    raw=plan(ids=ids);raw['presentation']=features
    a=answer(data,raw)['answer']['analysis']
    if features.get('analysis')=='correlation':assert a['value']==pytest.approx(1) and a['sample_count']==5
    else:assert a['change']==20 and a['max_drawdown_percent']==100
    if features.get('analysis')=='causality':assert '不能' in a['notice']


@pytest.mark.parametrize('query',[{'last_n':50000},{'top_n':0},{'value_filters':[{'op':'DROP TABLE','value':1}]},{'order':'random'}])
def test_invalid_plan_does_not_execute(data,query):
    store,ds=data
    result=FieldResolver(store,lambda _:plan('mean',**query)).tool_answer('查询',ds)
    assert result['kind']=='structured_tool_notice'


def test_unsupported_request_does_not_ask_user_to_confirm_fields(data):
    store,ds=data
    result=FieldResolver(store,lambda _:dict(intent='unsupported',reason='不支持分组滚动统计')).tool_answer('按年滚动统计',ds)
    assert result['kind']=='structured_tool_notice' and '不支持' in result['message']


def test_large_correlation_uses_all_dates_but_bounds_display_rows():
    import datetime
    rows=[dict(date=(datetime.date(2020,1,1)+datetime.timedelta(days=i)).isoformat(),value=float(i+1),source_cell='B'+str(i+2)) for i in range(250)]
    other=[dict(r,value=float(i+1) if i<200 else float(501-i)) for i,r in enumerate(rows)]
    a=analyze('correlation',{'v1':{'rows':rows},'v2':{'rows':other}})
    assert a['sample_count']==250 and len(a['aligned_rows'])==200 and a['aligned_rows_truncated']
    assert a['value']<0.99


def test_equality_filter_has_same_contract_in_plan_and_executor(data):
    result=answer(data,plan('count',value_filters=[dict(op='eq',value=15)]))['answer']['sources']['v1']
    assert result['aggregate']==1 and result['rows'][0]['source_cell']=='B6'


def test_rank_count_cannot_be_silently_ignored_on_rows():
    with pytest.raises(ValueError,match='top_n'):
        normalize_proposal(plan('rows',top_n=3),{},'读取指标')


def test_validated_time_evidence_survives_bounded_conversation(data):
    store,ds=data
    def model(payload):
        raw=plan('mean')
        raw['time_scope']=dict(kind='range',evidence=['整个一月'],explanation='保留用户先前的范围',start_date='2023-01-01',end_date='2023-01-31')
        return raw
    f=FieldResolver(store,model);result=f.answer('整个一月均价',ds)
    for i in range(9):
        result=f.answer(f'继续核对第{i+1}次',ds,previous_resolution_id=result['resolution_id'])
    assert '整个一月均价' not in result['user_messages']
    assert result['query']['start_date']=='2023-01-01'
    assert result['answer']['sources']['v1']['aggregate']==15


def test_source_formula_is_bounded_metadata_not_an_executable_instruction(data):
    store,ds=data
    import duckdb
    with duckdb.connect(str(store._path(ds)/"data.duckdb")) as con:
        con.execute("UPDATE observations SET formula=? WHERE series_id=?",['=VLOOKUP(A2,原始表!A:C,2,0)','p'])
    assert store.formula_examples(ds,['p'])=={'p':'=VLOOKUP(A2,原始表!A:C,2,0)'}
    assert store.formula_examples(ds,['q'])=={}
    def model(payload):
        p=next(c for g in payload['catalog'] for c in g['columns'] if c['id']=='p')
        assert p['formula_example']=='=VLOOKUP(A2,原始表!A:C,2,0)'
        return plan('latest')
    result=FieldResolver(store,model).answer('读取已保存的最后记录',ds)
    assert result['answer']['sources']['v1']['rows'][0]['value']==30


def test_partial_equivalent_group_is_repaired_internally_not_a_user_choice(data):
    store,ds=data;calls=[]
    import json,duckdb
    path=store._path(ds)/'series.json';series=json.loads(path.read_text())
    for item in series:
        if item['series_id']=='q':item.update(sheet='同源副本',metric='收盘价')
    path.write_text(json.dumps(series))
    with duckdb.connect(str(store._path(ds)/'data.duckdb')) as con:
        con.execute("UPDATE observations AS q SET value=p.value FROM observations AS p WHERE q.series_id='q' AND p.series_id='p' AND q.date=p.date")
    def model(payload):
        calls.append(deepcopy(payload));raw=plan('latest')
        raw['fields'][0].update(candidate_ids=['p','q'],equivalent_source_ids=['q'] if len(calls)==1 else ['p','q'])
        return raw
    result=FieldResolver(store,model).answer('已确定同口径的最新记录',ds)
    assert len(calls)==2 and '同源关系声明不完整' in calls[1]['repair_instruction']
    assert result['status']=='resolved' and len(result['fields'][0]['candidates'])==1


def test_sequence_analysis_tag_is_normalized_without_changing_the_analysis(data):
    raw=plan('rows');raw.update(intent='calculation',presentation={'analysis':'trend'},calculation={'expression':'','unit':'原表未注明单位'})
    result=answer(data,raw)
    assert result['intent']=='lookup' and result['query']['operation']=='rows'
    assert result['answer']['analysis']['change']==20
    raw['calculation']['expression']='v1+1'
    with pytest.raises(ValueError,match='calculation'):
        normalize_proposal(raw,{},'读取指标')


def test_distinct_same_sheet_metrics_cannot_be_collapsed_as_equivalent(data):
    store,ds=data;calls=[]
    def model(payload):
        calls.append(deepcopy(payload));raw=plan('latest')
        raw['fields'][0]['candidate_ids']=['p','q']
        if len(calls)==1:raw['fields'][0]['equivalent_source_ids']=['p','q']
        return raw
    result=FieldResolver(store,model).answer('两种实际价格定义待选择',ds)
    assert len(calls)==2 and '不同原始指标' in calls[1]['repair_instruction']
    assert result['status']=='needs_clarification' and len(result['fields'][0]['candidates'])==2
