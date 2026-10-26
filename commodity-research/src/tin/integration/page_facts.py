"""Small page read models from current source grants and saved real-ID bindings."""
from contextlib import contextmanager
from contextvars import ContextVar
from datetime import date, datetime
import hashlib
import json
import math
import re

import httpx
from fastapi import HTTPException

from tin.config import SHANGHAI, settings

_page = ContextVar('research_page_read', default=None)
MODULES = {'home','market','indicators','judgments','strategies','diagram','reports'}
SERIES = re.compile(r'kb-[0-9a-f]{48}\Z')
LEGACY_ID = re.compile(r'[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}\Z')


@contextmanager
def page_read_scope(module, *, series_ids=None, day=None, template_id=None, points_per_series=None):
    if module not in MODULES:
        raise HTTPException(400, '研究页面模块无效')
    if points_per_series is not None and (type(points_per_series) is not int or not 1 <= points_per_series <= 2000):
        raise HTTPException(400, '页面所需观测点数无效')
    from tin.integration.workbench_scope import current_workbench
    active = current_workbench.get()
    pattern = SERIES if active is not None and active.scope_version == 2 else LEGACY_ID
    if series_ids is not None and (not isinstance(series_ids, (list, tuple)) or len(series_ids) > 256
            or any(not isinstance(value, str) or not pattern.fullmatch(value) for value in series_ids)
            or len(set(series_ids)) != len(series_ids)):
        raise HTTPException(422, '页面指标参数无效或超过256项')
    if day is not None:
        try:
            day = day.isoformat() if type(day) is date else day
            if not isinstance(day, str) or date.fromisoformat(day).isoformat() != day:
                raise ValueError
        except ValueError:
            raise HTTPException(400, '页面日期需要明确为YYYY-MM-DD') from None
    previous = _page.get()
    if previous and previous['module'] == module:
        series_ids = previous['series_ids'] if series_ids is None else series_ids
        day = previous['day'] if day is None else day
        template_id = previous['template_id'] if template_id is None else template_id
        points_per_series = previous.get('points') if points_per_series is None else points_per_series
    marker = _page.set({'module':module,'series_ids':series_ids,'day':day,'template_id':template_id,'points':points_per_series})
    try:
        yield
    finally:
        _page.reset(marker)


def _request(scope, body):
    from tin.integration.workbench_scope import current_workbench
    from tin.provenance.bridge import structured_request
    marker = current_workbench.set(scope)
    try:
        response = structured_request('POST','/api/research/facts',body,return_error_response=True)
        if response.status_code==413:
            raise HTTPException(413,'当前来源目录或必要读取超过容量限制，请分批处理或拆分来源；未返回部分数据')
        if response.status_code in {400,401,403,404,409}:
            raise HTTPException(response.status_code, '数据来源、授权或所需指标已变化，请刷新后核对')
        response.raise_for_status()
        result = response.json()
        if (not isinstance(result, dict) or result.get('scope_version') != 2
                or result.get('workbench_id') != scope.workbench_id
                or type(result.get('sources_revision')) is not int or result['sources_revision'] != scope.sources_revision
                or not isinstance(result.get('version'), str) or not re.fullmatch('[0-9a-f]{64}',result['version'])):
            raise ValueError('Invalid current source response')
        return result
    except HTTPException:
        raise
    except (httpx.HTTPError,ValueError,TypeError,AttributeError,KeyError,OSError):
        raise HTTPException(503, '当前数据来源暂时不可读；未加载历史副本或全量事实') from None
    finally:
        current_workbench.reset(marker)


