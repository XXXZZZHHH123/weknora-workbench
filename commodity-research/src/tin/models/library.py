"""Durable, caller-owned processing branches for one immutable source file."""
from datetime import datetime

from sqlalchemy import ForeignKey, Index, Integer, String, Text
from sqlalchemy.orm import Mapped, mapped_column

from tin.db import Base, JSONType, UTCDateTime


class LibraryOperation(Base):
    __tablename__ = "library_operations"
    __table_args__ = (Index("uq_library_operation_key", "idempotency_key", unique=True),)
    id: Mapped[str] = mapped_column(String(32), primary_key=True)
    asset_id: Mapped[str] = mapped_column(ForeignKey("source_artifacts.id"), index=True)
    actor: Mapped[str] = mapped_column(String(128), index=True)
    purpose: Mapped[str] = mapped_column(String(16))
    status: Mapped[str] = mapped_column(String(32))
    idempotency_key: Mapped[str] = mapped_column(String(64))
    request: Mapped[dict] = mapped_column(JSONType)
    remote_ids: Mapped[dict] = mapped_column(JSONType, default=dict)
    result: Mapped[dict] = mapped_column(JSONType, default=dict)
    error: Mapped[str | None] = mapped_column(Text)
    attempts: Mapped[int] = mapped_column(Integer, default=0)
    created_at: Mapped[datetime] = mapped_column(UTCDateTime)
    updated_at: Mapped[datetime] = mapped_column(UTCDateTime)
