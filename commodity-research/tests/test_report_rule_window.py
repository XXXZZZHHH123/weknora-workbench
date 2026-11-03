"""Report rule trials read the same window as the judgment page, never the report's short one."""
from contextlib import contextmanager
from dataclasses import replace
from datetime import date

from tin.compute import signals
from tin.integration import page_facts, workbench_scope
from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope
from tin.web import report_period

SCOPE = WorkbenchScope(Identity('owner', '', 'Fixture', 17, 'owner'), '7ac25ec4-378c-4071-a8ee-c3679617b67a',
    'fixture-kb', 17, {'workspace_mode': 'isolated', 'can_edit': True, 'can_read_source': True}, 'Bearer fixture')


def recorder(monkeypatch):
    calls = []
    nested = object()

    @contextmanager
    def session(scope):
        calls.append(('session', scope.workbench_id, dict(page_facts._page.get())))
        yield nested
    monkeypatch.setattr(workbench_scope, 'workbench_session', session)
    monkeypatch.setattr(signals, 'evaluate_preview', lambda used, variety, day: calls.append(('evaluate', used is nested, variety, day)) or [])
    return calls


def test_report_rules_use_the_judgment_read_window(monkeypatch):
    calls = recorder(monkeypatch)
    report_period._rule_preview(object(), replace(SCOPE, scope_version=2), 'XY', date(2026, 10, 24), {'id': 3})
    assert calls[0] == ('session', SCOPE.workbench_id, {'module': 'judgments', 'series_ids': None, 'day': '2026-10-24',
        'template_id': None, 'points': None, 'judgment_id': 3})
    assert calls[1] == ('evaluate', True, 'XY', date(2026, 10, 24))


def test_without_a_current_source_scope_the_report_session_is_used(monkeypatch):
    calls = recorder(monkeypatch)
    outer = object()
    monkeypatch.setattr(signals, 'evaluate_preview', lambda used, variety, day: calls.append(('evaluate', used is outer)) or [])
    report_period._rule_preview(outer, None, 'XY', date(2026, 10, 24), {'id': 3})
    report_period._rule_preview(outer, SCOPE, 'XY', date(2026, 10, 24), {'id': 3})
    assert calls == [('evaluate', True), ('evaluate', True)]
