"""Request-only, read-only SQL views of the bound knowledge base's facts."""
from datetime import date, datetime, time, timezone
import hashlib
import json
import math
import re
import sqlite3
import sys
import threading
import time as clock
from collections import OrderedDict
from urllib.parse import quote

import httpx
from fastapi import HTTPException
from sqlalchemy.orm import Session
from sqlalchemy.pool import QueuePool

from tin.config import SHANGHAI, settings

FACT_TABLES = frozenset({'indicators', 'observations', 'derived', 'trading_days', 'observation_sources'})
MAX_ROWS = 2_000_000
PROJECTION_SECONDS = 300
MAX_RETAINED_BYTES = 64 * 1024 * 1024
MAX_PROJECTION_ENGINES = 4
_projection_engines = OrderedDict()
_projection_lock = threading.Lock()


def _projection_engine(engine):
    # Keep cached TEMP facts separate from raw artifact sessions. The latter
    # must never accidentally acquire a projection without a fresh check.
    from tin.db import make_engine
    key = str(engine.url)
    with _projection_lock:
        if key not in _projection_engines:
            _projection_engines[key] = make_engine(key, connect_args={'check_same_thread':False},
                poolclass=QueuePool, pool_size=1, max_overflow=3, pool_timeout=30, pool_recycle=PROJECTION_SECONDS)
        value = _projection_engines.pop(key)
        _projection_engines[key] = value
        while len(_projection_engines) > MAX_PROJECTION_ENGINES:
            _, old = _projection_engines.popitem(last=False)
            old.dispose()
        return value


def _retained_size(value):
    seen = set()
    def visit(item):
        if id(item) in seen:
            return 0
        seen.add(id(item))
        size = sys.getsizeof(item)
        if isinstance(item, dict):
            size += sum(visit(k)+visit(v) for k,v in item.items())
        elif isinstance(item, (tuple,list,set,frozenset)):
            size += sum(visit(v) for v in item)
        return size
    return visit(value)


def current_sources(scope):
    """Check current originals with the caller's bearer, never a saved inventory."""
    if not scope.authorization.startswith('Bearer '):
        raise HTTPException(401, '缺少当前知识库登录核验，未读取历史研究事实')
    endpoint = settings.weknora_api_url.rstrip('/') + '/api/v1/knowledge-bases/' + quote(scope.knowledge_base_id, safe='') + '/knowledge'
    try:
        sources, seen, expected_total = [], set(), None
        page, page_size = 1, 200
        while True:
            response = httpx.request('GET', endpoint, params={'page': page, 'page_size': page_size},
                headers={'Authorization': scope.authorization, 'X-Tenant-ID': str(scope.identity.tenant_id)},
                trust_env=False, follow_redirects=False, timeout=15)
            if response.status_code in {401, 403, 404}:
                raise HTTPException(response.status_code, '知识库来源授权已失效')
            response.raise_for_status()
            document = response.json()
            if not isinstance(document, dict):
                raise ValueError('Invalid knowledge inventory envelope')
            rows, total = document.get('data'), document.get('total')
            if (document.get('success') is not True or not isinstance(rows, list)
                    or type(total) is not int or total < 0):
                raise ValueError('Invalid current knowledge inventory')
            if total > 10000:
                raise HTTPException(503, '当前知识库超过10000条目录核验上限，未返回部分或历史事实')
            if expected_total is None:
                expected_total = total
            elif total != expected_total:
                raise ValueError('Knowledge inventory changed during pagination')
            # Older complete single-page envelopes are unambiguous. A partial
            # inventory requires the native page metadata to preserve offsets.
            if (page != 1 or total != len(rows)) and not {'page', 'page_size'} <= document.keys():
                raise ValueError('Missing knowledge inventory pagination metadata')
            actual_page = document.get('page', page)
            actual_size = document.get('page_size', page_size)
            if (type(actual_page) is not int or actual_page != page
                    or type(actual_size) is not int or not 1 <= actual_size <= 200
                    or (page > 1 and actual_size != page_size)):
                raise ValueError('Invalid knowledge inventory pagination')
            page_size = actual_size
            if len(rows) != min(page_size, total - len(seen)):
                raise ValueError('Incomplete current knowledge inventory page')
            for row in rows:
                if (not isinstance(row, dict) or row.get('knowledge_base_id') != scope.knowledge_base_id
                        or not isinstance(row.get('id'), str) or not row['id'] or row['id'] in seen):
                    raise ValueError('Invalid knowledge inventory scope')
                seen.add(row['id'])
                if row.get('parse_status') in {'deleting', 'deleted'} or row.get('deleted_at'):
                    continue
                if str(row.get('file_type','')).lower().lstrip('.') not in {'xlsx','xls','xlsm','csv','tsv'}:
                    continue
                fingerprint = row.get('file_hash')
                if not fingerprint:
                    continue
                if not isinstance(fingerprint, str) or not re.fullmatch('[0-9a-fA-F]{32}|[0-9a-fA-F]{64}', fingerprint):
                    raise ValueError('Invalid source fingerprint')
                sources.append({'knowledge_id': row['id'], 'file_hash': fingerprint.lower()})
                if len(sources) > 128:
                    raise HTTPException(503, '当前知识库超过一次核验的128份结构化原件上限，未返回部分或历史事实')
            if len(seen) == total:
                return sources
            page += 1
    except HTTPException:
        raise
    except (httpx.HTTPError, ValueError, TypeError, KeyError):
        raise HTTPException(503, '暂时无法核验当前知识库原件，未读取历史研究事实') from None


