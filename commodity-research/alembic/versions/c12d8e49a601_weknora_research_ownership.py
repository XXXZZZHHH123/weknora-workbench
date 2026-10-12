"""Bind verified accounts and isolate personal research versions.

Revision ID: c12d8e49a601
Revises: b7e4c1a09d55
"""
from alembic import op
import sqlalchemy as sa

revision = "c12d8e49a601"
down_revision = "b7e4c1a09d55"
branch_labels = None
depends_on = None


def upgrade():
    sqlite = op.get_bind().dialect.name == "sqlite"
    # Avoid recreating referenced SQLite tables: existing playbooks, briefs and
    # strategy events must survive with foreign-key enforcement enabled.
    op.add_column("researchers", sa.Column("weknora_user_id", sa.String(64), nullable=True))
    op.add_column("researchers", sa.Column("email", sa.String(254), nullable=True))
    op.create_index("uq_researchers_weknora_user_id", "researchers", ["weknora_user_id"], unique=True)
    op.create_index("uq_researchers_email", "researchers", ["email"], unique=True)
    if sqlite:
        op.execute("ALTER TABLE strategies ADD COLUMN researcher_id INTEGER REFERENCES researchers(id)")
    else:
        op.alter_column("researchers", "username", existing_type=sa.String(32), type_=sa.String(128))
        op.alter_column("researchers", "display_name", existing_type=sa.String(32), type_=sa.String(254))
        op.alter_column("daily_briefs", "reviewed_by", existing_type=sa.String(32), type_=sa.String(64))
        op.alter_column("brief_revisions", "actor", existing_type=sa.String(32), type_=sa.String(64))
        op.add_column("strategies", sa.Column("researcher_id", sa.Integer(), nullable=True))
        op.create_foreign_key("fk_strategy_researcher", "strategies", "researchers", ["researcher_id"], ["id"])
    op.create_index("ix_strategies_researcher_id", "strategies", ["researcher_id"])
    op.drop_index("uq_judgment_active", table_name="judgments")
    op.drop_index("uq_judgment_version", table_name="judgments")
    op.create_index("uq_judgment_active", "judgments", ["researcher_id", "variety"], unique=True,
                    sqlite_where=sa.text("status = '生效'"), postgresql_where=sa.text("status = '生效'"))
    op.create_index("uq_judgment_version", "judgments", ["researcher_id", "variety", "version"], unique=True)
    op.create_index("uq_judgment_legacy_active", "judgments", ["variety"], unique=True,
                    sqlite_where=sa.text("status = '生效' AND researcher_id IS NULL"),
                    postgresql_where=sa.text("status = '生效' AND researcher_id IS NULL"))
    op.create_index("uq_judgment_legacy_version", "judgments", ["variety", "version"], unique=True,
                    sqlite_where=sa.text("researcher_id IS NULL"), postgresql_where=sa.text("researcher_id IS NULL"))


def downgrade():
    # Refuse silent data loss or cross-owner version collisions on old schema.
    raise RuntimeError("Restore a pre-migration backup to downgrade research ownership safely")
