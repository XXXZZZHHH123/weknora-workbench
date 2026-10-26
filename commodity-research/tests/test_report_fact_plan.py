"""Report windows use only the current member's explicit saved bindings."""
from contextlib import contextmanager
from datetime import datetime, timezone
from datetime import date

import pytest
from fastapi import HTTPException

from tin.integration import page_facts, workbench_scope
from tin.jobs.seed import seed_researcher
from tin.models import SourceMapping, Strategy
from tin.web.restricted_views import CORE_KEY, ORIGINS_KEY, mapping_key
from tests.test_workbench_sources_v2 import IDENTITY, WB


@pytest.fixture
def report_scope(session, monkeypatch):
    session.info.update(IDENTITY.session_info(), workbench_id=WB)
    researcher = seed_researcher(session)
    core, quote, evidence, other = ['kb-' + char * 48 for char in ('a', 'b', 'c', 'd')]
    stamp = datetime(2026, 1, 2, tzinfo=timezone.utc)
    session.add(SourceMapping(id=mapping_key(session, CORE_KEY), config={'series_ids': [core]},
                              confirmed_by=IDENTITY.user_id, confirmed_at=stamp))
    origins = {identifier: {'knowledge_base_id': 'fixture-kb', 'tenant_id': 17}
               for identifier in (core, quote, evidence)}
    session.add(SourceMapping(id=mapping_key(session, ORIGINS_KEY), config={'origins': origins},
                              confirmed_by=IDENTITY.user_id, confirmed_at=stamp))
    for owner, identifier in ((researcher.id, quote), (None, other)):
        session.add(Strategy(researcher_id=owner, title='Isolated fixture strategy', variety='SN',
            kind='outright', mode='simulation', status='draft', created_at=stamp, author='fixture',
            plan={'legs': [{'quote_series_id': identifier}],
                  'evidence': [{'series_id': evidence}] if owner else []}))
    session.commit()

    @contextmanager
    def artifact(_scope):
        yield session
    monkeypatch.setattr(workbench_scope, 'artifact_session', artifact)
    scope = workbench_scope.WorkbenchScope(IDENTITY, WB, '', 0, {'scope_version': 2},
        'Bearer fixture', scope_version=2, sources_revision=7)
    return scope, (core, quote, evidence, other), origins


@pytest.mark.parametrize('points', [None, 8])
def test_report_plan_includes_core_and_owned_strategy_bindings_without_browsing_catalog(report_scope, monkeypatch, points):
    scope, (core, quote, evidence, other), expected_origins = report_scope
    def forbidden(*args, **kwargs):
        raise AssertionError('Reports must not discover or preload the whole source catalog')
    monkeypatch.setattr(page_facts, 'catalog_page', forbidden)
    plans, origins = page_facts._saved_plan(scope, {
        'module': 'reports', 'series_ids': None, 'day': '2026-01-02',
        'template_id': None, 'points': points})
    assert {identifier for plan in plans for identifier in plan['ids']} == {core, quote, evidence}
    assert all(plan['points'] == (points or 2) for plan in plans)
    assert origins == expected_origins
    assert other not in {identifier for plan in plans for identifier in plan['ids']}


def test_report_boundaries_still_reject_over_budget_saved_bindings(report_scope, monkeypatch):
    from tin.web import restricted_views
    scope, _, _ = report_scope
    too_many = ['kb-' + format(index, '048x') for index in range(257)]
    monkeypatch.setattr(restricted_views, 'core_config', lambda session: too_many)
    with pytest.raises(HTTPException) as error:
        page_facts._saved_plan(scope, {'module': 'reports', 'series_ids': None,
                                     'day': None, 'template_id': None, 'points': 8})
    assert error.value.status_code == 413


def test_report_display_points_never_shorten_a_saved_rule_window(report_scope, monkeypatch):
    from tin.judgments import service
    from tin.schemas.judgment import JudgmentIn
    scope, (core, *_), _ = report_scope
    payload = JudgmentIn(variety='SN', author='fixture', written_at=date(2026, 1, 2), contradiction={},
        falsifiers=[{'id': 'long_window', 'desc': 'Synthetic observed changes', 'series_id': core,
                     'expression': 'change > 0', 'unit_confirmed': '吨', 'consecutive_periods': 9, 'side': 'bull'}])
    monkeypatch.setattr(service, 'current', lambda *_: object())
    monkeypatch.setattr(service, 'to_payload', lambda _: payload)
    plans, _ = page_facts._saved_plan(scope, {'module': 'reports', 'series_ids': None,
        'day': '2026-01-02', 'template_id': None, 'points': 2})
    assert next(plan['points'] for plan in plans if core in plan['ids']) == 10
