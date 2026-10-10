"""Source-independent task requirements and bounded, deterministic analysis.

No commodity names or aliases live here. Field identity is the semantic
resolver's job; explicit dates/filters/output obligations cannot be discarded
by its proposed plan. Unknown tasks fail visibly rather than run a substitute.
"""
from copy import deepcopy
from datetime import date, timedelta
import math
import re


def literal_scope_matches(question, series):
    """A header fast path cannot discard an extra country/product qualifier."""
    fields=series if isinstance(series,list) else [series]
    text=question
    for field in fields:
        text=text.replace(field['metric'],' ')
        for value in field.get('dimensions',{}).values():
            if isinstance(value,(str,int,float)):text=text.replace(str(value),' ')
    text=re.sub(r'\d{4}[-/年]\d{1,2}[-/月]\d{1,2}日?|\d{4}年\d{1,2}月|\d{4}年',' ',text)
    words=('哪一天','有多少货','分别是哪天','的时候','最近','最新','平均数','平均','均值','交易日','记录','排除','空值','后的','最高','最低','最大','最小','最多','最少','差值','多少','哪天','一共','合计','超过','大于','不低于','不超过','至少','至多','小于','低于','首尾差','最大回撤','总体是涨还是跌','折线图','画成','标出','相关系数','共同日期','对齐','不填补','Pearson','按','算','有','是','在','把','分别','和','的','呢','条','个','天','价')
    for word in sorted((*words,'相关吗'),key=len,reverse=True):text=text.replace(word,' ')
    parts=re.findall(r'[A-Za-z]+|[\u4e00-\u9fff]+',text)
    scope=' '.join(field['sheet']+' '+str(field.get('dimensions',{})) for field in fields)
    return all(part in scope for part in parts)


