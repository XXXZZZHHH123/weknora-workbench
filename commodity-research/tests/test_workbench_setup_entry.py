from dataclasses import replace
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import shutil
import subprocess

from jinja2 import ChoiceLoader, DictLoader, Environment, FileSystemLoader, select_autoescape
import pytest

from tin.config import settings
from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope, artifact_session
from tin.models import Judgment, Researcher, SourceMapping
from tin.models.workbench import WorkbenchArtifact
from tin.web.research_dimensions import DIMENSIONS_KEY, default_dimensions
from tin.web.research_references import remember_record_scope
from tin.web.restricted_views import mapping_key
from tin.web.workbench_setup_entry import home_setup_context

ROOT = Path(__file__).resolve().parents[1]
WORKBENCH = '9b06c538-593a-453d-92fe-6374d5e73e62'
STAMP = datetime(2026, 3, 2, tzinfo=timezone.utc)


@pytest.fixture
def setup_scope(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    return WorkbenchScope(Identity('owner', '', 'Fixture', 17, 'owner'), WORKBENCH, 'fixture-kb', 17,
        {'workspace_mode': 'isolated', 'can_edit': True}, 'Bearer fixture', scope_version=2)


@pytest.mark.parametrize('saved', ['nothing', 'dimensions', 'chart', 'judgment', 'deleted_chart'])
def test_setup_guide_requires_an_empty_workbench(setup_scope, saved):
    scope = setup_scope
    with artifact_session(scope) as session:
        if saved == 'dimensions':
            session.add(SourceMapping(id=mapping_key(session, DIMENSIONS_KEY),
                config={'dimensions': default_dimensions()}, version=1, confirmed_by='owner', confirmed_at=STAMP))
        if saved in {'chart', 'deleted_chart'}:
            chart = WorkbenchArtifact(id='chart-fixture', module='charts', actor='owner', title='Saved chart',
                body='{}', status='deleted' if saved == 'deleted_chart' else 'draft', source_ids=[],
                source_snapshots=[], revision=1, created_at=STAMP, updated_at=STAMP)
            session.add(chart)
            session.flush()
            remember_record_scope(session, 'artifact', chart.id)
        if saved == 'judgment':
            owner = Researcher(username='fixture-owner', weknora_user_id='owner', display_name='Fixture',
                varieties=[], status='在职', created_at=STAMP)
            session.add(owner)
            session.flush()
            judgment = Judgment(variety='XY', version=1, researcher_id=owner.id, author='owner',
                written_at=STAMP.date(), review_due=STAMP.date(), status='草稿', contradiction={}, marginal_focus=[],
                pricing_power={}, whitelist=[], thresholds=[], falsifiers=[], created_at=STAMP)
            session.add(judgment)
            session.flush()
            remember_record_scope(session, 'judgment', judgment.id)
        session.commit()
        context = home_setup_context(session, scope)
    assert context == {'setup_enabled': True, 'setup_empty': saved in {'nothing', 'deleted_chart'}}


def test_setup_entry_hidden_without_edit_permission(setup_scope):
    scope = replace(setup_scope, metadata={**setup_scope.metadata, 'can_edit': False})
    with artifact_session(scope) as session:
        assert home_setup_context(session, scope) == {'setup_enabled': False, 'setup_empty': False}


def test_setup_checklist_browser_flow():
    node = shutil.which('node')
    if not node or not os.environ.get('RESEARCH_PLAYWRIGHT') or not os.environ.get('SHEET_REVIEW_BROWSER'):
        pytest.skip('Node, Playwright and Chromium are required for the setup dialog browser flow')
    shell = '<head>{% block head %}{% endblock %}</head><body>{% block main %}{% endblock %}</body>'
    templates = Environment(loader=ChoiceLoader([DictLoader({'base.html': shell}),
        FileSystemLoader(ROOT / 'src/tin/web/templates')]), autoescape=select_autoescape())
    templates.globals.update(research_static_prefix=lambda: '', static_version=lambda: 'fixture',
        research_url=lambda path: path, research_workbench=lambda: None)
    page = templates.get_template('_affairs_home.html')
    fixture = {'home': page.render(affairs={'setup_enabled': True, 'setup_empty': True,
        'alerts': [], 'sections': []}, restricted_view=True),
        'dimensions': templates.get_template('dimensions.html').render(
            dimension_page={'can_write': True, 'setup_enabled': True})}
    result = subprocess.run([node, str(ROOT / 'tests/workbench-setup-ui.cjs')], input=json.dumps(fixture),
        cwd=ROOT, capture_output=True, text=True, timeout=35)
    assert result.returncode == 0, result.stdout + result.stderr