def current_version(scope, sources):
    from tin.provenance.bridge import structured_request
    try:
        response = structured_request('POST','/api/research/facts',
            {'sources':sources,'metadata_only':True},return_error_response=True)
        if response.status_code in {401,403,404,409}:
            raise HTTPException(response.status_code,'当前知识库结构化来源或授权已改变，请重新核对')
        response.raise_for_status()
        doc = response.json()
        if (not isinstance(doc,dict) or doc.get('metadata_only') is not True
                or doc.get('knowledge_base_id') != scope.knowledge_base_id
                or doc.get('knowledge_base_tenant_id') != scope.knowledge_base_tenant_id
                or not isinstance(doc.get('version'),str) or not re.fullmatch('[0-9a-f]{64}',doc['version'])):
            raise ValueError('Invalid current version')
        return doc['version']
    except HTTPException:
        raise
    except (httpx.HTTPError,ValueError,TypeError,KeyError,OSError):
        raise HTTPException(503,'当前知识库版本暂时无法核验，未返回缓存事实') from None


def fact_pages(scope, sources=None, pinned_version=None):
    from tin.provenance.bridge import structured_request
    sources, offset, version, total = current_sources(scope) if sources is None else sources, 0, pinned_version, None
    catalog = None
    while True:
        body = {'sources': sources, 'offset': offset, 'limit': 10000}
        if version is not None:
            body['version'] = version
        try:
            response = structured_request('POST', '/api/research/facts', body, return_error_response=True)
            if response.status_code == 409:
                raise HTTPException(409, '知识库结构化版本在读取期间改变或已失效，请重新核对')
            if response.status_code in {401, 403, 404}:
                raise HTTPException(response.status_code, '当前知识库事实授权已失效')
            response.raise_for_status()
            page = response.json()
        except (httpx.HTTPError, ValueError, TypeError, KeyError, OSError):
            raise HTTPException(503, '知识库结构化事实暂时不可读，未使用研究台历史副本') from None
        if (not isinstance(page, dict) or page.get('knowledge_base_id') != scope.knowledge_base_id
                or page.get('knowledge_base_tenant_id') != scope.knowledge_base_tenant_id
                or not isinstance(page.get('version'), str) or not re.fullmatch('[0-9a-f]{64}', page['version'])
                or not isinstance(page.get('indicators'), list) or not isinstance(page.get('rows'), list)
                or type(page.get('total')) is not int or not 0 <= page['total'] <= MAX_ROWS
                or page.get('offset') != offset or len(page['rows']) > 10000):
            raise HTTPException(503, '知识库事实响应不完整或超过规模上限，未返回部分事实')
        if catalog is None:
            if version is not None and page['version'] != version:
                raise HTTPException(409,'知识库版本在装载前改变，请重试')
            version, total, catalog = page['version'], page['total'], page['indicators']
        if page['version'] != version or page['total'] != total or page['indicators'] != catalog:
            raise HTTPException(409, '知识库结构化版本在读取期间改变，请刷新重试')
        next_offset = page.get('next_offset')
        consumed = offset + len(page['rows'])
        if (consumed > total or (next_offset is None and (consumed != total or page.get('truncated')))
                or (next_offset is not None and (not page['rows'] or next_offset != consumed or next_offset >= total))):
            raise HTTPException(503, '知识库事实分页不完整，未使用截断数据')
        yield page
        if next_offset is None:
            return
        offset = next_offset


