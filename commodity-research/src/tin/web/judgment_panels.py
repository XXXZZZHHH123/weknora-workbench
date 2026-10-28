"""Named views of saved judgments; selecting a view never activates a rule."""
from datetime import datetime, timezone
import json
from types import SimpleNamespace
from uuid import NAMESPACE_URL, UUID, uuid5

from fastapi import APIRouter, HTTPException, Query, Request
from pydantic import BaseModel, ConfigDict, Field, ValidationError
from sqlalchemy import select, update
from sqlalchemy.exc import IntegrityError

from tin.integration.workbench_scope import artifact_session, current_workbench, require_shared_write
from tin.judgments.service import _refs, to_payload
from tin.models import AuditLog, Judgment
from tin.models.workbench import WorkbenchArtifact
from tin.ownership import scope_query
from tin.web.workbench_artifacts import record_revision

FORMAT = 'judgment-panel.v1'
router = APIRouter(prefix='/api/sn/judgment/panels')


class PanelInput(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    judgment_id: int = Field(ge=1, le=2**63 - 1)
    version: int = Field(ge=1, le=2**31 - 1)
    title: str = Field(min_length=1, max_length=120)


class RenameInput(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    title: str = Field(min_length=1, max_length=120)
    revision: int = Field(ge=1, le=2**31 - 1)


def panel_scope(write=False):
    scope = current_workbench.get()
    if scope is None or not scope.authorization:
        raise HTTPException(409, '请从当前研究工作台管理研判面板')
    if scope.restricted or 'judgments' not in scope.view_modules:
        raise HTTPException(403, '当前授权不开放个人研判面板')
    if write:
        if not scope.identity.can_write:
            raise HTTPException(403, '当前角色不能修改研判面板')
        require_shared_write(scope)
    return scope


def selected_judgment(session, judgment_id, *, scope=None):
    if type(judgment_id) is not int or not 1 <= judgment_id <= 2**63 - 1:
        raise HTTPException(422, '研判编号需要明确的正整数')
    scope = scope or current_workbench.get()
    if session.info.get('workbench_id') and scope is None:
        raise HTTPException(403, '缺少工作台授权')
    if scope is not None and (scope.restricted or 'judgments' not in scope.view_modules):
        raise HTTPException(403, '当前授权不开放个人研判面板')
    row = session.scalar(scope_query(session, select(Judgment).where(Judgment.id == judgment_id), Judgment))
    if row is None:
        raise HTTPException(404, '研判不存在或无权访问')
    if scope is not None:
        from tin.web.research_references import record_in_workbench
        if not record_in_workbench(session, scope, 'judgment', str(row.id), record=row):
            raise HTTPException(404, '研判不属于当前工作台')
    return row


def judgment_series_ids(row):
    try:
        return list(dict.fromkeys(identifier for _, _, identifier in _refs(to_payload(row))))
    except (ValidationError, ValueError, TypeError):
        raise HTTPException(409, '此研判的绑定结构不完整，请核对后重新导入') from None


def panel_identifier(scope, judgment_id):
    return str(uuid5(NAMESPACE_URL, 'research-judgment-panel:' + scope.workbench_id + ':'
                     + scope.identity.user_id + ':' + str(judgment_id)))


def _metadata(row, scope):
    try:
        body = json.loads(row.body)
    except (ValueError, TypeError):
        return None
    if (not isinstance(body, dict) or body.get('format') != FORMAT
            or body.get('workbench_id') != scope.workbench_id
            or type(body.get('judgment_id')) is not int or type(body.get('version')) is not int):
        return None
    return body


def _view(judgment, named=None):
    return {'id': named.id if named is not None else None, 'judgment_id': judgment.id,
            'version': judgment.version, 'variety': judgment.variety, 'status': judgment.status,
            'title': named.title if named is not None else f'{judgment.variety} 研判 · v{judgment.version}',
            'author': judgment.author, 'written_at': judgment.written_at.isoformat(),
            'revision': named.revision if named is not None else None,
            'named': named is not None}


def register_panel(session, scope, judgment, title=''):
    """The immutable judgment remains the single payload; the panel stores a pin."""
    if scope is None:
        return None
    identifier = panel_identifier(scope, judgment.id)
    if session.get(WorkbenchArtifact, identifier) is not None:
        raise HTTPException(409, '此研判已在面板中，请直接切换或修改面板名称')
    title = title.strip() or f'{judgment.variety} 研判 · v{judgment.version}'
    if len(title) > 120:
        raise HTTPException(422, '面板名称最多120字')
    now = datetime.now(timezone.utc)
    row = WorkbenchArtifact(id=identifier, module='judgments', actor=scope.identity.user_id,
        title=title, body=json.dumps({'format': FORMAT, 'workbench_id': scope.workbench_id,
            'judgment_id': judgment.id, 'version': judgment.version}, ensure_ascii=False),
        status='draft', source_ids=[], source_snapshots=[], revision=1, created_at=now, updated_at=now)
    session.add(row)
    session.flush()
    record_revision(session, row, scope.identity.user_id)
    session.add(AuditLog(at=now, actor=scope.identity.user_id, action='研判面板引用',
        target_type='judgment_panel', target_id=row.id,
        detail={'judgment_id': judgment.id, 'version': judgment.version, 'workbench_id': scope.workbench_id}))
    return _view(judgment, row)


def judgment_directory(session, scope):
    """Bounded metadata only; apply legacy ownership proof before pagination."""
    from tin.web.research_references import record_in_workbench
    query = scope_query(session, select(Judgment), Judgment)
    records = list(session.execute(query.with_only_columns(Judgment.id, Judgment.researcher_id,
        Judgment.variety, Judgment.version, Judgment.status, Judgment.author, Judgment.written_at)
        .order_by(Judgment.created_at.desc(), Judgment.id.desc()).limit(1001)).mappings())
    if len(records) > 1000:
        raise HTTPException(413, '研判目录超过1000项，请先整理历史记录')
    return [SimpleNamespace(**record) for record in records if record_in_workbench(
        session, scope, 'judgment', str(record['id']), record=record)]


def list_panels(session, scope, *, limit=50, offset=0):
    items = []
    for judgment in judgment_directory(session, scope):
        named = session.get(WorkbenchArtifact, panel_identifier(scope, judgment.id))
        if named is not None and (named.module != 'judgments' or named.actor != scope.identity.user_id
                or _metadata(named, scope) != {'format': FORMAT, 'workbench_id': scope.workbench_id,
                    'judgment_id': judgment.id, 'version': judgment.version}):
            named = None
        items.append(_view(judgment, named))
    count = len(items)
    return {'items': items[offset:offset+limit], 'total': count, 'offset': offset,
            'has_more': offset + limit < count,
            'can_write': scope.identity.can_write and scope.can_edit,
            'notice': '面板切换只改变查看对象；研判生效、信号和交易状态保持各自的审核流程。'}


@router.get('')
def panels(limit: int = Query(50, ge=1, le=100), offset: int = Query(0, ge=0, le=100000)):
    scope = panel_scope()
    with artifact_session(scope) as session:
        return list_panels(session, scope, limit=limit, offset=offset)


@router.post('', status_code=201)
async def add_panel(request: Request):
    from tin.web.judgment_generation import read_body
    scope = panel_scope(write=True)
    body = await read_body(request, PanelInput)
    with artifact_session(scope) as session:
        judgment = selected_judgment(session, body.judgment_id, scope=scope)
        if judgment.version != body.version:
            raise HTTPException(409, '引用研判的版本不一致，请重新选择')
        if not body.title.strip():
            raise HTTPException(422, '请填写面板名称')
        try:
            result = register_panel(session, scope, judgment, body.title)
            session.commit()
            return result
        except IntegrityError:
            session.rollback()
            raise HTTPException(409, '此研判面板已建立，请刷新后查看') from None


@router.patch('/{panel_id}')
async def rename_panel(panel_id: UUID, request: Request):
    from tin.web.judgment_generation import read_body
    scope = panel_scope(write=True)
    body = await read_body(request, RenameInput)
    if not body.title.strip():
        raise HTTPException(422, '请填写面板名称')
    with artifact_session(scope) as session:
        row = session.scalar(select(WorkbenchArtifact).where(WorkbenchArtifact.id == str(panel_id),
            WorkbenchArtifact.module == 'judgments', WorkbenchArtifact.actor == scope.identity.user_id))
        metadata = _metadata(row, scope) if row is not None else None
        if metadata is None:
            raise HTTPException(404, '研判面板不存在或无权访问')
        judgment = selected_judgment(session, metadata['judgment_id'], scope=scope)
        if row.id != panel_identifier(scope, judgment.id):
            raise HTTPException(404, '研判面板不存在或无权访问')
        if judgment.version != metadata['version']:
            raise HTTPException(409, '研判面板版本不一致，请重新选择')
        changed = session.execute(update(WorkbenchArtifact).where(WorkbenchArtifact.id == row.id,
            WorkbenchArtifact.revision == body.revision).values(title=body.title.strip(),
                revision=body.revision + 1, updated_at=datetime.now(timezone.utc)))
        if changed.rowcount != 1:
            session.rollback()
            raise HTTPException(409, '面板名称已更新，请刷新后再修改')
        session.refresh(row)
        record_revision(session, row, scope.identity.user_id)
        session.add(AuditLog(at=row.updated_at, actor=scope.identity.user_id, action='研判面板命名',
            target_type='judgment_panel', target_id=row.id, detail={'revision': row.revision,
                'workbench_id': scope.workbench_id, 'judgment_id': judgment.id}))
        session.commit()
        return _view(judgment, row)
