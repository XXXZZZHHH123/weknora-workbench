"""Download a deck from the same authorized report view as HTML and Markdown."""
from copy import deepcopy
from datetime import datetime
from io import BytesIO
import json
from urllib.parse import quote

from fastapi import APIRouter, HTTPException, Request
from fastapi.responses import Response
from PIL import Image, UnidentifiedImageError
from pydantic import BaseModel, ConfigDict, StrictBool, StrictInt, StrictStr, ValidationError, field_validator
from starlette.concurrency import run_in_threadpool
from starlette.datastructures import UploadFile

from tin.config import SHANGHAI, settings
from tin.web.report_drafts import require_scope, validate_period

router = APIRouter(prefix='/api/sn/reports')
MAX_PNG_BYTES = 4 * 1024 * 1024
MAX_PNG_SIDE = 4000
CHANGED = '报告内容已变化，请刷新后重试'
MIME = 'application/vnd.openxmlformats-officedocument.presentationml.presentation'


class _Input(BaseModel):
    model_config = ConfigDict(extra='forbid')


class Sections(_Input):
    summary: StrictBool
    research: StrictBool
    dashboard: StrictBool
    watch: StrictBool


class PptOptions(_Input):
    sections: Sections
    figures_per_slide: StrictInt = 2

    @field_validator('figures_per_slide')
    @classmethod
    def valid_count(cls, value):
        if value not in (1, 2, 4):
            raise ValueError('每页图数只能为1、2或4')
        return value


class Failure(_Input):
    key: StrictStr
    reason: StrictStr


def read_report_view(scope, day, period):
    """Retain the page's projection, saved source versions and archive behavior."""
    from sqlalchemy import select
    from tin.integration.research_profile import research_variety
    from tin.integration.workbench_scope import artifact_session
    from tin.models import Indicator
    from tin.web import app as web
    from tin.web.report_period import build_period, period_start
    from tin.web.report_view import present_snapshot

    through = validate_period(day, period)
    frozen, names, catalog = False, {}, {}
    # Archives are immutable source text. Do not resolve chart pins or read
    # observations for them, including when building their PPT chart pages.
    if not settings.weknora_api_url and period == 'daily':
        with artifact_session(scope) as session:
            variety = research_variety(session)
        archive = settings.exports_dir / variety / f'{day}.json'
        if archive.exists():
            payload = json.loads(archive.read_text(encoding='utf-8'))
            frozen = True
    if not frozen:
        with web._page_session('reports', day=through,
                               points_per_series=8 if period == 'weekly' else 2) as session:
            payload = build_period(session, research_variety(session), through, period)
            names = dict(session.execute(select(Indicator.series_id, Indicator.name)).all())
            catalog = deepcopy(session.info.get('knowledge_facts_catalog') or {})
        if (payload.get('manual_report') or {}).get('unavailable_references'):
            raise HTTPException(409, '报告引用已不可读，请先在报告中调整引用再导出')
    view = present_snapshot(payload, names)
    observations = {row['series_id']: row for row in view.get('observations') or []}
    for figure in view.get('report_figures') or []:
        pin = figure.get('reference') or {}
        identifiers = ([figure['series_id']] if figure.get('series_id') else [
            curve.get('series_id') for curve in (pin.get('definition') or {}).get('curves') or []])
        sources = []
        for identifier in identifiers:
            field = catalog.get(identifier) or {}
            source = ' · '.join(str(field[key]) for key in ('source_file', 'sheet') if field.get(key))
            source = source or str((observations.get(identifier) or {}).get('source') or '')
            if source and source not in sources:
                sources.append(source)
        if sources:
            figure['source'] = ' / '.join(sources)
        elif pin:
            figure['source'] = f'已保存图表「{figure["title"]}」v{pin["revision"]}所引资料'
    view.update(workbench_name=scope.metadata.get('name') or '研究工作台',
                as_of=payload.get('as_of') or day, frozen=frozen,
                generated_at=datetime.now(SHANGHAI).isoformat(timespec='seconds'))
    view['period'] = payload.get('period') or {
        'kind': period, 'start': period_start(through, period).isoformat(), 'end': day}
    return view


