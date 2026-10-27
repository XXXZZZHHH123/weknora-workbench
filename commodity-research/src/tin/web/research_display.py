"""Display authorized research metadata; never select sources or change bindings."""
import re

from tin.schemas.judgment import EXPRESSION
from tin.web.report_watch import display_text

_TAG = re.compile(r'\[\s*(?:kb-[0-9a-f]{48}|[A-Za-z][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+)+)\s*\]')
_DEFAULT_TITLE = re.compile(r'^(?:研判|研究判断)\s*(?:v\d+|#\d+)?$')
_IDENTIFIER = re.compile(r'kb-[0-9a-f]{48}|[A-Za-z][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+)+')
_COMPARISON = {'>': '高于', '>=': '不低于', '<': '低于', '<=': '不高于'}


def human_text(value, names=None):
    text = str(value) if isinstance(value, (str, int, float)) else ''
    text = _TAG.sub('', text)
    # Only literal IDs occurring in this text need labels; do not iterate an
    # entire source directory for each small paragraph or template cell.
    labels = {match[0]: (names or {})[match[0]] for match in _IDENTIFIER.finditer(text)
              if match[0] in (names or {})}
    return _TAG.sub('', display_text(text, labels)).strip()


def indicator_name(identifier, names=None):
    return human_text((names or {}).get(identifier)) or '指标名称待核对'


def reference_label(row, fallback='研究判断'):
    def get(key):
        return row.get(key) if isinstance(row, dict) else getattr(row, key, None)
    title = human_text(get('title'))
    if not title or _DEFAULT_TITLE.fullmatch(title):
        title = fallback
    stamp = get('written_at') or get('created_at') or get('updated_at')
    if stamp:
        title += ' · ' + str(stamp)[:16].replace('T', ' ')
    return title


def condition_text(expression):
    match = EXPRESSION.fullmatch(str(expression or '').strip())
    if match:
        return ('较上一期变化' if match[1] == 'change' else '指标数值') + _COMPARISON[match[2]] + ' ' + match[3]
    return human_text(expression)


def priority_labels(order, thresholds):
    names = {row['id']: human_text(row.get('name')) or '已保存观察条件'
             for row in thresholds or [] if isinstance(row, dict)}
    return ' → '.join(names.get(identifier, '已保存观察条件') for identifier in order or [])


def register_display(environment):
    environment.globals.update(human_text=human_text, indicator_name=indicator_name,
        reference_label=reference_label, condition_text=condition_text, priority_labels=priority_labels)
