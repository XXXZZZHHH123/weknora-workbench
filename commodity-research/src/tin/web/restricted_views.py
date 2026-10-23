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
from urllib.parse import quote

import httpx
from fastapi import HTTPException
from sqlalchemy import select
from starlette.concurrency import run_in_threadpool

from tin.config import SHANGHAI, settings
from tin.integration.workbench_scope import artifact_session, current_workbench
from tin.models import DailyBrief, DiagramTemplate, Judgment, Researcher, SourceMapping, Strategy
from tin.models.workbench import WorkbenchArtifact, WorkbenchStorageScope

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


def shared_diagrams(session):
    """All layouts in this workbench, never an unscoped legacy tenant list."""
    selected = session.get(SourceMapping, mapping_key(session, DIAGRAM_KEY))
    query = select(DiagramTemplate).where(DiagramTemplate.variety == settings.variety)
    if not session.info.get('source_storage_legacy'):
        # An isolated research DB belongs to exactly one verified workbench.
        # Layouts are shared workbench configuration, not personal records.
        return list(session.scalars(query.order_by(
            DiagramTemplate.is_default.desc(), DiagramTemplate.updated_at.desc(), DiagramTemplate.id)))
    snapshots = _diagram_snapshots(selected.config or {}) if selected else {}
    if not snapshots:
        # Legacy workbenches share a tenant DB. Only snapshots explicitly saved
        # in this workbench's namespace establish its permitted structure.
        return []
    rows = session.scalars(query.where(DiagramTemplate.id.in_([int(key) for key in snapshots]))
        .order_by(DiagramTemplate.is_default.desc(), DiagramTemplate.updated_at.desc(), DiagramTemplate.id))
    return [SimpleNamespace(id=row.id, name=str(snapshots[str(row.id)].get('name') or ''),
        layout=snapshots[str(row.id)]['layout'], is_default=row.is_default) for row in rows]


def _diagram_snapshots(config):
    snapshots = config.get('snapshots')
    result = {str(int(key)): value for key, value in snapshots.items()
        if isinstance(key, str) and len(key) <= 19 and key.isascii() and key.isdecimal() and 0 < int(key) <= 2**63 - 1
        and isinstance(value, dict) and isinstance(value.get('layout'), dict)} if isinstance(snapshots, dict) else {}
    identifier, snapshot = config.get('template_id'), config.get('snapshot')
    if type(identifier) is int and 0 < identifier <= 2**63 - 1 and isinstance(snapshot, dict) and isinstance(snapshot.get('layout'), dict):
        # Preserve the original single-layout format without a data migration.
        result.setdefault(str(identifier), snapshot)
    return result


def default_diagram(session, rows=None):
    rows = shared_diagrams(session) if rows is None else rows
    selected = session.get(SourceMapping, mapping_key(session, DIAGRAM_KEY))
    if selected:
        identifier = (selected.config or {}).get('template_id')
        current = next((row for row in rows if row.id == identifier), None)
        if current:
            return current
    return rows[0] if rows else None


def remember_diagram(session, template):
    snapshot = {'name': template.name, 'layout': deepcopy(template.layout)}
    config = {'template_id': template.id, 'snapshot': snapshot}
    if session.info.get('source_storage_legacy'):
        selected = session.get(SourceMapping, mapping_key(session, DIAGRAM_KEY))
        snapshots = _diagram_snapshots(selected.config or {}) if selected else {}
        snapshots[str(template.id)] = snapshot
        config['snapshots'] = snapshots
    _save_mapping(session, DIAGRAM_KEY, config)


def forget_diagram(session, template_id):
    selected = session.get(SourceMapping, mapping_key(session, DIAGRAM_KEY))
    if selected is None:
        return
    snapshots = _diagram_snapshots(selected.config or {})
    snapshots.pop(str(template_id), None)
    identifier = selected.config.get('template_id')
    if identifier == template_id:
        identifier = next((int(key) for key in snapshots), None)
    _save_mapping(session, DIAGRAM_KEY, {'template_id': identifier,
        'snapshot': snapshots.get(str(identifier)), 'snapshots': snapshots})


