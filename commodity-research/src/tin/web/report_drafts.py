"""Personal, dated report prose; observed facts never come from this input."""
from copy import deepcopy
from datetime import date, datetime, timezone
import json
import re
from typing import Literal
from uuid import NAMESPACE_URL, uuid5

from fastapi import APIRouter, HTTPException, Request
from fastapi.encoders import jsonable_encoder
from pydantic import BaseModel, ConfigDict, Field, StrictBool, ValidationError, field_validator
from sqlalchemy import update
from sqlalchemy.exc import IntegrityError
from starlette.concurrency import run_in_threadpool

from tin.integration.workbench_scope import artifact_session, current_workbench
from tin.models.workbench import WorkbenchArtifact
from tin.web.workbench_artifacts import artifact_query, record_revision

FORMAT = 'research-report-draft.v1'
SECTION_LABELS = {'summary': '摘要', 'research': '研判策略', 'dashboard': '数据展板', 'watch': '待关注'}
MAX_BYTES = 128 * 1024
router = APIRouter(prefix='/api/sn/report-drafts')


class _Input(BaseModel):
    model_config = ConfigDict(extra='forbid')


class ReportSection(_Input):
    visible: StrictBool = True
    text: str = Field(default='', max_length=4000)


class ReportSections(_Input):
    summary: ReportSection = Field(default_factory=ReportSection)
    research: ReportSection = Field(default_factory=ReportSection)
    dashboard: ReportSection = Field(default_factory=ReportSection)
    watch: ReportSection = Field(default_factory=ReportSection)


class ReportFigure(_Input):
    series_id: str = Field(min_length=1, max_length=128)
    caption: str = Field(default='', max_length=600)


class ReportReference(_Input):
    kind: Literal['judgment', 'strategy']
    id: str = Field(min_length=1, max_length=128)
    revision: int = Field(ge=1, strict=True)
    section: Literal['summary', 'research', 'dashboard', 'watch'] = 'research'


class ReportDraft(_Input):
    format: Literal['research-report-draft.v1'] = FORMAT
    period: Literal['daily', 'weekly']
    date: str
    title: str = Field(min_length=1, max_length=120)
    sections: ReportSections = Field(default_factory=ReportSections)
    figures: list[ReportFigure] = Field(default_factory=list, max_length=6)
    references: list[ReportReference] = Field(default_factory=list, max_length=8)

    @field_validator('date')
    @classmethod
    def exact_date(cls, value):
        if date.fromisoformat(value).isoformat() != value:
            raise ValueError('Report date must be YYYY-MM-DD')
        return value

    @field_validator('title')
    @classmethod
    def nonempty_title(cls, value):
        if not value.strip():
            raise ValueError('Report title cannot be empty')
        return value.strip()


class DraftInput(_Input):
    draft: ReportDraft
    revision: int | None = Field(default=None, ge=1, strict=True)


def require_scope(*, write=False):
    scope = current_workbench.get()
    if scope is None:
        raise HTTPException(409, '请从已授权研究工作台打开报告')
    if scope.restricted or 'reports' not in scope.view_modules:
        raise HTTPException(403, '此授权不开放个人报告草稿')
    if write and not scope.can_edit:
        raise HTTPException(403, '当前账号没有研究台编辑权限')
    return scope


def validate_period(day, period):
    try:
        if not isinstance(day, str) or date.fromisoformat(day).isoformat() != day:
            raise ValueError
    except (ValueError, TypeError):
        raise HTTPException(400, '报告日期应为YYYY-MM-DD') from None
    if period not in {'daily', 'weekly'}:
        raise HTTPException(400, '报告周期应为daily或weekly')
    return date.fromisoformat(day)


def report_id(scope, day, period):
    # One managed report per verified person/workbench/date/period. The primary
    # key also makes concurrent first saves conflict instead of duplicating.
    return str(uuid5(NAMESPACE_URL, json.dumps([scope.identity.tenant_id,
        scope.workbench_id, scope.identity.user_id, day, period], separators=(',', ':'))))


def resolve_saved_reference(session, scope, kind, identifier, revision):
    from tin.web.research_references import resolve_reference
    return resolve_reference(session, scope, kind, identifier, revision)


