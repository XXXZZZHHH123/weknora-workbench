"""Composable SQL analysis on bounded, isolated copies of authorized inputs."""
import datetime as dt
from decimal import Decimal
import threading
import math
import secrets
import os
import tempfile
import re
import duckdb

MAX_INPUT_ROWS = 50000


def analyze_table(store, dataset_id, series_ids, sql, start_date=None, end_date=None, limit=200):
    if not isinstance(series_ids,list) or not 1 <= len(series_ids) <= 16 or any(not isinstance(s,str) for s in series_ids):
        raise ValueError('series_ids需要1至16个真实字段ID')
    if not isinstance(sql,str) or not sql.strip() or len(sql)>16000:
        raise ValueError('请提供单条只读SELECT或WITH查询，最多16000字符')
    if type(limit) is not int or not 1<=limit<=500:
        raise ValueError('输出limit应为1至500')
    series_ids=list(dict.fromkeys(series_ids))
    series={sid:store._series(dataset_id,sid) for sid in series_ids}
    if any(s.get('chart_id') for s in series.values()):
        raise ValueError('分析请使用图表对应的source_series_id与明确日期范围，而不是图表包装ID')
    start=store._date(start_date);end=store._date(end_date)
    if start and end and start>end:raise ValueError('开始日期不能晚于结束日期')
    where='series_id IN ('+','.join('?' for _ in series_ids)+')'
    params=list(series_ids)
    if start:where+=' AND date>=?';params.append(start)
    if end:where+=' AND date<=?';params.append(end)
    with store._connect(dataset_id) as source:
        n=source.execute('SELECT count(*) FROM observations WHERE '+where,params).fetchone()[0]
        if n>MAX_INPUT_ROWS:raise ValueError('输入超过50000条；请按范围分步分析，未使用截断样本计算')
        inputs=source.execute('SELECT series_id,date,value,source_cell,date_cell,period,value_status FROM observations WHERE '+where+' ORDER BY series_id,date,source_cell',params).fetchall()
    provenance={}
    data=[]
    for sid,date,value,cell,date_cell,period,status in inputs:
        s=series[sid]
        data.append(dict(series_id=sid,date=date,value=value,source_cell=cell,date_cell=date_cell,period=period,value_status=status,metric=s['metric'],sheet=s['sheet'],unit=s.get('unit')))
    for sid,s in series.items():
        rows=[r for r in data if r['series_id']==sid]
        dates=[r['date'] for r in rows if r['date']]
        valid_dates=[r['date'] for r in rows if r['date'] and r['value'] is not None]
        provenance[sid]={'series':s,'input_count':len(rows),'valid_count':sum(r['value'] is not None for r in rows),
                         'actual_first_date':str(min(dates)) if dates else None,'actual_last_date':str(max(dates)) if dates else None,
                         'valid_first_date':str(min(valid_dates)) if valid_dates else None,'valid_last_date':str(max(valid_dates)) if valid_dates else None,
                         'example_cells':[r['source_cell'] for r in rows[:5]]}
    manifest=store.manifest(dataset_id)
    # The analysis connection has no dataset file attached. It contains only the
    # selected inputs, even if the model queries system catalogues or subqueries.
    with duckdb.connect(config={'enable_external_access':'false','threads':'2','memory_limit':'256MB'}) as con:
        statements=con.extract_statements(sql)
        if not re.match(r'^\s*(SELECT|WITH)\b',sql,re.I) or len(statements)!=1 or statements[0].type!=duckdb.StatementType.SELECT:
            raise ValueError('仅允许单条SELECT/WITH查询；修改、导入导出及配置命令不可执行')
        con.execute('CREATE TABLE data(series_id VARCHAR,date DATE,value DOUBLE,source_cell VARCHAR,date_cell VARCHAR,period VARCHAR,value_status VARCHAR,metric VARCHAR,sheet VARCHAR,unit VARCHAR)')
        if data:
            # Column types are explicit even when an entire column is NULL.
            con.execute('INSERT INTO data SELECT r.series_id,r.date,r.value,r.source_cell,r.date_cell,r.period,r.value_status,r.metric,r.sheet,r.unit FROM UNNEST(?::STRUCT(series_id VARCHAR,date DATE,value DOUBLE,source_cell VARCHAR,date_cell VARCHAR,period VARCHAR,value_status VARCHAR,metric VARCHAR,sheet VARCHAR,unit VARCHAR)[]) t(r)',[data])
        from .observation_coverage import summarize_observation_coverage
        for sid in series_ids:
            provenance[sid]['observation_coverage'] = summarize_observation_coverage(
                con, table='data', where='series_id = ?', params=[sid])
            provenance[sid]['observation_coverage_scope'] = 'loaded_analysis_inputs_before_user_sql'
        timer=threading.Timer(8,con.interrupt)
        timer.start()
        try:
            result=con.execute(sql)
            columns=[c[0] for c in result.description]
            if len(columns)>40:raise ValueError('结果列超过40列，请选择需要展示的输出')
            rows=result.fetchmany(limit+1)
        except duckdb.Error as exc:
            raise ValueError('分析SQL未完成：'+str(exc)[:800]+'；数据表名为data，可根据错误修正查询') from None
        finally:timer.cancel()
    def encode(v):
        if isinstance(v,(dt.date,dt.datetime)):return v.isoformat()
        if isinstance(v,float) and not math.isfinite(v):return None
        if isinstance(v,Decimal):return str(v)
        if isinstance(v,dict):return {k:encode(x) for k,x in v.items()}
        if isinstance(v,(list,tuple)):return [encode(x) for x in v]
        return v
    # Reject oversized cells rather than sending an unbounded string_agg/list.
    import json
    values=encode(rows[:limit])
    if len(json.dumps(values,ensure_ascii=False))>100000:raise ValueError('结果超过展示预算，请聚合或减少输出列；未展示截断数值')
    result=dict(kind='structured_analysis',status='ok',dataset_id=dataset_id,sql=sql,columns=columns,rows=values,
                output_truncated=len(rows)>limit,returned_row_count=len(values),input_complete=True,input_count=n,requested_range={'start_date':start_date,'end_date':end_date},
                inputs=provenance,source={'file':manifest['source_file'],'sha256':manifest['sha256']},
                notice='inputs是装载到隔离表的范围，series.first_date/last_date是原列可用范围，不等于SQL筛选后实际统计范围。时间限制请优先用start_date/end_date传入；聚合需在SQL中返回used_first_date、used_last_date、used_count，以供核对实际使用记录。原文件没有声明指标可加性和统计年度定义，不能假定这些业务规则。SQL在上述全部输入上执行；输出行截断不等于计算输入截断。inputs日期是输入覆盖范围，极值日期必须来自SQL返回记录。单位与业务口径按原字段，不自动换算。')

    # Durable bounded result facts allow retrieval after context compaction.
    result['result_id']=secrets.token_hex(12)
    directory=store.root/'analysis-results';directory.mkdir(exist_ok=True)
    fd,temporary=tempfile.mkstemp(prefix='.result-',dir=directory)
    try:
        with os.fdopen(fd,'w') as f:json.dump(result,f,ensure_ascii=False)
        os.replace(temporary,directory/(result['result_id']+'.json'))
    finally:
        if os.path.exists(temporary):os.unlink(temporary)
    return result
