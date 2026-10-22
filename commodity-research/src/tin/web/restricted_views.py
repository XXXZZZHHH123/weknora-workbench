"""Page-scoped research projections for callers without raw KB access.

This path never creates a full facts session. Selected IDs come from persisted
server configuration; the browser cannot choose IDs, dates, layouts, or exports.
The source grant is refreshed for every bounded fetch and no result is cached.
"""
from dataclasses import replace
from copy import deepcopy
from datetime import date, datetime, timezone
import math
import re
from types import SimpleNamespace

import httpx
from fastapi import HTTPException
from sqlalchemy import select
from starlette.concurrency import run_in_threadpool

from tin.config import SHANGHAI, settings
from tin.integration.workbench_scope import artifact_session, current_workbench
from tin.models import DailyBrief, DiagramTemplate, Judgment, SourceMapping, Strategy
from tin.ownership import scope_query

CORE_KEY = 'workbench-market-core'
ORIGINS_KEY = 'workbench-series-origins'
DIAGRAM_KEY = 'workbench-diagram'
SERIES_ID = re.compile(r'kb-[0-9a-f]{48}')
PAGES = {
    '/': ('home', '工作台'), '/sn': ('market', '市场总览'),
    '/sn/indicators': ('indicators', '指标'), '/sn/judgment': ('judgments', '研究判断'),
    '/sn/strategies': ('strategies', '策略'), '/sn/diagram': ('diagram', '产业图'),
    '/sn/reports': ('reports', '日报'),
}


def _ids(values, limit=256):
    if (not isinstance(values, list) or len(values) > limit
            or any(not isinstance(value, str) or not SERIES_ID.fullmatch(value) for value in values)
            or len(set(values)) != len(values)):
        raise HTTPException(409, '研究台绑定指标配置无效，请由来源可读的编辑成员重新保存')
    return values


def mapping_key(session, key):
    from tin.integration.workbench_scope import canonical_workbench_id
    identifier = canonical_workbench_id(session.info.get('workbench_id'))
    return key + ':' + identifier


def remember_origins(session, identifiers):
    """Only a real verified catalog may establish an ID's source origin."""
    catalog = session.info.get('knowledge_facts_catalog')
    if not isinstance(catalog, dict) or not session.info.get('workbench_id'):
        return
    row = session.get(SourceMapping, mapping_key(session, ORIGINS_KEY))
    origins = dict((row.config or {}).get('origins') or {}) if row else {}
    for identifier in identifiers:
        spec = catalog.get(identifier)
        if isinstance(spec, dict) and spec.get('knowledge_base_id') and type(spec.get('knowledge_base_tenant_id')) is int:
            origins[identifier] = {'knowledge_base_id': spec['knowledge_base_id'],
                                   'tenant_id': spec['knowledge_base_tenant_id']}
    _save_mapping(session, ORIGINS_KEY, {'origins': origins})


def _save_mapping(session, key, config):
    key = mapping_key(session, key)
    row = session.get(SourceMapping, key)
    now = datetime.now(timezone.utc)
    actor = str(session.info.get('weknora_user_id') or '')
    if row is None:
        row = SourceMapping(id=key, config=config, confirmed_by=actor, confirmed_at=now)
        session.add(row)
    else:
        row.config, row.confirmed_by, row.confirmed_at = config, actor, now


def save_core_config(session, identifiers):
    identifiers = _ids(identifiers, 12)
    catalog = session.info.get('knowledge_facts_catalog') or {}
    if any(identifier not in catalog for identifier in identifiers):
        raise HTTPException(404, '核心指标已不在当前知识库来源，请刷新后重新选择')
    remember_origins(session, identifiers)
    _save_mapping(session, CORE_KEY, {'series_ids': identifiers})
    session.commit()


def core_config(session):
    row = session.get(SourceMapping, mapping_key(session, CORE_KEY))
    return _ids((row.config or {}).get('series_ids', []), 12) if row else []


def default_diagram(session):
    selected = session.get(SourceMapping, mapping_key(session, DIAGRAM_KEY))
    if selected:
        identifier = (selected.config or {}).get('template_id')
        row = session.scalar(select(DiagramTemplate).where(DiagramTemplate.id == identifier,
            DiagramTemplate.variety == settings.variety)) if type(identifier) is int else None
        if row is None:
            return None
        if session.info.get('source_storage_legacy'):
            # A legacy template row is mutable tenant-wide. Its ID alone does
            # not isolate WB grants. Use the layout explicitly saved in this
            # WB's namespace, so another WB editing the shared row cannot widen
            # this WB's labels, statics, or fact bindings.
            snapshot = selected.config.get('snapshot')
            if not isinstance(snapshot, dict) or not isinstance(snapshot.get('layout'), dict):
                return None
            return SimpleNamespace(id=identifier, name=str(snapshot.get('name') or ''), layout=snapshot['layout'])
        return row
    if session.info.get('source_storage_legacy'):
        # Multiple legacy workbenches share a tenant result DB. An unscoped
        # default template cannot establish this WB's permitted bindings.
        return None
    return session.scalar(select(DiagramTemplate).where(DiagramTemplate.variety == settings.variety)
        .order_by(DiagramTemplate.is_default.desc(), DiagramTemplate.updated_at.desc()).limit(1))


