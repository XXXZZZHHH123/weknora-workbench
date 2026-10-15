"""Pin manual research evidence and retain actual authored revisions."""
from alembic import op
import sqlalchemy as sa

revision = "a618c3e72f45"
down_revision = "9f17b80c4a32"
branch_labels = None
depends_on = None


def upgrade():
    op.add_column("workbench_artifacts", sa.Column("source_snapshots", sa.JSON(),
                  nullable=False, server_default=sa.text("'[]'")))
    history = op.create_table("workbench_artifact_revisions",
        sa.Column("artifact_id", sa.String(36), sa.ForeignKey("workbench_artifacts.id", ondelete="CASCADE"), primary_key=True),
        sa.Column("revision", sa.Integer(), primary_key=True),
        sa.Column("title", sa.String(120), nullable=False),
        sa.Column("body", sa.Text(), nullable=False),
        sa.Column("status", sa.String(16), nullable=False),
        sa.Column("source_ids", sa.JSON(), nullable=False),
        sa.Column("source_snapshots", sa.JSON(), nullable=False),
        sa.Column("edited_by", sa.String(128), nullable=False),
        sa.Column("recorded_at", sa.DateTime(timezone=True), nullable=False))
    current = sa.table("workbench_artifacts", sa.column("id", sa.String()),
        sa.column("revision", sa.Integer()), sa.column("title", sa.String()),
        sa.column("body", sa.Text()), sa.column("status", sa.String()),
        sa.column("source_ids", sa.JSON()), sa.column("source_snapshots", sa.JSON()),
        sa.column("updated_at", sa.DateTime(timezone=True)))
    connection = op.get_bind()
    # Only the current persisted version exists in the old schema. Preserve it
    # honestly; never invent overwritten text, authors, hashes or original files.
    for row in connection.execute(sa.select(current)).mappings().all():
        snapshots = [{"knowledge_id": identifier, "capture_status": "unfrozen",
                      "notice": "历史引用尚未冻结原件；未重建迁移前已覆盖的内容"}
                     for identifier in (row["source_ids"] or [])]
        connection.execute(current.update().where(current.c.id == row["id"])
                           .values(source_snapshots=snapshots))
        connection.execute(history.insert().values(artifact_id=row["id"], revision=row["revision"],
            title=row["title"], body=row["body"], status=row["status"],
            source_ids=row["source_ids"], source_snapshots=snapshots,
            edited_by="", recorded_at=row["updated_at"]))


def downgrade():
    op.drop_table("workbench_artifact_revisions")
    with op.batch_alter_table("workbench_artifacts") as batch:
        batch.drop_column("source_snapshots")
