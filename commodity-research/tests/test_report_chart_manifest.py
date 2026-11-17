"""Saved references drive report graphs; definitions and grants stay pinned."""
from copy import deepcopy
from datetime import date

import pytest

from tin.web.report_charts import collect_figures, attach_figures
from tin.web.report_view import present_snapshot, readable_markdown
from tests.test_chart_references import reference_client, create, frozen
from tests.chart_fixtures import SID, definition
from tests.test_chart_api import chart_client


def pin(identifier='one', revision=1, sid='a', **extra):
    return {'kind': 'chart', 'id': identifier, 'revision': revision, 'as_of': '2023-01-02',
            'title': identifier + str(revision), 'definition': {'curves': [{'series_id': sid}]}, **extra}


def payload():
    return {'as_of': '2024-03-29', 'observations': [
        {'series_id': 'a', 'name': '指标甲', 'value': 2, 'unit': '吨'},
        {'series_id': 'b', 'name': '指标乙', 'value': 3, 'unit': '元'}],
        'judgment': {'contradiction': {'bull': {'evidence': [{'desc': '保存依据'}]},
            'chart_references': [pin(target={'kind': 'evidence', 'side': 'bull', 'index': 0}), pin('two')]},
            'thresholds': [{'id': 't', 'name': '保存条件'}]},
        'research_cards': [{'title': '方案甲', 'direction_label': '保存方向', 'trigger': '保存触发条件',
            'invalidation': '保存撤回条件', 'review_date': '2024-04-01', 'citations': [pin(revision=3, field='trigger'), pin('three', sid='c')]}],
        'strategies': [{'title': '交易方案', 'status_label': '草稿', 'mode_label': '模拟跟踪',
            'chart_references': [pin('four', sid='d'), pin(revision=2)]}],
        'report_figure_selection': ['a', 'b']}


def test_order_max_revision_all_locations_and_selected_single_curve_dedup():
    source = payload()
    before = deepcopy(source)
    rows = collect_figures(source)
    assert [row['key'] for row in rows] == ['chart:one', 'chart:two', 'chart:three', 'chart:four', 'indicator:b']
    assert rows[0]['reference']['revision'] == 3 and rows[0]['title'] == 'one3'
    assert rows[0]['as_of'] == '2024-03-29'
    assert rows[0]['caption'] == '引用于：研判（支持依据「保存依据」）、策略「方案甲」（适用 / 触发）、策略「交易方案」'
    assert all(row['single_series_id'] == 'a' for row in rows[:2])
    assert source == before


def test_no_reference_count_cap_and_multicurve_does_not_hide_selected():
    source = payload()
    source['judgment']['contradiction']['chart_references'] = [pin(str(i), sid='c') for i in range(32)]
    source['research_cards'] = []
    source['strategies'] = []
    rows = collect_figures(source)
    assert len(rows) == 34
    source['judgment']['contradiction']['chart_references'] = [pin(definition={'curves': [{'series_id': 'a'}, {'series_id': 'b'}]})]
    assert len(collect_figures(source)) == 3


def test_saved_draft_keeps_caption_and_hidden_records_do_not_supply_graphs():
    source = payload()
    from tin.web.report_drafts import FORMAT
    source['manual_report'] = {'id': 'draft', 'revision': 1, 'draft': {'format': FORMAT,
        'title': '已存报告', 'period': 'daily', 'date': source['as_of'], 'references': [],
        'sections': {'research': {'visible': False, 'text': ''}, 'watch': {'visible': False, 'text': ''}},
        'figures': [{'series_id': 'a', 'caption': '人工图说'}]}}
    rows = collect_figures(source)
    assert len(rows) == 1 and rows[0]['caption'] == '人工图说'
    assert rows[0]['unit'] == '吨'
    source['manual_report']['draft']['sections']['research']['visible'] = True
    assert all(row['kind'] == 'chart' for row in collect_figures(source))
    assert source['manual_report']['draft']['figures'][0]['caption'] == '人工图说'


def test_restricted_figures_do_not_include_references_or_read_graph_metadata(monkeypatch):
    from types import SimpleNamespace
    from tin.web import chart_references
    monkeypatch.setattr(chart_references, 'reference_definition', lambda *a: pytest.fail('restricted graph read'))
    source = payload()
    attach_figures(source, SimpleNamespace(restricted=True))
    assert [row['key'] for row in source['report_figures']] == ['indicator:a', 'indicator:b']


