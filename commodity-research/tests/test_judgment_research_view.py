"""Synthetic two-sided research, necessary fact windows, and read-only evidence."""
from contextlib import contextmanager
from datetime import date, datetime, timezone
from types import SimpleNamespace

import pytest
from fastapi import HTTPException
from sqlalchemy import event
from sqlalchemy.orm import sessionmaker
from sqlalchemy.pool import StaticPool

from tin.db import Base, make_engine
from tin.integration import page_facts, workbench_scope
from tin.judgments import service
from tin.models import Indicator, Observation
from tin.schemas.judgment import JudgmentIn
from tin.web import judgment_research as research

A, B, OTHER = ['kb-' + letter * 48 for letter in 'abc']
DAY = date(2030, 1, 20)


@pytest.fixture
def facts():
    engine = make_engine('sqlite://', poolclass=StaticPool, connect_args={'check_same_thread': False})
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as session:
        for sid in (A, B, OTHER):
            session.add(Indicator(series_id=sid, name='Synthetic quantity ' + sid[-1], variety='TEST',
                category='Synthetic', unit='kg', caliber={}, source='synthetic', frequency='day', fetch_mode='manual'))
        session.commit()
        yield session
    engine.dispose()


def point(session, sid, day, value, *, revision=0, status='正常'):
    session.add(Observation(series_id=sid, value=value,
        as_of=datetime(2030, 1, day, 12, tzinfo=timezone.utc), fetched_at=datetime.now(timezone.utc),
        caliber_snapshot={}, source='synthetic', revision=revision, status=status))
    session.commit()


def saved(*, bull=None, bear=None, falsifiers=None, whitelist=None, identity=42):
    payload = JudgmentIn(variety='TEST', author='synthetic', written_at=date(2030, 1, 1),
        contradiction={'bull': {'claim': 'First hypothesis', 'evidence': bull or []},
            'bear': {'claim': 'Second hypothesis', 'evidence': bear or []}},
        falsifiers=falsifiers or [], whitelist=whitelist or [])
    return SimpleNamespace(id=identity, **payload.model_dump(mode='json'))


def rule(identifier, sid, side, expression='value > 10', periods=1):
    return {'id': identifier, 'desc': 'Original falsification condition ' + identifier,
        'series_id': sid, 'side': side, 'expression': expression, 'unit_confirmed': 'kg',
        'consecutive_periods': periods}


def test_empty_judgment_does_not_read_facts(facts, monkeypatch):
    monkeypatch.setattr(facts, 'scalars', lambda *args: pytest.fail('Empty judgment read facts'))
    result = research.judgment_research_view(facts, None, DAY)
    assert result['metrics'] == {} and result['shared'] == []
    assert result['counts'] == {'bound': 0, 'available': 0, 'thresholds': 0, 'falsifiers': 0, 'triggered': 0, 'pending': 0}


def test_current_previous_revision_and_date_spaced_sparkline_are_real(facts):
    point(facts, A, 1, 10)
    point(facts, A, 2, 12)
    point(facts, A, 10, 16)
    point(facts, A, 10, 20, revision=1)
    point(facts, OTHER, 19, 9999)
    judgment = saved(bull=[{'desc': 'First interpretation', 'series_id': A}],
        bear=[{'desc': 'Different interpretation', 'series_id': A}])
    view = research.judgment_research_view(facts, judgment, DAY)
    metric = view['metrics'][A]
    assert set(view['metrics']) == {A}
    assert metric['name'] == 'Synthetic quantity a' and metric['unit'] == 'kg'
    assert metric['latest'] == 20 and metric['previous'] == 12 and metric['change'] == 8
    assert metric['change_pct'] == pytest.approx(8 / 12 * 100)
    assert metric['date'].startswith('2030-01-10') and metric['previous_date'].startswith('2030-01-02')
    assert metric['point_count'] == 3
    assert [p['value'] for p in metric['points']] == [10, 12, 20]
    assert metric['points'][1]['x'] == pytest.approx(240 / 9)
    assert metric['points'][-1]['x'] == 240
    assert view['sides']['bull']['evidence'][0]['metric'] is metric
    assert view['shared'][0]['bull_texts'] == ['First interpretation']
    assert view['shared'][0]['bear_texts'] == ['Different interpretation']
    assert not {'strength', 'score', 'bull_weight', 'bear_weight'} & set(view)