def requirements(question):
    # Confirmed JSON is identity; the human's note still supplies constraints.
    q=re.sub(r'\n(?:用户)?已确认字段：[^\n]*','',question)
    result={'bounds':{}, 'dates':[], 'filters':[]}
    if '合约' in q:
        without_dates=re.sub(r'\d{4}[-/年]\d{1,2}(?:[-/月]\d{1,2}日?)?月?',' ',q)
        months=[int(n) for n in re.findall(r'(?<!\d)(0?[1-9]|1[0-2])(?:月(?:份)?|合约)',without_dates)]
        if months:result['contract_months']=list(dict.fromkeys(months))
    from .time_scope import calendar_scope
    result.update(calendar_scope(question))
    if re.search(r'有哪些|有几种|多少种|价格种类|价格类型|几种价格|几种指标|都有什么|什么库存数据|字段单位',q):result['metadata']=True
    if result.get('metadata') and '库存' in q and not re.search(r'负荷|开机|订单|成交|上市|出库|入库|产量|需求|价格|报价',q):result['inventory_directory_only']=True
    if re.search(r'最近|最新|现在|目前|当前',q):result['latest']=True
    numerals={'一':1,'两':2,'二':2,'三':3,'四':4,'五':5,'六':6,'七':7,'八':8,'九':9,'十':10}
    def number(s):return int(s) if s.isdigit() else numerals[s]
    window=re.search(r'最近\s*(\d+|[一两二三四五六七八九十])\s*(?:个)?(?:交易日|天|条记录)',q)
    if window:
        n=number(window[1])
        if not 1<=n<=200:raise ValueError('本次窗口支持1至200条原表日期记录')
        result['last_n']=n;result.pop('latest',None)
    top=re.search(r'(最高|最低|最大|最小)(?:的)?\s*(\d+|[一两二三四五六七八九十])\s*(?:个|条|天|日)',q)
    if top:
        result['top_n']=number(top[2]);result['order']='desc' if top[1] in ('最高','最大') else 'asc'
        if not 1<=result['top_n']<=200:raise ValueError('本次排序支持前1至200条记录')
    for words,op in [('(?<!不)超过|大于(?!等于)','gt'),('不低于|至少|大于等于','gte'),('(?<!不)低于|小于(?!等于)','lt'),('不超过|至多|小于等于','lte')]:
        for value,scale in re.findall('(?:'+words+r')\s*(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)\s*(万|千|亿|[kKmM])?',q):
            result['filters'].append({'op':op,'value':float(value)*{'万':10000,'千':1000,'亿':1e8,'k':1000,'m':1e6,'':1}.get(scale.lower(),1)})
    if re.search(r'排除\s*0|剔除\s*0|去掉\s*0|非零',q):result['filters'].append({'op':'ne','value':0})
    if re.search(r'多少(?:个)?(?:交易日|天|条)|有几(?:天|条)',q) and (result['filters'] or result['bounds'] or '记录' in q):result['aggregate']='count'
    elif re.search(r'平均|均值|平均数',q):result['aggregate']='mean'
    elif re.search(r'加起来|加总|总和|合计|一共',q):result['aggregate']='sum'
    if re.search(r'最高|最大|最多',q):result['max']=True
    if re.search(r'最低|最小|最少',q):result['min']=True
    if len(result['dates'])==1 and not result['bounds'] and not re.search(r'哪天|哪一天|历史|期间',q):
        # "2023/9/7最高价" can name a daily-high column, not global max.
        result.pop('max',None);result.pop('min',None)
    if result.get('max') and result.get('min') and not top:result['extrema']=True
    if re.search(r'百分之几|百分比|涨幅|跌幅',q):result['percentage']=True
    if re.search(r'(?:按|用).{0,8}汇率|折成|折算成|换算成',q):
        result['currency_conversion']=True
        target=re.search(r'(?:折成|折算成|换算成).{0,3}(人民币|美元|欧元)',q)
        if target:result['target_currency']={'人民币':'CNY','美元':'USD','欧元':'EUR'}[target[1]]
    if re.search(r'除以|减去|减|加上|加(?!(?:起来|总|是不是|是否|会不会|会|不|了|的话|可以|能))|差值|差多少|比.*高多少|比.*低多少',q):result['arithmetic']=True
    if len(result['dates'])>=2 and re.search(r'比|相比|涨|跌|差',q) and not re.search(r'至|范围',q):result['date_change']=True
    if re.search(r'相关系数|Pearson|相关吗',q,re.I):result['analysis']='correlation'
    elif re.search(r'最大回撤|总体.*(?:涨|跌)|首尾差',q):result['analysis']='trend'
    elif re.search(r'预测',q):result['analysis']='forecast'
    elif re.search(r'为什么|原因|证明.*原因',q):result['analysis']='causality'
    if re.search(r'画.*图|绘.*图|折线图|画成',q):result['chart']=True
    if re.search(r'会.*重复|重复计算|重复计',q):result['explain_cumulative']=True
    if result.get('last_n')==1 and not result.get('aggregate') and not result.get('analysis') and not result.get('chart'):
        result['latest']=True;result.pop('last_n',None)
    return result


