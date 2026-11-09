"""Space-wide, reversible indicator correspondence; observations stay in their sources."""
from datetime import datetime

from sqlalchemy import Index, Integer, String, text
from sqlalchemy.orm import Mapped, mapped_column

from tin.db import Base, JSONType, UTCDateTime


class IndicatorCorrespondence(Base):
    __tablename__ = 'indicator_correspondences'
    __table_args__ = (
        Index('uq_indicator_confirmed_personal', 'personal_id', unique=True,
              sqlite_where=text("state = 'confirmed_default'"),
              postgresql_where=text("state = 'confirmed_default'")),
    )

    id: Mapped[str] = mapped_column(String(64), primary_key=True)
    personal_id: Mapped[str] = mapped_column(String(128), index=True)
    default_id: Mapped[str] = mapped_column(String(128), index=True)
    personal_kb_id: Mapped[str] = mapped_column(String(128))
    default_kb_id: Mapped[str] = mapped_column(String(128))
    personal_tenant_id: Mapped[int] = mapped_column(Integer)
    default_tenant_id: Mapped[int] = mapped_column(Integer)
    state: Mapped[str] = mapped_column(String(24), default='pending', index=True)
    personal: Mapped[dict] = mapped_column(JSONType)
    default: Mapped[dict] = mapped_column(JSONType)
    candidate: Mapped[dict] = mapped_column(JSONType)
    evidence: Mapped[dict | None] = mapped_column(JSONType, nullable=True)
    history: Mapped[list] = mapped_column(JSONType, default=list)
    revision: Mapped[int] = mapped_column(Integer, default=1)
    confirmed_by: Mapped[str | None] = mapped_column(String(128), nullable=True)
    confirmed_at: Mapped[datetime | None] = mapped_column(UTCDateTime, nullable=True)
    created_at: Mapped[datetime] = mapped_column(UTCDateTime)
    updated_at: Mapped[datetime] = mapped_column(UTCDateTime)