def test_zero_is_real_and_zero_baseline_does_not_invent_percentage(facts):
    point(facts, A, 1, 0)
    point(facts, A, 2, 5)
    metric = research.judgment_research_view(facts, saved(whitelist=[A]), DAY)['metrics'][A]
    assert metric['previous'] == 0 and metric['latest'] == 5 and metric['change'] == 5
    assert metric['change_pct'] is None


def test_invalid_rows_do_not_turn_into_zero_or_connect_a_fake_segment(facts):
    point(facts, A, 1, 5)
    point(facts, A, 2, 999, status='无效')
    point(facts, A, 3, 0)
    metric = research.judgment_research_view(facts, saved(whitelist=[A]), DAY)['metrics'][A]
    assert metric['latest'] == 0 and metric['previous'] == 5 and metric['change'] == -5
    assert metric['point_count'] == 2 and len(metric['segments']) == 2
    assert all(999 != p['value'] for p in metric['points'])


def test_none_date_and_missing_field_never_read_or_backfill_values(facts, monkeypatch):
    point(facts, A, 2, 123)
    monkeypatch.setattr(research, 'recent_points', lambda *args: pytest.fail('Missing date read observations'))
    metric = research.judgment_research_view(facts, saved(whitelist=[A]), None)['metrics'][A]
    assert metric['latest'] is None and metric['previous'] is None and metric['points'] == []


def test_falsifiers_keep_explicit_side_and_selected_judgment_without_persistence(facts, monkeypatch):
    point(facts, A, 1, 1)
    point(facts, A, 2, 4)
    point(facts, A, 3, 8)
    judgment = saved(falsifiers=[rule('first', A, 'bull', 'change > 0', 2),
        rule('second', A, 'bear', 'value < 0')], identity=73)
    monkeypatch.setattr(service, 'current', lambda *a, **kw: pytest.fail('Replaced the selected judgment'))
    statements = []
    def observe(connection, cursor, statement, parameters, context, many):
        statements.append(statement.lower())
    event.listen(facts.bind, 'before_cursor_execute', observe)
    try:
        with monkeypatch.context() as patch:
            for name in ('add', 'add_all', 'flush', 'commit'):
                patch.setattr(facts, name, lambda *args: pytest.fail('Research read attempted a write'))
            view = research.judgment_research_view(facts, judgment, DAY)
    finally:
        event.remove(facts.bind, 'before_cursor_execute', observe)
    first, second = view['sides']['bull']['falsifiers'][0], view['sides']['bear']['falsifiers'][0]
    assert first['state'] == '触发' and first['current_value'] == 4
    assert second['state'] == '未触发' and second['current_value'] == 8
    assert first['rule']['side'] == 'bull' and second['rule']['side'] == 'bear'
    assert view['shared'][0]['bull_falsifiers'][0] is first
    assert view['shared'][0]['bear_falsifiers'][0] is second
    assert all(s.lstrip().startswith('select') for s in statements)
    assert all('signals' not in s and 'audit_log' not in s for s in statements)


def test_invalid_period_or_changed_unit_is_missing_not_untriggered(facts):
    point(facts, A, 1, 5)
    point(facts, A, 2, 8, status='无效')
    row = saved(falsifiers=[rule('first', A, 'bull', 'change > 0', 1)])
    result = research.judgment_research_view(facts, row, DAY)['sides']['bull']['falsifiers'][0]
    assert result['state'] == '数据缺失' and result['current_value'] is None
    facts.get(Indicator, A).unit = 'other unit'
    facts.commit()
    result = research.judgment_research_view(facts, row, DAY)['sides']['bull']['falsifiers'][0]
    assert result['state'] == '数据缺失' and result['current_value'] is None


