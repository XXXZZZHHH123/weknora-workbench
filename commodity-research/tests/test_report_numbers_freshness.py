"""Report values share precision rules and stale evidence keeps its conclusion."""
from copy import deepcopy
from datetime import date

import pytest

from tin.web.number_format import format_number


@pytest.mark.parametrize('value,expected', [
    (2574.064978, '2,574'), (97.9695486, '97.97'), (116.58, '116.6'),
    (-2574.9, '-2,575'), (-116.58, '-116.6'), (-12.3456, '-12.35'),
    (1, '1'), (0, '0'), (-0.0, '0'), (0.123456, '0.1235'),
    (-0.00123456, '-0.001235'), (0.000000123456, '0.0000001235'),
    (None, '—'), (float('nan'), '—'), (float('inf'), '—')])
def test_shared_report_precision_by_magnitude(value, expected):
    assert format_number(value) == expected


@pytest.mark.parametrize('value,digits,expected', [
    (2574.064978, 3, '2,574.065'), (116.58, 2, '116.58'),
    (-97.9695486, 0, '-98'), (1.2, 4, '1.2'), (0, 3, '0')])
def test_explicit_source_precision_and_trailing_zeros(value, digits, expected):
    assert format_number(value, decimals=digits) == expected


def _payload():
    return {'as_of': '2024-03-29', 'observations': [
        {'series_id': 'fixture.a', 'name': '样例指标', 'unit': '吨', 'value': 116.58,
         'as_of': '2023-08-31', 'caliber': {'display_decimals': 2}}],
        'judgment': {'status': '生效', 'tone': '中性', 'contradiction': {
            'bull': {'claim': '已保存支持', 'evidence': [{'desc': '依据一', 'series_id': 'fixture.a'}]},
            'bear': {'claim': '已保存反对', 'evidence': [{'desc': '依据二', 'series_id': 'fixture.a'}]}},
            'thresholds': [{'id': 'a', 'name': '区间一', 'series_id': 'fixture.a',
                'operator': 'between', 'value': [97.9695486, 2574.064978]}], 'falsifiers': []},
        'signals': [{'rule_id': 'a', 'current_value': 116.58, 'state': '在区间内'}]}


def test_stale_judgment_evidence_and_threshold_share_strategy_rule_and_markdown():
    from tin.web.report_view import present_snapshot, readable_markdown
    from tin.web.strategy_monitor import _staleness
    payload = _payload()
    before = deepcopy(payload)
    view = present_snapshot(payload)
    fresh = _staleness({'date': '2023-08-31', 'points': []}, date(2024, 3, 29))
    note = f"最新数据距今 {fresh['age_days']} 天，可能已停更"
    assert note == view['latest_values']['fixture.a']['stale_note']
    threshold = view['judgment']['thresholds'][0]
    assert threshold['stale_note'] == note
    assert threshold['state'] == '在区间内'
    assert threshold['current_value_text'] == '116.58'
    assert threshold['condition'] == '97.97 – 2,574.06'
    text = readable_markdown(payload)
    assert text.count(note) == 3
    assert '当前值：116.58' in text
    assert '最新 116.58 吨' in text
    assert payload == before


def test_judgment_freshness_uses_own_observation_interval_without_new_reads():
    from tin.web.report_view import present_snapshot
    payload = _payload()
    payload['as_of'] = '2023-09-20'
    payload['judgment_metrics'] = {'fixture.a': {'date': '2023-08-31', 'freshness': {
        'age_days': 20, 'usual_interval_days': 30, 'stale': False}}}
    assert present_snapshot(payload)['latest_values']['fixture.a']['stale_note'] == ''


def test_build_period_uses_observed_spacing_and_cutoff_for_judgment_freshness(session, monkeypatch):
    from tin.models import Indicator
    from tin.web.report_period import build_period
    from tests.test_report_period import _observation
    from tin.web import report_period
    monkeypatch.setattr(report_period, '_rule_preview', lambda *args: [])
    payload = _payload()
    payload['judgment']['thresholds'] = []
    session.add(Indicator(series_id='fixture.a', name='样例指标', variety='SN', unit='吨',
        category='测试', caliber={'display_decimals': 2}, source='fixture', frequency='月', fetch_mode='readonly'))
    session.flush()
    for day in ('2023-07-31T15:00', '2023-08-31T15:00', '2024-04-01T15:00'):
        _observation(session, 'fixture.a', day, 116.58)
    session.commit()
    result = build_period(session, 'SN', date(2024, 3, 29), source=payload, preview_rules=True)
    fresh = result['judgment_metrics']['fixture.a']['freshness']
    assert fresh['usual_interval_days'] == 31
    assert fresh['stale'] is True
    assert result['judgment_metrics']['fixture.a']['date'].startswith('2023-08-31')


def test_html_formats_evidence_and_warns_without_hiding_it(loaded, monkeypatch, tmp_path):
    import json
    from fastapi.testclient import TestClient
    from tin.config import settings
    from tin.web import app as web
    payload = _payload()
    payload['generated_at'] = '2024-03-29T16:00:00+08:00'
    monkeypatch.setattr(settings, 'weknora_api_url', '')
    monkeypatch.setattr(settings, 'exports_dir', tmp_path)
    monkeypatch.setattr(web, 'SessionLocal', lambda: loaded)
    folder = tmp_path / web.V
    folder.mkdir()
    (folder / '2024-03-29.json').write_text(json.dumps(payload))
    monkeypatch.setattr(web, 'archive_days', lambda *_: [{'date': '2024-03-29',
        'weekday': '五', 'tone': None, 'main': None, 'gaps': 0, 'generated_at': payload['generated_at']}])
    response = TestClient(web.app).get('/sn/reports')
    assert response.status_code == 200
    main = response.text.split('原始 JSON', 1)[0]
    assert main.count('最新数据距今 211 天，可能已停更') == 3
    assert '最新 116.58 吨' in main
    assert '在区间内' in main
    assert '97.97 – 2,574.06' in main
    assert '97.9695486' not in main


