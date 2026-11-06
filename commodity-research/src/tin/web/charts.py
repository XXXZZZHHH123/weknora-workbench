"""Shared chart definitions, revisions, templates and bounded chart data."""
from datetime import datetime, timezone
import json
from uuid import uuid4

from fastapi import APIRouter, Body, HTTPException, Query
from pydantic import TypeAdapter, ValidationError
from sqlalchemy import func, select, update

from tin.compute.cycles import BUILTIN_TEMPLATES, compute_cycles
from tin.config import settings
from tin.integration.workbench_scope import artifact_session, current_workbench, require_shared_write
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision
from tin.models import SourceMapping
from tin.schemas.chart import ChartDefinition, ChartUpdate, CycleTemplate, RevisionInput
from tin.web.chart_data import catalog_fields, cutoff, read_series, unit_notices, validate_definition
from tin.web.workbench_artifacts import record_revision

router = APIRouter(prefix='/api/sn')
TEMPLATE = TypeAdapter(CycleTemplate)


def require_scope(*, write=False, instant=False):
    scope = current_workbench.get()
    if scope is not None and scope.restricted:
        raise HTTPException(403, '受限视角暂不开放图表库和周期对比')
    if instant and (scope is not None or not settings.weknora_api_url):
        return scope
    if scope is None or scope.scope_version != 2:
        raise HTTPException(409, '图表库需要来源版本2，请重新打开研究台更新来源授权')
    if write:
        require_shared_write(scope)
    return scope


def artifact_query(module='charts', *, deleted=False):
    query = select(WorkbenchArtifact).where(WorkbenchArtifact.module == module)
    scope = current_workbench.get()
    if scope is not None:
        from tin.web.research_references import REGISTRY_FORMAT
        # Legacy result storage can contain more than one workbench. Chart
        # records carry the existing workbench stamp; its author does not limit
        # shared reads by another member of the same workbench.
        stamp = SourceMapping.config
        belongs = select(SourceMapping.id).where(stamp['format'].as_string() == REGISTRY_FORMAT,
            stamp['kind'].as_string() == 'artifact', stamp['record_id'].as_string() == WorkbenchArtifact.id,
            stamp['workbench_id'].as_string() == scope.workbench_id).exists()
        query = query.where(belongs)
    return query if deleted else query.where(WorkbenchArtifact.status != 'deleted')


def load_artifact(session, identifier, module='charts', *, deleted=False):
    row = session.scalar(artifact_query(module, deleted=deleted).where(WorkbenchArtifact.id == identifier))
    if row is None:
        raise HTTPException(404, '图表或周期模板不存在')
    return row


def decode_definition(row):
    try:
        return ChartDefinition.model_validate_json(row.body)
    except ValidationError:
        raise HTTPException(409, '已保存图表格式无效，请核对修订历史') from None


def serialize(row, *, definition=None, fields=None):
    definition = definition or decode_definition(row)
    result = {key: getattr(row, key) for key in ('id', 'title', 'status', 'revision', 'created_at', 'updated_at')}
    result['definition'] = definition.model_dump(mode='json')
    result['warnings'] = unit_notices(definition, fields) if fields is not None else []
    if fields is not None:
        result['curves'] = [{**curve.model_dump(mode='json'), 'status': 'linked' if curve.series_id in fields else 'missing',
                             'unit': fields.get(curve.series_id, {}).get('unit')} for curve in definition.curves]
    if row.source_snapshots:
        result['source_snapshots'] = row.source_snapshots
    return result


def create_row(session, scope, definition, *, module='charts', snapshots=None):
    now = datetime.now(timezone.utc)
    row = WorkbenchArtifact(id=str(uuid4()), module=module, actor=scope.identity.user_id,
        title=definition.title if module == 'charts' else definition.name,
        body=definition.model_dump_json(exclude_none=True), status='draft', source_ids=[],
        source_snapshots=snapshots or [], revision=1, created_at=now, updated_at=now)
    session.add(row)
    session.flush()
    from tin.web.research_references import remember_record_scope
    remember_record_scope(session, 'artifact', row.id)
    record_revision(session, row, scope.identity.user_id)
    return row


