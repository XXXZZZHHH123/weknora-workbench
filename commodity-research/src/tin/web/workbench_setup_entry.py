"""Entry eligibility and an empty-workbench check over saved metadata."""
from tin.integration.workbench_scope import current_workbench
from tin.judgments.service import authorized_judgments
from tin.models.workbench import WorkbenchArtifact
from tin.web.charts import artifact_query
from tin.web.dimension_board import revision


def setup_enabled(scope):
    return bool(scope and scope.scope_version == 2 and not scope.restricted and scope.can_edit
                and scope.metadata.get('can_read_source') is not False
                and scope.authorization.startswith('Bearer ')
                and {'home', 'indicators'}.issubset(scope.view_modules))


def home_setup_context(session, scope):
    enabled = setup_enabled(scope)
    empty = False
    if enabled and revision(session) == 0:
        marker = current_workbench.set(scope)
        try:
            charts = artifact_query().with_only_columns(WorkbenchArtifact.id).limit(1)
            empty = session.scalar(charts) is None and not (
                'judgments' in scope.view_modules and authorized_judgments(session, scope))
        finally:
            current_workbench.reset(marker)
    return {'setup_enabled': enabled, 'setup_empty': empty}
