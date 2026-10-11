"""Capability contracts independent of Chinese question templates."""
import json
import pytest
from test_store import snapshot
from structured_data.table_analysis import analyze_table
from structured_data.result_evidence import result_evidence
from structured_data.field_resolver import FieldResolver


def test_sql_composes_grouping_window_and_arithmetic(snapshot):
    store,ds,_=snapshot
    out=analyze_table(store,ds,['price'],'''WITH monthly AS (
      SELECT strftime(date,'%Y-%m') AS month,avg(value) AS mean,count(value) AS n
      FROM data GROUP BY 1
    ) SELECT month,mean,n,mean-lag(mean) OVER (ORDER BY month) AS delta FROM monthly''')
    assert out['rows']==[['2023-09',15,2,None]]
    assert out['input_count']==3 and out['input_complete']
    assert out['inputs']['price']['actual_last_date']=='2023-09-23'
    assert out['source']['file']=='fixture.xlsx'


def test_analysis_isolated_to_selected_inputs_and_output_limit(snapshot):
    store,ds,_=snapshot
    out=analyze_table(store,ds,['price'],'SELECT series_id,value,source_cell FROM data ORDER BY date',limit=1)
    assert out['rows']==[['price',10,'B2']] and out['output_truncated']
    assert out['input_count']==3
    out=analyze_table(store,ds,['price'],'SELECT count(*) FROM data WHERE series_id=\'stock\'')
    assert out['rows']==[[0]]
    with pytest.raises(ValueError):analyze_table(store,ds,['price'],'SELECT * FROM observations')


@pytest.mark.parametrize('sql',['DELETE FROM data','SELECT 1;SELECT 2',"COPY data TO '/tmp/escape.csv'","SELECT * FROM read_csv('/etc/passwd')",'PRAGMA version'])
def test_analysis_refuses_mutation_external_reads_and_multiple_statements(snapshot,sql):
    store,ds,_=snapshot
    with pytest.raises(ValueError):analyze_table(store,ds,['price'],sql)
    assert store.query_series(ds,'price',operation='sum')['aggregate']==30


def test_analysis_dates_and_missing_values_preserve_source_truth(snapshot):
    store,ds,_=snapshot
    out=analyze_table(store,ds,['price','stock'],'''SELECT a.date,a.value-b.value AS diff,a.source_cell,b.source_cell
    FROM data a JOIN data b ON a.date=b.date WHERE a.series_id='price' AND b.series_id='stock' ''',start_date='2023-09-21',end_date='2023-09-21')
    assert out['rows']==[['2023-09-21',-20,'B2','C2']]
    out=analyze_table(store,ds,['price'],'SELECT count(value),count(*) FROM data')
    assert out['rows']==[[2,3]]


def test_bounded_evidence_retains_actual_coverage_and_calculation(snapshot):
    store,ds,_=snapshot
    source=store.query_series(ds,'price',operation='sum')
    answer={'operation':'sum','start_date':'2020-01-01','end_date':None,'sources':{'v1':source},'rounded_value':'30','explanation':'小计'}
    evidence=result_evidence(answer)
    assert evidence['sources']['v1']['actual_last_date']=='2023-09-23'
    assert evidence['sources']['v1']['valid_count']==2
    assert evidence['rounded_value']=='30'
    assert '2023-09-23' in __import__('structured_data.answer_text',fromlist=['summarize']).summarize(answer)


def test_attach_answer_saves_result_facts_without_unbounded_answer(snapshot):
    store,ds,_=snapshot
    resolver=FieldResolver(store,model_fn=lambda *a:None)
    plan={'status':'resolved','intent':'lookup','resolution_id':'a'*24,'dataset_id':ds,'fields':[{'candidates':[{'series_id':'price','date_mode':'date'}]}],'query':{'operation':'sum'},'task_requirements':{}}
    resolver._save(plan)
    resolver.execute=lambda *a:{'operation':'sum','sources':{'v1':store.query_series(ds,'price',operation='sum')}}
    resolver.attach_answer(plan)
    saved=json.loads(resolver._path(plan['resolution_id']).read_text())
    assert 'answer' not in saved
    assert saved['result_evidence']['sources']['v1']['aggregate']==30
    assert saved['result_evidence']['sources']['v1']['actual_last_date']=='2023-09-23'


def test_analysis_result_is_retrievable_and_nonfinite_values_are_not_numbers(snapshot):
    store,ds,_=snapshot
    out=analyze_table(store,ds,['price'],'SELECT 1.0/0.0 AS invalid')
    assert out['rows']==[[None]]
    saved=json.loads((store.root/'analysis-results'/(out['result_id']+'.json')).read_text())
    assert saved['result_id']==out['result_id'] and saved['input_count']==3
