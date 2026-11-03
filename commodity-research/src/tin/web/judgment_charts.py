"""Read one saved judgment binding, with a bounded observation window."""
from dataclasses import replace
from datetime import date as Date, datetime
import math
import re
from types import SimpleNamespace

import httpx
from fastapi import APIRouter, HTTPException, Query, Request
from sqlalchemy import select

from tin.config import SHANGHAI
from tin.integration.workbench_scope import artifact_session, current_workbench
from tin.judgments.service import to_payload
from tin.models import Judgment, SourceMapping
from tin.web.judgment_panels import judgment_series_ids, selected_judgment
from tin.web.restricted_views import ORIGINS_KEY, mapping_key, shared_owner_query, shared_owner_storage_available

router = APIRouter(prefix='/api/sn/judgment')


def _record(session, scope, identifier):
    if not scope.restricted:
        return selected_judgment(session, identifier, scope=scope)
    if not shared_owner_storage_available(session, scope):
        raise HTTPException(403, '历史记录尚未明确归属到当前工作台')
    row = session.scalar(shared_owner_query(scope, select(Judgment).where(Judgment.id == identifier), Judgment))
    if row is None:
        raise HTTPException(404, '没有这份已授权的研判')
    return row


def _legacy_points(scope, identifier, day, limit, origins):
    from tin.integration.kb_facts import current_sources
    from tin.provenance.bridge import structured_request
    permitted = {(scope.knowledge_base_id, scope.knowledge_base_tenant_id),
        *((source['knowledge_base_id'], source['tenant_id']) for source in scope.additional_knowledge_bases)}
    origin = origins.get(identifier) or {}
    target = (origin.get('knowledge_base_id'), origin.get('tenant_id'))
    if target not in permitted:
        raise HTTPException(409, '此指标的来源关联已失效，请核对研究逻辑')
    local = replace(scope, knowledge_base_id=target[0], knowledge_base_tenant_id=target[1],
                    additional_knowledge_bases=())
    marker = current_workbench.set(local)
    try:
        response = structured_request('POST', '/api/research/facts', {
            'sources': current_sources(local), 'series_ids': [identifier], 'view_module': 'judgments',
            'as_of': day.isoformat(), 'points_per_series': limit, 'include_invalid': True},
            return_error_response=True)
        if response.status_code in {401, 403, 404, 409}:
            raise HTTPException(response.status_code, '当前研判来源或权限已改变，请刷新')
        if response.status_code == 422:
            raise HTTPException(409, '此来源暂不支持完整均线窗口，请更新服务后重试')
        response.raise_for_status()
        result = response.json()
        if (not isinstance(result, dict) or result.get('bounded') is not True
                or result.get('include_invalid') is not True
                or result.get('knowledge_base_id') != target[0]
                or type(result.get('knowledge_base_tenant_id')) is not int
                or result['knowledge_base_tenant_id'] != target[1]
                or not isinstance(result.get('version'), str) or not re.fullmatch('[a-f0-9]{64}', result['version'])
                or result.get('offset') != 0 or result.get('next_offset') is not None
                or result.get('truncated') is not False or type(result.get('total')) is not int
                or not isinstance(result.get('rows'), list) or result['total'] != len(result['rows'])):
            raise ValueError('Invalid bound chart response')
        return result
    except HTTPException:
        raise
    except (httpx.HTTPError, ValueError, TypeError, KeyError):
        raise HTTPException(503, '当前图表数据暂不可用，请稍后重试') from None
    finally:
        current_workbench.reset(marker)


