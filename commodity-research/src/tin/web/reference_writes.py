"""Fence explicit authored references in the transaction publishing their owner."""
import json

from fastapi import HTTPException
from sqlalchemy import update
from sqlalchemy.exc import OperationalError

from tin.models import Judgment
from tin.web.research_references import resolve_reference


def fence_reference(session, scope, reference, seen=None):
    if hasattr(reference, 'model_dump'):
        reference = reference.model_dump()
    if not isinstance(reference, dict):
        raise HTTPException(422, '研究引用格式无效')
    kind, identifier, revision = (reference.get(key) for key in ('kind', 'id', 'revision'))
    if kind not in {'judgment', 'strategy'} or type(revision) is not int or revision < 1:
        raise HTTPException(422, '请明确选择引用记录及其版本')
    seen = set() if seen is None else seen
    key = (kind, str(identifier), revision)
    if key in seen:
        return
    if len(seen) >= 32:
        raise HTTPException(413, '研究引用层级或数量过多，请精简引用')
    seen.add(key)
    actual = resolve_reference(session, scope, kind, identifier, revision)
    if kind == 'judgment':
        # A no-op UPDATE serializes this publication with the delete CAS. A
        # previously validated snapshot cannot publish after its source dies.
        try:
            changed = session.execute(update(Judgment).where(Judgment.id == int(identifier),
                Judgment.version == revision, Judgment.status != '已删除').values(status=Judgment.status),
                execution_options={'synchronize_session': False})
        except OperationalError:
            raise HTTPException(409, '引用研判正在更新，请刷新后重新保存') from None
        if changed.rowcount != 1:
            raise HTTPException(409, '引用研判已更新或删除，请重新选择')
        return
    payload = actual.get('payload') or {}
    linked = payload.get('judgment_reference')
    if linked is not None:
        fence_reference(session, scope, linked, seen)
    linked = payload.get('reference')
    if linked is not None:
        fence_reference(session, scope, linked, seen)
    context = payload.get('source_context')
    research = context.get('research') if isinstance(context, dict) else None
    if isinstance(research, dict) and research.get('id') is not None:
        fence_reference(session, scope, {'kind': 'judgment', 'id': str(research['id']),
            'revision': research.get('version')}, seen)


def fence_artifact_references(session, scope, module, text):
    if module not in {'strategies', 'briefs'}:
        return
    try:
        data = json.loads(text)
    except (ValueError, TypeError, RecursionError):
        return  # Ordinary prose does not declare structured references.
    if not isinstance(data, dict):
        return
    if module == 'strategies' and data.get('format') == 'research-strategy-card.v1':
        seen = set()
        for key in ('judgment_reference', 'reference'):
            if data.get(key) is not None:
                fence_reference(session, scope, data[key], seen)
    elif module == 'briefs' and data.get('format') == 'research-report-draft.v1':
        seen = set()
        for item in data.get('references') or []:
            fence_reference(session, scope, item, seen)
