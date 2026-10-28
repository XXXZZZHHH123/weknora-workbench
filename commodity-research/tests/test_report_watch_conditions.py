"""Research watch conditions stay distinct from import diagnostics."""
from copy import deepcopy

from tin.web.report_view import present_snapshot, readable_markdown


FIELD = 'kb-' + 'a' * 48


def sample():
    return {'as_of': '2026-01-09', 'period': {'kind': 'daily', 'start': '2026-01-09', 'end': '2026-01-09'},
        'observations': [{'series_id': FIELD, 'name': '港口库存 / ID123456 / 周', 'value': 7, 'unit': '件'}],
        'judgment': {'unstructured': [
            {'section': 'whitelist', 'text': 'a12345678', 'reason': '目录尚未绑定'},
            {'section': 'thresholds', 'text': FIELD + ' 是否高于 10', 'reason': '模型返回了错误字段'},
            {'section': 'falsifiers', 'text': '观察库存是否连续两周增加', 'reason': '尚未实现连续期接口'},
            {'section': '/binding_units/' + FIELD, 'text': FIELD, 'reason': '接口缺少单位'}]},
        # Old report payloads used to mix both sorts of text into this array.
        'next_review': [{'kind': 'unconfirmed', 'text': FIELD + ' · 模型返回了错误字段'}]}


def test_watch_uses_original_research_conditions_not_import_diagnostics():
    payload = sample()
    original = deepcopy(payload)
    view = present_snapshot(payload)
    text = view['panels']['watch']['text']
    assert '港口库存' in text and '是否高于 10' in text
    assert '观察库存是否连续两周增加' in text
    for diagnosis in ['模型返回', '错误字段', '连续期接口', '目录尚未绑定', 'binding_units', 'a12345678']:
        assert diagnosis not in text
    assert FIELD not in text and 'ID123456' not in text
    assert payload == original


def test_watch_export_and_default_editor_use_same_human_readable_conditions():
    payload = sample()
    view = present_snapshot(payload)
    assert view['editable_draft']['sections']['watch']['text'] == view['panels']['watch']['text']
    markdown = readable_markdown(payload)
    watch = markdown.split('## 待关注\n\n', 1)[1].split('<details>', 1)[0]
    assert FIELD not in watch and 'ID123456' not in watch and '模型返回' not in watch
    assert '连续两周增加' in watch and '是否高于 10' in watch


def test_watch_keeps_quantitative_rules_strategy_conditions_and_human_labels():
    payload = {'as_of': '2026-01-09', 'judgment': {
        'thresholds': [{'id': 'private-rule-id', 'name': '', 'series_id': FIELD,
            'operator': 'between', 'value': [0, 10]}],
        'falsifiers': [{'id': 'private-falsifier', 'desc': '供应压力持续增加', 'series_id': FIELD,
            'expression': 'change >= 2', 'consecutive_periods': 2, 'unit_confirmed': '件'}]},
        'strategies': [{'title': '模拟方案 AB2601', 'status': 'draft', 'invalidation': '需求反弹则复核'}],
        'research_cards': [{'title': '跟踪计划', 'trigger': '库存回落后再评估',
            'invalidation': '供给恢复则撤回', 'review_date': '2026-01-12'}]}
    view = present_snapshot(payload, {FIELD: '库存 / 流量比率'})
    text = view['panels']['watch']['text']
    assert '库存 / 流量比率' in text and '0' in text and '10' in text
    assert '连续 2 期' in text and '较上一期变化' in text
    assert '需求反弹则复核' in text and '库存回落后再评估' in text and '供给恢复则撤回' in text
    assert 'AB2601' in text
    assert FIELD not in text and 'private-rule-id' not in text and 'private-falsifier' not in text


def test_watch_unknown_field_hides_protocol_id_without_guessing_a_source():
    payload = {'as_of': '2026-01-09', 'judgment': {'unstructured': [
        {'section': 'falsifiers', 'text': FIELD + '连续两个观测期上升', 'reason': '缺少当前数据'}]}}
    text = present_snapshot(payload)['panels']['watch']['text']
    assert FIELD not in text and '连续两个观测期上升' in text
    assert '缺少当前数据' not in text


def test_watch_resolves_known_legacy_ids_and_keeps_the_observed_unit():
    legacy = 'fixture.inventory'
    payload = {'as_of': '2026-01-09', 'observations': [
        {'series_id': legacy, 'name': '库存', 'unit': '吨', 'value': 0}],
        'judgment': {'thresholds': [{'name': '复核补库条件', 'series_id': legacy,
            'operator': '>=', 'value': 10}], 'unstructured': [
                {'section': 'falsifiers', 'text': legacy + '连续两期上升', 'reason': '系统未支持'}]}}
    text = present_snapshot(payload)['panels']['watch']['text']
    assert '库存不低于 10吨' in text and '库存连续两期上升' in text
    assert legacy not in text and '系统未支持' not in text


def test_manual_watch_is_presented_with_names_without_rewriting_stored_text():
    from tin.web.report_drafts import FORMAT
    payload = sample()
    saved = {'format': FORMAT, 'period': 'daily', 'date': '2026-01-09', 'title': '人工报告',
        'sections': {key: {'visible': True, 'text': FIELD + '连续两周上升' if key == 'watch' else ''}
                     for key in ['summary', 'research', 'dashboard', 'watch']},
        'figures': [], 'references': [], 'reference_snapshots': []}
    payload['manual_report'] = {'revision': 1, 'draft': saved}
    original = deepcopy(payload)
    view = present_snapshot(payload)
    assert '港口库存' in view['panels']['watch']['text'] and FIELD not in view['panels']['watch']['text']
    assert payload == original and view['manual_report']['draft']['sections']['watch']['text'].startswith(FIELD)
