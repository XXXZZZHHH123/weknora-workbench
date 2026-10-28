"""Page selection, fact planning and downloads agree on the selected record."""
from contextlib import contextmanager
from datetime import date
import json

from fastapi import HTTPException
import pytest
from starlette.requests import Request

from tests.test_judgment_panels import panels, seed
from tests.test_knowledge_facts import canonical, page
from tests.test_structured_judgment_import import payload
from tin.integration import page_facts
from tin.integration.identity import current_identity
from tin.integration.workbench_scope import artifact_session, current_workbench
from tin.judgments.service import current, save_version
from tin.schemas.judgment import JudgmentIn


def alternative(state):
    document = payload()
    old = document['whitelist'][0]
    document = json.loads(json.dumps(document).replace(old, 'kb-' + 'b' * 48))
    document['contradiction']['bull']['claim'] = 'An independent draft explanation'
    with artifact_session(state['scope']) as session:
        session.info.update(knowledge_facts=True, knowledge_facts_catalog={document['whitelist'][0]: {'unit': '吨'}})
        row = save_version(session, JudgmentIn.model_validate(document), actor=state['scope'].identity.user_id)
        session.commit()
        return row.id, document['whitelist'][0]


def test_selected_judgment_plans_its_bindings_and_preserves_formal_current(panels):
    _client, state = panels
    active, _ = seed(state, active=True)
    selected, identifier = alternative(state)
    plans, _ = page_facts._saved_plan(state['scope'], {'module': 'judgments', 'series_ids': None,
        'day': '2026-09-18', 'template_id': None, 'judgment_id': selected})
    assert {sid for plan in plans for sid in plan['ids']} == {identifier}
    with artifact_session(state['scope']) as session:
        assert current(session, 'XY').id == active


def test_selected_judgment_renders_its_own_explanation_and_identity(panels, monkeypatch):
    from tin.web import app as web
    _client, state = panels
    active, _ = seed(state, active=True)
    selected, _ = alternative(state)
    opened = []
    @contextmanager
    def page(module, **options):
        opened.append((module, options))
        with artifact_session(state['scope']) as session:
            yield session
    monkeypatch.setattr(web, '_page_session', page)
    monkeypatch.setattr(web, '_shell', lambda session, nav, day, **extra: extra)
    monkeypatch.setattr(web.templates, 'TemplateResponse', lambda request, name, context: context)
    mark = current_workbench.set(state['scope'])
    actor = current_identity.set(state['scope'].identity)
    try:
        request = Request({'type': 'http', 'path': '/sn/judgment', 'query_string': f'judgment_id={selected}'.encode(), 'headers': []})
        result = web._judgment_response(request)
        assert result['j'].id == selected and result['block']['id'] == selected
        assert result['block']['contradiction']['bull']['claim'] == 'An independent draft explanation'
        assert result['selected_judgment_id'] == selected
        assert opened[0][1]['judgment_id'] == selected
        with artifact_session(state['scope']) as session:
            assert current(session, 'XY').id == active
    finally:
        current_identity.reset(actor)
        current_workbench.reset(mark)


@pytest.mark.parametrize('invalid', [0, -1, True, '1', 2**63])
def test_invalid_selected_judgment_never_becomes_a_default_scan(invalid):
    with pytest.raises(HTTPException):
        with page_facts.page_read_scope('judgments', judgment_id=invalid):
            pass


def test_nested_judgment_selection_keeps_explicit_record():
    with page_facts.page_read_scope('judgments', judgment_id=37):
        with page_facts.page_read_scope('judgments'):
            assert page_facts._page.get()['judgment_id'] == 37
    assert page_facts._page.get() is None


def test_daily_json_download_uses_the_same_saved_report_builder(monkeypatch, session):
    from tin.web import app as web
    from tin.web import report_period
    @contextmanager
    def page(*args, **kwargs):
        yield session
    expected = {'manual_report': {'draft': {'title': 'A saved human draft'}}, 'period': {'kind': 'daily'}}
    monkeypatch.setattr(web.settings, 'weknora_api_url', 'http://native.invalid')
    monkeypatch.setattr(web, '_page_session', page)
    monkeypatch.setattr(report_period, 'build_period', lambda s, variety, cutoff, kind: expected)
    result = web.report_file('2026-01-09')
    assert json.loads(result.body) == expected


def test_verified_home_does_not_open_the_fact_projection_or_ticker(panels, monkeypatch):
    from tin.web import app as web
    _client, state = panels
    def forbidden(*args, **kwargs):
        raise AssertionError('Opening affairs must not load the market projection or ticker')
    monkeypatch.setattr(web, '_page_session', forbidden)
    monkeypatch.setattr(web, '_shell', forbidden)
    monkeypatch.setattr(web.templates, 'TemplateResponse', lambda request, name, context: context)
    mark = current_workbench.set(state['scope'])
    actor = current_identity.set(state['scope'].identity)
    try:
        result = web.home_page(Request({'type': 'http', 'path': '/', 'query_string': b'', 'headers': []}))
        assert result['nav'] == 'home' and result['ticker'] == []
        assert 'alerts' in result['affairs'] and 'recent' not in result['affairs']
    finally:
        current_identity.reset(actor)
        current_workbench.reset(mark)


def test_warning_check_remains_readonly_for_a_source_viewer(canonical):
    client, headers, _state, _path = canonical
    headers = {**headers, 'Authorization': 'Bearer source-viewer'}
    response = client.post(page(suffix='/api/sn/affairs/check'), headers=headers)
    assert response.status_code == 409, response.text  # v1 grant requires reopening; not an edit denial.
    assert '来源授权' in response.json()['detail']
    assert client.post(page(suffix='/api/sn/affairs/check'),
        headers={**headers, 'Origin': 'http://outside.test'}).status_code == 403