def _insert(connection, table, rows):
    if not rows:
        return
    columns = list(rows[0])
    sql = 'INSERT INTO temp."_kb_' + table + '" (' + ','.join('"' + c + '"' for c in columns) + ') VALUES (' + ','.join('?' for _ in columns) + ')'
    connection.exec_driver_sql(sql, [tuple(row.get(c) for c in columns) for row in rows])


def _sql_time(value):
    return value.astimezone(timezone.utc).replace(tzinfo=None).strftime('%Y-%m-%d %H:%M:%S.%f')


def install_projection(connection, scope, sources=None, pinned_version=None):
    connection.connection.driver_connection.set_authorizer(None)
    connection.exec_driver_sql('PRAGMA temp_store=MEMORY')
    connection.exec_driver_sql('DROP TABLE IF EXISTS temp._kb_evidence')
    connection.exec_driver_sql('CREATE TEMP TABLE _kb_evidence '
        '(id INTEGER PRIMARY KEY,series_id TEXT NOT NULL,value_cell TEXT,date_cell TEXT,raw_value TEXT,formula TEXT,source_date TEXT)')
    for table in FACT_TABLES:
        connection.exec_driver_sql('DROP VIEW IF EXISTS temp."' + table + '"')
        connection.exec_driver_sql('DROP TABLE IF EXISTS temp."_kb_' + table + '"')
        connection.exec_driver_sql('CREATE TEMP TABLE "_kb_' + table + '" AS SELECT * FROM main."' + table + '" WHERE 0')
    known, seen, version, datasets, raw_counts = {}, {}, None, [], {}
    for index, page in enumerate(fact_pages(scope, sources, pinned_version)):
        version = page['version']
        if index == 0:
            datasets = page.get('datasets', [])
            indicators = []
            for spec in page['indicators']:
                if (not isinstance(spec, dict) or not isinstance(spec.get('series_id'), str)
                        or not re.fullmatch('kb-[0-9a-f]{48}', spec['series_id'])
                        or spec['series_id'] in known or not isinstance(spec.get('source_knowledge_id'), str)
                        or not isinstance(spec.get('dataset_id'), str) or not isinstance(spec.get('source_series_id'), str)):
                    raise HTTPException(503, '知识库字段身份不完整，未构建研究事实')
                name = spec.get('metric') or spec.get('name')
                if not isinstance(name, str) or not name.strip():
                    raise HTTPException(503, '知识库真实字段缺少名称')
                known[spec['series_id']] = spec
                indicators.append({'series_id': spec['series_id'], 'name': name, 'variety': '',
                    'category': '知识库字段', 'caliber': json.dumps({'time_type': '原表日期', 'note': '原表日期只用于日期查询，不声明发布时间或当时可知性'}, ensure_ascii=False),
                    'unit': spec.get('unit') or '未注明', 'source': '知识库结构化',
                    'source_url': None, 'frequency': spec.get('frequency') or '未注明', 'fetch_mode': 'readonly',
                    'phase': '', 'owner': None, 'status': '可用', 'is_proxy': 0, 'vendor_code': None,
                    'note': '事实来自绑定知识库当前已确认工作表；在知识库统一维护', 'default_entry_time': None})
            _insert(connection, 'indicators', indicators)
        rows,evidence_rows = [],[]
        for raw in page['rows']:
            spec = known.get(raw.get('series_id')) if isinstance(raw, dict) else None
            if spec is None or any(raw.get(k) != spec.get(k) for k in ('source_knowledge_id', 'dataset_id', 'source_series_id')):
                raise HTTPException(503, '知识库观测来源与字段身份不一致')
            raw_counts[spec['dataset_id']] = raw_counts.get(spec['dataset_id'], 0) + 1
            value = raw.get('value')
            if raw.get('value_status') not in {'valid', 'ok', '正常'} or value is None:
                continue
            if type(value) not in {int, float} or not math.isfinite(value):
                raise HTTPException(503, '知识库观测数值无效')
            when = raw.get('date')
            if when is None:
                continue
            try:
                if not isinstance(when, str):
                    raise ValueError
                if len(when) == 10:
                    parsed = datetime.combine(date.fromisoformat(when), time.min, SHANGHAI)
                else:
                    parsed = datetime.fromisoformat(when)
                    if parsed.tzinfo is None:
                        raise ValueError
            except (ValueError, TypeError):
                raise HTTPException(503, '知识库日期没有明确映射，未推定研究时点') from None
            stamp = _sql_time(parsed)
            unique = raw['series_id'], stamp
            if unique in seen:
                if seen[unique] != value:
                    raise HTTPException(409, '同一知识库字段日期存在冲突观测，请在结构化数据中处理')
                continue
            seen[unique] = value
            identifier = int(hashlib.sha256(json.dumps([spec['source_knowledge_id'], spec['dataset_id'], spec['source_series_id'], raw.get('source_cell'), when], ensure_ascii=False).encode()).hexdigest()[:15], 16)
            rows.append({'id': identifier, 'series_id': raw['series_id'], 'value': value, 'as_of': stamp,
                'fetched_at': _sql_time(datetime.now(timezone.utc)), 'caliber_snapshot': json.dumps({'time_type': '原表日期'}),
                'source': '知识库结构化', 'source_url': None, 'revision': 0, 'entered_by': None,
                'note': '直接读取知识库已确认结构化数据，原表日期不表示发布时间', 'status': '正常'})
            evidence_rows.append((identifier,raw['series_id'],raw.get('source_cell'),raw.get('date_cell'),
                json.dumps(raw.get('raw_value'),ensure_ascii=False),raw.get('formula'),when))
        _insert(connection, 'observations', rows)
        if evidence_rows:
            connection.exec_driver_sql('INSERT INTO temp._kb_evidence VALUES (?,?,?,?,?,?,?)',evidence_rows)
    connection.exec_driver_sql('CREATE INDEX temp._kb_obs_lookup ON _kb_observations(series_id,as_of)')
    connection.exec_driver_sql('CREATE UNIQUE INDEX temp._kb_indicator_lookup ON _kb_indicators(series_id)')
    for table in FACT_TABLES:
        connection.exec_driver_sql('CREATE TEMP VIEW "' + table + '" AS SELECT * FROM temp."_kb_' + table + '"')
    connection.commit()

    def protect(action, first, second, database, trigger):
        if action == sqlite3.SQLITE_READ and database == 'main' and first in FACT_TABLES:
            return sqlite3.SQLITE_DENY
        if action in {sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE, sqlite3.SQLITE_DELETE} and (first in FACT_TABLES or str(first).startswith('_kb_')):
            return sqlite3.SQLITE_DENY
        return sqlite3.SQLITE_OK

    connection.connection.driver_connection.set_authorizer(protect)
    return {'knowledge_facts': True, 'knowledge_facts_version': version,
            'knowledge_facts_catalog': known,
            'knowledge_facts_datasets': [{**dataset,
                'series_count': sum(spec['dataset_id'] == dataset.get('dataset_id') for spec in known.values()),
                'observation_count': raw_counts.get(dataset.get('dataset_id'), 0)} for dataset in datasets
                if isinstance(dataset, dict) and any(spec['dataset_id'] == dataset.get('dataset_id') for spec in known.values())]}