def _diagram_ids(layout):
    identifiers = []
    for node in layout.get('nodes') or []:
        binding = node.get('binding') or {}
        identifier = binding.get('series_id')
        if binding.get('kind') == 'series' and isinstance(identifier, str) and SERIES_ID.fullmatch(identifier):
            identifiers.append(identifier)
    return _ids(list(dict.fromkeys(identifiers)))


def bound_series_ids(session, module):
    """An indicators grant is still a bounded workbench view, never a catalog."""
    if module == 'market':
        return core_config(session)
    if module in {'diagram', 'indicators'}:
        templates = shared_diagrams(session)
        diagram = [identifier for template in templates for identifier in _diagram_ids(template.layout or {})]
        return _ids(list(dict.fromkeys((core_config(session) if module == 'indicators' else []) + diagram)))
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
                'unit': spec.get('unit') or '', 'frequency': spec.get('frequency') or '',
                'date': latest.get('date'), 'value': value,
                'change': value - old if value is not None and old is not None else None,
                'change_pct': (value - old) / abs(old) * 100 if value is not None and old not in {None, 0} else None}
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


def readonly_diagram_layout(layout):
    """Only fields drawn by the canvas cross the restricted-view boundary."""
    from tin.export.diagram_md import ID_TOKEN
    sources = [node for node in layout.get('nodes') or [] if isinstance(node, dict)]
    identifiers, used = {}, set()
    for index, source in enumerate(sources):
        original = str(source.get('id') or '')
        identifier = original if ID_TOKEN.fullmatch(original) and original not in {'__proto__', 'constructor', 'prototype'} else f'view-node-{index}'
        while identifier in used:
            identifier += '-view'
        identifiers[original] = identifier
        used.add(identifier)
    nodes = []
    for source in sources:
        node = {'id': identifiers[str(source.get('id') or '')],
                'label': source.get('label') if isinstance(source.get('label'), str) else ''}
        for key in ('kind', 'layer'):
            if isinstance(source.get(key), str):
                node[key] = source[key]
        if source.get('parent') is not None:
            node['parent'] = identifiers.get(str(source['parent']))
        for key, fallback in [('x', 0), ('y', 0), ('w', 160), ('h', 80)]:
            number = source.get(key, fallback)
            node[key] = number if type(number) in {float, int} and math.isfinite(number) else fallback
        if isinstance(source.get('binding'), dict):
            binding = source['binding']
            node['binding'] = {key: binding[key] for key in ('kind', 'series_id', 'formula_id', 'op')
                               if isinstance(binding.get(key), str)}
            if 'proxy' in binding:
                node['binding']['proxy'] = binding['proxy'] is True
            if isinstance(binding.get('members'), list):
                node['binding']['members'] = [identifiers.get(str(member), 'missing-view-member')
                                               for member in binding['members']]
        if isinstance(source.get('statics'), list):
            node['statics'] = [{key: item.get(key, '') for key in ('label', 'value')
                                if isinstance(item.get(key, ''), (str, int, float, bool))}
                               for item in source['statics'] if isinstance(item, dict)]
        nodes.append(node)
    edges = []
    for source in layout.get('edges') or []:
        if not isinstance(source, dict):
            continue
        start, end = identifiers.get(str(source.get('from'))), identifiers.get(str(source.get('to')))
        if start and end:
            edge = {'from': start, 'to': end}
            if isinstance(source.get('kind'), str):
                edge['kind'] = source['kind']
            edges.append(edge)
    return {'nodes': nodes, 'edges': edges}


def shared_owner_id(scope):
    owner = scope.metadata.get('owner_id')
    return owner if isinstance(owner, str) and owner.strip() and len(owner) <= 128 else None


