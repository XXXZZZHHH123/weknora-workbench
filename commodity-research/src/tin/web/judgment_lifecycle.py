"""Explicit version deletion keeps authored snapshots and monotonic save counters."""
from datetime import datetime, timezone
import json

from fastapi import APIRouter, HTTPException, Path, Request
from pydantic import BaseModel, ConfigDict, Field, StrictBool
from sqlalchemy import select, update
from sqlalchemy.exc import IntegrityError, OperationalError

from tin.integration.workbench_scope import artifact_session
from tin.models import AuditLog, Judgment, Strategy
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision
from tin.web.judgment_panels import FORMAT, _metadata, panel_identifier, panel_scope, selected_judgment
from tin.web.workbench_artifacts import record_revision

router = APIRouter(prefix='/api/sn/judgment/records')


class DeleteRequest(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    version: int = Field(ge=1, le=2**31 - 1)
    revision: int = Field(ge=0, le=2**31 - 1)
    confirmed: StrictBool


def delete_revision(session, scope, judgment):
    panel = session.get(WorkbenchArtifact, panel_identifier(scope, judgment.id))
    if panel is None:
        return 0
    if (panel.module != 'judgments' or panel.actor != scope.identity.user_id
            or _metadata(panel, scope) != {'format': FORMAT, 'workbench_id': scope.workbench_id,
                'judgment_id': judgment.id, 'version': judgment.version}):
        raise HTTPException(409, '研判管理记录不一致，请刷新后核对')
    return panel.revision


def _points_to(value, identifier, version, version_field='revision'):
    return (isinstance(value, dict) and type(value.get(version_field)) is int
        and value[version_field] == version and str(value.get('id', '')) == str(identifier))


def _references(body, identifier, version):
    if not isinstance(body, dict):
        return False
    if _points_to(body.get('judgment_reference'), identifier, version):
        return True
    references = body.get('references')
    if isinstance(references, list) and any(isinstance(item, dict) and item.get('kind') == 'judgment'
            and _points_to(item, identifier, version) for item in references):
        return True
    context = body.get('source_context')
    return isinstance(context, dict) and _points_to(context.get('research'), identifier, version, 'version')


def _decode(body):
    try:
        return json.loads(body) if isinstance(body, str) else body
    except (ValueError, TypeError, RecursionError):
        return None


def ensure_unreferenced(session, scope, judgment):
    # The write fence is already held. A concurrent authored reference must
    # re-read the judgment after this transaction, never publish a stale pin.
    statements = [
        select(WorkbenchArtifact.body).where(WorkbenchArtifact.actor == scope.identity.user_id,
            WorkbenchArtifact.module.in_(['strategies', 'briefs'])),
        select(WorkbenchArtifactRevision.body).join(WorkbenchArtifact,
            WorkbenchArtifact.id == WorkbenchArtifactRevision.artifact_id).where(
                WorkbenchArtifact.actor == scope.identity.user_id,
                WorkbenchArtifact.module.in_(['strategies', 'briefs'])),
        select(Strategy.plan).where(Strategy.researcher_id == judgment.researcher_id),
    ]
    examined = 0
    for statement in statements:
        for body in session.scalars(statement.execution_options(yield_per=64)):
            examined += 1
            if examined > 10000:
                raise HTTPException(413, '引用历史较多，请先整理策略和报告后再删除研判')
            if _references(_decode(body), judgment.id, judgment.version):
                raise HTTPException(409, '策略或报告（含历史版本）正在引用此研判，请先处理引用后再删除')


@router.delete('/{judgment_id}')
async def delete_judgment(request: Request, judgment_id: int = Path(ge=1, le=2**63 - 1)):
    from tin.web.judgment_generation import read_body
    scope = panel_scope(write=True)
    body = await read_body(request, DeleteRequest)
    if body.confirmed is not True:
        raise HTTPException(422, '请明确确认删除此研判版本')
    with artifact_session(scope) as session:
        try:
            judgment = selected_judgment(session, judgment_id, scope=scope)
            if judgment.version != body.version or delete_revision(session, scope, judgment) != body.revision:
                raise HTTPException(409, '研判或名称已经更新，请刷新后重新确认删除')
            old_status = judgment.status
            changed = session.execute(update(Judgment).where(Judgment.id == judgment.id,
                Judgment.researcher_id == judgment.researcher_id, Judgment.version == body.version,
                Judgment.status == old_status).values(status='已删除'),
                execution_options={'synchronize_session': False})
            if changed.rowcount != 1:
                raise HTTPException(409, '研判已经更新或删除，请刷新后继续')
            ensure_unreferenced(session, scope, judgment)
            now = datetime.now(timezone.utc)
            identifier = panel_identifier(scope, judgment.id)
            panel = session.get(WorkbenchArtifact, identifier)
            if panel is None:
                panel = WorkbenchArtifact(id=identifier, module='judgments', actor=scope.identity.user_id,
                    title=f'{judgment.variety} 研判', body=json.dumps({'format': FORMAT,
                        'workbench_id': scope.workbench_id, 'judgment_id': judgment.id,
                        'version': judgment.version}, ensure_ascii=False), status='deleted',
                    source_ids=[], source_snapshots=[], revision=1, created_at=now, updated_at=now)
                session.add(panel)
                session.flush()
            else:
                changed = session.execute(update(WorkbenchArtifact).where(WorkbenchArtifact.id == identifier,
                    WorkbenchArtifact.revision == body.revision).values(status='deleted',
                        revision=body.revision + 1, updated_at=now),
                        execution_options={'synchronize_session': False})
                if changed.rowcount != 1:
                    raise HTTPException(409, '研判名称已经更新，请刷新后重新确认删除')
                session.refresh(panel)
            record_revision(session, panel, scope.identity.user_id)
            session.add(AuditLog(at=now, actor=scope.identity.user_id, action='研判删除',
                target_type='judgment', target_id=str(judgment.id), detail={
                    'workbench_id': scope.workbench_id, 'judgment_id': judgment.id,
                    'version': judgment.version, 'previous_status': old_status, 'revision': panel.revision}))
            session.commit()
            return {'deleted': True, 'id': judgment.id, 'version': judgment.version,
                'revision': panel.revision}
        except (IntegrityError, OperationalError):
            session.rollback()
            raise HTTPException(409, '研判正在更新，请刷新后重新确认删除') from None
