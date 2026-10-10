"""Complete header inventory and deterministic grouping; never query observations."""
from collections import defaultdict
import json


def summary(catalog):
    sheets=defaultdict(set)
    for s in catalog:sheets[s['sheet']].add(s['metric'])
    return [{'sheet':sheet,'metrics':sorted(names)} for sheet,names in sorted(sheets.items())]


def validate_metadata(raw,catalog):
    scope=raw.get('metadata')
    if not isinstance(scope,dict):raise ValueError('目录查询需要metadata.sheets和metadata.metrics；只使用完整表头目录中的名称')
    definitions=scope.get('definitions',{})
    if not isinstance(definitions,dict) or len(definitions)>8 or any(not isinstance(v,str) or len(v)>600 for v in definitions.values()):raise ValueError('指标概念说明格式无效')
    if scope.get('series_ids') is not None:
        ids=scope['series_ids']
        if not isinstance(ids,list) or not ids or len(ids)>1000 or any(not isinstance(s,str) for s in ids):raise ValueError('目录字段ID列表无效')
        selected=[s for s in catalog if s['series_id'] in ids]
        if {s['series_id'] for s in selected}!=set(ids):raise ValueError('目录字段ID不属于真实文件目录')
        scope={'sheets':sorted({s['sheet'] for s in selected}),'metrics':sorted({s['metric'] for s in selected}),'series_ids':list(dict.fromkeys(ids))}
        if not set(definitions).issubset(set(ids)):raise ValueError('概念说明必须绑定已选的真实字段')
    sheets=scope.get('sheets');metrics=scope.get('metrics')
    if any(not isinstance(x,list) or not x or len(x)>1000 or any(not isinstance(v,str) for v in x) for x in (sheets,metrics)):
        raise ValueError('目录查询必须明确实际工作表及指标名称列表；不能把检索候选数量当作种类数')
    if not set(sheets).issubset({s['sheet'] for s in catalog}):raise ValueError('目录查询的工作表不在真实目录中')
    scoped=[s for s in catalog if s['sheet'] in sheets]
    if not set(metrics).issubset({s['metric'] for s in scoped}):raise ValueError('目录查询的字段不在指定工作表的真实目录中')
    clarify=raw.get('decision')=='clarify'
    if raw.get('decision') not in {'resolved','clarify'}:raise ValueError('目录查询的decision无效')
    missing=raw.get('clarification') or {}
    if clarify and (not isinstance(missing,dict) or missing.get('missing_input') not in {'scope','definition'} or not missing.get('reason')):
        raise ValueError('目录查询只能针对实际主题或范围歧义询问；不要求用户选择具体报价列或日期')
    return dict(kind='structured_field_resolution',status='needs_clarification' if clarify else 'resolved',intent='metadata',question=str(raw.get('question') or missing.get('reason') or ''),fields=[],
                requires_note=clarify,candidates_truncated=False,calculation={},query={'operation':'list_fields'},
                metadata={'sheets':sorted(set(sheets)),'metrics':sorted(set(metrics)),**({'series_ids':scope['series_ids']} if 'series_ids' in scope else {}),**({'definitions':definitions} if definitions else {})})


def answer(store,plan):
    catalog=store.catalog(plan['dataset_id'],sheet=plan.get('explicit_sheet'),question=plan.get('original_question',''))
    validate_metadata({'metadata':plan['metadata'],'decision':'resolved'},catalog)
    file=store.manifest(plan['dataset_id'])['source_file']
    groups=[]
    for metric in plan['metadata']['metrics']:
        sources=[{k:s.get(k) for k in ('series_id','sheet','metric','unit','header_cells','dimensions')}
                 for s in catalog if s['sheet'] in plan['metadata']['sheets'] and s['metric']==metric and ('series_ids' not in plan['metadata'] or s['series_id'] in plan['metadata']['series_ids'])]
        sources=list({json.dumps(s,sort_keys=True,ensure_ascii=False):s for s in sources}.values())
        groups.append({'metric':metric,'source_count':len(sources),'sources':sources[:8],
                       'sources_truncated':len(sources)>8,'definition':next((plan['metadata'].get('definitions',{}).get(s['series_id']) for s in sources if plan['metadata'].get('definitions',{}).get(s['series_id'])),None)})
    result={'intent':'metadata','operation':'list_fields','count':len(groups),'groups':groups,'sources':{},
            'file':file,'sheets':plan['metadata']['sheets'],'complete':True,
            'notice':'按完整已导入字段目录的原指标名称分类；不同合约列不增加指标种类。证据来自表头，不要求日期，也未查询报价数值。'}
    categories=defaultdict(set)
    for g in groups:
        for s in g['sources']:categories[s.get('unit') or '原表未注明单位'].add(g['metric'])
    result['unit_groups']={u:sorted(v) for u,v in categories.items()}
    from .answer_text import summarize
    result['summary']=summarize(result)
    return result
