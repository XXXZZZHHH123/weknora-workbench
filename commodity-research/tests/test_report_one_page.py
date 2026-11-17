"""Display-only selection and shared report projections use synthetic observations."""
from copy import deepcopy

from tin.web.report_one_page import dimension_overview, evidence_groups, select_facts
from tin.web.report_view import present_snapshot, readable_markdown


def dimension(identifier, *items):
    return {'id': identifier, 'label': '自定' + identifier, 'category': '其他', 'description': '',
            'sections': [{'id': 'main', 'label': '环节', 'items': list(items)}]}


def card(sid, rate, rank=None, n=4, value=23.4567):
    return {'kind': 'indicator', 'id': sid, 'series_id': sid, 'name': '指标' + sid, 'status': 'ok',
            'latest': value, 'unit': '件', 'date': '2026-11-13', 'change': 1.234,
            'change_pct': rate, 'display_decimals': 2,
            'baseline': {'date': '2026-11-06', 'value': 22.2227},
            'position': {'rank': rank, 'rank_low': 6-rank if rank else None, 'n': n,
                         'text': f'近 5 年同期第 {rank} 高' if rank else ''}}


def source():
    dimensions = [dimension('custom_b', card('b', .2)), dimension('custom_a'), dimension('custom_c')]
    return {'as_of': '2026-11-13', 'observations': [], 'research_dimensions': dimensions,
            'dimension_data': {'dimensions': dimensions},
            'judgment': {'status': '生效', 'tone': '中性', 'tone_note': '保存的基调',
                'contradiction': {'text': '保存的主要矛盾', 'bull': {'evidence': [
                    {'desc': '原文依据', 'dimension': 'custom_a'}, {'desc': '无标签'},
                    {'desc': '历史编号', 'dimension': 'deleted-id'}]}, 'bear': {'evidence': []}},
                'thresholds': [], 'falsifiers': []}}


def note(payload, notes, conclusions=None):
    from tin.web.report_drafts import FORMAT
    payload['manual_report'] = {'id': 'fixture', 'revision': 1, 'draft': {
        'format': FORMAT, 'date': payload['as_of'], 'period': 'daily', 'title': '合成报告',
        'sections': {key: {'visible': True, 'text': ''} for key in ('summary', 'research', 'dashboard', 'watch')},
        'figures': [], 'references': [], 'dimension_notes': notes, 'figure_conclusions': conclusions or {}}}


def test_extremes_then_absolute_rate_stable_dedup_and_limit():
    items = [card('large', -.9), card('small', .1, rank=2), card('largest', .95, rank=3),
             card('extreme', -.2, rank=1), card('last', None), card('ignored', 4, n=1)]
    items += [deepcopy(items[1]), {**card('bad', 9), 'status': 'unavailable'}]
    rows = select_facts(dimension('custom', *items))
    assert [row['id'] for row in rows] == ['extreme', 'small', 'ignored', 'largest']
    assert rows[0]['text'] == '指标extreme 23.46 件（较 11-06 +1.23，近 5 年同期第 1 高）'
    assert '近' not in select_facts(dimension('custom', card('short', .1, rank=1, n=1)))[0]['text']


def test_no_update_is_not_a_flat_change_and_updated_facts_are_preferred():
    old = {**card('old', .99, rank=1), 'update_status': 'no_update', 'direction': 'no_update',
           'date': '2026-03-21', 'change': 0, 'display': {'latest': '23.46', 'change': '+0'}}
    current = {**card('current', .01), 'update_status': 'updated'}
    facts = select_facts(dimension('custom', old, current))
    assert [fact['id'] for fact in facts] == ['current', 'old']
    assert '本期无新数据，最新 2026-03-21' in facts[1]['text']
    assert '+0' not in facts[1]['text'] and '较 ' not in facts[1]['text']
    assert facts[1]['update_status'] == 'no_update'