def test_authorized_projection_precision_reaches_report_and_monitor_without_rounding_values(session, monkeypatch):
    from tin.models import Indicator
    from tin.web.judgment_research import _metric
    from tin.web.report_period import build_period
    from tin.web.report_view import present_snapshot
    from tests.test_report_period import _observation
    identifier = 'fixture.precision'
    session.add(Indicator(series_id=identifier, name='精度样例', variety='SN', unit='吨',
        category='测试', caliber={}, source='fixture', frequency='日', fetch_mode='readonly'))
    session.flush()
    _observation(session, identifier, '2026-01-05T15:00', 2574.064978)
    _observation(session, identifier, '2026-01-09T15:00', 2575.389012)
    session.commit()
    session.info['knowledge_facts_display_precision'] = {
        identifier: {'2026-01-05': 3, '2026-01-09': 3}}
    payload = build_period(session, 'SN', date(2026, 1, 9), 'weekly', source={
        'as_of': '2026-01-09', 'observations': [{'series_id': identifier, 'value': 2575.389012,
            'unit': '吨', 'as_of': '2026-01-09'}]})
    view = present_snapshot(payload)
    assert view['latest_values'][identifier]['value_text'] == '2,575.389 吨'
    assert view['changes'][0]['first_text'] == '2,574.065'
    assert view['changes'][0]['last_text'] == '2,575.389'
    assert view['changes'][0]['change_text'] == '1.324'
    assert payload['observations'][0]['value'] == 2575.389012
    assert _metric(session, identifier, date(2026, 1, 9))['display_decimals'] == 3


def test_report_formats_strategy_condition_and_entry_from_known_source_precision():
    from tin.web.report_view import present_snapshot, readable_markdown
    payload = _payload()
    payload['strategies'] = [{'title': '已存交易计划', 'status_label': '等待入场', 'mode_label': '模拟跟踪',
        'legs': [{'quote_series_id': 'fixture.a'}], 'entry': {'low': 2574.064978, 'high': 2575.389012}}]
    payload['research_cards'] = [{'id': 'card', 'title': '条件卡', 'trigger': '', 'response_action': '',
        'invalidation': '', 'review_date': '', 'direction_label': '观察',
        'monitor': {'conditions': [{'title': '条件一', 'value': 2574.064978, 'unit': '吨',
            'condition_definition': {'operator': 'between', 'value': [97.9695486, 2574.064978]},
            'chart': {'display_decimals': 3}}], 'indicators': [
                {'title': '指标一', 'chart': {'latest': 2574.064978, 'display_decimals': 3}}]}}]
    before = deepcopy(payload)
    view = present_snapshot(payload)
    assert view['strategies'][0]['entry']['low_text'] == '2,574.06'
    assert view['strategies'][0]['entry']['high_text'] == '2,575.39'
    condition = view['research_cards'][0]['monitor_view']['conditions'][0]
    assert condition['value_text'] == '2,574.065 吨'
    assert condition['condition'] == '97.97 – 2,574.065'
    assert '当前 2,574.065 吨' in readable_markdown(payload)
    assert payload == before


def test_projection_preserves_optional_precision_already_returned_by_authorized_reader(tmp_path, monkeypatch):
    from tin.integration import page_facts
    from tin.integration.workbench_scope import WorkbenchScope
    from tin.web.judgment_research import _metric
    from tests.test_workbench_sources_v2 import IDENTITY, WB, source
    from tin.db import Base, make_engine
    engine = make_engine('sqlite:///' + str(tmp_path / 'projection.db'))
    Base.metadata.create_all(engine)
    identifier = 'kb-' + 'a' * 48
    scope = WorkbenchScope(IDENTITY, WB, '', 0, {'scope_version': 2}, 'Bearer fixture',
        scope_version=2, sources_revision=7, sources=(source(1),))
    monkeypatch.setattr(page_facts, '_saved_plan', lambda *args: ([{'ids': [identifier], 'points': 2}], {}))
    calls = []
    def bounded(*args, **kwargs):
        calls.append(kwargs)
        return {'version': 'a' * 64, 'source_versions': {}, 'indicators': [{
            'series_id': identifier, 'metric': '精度样例', 'unit': '吨',
            'source_knowledge_id': 'fixture', 'dataset_id': 'fixture', 'source_series_id': 'field'}],
            'rows': [{'series_id': identifier, 'date': '2026-01-09', 'value': 2574.064978,
                'value_status': 'valid', 'display_decimals': 3}]}
    monkeypatch.setattr(page_facts, 'bounded_facts', bounded)
    with page_facts.page_read_scope('reports', day='2026-01-09'):
        with page_facts.projected_page_session(engine, scope) as facts:
            assert facts.info['knowledge_facts_display_precision'] == {identifier: {'2026-01-09': 3}}
            metric = _metric(facts, identifier, date(2026, 1, 9))
            assert metric['display_decimals'] == 3
            assert metric['latest'] == 2574.064978
    assert len(calls) == 1
    assert not calls[0].get('include_display_precision')
