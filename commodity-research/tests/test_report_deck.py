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
         'facts': [{'name': '样本指标', 'text': '样本指标 12.34 吨（较 04-01 +1.23，近 5 年同期第 2 高）'}],
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
    assert not any(shape.name == 'Period conclusion' for shape in deck.slides[0].shapes)
    assert next(shape.top for shape in deck.slides[0].shapes if shape.has_table) == Inches(1.7)
    assert len([shape for shape in deck.slides[0].shapes if shape.name == 'Chisage logo']) == 1
    assert THEME_RED in deck.slides[0]._element.xml and 'F2F2F2' in deck.slides[0]._element.xml
    assert not chart_pictures(deck)


@pytest.mark.parametrize(('period', 'label'), [('weekly', '本周结论'), ('daily', '本日结论')])
def test_period_conclusion_appears_above_the_first_table(view, period, label):
    one_page_view(view)
    view['period']['kind'] = period
    summary = '成本支撑使价格偏多。后续关注成本能否维持。'
    view['panels']['summary']['text'] = summary
    deck = new_deck(view)
    assert len(deck.slides) == 1
    slide = deck.slides[0]
    conclusion = next(shape for shape in slide.shapes if shape.name == 'Period conclusion')
    table = next(shape for shape in slide.shapes if shape.has_table)
    assert conclusion.text == f'{label} · {summary}'
    assert conclusion.top + conclusion.height < table.top
    assert table.table.cell(0, 2).text == '本期结论'
    assert_one_page_text_fits(deck)


def test_one_page_compresses_twelve_rows_and_notes_keep_full_text(view):
    one_page_view(view)
    view['dimension_overview'] *= 2
    for row in view['dimension_overview']:
        row['sentence'] = '研究员长文字' * 60 + '保留全文末尾'
        row['facts'] *= 4
    deck = new_deck(view)
    assert len(deck.slides) == 1
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


def test_short_overview_limits_facts_to_two_and_keeps_all_in_notes(view):
    one_page_view(view)
    view['dimension_overview'] = view['dimension_overview'][:1]
    view['dimension_overview'][0]['facts'] = [{'text': f'指标{index} 值 {index + 10} 吨'} for index in range(4)]
    deck = new_deck(view)
    assert len(deck.slides) == 1
    for index in range(2):
        assert f'指标{index} 值 {index + 10} 吨' in text(deck.slides[0])
    assert '指标2 值 12 吨' not in text(deck.slides[0])
    for index in range(4):
        assert f'指标{index} 值 {index + 10} 吨' in deck.slides[0].notes_slide.notes_text_frame.text



def dense_eight_dimension_view(view):
    """Isolated synthetic acceptance data, deliberately unrelated to a commodity."""
    one_page_view(view)
    impacts = [('bullish', '偏多'), ('bearish', '偏空'), ('neutral', '中性'), ('undecided', '未判断')]
    view['dimension_overview'] = []
    for index in range(8):
        impact, label = impacts[index % 4]
        facts = []
        for metric in range(4):
            name = f'合成维度{index + 1}的非常长且需精简的指标名称{metric + 1}'
            no_update = index % 2 == 0
            details = '本期无新数据（最新 2030-03-01）' if no_update else '较 04-01 +1.23'
            facts.append({'name': name, 'readable_name': name, 'latest': 1234.56 + metric,
                'latest_date': '2030-03-01' if no_update else '2030-04-04',
                'unit': '吨', 'display': {'latest': f'{1234.56 + metric:,.2f}', 'change': '+1.23'},
                'update_status': 'no_update' if no_update else 'updated',
                'baseline': {'date': '2030-04-01'}, 'change': 1.23,
                'position': {'rank': 2, 'n': 5, 'text': '近 6 年同期第 2 高'},
                'text': f'{name} {1234.56 + metric:,.2f} 吨（{details}，近 6 年同期第 2 高）'})
        view['dimension_overview'].append({'id': f'dense-{index}', 'label': f'维度{index + 1}',
            'facts': facts, 'no_update': index % 2 == 0, 'latest_date': '2030-03-01',
            'change_summary': '本期无新数据（最新 2030-03-01）' if index % 2 == 0 else '',
            'sentence': '研究员已经确认但超过六十字的完整分析，包含各环节的相互关系和下一阶段需要核验的条件。' * 4 + f'全文末尾{index}',
            'impact': impact, 'impact_label': label, 'support_count': 123, 'oppose_count': 456,
            'warning_summary': '4 项停更（最长 35 天）', 'warnings': ['合成停更明细：最新数据距今 35 天']})
    view['logic_strategy'] = {'active': True, 'tone': '中性 · 等待条件核验',
        'contradiction': '合成供应与需求之间的主要矛盾，保留已确认研究员的判断。',
        'conditions': [{'name': f'合成关注区间{index + 1}', 'condition': f'>={20 + index}',
            'state': '未触发' if index % 2 else '已触发', 'current_value_text': str(19 + index)} for index in range(4)],
        'strategies': [{'title': f'方案{index + 1}',
            'direction': '观察', 'thesis': '核心逻辑为等待合成区间有效突破并且需要研究员重新确认' * 5,
            'trigger': '已保存条件达到且复核通过' * 5, 'response_action': '重新核验并按原定措施应对' * 5,
            'text': '观察；核心逻辑为等待合成区间有效突破；触发：已保存条件；应对：重新核验。' * 5 + f'策略全文末尾{index}'} for index in range(3)]}
    return view