def test_only_twenty_four_points_and_bound_saved_ids_are_read(facts, monkeypatch):
    for day in range(1, 21):
        point(facts, A, day, day)
    wanted = []
    actual = research.recent_points
    def observed(session, sid, day, count):
        wanted.append((sid, count))
        return actual(session, sid, day, count)
    monkeypatch.setattr(research, 'recent_points', observed)
    result = research.judgment_research_view(facts, saved(whitelist=[A]), DAY)
    assert wanted == [(A, 24)] and result['metrics'][A]['point_count'] == 20


def _plan_fixture(monkeypatch, judgment):
    @contextmanager
    def session(scope):
        yield SimpleNamespace(get=lambda *args: None, info={'workbench_id': '10000000-0000-4000-8000-000000000071'})
    monkeypatch.setattr(workbench_scope, 'artifact_session', session)
    monkeypatch.setattr(service, 'current', lambda *args, **kwargs: judgment)
    from tin.integration import research_profile
    monkeypatch.setattr(research_profile, 'research_variety', lambda *args, **kwargs: 'TEST')
    return SimpleNamespace(scope_version=2)


def test_v2_only_saved_judgment_windows_get_twenty_four_points(monkeypatch):
    row = saved(whitelist=[A, B], falsifiers=[rule('long', B, 'bull', 'change > 0', 120)])
    scope = _plan_fixture(monkeypatch, row)
    plans, _ = page_facts._saved_plan(scope, {'module': 'judgments', 'series_ids': None})
    assert plans == [{'ids': [A], 'points': 24, 'include_invalid': True}, {'ids': [B], 'points': 121, 'include_invalid': True}]
    other, _ = page_facts._saved_plan(scope, {'module': 'home', 'series_ids': None})
    assert any(plan['points'] == 2 for plan in other if A in plan['ids'])


def test_explicit_preview_window_stays_exactly_requested(monkeypatch):
    scope = _plan_fixture(monkeypatch, saved(whitelist=[A]))
    plans, _ = page_facts._saved_plan(scope, {'module': 'judgments', 'series_ids': [B], 'points': 1})
    assert plans == [{'ids': [B], 'points': 1, 'explicit': True}]


def test_saved_selected_judgment_does_not_load_current_records_bindings(monkeypatch):
    scope = _plan_fixture(monkeypatch, saved(whitelist=[A]))
    from tin.web import judgment_panels
    chosen = saved(whitelist=[B], identity=19)
    def selected(session, identifier, **kwargs):
        assert identifier == 19
        return chosen
    monkeypatch.setattr(judgment_panels, 'selected_judgment', selected)
    plans, _ = page_facts._saved_plan(scope, {'module': 'judgments', 'series_ids': None, 'judgment_id': 19})
    assert plans == [{'ids': [B], 'points': 24, 'include_invalid': True}]


def test_v1_and_other_modules_keep_their_original_windows(monkeypatch):
    scope = _plan_fixture(monkeypatch, saved(whitelist=[A]))
    scope.scope_version = 1
    plans, _ = page_facts._saved_plan(scope, {'module': 'judgments', 'series_ids': None})
    assert plans == [{'ids': [A], 'points': 2}]
    explicit_count, _ = page_facts._saved_plan(scope, {'module': 'judgments', 'series_ids': None, 'points': 1})
    assert explicit_count == [{'ids': [A], 'points': 1}]


def test_v2_saved_window_cannot_be_clipped_below_the_rule_requirement(monkeypatch):
    scope = _plan_fixture(monkeypatch, saved(whitelist=[A]))
    plans, _ = page_facts._saved_plan(scope, {'module': 'judgments', 'series_ids': None, 'points': 1})
    assert plans == [{'ids': [A], 'points': 24, 'include_invalid': True}]