@router.get('/charts')
def list_charts(q: str = Query('', max_length=200), group: str | None = Query(None, max_length=80),
                series_id: str | None = Query(None, max_length=128), offset: int = Query(0, ge=0),
                limit: int = Query(100, ge=1, le=100)):
    scope = require_scope()
    with artifact_session(scope) as session:
        query = artifact_query()
        if q:
            query = query.where(WorkbenchArtifact.title.icontains(q, autoescape=True))
        if group is not None:
            query = query.where(func.json_extract(WorkbenchArtifact.body, '$.group') == group)
        # Inspect saved real IDs, never match descriptors or labels as bindings.
        rows = session.scalars(query.order_by(WorkbenchArtifact.updated_at.desc(), WorkbenchArtifact.id)).all()
        definitions = [(row, decode_definition(row)) for row in rows]
        if series_id is not None:
            definitions = [(row, value) for row, value in definitions if any(c.series_id == series_id for c in value.curves)]
        total = len(definitions)
        selected = definitions[offset:offset + limit]
        fields = catalog_fields(scope, [curve.series_id for _, value in selected for curve in value.curves])
        return {'items': [serialize(row, definition=value, fields=fields) for row, value in selected],
                'total': total, 'offset': offset, 'next_offset': offset + len(selected) if offset + len(selected) < total else None}


@router.post('/charts', status_code=201)
def create_chart(body: ChartDefinition):
    scope = require_scope(write=True)
    warnings = validate_definition(scope, body)
    with artifact_session(scope) as session:
        row = create_row(session, scope, body)
        session.commit()
        result = serialize(row, definition=body)
        result['warnings'] = warnings
        return result


