"""Personal PPT templates. All reads stay inside a verified v2 workbench."""
from datetime import datetime, timezone
import json
import os
from pathlib import Path
from tempfile import NamedTemporaryFile, TemporaryDirectory
from uuid import uuid4

from fastapi import APIRouter, Body, HTTPException, Query, Request
from fastapi.routing import APIRoute
from fastapi.responses import StreamingResponse
from pydantic import TypeAdapter, ValidationError
from sqlalchemy import func, select, update
from starlette.background import BackgroundTask
from starlette.concurrency import run_in_threadpool
from starlette.datastructures import UploadFile

from tin.integration.workbench_scope import artifact_session, current_workbench, require_shared_write
from tin.models.workbench import WorkbenchArtifact
from tin.ppt.candidates import template_candidates
from tin.ppt.exporter import MAX_IMAGES, MAX_PNG, PNG_SIGNATURE, assemble
from tin.ppt.package import MAX_UPLOAD, PptError
from tin.ppt.parser import parse_pptx
from tin.ppt.storage import export_lock, export_path, read_original, save_original, storage_root
from tin.schemas.chart import RevisionInput
from tin.schemas.ppt import BindingsUpdate, ExportSlotMeta
from tin.web import charts
from tin.web.chart_data import catalog_fields, cutoff, read_series, unit_notices
from tin.web.workbench_artifacts import record_revision


class PptRoute(APIRoute):
    def get_route_handler(self):
        original = super().get_route_handler()

        async def handler(request):
            try:
                return await original(request)
            except PptError as exc:
                raise HTTPException(exc.status, str(exc)) from None
        return handler


router = APIRouter(prefix='/api/sn/ppt-templates', route_class=PptRoute)
META = TypeAdapter(dict[str, ExportSlotMeta])


def require_scope(*, write=False):
    scope = current_workbench.get()
    if scope is not None and scope.restricted:
        raise HTTPException(403, '受限视角暂不开放PPT模板')
    if scope is None or scope.scope_version != 2:
        raise HTTPException(409, 'PPT模板需要来源版本2，请重新打开研究台更新来源授权')
    if write:
        require_shared_write(scope)
    return scope


def owned_query(scope, *, deleted=False):
    # Shared-store workbench stamp plus the personal owner boundary.
    return charts.artifact_query('ppt_templates', deleted=deleted).where(
        WorkbenchArtifact.actor == scope.identity.user_id)


def load_template(session, scope, identifier, *, deleted=False):
    row = session.scalar(owned_query(scope, deleted=deleted).where(WorkbenchArtifact.id == identifier))
    if row is None:
        raise HTTPException(404, 'PPT模板不存在或无权访问')
    return row


def contents(row):
    try:
        value = json.loads(row.body)
        if value['format'] != 'ppt-template.v1':
            raise ValueError
        return value
    except (ValueError, KeyError, TypeError):
        raise HTTPException(409, 'PPT模板记录格式无效') from None


def serialize(row, *, detail=True):
    result = {key: getattr(row, key) for key in ('id', 'title', 'status', 'revision', 'created_at', 'updated_at')}
    value = contents(row)
    slots = [slot for slide in value['presentation']['slides'] for slot in slide['slots']]
    slot_counts = {'bound': 0, 'kept': 0, 'pending': 0}
    for slot in slots:
        binding = value['bindings'].get(slot['slot_id'])
        state = ('kept' if not slot['replaceable'] or (binding and binding['kind'] == 'keep')
                 else 'bound' if binding else 'pending')
        slot_counts[state] += 1
    result.update(filename=value['filename'], sha256=value['sha256'],
                  slide_count=len(value['presentation']['slides']),
                  slot_count=len(slots), slot_counts=slot_counts)
    if detail:
        result.update(presentation=value['presentation'], bindings=value['bindings'], exports=value['exports'])
    return result


def save_revision(session, scope, row, revision, value, *, status=None):
    changed = session.execute(update(WorkbenchArtifact).where(
        WorkbenchArtifact.id == row.id, WorkbenchArtifact.module == 'ppt_templates',
        WorkbenchArtifact.actor == scope.identity.user_id, WorkbenchArtifact.status != 'deleted',
        WorkbenchArtifact.revision == revision).values(
            body=json.dumps(value, ensure_ascii=False, allow_nan=False), revision=revision + 1,
            status=status or row.status, updated_at=datetime.now(timezone.utc)))
    if changed.rowcount != 1:
        raise HTTPException(409, 'PPT模板已更新，请刷新后继续')
    session.refresh(row)
    record_revision(session, row, scope.identity.user_id)
    session.commit()


