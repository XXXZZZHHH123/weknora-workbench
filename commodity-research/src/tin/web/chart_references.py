"""Explicit chart pins for personal research; shared charts keep their own ACL."""
from copy import deepcopy
from datetime import date, timedelta
import math

from fastapi import APIRouter, HTTPException, Query
from pydantic import BaseModel, ConfigDict, Field
from sqlalchemy import update
from sqlalchemy.exc import OperationalError

from tin.integration.page_facts import catalog_page
from tin.integration.workbench_scope import artifact_session, current_workbench
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision
from tin.web import charts
from tin.web.chart_data import cutoff, definition_series_ids

router = APIRouter(prefix='/api/sn/chart-references')
RESTRICTED_NOTICE = '图表引用暂不对受限视角开放'


def require_scope(scope=None):
    scope = scope or current_workbench.get()
    if scope is not None and scope.restricted:
        raise HTTPException(403, RESTRICTED_NOTICE)
    charts.require_scope()
    if scope is None or not {'judgments', 'strategies'}.intersection(scope.view_modules):
        raise HTTPException(403, '当前授权不开放个人研究图表引用')
    return scope


def _pin(value):
    if hasattr(value, 'model_dump'):
        value = value.model_dump(mode='json', exclude_none=True)
    if (not isinstance(value, dict) or value.get('kind', 'chart') != 'chart'
            or not isinstance(value.get('id'), str) or not 1 <= len(value['id']) <= 128
            or type(value.get('revision')) is not int or not 1 <= value['revision'] <= 2**31 - 1):
        raise HTTPException(422, '请明确选择图表及其修订号')
    return value


def _metadata(scope, identifiers):
    fields, version = {}, None
    identifiers = list(dict.fromkeys(identifiers))
    for offset in range(0, max(1, len(identifiers)), 200):
        batch = identifiers[offset:offset + 200]
        page = catalog_page(scope, series_ids=batch, limit=200, version=version, apply_dedup=False)
        if (not isinstance(page.get('version'), str) or not page['version']
                or version is not None and page['version'] != version
                or page.get('next_offset') is not None or page.get('truncated')
                or page.get('total') != len(page.get('series', []))):
            raise HTTPException(409, '图表引用来源版本不完整或已变化，请重新选择')
        version = page['version']
        for field in page['series']:
            sid = field.get('series_id')
            if sid not in batch or sid in fields:
                raise HTTPException(503, '图表引用指标目录核验失败')
            if field.get('source_version') is not None and (not isinstance(field['source_version'], str)
                    or not 1 <= len(field['source_version']) <= 256):
                raise HTTPException(503, '图表指标来源版本无效')
            fields[sid] = field
    return fields, version


def definition_summary(definition):
    labels = '、'.join(curve.label for curve in definition.curves)
    mode = '周期对比' if definition.time.mode == 'cycle' else '走势'
    return (mode + ' · ' + labels)[:600]


def fence_chart_reference(session, scope, reference):
    require_scope(scope)
    value = _pin(reference)
    # Workbench membership is proved before the CAS; authorship does not limit
    # access to shared charts. This lock serializes publication with delete/edit.
    row = charts.load_artifact(session, value['id'])
    if row.revision != value['revision']:
        raise HTTPException(409, '图表已有新修订，请重新选择后保存')
    try:
        changed = session.execute(update(WorkbenchArtifact).where(
            WorkbenchArtifact.id == row.id, WorkbenchArtifact.module == 'charts',
            WorkbenchArtifact.status != 'deleted', WorkbenchArtifact.revision == value['revision'])
            .values(revision=WorkbenchArtifact.revision), execution_options={'synchronize_session': False})
    except OperationalError:
        raise HTTPException(409, '引用图表正在更新，请刷新后保存') from None
    if changed.rowcount != 1:
        raise HTTPException(409, '引用图表已修订或删除，请重新选择')
    return row


def freeze_chart_references(session, scope, values, as_of):
    """Discard client snapshots, then rebuild pins inside the owner's write txn."""
    if not isinstance(values, list) or len(values) > 32:
        raise HTTPException(422, '图表引用最多32项')
    if not values:
        return []
    require_scope(scope)
    if (session.info.get('workbench_id') != scope.workbench_id
            or str(session.info.get('weknora_user_id') or '') != scope.identity.user_id):
        raise HTTPException(403, '图表引用的研究台或账号不匹配')
    day = cutoff(as_of.isoformat() if hasattr(as_of, 'isoformat') else as_of)
    rows, identifiers = [], []
    for raw in values:
        value = _pin(raw)
        row = fence_chart_reference(session, scope, value)
        definition = charts.decode_definition(row)
        rows.append((value, row, definition))
        identifiers.extend(definition_series_ids(definition))
    fields, version = _metadata(scope, identifiers)
    if any(sid not in fields for sid in identifiers):
        raise HTTPException(422, '引用图表包含已失效或未授权指标，请先修订图表')
    return [{'kind': 'chart', 'id': row.id, 'revision': row.revision, 'as_of': day.isoformat(),
             'source_version': version,
             'source_versions': {sid: fields[sid].get('source_version') or version
                                 for sid in definition_series_ids(definition)},
             'title': definition.title, 'definition_summary': definition_summary(definition),
             'definition': definition.model_dump(mode='json'),
             **({'target': deepcopy(value['target'])} if 'target' in value else {})}
            for value, row, definition in rows]


