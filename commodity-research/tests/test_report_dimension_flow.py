"""Real dimension bindings, observation calculations and report projections agree."""
from contextlib import contextmanager
from copy import deepcopy
from io import BytesIO
import json
from types import SimpleNamespace

from fastapi.testclient import TestClient
from lxml.html import fromstring
from pptx import Presentation

from tin.config import settings
from tin.web import app as web, dimension_board, dimension_data
from tin.web.report_view import present_snapshot
from tests.chart_fixtures import SID, OTHER, THIRD
from tests.test_chart_api import chart_client  # noqa: F401
from tests.test_chart_references import reference_client, create  # noqa: F401
from tests.test_report_chart_flow import report_flow  # noqa: F401
from tests.test_research_strategy_cards import entry

DAY = '2023-01-09'


def configured_flow(report_flow):
    client, state = report_flow
    state.app.include_router(dimension_board.router)
    for field in state.fields.values():
        field['frequency'] = '日'
    state.observations[SID] = [('2022-12-26', 8), ('2023-01-02', 11), (DAY, 14)]
    state.observations[OTHER] = [('2022-12-26', 15), ('2023-01-02', 20), (DAY, 40)]
    state.observations[THIRD] = [('2023-01-02', 90), (DAY, 100)]
    bound_chart = create(client, title='绑定的差值证据图', curves=[
        {'derived': {'op': 'sub', 'a': SID, 'b': OTHER}, 'label': '显式差值'}])
    other_chart = create(client, title='未绑定维度的证据图', curves=[{'series_id': THIRD, 'label': '字段丙'}])
    response = client.put('/api/sn/dimensions', json={'revision': 0, 'dimensions': [
        {'id': 'custom-first', 'label': '先看下游', 'category': '需求', 'description': '研究员自定次序',
         'sections': [{'id': 's-first', 'label': '跟踪', 'items': [{'kind': 'indicator', 'id': OTHER}]}]},
        {'id': 'custom-second', 'label': '再看价差', 'category': '核心标的', 'description': '',
         'sections': [{'id': 's-second', 'label': '价差', 'items': [{'kind': 'chart', 'id': bound_chart['id'], 'revision': 1}]}]},
        {'id': 'custom-empty', 'label': '暂不展示', 'category': '其他', 'description': '', 'sections': []}
    ]})
    assert response.status_code == 200, response.text
    dimensions = response.json()['dimensions']
    first, second, empty = [dimension['id'] for dimension in dimensions]
    saved = client.post('/fixture/judgment', json={
        'variety': 'SN', 'author': 'fixture', 'written_at': DAY,
        'contradiction': {'bull': {'claim': '已保存观点', 'evidence': [
            {'desc': '维度依据原文', 'series_id': SID, 'dimension': second}]},
            'bear': {'claim': '', 'evidence': []}},
        'chart_references': [{'kind': 'chart', 'id': bound_chart['id'], 'revision': 1,
                              'target': {'kind': 'evidence', 'side': 'bull', 'index': 0}}]})
    assert saved.status_code == 200, saved.text
    card = entry(source_ids=[])
    card['card'].update(title='未标维度的保存方案', citations=[
        {'kind': 'chart', 'id': other_chart['id'], 'revision': 1, 'field': 'trigger'}])
    saved_card = client.post('/api/sn/research-strategies', json=card)
    assert saved_card.status_code == 201, saved_card.text
    draft = {'format': 'research-report-draft.v1', 'period': 'weekly', 'date': DAY, 'title': '维度集成周报',
             'figures': [{'series_id': OTHER, 'caption': '人工指标图'}], 'references': [],
             'dimension_notes': {second: {'text': '研究员确认的维度句子', 'impact': 'bearish'}},
             'figure_conclusions': {'chart:' + bound_chart['id']: '研究员确认的图结论'}}
    created = client.post('/api/sn/report-drafts', json={'draft': draft})
    assert created.status_code == 201, created.text
    return client, state, dimensions, bound_chart, other_chart