def assert_one_page_text_fits(deck):
    """Estimate occupied height from the actual python-pptx paragraphs and fonts."""
    from tin.ppt.report_deck import _wrap
    for slide in deck.slides:
        for shape in slide.shapes:
            assert shape.left >= 0 and shape.top >= 0
            assert shape.left + shape.width <= deck.slide_width + 1
            assert shape.top + shape.height <= deck.slide_height + 1
            frames = []
            if shape.has_text_frame and shape.text:
                frames.append((shape.text_frame, shape.width - shape.text_frame.margin_left - shape.text_frame.margin_right,
                               shape.height - shape.text_frame.margin_top - shape.text_frame.margin_bottom))
            if shape.has_table:
                for rindex, row in enumerate(shape.table.rows):
                    for cindex, cell in enumerate(row.cells):
                        if cell.text:
                            frames.append((cell.text_frame, shape.table.columns[cindex].width - cell.margin_left - cell.margin_right,
                                           row.height - cell.margin_top - cell.margin_bottom))
            for frame, width, height in frames:
                used = 0
                for paragraph in frame.paragraphs:
                    size = max((run.font.size.pt for run in paragraph.runs if run.font.size), default=10.5)
                    assert size >= 10.5
                    line_count = len(_wrap(paragraph.text,
                        width / Inches(1), size))
                    # The generated paragraphs are explicitly wrapped; no extra
                    # automatic line can silently spill into another cell.
                    assert line_count == 1
                    spacing = paragraph.line_spacing
                    spacing = spacing if isinstance(spacing, float) else spacing.pt / size if spacing is not None else 1.15
                    used += size * spacing / 72
                available = height / Inches(1)
                assert used <= available + .015, (shape.name, frame.text, used, available)


def test_eight_dimensions_long_content_fit_one_page_with_labels_and_full_notes(view):
    dense_eight_dimension_view(view)
    deck = new_deck(view)
    assert len(deck.slides) == 1
    slide = deck.slides[0]
    body = text(slide)
    notes = slide.notes_slide.notes_text_frame.text
    table = next(shape.table for shape in slide.shapes if shape.has_table)
    assert len(table.rows) == 9
    assert [cell.text for cell in table.rows[0].cells] == ['维度', '本期变化', '本期结论', '影响']
    for index in range(8):
        assert f'维度{index + 1}' in body
        assert f'全文末尾{index}' in notes and f'全文末尾{index}' not in body
        assert len(table.cell(index + 1, 2).text.replace('\n', '')) <= 60
    assert '支持 123' not in body and '反对 456' not in body
    assert '无新数据 · 最新 2030-03-01' in body and '较 04-01 +0' not in body
    assert all(value in body for value in ('基调：', '主要矛盾：', '合成关注区间1', '合成关注区间2', '合成关注区间3', '当前 19', '已触发', '未触发'))
    assert '合成关注区间4' not in body and '合成关注区间4' in notes
    assert all(f'策略全文末尾{index}' in notes for index in range(3))
    strategy_boxes = [shape for shape in slide.shapes if shape.name.startswith('Strategy summary')]
    strategy_lines = [line for shape in strategy_boxes for line in shape.text.splitlines() if line]
    assert len(strategy_lines) <= 9
    assert any('观察' in line and '方案1' in line for line in strategy_lines)
    assert any('触发' in line and '→' in line for line in strategy_lines)
    assert '合成停更明细' in notes and '合成停更明细' not in body
    assert '条件触发不等于成交' in notes
    tags = [shape for shape in slide.shapes if shape.name.startswith('Impact tag ')]
    assert len(tags) == 8 and len({str(shape.fill.fore_color.rgb) for shape in tags}) == 4
    assert_one_page_text_fits(deck)


