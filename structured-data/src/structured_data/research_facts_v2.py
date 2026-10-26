"""One bounded read across freshly authorized source bindings, without fact copies."""
from dataclasses import replace
from contextlib import ExitStack
from datetime import date, datetime
import json
import re
from zoneinfo import ZoneInfo

from fastapi import HTTPException

from .workspaces import current_identity, valid_knowledge_base_id
from .research_facts import _hash, _bounded_parameters, _bounded_rows, _same_day_date, _same_day_rows, _RESEARCH_ROW_FILTER


def parameters(body):
    allowed = {'source_bindings','catalog_only','offset','limit','search','series_ids','version',
               'view_module','as_of','points_per_series','same_day_series_ids','allow_unavailable','source_kind'}
    if not isinstance(body, dict) or set(body) - allowed:
        raise ValueError('多来源读取只接受明确绑定、目录分页或页面所需字段')
    sources = body.get('source_bindings')
    if (not isinstance(sources, list) or len(sources) > 8
            or any(not isinstance(item, str) or not 1 <= len(item) <= 128 for item in sources)
            or len(set(sources)) != len(sources)):
        raise ValueError('数据来源绑定无效')
    catalog = body.get('catalog_only', False)
    if type(catalog) is not bool:
        raise ValueError('目录读取参数无效')
    if type(body.get('allow_unavailable',False)) is not bool:
        raise ValueError('绑定失效处理参数无效')
    requested = body.get('version')
    if requested is not None and (not isinstance(requested, str) or not re.fullmatch('[a-f0-9]{64}', requested)):
        raise ValueError('多来源快照版本无效')
    if catalog:
        if {'view_module','as_of','points_per_series','same_day_series_ids'} & body.keys():
            raise ValueError('目录接口不接受观测取值参数')
        offset, limit, search = body.get('offset', 0), body.get('limit', 200), body.get('search', '')
        if (type(offset) is not int or offset < 0 or type(limit) is not int or not 1 <= limit <= 200
                or not isinstance(search, str) or len(search) > 200):
            raise ValueError('目录分页参数无效')
        identifiers = body.get('series_ids')
        kind = body.get('source_kind')
        if kind is not None and (not isinstance(kind,str) or kind not in {'macro','industry'}):
            raise ValueError('目录来源范围无效')
        if identifiers is not None:
            _bounded_parameters({'series_ids': identifiers})
        return {'catalog':True,'offset':offset,'limit':limit,'search':search,'series_ids':identifiers,'source_kind':kind}
    if {'offset','limit','search','source_kind'} & body.keys() or 'series_ids' not in body:
        raise ValueError('观测读取必须明确所需字段，不接受全量目录或观测分页')
    selection = _bounded_parameters({key:value for key,value in body.items() if key not in {'source_bindings','version','catalog_only','allow_unavailable'}},extended=True)
    return {'catalog':False,'selection':selection}


def _originals(source):
    result, seen = [], set()
    for row in source['knowledge']:
        if not isinstance(row, dict) or not valid_knowledge_base_id(row.get('id')) or row['id'] in seen:
            raise HTTPException(403, '真实来源文件清单无效')
        seen.add(row['id'])
        if row.get('parse_status') in {'deleting','deleted'}:
            continue
        if str(row.get('file_type', '')).lower().lstrip('.') not in {'xlsx','xls','xlsm','csv','tsv'}:
            continue
        digest = row.get('file_hash')
        if not digest:
            continue
        if not isinstance(digest, str) or not re.fullmatch(r'(?:[0-9a-fA-F]{32}|[0-9a-fA-F]{64})', digest):
            raise HTTPException(403, '真实来源文件哈希无效')
        result.append({'knowledge_id':row['id'],'file_hash':digest.lower()})
    if len(result) > 128:
        raise HTTPException(413, '单个来源超过128份结构化原件，请拆分来源；未返回部分数据')
    return result


def read_current(registry, body, sources):
    with ExitStack() as leases:
        return _read_current(registry,body,sources,leases)