def _metric(result, identifier, day, limit):
    from tin.web.judgment_research import metric_from_rows
    try:
        fields, rows = result['indicators'], result['rows']
        if (not isinstance(fields, list) or len(fields) != 1 or not isinstance(fields[0], dict)
                or fields[0].get('series_id') != identifier or not isinstance(rows, list) or len(rows) > limit):
            raise ValueError('Unexpected chart scope')
        field = fields[0]
        if any(field.get(key) is not None and not isinstance(field[key], str)
               for key in ('metric', 'name', 'unit', 'frequency')):
            raise ValueError('Invalid chart metadata')
        observations, seen = [], set()
        for row in rows:
            stamp, value, status = row.get('date'), row.get('value'), row.get('value_status')
            if (row.get('series_id') != identifier or not isinstance(stamp, str)
                    or Date.fromisoformat(stamp).isoformat() != stamp or stamp > day.isoformat()
                    or stamp in seen or not isinstance(status, str)
                    or status not in {'valid', 'ok', '正常', 'missing'}
                    or value is not None and (type(value) not in {int, float} or not math.isfinite(value))):
                raise ValueError('Unexpected chart observation')
            seen.add(stamp)
            valid = status in {'valid', 'ok', '正常'} and value is not None
            observations.append(SimpleNamespace(as_of=datetime.fromisoformat(stamp).replace(tzinfo=SHANGHAI),
                value=value if valid else None, status='正常' if valid else '缺失'))
        indicator = SimpleNamespace(series_id=identifier, name=field.get('metric') or field.get('name') or '已绑定指标',
            unit=field.get('unit') or '', frequency=field.get('frequency') or '',
            caliber=field.get('caliber') if isinstance(field.get('caliber'), dict) else {})
        return metric_from_rows(indicator, sorted(observations, key=lambda row: row.as_of), limit=limit)
    except (ValueError, TypeError, KeyError, AttributeError):
        raise HTTPException(503, '图表响应超出当前指标或日期范围，未展示数据') from None


@router.get('/chart')
def chart(request: Request, judgment_id: int = Query(ge=1, le=2**63 - 1),
          series_id: str = Query(min_length=1, max_length=128), date: str | None = None,
          limit: int = Query(121, ge=2, le=121)):
    if (set(request.query_params) - {'judgment_id', 'series_id', 'date', 'limit'}
            or any(len(request.query_params.getlist(key)) > 1 for key in request.query_params)):
        raise HTTPException(422, '图表只接受当前研判、指标、日期与必要窗口')
    if (type(judgment_id) is not int or not 1 <= judgment_id <= 2**63 - 1
            or type(limit) is not int or not 2 <= limit <= 121
            or not isinstance(series_id, str) or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}', series_id)):
        raise HTTPException(422, '图表指标或观测范围无效')
    scope = current_workbench.get()
    if scope is None or not scope.authorization or not scope.source_available or 'judgments' not in scope.view_modules:
        raise HTTPException(403, '当前工作台未开放此研判页面')
    if scope.restricted and date is not None and date != datetime.now(SHANGHAI).date().isoformat():
        raise HTTPException(403, '受限视角仅提供当前页面的必要窗口，不开放历史范围查询')
    try:
        day = Date.fromisoformat(date) if date is not None else datetime.now(SHANGHAI).date()
        if date is not None and day.isoformat() != date or day > datetime.now(SHANGHAI).date():
            raise ValueError
    except ValueError:
        raise HTTPException(422, '图表日期须为今天或历史日期YYYY-MM-DD') from None
    with artifact_session(scope) as session:
        row = _record(session, scope, judgment_id)
        if series_id not in judgment_series_ids(row):
            raise HTTPException(403, '此指标未绑定到当前研判')
        payload = to_payload(row)
        mapping = session.get(SourceMapping, mapping_key(session, ORIGINS_KEY))
        origins = (mapping.config or {}).get('origins', {}) if mapping else {}
    if scope.scope_version == 2:
        from tin.integration.page_facts import bounded_facts
        result = bounded_facts(scope, [series_id], 'judgments', day=day.isoformat(),
            points=limit, origins=origins, include_invalid=True)
    else:
        result = _legacy_points(scope, series_id, day, limit, origins)
    metric = _metric(result, series_id, day, limit)
    from tin.web.judgment_research import metric_boundaries
    metric['boundaries'] = metric_boundaries(payload, series_id)
    return {'metric': metric}
