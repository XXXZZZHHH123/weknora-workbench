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
    return str(value) if isinstance(value, (str, int, float)) else ''


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


def build_report_deck(view, images=None, options=None):
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