def _json_field(form, key):
    values = form.getlist(key)
    if len(values) != 1 or not isinstance(values[0], str):
        raise HTTPException(422, f'导出参数 {key} 缺失或重复')
    try:
        return json.loads(values[0])
    except (ValueError, TypeError):
        raise HTTPException(422, '导出参数格式无效') from None


def validate_png(data):
    if len(data) > MAX_PNG_BYTES:
        raise HTTPException(413, '单张PNG不能超过4 MB')
    if not data.startswith(b'\x89PNG\r\n\x1a\n'):
        raise HTTPException(422, '图表文件必须是有效PNG图片')
    try:
        with Image.open(BytesIO(data)) as picture:
            if picture.format != 'PNG':
                raise ValueError
            if not (0 < picture.width <= MAX_PNG_SIDE and 0 < picture.height <= MAX_PNG_SIDE):
                raise HTTPException(422, 'PNG宽高不能超过4000像素')
            picture.verify()
        with Image.open(BytesIO(data)) as picture:
            picture.load()
    except HTTPException:
        raise
    except (OSError, ValueError, SyntaxError, UnidentifiedImageError, Image.DecompressionBombError):
        raise HTTPException(422, '图表PNG损坏或格式无效') from None
    return data


def revalidate_figures(scope, figures):
    """Recheck live bitmap operands against current grants, without observations.

    A retained chart key or saved draft is not permission to reuse old pixels
    after an operand has disappeared from the authorized source catalogue.
    """
    from tin.web.chart_data import definition_series_ids, unit_notices
    from tin.web.chart_references import _metadata, reference_definition

    operands, definitions = {}, {}
    for figure in figures:
        try:
            if figure['kind'] == 'chart':
                _, definition, _ = reference_definition(scope, figure['reference'])
                identifiers = definition_series_ids(definition)
                definitions[figure['key']] = definition
            else:
                identifiers = [figure['series_id']]
            operands[figure['key']] = identifiers
        except HTTPException as exc:
            figure['notice'] = str(exc.detail)
    identifiers = list(dict.fromkeys(sid for values in operands.values() for sid in values))
    if not identifiers:
        return
    try:
        fields, _ = _metadata(scope, identifiers)
    except HTTPException as exc:
        for figure in figures:
            figure['notice'] = str(exc.detail)
        return
    for figure in figures:
        required = operands.get(figure['key'])
        if required is None:
            continue
        if any(sid not in fields for sid in required):
            figure['notice'] = '图表指标来源已失效或未授权，请核对报告来源'
            continue
        definition = definitions.get(figure['key'])
        if definition is not None:
            try:
                unit_notices(definition, fields, reject=True)
            except HTTPException as exc:
                figure['notice'] = str(exc.detail)
                continue
        sources = list(dict.fromkeys(' · '.join(str(fields[sid][key]) for key in ('source_file', 'sheet')
                                               if fields[sid].get(key)) for sid in required))
        if any(sources):
            figure['source'] = ' / '.join(source for source in sources if source)


def download_headers(view):
    from tin.ppt.report_deck import filename
    # An ASCII fallback and RFC 5987 name cover both old and modern clients.
    return {'Content-Disposition': 'attachment; filename="research-report.pptx"; filename*=UTF-8\'\''
            + quote(filename(view), safe=''), 'Cache-Control': 'no-store'}


