"""A report is the collection of saved 研判 and 策略, with notes and charts as aids."""
from copy import deepcopy
from dataclasses import replace
from datetime import date

from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope
from tin.web import report_period
from tin.web.report_view import present_snapshot, readable_markdown

FIELD = 'kb-' + 'c' * 48


def payload():
    monitor = {'conditions': [
        {'title': 'Volatility guard', 'state': '触发', 'field_label': '暂停 / 撤回', 'condition': '> 19', 'value': 19.45,
         'unit': '点', 'as_of': '2026-10-24', 'note': '已越线', 'outdated': False,
         'chart': {'segments': ['0,40 120,20 240,5'], 'points': [{'x': 240, 'y': 5}]}},
        {'title': 'Trend guard', 'state': '未触发', 'field_label': '暂停 / 撤回', 'condition': '下穿 MA20', 'value': 7757.17,
         'unit': '未注明', 'as_of': '2026-10-22', 'note': '', 'outdated': True, 'chart': None}],
        'indicators': [{'title': 'Spot premium', 'field_label': '适用 / 触发', 'note': '', 'chart': {
            'latest': 2610, 'unit': '元/吨', 'change': 0, 'date': '2026-09-22T00:00:00+08:00',
            'segments': ['0,10 240,10'], 'points': [{'x': 240, 'y': 10}],
            'freshness': {'age_days': 32, 'usual_interval_days': 1, 'stale': True}}}]}
    return {'as_of': '2026-10-24', 'period': {'kind': 'daily', 'start': '2026-10-24', 'end': '2026-10-24'},
        'observations': [{'series_id': FIELD, 'name': 'Measured supply', 'value': 17500, 'unit': '元/吨', 'as_of': '2026-09-22'}],
        'judgment': {'id': 3, 'version': 3, 'status': '草稿', 'tone': '区间震荡', 'tone_note': 'Range trading; buy dips.',
            'contradiction': {'bull': {'claim': 'Supply is tight', 'evidence': [{'desc': 'Processing fee is low', 'series_id': FIELD}]},
                              'bear': {'claim': 'Demand is soft', 'evidence': []}},
            'thresholds': [], 'falsifiers': [],
            'unstructured': [{'section': 'falsifiers', 'text': '- Imports recover to ten thousand tonnes'}]},
        'research_cards': [{'id': 'card-1', 'title': 'Buy the range low', 'direction': 'range', 'direction_label': '区间观察',
            'status_label': '个人草稿', 'trigger': 'Price returns to the range low', 'response_action': 'Half size first',
            'invalidation': 'Cut when volatility rises', 'review_date': '2026-11-20', 'thesis': '', 'watch_for': '',
            'citations': [{'kind': 'indicator'}], 'monitor': monitor}],
        'strategies': [], 'briefs': [], 'signals': [], 'gaps': [], 'derived': []}


def test_view_summarises_saved_research_and_cited_strategy_conditions():
    source = payload()
    original = deepcopy(source)
    view = present_snapshot(source)
    assert view['conclusion'] == {'tone': '区间震荡', 'text': 'Range trading; buy dips.', 'source': '已保存研判回放'}
    assert view['strategy_overview'] == {'text': '1 个策略方案 · 1 项所引条件已触发',
                                         'triggered': ['Buy the range low：Volatility guard']}
    card = view['research_cards'][0]['monitor_view']
    assert card['triggered'] == 1 and card['conditions'][0]['value_text'] == '19.45 点'
    assert card['conditions'][1]['value_text'] == '7757.17' and card['conditions'][1]['outdated'] is True
    indicator = card['indicators'][0]
    assert indicator['value_text'] == '2610 元/吨' and indicator['as_of'] == '2026-09-22'
    assert indicator['stale_note'] == '最新数据距今 32 天，可能已停更'
    assert [row['text'] for row in view['pending_conditions']] == ['Imports recover to ten thousand tonnes']
    assert view['latest_values'][FIELD] == {'value_text': '17500 元/吨', 'as_of': '2026-09-22'}
    assert all(panel['text'] == '' for panel in view['panels'].values())
    assert source == original