def test_all_no_update_summarizes_once_with_two_latest_values_in_markdown():
    items = [{**card(str(i), .1), 'date': f'2026-03-{20 + i:02}',
              'update_status': 'no_update', 'direction': 'no_update'} for i in range(4)]
    dims = [dimension('custom', *items)]
    payload = {'as_of': '2026-11-13', 'observations': [], 'research_dimensions': dims,
               'dimension_data': {'dimensions': dims}}
    row = dimension_overview(payload)[0]
    assert row['no_update'] and len(row['facts']) == 2
    assert row['change_summary'] == '本期无新数据（最新 2026-03-23）'
    markdown = readable_markdown(payload)
    overview = markdown.split('## 本期综述')[1].split('## 逻辑与策略')[0]
    assert overview.count('本期无新数据') == 1
    assert all(fact['latest_text'] in overview for fact in row['facts'])
    assert '较 ' not in overview


def test_readable_fact_names_units_and_explicit_non_numeric_roles():
    named = {**card('named', .1), 'name': 'FEEDcode / 可读指数:级别9 / ID123456', 'unit': '未注明'}
    annotation = {**card('note', 100), 'field_metadata': {'role': 'annotation'}}
    text = {**card('text', 200), 'field_metadata': {'role': 'text_dimension'}}
    chart = {'kind': 'chart', 'id': 'chart', 'indicators': [annotation, text]}
    facts = select_facts(dimension('custom', named, annotation, chart))
    assert len(facts) == 1
    assert facts[0]['name'] == '可读指数:级别9'
    assert facts[0]['raw_name'] == named['name']
    assert facts[0]['unit'] == '' and '未注明' not in facts[0]['text']
    assert 'FEEDcode' not in facts[0]['text'] and 'ID123456' not in facts[0]['text']


def test_staleness_counts_all_numeric_bindings_deduplicates_and_folds_details():
    items = [{**card(str(i), .1), 'freshness': {'stale': True, 'age_days': 100 + i},
              'limited': True, 'history_coverage': {'limited': True},
              'position': {'notice': '同期样本说明'}} for i in range(6)]
    items += [deepcopy(items[0]), {**card('annotation', 1), 'field_metadata': {'role': 'annotation'},
                                 'freshness': {'stale': True, 'age_days': 999}}]
    dims = [dimension('custom', *items)]
    payload = {'as_of': '2026-11-13', 'observations': [], 'research_dimensions': dims,
               'dimension_data': {'dimensions': dims}}
    before = deepcopy(payload)
    row = dimension_overview(payload)[0]
    assert len(row['facts']) == 4
    assert row['warning_summary'] == '6 项停更（最长 105 天）'
    assert len(row['warnings']) == 8
    assert row['warnings'].count('同期样本说明') == 1
    markdown = readable_markdown(payload)
    assert '<details><summary>6 项停更（最长 105 天）</summary>' in markdown
    assert payload == before


def test_bound_chart_operands_supply_facts_without_an_extra_read():
    chart = {'kind': 'chart', 'id': 'chart-1', 'status': 'ok', 'indicators': [card('inside', .4, rank=1)]}
    assert select_facts(dimension('custom', chart))[0]['series_id'] == 'inside'


def test_historical_chart_window_is_evidence_without_becoming_current_change():
    historical = {'kind': 'chart', 'id': 'old-chart', 'status': 'ok', 'as_of': '2021-03-01',
                  'indicators': [card('old', .8)]}
    current = {**historical, 'id': 'current-chart', 'as_of': '2026-11-13',
               'indicators': [card('now', .2)]}
    dims = [dimension('custom', historical, current)]
    payload = {'as_of': '2026-11-13', 'dimension_data': {'dimensions': dims}, 'research_dimensions': dims}
    row = dimension_overview(payload)[0]
    assert [fact['series_id'] for fact in row['facts']] == ['now']
    assert '历史区间图' in row['warnings'][0]


def test_dimension_only_bindings_are_available_for_manual_figure_selection():
    payload = source()
    view = present_snapshot(payload)
    assert not view['observations']
    assert view['all_figure_options'] == [{'series_id': 'b', 'name': '指标b', 'unit': '件'}]
    assert view['figure_options'] == view['all_figure_options']


