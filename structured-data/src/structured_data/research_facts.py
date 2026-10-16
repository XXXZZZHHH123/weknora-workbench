"""Read-only research projection of the knowledge base's published snapshots."""
import hashlib
import json
import re

from fastapi import HTTPException

from .workspaces import current_identity, valid_knowledge_base_id

RESEARCH_ROWS_ENCODING='research-rows-v1'
RESEARCH_ROW_COLUMNS=('series_id','date','value','source_cell','formula','value_status','date_cell','raw_value')
# These are the existing research projection's technical row requirements,
# independent of question, business meaning, metric name, unit or source.
_RESEARCH_ROW_FILTER="value_status IN ('valid','ok','正常') AND value IS NOT NULL AND date IS NOT NULL"


def _hash(value):
    return hashlib.sha256(json.dumps(value,sort_keys=True,separators=(',',':'),ensure_ascii=False).encode()).hexdigest()


def _parameters(body):
    if not isinstance(body,dict) or set(body)-{'sources','offset','limit','version','metadata_only','encoding'}:
        raise ValueError('只接受当前原件、分页和快照版本参数')
    sources,offset,limit=body.get('sources'),body.get('offset',0),body.get('limit',10000)
    if not isinstance(sources,list) or len(sources)>128:
        raise ValueError('请提供至多128份已核验的当前知识库原件；不提供部分事实')
    if type(offset) is not int or offset<0 or type(limit) is not int or not 1<=limit<=10000:
        raise ValueError('事实分页参数无效')
    version=body.get('version')
    if type(body.get('metadata_only',False)) is not bool:
        raise ValueError('版本核验参数无效')
    if 'encoding' in body and (body['encoding']!=RESEARCH_ROWS_ENCODING or body.get('metadata_only')):
        raise ValueError('事实传输编码无效；版本核验不接受行编码')
    if version is not None and (not isinstance(version,str) or not re.fullmatch('[a-f0-9]{64}',version)):
        raise ValueError('事实快照版本无效')
    normalized=[];seen=set()
    for source in sources:
        if (not isinstance(source,dict) or set(source)!={'knowledge_id','file_hash'}
                or not valid_knowledge_base_id(source['knowledge_id'])
                or source['knowledge_id'] in seen or not isinstance(source['file_hash'],str)
                or not re.fullmatch(r'(?:[a-fA-F0-9]{32}|[a-fA-F0-9]{64})',source['file_hash'])):
            raise ValueError('当前原件身份或哈希无效')
        seen.add(source['knowledge_id'])
        normalized.append({'knowledge_id':source['knowledge_id'],'file_hash':source['file_hash'].lower()})
    return sorted(normalized,key=lambda item:item['knowledge_id']),offset,limit,version


def _page_series(identifiers,counts,offset,limit):
    """Narrow the stable series/date/cell ordering to the requested page.

    Counts come from this read-only snapshot, never the catalog's summaries.
    Only whole series before/after the page are omitted; ordering and offsets
    inside the selected range remain the same as a full-table sorted page.
    """
    selected=[];seen=0;start=None
    for identifier in identifiers:
        count=counts.get(identifier,0)
        if count and seen<offset+limit and offset<seen+count:
            if start is None:
                start=seen
            selected.append(identifier)
        seen+=count
    return selected,offset-(start or 0)


