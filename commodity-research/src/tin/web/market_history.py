"""Bounded historical charts of explicitly selected, current-KB observations.

This is a historical view, never a replacement maturity/contract classification.
Restricted callers use the page-scoped facts protocol, not a facts projection.
"""
from dataclasses import replace
from datetime import date, datetime
import math
import re

import httpx
from fastapi import HTTPException
from sqlalchemy import func, select

from tin.compute.engine import day_end
from tin.config import SHANGHAI
from tin.integration.workbench_scope import current_workbench, require_shared_write
from tin.models import Indicator, Observation, SourceMapping
from tin.web.restricted_views import (
    ORIGINS_KEY, _ids, _save_mapping, mapping_key, remember_origins,
)

HISTORY_KEY = 'workbench-market-history'
MAX_HISTORY_SERIES = 12
MAX_HISTORY_POINTS = 180


def _points(value):
    if type(value) is not int or not 1 <= value <= MAX_HISTORY_POINTS:
        raise HTTPException(409, '历史行情点数配置无效，最多180点')
    return value


def history_config(session):
    """History is explicit: core-card consent alone never publishes 180 points."""
    if not session.info.get('workbench_id'):
        raise HTTPException(409, '请从绑定知识库的研究工作台进入')
    row = session.get(SourceMapping, mapping_key(session, HISTORY_KEY))
    if row is None:
        return {'series_ids': [], 'points_per_series': MAX_HISTORY_POINTS}
    config = row.config
    if not isinstance(config, dict):
        raise HTTPException(409, '历史行情配置无效，请重新保存')
    return {'series_ids': _ids(config.get('series_ids'), MAX_HISTORY_SERIES),
            'points_per_series': _points(config.get('points_per_series', MAX_HISTORY_POINTS))}


def save_history_config(session, identifiers, points_per_series=MAX_HISTORY_POINTS):
    """Only a current source-readable editor may publish explicit history IDs."""
    scope = current_workbench.get()
    if (scope is None or scope.restricted or not scope.source_available and scope.scope_version != 2
            or session.info.get('workbench_id') != scope.workbench_id
            or session.info.get('knowledge_facts') is not True):
        raise HTTPException(403, '只有来源可读的研究台编辑成员可以配置历史行情')
    require_shared_write(scope)
    identifiers = _ids(identifiers, MAX_HISTORY_SERIES)
    points_per_series = _points(points_per_series)
    catalog = session.info.get('knowledge_facts_catalog')
    if not isinstance(catalog, dict) or any(identifier not in catalog for identifier in identifiers):
        raise HTTPException(404, '历史行情指标已不在当前知识库来源，请刷新后重新选择')
    remember_origins(session, identifiers)
    _save_mapping(session, HISTORY_KEY, {'series_ids': identifiers, 'points_per_series': points_per_series})
    session.commit()
    return {'series_ids': identifiers, 'points_per_series': points_per_series}


def _through(day):
    if type(day) is not date:
        raise HTTPException(400, '历史行情需要明确的日期边界')
    return day.isoformat()


def _chart(identifier, name, unit, observations):
    """Keep every real date/value and use time distance rather than row spacing."""
    rows = sorted(observations, key=lambda row: row['date'])
    values = [row['value'] for row in rows]
    result = {'series_id': identifier, 'name': name, 'unit': unit or '原表未注明单位',
              'points': [], 'count': len(rows), 'path': '', 'first_date': None,
              'last_date': None, 'latest': None, 'min': None, 'max': None}
    if not rows:
        return result
    low, high = min(values), max(values)
    # Scale before subtracting to keep finite coordinates even near Float64 limits.
    factor = max(abs(low), abs(high), 1)
    floor, span = low / factor, high / factor - low / factor
    days = [date.fromisoformat(row['date']).toordinal() for row in rows]
    distance = days[-1] - days[0]
    points = [{**row, 'x': round(52 + ((tick - days[0]) / distance * 432 if distance else 216), 2),
               'y': round(16 + ((high / factor - row['value'] / factor) / span * 112 if span else 56), 2)}
              for row, tick in zip(rows, days)]
    result.update(points=points, path=' '.join(f"{point['x']},{point['y']}" for point in points),
                  first_date=rows[0]['date'], last_date=rows[-1]['date'], latest=values[-1], min=low, max=high)
    return result


def _result(config, day, series):
    return {'series': series, 'series_ids': list(config['series_ids']),
            'points_per_series': config['points_per_series'], 'through': _through(day),
            'configured': bool(config['series_ids'])}