def test_custom_order_empty_hidden_notes_and_legacy_tag_buckets():
    payload = source()
    rows = dimension_overview(payload)
    assert [row['label'] for row in rows] == ['自定custom_b', '自定custom_a', '未标注', '已删除维度']
    assert rows[1]['support_count'] == 1 and rows[1]['evidence'][0]['text'] == '原文依据'
    note(payload, {'custom_c': {'text': '研究员填写', 'impact': 'neutral'}})
    assert [row['id'] for row in dimension_overview(payload)][:3] == ['custom_b', 'custom_a', 'custom_c']
    assert len(present_snapshot(payload)['editable_dimensions']) == 3


def test_review_only_uses_explicit_impact_and_evidence_sides():
    payload = source()
    for impact, expected in [('bearish', True), ('bullish', False), ('neutral', False), ('undecided', False)]:
        note(payload, {'custom_a': {'text': '与数字无关的人工说明', 'impact': impact}})
        row = next(row for row in dimension_overview(payload) if row['id'] == 'custom_a')
        assert row['review_needed'] is expected
    payload['judgment']['contradiction']['bear']['evidence'] = [{'dimension': 'custom_a', 'desc': '反对'}]
    note(payload, {'custom_a': {'text': '', 'impact': 'bearish'}})
    assert not next(row for row in dimension_overview(payload) if row['id'] == 'custom_a')['review_needed']
    payload['judgment']['contradiction']['bear']['evidence'].append({'dimension': 'custom_a', 'desc': '更多反对'})
    note(payload, {'custom_a': {'text': '', 'impact': 'bullish'}})
    assert next(row for row in dimension_overview(payload) if row['id'] == 'custom_a')['review_needed']


def test_deleted_dimensions_do_not_compare_unrelated_aggregate_evidence_and_notes():
    payload = source()
    note(payload, {'deleted-a': {'text': '原说明甲', 'impact': 'bullish'},
                   'deleted-b': {'text': '原说明乙', 'impact': 'bearish'}})
    row = next(row for row in dimension_overview(payload) if row['label'] == '已删除维度')
    assert row['impact'] == 'undecided' and not row['review_needed']
    assert len(row['historical_notes']) == 2
    assert '原说明甲' in row['sentence'] and '原说明乙' in row['sentence']


def test_grouping_prioritizes_bindings_then_tags_and_preserves_other():
    dims = [dimension('custom_b', {'kind': 'indicator', 'id': 'sid'}),
            dimension('custom_a', {'kind': 'chart', 'id': 'bound-chart'})]
    figures = [
        {'key': 'indicator:sid', 'kind': 'indicator', 'series_id': 'sid', 'title': '手选'},
        {'key': 'chart:bound-chart', 'kind': 'chart', 'reference': {'id': 'bound-chart'}, 'title': '绑定图'},
        {'key': 'chart:by-series', 'kind': 'chart', 'single_series_id': 'sid', 'title': '指标归属图'},
        {'key': 'chart:tagged', 'kind': 'chart', 'references': [{'dimension': 'custom_a'}], 'title': '引用归属图'},
        {'key': 'chart:unknown', 'kind': 'chart', 'title': '无归属图'},
    ]
    payload = {'dimension_data': {'dimensions': dims}, 'as_of': '2026-11-13'}
    note(payload, {}, {'chart:tagged': '研究员的图表结论'})
    groups = evidence_groups(payload, figures, dims)
    assert [group['label'] for group in groups] == ['自定custom_b', '自定custom_a', '其他']
    assert [len(group['figures']) for group in groups] == [2, 2, 1]
    assert groups[1]['figures'][1]['conclusion'] == '研究员的图表结论'
    assert len(figures) == sum(len(group['figures']) for group in groups)
    # A valid researcher ID called "other" must not absorb unclassified charts.
    additional = evidence_groups(payload, figures, [*dims, dimension('other')])
    assert [group['label'] for group in additional] == ['自定custom_b', '自定custom_a', '其他']