def chart_evidence(session, scope):
    definitions = []
    warnings = []
    for row in session.scalars(charts.artifact_query()).all():
        try:
            definitions.append((row.id, charts.decode_definition(row)))
        except HTTPException as exc:
            if exc.status_code != 409:
                raise
            warnings.append({'chart_id': row.id, 'code': 'invalid_chart'})
    fields = catalog_fields(scope, [curve.series_id for _, definition in definitions for curve in definition.curves])
    return ([{'id': identifier, 'title': definition.title,
              'labels': [curve.label for curve in definition.curves],
              'segment_labels': [style.label for style in getattr(definition.time, 'segment_styles', [])],
              'series_ids': [curve.series_id for curve in definition.curves],
              'units': [fields.get(curve.series_id, {}).get('unit') or '' for curve in definition.curves]}
             for identifier, definition in definitions], fields, warnings)


@router.get('')
def list_templates(offset: int = Query(0, ge=0), limit: int = Query(100, ge=1, le=100)):
    scope = require_scope()
    with artifact_session(scope) as session:
        query = owned_query(scope)
        total = session.scalar(select(func.count()).select_from(query.subquery()))
        rows = session.scalars(query.order_by(WorkbenchArtifact.updated_at.desc(), WorkbenchArtifact.id)
                               .offset(offset).limit(limit)).all()
        return {'items': [serialize(row, detail=False) for row in rows], 'total': total, 'offset': offset,
                'next_offset': offset + len(rows) if offset + len(rows) < total else None}


def create_template(scope, filename, data):
    presentation = parse_pptx(data)
    now = datetime.now(timezone.utc)
    with artifact_session(scope) as session:
        digest = save_original(storage_root(session), data)
        value = {'format': 'ppt-template.v1', 'filename': filename, 'sha256': digest,
                 'presentation': presentation, 'bindings': {}, 'exports': []}
        row = WorkbenchArtifact(id=str(uuid4()), module='ppt_templates', actor=scope.identity.user_id,
            title=filename[:120], body=json.dumps(value, ensure_ascii=False), status='draft',
            source_ids=[], source_snapshots=[], revision=1, created_at=now, updated_at=now)
        session.add(row)
        session.flush()
        from tin.web.research_references import remember_record_scope
        remember_record_scope(session, 'artifact', row.id)
        record_revision(session, row, scope.identity.user_id)
        session.commit()
        return serialize(row)


@router.post('', status_code=201)
async def upload_template(request: Request):
    scope = require_scope(write=True)
    async with request.form(max_files=1, max_fields=0) as form:
        values = list(form.multi_items())
        if len(values) != 1 or values[0][0] != 'file' or not isinstance(values[0][1], UploadFile):
            raise HTTPException(422, '请用file字段上传一个PPTX文件')
        upload = values[0][1]
        filename = (upload.filename or '').replace('\\', '/').rsplit('/', 1)[-1]
        if not filename.lower().endswith('.pptx') or len(filename) > 1000:
            raise HTTPException(400, '只支持.pptx文件')
        if upload.size is not None and upload.size > MAX_UPLOAD:
            raise HTTPException(413, 'PPT原件不能超过60MB')
        data = await upload.read(MAX_UPLOAD + 1)
        if len(data) > MAX_UPLOAD:
            raise HTTPException(413, 'PPT原件不能超过60MB')
        return await run_in_threadpool(create_template, scope, filename, data)


@router.get('/{template_id}')
def get_template(template_id: str):
    scope = require_scope()
    with artifact_session(scope) as session:
        row = load_template(session, scope, template_id)
        result = serialize(row)
        evidence, fields, warnings = chart_evidence(session, scope)
    result['candidates'] = template_candidates(result['presentation'], result['bindings'], evidence, fields, read_series)
    result['warnings'] = warnings
    return result


