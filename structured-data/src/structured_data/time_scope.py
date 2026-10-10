"""Validate a semantic time plan; never infer business dates from phrase rules."""
from datetime import date
import re


def human_text(question):
    # Server-added field identities can contain years; they are not user dates.
    return re.sub(r'\n(?:用户)?已确认字段：[^\n]*', '', question)


def task_evidence(question, previous=None):
    """Retain already validated user evidence when the bounded chat window rolls."""
    previous=previous or {}
    scope=(previous.get('task_requirements') or {}).get('time_scope') or {}
    return '\n'.join([*(previous.get('user_messages') or []),*scope.get('evidence',[]),question])


def validate_scope(raw, question):
    if not isinstance(raw,dict) or raw.get('kind') not in {'unbounded','range','dates','period','clarify'}:
        raise ValueError('时间方案缺少有效kind，尚未执行查询')
    kind=raw['kind']; evidence=raw.get('evidence',[]); text=human_text(question)
    if not isinstance(evidence,list) or len(evidence)>12 or any(not isinstance(s,str) or not s.strip() or s not in text for s in evidence):
        raise ValueError('时间方案的依据必须逐字来自用户原问题或补充说明')
    if kind!='unbounded' and not evidence:raise ValueError('时间方案缺少用户时间依据')
    explanation=raw.get('explanation','')
    if not isinstance(explanation,str) or not explanation.strip() or len(explanation)>600:raise ValueError('时间方案须说明本次范围的含义')
    def day(value):
        if value is None:return None
        if not isinstance(value,str) or not re.fullmatch(r'\d{4}-\d{2}-\d{2}',value):raise ValueError('时间方案日期必须为YYYY-MM-DD')
        try:return date.fromisoformat(value).isoformat()
        except ValueError:raise ValueError('时间方案包含不存在的日期') from None
    start,end=day(raw.get('start_date')),day(raw.get('end_date'))
    dates=raw.get('dates') or []
    if not isinstance(dates,list) or len(dates)>8:raise ValueError('时间方案dates必须为日期数组')
    dates=[day(d) for d in dates]
    if None in dates or len(set(dates))!=len(dates):raise ValueError('时间方案dates不得为空或重复')
    period=raw.get('period') or None
    if period is not None and (not isinstance(period,str) or period not in text):raise ValueError('期间标签须来自用户问题')
    if kind=='range' and (not(start or end) or dates or period):raise ValueError('范围方案须包含日期边界且不能混用日期列表或期间标签')
    if kind=='dates' and (not dates or start or end or period):raise ValueError('指定日期方案不能混用范围或期间标签')
    if kind=='period' and (not period or start or end or dates):raise ValueError('期间方案不能混用日期范围')
    if kind in {'clarify','unbounded'} and (start or end or dates or period):raise ValueError('待澄清或无时间限制方案不能携带执行日期')
    if start and end and start>end:raise ValueError('查询开始日期不能晚于结束日期')
    out=dict(kind=kind,evidence=evidence,explanation=explanation,start_date=start,end_date=end,dates=dates,period=period)
    if kind=='clarify':
        for key in ('question',):
            value=raw.get(key)
            if not isinstance(value,str) or not value.strip() or len(value)>600:raise ValueError('时间澄清须提供当前问题和输入提示')
            out[key]=value
        out['placeholder']='请填写起止时间，或用自己的话说明统计范围。'
    return out


def bind_scope(raw, req, question):
    """One validated scope owns every execution boundary, including cache hits."""
    if 'time_scope' not in raw:return
    scope=validate_scope(raw['time_scope'],question); raw['time_scope']=scope
    req['time_scope']=scope
    req['bounds']={k:scope[k] for k in ('start_date','end_date') if scope[k]}
    req['dates']=scope['dates']
    query=raw.setdefault('query',{})
    if not isinstance(query,dict):raise ValueError('查询操作格式无效')
    query.update(start_date=scope['start_date'],end_date=scope['end_date'],period=scope['period'])
    if scope['kind']=='clarify':
        raw['decision']='clarify';raw['question']=scope['question']
        raw['clarification']={'missing_input':'scope','reason':scope['explanation']}
