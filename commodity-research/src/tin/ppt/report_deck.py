"""Build a report deck from the server's readable report view, without inference.

All sizes are inches. Chart slots are a public contract with report-ppt.js; PNGs
are rendered at those aspect ratios before they reach this pure in-memory writer.
"""
from __future__ import annotations

from io import BytesIO
from pathlib import Path
import re
import unicodedata

from pptx import Presentation
from pptx.dml.color import RGBColor
from pptx.enum.shapes import MSO_SHAPE
from pptx.enum.text import MSO_ANCHOR, MSO_AUTO_SIZE, PP_ALIGN
from pptx.oxml.xmlchemy import OxmlElement
from pptx.util import Inches, Pt


# Modal saturated-red pixel in assets/chisage-logo.png: (190, 0, 8),
# 140173 pixels. Keep the actual corporate red instead of an approximate theme.
THEME_RED = 'BE0008'
TEXT = '262626'
MUTED = '666666'
WIDTH, HEIGHT = 13.333, 7.5
LOGO = Path(__file__).with_name('assets') / 'chisage-logo.png'
BODY_X, BODY_Y, BODY_W = .4, 1.48, 12.533
BODY_LINES = 17


def chart_slots(per_page=2):
    """Return image rectangles as ``(x, y, width, height)`` in inches."""
    if per_page == 1:
        return [(.4, 1.78, 12.533, 4.35)]
    if per_page == 2:
        return [(.4, 1.78, 6.1165, 4.35), (6.8165, 1.78, 6.1165, 4.35)]
    if per_page == 4:
        return [(.4, 1.75, 6.1165, 1.9), (6.8165, 1.75, 6.1165, 1.9),
                (.4, 4.55, 6.1165, 1.9), (6.8165, 4.55, 6.1165, 1.9)]
    raise ValueError('每页图数应为 1、2 或 4')


def _s(value):
    # Convert timestamps before any manual line wrapping splits them.
    return _display(value) if isinstance(value, str) else str(value) if isinstance(value, (int, float)) else ''


_ISO_STAMP = re.compile(r'(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2})(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?')


def _display(text):
    """Show ISO timestamps as dates (or date and minute); stored values are untouched."""
    return _ISO_STAMP.sub(lambda m: m.group(1) if m.group(2) == '00:00' else m.group(1) + ' ' + m.group(2), text)


def _join(*values):
    return ' · '.join(_s(value) for value in values if _s(value))


def _latest_data(view):
    stamp = _s((view.get('coverage') or {}).get('latest_data_date'))[:10]
    if stamp:
        return stamp
    return max((_s(row.get('as_of'))[:10] for row in view.get('observations') or []
                if row.get('as_of')), default='未注明')


def _period(view):
    period = view.get('period') or {}
    end = _s(period.get('end') or view.get('as_of'))[:10]
    start = _s(period.get('start'))[:10]
    weekly = period.get('kind') == 'weekly'
    return ('周报' if weekly else '日报', start + '—' + end if weekly and start else end)


def filename(view):
    """A filesystem/header-safe human-readable filename (RFC 5987 at the API)."""
    label, stamp = _period(view)
    name = _s(view.get('workbench_name')) or '研究工作台'
    return re.sub(r'[\x00-\x1f\x7f/\\:*?"<>|]', '_', f'{name} {label} {stamp}.pptx')


def _wrap(text, width, size=15):
    """Conservatively wrap CJK/Latin text, retaining every character."""
    budget = max(3, width * 72 / size * .88)
    result = []
    for paragraph in _s(text).replace('\r', '').split('\n'):
        line, used = '', 0
        for char in paragraph:
            cost = 1 if unicodedata.east_asian_width(char) in {'W', 'F'} else .58
            if line and used + cost > budget:
                result.append(line)
                line, used = '', 0
            line += char
            used += cost
        result.append(line)
    return result


def _font(run, size=15, *, bold=False, color=TEXT):
    run.font.name = 'Arial'
    run.font.size = Pt(size)
    run.font.bold = bold
    run.font.color.rgb = RGBColor.from_string(color)
    props = run._r.get_or_add_rPr()
    for tag, name in [('a:latin', 'Arial'), ('a:ea', 'Microsoft YaHei'), ('a:cs', 'Arial')]:
        item = props.find(tag, props.nsmap)
        if item is None:
            item = OxmlElement(tag)
            props.append(item)
        item.set('typeface', name)


def _text(slide, text, x, y, w, h, *, size=15, bold=False, color=TEXT,
          name=None, fill=None, align=PP_ALIGN.LEFT):
    shape = slide.shapes.add_textbox(Inches(x), Inches(y), Inches(w), Inches(h))
    if name:
        shape.name = name
    if fill:
        shape.fill.solid()
        shape.fill.fore_color.rgb = RGBColor.from_string(fill)
    frame = shape.text_frame
    frame.clear()
    frame.margin_left = frame.margin_right = Inches(.015)
    frame.margin_top = frame.margin_bottom = Inches(.015)
    frame.word_wrap = False
    frame.auto_size = MSO_AUTO_SIZE.NONE
    frame.vertical_anchor = MSO_ANCHOR.TOP
    for index, line in enumerate(_s(text).split('\n')):
        p = frame.paragraphs[0] if index == 0 else frame.add_paragraph()
        p.alignment = align
        p.space_before = p.space_after = Pt(0)
        p.line_spacing = 1.15
        run = p.add_run()
        run.text = _display(line)
        _font(run, size, bold=bold, color=color)
    return shape