def _dedup_notice(scope, identifiers):
    from tin.integration.indicator_dedup import confirmed_replacements
    replacements = confirmed_replacements(scope, identifiers)
    return ({'dedup_replacements': replacements, 'dedup_notice': '可改为引用 default 库指标'}
            if replacements else {})


def moving_observations(scope, identifiers, day, limit):
    """Page backward over real dates; invalid windows allow at most 121 rows."""
    from tin.integration.page_facts import bounded_facts
    rows = {sid: [] for sid in identifiers}
    pending = {sid: day for sid in identifiers}
    versions = {}
    while pending:
        groups = {}
        for sid, boundary in pending.items():
            size = min(121, limit - len(rows[sid]))
            groups.setdefault((boundary, size), []).append(sid)
        following = {}
        for (boundary, size), ids in groups.items():
            facts = bounded_facts(scope, ids, 'judgments', day=boundary.isoformat(),
                                  points=size, include_invalid=True)
            for binding, version in facts.get('source_versions', {}).items():
                if binding in versions and versions[binding] != version:
                    raise HTTPException(409, '均线来源在分页期间变化，请刷新')
                versions[binding] = version
            for sid in ids:
                page = sorted([point for point in facts['rows'] if point.get('series_id') == sid],
                              key=lambda point: point['date'])
                # bounded_facts validates IDs, unique dates, statuses and cutoff.
                # Invalid observations remain in every moving window.
                rows[sid].extend(page)
                if len(page) == size and len(rows[sid]) < limit:
                    earliest = date.fromisoformat(page[0]['date'])
                    if earliest > date.min:
                        following[sid] = earliest - timedelta(days=1)
        pending = following
    return {sid: sorted(points, key=lambda point: point['date']) for sid, points in rows.items()}


def overlay_plan(definition, rules):
    """Use typed rule definitions and exact direct curve IDs, never their labels."""
    from tin.schemas.judgment import EXPRESSION
    direct = {curve.series_id for curve in definition.curves if curve.series_id}
    overlays, moving = [], {}
    for rule in rules or []:
        if not isinstance(rule, dict) or rule.get('series_id') not in direct:
            continue
        base = {'series_id': rule['series_id'], 'rule_id': rule.get('id'),
                'name': rule.get('name') or rule.get('desc') or '规则阈值', 'operator': rule.get('operator')}
        value = rule.get('value')
        if isinstance(value, dict) and value.get('kind') == 'sma':
            window = value.get('window')
            if type(window) is int and 2 <= window <= 120:
                moving.setdefault(rule['series_id'], []).append((base, window))
            continue
        if 'expression' in rule:
            match = EXPRESSION.fullmatch(rule['expression'].strip()) if isinstance(rule['expression'], str) else None
            # A change condition is relative to each previous real observation.
            # It is rendered as a dated boundary rather than an absolute level.
            if not match:
                continue
            value = float(match.group(3))
            base['operator'] = match.group(2)
            if match.group(1) == 'change':
                moving.setdefault(rule['series_id'], []).append((base, ('change', value)))
                continue
        for number in value if isinstance(value, list) else [value]:
            if type(number) in {int, float} and math.isfinite(number):
                overlays.append({**base, 'kind': 'fixed', 'value': number})
    return overlays, moving


