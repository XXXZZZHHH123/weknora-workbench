"""Reopen actual OOXML decks and verify content, pagination and visual contracts."""
from copy import deepcopy
from io import BytesIO

from PIL import Image
from pptx import Presentation
from pptx.enum.shapes import MSO_SHAPE_TYPE
from pptx.util import Inches
import pytest

from tin.ppt.report_deck import (
    THEME_RED, WIDTH, HEIGHT, build_report_deck, chart_slots, filename,
)


def png():
    output = BytesIO()
    Image.new('RGB', (800, 500), 'white').save(output, format='PNG')
    return output.getvalue()


@pytest.fixture
def view():
    return {
        'period': {'kind': 'weekly', 'start': '2030-04-01', 'end': '2030-04-05'},
        'as_of': '2030-04-05', 'workbench_name': '合成工作台', 'report_title': '春季研究周报',
        'coverage': {'latest_data_date': '2030-04-04'}, 'generated_at': '2030-04-05 18:20:00 +08:00',
        'panels': {key: {'visible': True, 'text': '', 'references': []}
                   for key in ('summary', 'research', 'dashboard', 'watch')},
        'conclusion': {'tone': '中性', 'text': '仅回放已保存结论'},
        'strategy_overview': {'text': '1 个策略方案', 'triggered': [f'保存条件{index}' for index in range(6)]},
        'judgment': {'tone': '中性', 'status': '生效', 'contradiction': '供需主要矛盾',
                     'cases': [
                         {'label': '支持依据', 'claim': '已保存的支持逻辑',
                          'evidence': [{'text': '成本依据', 'name': '样本成本', 'series_id': 'test-a'}]},
                         {'label': '反对依据', 'claim': '已保存的反对逻辑', 'evidence': []}],
                     'thresholds': [{'name': '观察区间', 'series_name': '样本成本', 'condition': '>= 1.2万',
                                     'state': '未触发', 'current_value': 12345, 'current_value_text': '1.23万',
                                     'evidence_dates': ['2030-04-04'], 'stale_note': '最新数据距今 40 天，可能已停更'}],
                     'falsifiers': []},
        'latest_values': {'test-a': {'value_text': '1.23万 元/吨', 'as_of': '2030-04-04',
                                    'stale_note': '最新数据距今 40 天，可能已停更'}},
        'report_figures': [{'key': f'indicator:synthetic-{index}', 'title': f'合成图表{index}',
                            'caption': '引用于：研判与策略', 'unit': '元/吨', 'as_of': '2030-04-04',
                            'source': '合成资料源'} for index in range(3)],
        'research_cards': [{'title': '保存方案', 'direction_label': '观察', 'status_label': '草稿',
                            'thesis': '已保存核心逻辑', 'trigger': '适用条件', 'response_action': '应对措施',
                            'monitor_view': {'conditions': [{'title': '所引观察区间', 'state': '未触发',
                                'value_text': '1.23万', 'as_of': '2030-04-04'}],
                                'indicators': [{'title': '所引成本', 'value_text': '1.23万', 'as_of': '2030-04-04'}]}}],
        'strategies': [{'title': '保存交易计划', 'status_label': '草稿', 'mode_label': '单腿',
                        'thesis': '人工计划', 'legs': [{'contract': 'TEST01', 'side': 'buy'}],
                        'entry': {'low_text': '1.2万', 'high_text': '1.3万'}, 'invalidation': '人工复核'}],
    }


def load(view, images=None, options=None):
    # Preserve coverage of the legacy section-only export contract.
    options = {'sections': dict.fromkeys(('summary', 'research', 'dashboard', 'watch'), True), **(options or {})}
    return Presentation(BytesIO(build_report_deck(view, images or {}, options)))


def text(slide):
    rows = []
    for shape in slide.shapes:
        if shape.has_text_frame:
            rows.append(shape.text)
        if shape.has_table:
            rows.extend(cell.text for row in shape.table.rows for cell in row.cells)
    return '\n'.join(rows)


def title(slide):
    return next((shape.text for shape in slide.shapes if shape.name in {'Section title', 'Cover title'}), '')


def chart_pictures(deck):
    return [shape for slide in deck.slides for shape in slide.shapes if shape.name.startswith('Report chart ')]