def market_history(session, day):
    """Read only selected IDs from an already verified current-KB session."""
    scope = current_workbench.get()
    if (scope is None or scope.restricted or not scope.source_available and scope.scope_version != 2
            or session.info.get('workbench_id') != scope.workbench_id
            or session.info.get('knowledge_facts') is not True):
        raise HTTPException(403, '历史行情只读取当前知识库的已授权事实')
    _through(day)
    config = history_config(session)
    identifiers = config['series_ids']
    catalog = session.info.get('knowledge_facts_catalog')
    if not isinstance(catalog, dict):
        raise HTTPException(503, '当前知识库目录无法核验，未读取历史行情')
    unavailable = [identifier for identifier in identifiers if identifier not in catalog]
    if unavailable:
        # Source deletion or confirmation withdrawal may invalidate a saved
        # selection. Keep its configuration and let the editor select again;
        # never silently chart a partial set or fall back to old fact tables.
        return {**_result(config, day, []), 'unavailable': unavailable,
                'notice': '部分已选指标已不在当前已确认资料中，请重新选择行情。'}
    chosen = {row.series_id: row for row in session.scalars(
        select(Indicator).where(Indicator.series_id.in_(identifiers)))} if identifiers else {}
    if set(chosen) != set(identifiers):
        raise HTTPException(404, '历史行情绑定指标已改变，请刷新后重新选择')
    charts = []
    for identifier in identifiers:
        # Rank inside a selected, indexed field; SQL LIMIT bounds materialized rows.
        # Latest revision wins before status filtering, so invalid newer revisions
        # do not silently revive an earlier valid value.
        ranked = select(Observation.as_of, Observation.value, Observation.status,
            func.row_number().over(partition_by=Observation.as_of,
                order_by=(Observation.revision.desc(), Observation.id.desc())).label('rank')).where(
                    Observation.series_id == identifier, Observation.as_of < day_end(day)).subquery()
        rows = session.execute(select(ranked.c.as_of, ranked.c.value).where(
            ranked.c.rank == 1, ranked.c.status == '正常').order_by(ranked.c.as_of.desc())
            .limit(config['points_per_series'])).all()
        observations = []
        seen = set()
        for stamp, value in rows:
            if type(value) not in {int, float} or not math.isfinite(value):
                raise HTTPException(503, '历史行情含无效数值，未展示部分结果')
            original_day = stamp.astimezone(SHANGHAI).date().isoformat()
            # The KB projection has one original date per field; unexpected
            # intraday duplicates must not become an arbitrary daily close.
            if original_day in seen:
                raise HTTPException(409, '历史行情同一天有多个时点，请先明确日期口径')
            seen.add(original_day)
            observations.append({'date': original_day, 'value': value})
        spec = chosen[identifier]
        charts.append(_chart(identifier, spec.name, spec.unit, observations))
    return _result(config, day, charts)