def catalog_page(scope, offset=0, limit=200, search=None, series_ids=None, *, version=None, source_kind=None):
    if type(offset) is not int or offset < 0 or type(limit) is not int or not 1 <= limit <= 200:
        raise HTTPException(400, '指标目录分页无效')
    if source_kind is not None and (not isinstance(source_kind,str) or source_kind not in {'macro','industry'}):
        raise HTTPException(400,'目录来源范围无效')
    if search is not None and (not isinstance(search,str) or len(search)>200):
        raise HTTPException(400,'目录检索参数无效')
    if scope.scope_version != 2:
        return _legacy_catalog_page(scope,offset,limit,search,series_ids,version,source_kind)
    body = {'source_bindings':[source['id'] for source in scope.sources if not source['delegated']],
            'catalog_only':True,'offset':offset,'limit':limit}
    if search is not None: body['search'] = search
    if source_kind is not None: body['source_kind'] = source_kind
    if series_ids is not None: body['series_ids'] = list(series_ids)
    if version is not None: body['version'] = version
    result = _request(scope, body)
    if (result.get('catalog_only') is not True or not isinstance(result.get('series'),list)
            or type(result.get('total')) is not int or result['total'] < 0 or result.get('offset') != offset
            or len(result['series']) > limit or not isinstance(result.get('datasets'),list)):
        raise HTTPException(503, '来源指标目录分页不完整')
    direct = {source['id']:source for source in scope.sources if not source['delegated']}
    for field in result['series']:
        source = direct.get(field.get('source_binding_id')) if isinstance(field,dict) else None
        if (source is None or not isinstance(field.get('series_id'),str) or not SERIES.fullmatch(field['series_id'])
                or field.get('knowledge_base_id') != source['knowledge_base_id']
                or field.get('knowledge_base_tenant_id') != source['tenant_id']):
            raise HTTPException(503, '目录项超出当前直接来源授权')
    return result


def catalog_snapshot(scope):
    catalog, datasets, offset, version, total = {}, [], 0, None, None
    while True:
        page = catalog_page(scope,offset=offset,limit=200,version=version)
        if version is None:
            version, total, datasets = page['version'],page['total'],page.get('datasets',[])
            if total > 10000:
                raise HTTPException(413, '本次模型分析目录超过10000项，请缩小明确的数据来源范围')
        if page['version'] != version or page['total'] != total:
            raise HTTPException(409, '分析目录在读取期间改变，请刷新后重试')
        for field in page['series']:
            if field['series_id'] in catalog:
                raise HTTPException(503, '指标目录分页出现重复')
            catalog[field['series_id']] = field
        next_offset = page.get('next_offset')
        if next_offset is None:
            if page.get('truncated') or len(catalog) != total:
                raise HTTPException(503, '分析目录未完整读取，未使用截断目录')
            return {'catalog':catalog,'version':version,'datasets':datasets}
        if not page['series'] or next_offset != offset+len(page['series']):
            raise HTTPException(503, '分析目录分页进度无效')
        offset = next_offset


def _legacy_catalog_page(scope, offset, limit, search, series_ids, requested, source_kind=None):
    from dataclasses import replace
    from tin.integration.kb_facts import current_sources
    from tin.integration.workbench_scope import current_workbench
    from tin.provenance.bridge import structured_request
    parts, fields, datasets = [], [], []
    contexts = [scope] + [replace(scope,knowledge_base_id=source['knowledge_base_id'],
        knowledge_base_tenant_id=source['tenant_id'],additional_knowledge_bases=()) for source in scope.additional_knowledge_bases]
    for source in contexts:
        if source.restricted: continue
        marker = current_workbench.set(source)
        try:
            response = structured_request('POST','/api/research/facts',
                {'sources':current_sources(source),'catalog_only':True},return_error_response=True)
            if response.status_code in {401,403,404,409}: raise HTTPException(response.status_code,'知识库来源已变化')
            response.raise_for_status(); page = response.json()
        except (httpx.HTTPError,ValueError,TypeError):
            raise HTTPException(503,'知识库目录暂时不可读') from None
        finally: current_workbench.reset(marker)
        if page.get('catalog_only') is not True or not isinstance(page.get('indicators'),list):
            raise HTTPException(503,'知识库目录格式无效')
        fields.extend({**field,'knowledge_base_id':source.knowledge_base_id,
            'knowledge_base_tenant_id':source.knowledge_base_tenant_id,'source_kind':next((item.get('kind') for item in scope.additional_knowledge_bases if item['knowledge_base_id']==source.knowledge_base_id),'primary'),
            'source_version':page['version']} for field in page['indicators'])
        parts.append([source.knowledge_base_tenant_id,source.knowledge_base_id,page['version']]);datasets.extend(page.get('datasets',[]))
    version = parts[0][2] if len(parts) == 1 else hashlib.sha256(json.dumps(parts,separators=(',',':')).encode()).hexdigest()
    if requested is not None and requested != version: raise HTTPException(409,'知识库目录已变化')
    query = (search or '').casefold().strip()
    filtered = [field for field in fields if (series_ids is None or field['series_id'] in series_ids)
        and (source_kind is None or (field['source_kind']=='macro') == (source_kind=='macro'))
        and (not query or query in json.dumps({key:field.get(key) for key in ('metric','name','series_id','source_series_id',
            'source_file','unit','frequency','dimensions','source_kind')},ensure_ascii=False).casefold())]
    return {'series':filtered[offset:offset+limit],'total':len(filtered),'offset':offset,
        'next_offset':offset+limit if offset+limit<len(filtered) else None,'truncated':offset+limit<len(filtered),
        'version':version,'datasets':datasets,'unavailable_series_ids':sorted(set(series_ids or [])-{field['series_id'] for field in fields})}