def threshold_overlays(scope, definition, rules, day, *, point_limit=None, plan=None):
    from tin.compute.moving_average import sma_values
    overlays, moving = plan if plan is not None else overlay_plan(definition, rules)
    if moving:
        ids = list(moving)
        limit = min(point_limit or 2000, 2000, 20000 // len(ids))
        if definition.time.mode == 'recent':
            window = max(window if type(window) is int else 2 for items in moving.values() for _, window in items)
            limit = min(limit, definition.time.points + window - 1)
        observations = moving_observations(scope, ids, day, limit)
        for sid, items in moving.items():
            points = sorted([{'date': p['date'],
                **{key: p[key] for key in ('unit', 'caliber_snapshot') if key in p},
                'value': p.get('value') if p.get('value_status') in {'valid', 'ok', '正常'} else None}
                for p in observations[sid]], key=lambda p: p['date'])
            for base, window in items:
                if isinstance(window, tuple):
                    values = [None] + [p['value'] + window[1]
                        if type(p.get('value')) in {int, float} else None for p in points[:-1]]
                else:
                    values = sma_values(points, window)
                overlays.append({**base, 'kind': 'sma' if type(window) is int else 'dynamic', 'window': window if type(window) is int else None,
                    'points': [{'date': p['date'], 'value': value if value is not None and math.isfinite(value) else None}
                               for p, value in zip(points, values)]})
    return overlays


def render_chart_reference(scope, reference, *, current=False, rules=None):
    scope = require_scope(scope)
    value = _pin(reference)
    if not current and not isinstance(value.get('as_of'), str):
        raise HTTPException(422, '历史图表引用需要保存时的明确截止日期')
    with artifact_session(scope) as session:
        row = charts.load_artifact(session, value['id'], deleted=True)
        chosen = session.get(WorkbenchArtifactRevision, (row.id, value['revision']))
        if chosen is None or chosen.status == 'deleted':
            raise HTTPException(404, '所引用的图表修订不存在')
        saved = charts.decode_definition(chosen)
        status = 'deleted' if row.status == 'deleted' else 'updated' if row.revision != value['revision'] else 'current'
        # Current data changes the cutoff, while the research pin's definition
        # remains stable even after a chart edit or deletion.
        definition = saved
    day = cutoff(None if current else value['as_of'])
    identifiers = definition_series_ids(definition)
    fields, version = _metadata(scope, identifiers)
    plan = overlay_plan(definition, rules)
    # The graph operands and complete moving windows share one request budget.
    # Missing/repeated curves retain the chart library's definition-based cap.
    limit = min(2000, 20000 // (max(len(definition.curves), len(identifiers)) + len(plan[1])))
    payload = charts.definition_data(scope, definition, day, chart_id=value['id'], revision=value['revision'],
                                     fields=fields, point_limit=limit)
    actual_day = cutoff(payload.get('as_of', day.isoformat()))
    overlays = threshold_overlays(scope, definition, rules, actual_day, point_limit=limit, plan=plan)
    _, after = _metadata(scope, identifiers)
    if version != after:
        raise HTTPException(409, '图表来源在读取期间已变化，请刷新')
    notice = '图表已删除' if status == 'deleted' else '已有新修订' if status == 'updated' else ''
    version_changed = bool(value.get('source_version') and value['source_version'] != version)
    data_notice = ('来源数据版本已变化，按原截止日重算；当前来源不提供旧事实版本回放'
                   if version_changed and not current else '')
    return {'reference': {**value, 'title': saved.title, 'definition_summary': definition_summary(saved),
                          'definition': saved.model_dump(mode='json')},
            'title': saved.title, 'status': status, 'notice': notice, 'data_notice': data_notice,
            'source_version': version, 'source_version_changed': version_changed,
            'as_of': actual_day.isoformat(), 'research_as_of': value.get('as_of'), 'current': current,
            'definition': definition.model_dump(mode='json'),
            'payload': payload, 'overlays': overlays,
            **_dedup_notice(scope, identifiers)}


@router.get('/options')
def chart_options(q: str = Query('', max_length=200), group: str | None = Query(None, max_length=80),
                  favorites: bool = False, series_id: str | None = Query(None, max_length=128),
                  offset: int = Query(0, ge=0), limit: int = Query(30, ge=1, le=100)):
    scope = require_scope()
    from tin.web.favorites import owned_query, stored_items
    with artifact_session(scope) as session:
        rows = list(session.scalars(charts.artifact_query().order_by(WorkbenchArtifact.updated_at.desc(), WorkbenchArtifact.id)))
        definitions = [(row, charts.decode_definition(row)) for row in rows]
        groups = sorted({definition.group for _, definition in definitions if definition.group})
        selected = {item.id for item in stored_items(session.scalar(owned_query(scope))) if item.kind == 'chart'} if favorites else None
        items = [{'id': row.id, 'revision': row.revision, 'title': definition.title, 'group': definition.group,
                  'definition_summary': definition_summary(definition)}
                 for row, definition in definitions
                 if (not q or q.casefold() in definition.title.casefold())
                 and (group is None or definition.group == group)
                 and (selected is None or row.id in selected)
                 and (series_id is None or series_id in definition_series_ids(definition))]
        total = len(items)
        return {'items': items[offset:offset + limit], 'groups': groups, 'total': total,
                'next_offset': offset + limit if offset + limit < total else None}


class RenderInput(BaseModel):
    model_config = ConfigDict(extra='forbid')
    reference: dict
    current: bool = Field(default=False, strict=True)
    rules: list[dict] = Field(default_factory=list, max_length=64)


@router.post('/render')
def render_reference(body: RenderInput):
    return render_chart_reference(require_scope(), body.reference, current=body.current, rules=body.rules)