def _bounded_history_part(scope, identifiers, day, points_per_series):
    """No catalog, offsets, source cells, raw file, arbitrary range or cache."""
    from tin.integration.kb_facts import current_sources
    from tin.provenance.bridge import structured_request
    marker = current_workbench.set(scope)
    try:
        sources = current_sources(scope)  # Fresh delegated page authorization.
        body = {'sources': sources, 'series_ids': identifiers, 'view_module': 'market',
                'as_of': _through(day), 'points_per_series': points_per_series}
        response = structured_request('POST', '/api/research/facts', body, return_error_response=True)
        if response.status_code in {401, 403, 404, 409}:
            raise HTTPException(response.status_code, '历史行情来源授权已改变，请重新打开')
        response.raise_for_status()
        doc = response.json()
        if (not isinstance(doc, dict) or doc.get('bounded') is not True
                or doc.get('knowledge_base_id') != scope.knowledge_base_id
                or type(doc.get('knowledge_base_tenant_id')) is not int
                or doc['knowledge_base_tenant_id'] != scope.knowledge_base_tenant_id
                or not isinstance(doc.get('version'), str) or not re.fullmatch('[0-9a-f]{64}', doc['version'])
                or doc.get('offset') != 0 or doc.get('next_offset') is not None
                or doc.get('truncated') is not False or type(doc.get('total')) is not int
                or not isinstance(doc.get('rows'), list) or doc['total'] != len(doc['rows'])
                or len(doc['rows']) > len(identifiers) * points_per_series
                or not isinstance(doc.get('indicators'), list)
                or len(doc['indicators']) != len(identifiers)):
            raise ValueError('Invalid bounded history')
        chosen = {}
        observations = {identifier: [] for identifier in identifiers}
        seen = set()
        for spec in doc['indicators']:
            identifier = spec.get('series_id') if isinstance(spec, dict) else None
            if identifier not in observations or identifier in chosen:
                raise ValueError('Unexpected indicator')
            chosen[identifier] = spec
        if set(chosen) != set(identifiers):
            raise ValueError('Incomplete history catalog')
        for row in doc['rows']:
            identifier = row.get('series_id') if isinstance(row, dict) else None
            stamp, value = row.get('date'), row.get('value')
            if (identifier not in observations or not isinstance(stamp, str)
                    or date.fromisoformat(stamp).isoformat() != stamp or stamp > body['as_of']
                    or type(value) not in {int, float} or not math.isfinite(value)
                    or row.get('value_status') not in {'valid', 'ok', '正常'}
                    or (identifier, stamp) in seen
                    or len(observations[identifier]) >= points_per_series):
                raise ValueError('Unexpected history observation')
            seen.add((identifier, stamp))
            observations[identifier].append({'date': stamp, 'value': value})
        return {identifier: _chart(identifier, str(chosen[identifier].get('metric')
                    or chosen[identifier].get('name') or '已绑定指标'),
                    str(chosen[identifier].get('unit') or ''), observations[identifier])
                for identifier in identifiers}
    except HTTPException:
        raise
    except (httpx.HTTPError, ValueError, TypeError, KeyError, AttributeError, OSError):
        raise HTTPException(503, '历史行情响应超出授权范围或暂不可用，未展示缓存数据') from None
    finally:
        current_workbench.reset(marker)


def readonly_market_history(session, scope):
    """Read the saved page's finite history, without opening a facts session."""
    if (not scope.restricted or not scope.source_available or 'market' not in scope.view_modules
            or session.info.get('workbench_id') != scope.workbench_id):
        raise HTTPException(403, '当前成员没有市场历史行情展示授权')
    config = history_config(session)
    day = datetime.now(SHANGHAI).date()
    if not config['series_ids']:
        return _result(config, day, [])
    row = session.get(SourceMapping, mapping_key(session, ORIGINS_KEY))
    origins = (row.config or {}).get('origins') if row else None
    if not isinstance(origins, dict):
        raise HTTPException(409, '历史行情缺少已核验的来源绑定，请由编辑成员重新保存')
    if scope.scope_version == 2:
        from tin.integration.page_facts import bounded_facts
        result = bounded_facts(scope,config['series_ids'],'market',points=config['points_per_series'],origins=origins)
        if result.get('unavailable_series_ids'):
            return {**_result(config,day,[]),'unavailable':result['unavailable_series_ids'],
                'notice':'部分历史行情来源已解除或不可用，请重新选择。'}
        metadata = {field['series_id']:field for field in result['indicators']}
        return _result(config,day,[_chart(identifier,str(metadata[identifier].get('metric') or metadata[identifier].get('name') or '已绑定指标'),
            str(metadata[identifier].get('unit') or ''),[{'date':row['date'],'value':row['value']} for row in result['rows'] if row['series_id']==identifier])
            for identifier in config['series_ids']])
    permitted = {(scope.knowledge_base_id, scope.knowledge_base_tenant_id)}
    permitted.update((item['knowledge_base_id'], item['tenant_id']) for item in scope.additional_knowledge_bases)
    groups = {}
    for identifier in config['series_ids']:
        origin = origins.get(identifier)
        if not isinstance(origin, dict) or type(origin.get('tenant_id')) is not int:
            raise HTTPException(409, '历史行情来源绑定已失效，请由编辑成员重新保存')
        target = origin.get('knowledge_base_id'), origin['tenant_id']
        if target not in permitted:
            raise HTTPException(403, '历史行情绑定不在当前工作台来源授权内')
        groups.setdefault(target, []).append(identifier)
    charts = {}
    for (kb_id, tenant_id), identifiers in groups.items():
        source_scope = replace(scope, knowledge_base_id=kb_id, knowledge_base_tenant_id=tenant_id,
                               additional_knowledge_bases=())
        charts.update(_bounded_history_part(source_scope, identifiers, day, config['points_per_series']))
    return _result(config, day, [charts[identifier] for identifier in config['series_ids']])