def test_frozen_manifest_and_markdown_never_read_chart_data(monkeypatch):
    from tin.web import chart_references
    monkeypatch.setattr(chart_references, 'reference_definition', lambda *a: pytest.fail('frozen graph read'))
    monkeypatch.setattr(chart_references, 'render_chart_reference', lambda *a, **kw: pytest.fail('frozen observations'))
    source = payload()
    attach_figures(source, None, metadata=False)
    source['report_figures'][0]['unit'] = '吨'
    view = present_snapshot(source)
    assert len(view['automatic_figures']) == 4
    assert [row['series_id'] for row in view['figure_options']] == ['b']
    text = readable_markdown(source)
    assert text.index('## 研判') < text.index('## 图表') < text.index('## 策略')
    assert '### one3' in text and '引用于：研判' in text and '单位：吨' in text and '数据截至日：2024-03-29' in text
    assert '![' not in text


def test_reference_quotations_are_also_included_with_original_target_labels():
    source = {'as_of': '2024-01-01', 'manual_report': {'draft': {'figures': [], 'reference_snapshots': [
        {'kind': 'judgment', 'chart_references': [pin(report_target_text='区间与阈值「保存区间」')]},
        {'kind': 'strategy', 'title': '引用方案', 'chart_references': [pin('other')]}]}}}
    rows = collect_figures(source)
    assert rows[0]['caption'] == '引用于：研判（区间与阈值「保存区间」）'
    assert rows[1]['caption'] == '引用于：策略「引用方案」'


@pytest.fixture
def report_chart_client(reference_client, monkeypatch):
    from tin.web import report_figures
    client, state = reference_client
    state.app.include_router(report_figures.router)
    state.manifest = []
    monkeypatch.setattr(report_figures, 'report_manifest', lambda *a, **kw: state.manifest)
    return client, state


def test_report_reads_saved_revision_at_report_cutoff_and_rejects_nonmember(report_chart_client):
    client, state = report_chart_client
    chart = create(client)
    reference = frozen(client, chart)
    assert client.patch('/api/sn/charts/' + chart['id'], json={'revision': 1, 'title': '已改标题'}).status_code == 200
    source = {'as_of': '2023-01-03', 'judgment': {'contradiction': {'chart_references': [reference]}}}
    state.manifest = collect_figures(source)
    url = '/api/sn/reports/figures/chart:' + chart['id'] + '?date=2023-01-03'
    result = client.get(url)
    assert result.status_code == 200, result.text
    value = result.json()
    assert value['rendered']['definition']['title'] == reference['title']
    assert value['rendered']['payload']['revision'] == 1
    assert [point['date'] for point in value['rendered']['payload']['curves'][0]['points']] == ['2023-01-01', '2023-01-02', '2023-01-03']
    assert value['figure']['unit'] == state.fields[SID]['unit'] and value['figure']['as_of'] == '2023-01-03'
    assert client.get('/api/sn/reports/figures/chart:other?date=2023-01-03').status_code == 404
    assert client.get(url, headers={'x-restricted': 'true'}).status_code == 403


def test_unreadable_reference_keeps_card_and_reason(report_chart_client):
    client, state = report_chart_client
    chart = create(client)
    reference = frozen(client, chart)
    state.manifest = collect_figures({'as_of': '2023-01-03', 'judgment': {'contradiction': {'chart_references': [reference]}}})
    response = client.get('/api/sn/reports/figures/chart:' + chart['id'] + '?date=2023-01-03', headers={'x-workbench': '10000000-0000-4000-8000-000000000092'})
    assert response.status_code == 200
    assert response.json()['status'] == 'unavailable'
    assert response.json()['figure']['title'] == reference['title']
    assert '不存在' in response.json()['notice']


def test_reference_metadata_uses_graph_acl_and_no_observation_reads(reference_client):
    from fastapi import Body
    from tin.integration.workbench_scope import current_workbench
    client, state = reference_client
    chart = create(client)
    reference = frozen(client, chart)
    @state.app.post('/manifest')
    def manifest(body: dict = Body()):
        return attach_figures(body, current_workbench.get())
    source = {'as_of': '2023-01-03', 'judgment': {'contradiction': {'chart_references': [reference]}}}
    state.requests.clear()
    result = client.post('/manifest', json=source)
    assert result.status_code == 200, result.text
    assert result.json()['report_figures'][0]['unit'] == state.fields[SID]['unit']
    assert all(request.get('catalog_only') for _, request in state.requests)
    foreign = client.post('/manifest', json=source, headers={'x-workbench': '10000000-0000-4000-8000-000000000092'})
    assert foreign.status_code == 200
    assert '不存在' in foreign.json()['report_figures'][0]['notice']
