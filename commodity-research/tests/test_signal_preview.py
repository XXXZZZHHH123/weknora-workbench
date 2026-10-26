"""Rule preview reuses the production evaluator but cannot persist results."""
from copy import deepcopy
from datetime import date

import pytest
from sqlalchemy import event, func, inspect, select

from tests.conftest import D
from tests.test_signals import activate, by_rule, enter, week
from tin.models import Indicator, Researcher, ReviewTask, Signal, Strategy


def _content(row):
    return {key: deepcopy(getattr(row, key)) for key in
            ('rule_id', 'rule_type', 'trade_date', 'state', 'current_value', 'evidence', 'gap_note')}


def test_preview_matches_the_same_pipeline_rules_without_reading_or_mutating_signal_rows(loaded, monkeypatch):
    from tin.compute.signals import evaluate, evaluate_preview
    activate(loaded)
    evaluate(loaded, 'SN', D)
    original = {row.id: (_content(row), row.generated_at) for row in loaded.scalars(select(Signal))}
    for day, value in ((4, 5000), (11, 5200), (18, 5450)):
        enter(loaded, 'MYSTEEL.SN.stock.social', value, week(day))
    statements = []
    def observe(connection, cursor, statement, parameters, context, many):
        statements.append(statement)
    event.listen(loaded.bind, 'before_cursor_execute', observe)
    def no_write(*args, **kwargs):
        pytest.fail('Readonly preview attempted an ORM write/flush/commit')
    try:
        with monkeypatch.context() as patch:
            for name in ('add', 'add_all', 'commit', 'flush'):
                patch.setattr(loaded, name, no_write)
            preview = evaluate_preview(loaded, 'SN', D)
    finally:
        event.remove(loaded.bind, 'before_cursor_execute', observe)
    assert preview and all(inspect(row).transient and row.id is None for row in preview)
    assert all('signals' not in statement.lower() for statement in statements)
    assert all(statement.lstrip().lower().startswith('select') for statement in statements)
    assert {row.id: (_content(row), row.generated_at) for row in loaded.scalars(select(Signal))} == original
    assert loaded.scalar(select(func.count()).select_from(ReviewTask)) == 0
    assert loaded.scalar(select(func.count()).select_from(Strategy)) == 0
    actual = evaluate(loaded, 'SN', D)
    assert {row.rule_id: _content(row) for row in preview} == {row.rule_id: _content(row) for row in actual}
    assert by_rule(preview)['fl_social_stock_2w'].state == '触发'


def test_preview_missing_windows_never_become_untriggered(loaded):
    from tin.compute.signals import evaluate_preview
    activate(loaded)
    enter(loaded, 'MYSTEEL.SN.stock.social', 5000, week(4))
    enter(loaded, 'MYSTEEL.SN.stock.social', 5200, week(11))
    result = by_rule(evaluate_preview(loaded, 'SN', D))
    assert result['fl_social_stock_2w'].state == '数据缺失'
    assert '需要 3 个数据点' in result['fl_social_stock_2w'].gap_note
    assert result['fl_myanmar'].state == '数据缺失'
    assert loaded.scalar(select(func.count()).select_from(Signal)) == 0


@pytest.mark.parametrize('unit,confirmed', [('千克',None), ('',None), ('实物吨','')])
def test_shared_falsifier_rejects_changed_units_in_pipeline_and_preview(loaded, unit, confirmed):
    from tin.compute.signals import evaluate, evaluate_preview
    judgment = activate(loaded)
    enter(loaded, 'CUSTOMS.2609.import.MM', 12300, week(1))
    loaded.get(Indicator, 'CUSTOMS.2609.import.MM').unit = unit
    if confirmed is not None:
        judgment.falsifiers = [{**row, 'unit_confirmed': confirmed} if row['id']=='fl_myanmar' else row
                              for row in judgment.falsifiers]
    loaded.commit()
    preview = by_rule(evaluate_preview(loaded, 'SN', D))['fl_myanmar']
    assert preview.state == '数据缺失' and preview.current_value is None
    assert '单位' in preview.gap_note
    assert ('千克' in preview.gap_note and '吨' in preview.gap_note) if unit=='千克' else '未注明' in preview.gap_note
    assert loaded.scalar(select(func.count()).select_from(Signal)) == 0
    actual = by_rule(evaluate(loaded, 'SN', D))['fl_myanmar']
    assert _content(actual) == _content(preview)


def test_existing_pipeline_rejects_unit_drift_before_numeric_falsifier_comparison(loaded):
    from tin.compute.signals import evaluate
    activate(loaded)
    enter(loaded, 'CUSTOMS.2609.import.MM', 12300, week(1))
    loaded.get(Indicator, 'CUSTOMS.2609.import.MM').unit = '千克'
    loaded.commit()
    row = by_rule(evaluate(loaded, 'SN', D))['fl_myanmar']
    assert row.state == '数据缺失' and '单位' in row.gap_note