def test_same_projected_numbers_markdown_structure_and_legacy_draft():
    payload = source()
    note(payload, {'custom_b': {'text': '研究员修改 | 保留', 'impact': 'bearish'}})
    view = present_snapshot(payload)
    md = readable_markdown(payload)
    assert view['dimension_overview'][0]['facts'][0]['text'] in md
    assert '| 自定custom_b |' in md and r'研究员修改 \| 保留' in md
    headings = ['## 本期综述', '## 逻辑与策略', '## 证据图', '<summary>完整记录</summary>']
    assert [md.index(value) for value in headings] == sorted(md.index(value) for value in headings)
    assert '保存的主要矛盾' in md and '保存的基调' in md
    payload['manual_report']['draft'].pop('dimension_notes')
    payload['manual_report']['draft'].pop('figure_conclusions')
    assert present_snapshot(payload)['editable_draft']['dimension_notes'] == {}


def test_hidden_research_does_not_reopen_in_concise_view():
    payload = source()
    note(payload, {})
    payload['manual_report']['draft']['sections']['research']['visible'] = False
    view = present_snapshot(payload)
    assert not view['logic_strategy']['contradiction'] and not view['logic_strategy']['conditions']
    assert not view['logic_strategy']['present'] and not view['logic_strategy']['active']
    assert not view['logic_strategy']['status'] and not view['logic_strategy']['written_at']
    assert all(not row['evidence'] for row in view['dimension_overview'])


def test_draft_judgment_uses_same_record_as_readable_report():
    payload = source()
    payload['judgment']['status'] = '草稿'
    payload['judgment']['written_at'] = '2026-11-09'
    view = present_snapshot(payload)
    logic = view['logic_strategy']
    assert logic['present'] and not logic['active']
    assert logic['status'] == view['judgment']['status'] == '草稿'
    assert logic['written_at'] == view['judgment']['written_at'] == '2026-11-09'
    assert logic['tone'] == '中性 · 保存的基调'
    assert logic['contradiction'] == view['judgment']['contradiction']
    assert view['judgment']['contradiction'] == '保存的主要矛盾'
    concise_markdown = readable_markdown(payload).split('## 逻辑与策略', 1)[1].split('## 证据图', 1)[0]
    assert '- 研判：草稿 · 2026-11-09' in concise_markdown
    html = _logic_html(view)
    assert '草稿 · 2026-11-09' in html and '保存的主要矛盾' in html
    assert '暂无研判' not in html and '来自生效研判' not in html


def _logic_html(view):
    from pathlib import Path
    from jinja2 import Environment, FileSystemLoader
    import tin.web.report_view

    environment = Environment(loader=FileSystemLoader(
        Path(tin.web.report_view.__file__).with_name('templates')), autoescape=True)
    return environment.get_template('_report_logic_strategy.html').render(
        report=view, panels=view['panels'])


def test_missing_judgment_does_not_invent_concise_record():
    payload = source()
    payload.pop('judgment')
    logic = present_snapshot(payload)['logic_strategy']
    assert not logic['present'] and not logic['active']
    assert not any(logic[key] for key in ('tone', 'contradiction', 'status', 'written_at', 'conditions'))
    assert '暂无研判。' in _logic_html(present_snapshot(payload))


def test_hidden_judgment_status_does_not_leak_through_concise_outputs():
    payload = source()
    payload['judgment'].update(status='草稿', written_at='2026-11-09')
    note(payload, {})
    payload['manual_report']['draft']['sections']['research']['visible'] = False
    view = present_snapshot(payload)
    html = _logic_html(view)
    assert '草稿' not in html and '2026-11-09' not in html
    concise_markdown = readable_markdown(payload).split('## 逻辑与策略', 1)[1].split('## 证据图', 1)[0]
    assert '草稿' not in concise_markdown and '2026-11-09' not in concise_markdown


def test_explicit_citation_dimension_survives_dedup_manifest():
    from tin.web.report_charts import collect_figures
    payload = source()
    payload['judgment']['chart_references'] = [{'id': 'chart-one', 'revision': 1, 'title': '原图',
        'target': {'kind': 'evidence', 'side': 'bull', 'index': 0}}]
    manifest = collect_figures(payload)
    assert manifest[0]['references'][0]['dimension'] == 'custom_a'
