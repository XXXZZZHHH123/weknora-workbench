"""Current research in a legacy shared DB still belongs to one actual workbench."""
from contextlib import contextmanager
from dataclasses import replace
from datetime import date, datetime, timezone
import json

import pytest

from tin.config import SHANGHAI
from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope, current_workbench
from tin.judgments.service import save_version
from tin.models import Indicator, Observation, Signal
from tin.schemas.judgment import JudgmentIn

A = '30303030-3030-4030-8030-303030303030'
B = '40404040-4040-4040-8040-404040404040'


def scopes(session):
    identity = Identity('scope-owner', 'scope@test.invalid', 'Scope', 17, 'owner')
    a = WorkbenchScope(identity, A, 'library-a', 17, {'workspace_mode': 'legacy'}, 'Bearer fixture',
                       scope_version=2, sources_revision=1)
    return a, replace(a, workbench_id=B, knowledge_base_id='library-b')


def saved(session, scope, name, variety, identifier, *, active=False):
    session.info.update(scope.identity.session_info(), workbench_id=scope.workbench_id)
    if session.get(Indicator, identifier) is None:
        session.add(Indicator(series_id=identifier, name='Isolated quantity field', variety=variety,
            category='fixture', caliber={}, unit='吨', source='Fixture', frequency='日', fetch_mode='readonly'))
        session.flush()
    document = JudgmentIn(variety=variety, author='fixture', written_at=date(2026, 1, 1),
        contradiction={'text': name}, thresholds=[{'id': name, 'name': name, 'series_id': identifier,
            'operator': '>', 'value': 1, 'type': '观察位', 'horizon': '短期'}])
    row = save_version(session, document, actor=scope.identity.user_id)
    if active:
        row.status = '生效'
    session.commit()
    return row


@contextmanager
def viewing(session, scope):
    session.info.update(scope.identity.session_info(), workbench_id=scope.workbench_id)
    marker = current_workbench.set(scope)
    try:
        yield
    finally:
        current_workbench.reset(marker)


def test_foreign_active_does_not_hide_own_draft_or_leak_signals_and_fact_references(session, monkeypatch):
    from tin.integration import page_facts, workbench_scope
    from tin.judgments.service import current
    from tin.export.board import snapshot, threshold_radar
    from tin.compute.signals import evaluate_preview
    a, b = scopes(session)
    aid, bid = 'kb-' + 'a' * 48, 'kb-' + 'b' * 48
    foreign = saved(session, a, 'FOREIGN_A_RESEARCH', 'ZZ', aid, active=True)
    own = saved(session, b, 'LOCAL_B_RESEARCH', 'ZZ', bid)
    stamp = datetime(2026, 1, 9, tzinfo=SHANGHAI)
    session.add(Observation(series_id=bid, value=7, as_of=stamp, fetched_at=stamp,
        caliber_snapshot={}, source='Fixture', revision=0, status='正常'))
    session.add(Signal(judgment_id=foreign.id, rule_id='FOREIGN_A_RESEARCH', rule_type='threshold',
        trade_date='2026-01-09', state='触发', current_value=999, confidence='已核验',
        evidence=[{'desc': 'FOREIGN_A_EVIDENCE'}], generated_at=stamp))
    session.commit()
    @contextmanager
    def artifact(_scope):
        yield session
    monkeypatch.setattr(workbench_scope, 'artifact_session', artifact)
    with viewing(session, b):
        assert current(session, 'ZZ').id == own.id
        data = snapshot(session, 'ZZ', date(2026, 1, 9))
        assert data['judgment']['id'] == own.id and data['signals'] == []
        assert 'FOREIGN_A_RESEARCH' not in json.dumps(data)
        assert 'FOREIGN_A_EVIDENCE' not in json.dumps(data)
        assert [row['id'] for row in threshold_radar(session, 'ZZ', date(2026, 1, 9))] == ['LOCAL_B_RESEARCH']
        assert {row.judgment_id for row in evaluate_preview(session, 'ZZ', date(2026, 1, 9))} == {own.id}
        plans, _ = page_facts._saved_plan(b, {'module': 'judgments', 'series_ids': None,
            'day': '2026-01-09', 'points': None, 'template_id': None})
        assert {identifier for plan in plans for identifier in plan['ids']} == {bid}


def test_latest_explicit_variety_is_selected_inside_workbench_not_from_newer_foreign_record(session):
    from tin.integration.research_profile import research_variety
    from tin.judgments.service import current
    a, b = scopes(session)
    local = saved(session, b, 'LOCAL_VARIETY', 'BB', 'kb-' + 'c' * 48)
    saved(session, a, 'NEWER_FOREIGN_VARIETY', 'AA', 'kb-' + 'd' * 48, active=True)
    with viewing(session, b):
        assert research_variety(session) == 'BB'
        assert current(session, research_variety(session)).id == local.id


def test_only_foreign_current_is_absent_and_unscoped_standalone_contract_stays(session):
    from tin.judgments.service import current
    a, b = scopes(session)
    foreign = saved(session, a, 'FOREIGN_ONLY', 'YY', 'kb-' + 'e' * 48, active=True)
    with viewing(session, b):
        assert current(session, 'YY') is None
    # The old standalone call still returns its single-researcher current row.
    assert current(session, 'YY').id == foreign.id