def read_facts(store,manager,body):
    identity=current_identity.get()
    if identity is None or not identity.knowledge_base_id:
        raise ValueError('研究指标必须绑定已授权知识库，不能读取空间旧事实')
    sources,offset,limit,requested=_parameters(body)
    compact=body.get('encoding')==RESEARCH_ROWS_ENCODING
    records=manager.list()
    selected=[]
    for source in sources:
        matches=[]
        for record in records:
            if not record.get('dataset_id') or record.get('dataset_kind')!='business':
                continue
            if any(isinstance(binding,dict)
                   and binding.get('source_knowledge_id')==source['knowledge_id']
                   and binding.get('file_hash','').lower()==source['file_hash']
                   and binding.get('knowledge_base_id')==identity.knowledge_base_id
                   and binding.get('tenant_id')==identity.knowledge_base_tenant_id
                   for binding in record.get('native_source_bindings',[])):
                matches.append(record)
        if not matches:
            continue
        newest=max(str(record.get('updated_at','')) for record in matches)
        current=[record for record in matches if str(record.get('updated_at',''))==newest]
        if len({record['dataset_id'] for record in current})!=1:
            raise HTTPException(409,'同一原件有互相冲突的当前结构化版本，请在知识库核对')
        selected.append((source,current[0]))

    datasets=[];indicators=[];bindings=[];verified={}
    for source,record in selected:
        dataset=record['dataset_id']
        try:
            if dataset not in verified:
                manifest=store.manifest(dataset)
                original=store._path(dataset)/'source.xlsx'
                raw=original.read_bytes()
                sha256=hashlib.sha256(raw).hexdigest()
                hashes={hashlib.md5(raw).hexdigest(),sha256}
                if manifest.get('sha256')!=sha256:
                    raise ValueError('原件哈希不符')
                catalog=json.loads((store._path(dataset)/'series.json').read_text())
                if (not isinstance(catalog,list) or any(not isinstance(field,dict)
                        or not isinstance(field.get('series_id'),str) or not field['series_id'] for field in catalog)
                        or len({field['series_id'] for field in catalog})!=len(catalog)):
                    raise ValueError('字段目录无效')
                verified[dataset]=(manifest,hashes,catalog)
            manifest,hashes,catalog=verified[dataset]
            if source['file_hash'] not in hashes:
                raise ValueError('结构化原件与知识库当前原件不同')
        except (ValueError,OSError,KeyError,TypeError):
            raise HTTPException(409,'当前知识库结构化快照或原件无法核验，请重新核对；未使用旧研究数值') from None
        try:
            review=manager.get(record['import_id'])
            confirmed=[sheet for sheet in review.get('sheets',[]) if sheet.get('status')=='confirmed']
            confirmed_names={sheet['name'] for sheet in confirmed}
        except (ValueError,OSError,KeyError,TypeError):
            raise HTTPException(409,'工作表确认记录不可读，请在知识库重新核对') from None
        catalog=[field for field in catalog if field.get('sheet') in confirmed_names]
        info={'source_knowledge_id':source['knowledge_id'],'dataset_id':dataset,
              'source_file':manifest.get('source_file'),'sha256':manifest['sha256'],
              'import_id':record['import_id'],'rule_version':manifest.get('rule_version'),
              'imported_at':manifest.get('imported_at'),
              'confirmed_sheets':sorted(confirmed_names),'review_fingerprint':_hash(confirmed)}
        datasets.append(info)
        mapping={}
        for field in catalog:
            identifier='kb-'+_hash([identity.knowledge_base_id,source['knowledge_id'],field['series_id']])[:48]
            mapping[field['series_id']]=identifier
            indicators.append({**field,'series_id':identifier,'source_series_id':field['series_id'],
                'source_knowledge_id':source['knowledge_id'],'dataset_id':dataset,
                'source_file':manifest.get('source_file'),'source_sha256':manifest['sha256']})
        bindings.append((info,mapping))
    version=_hash({'knowledge_base_id':identity.knowledge_base_id,
                   'knowledge_base_tenant_id':identity.knowledge_base_tenant_id,
                   'sources':sources,'datasets':datasets,'indicators':indicators})
    if requested is not None and requested!=version:
        raise HTTPException(409,'知识库结构化版本已改变，请重新读取完整事实；未混合新旧快照')
    if body.get('metadata_only'):
        # Exactly the same current original, confirmation and catalog checks as
        # a full read; no DuckDB row scan or page serialization is necessary.
        return {'knowledge_base_id':identity.knowledge_base_id,
                'knowledge_base_tenant_id':identity.knowledge_base_tenant_id,
                'version':version,'metadata_only':True}
    rows=[];total=0;raw_total=0
    for info,mapping in bindings:
        if compact:
            info['raw_observation_count']=0
        if not mapping:
            continue
        with store._connect(info['dataset_id']) as connection:
            identifiers=sorted(mapping)
            where=' WHERE series_id IN ('+','.join('?' for _ in identifiers)+')'
            if compact:
                grouped=connection.execute('SELECT series_id,count(*),count(*) FILTER (WHERE '+_RESEARCH_ROW_FILTER+'),'
                    'count(*) FILTER (WHERE value IS NOT NULL AND NOT isfinite(value))'
                    ' FROM observations'+where+' GROUP BY series_id',identifiers).fetchall()
                raw_count=sum(group[1] for group in grouped)
                counts={group[0]:group[2] for group in grouped}
                # The legacy strict JSON response also rejects NaN/Infinity,
                # even on missing-date or invalid-status rows. Do not silently
                # hide these corrupt facts through the compact row predicate.
                if any(group[3] for group in grouped):
                    raise HTTPException(503,'当前知识库结构化观测含非有限数值，未返回部分事实')
                info['raw_observation_count']=raw_count
            else:
                counts=dict(connection.execute('SELECT series_id,count(*) FROM observations'+where+' GROUP BY series_id',identifiers).fetchall())
                raw_count=sum(counts.values())
            count=sum(counts.values())
            raw_total+=raw_count
            if raw_count<0 or raw_total>2_000_000:
                raise HTTPException(413,'当前知识库超过200万条观测，未返回部分事实')
            if count and offset<total+count and len(rows)<limit:
                local_offset=max(0,offset-total)
                # A large OFFSET used to sort all confirmed series again on
                # every page. Real per-series counts let us sort just the
                # intersecting series without caching facts or weakening the
                # original/confirmation/version checks above.
                page_identifiers,local_offset=_page_series(identifiers,counts,local_offset,limit-len(rows))
                where=' WHERE series_id IN ('+','.join('?' for _ in page_identifiers)+')'
                if compact:
                    where+=' AND '+_RESEARCH_ROW_FILTER
                columns=','.join(RESEARCH_ROW_COLUMNS) if compact else '*'
                cursor=connection.execute('SELECT '+columns+' FROM observations'+where+' ORDER BY series_id,date NULLS LAST,source_cell LIMIT ? OFFSET ?',
                                          [*page_identifiers,limit-len(rows),local_offset])
                names=[column[0] for column in cursor.description]
                for values in cursor.fetchall():
                    if compact:
                        source_series=values[0]
                        if source_series not in mapping:
                            raise HTTPException(409,'观测与当前真实字段目录不一致，未返回部分事实')
                        rows.append([mapping[source_series],*values[1:]])
                        continue
                    row=dict(zip(names,values));source_series=row['series_id']
                    if source_series not in mapping:
                        raise HTTPException(409,'观测与当前真实字段目录不一致，未返回部分事实')
                    rows.append({**row,'series_id':mapping[source_series],'source_series_id':source_series,
                        'source_knowledge_id':info['source_knowledge_id'],'dataset_id':info['dataset_id']})
            total+=count
    document={'knowledge_base_id':identity.knowledge_base_id,
            'knowledge_base_tenant_id':identity.knowledge_base_tenant_id,'version':version,
            'datasets':datasets,'indicators':indicators,'rows':rows,'total':total,'offset':offset,
            'next_offset':offset+len(rows) if offset+len(rows)<total else None,'truncated':offset+len(rows)<total,
            'notice':'仅绑定知识库当前已发布的结构化数据；日期为原表日期/统计期，非推定发布时间。'}
    if compact:
        document.update(encoding=RESEARCH_ROWS_ENCODING,row_columns=list(RESEARCH_ROW_COLUMNS))
    return document
