"""A workspace's shared refresh plan and durable execution history."""
from datetime import datetime
from sqlalchemy import Boolean, Integer, String, Text, UniqueConstraint
from sqlalchemy.orm import Mapped, mapped_column
from tin.db import Base, JSONType, UTCDateTime


class RefreshSettings(Base):
    __tablename__ = "refresh_settings"
    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    enabled: Mapped[bool] = mapped_column(Boolean, default=True)
    sources: Mapped[list] = mapped_column(JSONType)
    times: Mapped[list] = mapped_column(JSONType)
    next_run_at: Mapped[datetime | None] = mapped_column(UTCDateTime)
    updated_at: Mapped[datetime] = mapped_column(UTCDateTime)
    updated_by: Mapped[str] = mapped_column(String(128))


class RefreshRun(Base):
    __tablename__ = "refresh_runs"
    __table_args__ = (UniqueConstraint("scheduled_for", name="uq_refresh_scheduled_slot"),)
    id: Mapped[str] = mapped_column(String(32), primary_key=True)
    trigger: Mapped[str] = mapped_column(String(16))
    status: Mapped[str] = mapped_column(String(32), index=True)
    started_at: Mapped[datetime] = mapped_column(UTCDateTime)
    finished_at: Mapped[datetime | None] = mapped_column(UTCDateTime)
    scheduled_for: Mapped[datetime | None] = mapped_column(UTCDateTime)
    actor: Mapped[str] = mapped_column(String(128))
    sources: Mapped[list] = mapped_column(JSONType)
    results: Mapped[list] = mapped_column(JSONType, default=list)
    error: Mapped[str | None] = mapped_column(Text)
    latest_trade_date: Mapped[str | None] = mapped_column(String(10))