def test_full_deck_order_counts_saved_values_and_notes(view):
    deck = load(view, {row['key']: png() for row in view['report_figures']})
    assert [title(slide) for slide in deck.slides] == [
        '春季研究周报', '导语', '研判', '研判 · 支持依据 / 反对依据', '研判 · 区间与阈值',
        '图表（1/2）', '图表（2/2）', '策略 · 保存方案', '交易计划 · 保存交易计划', '说明']
    lead = text(deck.slides[1])
    assert '沿用已保存研判' in lead and '另有 2 项' in lead
    assert '保存条件3' in lead and '保存条件4' not in lead
    body = '\n'.join(text(slide) for slide in deck.slides)
    for expected in ['1.23万', '2030-04-04', '可能已停更', '已保存核心逻辑', '所引观察区间',
                     '所引成本', '条件触发不等于成交', '2030-04-05 18:20:00 +08:00', '合成资料源']:
        assert expected in body
    assert len(chart_pictures(deck)) == 3


def test_page_geometry_logo_theme_and_explicit_fonts(view):
    deck = load(view, {view['report_figures'][0]['key']: png()})
    assert (deck.slide_width, deck.slide_height) == (Inches(WIDTH), Inches(HEIGHT))
    for index, slide in enumerate(deck.slides):
        logos = [shape for shape in slide.shapes if shape.name == 'Chisage logo']
        assert len(logos) == 1 and logos[0].shape_type == MSO_SHAPE_TYPE.PICTURE
        if index:
            assert logos[0].left == Inches(.18)
            assert logos[0].top == Inches(.12)
            assert logos[0].height == Inches(.5)
        assert THEME_RED in slide._element.xml
        for shape in slide.shapes:
            assert shape.left >= 0 and shape.top >= 0
            assert shape.left + shape.width <= deck.slide_width + 1
            assert shape.top + shape.height <= deck.slide_height + 1
        for run in slide._element.xpath('.//a:r'):
            props = run.find('{http://schemas.openxmlformats.org/drawingml/2006/main}rPr')
            assert props is not None
            assert int(props.get('sz')) >= 1100
            assert props.find('{http://schemas.openxmlformats.org/drawingml/2006/main}ea').get('typeface') == 'Microsoft YaHei'
            assert props.find('{http://schemas.openxmlformats.org/drawingml/2006/main}latin').get('typeface') == 'Arial'


@pytest.mark.parametrize('per_page,expected_pages', [(1, 3), (2, 2), (4, 1)])
def test_chart_density_uses_the_browser_slot_contract(view, per_page, expected_pages):
    deck = load(view, {row['key']: png() for row in view['report_figures']}, {'figures_per_slide': per_page})
    pages = [slide for slide in deck.slides if title(slide).startswith('图表（')]
    assert len(pages) == expected_pages
    assert len(chart_pictures(deck)) == len(view['report_figures'])
    for slide in pages:
        pictures = [shape for shape in slide.shapes if shape.name.startswith('Report chart ')]
        for picture, slot in zip(pictures, chart_slots(per_page)):
            assert (picture.left, picture.top, picture.width, picture.height) == tuple(Inches(value) for value in slot)
        assert '引用于：研判与策略' in text(slide)
        assert '单位：元/吨' in text(slide)


def test_unavailable_chart_keeps_its_slot_title_and_reason(view):
    view['report_figures'][1]['notice'] = '来源权限已变化，请核对引用'
    deck = load(view, {view['report_figures'][0]['key']: png()})
    charts = [slide for slide in deck.slides if title(slide).startswith('图表（')]
    assert '合成图表1' in text(charts[0])
    assert '来源权限已变化，请核对引用' in text(charts[0])
    assert len([shape for slide in charts for shape in slide.shapes if shape.name == 'Unavailable chart']) == 2


def test_long_text_two_columns_long_table_and_caption_continue_without_font_shrink(view):
    view['panels']['summary']['text'] = '导语长文字' * 600 + '导语末尾'
    view['judgment']['cases'][0]['evidence'][0]['text'] = '依据长文字' * 200 + '依据末尾'
    rule = view['judgment']['thresholds'][0]
    view['judgment']['thresholds'] = [deepcopy(rule) for _ in range(35)]
    view['judgment']['thresholds'][0]['name'] = '表格长文字' * 250 + '表格末尾'
    view['research_cards'][0]['thesis'] = '方案长文字' * 500 + '方案末尾'
    view['report_figures'][0]['caption'] = '引用详情' * 300 + '图说末尾'
    deck = load(view)
    titles = [title(slide) for slide in deck.slides]
    for prefix in ['导语', '研判 · 支持依据 / 反对依据', '研判 · 区间与阈值', '策略 · 保存方案']:
        assert prefix + '（续）' in titles
    assert '图表 · 图说（续）' in titles
    compact = ''.join(text(slide).replace('\n', '') for slide in deck.slides)
    for token in ['导语末尾', '依据末尾', '表格末尾', '方案末尾', '图说末尾']:
        assert token in compact
    for slide in deck.slides:
        for shape in slide.shapes:
            assert shape.top + shape.height <= Inches(6.95) or shape.top >= Inches(7)