def normalize_proposal(raw, req, question, prior=()):
    """Bind explicit requirements without selecting an ambiguous source."""
    if not isinstance(raw,dict):raise ValueError('字段模型没有返回JSON对象，尚未查询数值')
    raw=deepcopy(raw)
    if raw.get('intent')=='metadata':return raw
    fields=raw.get('fields',[]);query=raw.setdefault('query',{})
    if not isinstance(fields,list) or any(not isinstance(f,dict) for f in fields):raise ValueError('候选字段格式无效，未执行查询')
    for field in fields:
        if field.get('date') and (req['bounds'] or field['date'] not in req['dates']):field.pop('date')
    if req.get('bounds'):
        query.update(req['bounds']);query['period']=None
    for key in ('last_n','top_n','order'):
        if key in req:query[key]=req[key]
    if req['filters']:
        query['value_filters']=req['filters']
        for key in ('filter','filters','condition','conditions','value_filter','value_condition'):query.pop(key,None)
    if req.get('last_n') and query.get('period') and query['period'] in question:query['period']=None
    if req.get('analysis') or req.get('chart'):
        query['operation']='rows';raw['intent']='lookup';raw['calculation']={}
        for f in fields:f.pop('operation',None)
    elif req.get('top_n'):
        query['operation']='top';raw['intent']='lookup';raw['calculation']={}
        ids=[set(f.get('candidate_ids',[])) for f in fields]
        if len(fields)>1 and ids[0] and all(s==ids[0] for s in ids):fields=fields[:1];raw['fields']=fields
        for field in fields:field.pop('operation',None)
    elif req.get('aggregate'):
        if not any(req.get(k) for k in ('arithmetic','percentage','currency_conversion')):
            # An average/sum/count is owned by the executor. It is not an
            # arbitrary symbolic expression proposed by a language model.
            raw['intent']='lookup';raw['calculation']={};query['operation']=req['aggregate']
            ids=[set(f.get('candidate_ids',[])) for f in fields]
            if len(fields)>1 and ids[0] and all(s==ids[0] for s in ids):fields=fields[:1];raw['fields']=fields
            for field in fields:field.pop('operation',None)
        elif raw.get('intent')=='calculation' and len(fields)>1:
            if req.get('bounds'):
                query['operation']='statistics'
                for field in fields:field['operation']=req['aggregate']
        else:query['operation']=req['aggregate']
    elif req.get('latest'):query['operation']='latest'
    if req.get('extrema') and fields and not req.get('analysis') and not req.get('chart'):
        # The model may correctly identify unknown definitions without supplying
        # operations. The task supplies operations; choices stay unconfirmed.
        if len(fields)==1:
            fields=[dict(fields[0],role='v1',label='最高值'),dict(fields[0],role='v2',label='最低值')];raw['fields']=fields
        if len(fields)==2:
            for f,op in zip(fields,('max','min')):f['operation']=op
            query['operation']='statistics';raw['intent']='calculation'
            raw['calculation']={'expression':fields[0]['role']+'-'+fields[1]['role'],'unit':'原表未注明单位','explanation':'同一口径最高值减最低值'}
    elif req.get('min') and not req.get('analysis') and not req.get('chart') and not req.get('top_n'):query['operation']='min'
    elif req.get('max') and not req.get('analysis') and not req.get('chart') and not req.get('top_n'):query['operation']='max'
    if req.get('date_change') and fields:
        field=fields[0];raw['fields']=[dict(field,role='v1',date=req['dates'][0]),dict(field,role='v2',date=req['dates'][1])]
        raw['intent']='calculation';query['operation']='rows';query['start_date']=query['end_date']=None
        raw['calculation']={'expression':'v1-v2','unit':'原表未注明单位','explanation':'指定日期原值之差'}
        if req.get('percentage'):raw['calculation']['outputs']=[{'label':'差值','expression':'v1-v2','unit':'原表未注明单位'}, {'label':'涨幅','expression':'(v1-v2)/v2*100','unit':'%'}]
    if req.get('needs_period_definition'):
        # Preserve the selected fields before asking for an actual calendar
        # boundary. A crop-year string is not a monthly table's period label.
        query.update(operation='rows',start_date=None,end_date=None,period=None)
        for f in raw['fields']:f.pop('operation',None)
    # Preserve already confirmed entity dimensions when moving to another metric.
    contracts={s.get('dimensions',{}).get('contract') for s in prior if s.get('dimensions',{}).get('contract')}
    if len(contracts)==1 and not re.search(r'换|合约|[A-Za-z]+\d',question,re.I):raw['_inherit_contract']=next(iter(contracts))
    if query.get('operation')=='rows' and query.get('start_date')==query.get('end_date') and query.get('start_date'):
        raw['_point_date']=query.pop('start_date');query.pop('end_date',None)
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
