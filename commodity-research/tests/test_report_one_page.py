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
    assert all(not row['evidence'] for row in view['dimension_overview'])


def test_draft_judgment_stays_in_full_records_only():
    payload = source()
    payload['judgment']['status'] = '草稿'
    view = present_snapshot(payload)
    assert view['logic_strategy']['contradiction'] == ''
    assert view['judgment']['contradiction'] == '保存的主要矛盾'


def test_explicit_citation_dimension_survives_dedup_manifest():
    from tin.web.report_charts import collect_figures
    payload = source()
    payload['judgment']['chart_references'] = [{'id': 'chart-one', 'revision': 1, 'title': '原图',
        'target': {'kind': 'evidence', 'side': 'bull', 'index': 0}}]
    manifest = collect_figures(payload)
    assert manifest[0]['references'][0]['dimension'] == 'custom_a'
