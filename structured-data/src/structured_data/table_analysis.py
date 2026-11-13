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


ANALYSIS_INPUT_COLUMNS = (
    ('series_id', 'VARCHAR'), ('date', 'DATE'), ('value', 'DOUBLE'),
    ('source_cell', 'VARCHAR'), ('date_cell', 'VARCHAR'), ('period', 'VARCHAR'),
    ('value_status', 'VARCHAR'), ('metric', 'VARCHAR'), ('sheet', 'VARCHAR'), ('unit', 'VARCHAR'),
)


class AnalysisSQLError(ValueError):
    """A rejected SQL request, never an analysis result or rewritten query."""
    def __init__(self, cause, stage):
        super().__init__('分析SQL未完成：'+str(cause)[:800]+'；数据表名为data，可根据错误修正查询')
        self.stage = stage

    def diagnostic(self):
        return {'code': 'analysis_sql_'+self.stage+'_error', 'stage': 'sql_'+self.stage,
                'query_completed': False,
                'sql_contract': {'dialect': 'DuckDB', 'table': 'data', 'read_only': True,
                                 'columns': [{'name': name, 'type': dtype} for name, dtype in ANALYSIS_INPUT_COLUMNS]}}


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
        try:
            statements=con.extract_statements(sql)
        except duckdb.Error as exc:
            # Parsing happens before execute(), so syntax errors must use the
            # same corrective tool response as binding/execution errors.
            raise AnalysisSQLError(exc, 'parse') from None
        if not re.match(r'^\s*(SELECT|WITH)\b',sql,re.I) or len(statements)!=1 or statements[0].type!=duckdb.StatementType.SELECT:
            raise ValueError('仅允许单条SELECT/WITH查询；修改、导入导出及配置命令不可执行')
        con.execute('CREATE TABLE data('+','.join(name+' '+dtype for name,dtype in ANALYSIS_INPUT_COLUMNS)+')')
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
            logical_types=[str(c[1]) for c in result.description]
            if len(columns)>40:raise ValueError('结果列超过40列，请选择需要展示的输出')
            rows=result.fetchmany(limit+1)
            from .result_contract import excel_value_unit, unknown_unit
            from .sql_result_units import infer_sql_units, describe_excel_dependencies
            source_columns=[name for name,_ in ANALYSIS_INPUT_COLUMNS]
            source_units={name:unknown_unit() for name in source_columns}
            source_units['value']=excel_value_unit(series,manifest['sha256'])
            units=infer_sql_units(con,sql,source_units,len(columns))
            from .sql_result_operations import describe_sql_operations
            operations=describe_sql_operations(con,sql,len(columns))
            dependencies,dependency_verified=describe_excel_dependencies(con,sql,source_columns,len(columns))
        except duckdb.Error as exc:
            raise AnalysisSQLError(exc, 'execute') from None
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
                inputs=provenance,source=store.result_source(dataset_id,manifest),
                notice='inputs是装载到隔离表的范围，series.first_date/last_date是原列可用范围，不等于SQL筛选后实际统计范围。时间限制请优先用start_date/end_date传入；聚合需在SQL中返回used_first_date、used_last_date、used_count，以供核对实际使用记录。原文件没有声明指标可加性和统计年度定义，不能假定这些业务规则。SQL在上述全部输入上执行；输出行截断不等于计算输入截断。inputs日期是输入覆盖范围，极值日期必须来自SQL返回记录。单位与业务口径按原字段，不自动换算。')

    # Durable bounded result facts allow retrieval after context compaction.
    result['result_id']=secrets.token_hex(12)
    from .result_contract import attach_result_contract, snapshot, source_column, sql_execution, typed_sha256
    # Dates in the loaded input digest use exactly the ISO representation exposed
    # in the response. Scope is all loaded inputs, never aggregate membership.
    attach_result_contract(result,source_snapshot=snapshot(result['source'],kind='excel_observations',
        dataset_id=dataset_id,scope='loaded_inputs_before_user_sql',input_data=encode(data),
        versions={'manifest_sha256':typed_sha256(manifest)}),
        execution=sql_execution(sql,duckdb.__version__,'DuckDB DOUBLE source values; DECIMAL result values encoded as strings'),
        columns=[source_column(name,i,logical_types[i],unit=units[i],dependency=dependencies[i],operation=operations[i])
                 for i,name in enumerate(columns)],dependency_verified=dependency_verified)
    directory=store.root/'analysis-results';directory.mkdir(exist_ok=True)
    fd,temporary=tempfile.mkstemp(prefix='.result-',dir=directory)
    try:
        with os.fdopen(fd,'w') as f:json.dump(result,f,ensure_ascii=False)
        os.replace(temporary,directory/(result['result_id']+'.json'))
    finally:
        if os.path.exists(temporary):os.unlink(temporary)
    return result
