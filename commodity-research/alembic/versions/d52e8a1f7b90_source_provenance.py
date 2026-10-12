"""Immutable source artifacts, mappings, batches and observation evidence."""
from alembic import op
import sqlalchemy as sa
from tin.db import JSONType, UTCDateTime

revision = "d52e8a1f7b90"
down_revision = "c12d8e49a601"
branch_labels = None
depends_on = None


def upgrade():
    op.create_table("source_artifacts",
        sa.Column("id",sa.String(64),primary_key=True),
        sa.Column("filename",sa.String(254),nullable=False),
        sa.Column("kind",sa.String(32),nullable=False),
        sa.Column("media_type",sa.String(128),nullable=False),
        sa.Column("size",sa.Integer(),nullable=False),
        sa.Column("storage_key",sa.String(128),nullable=False),
        sa.Column("snapshot_key",sa.String(128),nullable=True),
        sa.Column("source_url",sa.Text(),nullable=True),
        sa.Column("details",JSONType,nullable=False),
        sa.Column("created_at",UTCDateTime(),nullable=False))
    op.create_table("source_batches",
        sa.Column("id",sa.String(64),primary_key=True),
        sa.Column("artifact_id",sa.String(64),sa.ForeignKey("source_artifacts.id"),nullable=False),
        sa.Column("idempotency_key",sa.String(64),nullable=False),
        sa.Column("kind",sa.String(32),nullable=False),
        sa.Column("actor",sa.String(128),nullable=False),
        sa.Column("status",sa.String(32),nullable=False),
        sa.Column("mapping",JSONType,nullable=False),
        sa.Column("result",JSONType,nullable=False),
        sa.Column("created_at",UTCDateTime(),nullable=False),
        sa.Column("committed_at",UTCDateTime(),nullable=True))
    op.create_index("ix_source_batches_artifact_id","source_batches",["artifact_id"])
    op.create_index("uq_source_batch_key","source_batches",["idempotency_key"],unique=True)
    op.create_table("source_mappings",
        sa.Column("id",sa.String(64),primary_key=True),
        sa.Column("config",JSONType,nullable=False),
        sa.Column("version",sa.Integer(),nullable=False),
        sa.Column("confirmed_by",sa.String(128),nullable=False),
        sa.Column("confirmed_at",UTCDateTime(),nullable=False))
    op.create_table("observation_sources",
        sa.Column("id",sa.String(64),primary_key=True),
        sa.Column("observation_id",sa.Integer(),sa.ForeignKey("observations.id"),nullable=False),
        sa.Column("artifact_id",sa.String(64),sa.ForeignKey("source_artifacts.id"),nullable=False),
        sa.Column("batch_id",sa.String(64),sa.ForeignKey("source_batches.id"),nullable=True),
        sa.Column("locator",JSONType,nullable=False),
        sa.Column("mapping_version",sa.String(64),nullable=False),
        sa.Column("transformation",JSONType,nullable=False),
        sa.Column("created_at",UTCDateTime(),nullable=False))
    for field in ("observation_id","artifact_id","batch_id"):
        op.create_index("ix_observation_sources_"+field,"observation_sources",[field])


def downgrade():
    for table in ("observation_sources","source_mappings","source_batches","source_artifacts"):
        op.drop_table(table)