def freeze_references(session, scope, references):
    result, seen = [], set()
    for item in references:
        item = item.model_dump() if isinstance(item, ReportReference) else item
        key = (item['kind'], item['id'], item['revision'], item['section'])
        if key in seen:
            raise HTTPException(422, '报告引用重复，请保留一次')
        seen.add(key)
        actual = resolve_saved_reference(session, scope, item['kind'], item['id'], item['revision'])
        if (actual.get('kind') != item['kind'] or str(actual.get('id')) != item['id']
                or actual.get('revision') != item['revision']):
            raise HTTPException(409, '引用版本已变化，未替换为最新版本')
        result.append({**{key: deepcopy(actual.get(key)) for key in
            ('kind', 'id', 'revision', 'title', 'status', 'resource_type', 'summary', 'body_text',
             'body_text_truncated', 'series_ids', 'source_ids')}, 'section': item['section']})
    return result


def bound_series(session, scope, references=()):
    from tin.web.report_figures import saved_bound_series
    values = saved_bound_series(session, scope)
    for item in references:
        values.extend(item.get('series_ids') or [])
    return list(dict.fromkeys(values))


def current_source_version(scope, identifiers, day):
    if scope.scope_version == 2:
        from tin.integration.page_facts import bounded_facts
        return bounded_facts(scope, identifiers, 'reports', day=day.isoformat(), points=1,
                             direct_only=True)['version']
    from tin.integration.kb_facts import current_sources, current_version
    return current_version(scope, current_sources(scope))


def _stored(row):
    try:
        data = json.loads(row.body)
        user = ReportDraft.model_validate({key: data[key] for key in ReportDraft.model_fields})
        if not isinstance(data.get('reference_snapshots'), list):
            raise ValueError
        version = data.get('source_revision')
        if not isinstance(version, str) or not re.fullmatch('[a-f0-9]{64}', version):
            raise ValueError
        if not isinstance(data.get('workbench_id'), str):
            raise ValueError
        return {**user.model_dump(mode='json'), 'source_revision': version, 'workbench_id': data['workbench_id'],
                'reference_snapshots': data['reference_snapshots']}
    except (ValueError, TypeError, KeyError, ValidationError):
        raise HTTPException(409, '已保存报告格式不能读取，未覆盖原内容') from None


def _row(session, scope, identifier):
    row = session.scalar(artifact_query(scope, 'briefs').where(WorkbenchArtifact.id == identifier))
    if row is None:
        return None
    saved = _stored(row)
    if saved['workbench_id'] != scope.workbench_id or row.id != report_id(scope, saved['date'], saved['period']):
        return None
    return row


def serialize_report(session, scope, row, *, strict=False):
    saved = _stored(row)
    # An embedded quotation is not a permanent grant. Resolve exact revisions
    # again; unavailable quotations never enter HTML, JSON audit or exports.
    snapshots, unavailable = [], []
    frozen = {(item.get('kind'), str(item.get('id')), item.get('revision'), item.get('section')): item
              for item in saved['reference_snapshots'] if isinstance(item, dict)}
    for reference in saved['references']:
        try:
            actual = freeze_references(session, scope, [reference])[0]
            previous = frozen.get((reference['kind'], reference['id'], reference['revision'], reference['section']))
            stable = ('kind', 'id', 'revision', 'title', 'resource_type', 'summary', 'body_text',
                      'body_text_truncated', 'series_ids', 'source_ids', 'section')
            if previous is None or any(previous.get(key) != actual.get(key) for key in stable):
                raise HTTPException(409, '引用内容与已保存版本不一致，请重新核对')
            # Preserve the saved quotation and status after checking its current
            # grant and immutable revision, instead of silently replacing it.
            snapshots.append({key: deepcopy(previous.get(key)) for key in (*stable, 'status')})
        except HTTPException as error:
            if error.status_code not in {403, 404, 409}:
                raise
            if strict:
                raise HTTPException(409, '报告引用已不可读，请先在报告中调整引用再导出') from None
            unavailable.append({**reference, 'notice': '引用已删除、版本不可读或权限已变化'})
    saved['reference_snapshots'] = snapshots
    return jsonable_encoder({'id': row.id, 'revision': row.revision, 'draft': saved,
        'updated_at': row.updated_at, 'unavailable_references': unavailable, 'can_write': scope.can_edit})


def load_report(session, scope, day, period, *, strict=False):
    validate_period(day, period)
    if scope is None or scope.restricted or 'reports' not in scope.view_modules:
        return None
    row = _row(session, scope, report_id(scope, day, period))
    return serialize_report(session, scope, row, strict=strict) if row else None


def attach_saved_report(session, scope, payload, *, strict=False):
    value = deepcopy(payload)
    period = value.get('period') or {'kind': 'daily', 'end': value.get('as_of')}
    saved = load_report(session, scope, period['end'], period['kind'], strict=strict)
    if saved is not None:
        value['manual_report'] = saved
    return value


