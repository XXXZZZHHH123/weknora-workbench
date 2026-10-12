"""Immutable source versions and many-to-one observation evidence."""
from datetime import datetime
from sqlalchemy import ForeignKey, Index, Integer, String, Text
from sqlalchemy.orm import Mapped, mapped_column
from tin.db import Base, JSONType, UTCDateTime


class SourceArtifact(Base):
    __tablename__ = "source_artifacts"
    id: Mapped[str] = mapped_column(String(64), primary_key=True)  # SHA256 bytes
    filename: Mapped[str] = mapped_column(String(254))
    kind: Mapped[str] = mapped_column(String(32))
    media_type: Mapped[str] = mapped_column(String(128))
    size: Mapped[int] = mapped_column(Integer)
    storage_key: Mapped[str] = mapped_column(String(128))
    snapshot_key: Mapped[str | None] = mapped_column(String(128))
    source_url: Mapped[str | None] = mapped_column(Text)
    details: Mapped[dict] = mapped_column(JSONType, default=dict)
    created_at: Mapped[datetime] = mapped_column(UTCDateTime)


class SourceBatch(Base):
    __tablename__ = "source_batches"
    __table_args__ = (Index("uq_source_batch_key", "idempotency_key", unique=True),)
    id: Mapped[str] = mapped_column(String(64), primary_key=True)
    artifact_id: Mapped[str] = mapped_column(ForeignKey("source_artifacts.id"), index=True)
    idempotency_key: Mapped[str] = mapped_column(String(64))
    kind: Mapped[str] = mapped_column(String(32))
    actor: Mapped[str] = mapped_column(String(128))
    status: Mapped[str] = mapped_column(String(32), default="preview")
    mapping: Mapped[dict] = mapped_column(JSONType, default=dict)
    result: Mapped[dict] = mapped_column(JSONType, default=dict)
    created_at: Mapped[datetime] = mapped_column(UTCDateTime)
    committed_at: Mapped[datetime | None] = mapped_column(UTCDateTime)


class SourceMapping(Base):
    __tablename__ = "source_mappings"
    id: Mapped[str] = mapped_column(String(64), primary_key=True)  # format fingerprint
    config: Mapped[dict] = mapped_column(JSONType)
    version: Mapped[int] = mapped_column(Integer, default=1)
    confirmed_by: Mapped[str] = mapped_column(String(128))
    confirmed_at: Mapped[datetime] = mapped_column(UTCDateTime)


class ObservationSource(Base):
    __tablename__ = "observation_sources"
    id: Mapped[str] = mapped_column(String(64), primary_key=True)
    observation_id: Mapped[int] = mapped_column(ForeignKey("observations.id"), index=True)
    artifact_id: Mapped[str] = mapped_column(ForeignKey("source_artifacts.id"), index=True)
    batch_id: Mapped[str | None] = mapped_column(ForeignKey("source_batches.id"), index=True)
    locator: Mapped[dict] = mapped_column(JSONType)
    mapping_version: Mapped[str] = mapped_column(String(64))
    transformation: Mapped[dict] = mapped_column(JSONType)
    created_at: Mapped[datetime] = mapped_column(UTCDateTime)