def remember_diagram(session, template):
    _save_mapping(session, DIAGRAM_KEY, {'template_id': template.id,
        'snapshot': {'name': template.name, 'layout': deepcopy(template.layout)}})


def _diagram_ids(layout):
    identifiers = []
    for node in layout.get('nodes') or []:
        binding = node.get('binding') or {}
        if binding.get('kind') == 'series' and binding.get('series_id'):
            identifiers.append(binding['series_id'])
    return _ids(list(dict.fromkeys(identifiers)))


def bound_series_ids(session, module):
    """An indicators grant is still a bounded workbench view, never a catalog."""
    if module == 'market':
        return core_config(session)
    if module in {'diagram', 'indicators'}:
        template = default_diagram(session)
        diagram = _diagram_ids(template.layout or {}) if template else []
        return list(dict.fromkeys((core_config(session) if module == 'indicators' else []) + diagram))
    return []


def _bounded_part(scope, identifiers, module):
    from tin.integration.kb_facts import current_sources
    from tin.provenance.bridge import structured_request
    sources = current_sources(scope)  # Includes a fresh delegated page consent.
    body = {'sources': sources, 'series_ids': identifiers, 'view_module': module,
            'as_of': datetime.now(SHANGHAI).date().isoformat(), 'points_per_series': 2}
    marker = current_workbench.set(scope)
    try:
        response = structured_request('POST', '/api/research/facts', body, return_error_response=True)
        if response.status_code in {401, 403, 404, 409}:
            raise HTTPException(response.status_code, '研究台来源或绑定指标授权已改变，请重新打开')
        response.raise_for_status()
        doc = response.json()
        if (not isinstance(doc, dict) or doc.get('bounded') is not True
                or doc.get('knowledge_base_id') != scope.knowledge_base_id
                or doc.get('knowledge_base_tenant_id') != scope.knowledge_base_tenant_id
                or not isinstance(doc.get('version'), str) or not re.fullmatch('[0-9a-f]{64}', doc['version'])
                or not isinstance(doc.get('indicators'), list) or not isinstance(doc.get('rows'), list)
                or doc.get('offset') != 0 or doc.get('next_offset') is not None or doc.get('truncated') is not False
                or type(doc.get('total')) is not int or doc['total'] != len(doc['rows'])
                or len(doc['rows']) > len(identifiers) * 2):
            raise ValueError('Invalid bounded projection')
        catalog = {}
        for spec in doc['indicators']:
            identifier = spec.get('series_id') if isinstance(spec, dict) else None
            if identifier not in identifiers or identifier in catalog:
                raise ValueError('Unexpected indicator')
            catalog[identifier] = spec
        if set(catalog) != set(identifiers):
            raise ValueError('Incomplete bound catalog')
        result = {}
        for row in doc['rows']:
            identifier = row.get('series_id') if isinstance(row, dict) else None
            value, stamp = row.get('value'), row.get('date')
            if (identifier not in catalog or isinstance(value, bool) or not isinstance(value, (float, int))
                    or not math.isfinite(value) or not isinstance(stamp, str)
                    or row.get('value_status') not in {'valid', 'ok', '正常'}):
                raise ValueError('Unexpected observation')
            day = date.fromisoformat(stamp[:10])
            if day.isoformat() > body['as_of']:
                raise ValueError('Out of range observation')
            entries = result.setdefault(identifier, [])
            if len(entries) >= 2 or any(item['date'] == day.isoformat() for item in entries):
                raise ValueError('Duplicate or excess observations')
            entries.append({'date': day.isoformat(), 'value': value})
        output = {}
        for identifier in identifiers:
            spec = catalog.get(identifier)
            if spec is None:
                continue
            rows = sorted(result.get(identifier, []), key=lambda row: row['date'], reverse=True)
            latest, previous = (rows[0] if rows else {}), (rows[1] if len(rows) > 1 else {})
            value, old = latest.get('value'), previous.get('value')
            output[identifier] = {'name': spec.get('metric') or spec.get('name') or '已绑定指标',
                'unit': spec.get('unit') or '', 'date': latest.get('date'), 'value': value,
                'change': value - old if value is not None and old is not None else None}
        return output
    except HTTPException:
        raise
    except (httpx.HTTPError, ValueError, TypeError, KeyError, AttributeError, OSError):
        raise HTTPException(503, '必要指标响应超出授权范围或暂不可用，未展示缓存数据') from None
    finally:
        current_workbench.reset(marker)