def save_report(scope, body, identifier=None):
    from tin.web.report_figures import validate_selection
    day = validate_period(body.draft.date, body.draft.period)
    expected_id = report_id(scope, body.draft.date, body.draft.period)
    with artifact_session(scope) as session:
        row = _row(session, scope, identifier or expected_id)
        if identifier:
            if row is None:
                raise HTTPException(404, '报告不存在或不属于当前账号和研究台')
            if identifier != expected_id:
                raise HTTPException(409, '已有报告不能改成其他日期或周期')
            if body.revision is None:
                raise HTTPException(400, '请携带当前版本，避免覆盖其他修改')
            if row.revision != body.revision:
                raise HTTPException(409, '报告已有新版本，请重新打开后继续修改')
        elif row is not None:
            raise HTTPException(409, '本期已有个人报告，请重新打开后继续修改')
        snapshots = freeze_references(session, scope, body.draft.references)
        allowed = bound_series(session, scope, snapshots)
        chosen = validate_selection([item.series_id for item in body.draft.figures], allowed)
        version = current_source_version(scope, chosen, day)
        if not isinstance(version, str) or not re.fullmatch('[a-f0-9]{64}', version):
            raise HTTPException(503, '报告来源版本无法核验，未保存')
        saved = {**body.draft.model_dump(mode='json'), 'source_revision': version, 'workbench_id': scope.workbench_id,
                 'reference_snapshots': snapshots}
        text = json.dumps(saved, ensure_ascii=False, separators=(',', ':'), allow_nan=False)
        if len(text) > 60000 or len(text.encode()) > MAX_BYTES:
            raise HTTPException(413, '报告与引用超过保存容量，请缩短正文或减少引用')
        now = datetime.now(timezone.utc)
        if identifier:
            result = session.execute(update(WorkbenchArtifact).where(WorkbenchArtifact.id == identifier,
                WorkbenchArtifact.actor == scope.identity.user_id, WorkbenchArtifact.module == 'briefs',
                WorkbenchArtifact.revision == body.revision).values(title=body.draft.title, body=text,
                revision=body.revision + 1, updated_at=now))
            if result.rowcount != 1:
                raise HTTPException(409, '报告已有新版本，请重新打开后继续修改')
            session.refresh(row)
        else:
            row = WorkbenchArtifact(id=expected_id, actor=scope.identity.user_id, module='briefs',
                title=body.draft.title, body=text, status='draft', source_ids=[], source_snapshots=[],
                revision=1, created_at=now, updated_at=now)
            session.add(row)
            try:
                session.flush()
            except IntegrityError:
                session.rollback()
                raise HTTPException(409, '本期报告刚刚被保存，请重新打开') from None
            from tin.web.research_references import remember_record_scope
            remember_record_scope(session, 'artifact', row.id)
        record_revision(session, row, scope.identity.user_id)
        session.commit()
        return serialize_report(session, scope, row)


def _no_duplicates(items):
    result = {}
    for key, value in items:
        if key in result:
            raise ValueError('Duplicate JSON properties')
        result[key] = value
    return result


async def read_input(request):
    raw = bytearray()
    async for part in request.stream():
        raw.extend(part)
        if len(raw) > MAX_BYTES:
            raise HTTPException(413, '报告请求超过128KiB，请缩短正文')
    try:
        def invalid(_value):
            raise ValueError('Nonfinite JSON')
        return DraftInput.model_validate(json.loads(raw.decode('utf-8-sig'),
            object_pairs_hook=_no_duplicates, parse_constant=invalid))
    except (ValueError, TypeError, UnicodeError, ValidationError, RecursionError):
        raise HTTPException(422, '报告格式无效；正文可留空，不能改写原始事实或来源版本') from None


@router.get('')
def get_report(date: str, period: str = 'daily'):
    scope = require_scope()
    validate_period(date, period)
    with artifact_session(scope) as session:
        return load_report(session, scope, date, period) or {'id': None, 'revision': None,
            'draft': None, 'can_write': scope.can_edit, 'unavailable_references': []}


@router.post('', status_code=201)
async def create_report(request: Request):
    scope = require_scope(write=True)
    body = await read_input(request)
    return await run_in_threadpool(save_report, scope, body)


@router.patch('/{identifier}')
async def update_report(identifier: str, request: Request):
    scope = require_scope(write=True)
    body = await read_input(request)
    return await run_in_threadpool(save_report, scope, body, identifier)