def test_markdown_orders_lead_research_strategy_and_sources():
    text = readable_markdown(payload(), title='Fixture daily')
    order = [text.index(marker) for marker in ['\n## 导语\n', '\n## 研判\n', '\n## 策略\n', '\n## 数据缺口与来源\n']]
    assert order == sorted(order)
    lead = text.split('\n## 导语\n', 1)[1].split('\n## 研判\n', 1)[0]
    assert '区间震荡，Range trading; buy dips.' in lead and '1 项所引条件已触发' in lead
    strategy = text.split('\n## 策略\n', 1)[1].split('<details>', 1)[0]
    assert '所引条件「Volatility guard」（暂停 / 撤回）：已触发；当前 19.45 点 · 条件 &gt; 19 · 数据 2026-10-24 · 已越线' in strategy
    assert '所引指标「Spot premium」：最新 2610 元/吨 · 较前值 0 · 数据 2026-09-22 · 最新数据距今 32 天，可能已停更' in strategy
    research = text.split('\n## 研判\n', 1)[1].split('\n## 策略\n', 1)[0]
    assert '- [ ] Imports recover to ten thousand tonnes' in research and '最新 17500 元/吨 · 2026-09-22' in research
    assert FIELD not in text.split('\n## 数据缺口与来源\n', 1)[0]


def test_hidden_sections_and_notes_follow_the_personal_draft():
    from tin.web.report_drafts import FORMAT
    source = payload()
    sections = {key: {'visible': key != 'watch', 'text': 'A dated note' if key == 'research' else ''}
                for key in ['summary', 'research', 'dashboard', 'watch']}
    source['manual_report'] = {'id': 'draft-1', 'revision': 2, 'draft': {'format': FORMAT, 'period': 'daily',
        'date': '2026-10-24', 'title': 'Personal daily', 'sections': sections, 'figures': [], 'references': [],
        'reference_snapshots': [], 'source_revision': 'a' * 64}}
    text = readable_markdown(source)
    assert text.startswith('# Personal daily') and '\n## 策略\n' not in text
    research = text.split('\n## 研判\n', 1)[1]
    assert research.index('研究员按语') < research.index('### 研究判断') and 'A dated note' in research
    assert 'Supply is tight' in research, 'Saved 研判 stays in the report beside the note'


def test_report_attaches_card_monitor_only_for_a_current_source_scope(monkeypatch):
    from tin.web import strategy_monitor
    calls = []
    monkeypatch.setattr(strategy_monitor, 'monitor_cards', lambda scope, cards, day: calls.append((cards, day)) or {
        'card-1': {'conditions': [], 'indicators': []}})
    scope = WorkbenchScope(Identity('owner', '', 'Fixture', 17, 'owner'), '7ac25ec4-378c-4071-a8ee-c3679617b67a',
        'fixture-kb', 17, {'workspace_mode': 'isolated', 'can_edit': True, 'can_read_source': True}, 'Bearer fixture')
    result = {'coverage': {}, 'research_cards': [{'id': 'card-1', 'citations': [{'kind': 'indicator'}]},
                                                 {'id': 'card-2', 'citations': []}]}
    report_period._attach_card_monitor(result, scope, date(2026, 10, 24))
    assert calls == []
    report_period._attach_card_monitor(result, replace(scope, scope_version=2), date(2026, 10, 24))
    assert calls == [([{'id': 'card-1', 'card': {'citations': [{'kind': 'indicator'}]}}], date(2026, 10, 24))]
    assert result['research_cards'][0]['monitor'] == {'conditions': [], 'indicators': []}
    assert 'monitor' not in result['research_cards'][1]


def test_overview_counts_cited_indicators_when_no_condition_is_cited():
    source = payload()
    source['research_cards'][0]['monitor']['conditions'] = []
    view = present_snapshot(source)
    assert view['strategy_overview'] == {'text': '1 个策略方案 · 跟踪 1 个引用指标', 'triggered': []}
    source['research_cards'][0]['monitor']['conditions'] = [{'title': 'Quiet', 'state': '未触发'}]
    assert present_snapshot(source)['strategy_overview']['text'] == '1 个策略方案 · 所引 1 项条件暂无触发'