class KnowledgeFactSession(Session):
    def close(self):
        connection = self.info.pop('_knowledge_connection', None)
        try:
            super().close()
        finally:
            if connection is not None:
                cached = connection.info.get('_kb_projection')
                if cached is None or cached['expires'] <= clock.monotonic():
                    connection.invalidate()
                connection.close()


def projected_session(engine, scope, **kwargs):
    connection = _projection_engine(engine).connect()
    try:
        sources = current_sources(scope)
        version = current_version(scope, sources)
        key = (scope.knowledge_base_tenant_id,scope.knowledge_base_id,version,
               settings.structured_data_url,settings.weknora_api_url)
        cached = connection.info.get('_kb_projection')
        if cached is not None and cached['key'] == key and cached['expires'] > clock.monotonic():
            info = cached['info']
        else:
            connection.info.pop('_kb_projection',None)
            info = install_projection(connection,scope,sources,version)
            size = _retained_size(info)
            size += connection.exec_driver_sql('PRAGMA temp.page_count').scalar() * connection.exec_driver_sql('PRAGMA temp.page_size').scalar()
            if size <= MAX_RETAINED_BYTES:
                connection.info['_kb_projection'] = {'key':key,'info':info,
                    'expires':clock.monotonic()+PROJECTION_SECONDS,'bytes':size}
            # PRAGMA introspection starts SQLAlchemy's outer transaction. End
            # it before binding a Session, so authored result commits persist.
            connection.commit()
        session = KnowledgeFactSession(bind=connection, expire_on_commit=False, **kwargs)
        session.info.update(info, _knowledge_connection=connection,
                            knowledge_base_id=scope.knowledge_base_id,
                            knowledge_base_tenant_id=scope.knowledge_base_tenant_id)
        return session
    except BaseException:
        connection.invalidate()
        connection.close()
        raise