def test_real_dimension_endpoint_matches_report_json_markdown_html_and_chart_operands(report_flow, monkeypatch):
    client, state, dimensions, bound_chart, other_chart = configured_flow(report_flow)
    response = client.get(f'/api/sn/dimensions/data?date={DAY}&period=weekly')
    assert response.status_code == 200, response.text
    board = response.json()
    assert board['failed'] == [] and board['as_of'] == DAY and board['revision'] == 1
    board_cards = board['dimensions'][1]['sections'][0]['items'][0]['indicators']
    assert {card['series_id'] for card in board_cards} == {SID, OTHER}, '派生图两个真实操作数均进入本期事实'
    assert {(card['series_id'], card['latest'], card['baseline']['date'], card['change']) for card in board_cards} == {
        (SID, 14, '2023-01-02', 3), (OTHER, 40, '2023-01-02', 20)}
    response = client.get(f'/sn/reports/{DAY}/weekly.json')
    assert response.status_code == 200, response.text
    payload = response.json()
    assert [dimension['id'] for dimension in payload['research_dimensions']] == [row['id'] for row in dimensions]
    rows = payload['dimension_overview']
    assert [row['label'] for row in rows] == ['先看下游', '再看价差']
    assert len(rows[1]['facts']) == 2
    for card in board_cards:
        fact = next(fact for fact in rows[1]['facts'] if fact['series_id'] == card['series_id'])
        assert (fact['latest'], fact['baseline'], fact['change'], fact['position']) == (
            card['latest'], card['baseline'], card['change'], card['position'])
        assert card['display']['latest'] in fact['text']
        assert '01-02' in fact['text'] and card['display']['change'] in fact['text']
    assert rows[1]['support_count'] == 1 and rows[1]['review_needed']
    assert rows[1]['sentence'] == '研究员确认的维度句子'
    view = present_snapshot(payload)
    assert [group['label'] for group in view['evidence_groups']] == ['先看下游', '再看价差', '其他']
    groups = {group['label']: group['figures'] for group in view['evidence_groups']}
    assert [figure['key'] for figure in groups['先看下游']] == ['indicator:' + OTHER]
    assert [figure['key'] for figure in groups['再看价差']] == ['chart:' + bound_chart['id']]
    assert [figure['key'] for figure in groups['其他']] == ['chart:' + other_chart['id']]
    assert groups['再看价差'][0]['conclusion'] == '研究员确认的图结论'
    markdown = client.get(f'/sn/reports/{DAY}/weekly.md')
    assert markdown.status_code == 200, markdown.text
    brief = markdown.text.split('## 本期综述', 1)[1].split('## 逻辑与策略', 1)[0]
    assert brief.index('先看下游') < brief.index('再看价差') and '暂不展示' not in brief
    assert all(fact['text'] in brief for row in rows for fact in row['facts'])
    assert '### 其他' in markdown.text and '研究员确认的图结论' in markdown.text
    page = client.get(f'/sn/reports?date={DAY}&period=weekly')
    assert page.status_code == 200, page.text
    html = fromstring(page.text)
    row_nodes = html.xpath('//*[@id="report-overview"]//tr[@data-report-dimension]')
    assert [node.get('data-report-dimension') for node in row_nodes] == [row['id'] for row in dimensions[:2]]
    assert all(fact['text'] in html.get_element_by_id('report-overview').text_content()
               for row in rows for fact in row['facts'])
    assert html.xpath('//*[@data-evidence-dimension]/h4/text()') == ['先看下游', '再看价差', '其他']
    assert '与当前研判在该维度的依据方向不一致' in html.get_element_by_id('report-overview').text_content()
    assert all('payload' not in card and 'points' not in card
               for dimension in payload['dimension_data']['dimensions']
               for section in dimension.get('sections', []) for card in section['items'])
    def no_dimension_reread(*args, **kwargs):
        raise AssertionError('逐图读取清单不应重新计算整台维度历史')
    monkeypatch.setattr(dimension_data, 'dimension_data', no_dimension_reread)
    figure = client.get(f'/api/sn/reports/figures/indicator%3A{OTHER}?date={DAY}&period=weekly')
    assert figure.status_code == 200, figure.text