def _judgment_windows(payload):
    from tin.judgments.service import _refs
    from tin.schemas.judgment import EXPRESSION
    windows={identifier:2 for _,_,identifier in _refs(payload)}
    for rule in payload.falsifiers:
        match=EXPRESSION.fullmatch(rule.expression.strip())
        if match is None or not 1<=rule.consecutive_periods<=120:
            raise HTTPException(409,'已保存的证伪规则窗口无效，请重新核对研究逻辑')
        need=rule.consecutive_periods+int(match.group(1)=='change')
        windows[rule.series_id]=max(windows[rule.series_id],need)
    return windows


def _saved_plan(scope, request):
    from tin.integration.workbench_scope import artifact_session
    from tin.models import SourceMapping
    from tin.web.restricted_views import core_config,mapping_key,ORIGINS_KEY,shared_diagrams,default_diagram,_diagram_ids
    from tin.web.market_history import history_config
    from tin.web.market_contracts import contract_config,_identifiers
    module = request['module']
    with artifact_session(scope) as session:
        row = session.get(SourceMapping,mapping_key(session,ORIGINS_KEY))
        origins = (row.config or {}).get('origins',{}) if row else {}
        if request['series_ids'] is not None:
            return [dict(ids=list(request['series_ids']),points=request.get('points') or 2,explicit=True)],origins
        if module == 'market':
            history, contracts = history_config(session),contract_config(session)
            return [dict(ids=core_config(session),points=2),
                dict(ids=history['series_ids'],points=history['points_per_series']),
                dict(ids=_identifiers(contracts),points=1,same_day=[row['settle'] for row in contracts])],origins
        identifiers = []
        windows = {}
        if module in {'home','indicators','reports'}: identifiers.extend(core_config(session))
        if module == 'diagram':
            templates = shared_diagrams(session)
            selected = next((row for row in templates if row.id == request['template_id']),None) if request['template_id'] is not None else default_diagram(session,templates)
            if request['template_id'] is not None and selected is None:
                raise HTTPException(404,'产业图不属于当前工作台')
            if selected: identifiers.extend(_diagram_ids(selected.layout or {}))
        if module in {'home','reports','judgments','strategies'}:
            from tin.judgments.service import current,to_payload
            from tin.integration.research_profile import research_variety
            judgment = current(session,research_variety(session))
            if judgment:
                payload = to_payload(judgment)
                windows.update(_judgment_windows(payload))
        contract_plan = None
        if module in {'strategies','reports'}:
            if module == 'strategies':
                contracts=contract_config(session)
                contract_plan=dict(ids=_identifiers(contracts),points=1,same_day=[row['settle'] for row in contracts])
            from tin.strategy.service import list_strategies
            for row in list_strategies(session):
                if isinstance(row.plan,dict):
                    identifiers.extend(leg['quote_series_id'] for leg in row.plan.get('legs',[]) if isinstance(leg,dict) and isinstance(leg.get('quote_series_id'),str) and SERIES.fullmatch(leg['quote_series_id']))
                    if module == 'reports':
                        identifiers.extend(item['series_id'] for item in row.plan.get('evidence',[])
                            if isinstance(item,dict) and isinstance(item.get('series_id'),str) and SERIES.fullmatch(item['series_id']))
        for identifier in identifiers:windows.setdefault(identifier,24 if module=='diagram' else 2)
        grouped = {}
        for identifier,points in windows.items():
            requested = request.get('points')
            count = max(requested, points) if module == 'reports' and requested else requested or points
            grouped.setdefault(count,[]).append(identifier)
        plans=[dict(ids=values,points=points) for points,values in sorted(grouped.items())]
        if contract_plan and contract_plan['ids']:plans.append(contract_plan)
        if len(set(windows)|set(contract_plan['ids'] if contract_plan else []))>256:
            raise HTTPException(413,'当前页面绑定超过256项，请分开研究页面')
        if sum(len(plan['ids'])*plan['points'] for plan in plans)>20000:
            raise HTTPException(413,'当前页面必要观测超过20000项，请拆分研究规则')
        return plans,origins