def canonical_observation_sources(session, observation_id):
    if not session.info.get('knowledge_facts'):
        return []
    from sqlalchemy import text
    row = session.execute(text('SELECT series_id,value_cell,date_cell,raw_value,formula,source_date '
                              'FROM temp._kb_evidence WHERE id=:id'),{'id':observation_id}).first()
    if row is None:
        return []
    spec = session.info['knowledge_facts_catalog'].get(row[0])
    if spec is None:
        raise HTTPException(503,'观测证据与知识库字段身份不一致')
    locator = {'knowledge_id':spec['source_knowledge_id'],'knowledge_base_id':session.info['knowledge_base_id'],
        'dataset_id':spec['dataset_id'],'series_id':spec['source_series_id'],'sheet':spec.get('sheet'),
        'value_cell':row[1],'date_cell':row[2],'raw_value':json.loads(row[3]),'formula':row[4],
        'date':row[5],'version':session.info['knowledge_facts_version'],
        'source_sha256':spec.get('source_sha256'),'filename':spec.get('source_file'),
        'header_cells':spec.get('header_cells'),'frequency':spec.get('frequency'),
        'date_mode':spec.get('date_mode'),'dimensions':spec.get('dimensions')}
    return [{'knowledge_id': locator['knowledge_id'], 'knowledge_base_id': locator['knowledge_base_id'],
        'sha256': locator.get('source_sha256'), 'filename': locator.get('filename'),
        'dataset_id': locator['dataset_id'], 'locator': locator, 'mapping_version': locator['version'],
        'url': '/platform/knowledge-bases/' + quote(locator['knowledge_base_id'], safe=''),
        'download_url': '/api/v1/knowledge/' + quote(locator['knowledge_id'], safe='') + '/download',
        'transformation': {'time_type': '原表日期', 'copied_to_research': False}}]
