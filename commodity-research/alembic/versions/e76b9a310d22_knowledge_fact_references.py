"""Keep research references independent of retired local fact tables."""
from alembic import op

revision = 'e76b9a310d22'
down_revision = 'a618c3e72f45'
branch_labels = None
depends_on = None


def upgrade():
    convention = {'fk': 'fk_%(table_name)s_%(column_0_name)s_%(referred_table_name)s'}
    with op.batch_alter_table('judgment_series_refs', naming_convention=convention) as batch:
        batch.drop_constraint('fk_judgment_series_refs_series_id_indicators', type_='foreignkey')


def downgrade():
    raise RuntimeError('知识库事实引用不能自动回退为研究台独立事实，请恢复匹配备份')