def _read_current(registry, body, sources, leases):
    parsed = parameters(body)
    identity = current_identity.get()
    if identity is None or identity.scope_version != 2 or identity.sources_revision < 1:
        raise HTTPException(403, '多来源读取缺少已核验的研究台范围')
    if {source['id'] for source in sources} != set(body['source_bindings']):
        raise HTTPException(403, '来源选择与实时授权不一致')
    if parsed['catalog']:
        return read_catalog(registry,identity,body,parsed,sources)
    parts, fields, datasets, bindings, metadata_bytes = [], [], [], [], 0
    for source in sources:
        local = replace(identity, knowledge_base_id=source['knowledge_base_id'],
                        knowledge_base_tenant_id=source['tenant_id'])
        marker = current_identity.set(local)
        try:
            objects = registry.objects()
            from .research_catalog import current_index,page
            connection,metadata = leases.enter_context(current_index(objects['store'],objects['manager'],_originals(source)))
            selected,_,_=page(connection,series_ids=parsed['selection']['series_ids'],limit=256)
            prepared={'version':metadata['version'],'indicators':selected,'datasets':[],'bindings':[]}
            for info in metadata['datasets']:
                mapping={field['source_series_id']:field['series_id'] for field in selected
                    if field['dataset_id']==info['dataset_id'] and field['source_knowledge_id']==info['source_knowledge_id']}
                if mapping:
                    prepared['datasets'].append(info);prepared['bindings'].append((info,mapping))
            metadata_bytes += sum(len(json.dumps(field,ensure_ascii=False,separators=(',',':')).encode()) for field in prepared['indicators'])
            if metadata_bytes > 32*1024*1024 or len(fields)+len(prepared['indicators']) > 25000:
                raise HTTPException(413, '本次目录超过25000项或32MiB，请缩小明确的数据来源范围')
            parts.append([source['id'], prepared['version']])
            extra = {'source_binding_id':source['id'],'source_type':source['source_type'],
                'source_id':source['source_id'],'knowledge_base_id':source['knowledge_base_id'],
                'knowledge_base_tenant_id':source['tenant_id'],'source_kind':source.get('kind') or source['source_type']}
            fields.extend({**field,**extra,'source_version':prepared['version']} for field in prepared['indicators'])
            datasets.extend({**dataset,**extra} for dataset in prepared['datasets'])
            bindings.extend(({**info,'_store':objects['store'],'_direct':not source['delegated']},mapping) for info,mapping in prepared['bindings'])
        finally:
            current_identity.reset(marker)
    known = {field['series_id'] for field in fields}
    if len(known) != len(fields):
        raise HTTPException(409, '不同来源的指标身份发生冲突')
    if len(fields) > 25000:
        raise HTTPException(413, '本次元数据目录超过25000项，请缩小明确的数据来源范围')
    fingerprint = {'sources_revision':identity.sources_revision,'parts':parts}
    selection = parsed['selection']
    unavailable = set(selection['series_ids']) - known
    if unavailable and not body.get('allow_unavailable'):
        raise HTTPException(403, '页面指标不在当前已确认来源中，未返回部分结果')
    selection = {**selection,'series_ids':sorted(known)}
    if 'same_day_series_ids' in selection:
        selected_date = None if set(selection['same_day_series_ids'])-known else _same_day_date(None,bindings,selection)
        selection = {**selection,'selected_date':selected_date}
    version = _hash({**fingerprint,'selection':selection})
    rows = _same_day_rows(None,bindings,selection) if 'same_day_series_ids' in selection else _bounded_rows(None,bindings,selection,strict=True)
    totals,calendar = _calendar(bindings,selection,
        include_totals=body.get('view_module')=='indicators' and selection['points_per_series']>2)
    result = {'bounded':True,'indicators':fields,'rows':rows,'total':len(rows),'offset':0,
        'next_offset':None,'truncated':False,'version':version,
        'series_totals':totals,'calendar':calendar,'source_versions':dict(parts),'unavailable_series_ids':sorted(unavailable),
        **({'selected_date':selection['selected_date']} if 'same_day_series_ids' in selection else {})}
    if body.get('version') is not None and body['version'] != version:
        raise HTTPException(409, '来源快照在读取期间发生变化，请重新读取')
    return {**result,'scope_version':2,'sources_revision':identity.sources_revision,
            'workbench_id':identity.workbench_id}