def necessary_values(session, scope, identifiers, module):
    if not identifiers:
        return {}
    row = session.get(SourceMapping, mapping_key(session, ORIGINS_KEY))
    origins = (row.config or {}).get('origins') or {} if row else {}
    permitted = {(scope.knowledge_base_id, scope.knowledge_base_tenant_id)}
    permitted.update((item['knowledge_base_id'], item['tenant_id']) for item in scope.additional_knowledge_bases)
    groups = {}
    for identifier in identifiers:
        origin = origins.get(identifier)
        # Missing origin metadata is not permission to try different KBs or
        # search a catalog. The source-readable editor must save it first.
        target = ((origin.get('knowledge_base_id'), origin.get('tenant_id')) if isinstance(origin, dict)
                  else None)
        if target in permitted:
            groups.setdefault(target, []).append(identifier)
    output = {}
    for (kb_id, tenant_id), ids in groups.items():
        source_scope = replace(scope, knowledge_base_id=kb_id, knowledge_base_tenant_id=tenant_id,
                               additional_knowledge_bases=())
        output.update(_bounded_part(source_scope, ids, module))
    return output


def page_data(scope, module):
    """Page bindings and private artifacts are read without any fact projection."""
    with artifact_session(scope) as session:
        identifiers = bound_series_ids(session, module)
        values = necessary_values(session, scope, identifiers, module)
        output = {'cards': [values[identifier] for identifier in identifiers if identifier in values],
                  'diagram': None, 'entries': []}
        if module == 'diagram':
            template = default_diagram(session)
            if template:
                layout = template.layout or {}
                nodes = []
                for source in layout.get('nodes') or []:
                    node = {key: source.get(key) for key in ('id', 'label', 'kind', 'layer', 'parent')}
                    for key, fallback in [('x', 0), ('y', 0), ('w', 160), ('h', 80)]:
                        number = source.get(key, fallback)
                        node[key] = number if type(number) in {float, int} and math.isfinite(number) else fallback
                    binding = source.get('binding') or {}
                    node['data'] = values.get(binding.get('series_id')) if binding.get('kind') == 'series' else None
                    node['statics'] = [{'label': str(item.get('label') or ''), 'value': str(item.get('value') or '')}
                                       for item in source.get('statics') or [] if isinstance(item, dict)]
                    nodes.append(node)
                by_id = {node['id']: node for node in nodes}
                edges = []
                for edge in layout.get('edges') or []:
                    source, target = by_id.get(edge.get('from')), by_id.get(edge.get('to'))
                    if source and target:
                        edges.append({'x1': source['x'] + source['w']/2, 'y1': source['y'] + source['h'],
                                      'x2': target['x'] + target['w']/2, 'y2': target['y']})
                output['diagram'] = {'name': template.name, 'nodes': nodes, 'edges': edges,
                    'width': max((node['x'] + node['w'] for node in nodes), default=800) + 20,
                    'height': max((node['y'] + node['h'] for node in nodes), default=400) + 20}
        if module in {'home', 'judgments', 'strategies', 'reports'} and not session.info.get('source_storage_legacy'):
            # Personal result ownership remains independent from WB visibility.
            # Saved snapshots, claims, source captures and generated signal
            # payloads may contain unrelated facts and are deliberately absent.
            if module == 'judgments':
                rows = session.scalars(scope_query(session, select(Judgment).where(Judgment.variety == settings.variety), Judgment)
                    .order_by(Judgment.version.desc()).limit(20))
                output['entries'] = [{'title': f'研究逻辑 v{row.version}', 'status': row.status,
                    'text': '', 'date': row.written_at.isoformat()} for row in rows]
            elif module == 'strategies':
                rows = session.scalars(scope_query(session, select(Strategy).where(Strategy.variety == settings.variety), Strategy)
                    .order_by(Strategy.created_at.desc()).limit(20))
                output['entries'] = [{'title': row.title, 'status': row.status, 'text': '',
                    'date': row.created_at.date().isoformat()} for row in rows]
            elif module == 'reports':
                rows = session.scalars(scope_query(session, select(DailyBrief).where(DailyBrief.variety == settings.variety), DailyBrief)
                    .order_by(DailyBrief.trade_date.desc()).limit(20))
                output['entries'] = [{'title': row.trade_date + ' 日报', 'status': row.status,
                    'text': '', 'date': row.trade_date} for row in rows]
        return output


async def restricted_response(request, scope, route_path):
    if request.method not in {'GET', 'HEAD'} or route_path not in PAGES:
        raise HTTPException(403, '受限视角不开放指标目录、任意取数、原文、导出和修改接口')
    if set(request.query_params) - {'_research_tenant', '_research_user'}:
        raise HTTPException(422, '受限视角由服务端绑定确定指标和最新时点，不接受自选查询范围')
    module, title = PAGES[route_path]
    authorized = scope.source_available and module in scope.view_modules
    payload = await run_in_threadpool(page_data, scope, module) if authorized else {'cards': [], 'entries': [], 'diagram': None}
    from tin.web.app import templates
    return templates.TemplateResponse(request, 'restricted.html', {'nav': {'market': 'variety', 'judgments': 'judgment'}.get(module, module),
        'restricted_view': True, 'restricted_module': module, 'restricted_title': title,
        'restricted_authorized': authorized, 'restricted_pages': PAGES, 'restricted_data': payload,
        'd': None, 'ticker': [], 'manual_indicators': [], 'err': None})