def bounded_facts(scope, identifiers, module, *, day=None, points=2, same_day=None, origins=None, direct_only=False):
    identifiers = list(dict.fromkeys(identifiers))
    query_ids=[identifier for identifier in identifiers if isinstance(identifier,str) and SERIES.fullmatch(identifier)]
    legacy_unavailable=set(identifiers)-set(query_ids)
    if direct_only and legacy_unavailable:
        raise HTTPException(422,'指定指标必须使用当前知识库真实ID')
    targets = [source for source in scope.sources if not source['delegated'] or not direct_only and module in source['view_modules']]
    if direct_only:
        available = catalog_page(scope,limit=200,series_ids=query_ids)
        if available.get('unavailable_series_ids'):
            raise HTTPException(403,'指定指标不在当前直接来源授权中')
    else:
        used = set()
        direct = {source['id'] for source in targets if not source['delegated']}
        allowed = {(source['knowledge_base_id'],source['tenant_id']):source for source in targets}
        for identifier in query_ids:
            origin = (origins or {}).get(identifier)
            if isinstance(origin,dict):
                matched = allowed.get((origin.get('knowledge_base_id'),origin.get('tenant_id')))
                if matched: used.add(matched['id'])
            else:
                # A missing legacy origin can only be checked against sources
                # the caller can browse directly, never a delegated source.
                used.update(direct)
        targets = [source for source in targets if source['id'] in used]
    body = {'source_bindings':[source['id'] for source in targets],'series_ids':query_ids,
        'view_module':module,'points_per_series':points,
        'as_of':day or datetime.now(SHANGHAI).date().isoformat(), 'allow_unavailable':not direct_only}
    if same_day: body['same_day_series_ids'] = same_day
    result = _request(scope,body)
    unavailable = result.get('unavailable_series_ids',[])
    if (not isinstance(unavailable,list) or any(identifier not in query_ids for identifier in unavailable)
            or len(unavailable)!=len(set(unavailable)) or direct_only and unavailable):
        raise HTTPException(503,'页面绑定失效响应无效')
    unavailable=[*unavailable,*sorted(legacy_unavailable)]
    result={**result,'unavailable_series_ids':unavailable}
    if (result.get('bounded') is not True or not isinstance(result.get('rows'),list)
            or len(result['rows']) > len(identifiers)*points or not isinstance(result.get('indicators'),list)
            or len(result['indicators']) != len(set(identifiers)-set(unavailable))
            or {field.get('series_id') for field in result['indicators'] if isinstance(field,dict)} != set(identifiers)-set(unavailable)):
        raise HTTPException(503,'页面取值响应不完整或超过必要范围')
    expected = {source['id']:source for source in targets}
    source_versions=result.get('source_versions',{})
    if (not isinstance(source_versions,dict) or set(source_versions)!=set(expected)
            or any(not isinstance(value,str) or not re.fullmatch('[a-f0-9]{64}',value) for value in source_versions.values())):
        raise HTTPException(503,'页面来源快照版本不完整')
    for spec in result['indicators']:
        source = expected.get(spec.get('source_binding_id'))
        if (source is None or spec.get('knowledge_base_id') != source['knowledge_base_id']
                or type(spec.get('knowledge_base_tenant_id')) is not int or spec['knowledge_base_tenant_id'] != source['tenant_id']
                or spec.get('source_id') != source['source_id'] or spec.get('source_type') != source['source_type']):
            raise HTTPException(503,'页面字段来源与授权绑定不一致')
    seen,counts = set(),{}
    for row in result['rows']:
        if (not isinstance(row,dict) or row.get('series_id') not in identifiers
                or type(row.get('value')) not in {int,float} or not math.isfinite(row['value'])
                or row.get('value_status') not in {'valid','ok','正常'}
                or not isinstance(row.get('date'),str) or row['date'] > body['as_of']
                or (row['series_id'],row['date']) in seen):
            raise HTTPException(503,'页面取值包含未授权或重复观测')
        try:
            if date.fromisoformat(row['date']).isoformat() != row['date']: raise ValueError
        except ValueError: raise HTTPException(503,'页面观测日期无效') from None
        seen.add((row['series_id'],row['date']))
        counts[row['series_id']] = counts.get(row['series_id'],0)+1
        if counts[row['series_id']]>points:
            raise HTTPException(503,'页面单字段观测超过必要窗口')
    return result