def shared_owner_storage_available(session, scope):
    """A legacy tenant DB is attributable only with one verified workbench binding.

    Assigned owner records are safe to share under that page's current consent.
    A missing or competing binding must not turn an entire tenant's personal
    history into this workbench's records; unassigned researchers remain excluded
    separately by ``shared_owner_query``.
    """
    if not session.info.get('source_storage_legacy'):
        return True
    identifier = getattr(scope, 'workbench_id', None)
    if not identifier or session.info.get('workbench_id') != identifier:
        return False
    expected = (identifier, scope.knowledge_base_id, scope.knowledge_base_tenant_id)
    bindings = session.execute(select(WorkbenchStorageScope.workbench_id,
        WorkbenchStorageScope.knowledge_base_id, WorkbenchStorageScope.knowledge_base_tenant_id).limit(2)).all()
    return len(bindings) == 1 and tuple(bindings[0]) == expected


def shared_owner_query(scope, statement, model):
    """Only the verified workbench owner's explicitly shared page records."""
    owner = shared_owner_id(scope)
    owner_rows = select(Researcher.id).where(Researcher.weknora_user_id == owner)
    return statement.where(model.researcher_id.in_(owner_rows)) if owner else statement.where(False)


def shared_artifacts(session, scope, module, *, with_truncation=False):
    stored_module = 'briefs' if module == 'reports' else module
    owner = shared_owner_id(scope)
    if not owner:
        return ([], False) if with_truncation else []
    rows = list(session.scalars(select(WorkbenchArtifact).where(
        WorkbenchArtifact.module == stored_module, WorkbenchArtifact.actor == owner)
        .order_by(WorkbenchArtifact.updated_at.desc()).limit(51)))
    items = [{'title': row.title, 'text': row.body, 'status': row.status,
              'date': row.updated_at.date().isoformat()} for row in rows[:50]]
    return (items, len(rows) > 50) if with_truncation else items


def _market_cards(identifiers, values):
    from tin.export.board import Card
    return [Card(key=identifier, series_id=identifier, label=values[identifier]['name'],
                 unit=values[identifier]['unit'], status='ok' if values[identifier]['value'] is not None else 'missing',
                 value=values[identifier]['value'], as_of=values[identifier]['date'],
                 change=values[identifier]['change'], change_pct=values[identifier]['change_pct'],
                 badge='已授权') for identifier in identifiers if identifier in values]


