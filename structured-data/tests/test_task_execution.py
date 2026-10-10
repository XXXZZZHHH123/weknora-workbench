import pytest
from structured_data.store import DataStore
from structured_data.field_resolver import FieldResolver
from structured_data.task_plan import requirements,analyze,literal_scope_matches


@pytest.fixture
def data(tmp_path):
    store=DataStore(tmp_path)
    rows=[]
    for sid,values,col in [('p',[10,20,None,0,15,30],'B'),('q',[11,21,None,1,16,31],'C')]:
        for i,v in enumerate(values,1):rows.append(dict(series_id=sid,date=f'2023-01-{i:02}',value=v,raw_value=v,source_cell=f'{col}{i+1}',date_cell=f'A{i+1}',value_status='valid' if v is not None else 'missing',formula=None))
    ds=store.publish(dict(source_file='example.xlsx',sha256='d'*64,rule_version='tasks',series=[dict(series_id=sid,sheet='记录',metric=name,dimensions={},header_cells=[col+'1'],unit=None) for sid,name,col in [('p','收盘价','B'),('q','结算价','C')]],observations=rows,sheets=[],excluded_regions=[],issues=[]))['dataset_id']
    return store,ds


def model(fields=('p',),**kw):
    return lambda _:dict(intent='lookup',decision='resolved',fields=[dict(role=f'v{i+1}',label='指标',candidate_ids=[sid]) for i,sid in enumerate(fields)],**kw)


def test_window_uses_last_dates_before_filter_and_returns_evidence(data):
    store,ds=data
    answer=FieldResolver(store,model()).answer('最近3个交易日收盘价平均多少？列出这3天',ds)['answer']
    s=answer['sources']['v1']
    assert s['aggregate']==15
    assert [r['date'] for r in s['window_rows']]==['2023-01-04','2023-01-05','2023-01-06']
    assert s['valid_count']==3


def test_zero_filter_and_conditional_count_are_not_dropped(data):
    store,ds=data;f=FieldResolver(store,model())
    a=f.answer('收盘价排除0和空值后的最低价多少？',ds)['answer']['sources']['v1']
    assert a['aggregate']==10 and a['rows'][0]['source_cell']=='B2'
    a=f.answer('2023年收盘价超过15的有多少个交易日？',ds)['answer']['sources']['v1']
    assert a['aggregate']==2  # Strictly >15: 20 and 30, not >=15.
    a=f.answer('2023年收盘价超过100的有多少个交易日？',ds)['answer']['sources']['v1']
    assert a['aggregate']==0 and a['status']=='ok' and a['scope_record_count']==6


def test_month_range_and_top_n(data):
    store,ds=data;f=FieldResolver(store,model())
    p=f.answer('2023年1月收盘价平均多少？',ds,date='2023-01')
    assert p['answer']['sources']['v1']['aggregate']==15
    assert p['answer']['start_date']=='2023-01-01'
    s=f.answer('收盘价最高的三天分别多少？',ds)['answer']['sources']['v1']
    assert [(r['date'],r['value']) for r in s['rows']]==[('2023-01-06',30),('2023-01-02',20),('2023-01-05',15)]


def test_ambiguous_extrema_need_one_source_choice_not_missing_operation(data):
    store,ds=data
    def ambiguous(_):return dict(intent='calculation',decision='clarify',fields=[dict(role='v1',label='最高价',candidate_ids=['p','q']),dict(role='v2',label='最低价',candidate_ids=['p','q'])],query={'operation':'statistics'})
    f=FieldResolver(store,ambiguous);p=f.answer('最高价和最低价差值多少？分别是哪天？',ds)
    assert p['status']=='needs_clarification'
    assert not p['requires_note'] and 'answer' not in p
    assert p['same_series_roles']==['v1','v2']
    chosen=f.attach_answer(f.confirm(p['resolution_id'],{'v1':'p'}))
    assert chosen['answer']['value']==30
    with pytest.raises(ValueError,match='同一'):
        f.confirm(p['resolution_id'],{'v1':'p','v2':'q'})


def test_single_day_and_cross_day_calculation(data):
    store,ds=data
    def calc(_):return dict(intent='calculation',decision='resolved',fields=[dict(role='v1',candidate_ids=['q']),dict(role='v2',candidate_ids=['p'])],calculation={'expression':'v1-v2','unit':'元/吨'},query={'operation':'rows','start_date':'2023-01-06','end_date':'2023-01-06'})
    p=FieldResolver(store,calc).answer('2023年1月6日结算价减收盘价是多少？',ds)
    assert p['answer']['value']==1 and p['answer']['unit']=='原表未注明单位'
    p=FieldResolver(store,model()).answer('2023年1月6日收盘价比1月5日涨了多少？涨幅多少？',ds)
    assert [o['value'] for o in p['answer']['outputs']]==[15,100]
    assert p['answer']['sources']['v2']['rows'][0]['date']=='2023-01-05'