def _calendar(bindings, selection, *, include_totals):
    cutoff=datetime.now(ZoneInfo('Asia/Shanghai')).date().isoformat()
    boundary=selection['as_of'] or cutoff
    dates={'as_of':boundary,'min_date':None,'max_date':None,'previous_date':None,'next_date':None,'latest_previous_date':None}
    latest,totals=[],{}
    def merge(key,value,choose):
        if value is None:return
        value=value.isoformat() if type(value) is date else value
        try:
            if not isinstance(value,str) or date.fromisoformat(value).isoformat()!=value or value>cutoff:raise ValueError
        except (ValueError,TypeError):
            raise HTTPException(409,'来源日期未明确为真实日期，无法生成日期导航') from None
        dates[key]=choose(dates[key],value) if dates[key] is not None else value
    for info,mapping in bindings:
        if not mapping or not info['_direct']:continue
        identifiers=sorted(mapping)
        where='series_id IN ('+','.join('?' for _ in identifiers)+') AND '+_RESEARCH_ROW_FILTER+' AND date <= ?'
        with info['_store']._connect(info['dataset_id']) as connection:
            aggregate=connection.execute('SELECT min(date),max(date),max(date) FILTER (WHERE date < ?),'
                'min(date) FILTER (WHERE date > ?) FROM observations WHERE '+where,[boundary,boundary,*identifiers,cutoff]).fetchone()
            for key,value,choose in zip(('min_date','max_date','previous_date','next_date'),aggregate,(min,max,max,min)):
                merge(key,value,choose)
            for (stamp,) in connection.execute('SELECT DISTINCT date FROM observations WHERE '+where+' ORDER BY date DESC LIMIT 2',
                    [*identifiers,cutoff]).fetchall():
                stamp=stamp.isoformat() if type(stamp) is date else stamp
                latest.append(stamp)
            if include_totals:
                count_where='series_id IN ('+','.join('?' for _ in identifiers)+') AND '+_RESEARCH_ROW_FILTER+' AND date <= ?'
                for identifier,count in connection.execute('SELECT series_id,count(DISTINCT date) FROM observations WHERE '
                        +count_where+' GROUP BY series_id',[*identifiers,boundary]).fetchall():
                    totals[mapping[identifier]]=count
    ordered=sorted(set(latest))
    if len(ordered)>1:merge('latest_previous_date',ordered[-2],max)
    return totals,dates


def read_catalog(registry, identity, body, parsed, sources):
    with ExitStack() as leases:
        return _read_catalog(registry,identity,body,parsed,sources,leases)


def _read_catalog(registry, identity, body, parsed, sources, leases):
    from .research_catalog import current_index,page
    rows,datasets,parts,present = [],[],[],set()
    offset,total = parsed['offset'],0
    for source in sorted(sources,key=lambda source:source['id']):
        if source['delegated']:
            raise HTTPException(403,'受限来源不允许浏览指标目录')
        marker = current_identity.set(replace(identity,knowledge_base_id=source['knowledge_base_id'],knowledge_base_tenant_id=source['tenant_id']))
        try:
            objects = registry.objects()
            originals = _originals(source)
            connection,metadata=leases.enter_context(current_index(objects['store'],objects['manager'],originals))
            parts.append([source['id'],metadata['version']])
            kind = source.get('kind') or source['source_type']
            if parsed['source_kind'] is not None and (kind=='macro') != (parsed['source_kind']=='macro'):
                continue
            fields,count,found = page(connection,search=parsed['search'],series_ids=parsed['series_ids'],offset=max(0,offset-total),limit=max(0,parsed['limit']-len(rows)))
            present.update(found)
            extra = {'source_binding_id':source['id'],'source_type':source['source_type'],'source_id':source['source_id'],
                'knowledge_base_id':source['knowledge_base_id'],'knowledge_base_tenant_id':source['tenant_id'],'source_kind':kind,
                'source_version':metadata['version']}
            rows.extend({**field,**extra} for field in fields)
            datasets.extend({**dataset,**extra} for dataset in metadata['datasets'])
            total += count
        finally:
            current_identity.reset(marker)
    version = _hash({'sources_revision':identity.sources_revision,'parts':parts})
    if body.get('version') is not None and body['version'] != version:
        raise HTTPException(409,'目录快照在读取期间变化，请重新读取')
    return {'scope_version':2,'sources_revision':identity.sources_revision,'workbench_id':identity.workbench_id,
        'catalog_only':True,'series':rows,'datasets':datasets,'total':total,'offset':offset,
        'next_offset':offset+len(rows) if offset+len(rows)<total else None,'truncated':offset+len(rows)<total,
        'version':version,'unavailable_series_ids':sorted(set(parsed['series_ids'] or [])-present)}
