import calendar

import pytest

from structured_data.field_resolver import FieldResolver
from structured_data.store import DataStore
from structured_data.task_plan import requirements


from structured_data.time_scope import validate_scope, bind_scope


def scope(kind='range', **kwargs):
    return dict(dict(kind=kind,evidence=['测试期间'],explanation='用户指定的完整日历范围'),**kwargs)


@pytest.mark.parametrize('start,end', [
    ('2018-01-01','2020-12-31'), ('2021-01-01','2023-12-31'),
    ('2022-01-01','2023-12-31'), ('2024-02-01','2024-02-29'),
    ('1999-12-01','2001-03-31'),
])
def test_semantic_boundaries_are_not_reinterpreted_by_phrase_rules(start,end):
    raw={'time_scope':scope(start_date=start,end_date=end),
         'query':{'start_date':'2022-01-01','end_date':'2022-01-31'}}
    req=requirements('测试期间合计')
    bind_scope(raw,req,'测试期间合计')
    assert req['bounds']==dict(start_date=start,end_date=end)
    assert raw['query']['start_date']==start and raw['query']['end_date']==end


@pytest.mark.parametrize('change,error', [
    ({'end_date':'2023-02-29'},'不存在'),
    ({'end_date':'2021-12-31'},'开始日期'),
    ({'evidence':['不在用户消息里的范围']},'依据'),
    ({'evidence':[]},'依据'),
    ({'dates':['2022-01-02']},'混用'),
    ({'start_date':'2022-1-1'},'YYYY-MM-DD'),
])
def test_invalid_model_scope_is_system_error_not_a_business_quiz(change,error):
    raw=scope(start_date='2022-01-01',end_date='2023-12-31');raw.update(change)
    with pytest.raises(ValueError,match=error):validate_scope(raw,'测试期间合计')


def test_no_phrase_fallback_or_field_metadata_can_decide_time():
    for q in ['2021/23年度合计','2022年1月开始，23年12月结束','两年整年的']:
        assert requirements(q)['bounds']=={}  # Natural language belongs to the semantic plan.
    with pytest.raises(ValueError,match='依据'):
        validate_scope(scope(evidence=['2016-09-30'],start_date='2016-09-30'),
                       '测试期间合计\n已确认字段：{"date":"2016-09-30"}')
    with pytest.raises(ValueError,match='不能携带'):
        validate_scope(scope(kind='clarify',start_date='2022-01-01'),'测试期间合计')


@pytest.fixture
def monthly(tmp_path):
    store=DataStore(tmp_path);rows=[]
    # 22 monthly values, plus an internal missing value in imports.
    for i in range(22):
        y,m=2022+i//12,1+i%12
        for sid,value,col in [('p',10,'B'),('i',None if i==2 else 2,'C')]:
            day=1 if i==6 else calendar.monthrange(y,m)[1]
            rows.append(dict(series_id=sid,date=f'{y}-{m:02d}-{day:02d}',value=value,raw_value=value,source_cell=f'{col}{i+2}',date_cell=f'A{i+2}',value_status='valid' if value is not None else 'missing',formula=None))
    ds=store.publish(dict(source_file='monthly.xlsx',sha256='d'*64,rule_version='calendar-test',series=[dict(series_id=sid,sheet='月度供给',metric=name,dimensions={},header_cells=[col+'1'],unit=None) for sid,name,col in [('p','产量','B'),('i','进口','C')]],observations=rows,sheets=[],excluded_regions=[],issues=[]))['dataset_id']
    return store,ds


@pytest.mark.parametrize('note',['两年整年的','2022年1月到23年12月'])
def test_real_confirmation_keeps_fields_and_exposes_incomplete_months(monthly,note):
    store,ds=monthly
    def proposal(payload):
        temporal=(dict(kind='range',evidence=[note],explanation='补充说明完整起止年月',start_date='2022-01-01',end_date='2023-12-31') if '用户补充：' in payload['question'] else dict(kind='clarify',evidence=['2022/23年度'],explanation='尚未明确统计期间',question='您指自然年范围还是另有统计起止月份？',placeholder='说明希望统计的期间'))
        return dict(time_scope=temporal,intent='calculation',decision='resolved',fields=[dict(role='v1',label='产量',candidate_ids=['p']),dict(role='v2',label='进口',candidate_ids=['i'])],query={'operation':'rows'},calculation={'expression':'v1+v2','unit':'原表未注明单位','explanation':'完整两年总量，这是错误的模型文案'})
    resolver=FieldResolver(store,proposal)
    first=resolver.answer('2022/23年度产量加进口，合计是多少？',ds)
    assert first['clarification']['type']=='time_range' and first['requires_note']
    # Ordinary chat note, no artificial human re-selection of unique fields.
    answer=resolver.attach_answer(resolver.confirm(first['resolution_id'],note=note))
    assert answer['status']=='resolved' and not answer.get('requires_note')
    result=answer['answer'];assert result['start_date']=='2022-01-01' and result['end_date']=='2023-12-31'
    assert result['sources']['v1']['aggregate']==220 and result['sources']['v2']['aggregate']==42
    assert result['value']==262 and result['coverage_complete'] is False
    assert result['sources']['v1']['coverage']['missing_months']==['2023-11','2023-12']
    assert result['sources']['v2']['coverage']['missing_months']==['2022-03','2023-11','2023-12']
    assert '已记录月份小计' in result['summary'] and '完整两年总量' not in result['summary']
    assert '2023-12-31' in result['summary']


def test_monthly_coverage_is_complete_for_actual_subrange(monthly):
    store,ds=monthly
    c=store.monthly_coverage(ds,'p','2022-09-01','2023-08-31')
    assert c['complete'] and c['expected_month_count']==12 and c['valid_month_count']==12
    assert store.monthly_coverage(ds,'p','2022-09-15','2023-08-31') is None


def test_cached_semantic_plan_preserves_boundaries_and_rejects_executor_drift(monthly):
    store,ds=monthly
    calls=[]
    def proposal(payload):
        calls.append(payload)
        return dict(intent='lookup',decision='resolved',
            fields=[dict(role='v1',candidate_ids=['p'])],query={'operation':'sum'},
            time_scope=dict(kind='range',evidence=['所说的整个期间'],explanation='完整期间',start_date='2022-01-01',end_date='2023-12-31'))
    resolver=FieldResolver(store,proposal)
    q='所说的整个期间产量合计'
    first=resolver.answer(q,ds);second=resolver.answer(q,ds)
    assert second['cached'] and len(calls)==1
    assert first['answer']['sources']['v1']['aggregate']==220
    assert second['answer']['end_date']=='2023-12-31'
    second['query']['end_date']='2022-01-31';resolver._save(second)
    with pytest.raises(ValueError,match='不一致'):resolver.execute(second['resolution_id'])


def test_resolved_time_does_not_erase_a_different_business_scope_question():
    raw={'time_scope':scope(start_date='2022-01-01',end_date='2022-12-31'),
         'clarification':{'missing_input':'scope','reason':'国家范围尚未确定'}}
    bind_scope(raw,requirements('测试期间合计'),'测试期间合计')
    assert raw['clarification']['reason']=='国家范围尚未确定'