def test_analysis_uses_complete_intersection_and_causality_boundary(data):
    store,ds=data
    p=FieldResolver(store,model(('p','q'))).answer('2023年收盘价和结算价按共同日期算Pearson相关系数',ds)
    a=p['answer']['analysis'];assert a['value']==pytest.approx(1) and a['sample_count']==5
    assert not any(r['date']=='2023-01-03' for r in a['aligned_rows'])
    p=FieldResolver(store,model()).answer('2023年1月收盘价总体是涨还是跌？首尾差多少，最大回撤多少？',ds)
    assert p['answer']['analysis']['change']==20 and p['answer']['analysis']['max_drawdown_percent']==100
    p=FieldResolver(store,model()).answer('2023年1月收盘价为什么变化？区分文件证据和推测',ds)
    assert '不能' in p['answer']['analysis']['notice']


def test_chart_has_raw_values_and_future_is_not_historical_fact(data):
    store,ds=data;f=FieldResolver(store,model())
    p=f.answer('把2023年1月收盘价画成折线图，标出最高最低',ds)
    assert len(p['answer']['chart']['series'][0]['rows'])==5
    assert p['answer']['analysis']['max']['source_cell']=='B7'
    p=f.answer('根据收盘价预测2026年10月11日价格，说明最后数据日期',ds)
    a=p['answer']['analysis'];assert a['last_observation']['date']=='2023-01-06' and 'value' not in a


def test_invalid_filters_and_excessive_window_fail_before_query(data):
    store,ds=data
    with pytest.raises(ValueError):store.query_series(ds,'p',value_filters=[{'op':'gt;DROP TABLE observations','value':1}])
    with pytest.raises(ValueError):requirements('最近50000个交易日均价')
    with pytest.raises(ValueError):requirements('2023年2月30日价格')


def test_literal_fast_path_preserves_unmatched_entity_and_one_day_latest():
    field={'metric':'在库量','sheet':'新疆棉入库','dimensions':{}}
    assert literal_scope_matches('在库量哪天最多？有多少货？',field)
    assert not literal_scope_matches('印度在库量哪天最多？',field)
    assert requirements('最近一天的价格')['latest']
    assert 'last_n' not in requirements('最近一天的价格')
    assert requirements('不超过1.7万的有多少天')['filters']==[{'op':'lte','value':17000}]
    assert requirements('不低于1.7万的有多少天')['filters']==[{'op':'gte','value':17000}]


def test_pending_followup_keeps_alternatives_until_actual_human_choice(data):
    store,ds=data
    f=FieldResolver(store,lambda _:dict(intent='lookup',decision='clarify',question='哪一种价格？',fields=[dict(role='v1',candidate_ids=['p','q'])],query={'operation':'latest'}))
    first=f.answer('最近一天的价格',ds)
    p=f.answer('最高价和最低价差值多少？分别是哪天？',ds,pending_resolution_id=first['resolution_id'])
    assert p['status']=='needs_clarification' and 'answer' not in p
    assert p['same_series_roles']==['v1','v2']
    assert all({c['series_id'] for c in group['candidates']}=={'p','q'} for group in p['fields'])
    assert f.attach_answer(f.confirm(p['resolution_id'],{'v1':'p'}))['answer']['value']==30


def test_human_note_dates_are_not_lost_after_confirmed_json():
    req=requirements('2022/23年度产量与进口合计\n已确认字段：{"series_id":"a"}\n用户补充：按2022年9月1日到2023年8月31日求和')
    assert req['bounds']=={'start_date':'2022-09-01','end_date':'2023-08-31'}
    assert not req.get('needs_period_definition')
    assert requirements('2009年1月6日01合约结算价')['contract_months']==[1]
    assert requirements('2023年9月22日1月和5月合约结算价差')['contract_months']==[1,5]


def test_equivalent_copies_use_newest_coverage_and_preserve_confirmed_source(tmp_path):
    store=DataStore(tmp_path)
    series=[dict(series_id=sid,sheet='交易记录',metric=name,dimensions={},header_cells=[col+'1'],unit=None) for sid,name,col in [('current','收盘价','B'),('copy','活跃收盘','C'),('undated','收盘价','D')]]
    obs=[dict(series_id=sid,date=day,value=v,raw_value=v,source_cell=cell,date_cell='A2',value_status='valid',formula=None) for sid,day,v,cell in [('current','2023-01-02',20,'B2'),('current','2023-01-03',30,'B3'),('copy','2023-01-02',20,'C2')]]
    ds=store.publish(dict(source_file='copies.xlsx',sha256='f'*64,rule_version='tasks',series=series,observations=obs,sheets=[],excluded_regions=[],issues=[]))['dataset_id']
    def proposal(_):return dict(intent='lookup',decision='resolved',fields=[dict(role='v1',label='活跃交易价格',candidate_ids=['copy'],equivalent_source_ids=['current','copy'])],query={'operation':'latest'})
    f=FieldResolver(store,proposal)
    p=f.answer('交易记录最新的活跃交易价格',ds)
    assert p['answer']['sources']['v1']['series']['series_id']=='current'
    assert p['answer']['sources']['v1']['rows'][0]['source_cell']=='B3'
    p=f.answer('交易记录最新的活跃交易价格',ds,prior_series_ids=['copy'])
    assert p['answer']['sources']['v1']['series']['series_id']=='copy'