# Keep this literal route ahead of /charts/{chart_id}.
@router.get('/charts/anomalies')
def chart_anomalies(date: str | None = None, limit: int | None = Query(None, ge=1)):
    """Compare every saved cycle chart; limit applies after the global sort."""
    scope = require_scope()
    day = cutoff(date)
    grouped, items, failed = {}, [], []

    def failure(row, error):
        reason = error.detail if isinstance(error, HTTPException) else str(error)
        failed.append({'id': row.id, 'title': row.title, 'reason': reason or '图表读取或计算失败'})

    with artifact_session(scope) as session:
        rows = session.scalars(artifact_query().order_by(WorkbenchArtifact.id)).all()
        for row in rows:
            try:
                definition = decode_definition(row)
                if definition.time.mode == 'cycle':
                    grouped.setdefault(definition.curves[0].series_id, []).append((row, definition))
            except Exception as error:
                failure(row, error)

    def read_batch(identifiers):
        try:
            fields = catalog_fields(scope, identifiers)
            data = read_series([sid for sid in identifiers if sid in fields], day, limit=2000)
        except Exception as error:
            # Authorization and source changes must still reject the read. Do
            # not retry them against a new grant or snapshot within this request.
            if isinstance(error, HTTPException) and error.status_code in {401, 403, 409}:
                raise
            if len(identifiers) > 1:
                middle = len(identifiers) // 2
                read_batch(identifiers[:middle])
                read_batch(identifiers[middle:])
            else:
                for row, _ in grouped[identifiers[0]]:
                    failure(row, error)
            return
        for sid in identifiers:
            for row, definition in grouped[sid]:
                try:
                    field, series = fields.get(sid), data.get(sid)
                    if field is None:
                        raise HTTPException(404, '指标已不在当前来源目录')
                    if series is None:
                        raise HTTPException(404, '指标观测不可读')
                    if (field.get('unit') or '').strip() and field['unit'].strip() != series['unit'].strip():
                        raise HTTPException(409, '图表单位在目录与观测读取之间已变化，请刷新核对')
                    item = anomaly_item(row, definition, field, series, day)
                    if item is not None:
                        items.append(item)
                except Exception as error:
                    failure(row, error)

    identifiers = list(grouped)
    # Retain the chart data window: 2000 points per metric, at most 256 metrics
    # and 20000 points per page projection. Shared metrics are fetched once.
    batch_size = min(256, 20000 // 2000)
    for offset in range(0, len(identifiers), batch_size):
        read_batch(identifiers[offset:offset + batch_size])
    items.sort(key=lambda item: (not any(reason in {'extreme_high', 'extreme_low'} for reason in item['reasons']),
                                -abs(item['rank_change'] or 0), item['title'], item['id']))
    failed.sort(key=lambda item: item['id'])
    return {'as_of': day.isoformat(), 'items': items[:limit], 'total': len(items), 'failed': failed}


def anomaly_item(row, definition, field, series, day):
    time = definition.time
    template = time.template.model_dump(mode='json', exclude_none=True)
    template['transform'] = time.transform
    current = cycle_result(series, template, day, band_cycles=time.band_cycles, cycles=time.cycles)
    position = current['position']
    if position is None or position['n'] < 3 or position.get('rank') is None:
        return None
    previous_days = [point['date'] for point in series['points']
                     if point['value'] is not None and point['date'] < position['date']]
    previous = (cycle_result(series, template, cutoff(previous_days[-1]),
                             band_cycles=time.band_cycles, cycles=time.cycles)['position']
                if previous_days else None)
    previous_rank = previous.get('rank') if previous else None
    rank_change = position['rank'] - previous_rank if previous_rank is not None else None
    reasons = []
    if position['rank'] <= 2:
        reasons.append('extreme_high')
    if position['rank'] >= position['n']:
        reasons.append('extreme_low')
    if rank_change is not None and abs(rank_change) >= 2:
        reasons.append('rank_change')
    if not reasons:
        return None
    recent_values = [peer['value'] for peer in position['peers'][:5]]
    return {'id': row.id, 'title': definition.title, 'revision': row.revision, 'series_id': definition.curves[0].series_id,
            'metric': field.get('metric') or series['name'], 'date': position['date'], 'value': position['value'],
            'unit': field.get('unit'), 'transform': time.transform, 'rank': position['rank'], 'n': position['n'],
            'previous_date': previous['date'] if previous else None, 'previous_rank': previous_rank,
            'previous_n': previous['n'] if previous else None, 'rank_change': rank_change,
            'share_below': position['share_below'], 'vs_recent_mean': position['vs_recent_mean'],
            'recent_mean': sum(recent_values) / len(recent_values), 'recent_cycles': len(recent_values),
            'reasons': reasons, 'warnings': current['warnings']}


@router.get('/charts/{chart_id}')
def get_chart(chart_id: str):
    scope = require_scope()
    with artifact_session(scope) as session:
        row = load_artifact(session, chart_id)
        definition = decode_definition(row)
        fields = catalog_fields(scope, [curve.series_id for curve in definition.curves])
        return serialize(row, definition=definition, fields=fields)


@router.get('/charts/{chart_id}/reconvert')
def reconvert_chart(chart_id: str):
    """Propose a fresh definition without mutating the saved chart or history."""
    scope = require_scope()
    with artifact_session(scope) as session:
        row = load_artifact(session, chart_id)
        definition = decode_definition(row)
        revision = row.revision
    if definition.origin.kind != 'native' or not definition.origin.ref:
        raise HTTPException(422, '此图表没有可重新转换的原表来源')
    # Import lazily: the import workflow shares this module's artifact helpers.
    from tin.web.chart_import import original_native_chart, suggestions_for
    original = original_native_chart(scope, definition.origin.ref)
    suggestion = suggestions_for(scope, [original])[0]
    return {'id': chart_id, 'revision': revision, 'suggestion': suggestion}


@router.patch('/charts/{chart_id}')
def update_chart(chart_id: str, body: ChartUpdate):
    scope = require_scope(write=True)
    with artifact_session(scope) as session:
        row = load_artifact(session, chart_id)
        if row.revision != body.revision:
            raise HTTPException(409, '图表已更新，请刷新后继续修改')
        values = decode_definition(row).model_dump(mode='json')
    values.update(body.model_dump(mode='json', exclude_unset=True, exclude={'revision'}))
    try:
        definition = ChartDefinition.model_validate(values)
    except ValidationError as exc:
        raise HTTPException(422, str(exc)) from None
    warnings = validate_definition(scope, definition)
    with artifact_session(scope) as session:
        row = load_artifact(session, chart_id, deleted=True)
        changed = session.execute(update(WorkbenchArtifact).where(WorkbenchArtifact.id == chart_id,
            WorkbenchArtifact.module == 'charts', WorkbenchArtifact.status != 'deleted',
            WorkbenchArtifact.revision == body.revision).values(title=definition.title,
                body=definition.model_dump_json(exclude_none=True), revision=body.revision + 1,
                updated_at=datetime.now(timezone.utc)))
        if changed.rowcount != 1:
            raise HTTPException(409, '图表已更新，请刷新后继续修改')
        session.refresh(row)
        record_revision(session, row, scope.identity.user_id)
        session.commit()
        result = serialize(row, definition=definition)
        result['warnings'] = warnings
        return result


@router.delete('/charts/{chart_id}')
def delete_chart(chart_id: str, revision: int | None = Query(None, ge=1), body: RevisionInput | None = Body(None)):
    scope = require_scope(write=True)
    if body is not None:
        if revision is not None and revision != body.revision:
            raise HTTPException(422, '删除请求的修订号不一致')
        revision = body.revision
    if revision is None:
        raise HTTPException(422, '删除需要当前revision')
    with artifact_session(scope) as session:
        row = load_artifact(session, chart_id)
        changed = session.execute(update(WorkbenchArtifact).where(WorkbenchArtifact.id == chart_id,
            WorkbenchArtifact.module == 'charts', WorkbenchArtifact.status != 'deleted',
            WorkbenchArtifact.revision == revision).values(status='deleted', revision=revision + 1,
                updated_at=datetime.now(timezone.utc)))
        if changed.rowcount != 1:
            raise HTTPException(409, '图表已更新，请刷新后继续删除')
        session.refresh(row)
        record_revision(session, row, scope.identity.user_id)
        session.commit()
        return {'id': row.id, 'status': row.status, 'revision': row.revision}


@router.get('/charts/{chart_id}/revisions')
def revisions(chart_id: str, offset: int = Query(0, ge=0), limit: int = Query(100, ge=1, le=100)):
    scope = require_scope()
    with artifact_session(scope) as session:
        load_artifact(session, chart_id, deleted=True)
        query = select(WorkbenchArtifactRevision).where(WorkbenchArtifactRevision.artifact_id == chart_id)
        total = session.scalar(select(func.count()).select_from(query.subquery()))
        rows = session.scalars(query.order_by(WorkbenchArtifactRevision.revision.desc()).offset(offset).limit(limit)).all()
        return {'items': [{'revision': row.revision, 'definition': json.loads(row.body), 'status': row.status,
                           'edited_by': row.edited_by, 'recorded_at': row.recorded_at} for row in rows],
                'total': total, 'offset': offset, 'next_offset': offset + len(rows) if offset + len(rows) < total else None}


def cycle_result(series, template, day, *, band_cycles=5, cycles=None):
    points = [(point['date'], point['value']) for point in series['points'] if point['value'] is not None]
    result = compute_cycles(points, template, day, band_cycles=band_cycles, cycles=cycles)
    if series['limited']:
        result['warnings'].append('observation_window_limited')
    return result


@router.get('/charts/{chart_id}/data')
def chart_data(chart_id: str, date: str | None = None):
    scope = require_scope()
    day = cutoff(date)
    with artifact_session(scope) as session:
        row = load_artifact(session, chart_id)
        definition, revision = decode_definition(row), row.revision
    fields = catalog_fields(scope, [curve.series_id for curve in definition.curves])
    notices = unit_notices(definition, fields)
    if any(item['code'] == 'axis_unit_mismatch' for item in notices):
        raise HTTPException(409, '来源单位已变化，同一纵轴单位不一致，请修改图表')
    time = definition.time
    if time.mode == 'range':
        day = min(day, time.end)
    limit = min(2000, 20000 // len(definition.curves))
    if time.mode == 'recent':
        limit = min(limit, time.points)
    data = read_series([sid for sid in dict.fromkeys(c.series_id for c in definition.curves) if sid in fields], day, limit=limit)
    if any((fields[sid].get('unit') or '').strip() and fields[sid]['unit'].strip() != series['unit'].strip()
           for sid, series in data.items()):
        raise HTTPException(409, '图表单位在目录与观测读取之间已变化，请刷新核对')
    if time.mode == 'cycle':
        sid = definition.curves[0].series_id
        template = time.template.model_dump(mode='json', exclude_none=True)
        template['transform'] = time.transform
        series = data.get(sid, {'points': [], 'limited': False})
        result = cycle_result(series, template, day, band_cycles=time.band_cycles, cycles=time.cycles)
        result.update(id=chart_id, revision=revision, series_id=sid, unit=fields.get(sid, {}).get('unit'), notices=notices)
        return result
    curves = []
    for curve in definition.curves:
        series = data.get(curve.series_id)
        points = series['points'] if series is not None else []
        if time.mode in {'since', 'range'}:
            points = [point for point in points if point['date'] >= time.start.isoformat()]
        curves.append({**curve.model_dump(mode='json'), 'status': 'ok' if series else 'missing',
                       'unit': fields.get(curve.series_id, {}).get('unit'), 'points': points,
                       'freq_days': series['freq_days'] if series else None,
                       'limited': series['limited'] if series else False})
    return {'id': chart_id, 'revision': revision, 'as_of': day.isoformat(), 'curves': curves,
            'axes': definition.axes.model_dump(), 'warnings': notices, 'point_limit': limit}


@router.get('/indicators/{series_id}/cycle')
def indicator_cycle(series_id: str, template: str = '自然年', date: str | None = None):
    scope = require_scope(instant=True)
    day = cutoff(date)
    selected = BUILTIN_TEMPLATES.get(template)
    if selected is None:
        scope = require_scope()
        with artifact_session(scope) as session:
            row = load_artifact(session, template, 'cycle_templates')
            try:
                selected = TEMPLATE.validate_json(row.body).model_dump(mode='json', exclude_none=True)
            except ValidationError:
                raise HTTPException(409, '周期模板格式无效') from None
    if scope is not None and scope.scope_version == 2:
        if series_id not in catalog_fields(scope, [series_id]):
            raise HTTPException(404, '指标不存在于当前研究台来源目录')
    series = read_series([series_id], day)
    if series_id not in series:
        raise HTTPException(404, '指标不存在于当前会话')
    result = cycle_result(series[series_id], selected, day)
    result.update(series_id=series_id, unit=series[series_id]['unit'])
    return result


@router.get('/cycle-templates')
def list_templates():
    scope = require_scope()
    with artifact_session(scope) as session:
        rows = session.scalars(artifact_query('cycle_templates').order_by(WorkbenchArtifact.created_at, WorkbenchArtifact.id)).all()
        return {'items': [*({'id': name, 'builtin': True, 'template': value} for name, value in BUILTIN_TEMPLATES.items()),
                          *({'id': row.id, 'builtin': False, 'revision': row.revision,
                             'template': TEMPLATE.validate_json(row.body).model_dump(mode='json', exclude_none=True)} for row in rows)]}


@router.post('/cycle-templates', status_code=201)
def create_template(body: CycleTemplate):
    scope = require_scope(write=True)
    with artifact_session(scope) as session:
        row = create_row(session, scope, body, module='cycle_templates')
        session.commit()
        return {'id': row.id, 'revision': row.revision, 'builtin': False,
                'template': body.model_dump(mode='json', exclude_none=True)}
