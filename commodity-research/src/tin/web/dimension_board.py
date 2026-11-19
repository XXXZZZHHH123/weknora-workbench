"""Ordered shared research dimensions and explicit, revision-locked bindings."""
from copy import deepcopy
from datetime import date, datetime, timezone
import re
from typing import Annotated
from uuid import uuid4

from fastapi import APIRouter, HTTPException, Query, Request
from pydantic import BaseModel, ConfigDict, Field
from sqlalchemy import select, update
from sqlalchemy.exc import IntegrityError, OperationalError
from starlette.concurrency import run_in_threadpool

from tin.integration.page_facts import source_error_details
from tin.integration.workbench_scope import artifact_session, current_workbench, require_shared_write
from tin.models import Judgment, SourceMapping
from tin.web.research_dimensions import (CATEGORIES, DIMENSIONS_KEY, default_dimensions, read_dimensions,
                                         validate_dimension_list)
from tin.web.research_framework import dimension_label
from tin.web.research_display import indicator_name
from tin.web.restricted_views import mapping_key

router = APIRouter(prefix='/api/sn/dimensions')
SECTION_ID = re.compile(r'[a-z][a-z0-9_-]{1,39}')


class DimensionConfig(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    revision: int = Field(ge=0)
    dimensions: list[dict] = Field(max_length=12)
    confirm_delete: list[str] = Field(default_factory=list, max_length=12)


class DimensionTemplateDraft(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    revision: int = Field(ge=0)
    dimensions: list[dict] | None = Field(default=None, max_length=24)


def require_scope(*, write=False):
    scope = current_workbench.get()
    if scope is None or scope.scope_version != 2:
        raise HTTPException(409, '研究维度需要来源版本2，请重新打开研究台')
    if scope.restricted:
        raise HTTPException(403, '受限视角仅可查看服务端绑定的研究维度，不开放任意取数和修改')
    if 'indicators' not in scope.view_modules:
        raise HTTPException(403, '当前授权未开放指标页面')
    if write:
        require_shared_write(scope)
    return scope


def revision(session):
    row = session.get(SourceMapping, mapping_key(session, DIMENSIONS_KEY))
    return (row.version or 1) if row else 0


def validate_config(values, previous, *, draft=False, admitted_ids=()):
    """Keep persisted/reserved ids; all other new ids are server authored.

    Template drafts may temporarily retain extra dimensions and sections for
    review. Saving always uses the original shared-contract limits.
    """
    values = deepcopy(values)
    known = {item['id'] for item in previous}
    known.update(admitted_ids)
    defaults = {item['id']: item['category'] for item in default_dimensions()}
    seen_client, seen_items, section_ids = set(), set(), set()
    total = 0
    for dim in values:
        if not isinstance(dim, dict):
            raise HTTPException(422, '维度必须为对象')
        identifier = dim.get('id')
        if identifier is not None and (not isinstance(identifier, str) or identifier in seen_client):
            raise HTTPException(422, '维度编号无效或重复')
        if identifier:
            seen_client.add(identifier)
        if identifier not in known and defaults.get(identifier) != dim.get('category'):
            dim['id'] = 'd-' + uuid4().hex[:8]
        dim['label'] = dim.get('label', '').strip() if isinstance(dim.get('label'), str) else None
        sections = dim.get('sections', [])
        section_limit = 16 if draft else 8
        if not isinstance(sections, list) or len(sections) > section_limit:
            raise HTTPException(422, f'每个维度最多{section_limit}个环节')
        clean_sections = []
        for section in sections:
            if (not isinstance(section, dict) or not isinstance(section.get('label'), str)
                    or not 1 <= len(section['label'].strip()) <= 40):
                raise HTTPException(422, '环节需要名称（1–40字）')
            sid = section.get('id') or 's-' + uuid4().hex[:8]
            if not isinstance(sid, str) or not SECTION_ID.fullmatch(sid) or sid in section_ids:
                raise HTTPException(422, '环节编号无效或重复')
            section_ids.add(sid)
            entries = section.get('items', [])
            if not isinstance(entries, list) or len(entries) > 12:
                raise HTTPException(422, '每个环节最多12条')
            clean = []
            for item in entries:
                if (not isinstance(item, dict) or item.get('kind') not in {'indicator', 'chart'}
                        or not isinstance(item.get('id'), str) or not 1 <= len(item['id']) <= 128):
                    raise HTTPException(422, '请选择真实指标或图表')
                from tin.integration.page_facts import SERIES, LEGACY_ID
                pattern = SERIES if item['kind'] == 'indicator' else LEGACY_ID
                if not pattern.fullmatch(item['id']):
                    raise HTTPException(422, '条目编号格式无效')
                identity = (item['kind'], item['id'])
                if identity in seen_items:
                    raise HTTPException(422, '同一条目不能重复绑定')
                seen_items.add(identity)
                entry = {'kind': item['kind'], 'id': item['id']}
                if item['kind'] == 'chart':
                    if type(item.get('revision')) is not int or not 1 <= item['revision'] <= 2**31 - 1:
                        raise HTTPException(422, '图表需要明确的修订号')
                    entry['revision'] = item['revision']
                clean.append(entry)
            total += len(clean)
            clean_sections.append({'id': sid, 'label': section['label'].strip(), 'items': clean})
        dim['sections'] = clean_sections
    if total > 96:
        raise HTTPException(422, '研究维度总计最多96条')
    if draft:
        if not 1 <= len(values) <= 24:
            raise HTTPException(422, '模板草稿需要1–24个维度')
        labels = set()
        for dim in values:
            validate_dimension_list([dim])
            if dim['label'] in labels:
                raise HTTPException(422, '研究维度的名称不能重复')
            labels.add(dim['label'])
    else:
        validate_dimension_list(values)
    return [{key: dim.get(key, '') for key in ('id', 'label', 'category', 'description', 'sections')} for dim in values]


def evidence_counts(session, dimensions, scope=None):
    """Read optional tags directly, tolerating old evidence without dimension."""
    counts = {dim['id']: {'support': 0, 'oppose': 0, 'total': 0} for dim in dimensions}
    extra = {key: {'support': 0, 'oppose': 0, 'total': 0} for key in ('unassigned', 'deleted')}
    if scope is not None and 'judgments' not in scope.view_modules:
        return counts, extra
    from tin.ownership import scope_query
    query = select(Judgment).where(Judgment.status == '生效')
    if scope is not None and scope.restricted:
        from tin.web.restricted_views import shared_owner_query, shared_owner_storage_available
        if not shared_owner_storage_available(session, scope):
            return counts, extra
        query = shared_owner_query(scope, query, Judgment)
    else:
        query = scope_query(session, query, Judgment)
    for row in session.scalars(query):
        if scope is not None and not scope.restricted:
            from tin.web.research_references import record_in_workbench
            if not record_in_workbench(session, scope, 'judgment', str(row.id), row):
                continue
        contradiction = row.contradiction if isinstance(row.contradiction, dict) else {}
        for side, label in (('bull', 'support'), ('bear', 'oppose')):
            group = contradiction.get(side) or {}
            for item in group.get('evidence', []) if isinstance(group, dict) else []:
                if not isinstance(item, dict):
                    continue
                tag = item.get('dimension')
                target = counts.get(tag) if isinstance(tag, str) else None
                if target is None:
                    target = extra['deleted' if tag else 'unassigned']
                target[label] += 1
                target['total'] += 1
    return counts, extra


def _items(dimensions):
    return [item for dim in dimensions for section in dim.get('sections', []) for item in section['items']]


def _threshold_condition(rule):
    from tin.compute.rule_values import decimal_value
    from tin.schemas.judgment import MovingAverageTarget
    value = rule.value
    if isinstance(value, MovingAverageTarget):
        operator = {'crosses_below': '下穿', 'crosses_above': '上穿'}.get(rule.operator, rule.operator)
        return f'{operator}MA{value.window}'
    if isinstance(value, list):
        return '–'.join(format(decimal_value(endpoint).normalize(), 'f') for endpoint in value)
    return rule.operator + format(decimal_value(value).normalize(), 'f')


def _judgment_references(session, scope, identifiers, judgment_id=None):
    """Describe links from the saved payload, without reading observations."""
    if not identifiers or 'judgments' not in scope.view_modules:
        return None, {}
    from tin.integration.research_profile import research_variety
    from tin.judgments.service import current, to_payload
    from tin.schemas.judgment import EXPRESSION, binding_ids
    from tin.web.judgment_dimension_view import pending_note_entries
    from tin.web.judgment_panels import selected_judgment
    from tin.web.restricted_views import shared_judgment

    if scope.restricted:
        row = shared_judgment(session, scope)
    elif judgment_id is not None:
        row = selected_judgment(session, judgment_id, scope=scope)
    else:
        row = current(session, research_variety(session, scope=scope), scope=scope)
    if row is None:
        return None, {}
    try:
        payload = to_payload(row)
    except (TypeError, ValueError):
        # Legacy records with incomplete logic can still supply dimension counts.
        return None, {}
    references = {}

    def append(identifier, anchor, label):
        references.setdefault(identifier, []).append({'anchor': anchor, 'label': label})

    def bind(item, anchor, label):
        for identifier in binding_ids(item):
            if identifier in identifiers:
                append(identifier, anchor, label)

    for side, label in (('bull', '多方依据'), ('bear', '空方依据')):
        for index, item in enumerate(getattr(payload.contradiction, side).evidence):
            bind(item, f'judgment-evidence-{side}{index}', label)
    for rule in payload.thresholds:
        bind(rule, f'judgment-threshold-{rule.id}', f'阈值 {_threshold_condition(rule)}')
    for rule in payload.falsifiers:
        subject, operator, value = EXPRESSION.fullmatch(rule.expression).groups()
        condition = ('较上期变化' if subject == 'change' else '') + operator + value
        if rule.consecutive_periods > 1:
            condition += f'·连续{rule.consecutive_periods}期'
        bind(rule, f'judgment-falsifier-{rule.id}', f'证伪 {condition}')
    notes = {}
    for index, note, _, target in pending_note_entries(payload):
        kind, key = target or ('note', index)
        anchor = f'judgment-{kind}-{key}'
        for identifier in binding_ids(note):
            if identifier in identifiers:
                pair = identifier, anchor
                notes[pair] = notes.get(pair, 0) + 1
    for (identifier, anchor), count in notes.items():
        append(identifier, anchor, f'待核对 {count}')
    return row.id, references


def describe_config(session, scope, dimensions=None):
    from tin.web.chart_data import catalog_fields, chart_read_scope, definition_series_ids
    from tin.web.dimension_data import pinned_definition
    dimensions = deepcopy(dimensions if dimensions is not None else read_dimensions(session))
    entries = _items(dimensions)
    identifiers = [item['id'] for item in entries if item['kind'] == 'indicator']
    charts = {}
    for item in entries:
        if item['kind'] == 'chart':
            try:
                definition = pinned_definition(session, item)
                charts[item['id']] = definition
                identifiers.extend(definition_series_ids(definition))
            except HTTPException as error:
                charts[item['id']] = error.detail
    with chart_read_scope():
        fields = catalog_fields(scope, identifiers) if identifiers else {}
    counts, extra = evidence_counts(session, dimensions, scope)
    for dim in dimensions:
        dim.setdefault('sections', [])
        dim['references'] = counts[dim['id']]
        for section in dim['sections']:
            for item in section['items']:
                if item['kind'] == 'indicator':
                    field = fields.get(item['id'])
                    item.update(name=indicator_name(item['id'], {item['id']: field.get('name') or field.get('metric')}) if field else '已失效指标',
                        unit=field.get('unit') or '' if field else '', frequency=field.get('frequency') or '' if field else '',
                        source=field.get('source_file') or field.get('source') or '' if field else '',
                        sheet=field.get('sheet') or '' if field else '',
                        status='ok' if field else 'missing', reason='' if field else '指标已不在当前授权来源目录')
                else:
                    definition = charts.get(item['id'])
                    valid = not isinstance(definition, str)
                    missing = valid and any(sid not in fields for sid in definition_series_ids(definition))
                    sources = [{'id': sid, 'name': indicator_name(sid, {sid: fields[sid].get('name') or fields[sid].get('metric')}),
                                'unit': fields[sid].get('unit') or '', 'frequency': fields[sid].get('frequency') or '',
                                'source': fields[sid].get('source_file') or fields[sid].get('source') or '',
                                'sheet': fields[sid].get('sheet') or ''}
                               for sid in definition_series_ids(definition) if sid in fields] if valid else []
                    item.update(name=definition.title if valid else '已失效图表',
                        unit=' / '.join(dict.fromkeys(source['unit'] for source in sources if source['unit'])),
                        frequency=' / '.join(dict.fromkeys(source['frequency'] for source in sources if source['frequency'])),
                        source=' / '.join(dict.fromkeys(source['source'] for source in sources if source['source'])) or '图表库',
                        sources=sources, status='ok' if valid and not missing else 'missing',
                        reason=('图表曲线指标已不在当前授权来源目录' if missing else '' if valid else str(definition)))
    return {'dimensions': dimensions, 'revision': revision(session), 'can_write': bool(scope.can_edit and not scope.restricted),
            'categories': [{'id': value, 'label': dimension_label(value) if value != '其他' else value} for value in CATEGORIES],
            'evidence': extra, 'evidence_available': 'judgments' in scope.view_modules}


def save_config(session, scope, body, *, admitted_ids=()):
    previous = read_dimensions(session)
    expected = revision(session)
    if expected != body.revision:
        raise HTTPException(409, {'reason': 'revision_conflict', 'message': '配置已由其他研究员修改，请刷新后重试', 'revision': expected})
    dimensions = validate_config(body.dimensions, previous, admitted_ids=admitted_ids)
    removed = {dim['id'] for dim in previous} - {dim['id'] for dim in dimensions}
    counts, _ = evidence_counts(session, previous, scope)
    referenced = {key: counts[key]['total'] for key in removed if counts[key]['total'] and key not in body.confirm_delete}
    if referenced:
        raise HTTPException(409, {'reason': 'dimension_referenced', 'message': '删除后研判依据将显示为已删除维度，请显式确认', 'references': referenced})
    old_entries = {(item['kind'], item['id'], item.get('revision')) for item in _items(previous)}
    description = describe_config(session, scope, dimensions)
    for item in _items(description['dimensions']):
        if item['status'] != 'ok' and (item['kind'], item['id'], item.get('revision')) not in old_entries:
            raise HTTPException(422, item['reason'])
    from tin.web.chart_data import catalog_fields, chart_read_scope
    from tin.web.restricted_views import remember_origins
    ids = [item['id'] for item in _items(dimensions) if item['kind'] == 'indicator']
    with chart_read_scope():
        session.info['knowledge_facts_catalog'] = catalog_fields(scope, ids) if ids else {}
    remember_origins(session, ids)
    key = mapping_key(session, DIMENSIONS_KEY)
    values = {'config': {'dimensions': dimensions}, 'confirmed_by': scope.identity.user_id,
              'confirmed_at': datetime.now(timezone.utc), 'version': expected + 1}
    try:
        if expected == 0:
            session.add(SourceMapping(id=key, **values))
        else:
            changed = session.execute(update(SourceMapping).where(SourceMapping.id == key, SourceMapping.version == expected)
                                      .values(**values), execution_options={'synchronize_session': False})
            if changed.rowcount != 1:
                raise HTTPException(409, '配置已改变，请刷新后重试')
        session.commit()
    except (IntegrityError, OperationalError):
        session.rollback()
        raise HTTPException(409, '配置正在更新，请刷新后重试') from None
    session.expire_all()
    description['revision'] = expected + 1
    return description


@router.get('')
def get_config():
    scope = require_scope()
    with source_error_details(), artifact_session(scope) as session:
        return describe_config(session, scope)


@router.put('')
def put_config(body: DimensionConfig):
    scope = require_scope(write=True)
    with artifact_session(scope) as session:
        return save_config(session, scope, body)


@router.put('/from-setup/{job_id}')
async def put_setup_config(job_id: str, body: DimensionConfig):
    from tin.web.workbench_setup_jobs import job_result
    scope = require_scope(write=True)
    result = await job_result(scope, job_id)
    admitted_ids = {dimension['id'] for item in result['items']
                    if item['part'] == 'dimensions' and item['state'] == 'succeeded'
                    for dimension in item['data'].get('dimensions', [])}
    if not admitted_ids:
        raise HTTPException(409, '此任务没有可核对的维度草稿')

    def save():
        with artifact_session(scope) as session:
            return save_config(session, scope, body, admitted_ids=admitted_ids)

    return await run_in_threadpool(save)


@router.get('/templates')
def get_templates():
    from tin.web.dimension_templates import template_list
    require_scope()
    return {'templates': template_list()}


@router.post('/templates/{template_id}/draft')
def template_draft(template_id: str, body: DimensionTemplateDraft):
    from tin.web.dimension_templates import draft_warnings, get_template, merge_template
    scope = require_scope(write=True)
    template = get_template(template_id)
    with artifact_session(scope) as session:
        expected = revision(session)
        if body.revision != expected:
            raise HTTPException(409, {'reason': 'revision_conflict', 'message': '配置已由其他研究员修改，请刷新后重试', 'revision': expected})
        # An unsaved workbench has implicit defaults, not six custom dimensions.
        previous = read_dimensions(session) if expected else []
        if body.dimensions is not None:
            previous = validate_config(body.dimensions, read_dimensions(session), draft=True)
        dimensions = merge_template(template_id, previous)
        return {**describe_config(session, scope, dimensions),
                'template': {'id': template['id'], 'name': template['name']},
                'warnings': draft_warnings(dimensions)}


@router.get('/data')
def get_data(date: str | None = None, period: str = 'weekly',
             judgment_id: Annotated[int | None, Query(ge=1, le=2**63 - 1)] = None):
    from tin.web.chart_data import cutoff
    from tin.web.dimension_data import dimension_data
    scope = require_scope()
    if period not in {'daily', 'weekly'}:
        raise HTTPException(422, '周期只能为 daily 或 weekly')
    day = cutoff(date) if date is not None else None
    with artifact_session(scope) as session:
        dimensions = read_dimensions(session)
        counts, extra = evidence_counts(session, dimensions, scope)
        identifiers = {item['id'] for item in _items(dimensions) if item['kind'] == 'indicator'}
        selected_id, references = _judgment_references(session, scope, identifiers, judgment_id)
        rev = revision(session)
    with source_error_details():
        result = dimension_data(scope, dimensions, day, period)
    for dim in result['dimensions']:
        dim['references'] = counts[dim['id']]
    if references:
        result['judgment_id'] = str(selected_id)
        for item in _items(result['dimensions']):
            if item['kind'] == 'indicator' and item['id'] in references:
                item['judgment_references'] = references[item['id']]
    return {**result, 'revision': rev, 'evidence': extra, 'evidence_available': 'judgments' in scope.view_modules}


def build_page_router(templates):
    pages = APIRouter()

    @pages.get('/sn/dimensions')
    def page(request: Request):
        from tin.web.workbench_setup_entry import setup_enabled
        scope = require_scope()
        return templates.TemplateResponse(request, 'dimensions.html', {
            'nav': 'dimensions', 'ticker': [], 'names': {}, 'd': None,
            'dimension_page': {'date': request.query_params.get('date'), 'can_write': scope.can_edit,
                               'setup_enabled': setup_enabled(scope)}})

    return pages


def restricted_page(request, scope, templates):
    """Core-focus rules: persisted ids, latest two observations, no raw routes."""
    from tin.integration.page_facts import bounded_facts
    from tin.web.restricted_views import ORIGINS_KEY
    from tin.web.number_format import format_number
    from tin.web.strategy_monitor import _staleness
    from tin.web.dimension_data import observation_update
    from tin.config import SHANGHAI
    if 'indicators' not in scope.view_modules or not scope.source_available:
        raise HTTPException(403, '当前授权未开放研究维度')
    with artifact_session(scope) as session:
        dimensions = read_dimensions(session)
        identifiers = [item['id'] for item in _items(dimensions) if item['kind'] == 'indicator']
        row = session.get(SourceMapping, mapping_key(session, ORIGINS_KEY))
        origins = ((row.config or {}).get('origins') or {}) if row else {}
        facts = bounded_facts(scope, identifiers, 'indicators', origins=origins) if identifiers else {'indicators': [], 'rows': []}
        fields = {field['series_id']: field for field in facts['indicators']}
        counts, extra = evidence_counts(session, dimensions, scope)
        _, references = _judgment_references(session, scope, fields.keys())
        rev = revision(session)
    today = datetime.now(SHANGHAI).date()
    report_day = date.fromisoformat(max((point['date'] for point in facts['rows']
        if point['series_id'] in fields), default=today.isoformat()))
    failed, dates = [], []
    config_dims, data_dims = deepcopy(dimensions), deepcopy(dimensions)
    for config, dim in zip(config_dims, data_dims):
        config.setdefault('sections', [])
        dim.setdefault('sections', [])
        config['references'] = dim['references'] = counts[dim['id']]
        summary = dict.fromkeys(('up', 'down', 'flat', 'no_update', 'extreme'), 0)
        for config_section, section in zip(config['sections'], dim['sections']):
            for meta, item in zip(config_section['items'], section['items']):
                field = fields.get(item['id']) if item['kind'] == 'indicator' else None
                reason = '受限视角不开放图表库' if item['kind'] == 'chart' else '已绑定指标暂不可用'
                meta.update(name=indicator_name(item['id'], {item['id']: field.get('name') or field.get('metric')}) if field else '不可用条目',
                            unit=field.get('unit') or '' if field else '', frequency=field.get('frequency') or '' if field else '',
                            sheet=field.get('sheet') or '' if field else '',
                            source='已授权绑定', status='ok' if field else 'unavailable', reason='' if field else reason)
                item.update(meta)
                if field and item['id'] in references:
                    item['judgment_references'] = references[item['id']]
                points = sorted([point for point in facts['rows'] if point['series_id'] == item['id']], key=lambda point: point['date'])
                if not field or not points:
                    item.update(status='unavailable', reason=reason)
                    failed.append({'kind': item['kind'], 'id': item['id'], 'reason': reason})
                    continue
                latest, old = points[-1], points[-2] if len(points) > 1 else None
                value = latest['value']
                delta = value - old['value'] if old else None
                pct = delta / abs(old['value']) if old and old['value'] != 0 else None
                update = observation_update(latest['date'], old['date'] if old else None, report_day, 'daily')
                direction = ('no_update' if update['update_status'] == 'no_update' else
                             'up' if delta is not None and delta > 0 else 'down' if delta is not None and delta < 0 else 'flat' if delta == 0 else None)
                if direction:
                    summary[direction] += 1
                dates.append(latest['date'])
                item.update(latest=value, date=latest['date'], previous_date=old['date'] if old else None,
                    change=delta, change_pct=pct, direction=direction, points=[],
                    **update,
                    baseline={'label': '较上期', 'date': old['date'] if old else None},
                    display={'latest': format_number(value), 'change': format_number(delta, signed=True),
                             'change_pct': format_number(pct * 100 if pct is not None else None, signed=True) + ('%' if pct is not None else '')},
                    freshness=_staleness({'date': latest['date'], 'points': points}, today),
                    position={'rank': None, 'n': 0, 'text': '受限视角仅展示最新必要观测', 'notice': '受限视角不开放历史同期数据'})
        dim['summary'] = summary
    config = {'dimensions': config_dims, 'revision': rev, 'can_write': False, 'evidence': extra, 'categories': [],
              'evidence_available': 'judgments' in scope.view_modules}
    data = {'dimensions': data_dims, 'revision': rev, 'evidence': extra, 'as_of': max(dates, default=today.isoformat()),
            'period': 'daily', 'failed': failed}
    return templates.TemplateResponse(request, 'dimensions.html', {'nav': 'dimensions', 'ticker': [], 'names': {}, 'd': None,
        'restricted_view': True, 'dimension_page': {'can_write': False, 'restricted': True, 'config': config, 'data': data}})