def page_data(scope, module, template_id=None):
    """Render owner-shared page contents without unrelated source snapshots."""
    if module == 'home':
        from tin.web.restricted_home import home_payload
        return home_payload(scope)
    with artifact_session(scope) as session:
        templates = shared_diagrams(session) if module == 'diagram' else []
        template = (next((row for row in templates if row.id == template_id), None)
                    if template_id is not None else default_diagram(session, templates)) if module == 'diagram' else None
        if module == 'diagram' and template_id is not None and template is None:
            raise HTTPException(404, '没有这张已授权的产业图')
        identifiers = _diagram_ids(template.layout or {}) if template else (
            [] if module == 'diagram' else bound_series_ids(session, module))
        output = {'cards': [], 'diagram': None, 'entries': [],
                  'legacy_unassigned': not shared_owner_storage_available(session, scope),
                  'owner_unavailable': shared_owner_id(scope) is None}
        core_ids = list(identifiers)
        include_judgment = (module == 'judgments'
                            or (module == 'market' and 'judgments' in scope.view_modules))
        if include_judgment and not output['legacy_unassigned']:
            from tin.export.board import judgment_record_block
            query = shared_owner_query(scope, select(Judgment).where(Judgment.variety == settings.variety), Judgment)
            row = session.scalar(query.order_by((Judgment.status == '生效').desc(), Judgment.version.desc()).limit(1))
            threshold_ids = [t['series_id'] for t in (row.thresholds if row else [])
                             if isinstance(t, dict) and isinstance(t.get('series_id'), str)
                             and SERIES_ID.fullmatch(t['series_id'])]
            identifiers = _ids(list(dict.fromkeys((core_ids if module == 'market' else []) + threshold_ids)))
            versions = session.scalars(shared_owner_query(scope, select(Judgment).where(
                Judgment.variety == settings.variety), Judgment)
                .order_by(Judgment.version.desc()).limit(51)).all()
            output.update(j=row, block=judgment_record_block(row, datetime.now(SHANGHAI).date()),
                          versions=versions[:50], versions_truncated=len(versions) > 50)
        if module == 'indicators' and output['owner_unavailable']:
            identifiers = []
        values = necessary_values(session, scope, identifiers, module)
        output['cards'] = [values[identifier] for identifier in identifiers if identifier in values]
        output['market_cards'] = _market_cards(core_ids if module == 'market' else identifiers, values)
        if module == 'market':
            from tin.export.board import threshold_radar_values
            output['radar'] = threshold_radar_values(output.get('j'), values)
        if module == 'diagram' and template:
            layout = readonly_diagram_layout(template.layout or {})
            output['diagram'] = {'id': template.id, 'name': template.name,
                                 'layout': layout, 'is_default': bool(template.is_default)}
            output['diagrams'] = [{'id': row.id, 'name': row.name, 'is_default': bool(row.is_default)}
                                   for row in templates]
            output['diagram_values'] = diagram_values(layout, values)
        if module in {'judgments', 'strategies', 'reports'} and not output['legacy_unassigned']:
            output['entries'], output['entries_truncated'] = shared_artifacts(
                session, scope, module, with_truncation=True)
            if module == 'judgments':
                from tin.export.board import threshold_radar_values
                output['radar'] = threshold_radar_values(output['j'], values)
                output['names'] = {identifier: item['name'] for identifier, item in values.items()}
            elif module == 'strategies':
                rows = session.scalars(shared_owner_query(scope, select(Strategy).where(Strategy.variety == settings.variety), Strategy)
                    .order_by(Strategy.created_at.desc()).limit(51)).all()
                output['strategies_truncated'] = len(rows) > 50
                # Templates only render named plan fields; source_context and
                # captured evidence blobs are intentionally never serialized.
                output['strategies'] = [{'id': row.id, 'title': row.title, 'status': row.status,
                    'kind': row.kind, 'mode': row.mode,
                    'plan': {key: deepcopy(row.plan.get(key)) for key in (
                        'thesis', 'alternative_explanation', 'entry_condition', 'invalidation', 'catalyst',
                        'horizon_days', 'review_at', 'as_of', 'legs', 'entry', 'target', 'stop',
                        'market_expectation', 'risk_budget') if key in row.plan}}
                    for row in rows[:50]]
            elif module == 'reports':
                rows = session.scalars(shared_owner_query(scope, select(DailyBrief).where(DailyBrief.variety == settings.variety), DailyBrief)
                    .order_by(DailyBrief.trade_date.desc()).limit(41)).all()
                output['briefs_truncated'] = len(rows) > 40
                rows = rows[:40]
                output['briefs'] = [{key: getattr(row, key) for key in (
                    'trade_date', 'status', 'tone', 'tone_note', 'range_low', 'range_high',
                    'stance', 'summary', 'generated_at')}
                    for row in rows]
                for brief, row in zip(output['briefs'], rows):
                    brief['claims'] = [{key: item[key] for key in ('claim', 'series_id', 'value', 'as_of')
                        if key in item and isinstance(item[key], (str, int, float, bool))}
                        for item in row.claims if isinstance(item, dict)]
                    brief['unverified'] = [{key: item[key] for key in ('claim', 'reason')
                        if isinstance(item.get(key), str)} for item in row.unverified if isinstance(item, dict)]
        return output


def diagram_values(layout, values):
    """Resolve saved structure from bounded points without opening a facts session."""
    from tin.export.diagram import NodeValue, UNBOUND, NO_DATA, OK, STALE, _EXPECTED_DAYS, _aggregate
    today = datetime.now(SHANGHAI).date()
    output = {}
    for node in layout.get('nodes') or []:
        binding = node.get('binding') or {}
        item = NodeValue(node['id'], UNBOUND, node.get('label') or '').dump()
        if binding.get('kind') in {'series', 'derived'}:
            card = values.get(binding.get('series_id')) if binding.get('kind') == 'series' else None
            item.update(state=NO_DATA, series_id=binding.get('series_id') or binding.get('formula_id'),
                        proxy=bool(binding.get('proxy')), note='暂无已授权的观测值')
            if card:
                day = date.fromisoformat(card['date']) if card.get('date') else None
                gap = (today - day).days if day else None
                stale = gap is not None and gap > _EXPECTED_DAYS.get(card['frequency'], 30)
                item.update(state=STALE if stale else OK if card['value'] is not None else NO_DATA,
                    value=card['value'], unit=card['unit'], frequency=card['frequency'], as_of=card['date'],
                    delta=card['change'], mom=card['change_pct'],
                    note=f'已 {gap} 天未更新' if stale else None)
        output[node['id']] = item
    for node in layout.get('nodes') or []:
        binding = node.get('binding') or {}
        if binding.get('kind') == 'agg':
            output[node['id']].update(_aggregate(node, binding.get('op', 'sum'), layout, output))
    return {'nodes': list(output.values()), 'judgment': {},
            'as_of': max((item['as_of'] for item in output.values() if item.get('as_of')), default=None)}


