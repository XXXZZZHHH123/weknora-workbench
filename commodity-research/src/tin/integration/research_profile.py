"""Use the researcher's explicit saved product identity within one workbench."""
from sqlalchemy import select

from tin.config import settings
from tin.models import Judgment
from tin.ownership import scope_query


def research_variety(session, *, scope=None):
    if not session.info.get('workbench_id'):
        return settings.variety
    from tin.integration.workbench_scope import current_workbench
    scope = scope or current_workbench.get()
    if scope is not None:
        from tin.judgments.service import authorized_judgments
        rows = authorized_judgments(session, scope)
        return rows[0].variety if rows else settings.variety
    row = session.scalar(scope_query(session, select(Judgment.variety)
        .order_by(Judgment.created_at.desc(), Judgment.id.desc()).limit(1), Judgment))
    return row or settings.variety