@pytest.mark.parametrize('large', ['identifiers', 'observations'])
def test_saved_research_stays_within_existing_fact_budgets(facts, monkeypatch, large):
    identifiers = ['kb-' + f'{index:048x}' for index in range(257 if large == 'identifiers' else 200)]
    row = saved(whitelist=identifiers, falsifiers=[] if large == 'identifiers' else [
        rule('condition_' + str(index), identifier, 'bull', 'change > 0', 120)
        for index, identifier in enumerate(identifiers)])
    monkeypatch.setattr(facts, 'get', lambda *args: pytest.fail('Read beyond research fact budget'))
    with pytest.raises(HTTPException) as caught:
        research.judgment_research_view(facts, row, DAY)
    assert caught.value.status_code == 413
    scope = _plan_fixture(monkeypatch, row)
    with pytest.raises(HTTPException) as caught:
        page_facts._saved_plan(scope, {'module': 'judgments', 'series_ids': None})
    assert caught.value.status_code == 413


def test_long_history_shows_only_latest_twenty_four_real_points(facts):
    for day in range(1, 32):
        point(facts, A, day, day)
    metric = research.judgment_research_view(facts, saved(whitelist=[A]), date(2030, 1, 31))['metrics'][A]
    assert metric['point_count'] == 24
    assert metric['points'][0]['date'].startswith('2030-01-08')
    assert metric['points'][-1]['date'].startswith('2030-01-31')


def test_newest_invalid_revision_never_falls_back_to_an_old_value_at_the_same_stamp(facts):
    point(facts, A, 1, 2)
    point(facts, A, 2, 3)
    point(facts, A, 3, 4)
    point(facts, A, 3, 999, revision=1, status='无效')
    metric = research.judgment_research_view(facts, saved(whitelist=[A]), DAY)['metrics'][A]
    assert metric['latest'] == 3 and metric['previous'] == 2
    assert metric['date'].startswith('2030-01-02')
    assert [p['value'] for p in metric['points']] == [2, 3]


def test_pending_conditions_include_business_observations_not_system_diagnostics(facts):
    row = saved(whitelist=[A])
    row.unstructured = [
        {'section': 'Diagnostic', 'text': 'Internal parser failure', 'reason': 'Unit problem',
            'business_condition': None, 'rule_kind': None},
        {'section': 'Observation', 'text': 'Original measurable hypothesis', 'reason': 'Needs confirmation',
            'business_condition': 'Quantity remains above the original level.', 'rule_kind': 'threshold'},
        {'section': 'falsifiers', 'text': 'Historical observation retained', 'reason': 'Legacy record'},
    ]
    view = research.judgment_research_view(facts, row, DAY)
    assert [item['text'] for item in view['pending_conditions']] == [
        'Quantity remains above the original level.', 'Historical observation retained']
    assert view['counts']['pending'] == 2


def test_missing_bound_metadata_and_nonfinite_changes_are_not_fabricated(facts):
    missing = 'kb-' + 'd' * 48
    point(facts, A, 1, -1.7e308)
    point(facts, A, 2, 1.7e308)
    view = research.judgment_research_view(facts, saved(whitelist=[A, missing]), DAY)
    assert view['metrics'][missing]['latest'] is None
    assert view['metrics'][missing]['name'] == '指标名称待核对'
    assert view['metrics'][A]['change'] is None
    assert all(0 <= point['y'] <= 48 for point in view['metrics'][A]['points'])


def test_metric_labels_do_not_expose_a_catalogs_internal_id_annotation(facts):
    facts.get(Indicator, A).name = 'Synthetic quantity [' + A + ']'
    facts.commit()
    view = research.judgment_research_view(facts, saved(whitelist=[A]), DAY)
    assert view['metrics'][A]['name'] == 'Synthetic quantity'
