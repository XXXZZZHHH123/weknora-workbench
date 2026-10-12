"""Personal research ownership derived exclusively from verified session identity.

Tenant isolation is provided by the database session. With no verified identity,
the standalone CLI retains its single-researcher behavior; HTTP sessions must be
authenticated before they reach this module.
"""
from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from tin.models import Judgment, Researcher


def current_researcher(session: Session) -> Researcher | None:
    user_id = session.info.get("weknora_user_id")
    if not user_id:
        return None
    row = session.scalar(select(Researcher).where(Researcher.weknora_user_id == str(user_id)))
    email = session.info.get("weknora_email") or None
    if row is None:
        # Email is descriptive; user ID is the authority. Never claim legacy `me`
        # or another account's records by an email match on first login.
        values = dict(username=f"wk:{user_id}", weknora_user_id=str(user_id),
                      email=email, display_name=(session.info.get("weknora_display_name")
                                               or email or str(user_id)),
                      varieties=[], status="在职", created_at=datetime.now(timezone.utc))
        dialect = session.get_bind().dialect.name
        if dialect in {"sqlite", "postgresql"}:
            if dialect == "sqlite":
                from sqlalchemy.dialects.sqlite import insert
            else:
                from sqlalchemy.dialects.postgresql import insert
            session.execute(insert(Researcher).values(**values).on_conflict_do_nothing(
                index_elements=[Researcher.weknora_user_id]))
            row = session.scalar(select(Researcher).where(Researcher.weknora_user_id == str(user_id)))
        else:
            row = Researcher(**values)
            session.add(row)
            session.flush()
    return row


def researcher_id(session: Session) -> int | None:
    row = current_researcher(session)
    return row.id if row is not None else None


def scope_query(session: Session, statement, model):
    rid = researcher_id(session)
    return statement.where(model.researcher_id == rid) if rid is not None else statement


def require_researcher(session: Session, requested_id: int) -> int:
    rid = researcher_id(session)
    if rid is not None and requested_id != rid:
        raise PermissionError("无权访问其他研究员的个人研究记录")
    return requested_id


def require_owned(session: Session, row):
    rid = researcher_id(session)
    if rid is None:
        return row
    owner = row
    if owner is not None and hasattr(owner, "judgment_id"):
        owner = session.get(Judgment, owner.judgment_id)
    if owner is None or getattr(owner, "researcher_id", None) != rid:
        raise PermissionError("研究记录不存在或不属于当前账号")
    return row


def verified_actor(session: Session, fallback: str) -> str:
    user_id = session.info.get("weknora_user_id")
    return str(user_id) if user_id else fallback


def scoped_directory(base, *, session: Session | None = None, personal: bool = False):
    """Workspace/user scoped files; never interpolate unverified path components."""
    import hashlib
    from pathlib import Path
    from tin.config import settings
    from tin.integration.identity import current_identity
    info = session.info if session is not None else {}
    identity = current_identity.get()
    tenant = info.get("tenant_id") or (identity.tenant_id if identity else None)
    user = info.get("weknora_user_id") or (identity.user_id if identity else None)
    if tenant is None:
        if settings.weknora_api_url:
            raise PermissionError("Verified workspace identity is required")
        return Path(base)
    if isinstance(tenant, bool) or not isinstance(tenant, int) or tenant <= 0:
        raise PermissionError("Invalid workspace identity")
    path = Path(base) / "workspaces" / str(tenant)
    if personal:
        if not user:
            raise PermissionError("Verified user identity is required")
        path = path / "users" / hashlib.sha256(str(user).encode()).hexdigest()
    return path