def _rect(slide, x, y, w, h, color):
    shape = slide.shapes.add_shape(MSO_SHAPE.RECTANGLE, Inches(x), Inches(y), Inches(w), Inches(h))
    shape.fill.solid()
    shape.fill.fore_color.rgb = RGBColor.from_string(color)
    shape.line.fill.background()
    shape._element.spPr.append(OxmlElement('a:effectLst'))
    for effect in shape._element.xpath('./p:style/a:effectRef'):
        effect.set('idx', '0')
    return shape


class _Deck:
    def __init__(self, view):
        self.view = view
        self.prs = Presentation()
        self.prs.slide_width, self.prs.slide_height = Inches(WIDTH), Inches(HEIGHT)
        self.prs.core_properties.title = _s(view.get('report_title')) or filename(view)[:-5]
        self.prs.core_properties.author = _s(view.get('workbench_name')) or '研究工作台'

    def slide(self, title, *, cover=False):
        slide = self.prs.slides.add_slide(self.prs.slide_layouts[6])
        slide.background.fill.solid()
        slide.background.fill.fore_color.rgb = RGBColor(255, 255, 255)
        logo = slide.shapes.add_picture(str(LOGO), Inches(.5 if cover else .18),
                                       Inches(.4 if cover else .12), height=Inches(.83 if cover else .5))
        logo.name = 'Chisage logo'
        if not cover:
            _text(slide, title, .4, .8, 12.5, .5, size=26, bold=True, name='Section title')
            _rect(slide, .4, 1.33, 12.533, .018, THEME_RED)
        cutoff = _latest_data(self.view)
        _text(slide, f'数据截至 {cutoff} · {_s(self.view.get("workbench_name")) or "研究工作台"}',
              .4, 7.04, 11.8, .25, size=11, color=MUTED, name='Footer')
        _rect(slide, 12.55, 7.02, .38, .28, THEME_RED)
        _text(slide, str(len(self.prs.slides)), 12.59, 7.035, .3, .24, size=11, color='FFFFFF', align=PP_ALIGN.CENTER)
        return slide

    def flow(self, title, paragraphs, *, width=BODY_W):
        lines = []
        for paragraph in paragraphs:
            if not _s(paragraph):
                continue
            if lines:
                lines.append('')
            lines.extend(_wrap(paragraph, width))
        if not lines:
            return
        for index in range(0, len(lines), BODY_LINES):
            slide = self.slide(title + ('（续）' if index else ''))
            _text(slide, '\n'.join(lines[index:index + BODY_LINES]), BODY_X, BODY_Y,
                  width, 5.3, name='Report body')

    def table(self, title, headings, rows, widths=None):
        if not rows:
            return
        widths = widths or [BODY_W / len(headings)] * len(headings)
        # A single unusually long cell also splits into continued rows/pages.
        parts = []
        for row in rows:
            lines = [_wrap(value, width - .16, 12) for value, width in zip(row, widths)]
            for offset in range(0, max(map(len, lines)), 16):
                segment = ['\n'.join(column[offset:offset + 16]) for column in lines]
                height = max(.43, max(len(column[offset:offset + 16]) for column in lines) * .215 + .13)
                parts.append((segment, height))
        pages, current, height = [], [], .45
        for row, row_h in parts:
            if current and height + row_h > 5.25:
                pages.append(current)
                current, height = [], .45
            current.append((row, row_h))
            height += row_h
        if current:
            pages.append(current)
        for page, records in enumerate(pages):
            slide = self.slide(title + ('（续）' if page else ''))
            total_h = .45 + sum(height for _, height in records)
            table = slide.shapes.add_table(len(records) + 1, len(headings), Inches(BODY_X),
                                           Inches(BODY_Y), Inches(BODY_W), Inches(total_h)).table
            for index, width in enumerate(widths):
                table.columns[index].width = Inches(width)
            for rindex, (values, row_h) in enumerate([(headings, .45), *records]):
                table.rows[rindex].height = Inches(row_h)
                for cindex, value in enumerate(values):
                    cell = table.cell(rindex, cindex)
                    cell.margin_left = cell.margin_right = Inches(.065)
                    cell.margin_top = cell.margin_bottom = Inches(.045)
                    cell.fill.solid()
                    # Header: light grey with red text, so the theme colour stays an accent.
                    cell.fill.fore_color.rgb = RGBColor.from_string('F2F2F2' if rindex == 0 else ('FAFAFA' if rindex % 2 else 'FFFFFF'))
                    cell.text_frame.clear()
                    cell.text_frame.word_wrap = False
                    cell.text_frame.auto_size = MSO_AUTO_SIZE.NONE
                    for lindex, line in enumerate(_s(value).split('\n')):
                        p = cell.text_frame.paragraphs[0] if lindex == 0 else cell.text_frame.add_paragraph()
                        p.alignment = PP_ALIGN.LEFT
                        p.space_before = p.space_after = Pt(0)
                        p.line_spacing = 1.05
                        run = p.add_run()
                        run.text = _display(line)
                        _font(run, 12, bold=rindex == 0, color=THEME_RED if rindex == 0 else TEXT)


def _notes(view, section):
    panel = (view.get('panels') or {}).get(section) or {}
    rows = []
    if panel.get('text'):
        rows.append('研究员导语：' + panel['text'] if section == 'summary' else '研究员按语：' + panel['text'])
    for ref in panel.get('references') or []:
        rows.append(_join('引用记录摘要', ref.get('title'), 'v' + _s(ref.get('revision')))
                    + '\n' + _s(ref.get('body_text') or ref.get('summary'))
                    + ('\n引用正文为节选，完整记录保留在原页面。' if ref.get('body_text_truncated') else ''))
    return rows