def publish_diagram(scope):
    """Explicitly publish the original page's full layout list in this WB scope."""
    from tin.integration.workbench_scope import require_shared_write, workbench_session
    require_shared_write(scope)
    if scope.restricted:
        raise HTTPException(403, '仅可读取来源的研究台编辑成员能发布产业图')
    with workbench_session(scope) as session:
        from tin.export.diagram import list_templates
        # Use exactly the normal page's verified-source query. In legacy mode
        # this is an explicit editor publication, never a visitor-side fallback
        # to a tenant-wide list just because another WB has a diagram grant.
        layouts = list_templates(session, settings.variety)
        if not layouts:
            return {'published': False, 'template_id': None}
        # A cold projection may take time. Do not commit a new shared snapshot
        # using edit/source rights that were revoked during that preparation.
        try:
            response = httpx.request('GET', settings.weknora_api_url.rstrip('/')
                + '/api/v1/workspaces/research/' + quote(scope.workbench_id, safe=''),
                headers={'Authorization': scope.authorization, 'X-Tenant-ID': str(scope.identity.tenant_id)},
                trust_env=False, follow_redirects=False, timeout=15)
            if response.status_code in {401, 403, 404}:
                raise HTTPException(response.status_code, '产业图发布权限已改变，请重新打开研究台')
            response.raise_for_status()
            document = response.json()
            current = document.get('data') if isinstance(document, dict) else None
            if (document.get('success') is not True or not isinstance(current, dict)
                    or current.get('id') != scope.workbench_id
                    or type(current.get('scope_tenant_id')) is not int
                    or current['scope_tenant_id'] != scope.identity.tenant_id):
                raise ValueError('Invalid current workbench')
            if (current.get('knowledge_base_id') != scope.knowledge_base_id
                    or current.get('workspace_mode') != scope.metadata.get('workspace_mode')):
                raise HTTPException(409, '研究台来源已改变，未发布旧产业图')
            if not shared_owner_id(scope) or current.get('owner_id') != shared_owner_id(scope):
                raise HTTPException(409, '研究台主人已改变或尚未核验，未发布旧产业图')
            if current.get('can_edit') is not True or current.get('can_read_source') is not True:
                raise HTTPException(403, '产业图发布需要当前来源读取和研究台编辑权限')
        except (httpx.HTTPError, ValueError, TypeError, AttributeError):
            raise HTTPException(503, '暂时无法重新核验产业图发布权限，未保存快照') from None
        identifiers = list(dict.fromkeys(identifier for layout in layouts
            for identifier in _diagram_ids(layout['layout'] or {})))
        remember_origins(session, identifiers)
        template = layouts[0]
        snapshot = {'name': template['name'], 'layout': deepcopy(template['layout'])}
        config = {'template_id': template['id'], 'snapshot': snapshot}
        if session.info.get('source_storage_legacy'):
            config['snapshots'] = {str(layout['id']): {'name': layout['name'],
                'layout': deepcopy(layout['layout'])} for layout in layouts}
        _save_mapping(session, DIAGRAM_KEY, config)
        session.commit()
        return {'published': True, 'template_id': template['id']}


