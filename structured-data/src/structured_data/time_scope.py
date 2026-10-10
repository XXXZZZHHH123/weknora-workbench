"""Calendar scope from explicit human text, with later clarifications taking precedence.

These are calendar grammar rules, not commodity-year definitions. An unspecified
business year remains ambiguous; a confirmed natural-year range does not.
"""
import calendar
from datetime import date
import re


def _year(value, reference):
    n = int(value)
    return reference // 100 * 100 + n if len(value) == 2 else n


def _bounds(y1, m1, y2, m2):
    if not (1 <= m1 <= 12 and 1 <= m2 <= 12):
        raise ValueError('月份不存在，请修正月份')
    start = date(y1, m1, 1)
    end = date(y2, m2, calendar.monthrange(y2, m2)[1])
    if start > end:
        raise ValueError('查询开始日期不能晚于结束日期')
    return {'start_date': start.isoformat(), 'end_date': end.isoformat()}


def calendar_scope(question):
    # Field JSON contains source dates and IDs, not human temporal instructions.
    clean = re.sub(r'\n(?:用户)?已确认字段：[^\n]*', '', question)
    parts = clean.split('\n用户补充：')
    context_pair = re.search(r'(?<!\d)(\d{4})\s*(?:/|、|和|与|到|至|[-—~～])\s*(\d{4}|\d{2})(?:年|年度)?(?!\d)', clean)
    pair = None
    if context_pair:
        a = int(context_pair[1]); pair = (a, _year(context_pair[2], a))
    # Latest human clarification wins over earlier crop-year wording or ranges.
    for text in reversed(parts):
        full_days = re.findall(r'(?<!\d)(\d{4})[-/年](\d{1,2})[-/月](\d{1,2})日?(?!\d)', text)
        if full_days:
            days = []
            for y, m, d in full_days:
                try: day = date(int(y), int(m), int(d)).isoformat()
                except ValueError: raise ValueError('所问日期不存在，请修正日期；尚未查询数值') from None
                if day not in days: days.append(day)
            for m, d in re.findall(r'(?<![\d/年-])(\d{1,2})月(\d{1,2})日', text):
                try: day = date(int(days[0][:4]), int(m), int(d)).isoformat()
                except ValueError: raise ValueError('所问日期不存在，请修正日期；尚未查询数值') from None
                if day not in days: days.append(day)
            bounds = {}
            if len(days) >= 2 and re.search(r'至|到|范围|期间|之间', text):
                bounds = {'start_date': min(days), 'end_date': max(days)}
            return {'dates': days, 'bounds': bounds}
        month_range = re.search(r'(?<!\d)(\d{4})年\s*(\d{1,2})月\s*(?:到|至|[-—~～])\s*(?:(\d{4}|\d{2})年\s*)?(\d{1,2})月', text)
        if month_range:
            y1, m1 = int(month_range[1]), int(month_range[2])
            y2 = _year(month_range[3], y1) if month_range[3] else y1
            return {'dates': [], 'bounds': _bounds(y1, m1, y2, int(month_range[4]))}
        year_range = re.search(r'(?<!\d)(\d{4})年?\s*(?:至|到|和|与|、|[-—~～])\s*(\d{4}|\d{2})年(?!\d*月)', text)
        if year_range:
            y1 = int(year_range[1]); y2 = _year(year_range[2], y1)
            return {'dates': [], 'bounds': _bounds(y1, 1, y2, 12)}
        if pair and re.search(r'(?:(?:两|二|2)(?:个)?(?:年|自然年).{0,6}(?:整年|全年|完整)|(?:两|二|2)(?:个)?完整(?:的)?(?:自然)?年|自然年|各自全年)', text):
            return {'dates': [], 'bounds': _bounds(pair[0], 1, pair[1], 12)}
        # Never silently narrow an unparsed range to its first recognized month.
        month = re.search(r'(?<!\d)(\d{4})年(\d{1,2})月', text)
        if month:
            if re.search(r'\d{1,2}月\s*(?:至|到|[-—~～])', text):
                raise ValueError('时间范围的终点未能识别，请写明结束年月；尚未计算')
            y, m = map(int, month.groups())
            return {'dates': [], 'bounds': _bounds(y, m, y, m)}
        crop = re.search(r'(?<!\d)(\d{4})/(\d{2,4})年度', text)
        if crop:
            return {'dates': [], 'bounds': {}, 'period_label': crop.group(0).replace('年度', ''), 'needs_period_definition': True}
        yearly = re.search(r'(?<!\d)(\d{4})年', text)
        if yearly:
            y = int(yearly[1])
            return {'dates': [], 'bounds': _bounds(y, 1, y, 12)}
    return {'dates': [], 'bounds': {}}