def _cover(deck, view):
    label, stamp = _period(view)
    title = _s(view.get('report_title')) or (_s(view.get('workbench_name')) or '研究工作台') + ' ' + label
    lines = _wrap(title, 11.7, 28)
    slide = deck.slide('', cover=True)
    _rect(slide, .5, 2.02, .07, 3.7, THEME_RED)
    _text(slide, '\n'.join(lines[:4]), .85, 2.1, 11.6, 2.15, size=28, bold=True, name='Cover title')
    _text(slide, _join(label, stamp), .85, 4.55, 11.6, .42, size=18)
    latest = _latest_data(view)
    _text(slide, (_s(view.get('workbench_name')) or '研究工作台') + '\n最近数据日：' + latest,
          .85, 5.2, 11.6, .85, size=15, color=MUTED)
    if len(lines) > 4:
        deck.flow('报告标题（续）', ['\n'.join(lines[4:])])


def _lead(deck, view):
    rows = _notes(view, 'summary')
    conclusion, judgment = view.get('conclusion') or {}, view.get('judgment') or {}
    rows.append('研判结论：' + _join(conclusion.get('tone') or judgment.get('tone') or '未填写基调',
                                  conclusion.get('text') or '当前保存记录未填写结论说明。'))
    rows.append('沿用已保存研判 · 不是本次新生成的结论')
    overview = view.get('strategy_overview') or {}
    rows.append('策略概况：' + _s(overview.get('text') or '尚无已保存策略'))
    triggered = overview.get('triggered') or []
    if triggered:
        rows.append('已触发条件：\n' + '\n'.join('• ' + _s(row) for row in triggered[:4]))
    if len(triggered) > 4:
        rows.append(f'另有 {len(triggered) - 4} 项，见策略。')
    deck.flow('导语', rows)