@router.patch('/{template_id}/bindings')
def patch_bindings(template_id: str, body: BindingsUpdate):
    scope = require_scope(write=True)
    with artifact_session(scope) as session:
        row = load_template(session, scope, template_id)
        if row.revision != body.revision:
            raise HTTPException(409, 'PPT模板已更新，请刷新后继续')
        value = contents(row)
        slots = {slot['slot_id']: slot for slide in value['presentation']['slides'] for slot in slide['slots']}
        for change in body.slots:
            slot = slots.get(change.slot_id)
            if slot is None:
                raise HTTPException(422, '图位不存在于当前模板')
            binding = change.binding
            if binding and binding.kind != 'keep' and not slot['replaceable']:
                raise HTTPException(422, '原生图表、SVG或缺失媒体仅支持保留原图')
            if binding and binding.kind == 'chart':
                chart = session.scalar(charts.artifact_query().where(WorkbenchArtifact.id == binding.chart_id))
                if chart is None:
                    raise HTTPException(422, '绑定图表不存在于本研究台图表库')
            value['bindings'][change.slot_id] = binding.model_dump(mode='json') if binding else None
    identifiers = [change.binding.series_id for change in body.slots if change.binding and change.binding.kind == 'series']
    fields = catalog_fields(scope, identifiers)
    if any(identifier not in fields for identifier in identifiers):
        raise HTTPException(422, '绑定指标不存在于当前研究台目录')
    with artifact_session(scope) as session:
        row = load_template(session, scope, template_id, deleted=True)
        if row.revision != body.revision or row.status == 'deleted':
            raise HTTPException(409, 'PPT模板已更新，请刷新后继续')
        for change in body.slots:
            if change.binding and change.binding.kind == 'chart':
                chart = session.scalar(charts.artifact_query().where(WorkbenchArtifact.id == change.binding.chart_id))
                if chart is None:
                    raise HTTPException(422, '绑定图表不存在于本研究台图表库')
        save_revision(session, scope, row, body.revision, value)
        return serialize(row)


@router.delete('/{template_id}')
def delete_template(template_id: str, revision: int | None = Query(None, ge=1), body: RevisionInput | None = Body(None)):
    scope = require_scope(write=True)
    if body is not None:
        if revision is not None and revision != body.revision:
            raise HTTPException(422, '删除请求的revision不一致')
        revision = body.revision
    with artifact_session(scope) as session:
        row = load_template(session, scope, template_id)
        # DELETE has no required body in the task contract; optionally fence a caller revision.
        revision = row.revision if revision is None else revision
        save_revision(session, scope, row, revision, contents(row), status='deleted')
        return {'id': row.id, 'revision': row.revision, 'status': row.status}


def binding_states(session, scope, bindings):
    definitions, reasons = {}, {}
    for binding in bindings.values():
        if not binding or binding['kind'] != 'chart' or binding['chart_id'] in definitions:
            continue
        identifier = binding['chart_id']
        row = session.scalar(charts.artifact_query().where(WorkbenchArtifact.id == identifier))
        if row is None:
            reasons[identifier] = 'chart_missing'
            continue
        try:
            definitions[identifier] = charts.decode_definition(row)
        except HTTPException:
            reasons[identifier] = 'chart_invalid'
    identifiers = [binding['series_id'] for binding in bindings.values() if binding and binding['kind'] == 'series']
    identifiers.extend(curve.series_id for definition in definitions.values() for curve in definition.curves)
    try:
        fields = catalog_fields(scope, identifiers)
    except HTTPException as exc:
        if exc.status_code != 503:
            raise
        return {}, {identifier: 'source_unavailable' for identifier in definitions} | reasons, True
    for identifier, definition in definitions.items():
        notices = unit_notices(definition, fields)
        if any(curve.series_id not in fields for curve in definition.curves):
            reasons[identifier] = 'series_missing'
        elif any(notice['code'] == 'axis_unit_mismatch' for notice in notices):
            reasons[identifier] = 'chart_unit_mismatch'
    return fields, reasons, False


def export_template(scope, template_id, images, day, mark_headlines, meta, root):
    with export_lock(root, template_id), artifact_session(scope) as session:
        row = load_template(session, scope, template_id)
        value, revision = contents(row), row.revision
        slots = {slot['slot_id']: slot for slide in value['presentation']['slides'] for slot in slide['slots']}
        if (set(images) | set(meta)) - slots.keys():
            raise HTTPException(422, '导出包含未知图位')
        fields, invalid_charts, unavailable = binding_states(session, scope, value['bindings'])
        updated, kept, replacements = [], [], {}
        for identifier, slot in slots.items():
            binding = value['bindings'].get(identifier)
            metadata = meta.get(identifier, ExportSlotMeta())
            reason = slot['preserve_reason']
            if reason is None:
                if not binding:
                    reason = 'unbound'
                elif binding['kind'] == 'keep':
                    reason = 'keep'
                elif binding['kind'] == 'chart':
                    reason = invalid_charts.get(binding['chart_id'])
                elif unavailable:
                    reason = 'source_unavailable'
                elif binding['series_id'] not in fields:
                    reason = 'series_missing'
            if reason is None and metadata.error:
                reason = 'data_read_failed'
            if reason is None and identifier not in images:
                reason = 'png_not_supplied'
            item = {'slot_id': identifier, 'slide': slot['slide'],
                    'data_as_of': metadata.data_as_of.isoformat() if metadata.data_as_of else None}
            if reason:
                kept.append({**item, 'reason': reason, **({'detail': metadata.error} if metadata.error else {})})
            else:
                replacements[identifier] = images[identifier]
                updated.append(item)
        original = read_original(root, value['sha256'])
        export_id = str(uuid4())
        destination = export_path(root, template_id, export_id)
        with NamedTemporaryFile(dir=destination.parent, prefix='.assembling-', delete=False) as stream:
            temporary = Path(stream.name)
        try:
            titles = assemble(original, temporary, value['presentation'], replacements, mark_headlines=mark_headlines)
            report = {'export_id': export_id, 'date': day.isoformat(), 'revision': revision + 1,
                'created_at': datetime.now(timezone.utc).isoformat(), 'updated_slots': updated,
                'kept_slots': kept, 'headlines_to_check': titles, 'mark_headlines': mark_headlines,
                'data_as_of_source': 'frontend',
                'download_url': f'/api/sn/ppt-templates/{template_id}/exports/{export_id}'}
            previous = value['exports']
            value['exports'] = [*previous, report][-5:]
            temporary.replace(destination)
            try:
                save_revision(session, scope, row, revision, value)
            except BaseException:
                destination.unlink(missing_ok=True)
                raise
            for expired in previous[:-4]:
                export_path(root, template_id, expired['export_id']).unlink(missing_ok=True)
            return report
        finally:
            temporary.unlink(missing_ok=True)