@router.post('/{day}/{period}/ppt')
async def export_report_ppt(day: str, period: str, request: Request):
    scope = require_scope()  # Reading a report is sufficient; no editor gate.
    validate_period(day, period)
    view = await run_in_threadpool(read_report_view, scope, day, period)
    figures = view.get('report_figures') or []
    maximum = max(256 * 1024, len(figures) * MAX_PNG_BYTES + 256 * 1024)
    length = request.headers.get('content-length')
    if length and length.isdecimal() and int(length) > maximum:
        raise HTTPException(413, '上传图表总大小超出报告所需范围')
    if not request.headers.get('content-type', '').lower().startswith('multipart/form-data'):
        raise HTTPException(422, '请使用图表PNG表单生成PPT')
    try:
        async with request.form(max_files=max(1, len(figures) + 1),
                                max_fields=max(16, len(figures) + 4), max_part_size=64 * 1024) as form:
            if set(form) - {'options', 'revision', 'keys', 'images', 'failures'}:
                raise HTTPException(422, '导出表单含有不支持的字段')
            try:
                options = PptOptions.model_validate(_json_field(form, 'options'))
                failures = [Failure.model_validate_json(value) if isinstance(value, str) else None
                            for value in form.getlist('failures')]
            except ValidationError:
                raise HTTPException(422, '导出选项或图表失败记录无效') from None
            revision = _json_field(form, 'revision')
            if revision is not None and (type(revision) is not int or revision < 1):
                raise HTTPException(422, '报告草稿版本无效')
            if revision != (view.get('manual_report') or {}).get('revision'):
                raise HTTPException(409, CHANGED)
            selected = options.sections.model_dump()
            for key, enabled in selected.items():
                if enabled and not view['panels'][key]['visible']:
                    raise HTTPException(409, CHANGED)
            # Options choose text sections; the chart collection always remains
            # the complete collection shown in the saved report, in its order.
            expected = [row['key'] for row in figures] if selected['dashboard'] else []
            keys = _json_field(form, 'keys')
            if not isinstance(keys, list) or keys != expected:
                raise HTTPException(409, CHANGED)
            uploads = form.getlist('images')
            if any(not isinstance(item, UploadFile) for item in uploads):
                raise HTTPException(422, '图表文件格式无效')
            if any(row is None or not row.key or len(row.reason) > 1000 for row in failures):
                raise HTTPException(422, '图表失败记录无效')
            failed_keys = [row.key for row in failures]
            if len(set(failed_keys)) != len(failed_keys) or set(failed_keys) - set(expected):
                raise HTTPException(422, '图表失败记录与报告清单不符')
            if view.get('frozen'):
                if uploads or failures:
                    raise HTTPException(422, '历史归档不重新绘图，请刷新后导出')
            elif len(uploads) + len(failed_keys) != len(expected):
                raise HTTPException(422, 'PNG张数与报告图表清单不符')
            images, upload_index = {}, 0
            view = deepcopy(view)
            if not view.get('frozen'):
                if selected['dashboard']:
                    await run_in_threadpool(revalidate_figures, scope, view.get('report_figures') or [])
                for figure in view.get('report_figures') or []:
                    if figure['key'] not in expected:
                        continue
                    if figure['key'] in failed_keys:
                        # Client prose is never inserted into the document.
                        figure['notice'] = figure.get('notice') or '浏览器未能读取或绘制此图，请刷新报告后重试'
                        images[figure['key']] = None
                    else:
                        upload = uploads[upload_index]
                        upload_index += 1
                        data = await upload.read(MAX_PNG_BYTES + 1)
                        await run_in_threadpool(validate_png, data)
                        # A withdrawn source may keep its visible placeholder,
                        # but never embed a stale client bitmap after ACL failure.
                        images[figure['key']] = None if figure.get('notice') else data
            from tin.ppt.report_deck import build_report_deck
            deck = await run_in_threadpool(build_report_deck, view, images, options.model_dump())
    except HTTPException as exc:
        if exc.status_code == 400:
            raise HTTPException(422, '上传表单无效或图表张数超限') from None
        raise
    return Response(deck, media_type=MIME, headers=download_headers(view))