def _judgment(deck, view):
    judgment = view.get('judgment') or {}
    intro = []
    for brief in view.get('briefs') or []:
        intro.append(_join('日报结论', brief.get('trade_date'), brief.get('status')) + '\n' + _s(brief.get('summary')))
        intro.append(_join('基调：' + _s(brief.get('tone')), brief.get('stance')))
        if brief.get('range_low') is not None and brief.get('range_high') is not None:
            intro.append('参考区间：' + _s(brief.get('range_low_text')) + ' – ' + _s(brief.get('range_high_text')))
        for claim in brief.get('claims') or []:
            intro.append(_s(claim.get('text')) + '\n' + '\n'.join(_join(row.get('name') or '已保存证据', row.get('value_text'), row.get('as_of')) for row in claim.get('evidence') or []))
        for row in brief.get('unverified') or []:
            intro.append('待核验：' + _join(row.get('text'), row.get('reason')))
    if judgment:
        intro.append(_join('沿用已保存研判', judgment.get('status'), judgment.get('tone'), judgment.get('written_at')))
        if judgment.get('tone_note') and not (view.get('panels') or {}).get('summary', {}).get('visible', True):
            intro.append(_s(judgment['tone_note']))
        intro.append('主要矛盾：' + _s(judgment.get('contradiction') or '尚未填写'))
    else:
        intro.append('未登记研究判断。')
    deck.flow('研判', intro)
    if judgment.get('cases'):
        columns = []
        latest = view.get('latest_values') or {}
        for side in judgment['cases']:
            lines = _wrap(side.get('claim') or '待补充', 5.9)
            for item in side.get('evidence') or []:
                value = latest.get(item.get('series_id')) or {}
                text = _s(item.get('text')) + '\n' + _join(item.get('name') or '未绑定指标', value.get('value_text'), value.get('as_of'), value.get('stale_note'))
                lines += [''] + _wrap(text, 5.9)
            columns.append((side.get('label') or '依据', lines))
        for offset in range(0, len(columns), 2):
            group = columns[offset:offset + 2]
            pages = max((len(lines) + 14) // 15 for _, lines in group)
            for index in range(pages):
                slide = deck.slide('研判 · 支持依据 / 反对依据' + ('（续）' if index else ''))
                for cindex, (label, lines) in enumerate(group):
                    x = .4 + cindex * 6.4165
                    _text(slide, label, x, 1.48, 5.95, .35, size=16, bold=True, color=THEME_RED)
                    _text(slide, '\n'.join(lines[index * 15:(index + 1) * 15]), x, 1.94, 5.95, 4.8)
    preview = view.get('rule_preview') or {}
    if preview:
        deck.flow('研判 · 条件口径', [_join(preview.get('label'), preview.get('notice'))])
    for key, label in [('thresholds', '区间与阈值'), ('falsifiers', '证伪条件')]:
        rows = []
        for row in judgment.get(key) or []:
            state = _join(row.get('state'), '当前规则试算' if row.get('is_preview') else '',
                          '原已记录：' + _s(row.get('recorded_state')) if row.get('is_preview') and row.get('recorded_state') else '')
            rows.append([row.get('name'), row.get('series_name'), row.get('condition'),
                         _join(state, row.get('stale_note'), row.get('gap_note')),
                         row.get('current_value_text') if row.get('current_value') is not None else '暂无值',
                         '、'.join(row.get('evidence_dates') or []) or '未注明'])
        deck.table('研判 · ' + label, ['条件名', '指标', '条件', '状态', '当前值', '日期'], rows,
                   [2.05, 2.1, 2.2, 2.25, 1.65, 2.283])
    tail = []
    for row in view.get('pending_conditions') or []:
        tail.append('待验证条件：' + _s(row.get('text')))
    for row in view.get('research_events') or []:
        tail.append(_join('版本变化', row.get('written_at'), row.get('status'), row.get('recorded_at')))
    deck.flow('研判 · 按语与引用', tail + _notes(view, 'research'))
    changes = view.get('changes') or []
    if changes:
        deck.table('研判 · 主要变化', ['指标', '首个有效值', '末个有效值', '变化', '数据日期与口径'],
                   [[_join(row.get('name'), row.get('unit')), row.get('first_text'), row.get('last_text'),
                     _join(row.get('change_text'), _s(row.get('change_pct_text')) + '%' if row.get('change_pct') is not None else ''),
                     _join((row.get('first') or {}).get('as_of'), (row.get('last') or {}).get('as_of'), row.get('note'))] for row in changes])


def _figures(deck, view, images, per_page):
    deck.flow('图表 · 研究员按语', _notes(view, 'dashboard'))
    figures = view.get('report_figures') or []
    if not figures:
        deck.flow('图表', ['本期报告暂无图表。'])
        return
    if view.get('frozen'):
        deck.flow('图表', ['历史归档不重新绘图'] + [
            _join(row.get('title'), row.get('caption'), '数据截至 ' + _s(row.get('as_of'))) for row in figures])
        return
    pages = (len(figures) + per_page - 1) // per_page
    overflow = []
    for offset in range(0, len(figures), per_page):
        slide = deck.slide(f'图表（{offset // per_page + 1}/{pages}）')
        for figure, slot in zip(figures[offset:offset + per_page], chart_slots(per_page)):
            x, y, w, h = slot
            title_lines = _wrap(figure.get('title') or '报告图表', w, 14)
            _text(slide, title_lines[0], x, y - .34, w, .31, size=14, bold=True)
            metadata = _join(figure.get('caption'), '单位：' + _s(figure.get('unit') or '未注明'),
                             '数据截至 ' + _s(figure.get('as_of') or '未注明'))
            meta_lines = _wrap(metadata, w, 11)
            _text(slide, '\n'.join(meta_lines[:2]), x, y + h + .065, w, .48, size=11, color=MUTED)
            if len(title_lines) > 1 or len(meta_lines) > 2:
                overflow.append(_s(figure.get('title')) + '\n' + metadata)
            raw = images.get(figure['key'])
            if raw:
                deck_image = slide.shapes.add_picture(BytesIO(raw), Inches(x), Inches(y), width=Inches(w), height=Inches(h))
                deck_image.name = 'Report chart ' + figure['key']
            else:
                _rect(slide, x, y, w, h, 'F0F0F0')
                reason = '图表读取失败：' + _s(figure.get('notice') or '未能取得可用图片')
                reason_lines = _wrap(reason, w - .4, 14)
                maximum = max(1, int((h - .3) / .25))
                _text(slide, '\n'.join(reason_lines[:maximum]), x + .2, y + .15, w - .4,
                      h - .3, size=14, color=MUTED, name='Unavailable chart')
                if len(reason_lines) > maximum:
                    overflow.append(_s(figure.get('title')) + '\n' + reason)
    if overflow:
        deck.flow('图表 · 图说（续）', overflow)


def _strategy(deck, view):
    deck.flow('策略 · 按语与引用', _notes(view, 'watch'))
    cards, plans = view.get('research_cards') or [], view.get('strategies') or []
    for card in cards:
        title = _s(card.get('title')) or '已保存策略'
        rows = ['方案名：' + title,
                _join('方向：' + _s(card.get('direction_label')), card.get('status_label'))]
        for key, label in [('thesis', '核心逻辑 / 适用范围'), ('trigger', '适用 / 触发'),
                           ('response_action', '应对措施'), ('invalidation', '暂停 / 撤回'),
                           ('review_date', '复核日期'), ('watch_for', '执行前核对')]:
            if card.get(key):
                rows.append(label + '：' + _s(card[key]))
        monitor = card.get('monitor_view') or {}
        for row in monitor.get('conditions') or []:
            rows.append('所引条件：' + _join(row.get('title'), row.get('state'), row.get('field_label'),
                row.get('condition'), row.get('value_text'), row.get('as_of'), row.get('stale_note'),
                row.get('note'), '所引研判已有新版本' if row.get('outdated') else ''))
        for row in monitor.get('indicators') or []:
            rows.append('所引指标：' + _join(row.get('title'), row.get('field_label'), row.get('value_text'),
                row.get('change_text'), row.get('as_of'), row.get('stale_note'), row.get('note')))
        if card.get('citations') and not (monitor.get('conditions') or monitor.get('indicators')):
            rows.append((view.get('coverage') or {}).get('research_cards_monitor_error') or '所引条件将在当前研究台中核对。')
        deck.flow('策略 · ' + _wrap(title, 10, 26)[0], rows)
    for plan in plans:
        title = _s(plan.get('title')) or '交易计划'
        rows = ['交易计划：' + title, _join(plan.get('status_label'), plan.get('mode_label')), _s(plan.get('thesis'))]
        for leg in plan.get('legs') or []:
            rows.append(_join(leg.get('contract'), {'buy': '买入', 'sell': '卖出'}.get(leg.get('side'), leg.get('side'))))
        entry = plan.get('entry') or {}
        if entry:
            rows.append('入场：' + _s(entry.get('low_text')) + ' – ' + _s(entry.get('high_text')))
        if plan.get('invalidation'):
            rows.append('失效 / 复核：' + plan['invalidation'])
        if plan.get('review_at'):
            rows.append('复核时间：' + _s(plan['review_at']))
        deck.flow('交易计划 · ' + _wrap(title, 9, 26)[0], rows)
    if not cards and not plans:
        deck.flow('策略', ['尚无已保存策略。'])
    deck.flow('策略 · 期内事件', [_join(row.get('at'), row.get('title'), row.get('label'), row.get('reason'))
                            for row in view.get('strategy_events') or []])


def _clip(value, width, lines=2, size=12):
    wrapped = _wrap(value, width, size)
    if len(wrapped) <= lines:
        return '\n'.join(wrapped)
    last = wrapped[lines - 1].rstrip()
    while last and len(_wrap(last + '…', width, size)) > 1:
        last = last[:-1]
    return '\n'.join(wrapped[:lines - 1] + [last + '…'])


def _logic_columns(view):
    """Read the shared, visibility-filtered projection without reviving old records."""
    panels = view.get('panels') or {}
    research_visible = (panels.get('research') or {}).get('visible', True)
    watch_visible = (panels.get('watch') or {}).get('visible', True)
    logic = view.get('logic_strategy')
    if not isinstance(logic, dict):
        judgment = view.get('judgment') or {}
        judgment = judgment if judgment.get('status') == '生效' else {}
        logic = {'contradiction': judgment.get('contradiction'), 'tone': judgment.get('tone'),
                 'conditions': [*(judgment.get('thresholds') or []), *(judgment.get('falsifiers') or [])]}
    research = []
    if research_visible and logic.get('active', True) and any(logic.get(key) for key in ('contradiction', 'tone', 'conditions')):
        contradiction = logic.get('contradiction')
        if isinstance(contradiction, dict):
            contradiction = contradiction.get('text')
        research.extend(['基调：' + (_s(logic.get('tone')) or '未填写'),
                         '主要矛盾：' + (_s(contradiction) or '未填写')])
        for item in logic.get('conditions') or []:
            research.append('关注：' + _join(item.get('dimension_label'), item.get('name'), item.get('condition'),
                item.get('state'), item.get('current_value_text'), item.get('stale_note')))
    if not research:
        research = ['暂无生效研判']
    strategies = logic.get('strategies') if watch_visible else []
    if strategies is None:
        strategies = []
        for row in view.get('research_cards') or []:
            strategies.append({'title': row.get('title'), 'text': _join(row.get('direction_label'),
                row.get('thesis'), '触发：' + _s(row.get('trigger')) if row.get('trigger') else '',
                '应对：' + _s(row.get('response_action')) if row.get('response_action') else '')})
        for row in view.get('strategies') or []:
            directions = ' / '.join(dict.fromkeys({'buy': '买入', 'sell': '卖出', 'long': '做多', 'short': '做空'}.get(
                leg.get('side'), leg.get('side') or '') for leg in row.get('legs') or []))
            entry = row.get('entry') or {}
            trigger = ' – '.join(_s(entry[key]) for key in ('low_text', 'high_text') if entry.get(key))
            strategies.append({'title': row.get('title'), 'text': _join(directions, row.get('thesis'),
                '触发：' + trigger if trigger else '',
                '应对：' + _s(row.get('invalidation')) if row.get('invalidation') else '')})
    plans = [_join(item.get('title'), _s(item.get('text')).replace('；', ' · ')) for item in strategies]
    return research, [line for line in plans if line] or ['暂无策略方案']


def _logic_lines(view):
    research, strategies = _logic_columns(view)
    return ['研判：' + line for line in research] + ['策略：' + line for line in strategies]


def _overview_rows(view):
    if not (view.get('panels', {}).get('summary') or {}).get('visible', True):
        return []
    return [[_s(row.get('label')),
             '\n'.join(filter(None, [_s(row.get('change_summary')),
                *(_full_fact_text(fact) for fact in row.get('facts') or [])])),
             _s(row.get('sentence')),
             row.get('impact_label') or {'bullish': '偏多', 'bearish': '偏空',
                 'neutral': '中性', 'undecided': '未判断'}.get(row.get('impact'), '未判断')]
            for row in view.get('dimension_overview') or []]


_OVERVIEW_WIDTHS = [.95, 6.15, 4.45, .983]
_IMPACT_COLORS = {'偏多': ('FBE7E8', THEME_RED), '偏空': ('F0E5E7', '7D3540'),
                  '中性': ('F2EEEE', '665A5B'), '未判断': ('F3F3F3', MUTED)}


def _fact_parts(fact):
    """Shorten presentation labels, retaining the actual value and comparison."""
    display = fact.get('display') or {}
    latest = display.get('latest')
    if not latest and fact.get('latest') is not None:
        latest = _s(fact['latest'])
    if not latest:
        return None
    name = _s(fact.get('readable_name') or fact.get('name'))
    name = re.sub(r'\s+', ' ', name).strip()
    unit = _s(fact.get('unit')).strip()
    if unit == '未注明':
        unit = ''
    value = _s(latest) + unit
    details = []
    no_update = fact.get('update_status') == 'no_update' or fact.get('direction') == 'no_update'
    if no_update:
        details.append('本期无新数据')
    else:
        baseline = fact.get('baseline') or {}
        change = display.get('change')
        if not change and fact.get('change') is not None:
            change = f"{fact['change']:+g}" if isinstance(fact['change'], (int, float)) else _s(fact['change'])
        if baseline.get('date') and change:
            details.append('较' + _s(baseline['date'])[5:10] + ' ' + _s(change))
    position = fact.get('position') or {}
    if position.get('rank') is not None and position.get('n', 0) >= 3 and position.get('text'):
        ranking = re.sub(r'^近\s*\d+\s*年\s*', '', _s(position['text']))
        details.append(re.sub(r'\s+', '', ranking))
    return name, value, '（' + '，'.join(details) + '）' if details else ''


def _full_fact_text(fact):
    original = _s(fact.get('text'))
    if original:
        return original
    parts = _fact_parts(fact)
    if not parts:
        return ''
    name, value, details = parts
    result = name + ' ' + value + details
    if fact.get('update_status') == 'no_update' and fact.get('latest_date'):
        result += ' · 最新 ' + _s(fact['latest_date'])[:10]
    return result


def _named_fact(name, remainder, width, size):
    # The reference source remains complete in notes; only its display label is
    # shortened. Numeric values, signs and units never get an ellipsis.
    original = name
    while name and len(_wrap(name + ' ' + remainder, width, size)) > 1:
        name = name[:-1]
    if name != original:
        while name and len(_wrap(name + '… ' + remainder, width, size)) > 1:
            name = name[:-1]
        name += '…'
    return (name + ' ' + remainder).strip()


def _short_fact(fact, width, size, *, reference=False):
    parts = _fact_parts(fact)
    if parts is None:
        original = _s(fact.get('text'))
        name = _s(fact.get('readable_name') or fact.get('name'))
        if name and original.startswith(name):
            return '\n'.join(_wrap(_named_fact(name, original[len(name):].strip(), width, size), width, size))
        if len(_wrap(original, width, size)) > 1:
            # Old archives may contain only rendered prose. Do not guess where
            # an identifier ends and a numerical observation begins.
            return '完整指标事实见备注'
        return original
    name, value, details = parts
    return '\n'.join(_wrap(_named_fact(name, value + ('' if reference else details), width, size), width, size))


def _compact_row(source, size, fact_limit=2, lines=2):
    width = _OVERVIEW_WIDTHS[1] - .14
    facts = (source.get('facts') or [])[:fact_limit]
    all_no_update = source.get('no_update') or facts and all(
        fact.get('update_status') == 'no_update' or fact.get('direction') == 'no_update' for fact in facts)
    if all_no_update:
        summary = _s(source.get('change_summary')) or '本期无新数据'
        if not source.get('change_summary') and source.get('latest_date'):
            summary += '（最新 ' + _s(source['latest_date'])[:10] + '）'
        if lines == 1:
            fact_text = summary + ('；' + _short_fact(facts[0], max(1, width - 3), size, reference=True) if facts else '')
        else:
            each_width = (width - .2 * (len(facts) - 1)) / max(1, len(facts))
            references = '；'.join(_short_fact(fact, each_width, size, reference=True) for fact in facts)
            fact_text = summary + ('\n' + references if references else '')
    else:
        fact_text = '\n'.join(_short_fact(fact, width, size) for fact in facts)
    sentence = _s(source.get('sentence'))
    sentence = sentence if len(sentence) <= 60 else sentence[:59] + '…'
    impact = source.get('impact_label') or {'bullish': '偏多', 'bearish': '偏空',
        'neutral': '中性', 'undecided': '未判断'}.get(source.get('impact'), '未判断')
    return [_clip(source.get('label'), _OVERVIEW_WIDTHS[0] - .12, lines, size), fact_text,
            _clip(sentence, _OVERVIEW_WIDTHS[2] - .14, lines, size), impact]


def _table_records(rows, size=12):
    line_height = size / 72 * 1.08
    return [(row, max(.25, max(len(value.split('\n')) for value in row) * line_height + .075)) for row in rows]


def _overview_table(slide, rows, y=1.7, size=12):
    headings = ['维度', '本期变化', '研究员一句话', '影响']
    records = _table_records(rows, size)
    header_h = .30
    table = slide.shapes.add_table(len(records) + 1, 4, Inches(.4), Inches(y), Inches(BODY_W),
                                   Inches(header_h + sum(height for _, height in records))).table
    for index, width in enumerate(_OVERVIEW_WIDTHS):
        table.columns[index].width = Inches(width)
    offset = y
    for rindex, (values, height) in enumerate([(headings, header_h), *records]):
        table.rows[rindex].height = Inches(height)
        for cindex, value in enumerate(values):
            cell = table.cell(rindex, cindex)
            cell.margin_left = cell.margin_right = Inches(.055)
            cell.margin_top = cell.margin_bottom = Inches(.03)
            cell.fill.solid()
            cell.fill.fore_color.rgb = RGBColor.from_string('F2F2F2' if rindex == 0 else ('FCFAFA' if rindex % 2 else 'FFFFFF'))
            cell.text_frame.clear()
            cell.text_frame.word_wrap = False
            cell.text_frame.auto_size = MSO_AUTO_SIZE.NONE
            if rindex and cindex == 3:
                continue
            for index, line in enumerate(value.split('\n')):
                paragraph = cell.text_frame.paragraphs[0] if index == 0 else cell.text_frame.add_paragraph()
                paragraph.space_before = paragraph.space_after = Pt(0)
                paragraph.line_spacing = Pt(size * 1.08)
                run = paragraph.add_run()
                run.text = line
                _font(run, size, bold=rindex == 0, color=THEME_RED if rindex == 0 else TEXT)
        if rindex:
            label = values[3]
            fill, color = _IMPACT_COLORS.get(label, _IMPACT_COLORS['未判断'])
            x = .4 + sum(_OVERVIEW_WIDTHS[:3]) + .08
            top, tag_h = offset + (height - .25) / 2, .25
            tag = slide.shapes.add_shape(MSO_SHAPE.ROUNDED_RECTANGLE, Inches(x), Inches(top), Inches(.82), Inches(tag_h))
            tag.name = 'Impact tag ' + str(rindex)
            tag.fill.solid()
            tag.fill.fore_color.rgb = RGBColor.from_string(fill)
            tag.line.fill.background()
            tag._element.spPr.append(OxmlElement('a:effectLst'))
            for effect in tag._element.xpath('./p:style/a:effectRef'):
                effect.set('idx', '0')
            _text(slide, label, x, top + .005, .82, .24, size=size, color=color, align=PP_ALIGN.CENTER,
                  name='Impact label ' + str(rindex))
        offset += height
    return offset


def _strategy_summary(item, width, size):
    if not any(key in item for key in ('direction', 'thesis', 'trigger', 'response_action')):
        return _clip(_join(item.get('title'), _s(item.get('text')).replace('；', ' · ')), width, 1, size)
    def units(value):
        return sum(1 if unicodedata.east_asian_width(char) in {'W', 'F'} else .58 for char in value)
    def piece(value, budget):
        value = _s(value) or '未填写'
        return _clip(value, budget * size / 72 / .88, 1, size)
    direction = piece(item.get('direction'), 4)
    budget = width * 72 / size * .88 - units(direction) - units(' ·  · 触发： · 应对：')
    thesis = piece(item.get('thesis'), budget * .44)
    trigger = piece(item.get('trigger'), budget * .28)
    response = piece(item.get('response_action'), budget * .28)
    return direction + ' · ' + thesis + ' · 触发：' + trigger + ' · 应对：' + response


def _logic_block(slide, view, y, size):
    research, strategies = _logic_columns(view)
    _text(slide, '逻辑与策略', .4, y, BODY_W, .25, size=14, bold=True, color=THEME_RED)
    top = y + .29
    width = (BODY_W - .24) / 2
    _text(slide, '研判', .4, top, width, .22, size=size, bold=True, color=THEME_RED)
    _text(slide, '策略', .4 + width + .24, top, width, .22, size=size, bold=True, color=THEME_RED)
    top += .25
    line_h = size / 72 * 1.15
    available = max(1, int((6.86 - top - .03) / line_h))
    # One tone, one contradiction and up to three current conditions.
    left = research[:2]
    projection = view.get('logic_strategy')
    if not isinstance(projection, dict):
        judgment = view.get('judgment') or {}
        projection = {'conditions': [*(judgment.get('thresholds') or []), *(judgment.get('falsifiers') or [])]}
    if research != ['暂无生效研判']:
        for item in (projection.get('conditions') or [])[:3]:
            remainder = _join(item.get('condition'), item.get('state'),
                '当前 ' + _s(item['current_value_text']) if item.get('current_value_text') else '')
            left.append('关注：' + _named_fact(_s(item.get('name') or item.get('dimension_label')),
                remainder, width - .65, size))
    if len(left) > available:
        left = left[:available]
    strategy_sources = projection.get('strategies')
    watch_visible = ((view.get('panels') or {}).get('watch') or {}).get('visible', True)
    if watch_visible and strategy_sources:
        right = [_strategy_summary(item, width - .03, size) for item in strategy_sources[:available]]
    else:
        right = strategies[:available]
    _text(slide, '\n'.join(_clip(line, width - .03, 1, size) for line in left),
          .4, top, width, 6.86 - top, size=size, name='Judgment summary')
    _text(slide, '\n'.join(_clip(line, width - .03, 1, size) for line in right),
          .4 + width + .24, top, width, 6.86 - top, size=size, name='Strategy summary')
    return len(research) > len(left) or len(strategies) > len(right) or any(
        len(_wrap(line, width - .03, size)) > 1 for line in left + right)


def _one_page(deck, view):
    """Fit the overview before considering continuation; preserve full text in notes."""
    rows = _overview_rows(view)
    sources = list(view.get('dimension_overview') or []) if rows else []
    logic = _logic_lines(view)
    # A fixed lower reserve guarantees that eight ordinary dimensions leave
    # room for an actual judgment and strategies, rather than crowding them out.
    table_budget = 3.65
    compact, size = [], 12
    for fact_limit, lines in ((2, 2), (1, 2), (1, 1)):
        for size in (12, 11.5, 11, 10.5):
            compact = [_compact_row(source, size, fact_limit, lines) for source in sources]
            if .30 + sum(height for _, height in _table_records(compact, size)) <= table_budget:
                break
        else:
            continue
        break
    # Unusually large custom configurations can still continue, after the
    # minimum font and one-fact layout have both been exhausted.
    groups, group, used = [], [], .30
    for row, height in _table_records(compact, size):
        if group and used + height > table_budget:
            groups.append(group)
            group, used = [], .30
        group.append(row)
        used += height
    groups.append(group)
    full = ['本期综述（全文附录）'] + ['\n'.join(row) for row in rows] + ['逻辑与策略'] + logic
    if sources:
        full += ['提示明细：' + _s(source.get('label')) + '\n' + '\n'.join(filter(None,
            [_s(source.get('warning_summary')), *(_s(warning) for warning in source.get('warnings') or [])]))
            for source in sources if source.get('warnings') or source.get('warning_summary')]
    full += ['省略文字保留于本页全文附录。条件触发不等于成交。']
    full_text = '\n\n'.join(full)
    shortened = compact != rows
    summary_visible = (view.get('panels', {}).get('summary') or {}).get('visible', True)
    for index, group in enumerate(groups):
        slide = deck.slide((_s(view.get('report_title')) or '本期报告') + ('（续）' if index else ''))
        if summary_visible:
            _text(slide, '本期综述', .4, 1.45, 12.5, .25, size=14, bold=True, color=THEME_RED)
        y = _overview_table(slide, group, size=size) if group else 2.05 if summary_visible else 1.45
        if summary_visible and not group:
            _text(slide, '本期暂无维度综述。', .4, 1.8, 12.5, .3, size=12, color=MUTED)
        shortened = _logic_block(slide, view, y + .12, size) or shortened
        slide.notes_slide.notes_text_frame.text = full_text
    return rows, logic, shortened


def _evidence(deck, view, images, per_page):
    # Group membership is already computed from real dimension bindings by the
    # shared report projection. Reuse the validated canonical figure objects so
    # a revoked source cannot leave an old bitmap in a group copy.
    if not (view.get('panels', {}).get('dashboard') or {}).get('visible', True):
        return
    canonical = {row['key']: row for row in view.get('report_figures') or []}
    groups = view.get('evidence_groups') or [{'label': '其他', 'figures': list(canonical.values())}]
    for group in groups:
        figures = [canonical[row['key']] for row in group.get('figures') or [] if row.get('key') in canonical]
        if not figures:
            continue
        label = _s(group.get('label')) or '其他'
        if view.get('frozen'):
            deck.flow('证据 · ' + label, ['历史归档不重新绘图'] + [
                _join(row.get('title'), row.get('conclusion'), row.get('caption')) for row in figures])
            continue
        for offset in range(0, len(figures), per_page):
            part = figures[offset:offset + per_page]
            slide = deck.slide('证据 · ' + label + f'（{offset // per_page + 1}/{(len(figures) + per_page - 1) // per_page}）')
            for figure, (x, y, width, height) in zip(part, chart_slots(per_page)):
                conclusion = figure.get('conclusion') or '尚未填写图表结论'
                _text(slide, _clip(conclusion, width, 1, 14), x, y - .35, width, .31, size=14, bold=True)
                metadata = _join(figure.get('title'), figure.get('caption'),
                    '单位：' + _s(figure.get('unit') or '未注明'), '数据截至 ' + _s(figure.get('as_of') or '未注明'),
                    '来源：' + _s(figure.get('source') or '沿用报告所引资料'))
                _text(slide, _clip(metadata, width, 3, 11), x, y + height + .06, width, .7, size=11, color=MUTED)
                raw = images.get(figure['key'])
                if raw:
                    shape = slide.shapes.add_picture(BytesIO(raw), Inches(x), Inches(y), width=Inches(width), height=Inches(height))
                    shape.name = 'Report chart ' + figure['key']
                else:
                    _rect(slide, x, y, width, height, 'F0F0F0')
                    _text(slide, _clip('图表读取失败：' + _s(figure.get('notice') or '未能取得可用图片'), width - .4, 6, 14),
                          x + .2, y + .2, width - .4, height - .4, size=14, color=MUTED, name='Unavailable chart')
            slide.notes_slide.notes_text_frame.text = '\n\n'.join(_join(row.get('title'), row.get('conclusion'), row.get('caption'), row.get('source')) for row in part)


def build_report_deck(view, images=None, options=None):
    """Build the default one-page report or append evidence and full records.

    Legacy section-only options retain the old interface for existing clients.
    New modes always rebuild from the same server-owned view as the report page.
    """
    options, images = options or {}, images or {}
    mode = options.get('mode')
    if mode is not None and options.get('sections') is not None:
        raise ValueError('导出模式与旧分节选项不能同时使用')
    if mode is None and options.get('sections') is not None:
        return _legacy_report_deck(view, images, options)
    mode = mode or 'one_page'
    if mode not in {'one_page', 'evidence', 'full'}:
        raise ValueError('导出模式无效')
    per_page = options.get('figures_per_slide', 2)
    if per_page not in (1, 2):
        raise ValueError('证据附录每页图数应为 1 或 2')
    deck = _Deck(view)
    rows, logic, shortened = _one_page(deck, view)
    if mode in {'evidence', 'full'}:
        _evidence(deck, view, images, per_page)
        if shortened:
            deck.flow('综述全文附录', ['\n'.join(row) for row in rows] + ['逻辑与策略'] + logic)
    if mode == 'full':
        panels = view.get('panels') or {}
        if (panels.get('summary') or {}).get('visible', True):
            deck.flow('完整记录 · 原导语', _notes(view, 'summary'))
        if (panels.get('research') or {}).get('visible', True):
            _judgment(deck, view)
        if (panels.get('dashboard') or {}).get('visible', True):
            deck.flow('完整记录 · 原图表按语', _notes(view, 'dashboard'))
        if (panels.get('watch') or {}).get('visible', True):
            _strategy(deck, view)
    buffer = BytesIO()
    deck.prs.save(buffer)
    return buffer.getvalue()


def _legacy_report_deck(view, images=None, options=None):
    """Return pptx bytes. Inputs are server-owned data; no files or model calls.

    The caller enforces upload ACL, exact manifest/revision and PNG bounds. This
    writer deliberately does not fetch data, persist output, or interpret intent.
    Hidden report panels cannot be enabled through export options.
    """
    options, images = options or {}, images or {}
    per_page = options.get('figures_per_slide', 2)
    chart_slots(per_page)
    selected = options.get('sections') or {}
    panels = view.get('panels') or {}
    visible = {key: selected.get(key, True) is True and (panels.get(key) or {}).get('visible', True) is True
               for key in ('summary', 'research', 'dashboard', 'watch')}
    deck = _Deck(view)
    _cover(deck, view)
    if visible['summary']:
        _lead(deck, view)
    if visible['research']:
        _judgment(deck, view)
    if visible['dashboard']:
        _figures(deck, view, images, per_page)
    if visible['watch']:
        _strategy(deck, view)
    sources = []
    if visible['dashboard']:
        for row in view.get('report_figures') or []:
            sources.append(_join(row.get('title'), '来源：' + _s(row.get('source') or '沿用报告所引资料'),
                                 '数据截至 ' + _s(row.get('as_of') or '未注明')))
    deck.flow('说明', ['文字沿用已保存研判、策略与研究员按语，不是本次新生成的结论。',
                        '条件触发不等于成交；策略方案与交易计划不代表实际成交记录。',
                        '历史归档不重新绘图。' if view.get('frozen') else '',
                        '图表数据来源与截至日：\n' + '\n'.join(sources) if sources else '本次未导出图表。',
                        '生成时间：' + _s(view.get('generated_at') or '未注明')])
    buffer = BytesIO()
    deck.prs.save(buffer)
    return buffer.getvalue()