@router.post('/{template_id}/export')
async def post_export(template_id: str, request: Request):
    scope = require_scope(write=True)
    with artifact_session(scope) as session:
        load_template(session, scope, template_id)  # Reject foreign templates before consuming files.
        root = storage_root(session)
    async with request.form(max_files=MAX_IMAGES, max_fields=3, max_part_size=1024 * 1024) as form:
        items = list(form.multi_items())
        keys = [key for key, _ in items]
        if len(set(keys)) != len(keys):
            raise HTTPException(422, '导出字段不能重复')
        if not isinstance(form.get('date'), str):
            raise HTTPException(422, '导出需要date字段')
        day = cutoff(form['date'])
        mark = form.get('mark_headlines', 'false')
        if mark not in {'true', 'false', '1', '0', 'on', 'off'}:
            raise HTTPException(422, 'mark_headlines需要布尔值')
        try:
            raw_meta = form.get('meta', '{}')
            if not isinstance(raw_meta, str):
                raise ValueError
            meta = META.validate_json(raw_meta)
        except (ValidationError, ValueError):
            raise HTTPException(422, 'meta需要按图位提供有效的数据截至日期或读取错误') from None
        if any(item.data_as_of and item.data_as_of > day for item in meta.values()):
            raise HTTPException(422, '图位数据截至日期不能晚于导出截止日')
        with TemporaryDirectory(dir=root, prefix='.export-input-') as directory:
            images = {}
            for key, upload in items:
                if key in {'date', 'mark_headlines', 'meta'}:
                    continue
                if not isinstance(upload, UploadFile):
                    raise HTTPException(422, '导出包含未知文本字段')
                if upload.size is not None and upload.size > MAX_PNG:
                    raise HTTPException(413, '单张PNG不能超过4MB')
                data = await upload.read(MAX_PNG + 1)
                if len(data) > MAX_PNG:
                    raise HTTPException(413, '单张PNG不能超过4MB')
                if not data.startswith(PNG_SIGNATURE):
                    raise HTTPException(400, '图位图片必须是PNG签名')
                with NamedTemporaryFile(dir=directory, delete=False) as stream:
                    stream.write(data)
                    images[key] = Path(stream.name)
            return await run_in_threadpool(export_template, scope, template_id, images, day,
                                           mark in {'true', '1', 'on'}, meta, root)


@router.get('/{template_id}/exports/{export_id}')
def download_export(template_id: str, export_id: str):
    scope = require_scope()
    with artifact_session(scope) as session:
        row = load_template(session, scope, template_id)
        root = storage_root(session)
        with export_lock(root, template_id):
            # Refresh after the file lock: a concurrent export may have pruned a result.
            session.refresh(row)
            if row.status == 'deleted' or not any(item['export_id'] == export_id for item in contents(row)['exports']):
                raise HTTPException(404, '导出不存在或已超出最近5份保留范围')
            path = export_path(root, template_id, export_id)
            try:
                descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
                stream = os.fdopen(descriptor, 'rb')
            except OSError:
                raise HTTPException(404, '导出文件不存在') from None
    # Keep an open descriptor so later retention cannot interrupt this download.
    return StreamingResponse(iter(lambda: stream.read(1024 * 1024), b''),
        media_type='application/vnd.openxmlformats-officedocument.presentationml.presentation',
        headers={'Content-Disposition': f'attachment; filename="{template_id}-{export_id}.pptx"'},
        background=BackgroundTask(stream.close))