def test_restricted_dimension_endpoint_denies_raw_reads_before_transport(report_flow):
    client, state, *_ = configured_flow(report_flow)
    state.requests.clear()
    result = client.get(f'/api/sn/dimensions/data?date={DAY}&period=weekly', headers={'x-restricted': 'true'})
    assert result.status_code == 403
    assert state.requests == [], '受限视角不能借维度数据接口读取原始观测'
    assert 'dimensions' not in result.json()


def test_frozen_archive_keeps_custom_dimensions_and_never_reads_dimension_data(loaded, tmp_path, monkeypatch):
    monkeypatch.setattr(settings, 'weknora_api_url', '')
    monkeypatch.setattr(settings, 'exports_dir', tmp_path)
    monkeypatch.setattr(web, 'SessionLocal', lambda: loaded)
    def forbidden(*args, **kwargs):
        raise AssertionError('冻结归档不得重读研究维度数据')
    monkeypatch.setattr(dimension_data, 'dimension_data', forbidden)
    dimensions = [{'id': 'd-frozen01', 'label': '归档自定义维度', 'category': '其他', 'description': ''}]
    card = {'kind': 'indicator', 'id': SID, 'series_id': SID, 'status': 'ok', 'name': '归档指标',
            'latest': 21, 'unit': '件', 'date': '2026-09-18', 'baseline': {'date': '2026-09-11', 'value': 20},
            'change': 1, 'change_pct': .05, 'display': {'latest': '21', 'change': '+1'}, 'position': {'n': 0}}
    payload = {'variety': web.V, 'as_of': '2026-09-18', 'generated_at': '2026-09-18T16:00:00+08:00',
               'observations': [], 'research_dimensions': dimensions,
               'dimension_data': {'dimensions': [{**deepcopy(dimensions[0]), 'sections': [
                   {'id': 's-frozen', 'label': '归档环节', 'items': [card]}]}]}}
    folder = tmp_path / web.V
    folder.mkdir()
    (folder / '2026-09-18.json').write_text(json.dumps(payload, ensure_ascii=False), encoding='utf-8')
    # The PPT archive loader already preserves frozen configuration. Keep HTML
    # and Markdown on that same projection without touching live dimensions.
    @contextmanager
    def archive_session(scope):
        yield loaded
    monkeypatch.setattr('tin.integration.workbench_scope.artifact_session', archive_session)
    from tin.web.report_ppt import read_report_view
    from tin.ppt.report_deck import build_report_deck
    ppt_view = read_report_view(SimpleNamespace(metadata={'name': '归档研究台'}), '2026-09-18', 'daily')
    assert ppt_view['frozen'] is True
    assert [row['id'] for row in ppt_view['dimension_overview']] == ['d-frozen01']
    deck = Presentation(BytesIO(build_report_deck(ppt_view, options={'mode': 'one_page'})))
    assert len(deck.slides) == 1
    ppt_text = '\n'.join(cell.text for slide in deck.slides for shape in slide.shapes if shape.has_table
                         for row in shape.table.rows for cell in row.cells)
    assert '归档自定义维度' in ppt_text.replace('\n', '') and '归档指标 21 件（较 09-11 +1）' in ppt_text
    client = TestClient(web.app)
    page = client.get('/sn/reports?date=2026-09-18')
    assert page.status_code == 200, page.text
    html = fromstring(page.text)
    assert '历史冻结归档' in page.text
    assert html.xpath('//*[@id="report-overview"]//tr[@data-report-dimension]/@data-report-dimension') == ['d-frozen01']
    assert '归档指标 21 件（较 09-11 +1）' in html.get_element_by_id('report-overview').text_content()
    markdown = client.get('/sn/reports/2026-09-18/daily.md')
    assert markdown.status_code == 200, markdown.text
    assert '| 归档自定义维度 | 归档指标 21 件（较 09-11 +1）' in markdown.text
    exported = client.get('/sn/reports/2026-09-18.json')
    assert exported.status_code == 200, exported.text
    frozen_json = exported.json()
    assert [row['id'] for row in frozen_json['dimension_overview']] == ['d-frozen01']
    assert frozen_json['dimension_overview'][0]['facts'][0]['text'] == '归档指标 21 件（较 09-11 +1）'
    assert json.loads((folder / '2026-09-18.json').read_text(encoding='utf-8')) == payload, '归档文件保持冻结原文'