async def restricted_response(request, scope, route_path):
    if request.method not in {'GET', 'HEAD'} or route_path not in PAGES:
        raise HTTPException(403, '受限视角不开放指标目录、任意取数、原文、导出和修改接口')
    allowed = {'_research_tenant', '_research_user'} | ({'template'} if route_path == '/sn/diagram' else set())
    if set(request.query_params) - allowed:
        raise HTTPException(422, '受限视角由服务端绑定确定指标和最新时点，不接受自选查询范围')
    template_id = None
    if 'template' in request.query_params:
        values = request.query_params.getlist('template')
        value = values[0]
        if len(values) != 1 or not value.isascii() or not value.isdecimal() or len(value) > 19 or not 0 < int(value) <= 2**63 - 1:
            raise HTTPException(422, '请选择一张已授权的产业图')
        template_id = int(value)
    module, title = PAGES[route_path]
    authorized = scope.source_available and module in scope.view_modules
    payload = await run_in_threadpool(page_data, scope, module, template_id) if authorized else {'cards': [], 'entries': [], 'diagram': None}
    from tin.web.app import templates
    context = {'nav': {'market': 'variety', 'judgments': 'judgment'}.get(module, module),
        'restricted_view': True, 'restricted_module': module, 'restricted_title': title,
        'restricted_authorized': authorized, 'restricted_pages': PAGES, 'restricted_data': payload,
        'd': None, 'ticker': [], 'manual_indicators': [], 'err': None}
    if authorized and module == 'home':
        return templates.TemplateResponse(request, 'readonly_home.html', context)
    if authorized and module == 'diagram' and payload.get('diagram'):
        context.update(template=payload['diagram'], templates_all=payload['diagrams'],
                       restricted_diagram_values=payload['diagram_values'])
        return templates.TemplateResponse(request, 'diagram.html', context)
    if authorized and module == 'indicators':
        rows = [{'series_id': card.series_id, 'name': card.label, 'unit': card.unit,
                 'frequency': values.get('frequency', ''), 'source': '已授权绑定',
                 'category': '工作台绑定指标', 'macro': False, 'registered': False,
                 'caliber': {}, 'status': '可用', 'fetch_mode': 'readonly', 'vendor_code': None,
                 'is_proxy': False, 'value': card.value, 'as_of': card.as_of,
                 'change': card.change, 'change_pct': card.change_pct}
                for card, values in zip(payload.get('market_cards', []), payload.get('cards', []))]
        context.update(rows=rows, groups=[{'name': '工作台绑定指标', 'rows': rows}],
                       restricted_cards=rows, dimensions=[], checks=[], sources={'rows': [], 'counts': {}})
        return templates.TemplateResponse(request, 'indicators.html', context)
    if authorized and module == 'market':
        dates = [card.as_of for card in payload.get('market_cards', []) if card.as_of]
        context.update(cards=payload.get('market_cards', []), market_catalog=None, judgment=payload.get('block'),
            radar=payload.get('radar', []), gaps=[], macro=[], liquidity_min=settings.liquidity_min_volume,
            curve={'rows': [], 'shape': None}, d=max(dates, default=None))
        return templates.TemplateResponse(request, 'variety.html', context)
    if authorized and not payload.get('legacy_unassigned') and not payload.get('owner_unavailable'):
        if module == 'judgments':
            context.update(j=payload['j'], block=payload['block'], versions=payload['versions'],
                           radar=payload['radar'], names=payload['names'], report=None, logic_input='')
            return templates.TemplateResponse(request, 'judgment.html', context)
        if module == 'strategies':
            from tin.web.strategy import STATUS_LABELS, MODE_LABELS, KIND_LABELS
            rows = payload.get('strategies', [])
            context.update(strategies=rows, selected_status='all', status_labels=STATUS_LABELS,
                mode_labels=MODE_LABELS, kind_labels=KIND_LABELS,
                summary={'active': sum(row['status'] in {'watching', 'open'} for row in rows),
                         'drafts': sum(row['status'] == 'draft' for row in rows)})
            return templates.TemplateResponse(request, 'strategies.html', context)
        if module == 'reports':
            return templates.TemplateResponse(request, 'reports.html', context)
    return templates.TemplateResponse(request, 'restricted.html', context)
