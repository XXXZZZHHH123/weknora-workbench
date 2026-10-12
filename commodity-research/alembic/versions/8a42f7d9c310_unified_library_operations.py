"""Durable processing branches in the unified source library."""
from alembic import op
import sqlalchemy as sa
from tin.db import JSONType, UTCDateTime

revision = "8a42f7d9c310"
down_revision = "f63c9b24a801"
branch_labels = None
depends_on = None


def upgrade():
    op.create_table("library_operations",
        sa.Column("id", sa.String(32), primary_key=True),
        sa.Column("asset_id", sa.String(64), sa.ForeignKey("source_artifacts.id"), nullable=False),
        sa.Column("actor", sa.String(128), nullable=False),
        sa.Column("purpose", sa.String(16), nullable=False),
        sa.Column("status", sa.String(32), nullable=False),
        sa.Column("idempotency_key", sa.String(64), nullable=False),
        sa.Column("request", JSONType, nullable=False),
        sa.Column("remote_ids", JSONType, nullable=False),
        sa.Column("result", JSONType, nullable=False),
        sa.Column("error", sa.Text(), nullable=True),
        sa.Column("attempts", sa.Integer(), nullable=False),
        sa.Column("created_at", UTCDateTime(), nullable=False),
        sa.Column("updated_at", UTCDateTime(), nullable=False))
    op.create_index("ix_library_operations_asset_id", "library_operations", ["asset_id"])
    op.create_index("ix_library_operations_actor", "library_operations", ["actor"])
    op.create_index("uq_library_operation_key", "library_operations", ["idempotency_key"], unique=True)


def downgrade():
    op.drop_table("library_operations")
