"""Validate model-owned plans and execute bounded deterministic analysis."""
from copy import deepcopy
from datetime import date, timedelta
import math


class TaskPlanError(ValueError):
    """The semantic task itself needs repair, rather than a new field binding."""


def requirements(_question=None):
    """Empty execution options. Natural language is interpreted only by the model."""
    return {'bounds': {}, 'dates': [], 'filters': []}


def normalize_proposal(raw, req, question, prior=()):
    """Validate the model's plan without inferring or rewriting its intent."""
    if not isinstance(raw, dict):
        raise ValueError('任务模型没有返回JSON对象，尚未查询数值')
    raw = deepcopy(raw)
    req.clear(); req.update(requirements())
    if raw.get('intent') == 'unsupported':
        raise ValueError('当前无法执行所请求任务：' + str(raw.get('reason') or '需要未支持的运算能力')[:400])
    if raw.get('intent') == 'metadata':
        req['metadata'] = True
        return raw
    from .time_scope import bind_scope
    bind_scope(raw, req, question)
    features = raw.get('presentation') or {}
    if not isinstance(features, dict) or set(features) - {'analysis', 'chart', 'explain_cumulative'}:
        raise ValueError('任务呈现方案包含不支持的属性')
    if features.get('analysis') not in (None, 'correlation', 'trend', 'forecast', 'causality'):
        raise ValueError('当前不支持该分析，未执行替代运算')
    for key in ('chart', 'explain_cumulative'):
        if key in features and not isinstance(features[key], bool):
            raise ValueError('任务呈现开关必须是布尔值')
    req.update({k: v for k, v in features.items() if v})
    fields = raw.get('fields', [])
    query = raw.setdefault('query', {})
    if not isinstance(fields, list) or any(not isinstance(f, dict) for f in fields) or not isinstance(query, dict):
        raise ValueError('任务字段或查询格式无效')
    # Optional JSON null is absence, not a different semantic operation.
    for key in ('last_n','top_n','order','value_filters'):
        if query.get(key,'absent') is None:query.pop(key)
    for field in fields:
        if field.get('operation','absent') is None:field.pop('operation')
    linked=raw.get('same_series_roles') or []
    if not isinstance(linked,list) or any(r not in {f.get('role') for f in fields} for r in linked):
        raise ValueError('same_series_roles只能包含已有的v1至v8角色')
    if len(linked)<2:raw.pop('same_series_roles',None)
    for key in ('last_n', 'top_n'):
        if key in query and (type(query[key]) is not int or not 1 <= query[key] <= 200):
            raise ValueError('窗口或排序条数必须是1至200的整数')
    if 'order' in query and query['order'] not in ('asc', 'desc'):
        raise ValueError('排序方向无效')
    if query.get('top_n') is not None and query.get('operation') != 'top':
        raise ValueError('top_n仅用于top数值排名；最近日期窗口使用last_n')
    filters=query.get('value_filters', [])
    if not isinstance(filters,list) or len(filters)>8 or any(not isinstance(f,dict) or set(f)!={'op','value'} or f['op'] not in ('gt','gte','lt','lte','eq','ne') or type(f['value']) not in (int,float) or not math.isfinite(f['value']) for f in filters):
        raise ValueError('数值筛选条件格式无效')
    # The model can label sequence analysis as 'calculation' in ordinary
    # language. With rows + explicit presentation and no scalar formula, this
    # is the same typed execution plan, not a different user intent.
    calc=raw.get('calculation') or {}
    if (req.get('analysis') or req.get('chart')) and query.get('operation')=='rows' and raw.get('intent')=='calculation' and not calc.get('expression') and not calc.get('outputs') and not raw.get('outputs'):
        raw['intent']='lookup'
        raw.pop('calculation',None)
    if (req.get('analysis') or req.get('chart')) and (query.get('operation') != 'rows' or raw.get('intent') != 'lookup'):
        raise ValueError('analysis/chart需要query.operation=rows、intent=lookup及每个输入系列一个角色；不能同时携带标量calculation表达式')
    if req.get('analysis') == 'correlation' and len(fields) != 2:
        raise ValueError('相关分析需要两个字段')
    if (req.get('analysis') in ('trend', 'forecast', 'causality') or req.get('chart')) and len(fields) != 1:
        raise ValueError('当前该分析或图表支持一个字段')
    # Dates are model-selected, but must agree with its validated time scope.
    scope = req.get('time_scope')
    if scope:
        for field in fields:
            if field.get('date') and field['date'] not in scope['dates']:
                raise ValueError('角色日期不属于任务时间方案')
        if len(scope['dates'])>1 and {f.get('date') for f in fields}!=set(scope['dates']):
            raise ValueError('多个指定日期须完整绑定到各数值角色，不能丢弃日期或取无范围数据')
        if scope['kind'] == 'dates' and len(scope['dates']) == 1 and query.get('operation') != 'rows':
            query.update(start_date=scope['dates'][0], end_date=scope['dates'][0])
    if query.get('operation') == 'latest': req['latest'] = True
    return raw