def test_equivalent_sources_cannot_merge_conflicting_units(tmp_path):
    store=DataStore(tmp_path)
    series=[dict(series_id=sid,sheet='价格记录',metric='报价'+sid,dimensions={},header_cells=['B1'],unit=unit) for sid,unit in [('rmb','元/吨'),('usd','美元/吨')]]
    obs=[dict(series_id=sid,date='2023-01-02',value=20,raw_value=20,source_cell='B2',date_cell='A2',value_status='valid',formula=None) for sid in ('rmb','usd')]
    ds=store.publish(dict(source_file='currency.xlsx',sha256='e'*64,rule_version='tasks',series=series,observations=obs,sheets=[],excluded_regions=[],issues=[]))['dataset_id']
    def bad(_):return dict(intent='lookup',decision='resolved',fields=[dict(role='v1',candidate_ids=['rmb'],equivalent_source_ids=['rmb','usd'])],query={'operation':'latest'})
    with pytest.raises(ValueError,match='单位不同'):
        FieldResolver(store,bad).answer('当前交易价格',ds)


def test_pending_filter_followup_keeps_source_choices_without_another_model_call(data):
    store,ds=data;calls=[]
    def ambiguous(_):
        calls.append(1)
        return dict(intent='lookup',decision='clarify',fields=[dict(role='v1',label='价格',candidate_ids=['p','q'])],query={'operation':'latest'})
    f=FieldResolver(store,ambiguous);first=f.answer('记录最新价格',ds)
    p=f.answer('记录收盘价排除0和空值后的最低价是多少？在哪天？',ds,pending_resolution_id=first['resolution_id'])
    assert len(calls)==1 and p['status']=='needs_clarification'
    assert '最低价' in p['question'] and '最新价格' not in p['question']
    assert p['query']['value_filters']==[{'op':'ne','value':0}]
    assert f.attach_answer(f.confirm(p['resolution_id'],{'v1':'p'}))['answer']['sources']['v1']['aggregate']==10

def test_large_correlation_uses_all_dates_but_bounds_display_rows():
    import datetime,math
    rows=[dict(date=(datetime.date(2020,1,1)+datetime.timedelta(days=i)).isoformat(),value=float(i+1),source_cell='B'+str(i+2)) for i in range(250)]
    other=[dict(r,value=float(i+1) if i<200 else float(501-i)) for i,r in enumerate(rows)]
    a=analyze('correlation',{'v1':{'rows':rows},'v2':{'rows':other}})
    assert a['sample_count']==250 and len(a['aligned_rows'])==200 and a['aligned_rows_truncated']
    assert a['value']<0.99 and '全部250' in a['notice']


def test_aggregate_task_owns_expression_and_duplicate_output_roles(data):
    store,ds=data
    def invalid_model(_):return dict(intent='calculation',decision='resolved',fields=[dict(role='v1',candidate_ids=['p'],operation='mean'),dict(role='v2',candidate_ids=['p'],operation='mean')],query={'operation':'statistics'},calculation={'expression':'mean(v1)','unit':'元/吨'})
    a=FieldResolver(store,invalid_model).answer('最近3个交易日收盘价平均多少？列出这3天',ds)
    assert a['intent']=='lookup' and len(a['fields'])==1
    assert a['answer']['sources']['v1']['aggregate']==15
    assert len(a['answer']['sources']['v1']['window_rows'])==3

def test_explicit_addition_and_cumulative_sum_are_different_tasks():
    assert requirements('产量加进口合计多少')['arithmetic']
    assert not requirements('累计入库量每天加起来多少').get('arithmetic')


def test_top_records_are_not_a_free_form_calculation(data):
    store,ds=data
    def invalid_model(_):return dict(intent='calculation',decision='resolved',fields=[dict(role='v1',candidate_ids=['p']),dict(role='v2',candidate_ids=['p'])],query={'operation':'rows'},calculation={'expression':'top(v1)','unit':'原表未注明单位'})
    a=FieldResolver(store,invalid_model).answer('收盘价最高的三天分别是哪天、多少钱？',ds)
    assert a['intent']=='lookup' and len(a['fields'])==1
    assert [(r['date'],r['value']) for r in a['answer']['sources']['v1']['rows']]==[('2023-01-06',30),('2023-01-02',20),('2023-01-05',15)]


def test_overlap_is_complete_supplementary_evidence_not_an_auto_selector(data):
    store,ds=data
    evidence=store.source_overlap(ds,'p','q')
    assert evidence['common_valid_dates']==5 and evidence['exactly_equal_values']==0
    assert evidence['first_date']=='2023-01-01' and evidence['last_date']=='2023-01-06'
    with pytest.raises(ValueError):store.source_overlap(ds,'p','nonexistent')
    assert not requirements('累计入库量每天加起来是多少？这样加是不是会重复计算？').get('arithmetic')