def test_missing_projected_judgment_has_explicit_left_column_and_never_reopens_record(view):
    dense_eight_dimension_view(view)
    view['logic_strategy'].update(present=False, active=False, tone='', contradiction='', conditions=[])
    view['judgment']['contradiction'] = '旧生效记录不得回退'
    deck = new_deck(view)
    assert '暂无研判' in text(deck.slides[0])
    assert '旧生效记录不得回退' not in text(deck.slides[0]) + deck.slides[0].notes_slide.notes_text_frame.text
    assert_one_page_text_fits(deck)



def test_legacy_text_only_long_fact_never_silently_drops_numerical_tail(view):
    one_page_view(view)
    view['dimension_overview'] = view['dimension_overview'][:1]
    name = '长指标的完整名称' * 20
    original = name + ' 87.65 吨（较 04-01 -4.32）'
    view['dimension_overview'][0]['facts'] = [{'name': name, 'text': original},
        {'text': '没有结构化名称的长指标' * 20 + ' 98.76 吨（较 04-01 +5.43）'}]
    deck = new_deck(view)
    body = text(deck.slides[0])
    assert '87.65 吨（较 04-01 -4.32）' in body
    assert '完整指标事实见备注' in body
    assert '98.76 吨（较 04-01 +5.43）' in deck.slides[0].notes_slide.notes_text_frame.text
    assert_one_page_text_fits(deck)


def custom_dimension_acceptance_view(view):
    """Eight configured rows, plus out-of-band evidence and two long draft plans."""
    from tin.web.report_one_page import logic_strategy

    dense_eight_dimension_view(view)
    labels = ['样本价格价差分析', '外部供应', '境内供给与流入', '上游加工需求',
              '织布以及终端需求', '库存及凭证', '关联替代品', '资金与环境']
    view['research_dimensions'] = []
    for index, (row, label) in enumerate(zip(view['dimension_overview'], labels)):
        row['label'] = label
        view['research_dimensions'].append({'id': row['id'], 'label': label})
        for metric, fact in enumerate(row['facts']):
            original_name = fact['name']
            fact['name'] = fact['readable_name'] = f'样本指标{index + 1}-{metric + 1}'
            fact['text'] = fact['text'].replace(original_name, fact['name'])
    view['dimension_overview'].append({'id': '__unlabeled__', 'label': '未标注',
        'facts': [], 'sentence': '未标注依据保留说明', 'impact': 'undecided', 'impact_label': '未判断',
        'evidence': [{'text': '未标注依据完整内容'}]})
    view['judgment'].update(status='草稿', written_at='2030-04-03T14:30:00+08:00',
                            tone='中性', tone_note='等待核验', contradiction='供应释放与需求恢复之间的矛盾')
    view['research_cards'] = [{
        'id': f'plan-{index}', 'title': f'合成方案{index + 1}', 'direction_label': '观察',
        'thesis': '等待样本供需关系确认，综合跟踪加工需求和供给变化。' * 5 + f'逻辑末尾{index}',
        'trigger': '样本条件达到阈值并且连续观察确认后复核。' * 5 + f'触发末尾{index}',
        'response_action': '重新核验指标并按照保存的方案调整风险敞口。' * 5 + f'应对末尾{index}',
    } for index in range(2)]
    view['strategies'] = []
    view['logic_strategy'] = logic_strategy(view)
    return view


def _one_page_table(slide):
    return next(shape for shape in slide.shapes if shape.has_table)


def _table_sizes(slide):
    return [run.font.size.pt for row in _one_page_table(slide).table.rows
            for cell in row.cells for paragraph in cell.text_frame.paragraphs
            for run in paragraph.runs if run.text and run.font.size]


