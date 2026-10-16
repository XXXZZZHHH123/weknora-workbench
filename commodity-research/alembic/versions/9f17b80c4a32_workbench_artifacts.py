"""Source-linked artifacts for isolated research workbenches."""
from alembic import op
import sqlalchemy as sa

revision = "9f17b80c4a32"
down_revision = "8a42f7d9c310"
branch_labels = None
depends_on = None


def upgrade():
    op.create_table("isolated_scope",
        sa.Column("workbench_id", sa.String(36), primary_key=True),
        sa.Column("knowledge_base_id", sa.String(128), nullable=False),
        sa.Column("knowledge_base_tenant_id", sa.Integer(), nullable=False))
    op.create_table("workbench_artifacts",
        sa.Column("id", sa.String(36), primary_key=True),
        sa.Column("module", sa.String(24), nullable=False),
        sa.Column("actor", sa.String(128), nullable=False),
        sa.Column("title", sa.String(120), nullable=False),
        sa.Column("body", sa.Text(), nullable=False),
        sa.Column("status", sa.String(16), nullable=False),
        sa.Column("source_ids", sa.JSON(), nullable=False),
        sa.Column("revision", sa.Integer(), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("updated_at", sa.DateTime(timezone=True), nullable=False))
    op.create_index("ix_workbench_artifacts_module", "workbench_artifacts", ["module"])
    op.create_index("ix_workbench_artifacts_actor", "workbench_artifacts", ["actor"])


def downgrade():
    op.drop_index("ix_workbench_artifacts_actor", table_name="workbench_artifacts")
    op.drop_index("ix_workbench_artifacts_module", table_name="workbench_artifacts")
    op.drop_table("workbench_artifacts")
    op.drop_table("isolated_scope")