def analyze(kind, sources, target_date=None):
    """Calculations reference actual source rows; no interpolation or inference."""
    good={role:[r for r in s['rows'] if r['value'] is not None and r.get('date')] for role,s in sources.items()}
    if kind=='correlation':
        if len(good)!=2:raise ValueError('相关分析需要两个已确定字段')
        left,right=list(good.values());a={r['date']:r for r in left};b={r['date']:r for r in right}
        days=sorted(a.keys()&b.keys())
        if len(days)<3:raise ValueError('共同有效日期少于3个，无法计算相关系数；不填补缺值')
        av=[a[d]['value'] for d in days];bv=[b[d]['value'] for d in days]
        am=sum(av)/len(days);bm=sum(bv)/len(days)
        va=sum((x-am)**2 for x in av);vb=sum((x-bm)**2 for x in bv)
        if not va or not vb:raise ValueError('其中一个字段为常数，Pearson相关系数无定义')
        value=sum((x-am)*(y-bm) for x,y in zip(av,bv))/math.sqrt(va*vb)
        return {'label':'Pearson相关系数','value':value,'sample_count':len(days),'aligned_rows':[{'date':d,'left_value':a[d]['value'],'left_cell':a[d]['source_cell'],'right_value':b[d]['value'],'right_cell':b[d]['source_cell']} for d in days[:200]],'aligned_rows_truncated':len(days)>200,'notice':'仅使用全部共同有效日期计算，不填补缺值；相关不证明因果。'+('对齐明细仅展示前200个日期，统计使用全部'+str(len(days))+'个日期。' if len(days)>200 else '')}
    if len(good)!=1:raise ValueError('此分析需要一个已确定字段')
    rows=next(iter(good.values()))
    if not rows:raise ValueError('指定范围没有有效值，无法分析')
    rows=sorted(rows,key=lambda r:r['date'])
    if kind=='forecast':
        last=rows[-1];target=target_date or (date.today()+timedelta(days=1)).isoformat()
        age=(date.fromisoformat(target)-date.fromisoformat(last['date'])).days
        if age<=0 or age>31:
            return {'label':'预测数据边界','last_observation':last,'target_date':target,'notice':'最后有效记录为'+last['date']+'，不能据此可靠预测'+target+'；没有将历史值当作未来成交价。'}
        if len(rows)<20:raise ValueError('有效历史记录少于20条，未输出预测值')
        returns=[math.log(b['value']/a['value']) for a,b in zip(rows,rows[1:]) if a['value']>0 and b['value']>0]
        if len(returns)<19:raise ValueError('正值历史记录不足，未输出预测值')
        mean=sum(returns)/len(returns);vol=math.sqrt(sum((r-mean)**2 for r in returns)/(len(returns)-1))
        return {'label':'持平基线预测（不是成交事实）','value':last['value'],'target_date':target,'last_observation':last,'interval':[last['value']*math.exp(-1.96*vol*math.sqrt(age)),last['value']*math.exp(1.96*vol*math.sqrt(age))], 'notice':'采用最近有效值持平基线；区间用历史日变动波动率估计，非价格保证，未考虑新信息。'}
    first,last=rows[0],rows[-1];peak=first;max_dd=0;pair=None
    for r in rows:
        if r['value']>peak['value']:peak=r
        if peak['value']<=0:continue
        dd=(peak['value']-r['value'])/peak['value']
        if dd>max_dd:max_dd=dd;pair={'peak':peak,'trough':r}
    result={'label':'期间趋势','first':first,'last':last,'change':last['value']-first['value'],'max_drawdown_percent':max_dd*100,'drawdown_sources':pair,'sample_count':len(rows),'max':max(rows,key=lambda r:r['value']),'min':min(rows,key=lambda r:r['value'])}
    if kind=='causality':result['notice']='文件数值可证明上述变动，不能单凭价格序列证明变动原因；供需、政策等原因须有额外证据，本次没有把推测当作文件事实。'
    return result