def test_custom_dimensions_draft_and_long_plans_have_one_page_geometry(view):
    custom_dimension_acceptance_view(view)
    before = deepcopy(view)
    deck = new_deck(view)
    assert len(deck.slides) == 1
    assert view == before
    slide = deck.slides[0]
    body, notes = text(slide), slide.notes_slide.notes_text_frame.text
    shape = _one_page_table(slide)
    table = shape.table
    assert len(table.rows) == len(view['research_dimensions']) + 1
    assert abs(shape.height - sum(row.height for row in table.rows)) <= len(table.rows)
    assert [table.cell(index + 1, 0).text.replace('\n', '') for index in range(8)] == [
        dimension['label'] for dimension in view['research_dimensions']]
    assert all('…' not in table.cell(index + 1, 0).text for index in range(8))
    assert '未标注' not in body
    assert '未标注' in notes and '未标注依据完整内容' in notes
    assert '草稿' in body and '2030-04-03' in body
    assert '供应释放与需求恢复之间的矛盾' in body.replace('\n', '')
    assert '2030-04-01—2030-04-05' in title(slide)

    row_top = shape.top + table.rows[0].height
    for index, row in enumerate(list(table.rows)[1:], 1):
        for prefix in ('Impact tag ', 'Impact label '):
            tag = next(item for item in slide.shapes if item.name == prefix + str(index))
            assert abs(tag.top + tag.height / 2 - (row_top + row.height / 2)) < row.height / 4
        row_top += row.height
    logic_title = next(item for item in slide.shapes if item.has_text_frame and item.text == '逻辑与策略')
    assert logic_title.top > shape.top + shape.height
    assert row_top == pytest.approx(shape.top + shape.height, abs=len(table.rows))

    strategy_boxes = [item for item in slide.shapes if item.name.startswith('Strategy summary')]
    assert strategy_boxes
    lines = [line for item in strategy_boxes for line in item.text.splitlines() if line]
    assert len(lines) <= 6
    for index in range(2):
        assert any(f'合成方案{index + 1}' in line and '观察' in line for line in lines)
        for suffix in ('逻辑末尾', '触发末尾', '应对末尾'):
            assert suffix + str(index) in notes
    assert sum('→' in line for line in lines) == 2
    assert all(11 <= size <= 14 for size in _table_sizes(slide))
    for row in list(table.rows)[1:]:
        # The impact text is an overlay. Its empty cell still needs an explicit
        # paragraph size, otherwise LibreOffice expands it using a 32pt default.
        assert row.cells[3].text == ''
        for paragraph in row.cells[3].text_frame.paragraphs:
            assert paragraph.font.size is not None
            assert 11 <= paragraph.font.size.pt <= 14
    for item in strategy_boxes + [item for item in slide.shapes if item.name == 'Judgment summary']:
        assert all(11 <= run.font.size.pt <= 13 for paragraph in item.text_frame.paragraphs
                   for run in paragraph.runs if run.text and run.font.size)
    assert_one_page_text_fits(deck)


def test_twenty_four_character_dimension_labels_wrap_without_truncation(view):
    custom_dimension_acceptance_view(view)
    view['dimension_overview'] = view['dimension_overview'][:8]
    view['logic_strategy']['strategies'] = []
    label = '自定义研究维度名称需要完整显示及支持列内自动换行'
    assert len(label) == 24
    for index, (row, dimension) in enumerate(zip(view['dimension_overview'], view['research_dimensions'])):
        row['label'] = dimension['label'] = label if index == 0 else '短名'
        row['sentence'] = '简短说明'
        row['facts'] = row['facts'][:1]
    deck = new_deck(view)
    assert len(deck.slides) == 1
    table = _one_page_table(deck.slides[0]).table
    assert table.columns[0].width <= Inches(1.65)
    for index, dimension in enumerate(view['research_dimensions']):
        assert table.cell(index + 1, 0).text.replace('\n', '') == dimension['label']
        assert '…' not in table.cell(index + 1, 0).text
    assert '\n' in table.cell(1, 0).text
    assert_one_page_text_fits(deck)


