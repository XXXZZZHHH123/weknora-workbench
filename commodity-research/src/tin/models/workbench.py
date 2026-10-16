"""Source-linked research authored inside a seed-free workbench."""
from datetime import datetime

from sqlalchemy import ForeignKey, Integer, String, Text
from sqlalchemy.orm import Mapped, mapped_column

from tin.db import Base, JSONType, UTCDateTime


class WorkbenchArtifact(Base):
    __tablename__ = "workbench_artifacts"

    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    module: Mapped[str] = mapped_column(String(24), index=True)
    actor: Mapped[str] = mapped_column(String(128), index=True)
    title: Mapped[str] = mapped_column(String(120))
    body: Mapped[str] = mapped_column(Text)
    status: Mapped[str] = mapped_column(String(16))
    source_ids: Mapped[list] = mapped_column(JSONType)
    source_snapshots: Mapped[list] = mapped_column(JSONType, default=list)
    revision: Mapped[int] = mapped_column(Integer, default=1)
    created_at: Mapped[datetime] = mapped_column(UTCDateTime)
    updated_at: Mapped[datetime] = mapped_column(UTCDateTime)


class WorkbenchArtifactRevision(Base):
    __tablename__ = "workbench_artifact_revisions"

    artifact_id: Mapped[str] = mapped_column(ForeignKey("workbench_artifacts.id", ondelete="CASCADE"), primary_key=True)
    revision: Mapped[int] = mapped_column(Integer, primary_key=True)
    title: Mapped[str] = mapped_column(String(120))
    body: Mapped[str] = mapped_column(Text)
    status: Mapped[str] = mapped_column(String(16))
    source_ids: Mapped[list] = mapped_column(JSONType)
    source_snapshots: Mapped[list] = mapped_column(JSONType)
    edited_by: Mapped[str] = mapped_column(String(128))
    recorded_at: Mapped[datetime] = mapped_column(UTCDateTime)


class WorkbenchStorageScope(Base):
    __tablename__ = "isolated_scope"

    workbench_id: Mapped[str] = mapped_column(String(36), primary_key=True)
    knowledge_base_id: Mapped[str] = mapped_column(String(128))
    knowledge_base_tenant_id: Mapped[int] = mapped_column(Integer)
