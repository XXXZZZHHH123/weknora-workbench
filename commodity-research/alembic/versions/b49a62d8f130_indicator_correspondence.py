"""Space-scoped indicator correspondence and evidence history."""
from alembic import op
import sqlalchemy as sa

revision = 'b49a62d8f130'
down_revision = 'e76b9a310d22'
branch_labels = None
depends_on = None


def upgrade():
    op.create_table('indicator_correspondences',
        sa.Column('id', sa.String(64), primary_key=True),
        sa.Column('personal_id', sa.String(128), nullable=False),
        sa.Column('default_id', sa.String(128), nullable=False),
        sa.Column('personal_kb_id', sa.String(128), nullable=False),
        sa.Column('default_kb_id', sa.String(128), nullable=False),
        sa.Column('personal_tenant_id', sa.Integer(), nullable=False),
        sa.Column('default_tenant_id', sa.Integer(), nullable=False),
        sa.Column('state', sa.String(24), nullable=False),
        sa.Column('personal', sa.JSON(), nullable=False),
        sa.Column('default', sa.JSON(), nullable=False),
        sa.Column('candidate', sa.JSON(), nullable=False),
        sa.Column('evidence', sa.JSON(), nullable=True),
        sa.Column('history', sa.JSON(), nullable=False),
        sa.Column('revision', sa.Integer(), nullable=False),
        sa.Column('confirmed_by', sa.String(128), nullable=True),
        sa.Column('confirmed_at', sa.DateTime(timezone=True), nullable=True),
        sa.Column('created_at', sa.DateTime(timezone=True), nullable=False),
        sa.Column('updated_at', sa.DateTime(timezone=True), nullable=False))
    for column in ('personal_id', 'default_id', 'state'):
        op.create_index('ix_indicator_correspondences_' + column, 'indicator_correspondences', [column])
    op.create_index('uq_indicator_confirmed_personal', 'indicator_correspondences', ['personal_id'], unique=True,
                    sqlite_where=sa.text("state = 'confirmed_default'"),
                    postgresql_where=sa.text("state = 'confirmed_default'"))


def downgrade():
    op.drop_table('indicator_correspondences')
