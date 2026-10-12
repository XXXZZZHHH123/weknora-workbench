"""Workspace refresh plans and durable attempt history.

Revision ID: f63c9b24a801
Revises: d52e8a1f7b90
"""
from alembic import op
import sqlalchemy as sa
revision = "f63c9b24a801"
down_revision = "d52e8a1f7b90"
branch_labels = None
depends_on = None


def upgrade():
    op.create_table("refresh_settings",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("enabled", sa.Boolean(), nullable=False),
        sa.Column("sources", sa.JSON(), nullable=False),
        sa.Column("times", sa.JSON(), nullable=False),
        sa.Column("next_run_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("updated_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("updated_by", sa.String(128), nullable=False))
    op.create_table("refresh_runs",
        sa.Column("id", sa.String(32), primary_key=True),
        sa.Column("trigger", sa.String(16), nullable=False),
        sa.Column("status", sa.String(32), nullable=False),
        sa.Column("started_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("finished_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("scheduled_for", sa.DateTime(timezone=True), nullable=True),
        sa.Column("actor", sa.String(128), nullable=False),
        sa.Column("sources", sa.JSON(), nullable=False),
        sa.Column("results", sa.JSON(), nullable=False),
        sa.Column("error", sa.Text(), nullable=True),
        sa.Column("latest_trade_date", sa.String(10), nullable=True),
        sa.UniqueConstraint("scheduled_for", name="uq_refresh_scheduled_slot"))
    op.create_index("ix_refresh_runs_status", "refresh_runs", ["status"])


def downgrade():
    op.drop_index("ix_refresh_runs_status", table_name="refresh_runs")
    op.drop_table("refresh_runs")
    op.drop_table("refresh_settings")