def test_dimension_width_and_body_font_follow_actual_content(view):
    dense = custom_dimension_acceptance_view(deepcopy(view))
    sparse = deepcopy(dense)
    sparse['dimension_overview'] = sparse['dimension_overview'][:2]
    sparse['research_dimensions'] = sparse['research_dimensions'][:2]
    for row, dimension in zip(sparse['dimension_overview'], sparse['research_dimensions']):
        row['label'] = dimension['label'] = '短名'
        row['facts'] = row['facts'][:1]
        row['sentence'] = '简短说明'
    sparse['logic_strategy']['strategies'] = []
    sparse['logic_strategy']['conditions'] = []
    dense_deck, sparse_deck = new_deck(dense), new_deck(sparse)
    dense_table, sparse_table = _one_page_table(dense_deck.slides[0]).table, _one_page_table(sparse_deck.slides[0]).table
    assert sparse_table.columns[0].width < dense_table.columns[0].width
    assert min(_table_sizes(sparse_deck.slides[0])) > min(_table_sizes(dense_deck.slides[0]))
    assert_one_page_text_fits(sparse_deck)


def test_no_update_badge_has_date_two_values_and_stays_inside_its_cell(view):
    custom_dimension_acceptance_view(view)
    deck = new_deck(view)
    slide = deck.slides[0]
    body = text(slide)
    assert '本期无新数据（最新' not in body
    assert '无新数据 · 最新 2030-03-01' in body
    shape = _one_page_table(slide)
    table = shape.table
    # Check one stale row and one updated row; numeric comparisons stay visible.
    assert '较04-01 +1.23' in table.cell(2, 1).text
    assert '样本指标1-1' in table.cell(1, 1).text
    for metric in range(2):
        if f'样本指标1-{metric + 1}' in table.cell(1, 1).text:
            assert f'{1234.56 + metric:,.2f}吨' in table.cell(1, 1).text
    assert '样本指标1-3' not in body
    assert '样本指标1-3' in slide.notes_slide.notes_text_frame.text
    paragraph = next(item for item in table.cell(1, 1).text_frame.paragraphs
                     if item.text == '无新数据 · 最新 2030-03-01')
    # A small muted tag gives the date less visual weight than the indicators.
    badge_sizes = [run.font.size.pt for run in paragraph.runs if run.text and run.font.size]
    assert badge_sizes and max(badge_sizes) <= max(_table_sizes(slide))
    colors = [color.get('val') for run in paragraph.runs
              for color in run._r.xpath('./a:rPr/a:highlight/a:srgbClr')]
    assert colors
    assert all(color[:2] == color[2:4] == color[4:6] and int(color[:2], 16) >= 230 for color in colors)
    assert_one_page_text_fits(deck)


def test_one_page_daily_title_has_single_date(view):
    one_page_view(view)
    view['period']['kind'] = 'daily'
    deck = new_deck(view)
    assert '2030-04-05' in title(deck.slides[0])
    assert '2030-04-01' not in title(deck.slides[0])
    assert '—' not in title(deck.slides[0])


def test_archive_without_one_page_projection_uses_same_draft_judgment(view):
    one_page_view(view)
    del view['logic_strategy']
    view['judgment'].update(status='草稿', written_at='2030-04-03T09:00:00+08:00')
    deck = new_deck(view)
    assert len(deck.slides) == 1
    body = text(deck.slides[0])
    for expected in ('草稿', '2030-04-03', '基调：中性', '主要矛盾：供需主要矛盾'):
        assert expected in body
    assert '暂无研判' not in body
    assert_one_page_text_fits(deck)


def test_long_tone_note_cannot_hide_judgment_status_and_date(view):
    from tin.web.report_one_page import logic_strategy

    custom_dimension_acceptance_view(view)
    note = '完整的研究基调补充说明需要保留，但状态应始终可见。' * 8
    view['judgment']['tone_note'] = note
    view['logic_strategy'] = logic_strategy(view)
    deck = new_deck(view)
    assert len(deck.slides) == 1
    summary = next(shape for shape in deck.slides[0].shapes if shape.name == 'Judgment summary')
    assert '草稿' in summary.text and '2030-04-03' in summary.text
    assert '基调：中性' in summary.text
    assert note in deck.slides[0].notes_slide.notes_text_frame.text
    assert_one_page_text_fits(deck)
