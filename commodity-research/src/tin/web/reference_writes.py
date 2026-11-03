"""Fence explicit authored references in the transaction publishing their owner."""
import json

from fastapi import HTTPException
from sqlalchemy import update
from sqlalchemy.exc import OperationalError

from tin.models import Judgment
from tin.web.research_references import declared_references, resolve_judgment_condition, resolve_reference


def fence_reference(session, scope, reference, seen=None):
    if hasattr(reference, 'model_dump'):
        reference = reference.model_dump()
    if not isinstance(reference, dict):
        raise HTTPException(422, '研究引用格式无效')
    kind, identifier, revision = (reference.get(key) for key in ('kind', 'id', 'revision'))
    if (not isinstance(kind, str) or kind not in {'judgment', 'strategy', 'judgment_condition'}
            or type(revision) is not int or revision < 1):
        raise HTTPException(422, '请明确选择引用记录及其版本')
    actual = None
    if kind == 'judgment_condition':
        # Check every rule pin before deduplicating the judgment write lock;
        # two different rules in the same version cannot bypass validation.
        actual = resolve_judgment_condition(session, scope, reference)
        kind, identifier = 'judgment', actual['id']
    seen = set() if seen is None else seen
    key = (kind, str(identifier), revision)
    if key in seen:
        return
    if len(seen) >= 32:
        raise HTTPException(413, '研究引用层级或数量过多，请精简引用')
    seen.add(key)
    actual = actual or resolve_reference(session, scope, kind, identifier, revision,
        include_declared=kind == 'strategy')
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
    references = actual['declared_references'] if 'declared_references' in actual else declared_references(actual.get('payload'))
    for linked in references:
        fence_reference(session, scope, linked, seen)


def fence_artifact_references(session, scope, module, text):
    if module not in {'strategies', 'briefs'}:
        return
    try:
        data = json.loads(text)
    except (ValueError, TypeError, RecursionError):
        return  # Ordinary prose does not declare structured references.
    if not isinstance(data, dict):
        return
    if module == 'strategies' and (data.get('format') == 'research-strategy-card.v1' or 'citations' in data):
        seen = set()
        for item in declared_references(data):
            fence_reference(session, scope, item, seen)
    elif module == 'briefs' and data.get('format') == 'research-report-draft.v1':
        seen = set()
        for item in data.get('references') or []:
            fence_reference(session, scope, item, seen)