def test_section_options_and_hidden_report_panels_cannot_be_enabled(view):
    hidden = {'sections': {key: False for key in ('summary', 'research', 'dashboard', 'watch')}}
    deck = load(view, options=hidden)
    assert [title(slide) for slide in deck.slides] == ['春季研究周报', '说明']
    assert not chart_pictures(deck)
    view['panels']['research']['visible'] = False
    deck = load(view, options={'sections': {'research': True}})
    assert not any(title(slide).startswith('研判') for slide in deck.slides)
    assert any(title(slide).startswith('策略') for slide in deck.slides)


def test_frozen_archive_never_embeds_chart_images(view):
    view['frozen'] = True
    deck = load(view, {row['key']: png() for row in view['report_figures']})
    assert not chart_pictures(deck)
    chart_page = next(slide for slide in deck.slides if title(slide) == '图表')
    assert '历史归档不重新绘图' in text(chart_page)
    for row in view['report_figures']:
        assert row['title'] in text(chart_page)
    assert '引用于：研判与策略' in text(chart_page)


def test_latest_data_falls_back_to_saved_observations_without_inventing_report_date(view):
    view['frozen'] = True
    view['coverage'] = {}
    view['observations'] = [{'as_of': '2030-03-28'}, {'as_of': '2030-03-29T00:00:00+08:00'}]
    deck = load(view)
    assert '最近数据日：2030-03-29' in text(deck.slides[0])
    assert all('数据截至 2030-03-29 · 合成工作台' in text(slide) for slide in deck.slides)
    view['observations'] = []
    assert '最近数据日：未注明' in text(load(view).slides[0])


def test_researcher_notes_and_explicit_reference_summary_are_preserved(view):
    view['panels']['research']['text'] = '研究员明确按语'
    view['panels']['research']['references'] = [{'title': '已保存研究记录', 'revision': 7, 'summary': '明确保存的摘要'}]
    view['panels']['watch']['text'] = '策略按语'
    deck = load(view)
    combined = '\n'.join(text(slide) for slide in deck.slides)
    for token in ['研究员明确按语', '引用记录摘要', '已保存研究记录', 'v7', '明确保存的摘要', '策略按语']:
        assert token in combined


def test_filename_daily_weekly_and_header_control_characters(view):
    assert filename(view) == '合成工作台 周报 2030-04-01—2030-04-05.pptx'
    view['period']['kind'] = 'daily'
    assert filename(view) == '合成工作台 日报 2030-04-05.pptx'
    view['workbench_name'] = '合成/台\r\n非法:*'
    result = filename(view)
    assert not any(char in result for char in '/\r\n:*')


def test_generator_does_not_mutate_server_view(view):
    before = deepcopy(view)
    load(view)
    assert view == before
    with pytest.raises(ValueError, match='1、2 或 4'):
        load(view, options={'figures_per_slide': 3})


def test_iso_timestamps_display_as_dates_without_changing_other_text():
    from tin.ppt.report_deck import _display
    assert _display('2023-08-31T00:00:00+08:00 → 2023-09-21T00:00:00+08:00') == '2023-08-31 → 2023-09-21'
    assert _display('生成时间：2026-11-17T11:39:46+08:00') == '生成时间：2026-11-17 11:39'
    assert _display('2024-03-29 · 116.6 万吨') == '2024-03-29 · 116.6 万吨'


def test_wrapped_table_cells_never_split_a_raw_timestamp():
    from tin.ppt.report_deck import _s
    assert _s('2023-08-31T00:00:00+08:00') == '2023-08-31'


def one_page_view(view):
    view['dimension_overview'] = [
        {'id': 'custom-' + str(index), 'label': '自定义维度' + str(index),
         'facts': [{'text': '样本指标 12.34 吨（较 04-01 +1.23，近 5 年同期第 2 高）'}],
         'sentence': '研究员确认的本期含义', 'impact': 'neutral', 'impact_label': '中性',
         'support_count': 2, 'oppose_count': 1}
        for index in range(6)]
    view['logic_strategy'] = {'contradiction': '本期主要矛盾', 'tone': '中性',
        'conditions': [{'dimension_label': '自定义维度0', 'name': '关注阈值', 'condition': '>=12.34', 'state': '未触发'}],
        'strategies': [{'text': '观察：研究员核心逻辑；条件出现时复核'}]}
    for row in view['report_figures']:
        row['conclusion'] = '该图研究员确认的一句结论'
    view['evidence_groups'] = [
        {'label': '自定义维度0', 'figures': view['report_figures'][:2]},
        {'label': '其他', 'figures': view['report_figures'][2:]}]
    return view