def _merge_calendars(calendars):
    result={}
    candidates=[]
    for calendar in calendars:
        if not calendar:continue
        if result and result['as_of']!=calendar.get('as_of'):
            raise HTTPException(503,'页面日期导航窗口不一致')
        result['as_of']=calendar['as_of']
        for key,choose in (('min_date',min),('max_date',max),('previous_date',max),('next_date',min)):
            value=calendar.get(key)
            if value is not None:result[key]=choose(result[key],value) if result.get(key) is not None else value
            else:result.setdefault(key,None)
        candidates.extend(value for value in (calendar.get('max_date'),calendar.get('latest_previous_date')) if value is not None)
    ordered=sorted(set(candidates))
    if result:result['latest_previous_date']=ordered[-2] if len(ordered)>1 else None
    return result


def projected_page_session(engine, scope, **kwargs):
    from tin.integration.kb_facts import KnowledgeFactSession,_projection_engine,_clear_projection,install_projection
    request = _page.get() or {'module':'home','series_ids':None,'day':None,'template_id':None}
    plans, origins = _saved_plan(scope,request)
    rows, fields, versions, totals, unavailable, calendars, source_versions = [], {}, [], {}, set(), [], {}
    for plan in plans:
        if not plan['ids']: continue
        result = bounded_facts(scope,plan['ids'],request['module'],day=request['day'],points=plan['points'],
            same_day=plan.get('same_day'),origins=origins,direct_only=plan.get('explicit',False))
        for binding_id,source_version in result.get('source_versions',{}).items():
            if binding_id in source_versions and source_versions[binding_id]!=source_version:
                raise HTTPException(409,'页面不同必要窗口的来源快照已变化，请刷新后重试')
            source_versions[binding_id]=source_version
        versions.append(result['version']);rows.extend(result['rows'])
        fields.update((field['series_id'],field) for field in result['indicators'])
        totals.update(result.get('series_totals',{}));unavailable.update(result.get('unavailable_series_ids',[]))
        calendar=result.get('calendar')
        if calendar is not None:
            expected_day=request['day'] or datetime.now(SHANGHAI).date().isoformat()
            if not isinstance(calendar,dict) or calendar.get('as_of')!=expected_day:
                raise HTTPException(503,'页面日期导航边界不一致')
            for key in ('min_date','max_date','previous_date','next_date','latest_previous_date'):
                value=calendar.get(key)
                if value is None:continue
                try:
                    if not isinstance(value,str) or date.fromisoformat(value).isoformat()!=value or value>datetime.now(SHANGHAI).date().isoformat():raise ValueError
                except (TypeError,ValueError):raise HTTPException(503,'页面日期导航无效') from None
            calendars.append(calendar)
    version = hashlib.sha256(json.dumps([scope.sources_revision,versions],separators=(',',':')).encode()).hexdigest()
    decoded = [{**row,**{key:fields[row['series_id']].get(key) for key in ('source_knowledge_id','dataset_id','source_series_id')}} for row in rows]
    page = {'version':version,'indicators':list(fields.values()),'datasets':[],'rows':decoded,'total':len(decoded),
            'offset':0,'next_offset':None,'truncated':False}
    connection = _projection_engine(engine).connect()
    try:
        _clear_projection(connection)
        info = install_projection(connection,scope,pages=[page])
        session = KnowledgeFactSession(bind=connection,expire_on_commit=False,**kwargs)
        source_summaries = [{key:source[key] for key in ('id','source_type','source_id','knowledge_base_id','tenant_id','kind') if key in source} for source in scope.sources]
        session.info.update(info,_knowledge_connection=connection,knowledge_base_sources=source_summaries,
            knowledge_base_id=scope.knowledge_base_id,knowledge_base_tenant_id=scope.knowledge_base_tenant_id,
            sources_revision=scope.sources_revision,knowledge_facts_bounded=True,
            knowledge_facts_series_totals=totals,knowledge_facts_calendar=_merge_calendars(calendars),
            knowledge_facts_unavailable_series_ids=sorted(unavailable))
        return session
    except BaseException:
        connection.invalidate();connection.close();raise
