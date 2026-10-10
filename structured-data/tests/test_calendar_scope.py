import calendar

import pytest

from structured_data.field_resolver import FieldResolver
from structured_data.store import DataStore
from structured_data.task_plan import requirements


@pytest.mark.parametrize('note,start,end', [
    ('两年整年的', '2022-01-01', '2023-12-31'),
    ('两个完整自然年', '2022-01-01', '2023-12-31'),
    ('2022年1月到23年12月', '2022-01-01', '2023-12-31'),
    ('2022年1月到2023年12月', '2022-01-01', '2023-12-31'),
    ('2022年到2023年', '2022-01-01', '2023-12-31'),
    ('2022和2023年', '2022-01-01', '2023-12-31'),
    ('2022年9月至23年8月', '2022-09-01', '2023-08-31'),
    ('2023年1月到12月', '2023-01-01', '2023-12-31'),
    ('按2022年9月1日到2023年8月31日求和', '2022-09-01', '2023-08-31'),
])
def test_calendar_clarification_overrides_business_year(note,start,end):
    q='2022/23年度产量加进口合计多少？\n已确认字段：{"date":"2016-09-30"}\n用户补充：'+note
    r=requirements(q)
    assert r['bounds']==dict(start_date=start,end_date=end)
    assert not r.get('needs_period_definition')


def test_later_correction_wins_and_unparsed_end_is_not_dropped():
    q='2022/23年度合计\n用户补充：2022年9月到2023年8月\n用户补充：不对，是两年整年的'
    assert requirements(q)['bounds']==dict(start_date='2022-01-01',end_date='2023-12-31')
    with pytest.raises(ValueError,match='终点'):
        requirements('2022年1月到下下个月，产量总和多少')
    with pytest.raises(ValueError,match='开始日期'):
        requirements('2023年12月到22年1月产量合计')
    assert requirements('2022/23年度产量加进口合计')['needs_period_definition']


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
    def proposal(_):
        return dict(intent='calculation',decision='resolved',fields=[dict(role='v1',label='产量',candidate_ids=['p']),dict(role='v2',label='进口',candidate_ids=['i'])],query={'operation':'rows'},calculation={'expression':'v1+v2','unit':'原表未注明单位','explanation':'完整两年总量，这是错误的模型文案'})
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