def test_preview_refuses_a_rule_window_above_its_page_budget(loaded):
    from tin.compute.signals import evaluate_preview
    judgment = activate(loaded)
    judgment.falsifiers = [{**judgment.falsifiers[0], 'consecutive_periods':121}]
    loaded.commit()
    with pytest.raises(ValueError, match='120'):
        evaluate_preview(loaded, 'SN', D)
    assert loaded.scalar(select(func.count()).select_from(Signal)) == 0


def test_preview_flag_restores_after_a_bad_legacy_rule(loaded):
    from tin.compute.signals import evaluate, evaluate_preview
    judgment = activate(loaded)
    original = deepcopy(judgment.falsifiers)
    judgment.falsifiers = [{**original[0], 'expression': 'bad legacy expression'}]
    loaded.commit()
    with pytest.raises(ValueError):
        evaluate_preview(loaded, 'SN', D)
    assert loaded.scalar(select(func.count()).select_from(Signal)) == 0
    judgment.falsifiers = original
    loaded.commit()
    rows = evaluate(loaded, 'SN', D)
    assert rows and all(inspect(row).persistent for row in rows)
    assert loaded.scalar(select(func.count()).select_from(Signal)) == len(rows)


def test_first_time_reader_does_not_create_a_researcher_or_claim_unassigned_logic(loaded):
    from tin.compute.signals import evaluate_preview
    activate(loaded)
    before = loaded.scalar(select(func.count()).select_from(Researcher))
    loaded.info['weknora_user_id'] = 'new-reader-without-profile'
    assert evaluate_preview(loaded, 'SN', D) == []
    assert loaded.scalar(select(func.count()).select_from(Researcher)) == before
    assert loaded.scalar(select(func.count()).select_from(Signal)) == 0


def test_preview_for_a_known_user_does_not_return_another_users_rules(loaded):
    from tin.compute.signals import evaluate_preview
    judgment = activate(loaded)
    people = [Researcher(username=name, weknora_user_id=name, display_name=name, varieties=[],
                         created_at=week(1)) for name in ('preview-owner', 'preview-other')]
    loaded.add_all(people); loaded.flush()
    judgment.researcher_id = people[0].id
    loaded.commit()
    loaded.info['weknora_user_id'] = 'preview-owner'
    assert evaluate_preview(loaded, 'SN', D)
    loaded.info['weknora_user_id'] = 'preview-other'
    assert evaluate_preview(loaded, 'SN', D) == []
    assert loaded.scalar(select(func.count()).select_from(Signal)) == 0


def test_report_keeps_saved_signals_but_presents_separate_current_draft_preview(loaded):
    from tin.compute.signals import evaluate
    from tin.web.report_period import build_period
    from tin.web.report_view import present_snapshot, readable_markdown
    judgment = activate(loaded)
    evaluate(loaded, 'SN', D)
    judgment.status = '草稿'
    loaded.commit()
    for day, value in ((4, 5000), (11, 5200), (18, 5450)):
        enter(loaded, 'MYSTEEL.SN.stock.social', value, week(day))
    original = {row.id: _content(row) for row in loaded.scalars(select(Signal))}
    report = build_period(loaded, 'SN', D)
    assert report['rule_preview']['mode'] == 'readonly_preview'
    assert report['rule_preview']['judgment_status'] == '草稿'
    assert '草稿' in report['rule_preview']['label']
    assert next(row for row in report['signals'] if row['rule_id'] == 'fl_social_stock_2w')['state'] == '数据缺失'
    view = present_snapshot(report)
    rule = next(row for row in view['judgment']['falsifiers'] if row['id'] == 'fl_social_stock_2w')
    assert rule['state'] == '触发' and rule['recorded_state'] == '数据缺失' and rule['is_preview'] is True
    assert rule['evidence_dates']
    assert {row.id: _content(row) for row in loaded.scalars(select(Signal))} == original
    markdown = readable_markdown(report)
    assert '规则试算' in markdown and '不是历史当时已知结论' in markdown


def test_supplied_archive_snapshot_is_never_recalculated(loaded, monkeypatch):
    from tin.compute import signals
    from tin.export.board import snapshot
    from tin.web.report_period import build_period
    activate(loaded)
    original = snapshot(loaded, 'SN', D)
    original['signals'] = [{'rule_id':'saved-rule','state':'保存的历史状态'}]
    monkeypatch.setattr(signals, 'evaluate_preview', lambda *args, **kwargs: pytest.fail('An archive must not be recalculated'))
    result = build_period(loaded, 'SN', D, source=original)
    assert result['signals'] == original['signals']
    assert 'rule_preview' not in result