def new_deck(view, mode='one_page', images=None, **options):
    return Presentation(BytesIO(build_report_deck(view, images, {'mode': mode, **options})))


def test_default_one_page_has_table_logo_and_exact_report_numbers(view):
    one_page_view(view)
    deck = Presentation(BytesIO(build_report_deck(view)))
    assert len(deck.slides) == 1
    body = text(deck.slides[0])
    for value in ('本期综述', '逻辑与策略', '12.34', '04-01', '近 5 年', '研究员确认', '本期主要矛盾', '未触发'):
        assert value in body
    assert sum(shape.has_table for shape in deck.slides[0].shapes) == 1
    assert len([shape for shape in deck.slides[0].shapes if shape.name == 'Chisage logo']) == 1
    assert THEME_RED in deck.slides[0]._element.xml and 'F2F2F2' in deck.slides[0]._element.xml
    assert not chart_pictures(deck)


def test_one_page_long_content_is_two_pages_at_most_and_notes_keep_full_text(view):
    one_page_view(view)
    view['dimension_overview'] *= 2
    for row in view['dimension_overview']:
        row['sentence'] = '研究员长文字' * 60 + '保留全文末尾'
        row['facts'] *= 4
    deck = new_deck(view)
    assert len(deck.slides) == 2
    assert '…' in ''.join(text(slide) for slide in deck.slides)
    assert all('保留全文末尾' in slide.notes_slide.notes_text_frame.text for slide in deck.slides)
    for slide in deck.slides:
        for shape in slide.shapes:
            assert shape.top + shape.height <= Inches(6.96) or shape.top >= Inches(7)


def test_evidence_mode_groups_one_or_two_charts_per_slide(view):
    one_page_view(view)
    images = {row['key']: png() for row in view['report_figures']}
    for per_page, expected in [(1, 3), (2, 2)]:
        deck = new_deck(view, 'evidence', images, figures_per_slide=per_page)
        pages = [slide for slide in deck.slides if title(slide).startswith('证据 · ')]
        assert len(pages) == expected
        assert len(chart_pictures(deck)) == 3
        assert '自定义维度0' in title(pages[0]) and '其他' in title(pages[-1])
        assert all('该图研究员确认的一句结论' in text(slide) for slide in pages)
    full = new_deck(view, 'full', images)
    assert len(full.slides) > len(new_deck(view, 'evidence', images).slides)
    assert '已保存核心逻辑' in ''.join(text(slide) for slide in full.slides)


def test_frozen_new_modes_never_embed_images_or_mutate_view(view):
    one_page_view(view)
    view['frozen'] = True
    before = deepcopy(view)
    deck = new_deck(view, 'evidence', {row['key']: png() for row in view['report_figures']})
    assert not chart_pictures(deck)
    assert '历史归档不重新绘图' in '\n'.join(text(slide) for slide in deck.slides)
    assert view == before


def test_new_modes_respect_hidden_panels_and_never_reopen_empty_logic(view):
    one_page_view(view)
    view['logic_strategy'] = {'contradiction': '', 'tone': '', 'conditions': [], 'strategies': []}
    view['judgment']['contradiction'] = '草稿研判绝不能回退'
    view['judgment']['status'] = '草稿'
    for mode in ('one_page', 'evidence', 'full'):
        copy = deepcopy(view)
        copy['panels']['summary']['visible'] = False
        copy['panels']['research']['visible'] = False
        copy['panels']['watch']['visible'] = False
        deck = new_deck(copy, mode)
        combined = '\n'.join(text(slide) + slide.notes_slide.notes_text_frame.text for slide in deck.slides)
        assert '草稿研判绝不能回退' not in combined
        assert '研究员确认的本期含义' not in combined
        assert '已保存核心逻辑' not in combined
        assert not any(shape.has_table for slide in deck.slides for shape in slide.shapes)
    projected = text(new_deck(view).slides[0])
    assert '草稿研判绝不能回退' not in projected
    with pytest.raises(ValueError, match='不能同时'):
        new_deck(view, sections={'summary': False})


def test_short_overview_keeps_all_facts_when_they_fit(view):
    one_page_view(view)
    view['dimension_overview'] = view['dimension_overview'][:1]
    view['dimension_overview'][0]['facts'] = [{'text': f'指标{index} 值 {index + 10} 吨'} for index in range(4)]
    deck = new_deck(view)
    assert len(deck.slides) == 1
    for index in range(4):
        assert f'指标{index} 值 {index + 10} 吨' in text(deck.slides[0])
