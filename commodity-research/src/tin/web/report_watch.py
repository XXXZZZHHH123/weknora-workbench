"""Present saved research conditions, independently of import diagnostics."""
import re

from tin.schemas.judgment import EXPRESSION


# These are storage/protocol identifiers, never business-intent classifiers.
_FIELD_ID = re.compile(r'kb-[0-9a-f]{48}(?![0-9a-f])|generic\.[0-9a-f]{12,48}(?![0-9a-f])')
_VENDOR_ID = re.compile(r'(?<![A-Za-z0-9])(?:ID\d{5,}|[A-Za-z]\d{7,})(?![A-Za-z0-9])')
_COMPARISON = {'>': '高于', '>=': '不低于', '<': '低于', '<=': '不高于'}


def _plain(value):
    return value.strip() if isinstance(value, str) else ''


def display_text(value, names=None):
    names = names or {}
    text = _plain(value)
    # Resolve only actual IDs present in this authorized report's name map.
    # ASCII boundaries allow an identifier immediately followed by Chinese.
    for identifier, name in sorted(names.items(), key=lambda item: -len(item[0])):
        if len(identifier) < 3 or not _plain(name):
            continue
        label = _FIELD_ID.sub('相关指标', _plain(name))
        text = re.sub(r'(?<![A-Za-z0-9_.-])' + re.escape(identifier) + r'(?![A-Za-z0-9_.-])',
                      lambda _: label, text)

    def label(match):
        name = _plain(names.get(match.group()))
        return _FIELD_ID.sub('相关指标', name) if name else '相关指标'

    text = _VENDOR_ID.sub('', _FIELD_ID.sub(label, text))
    return ' / '.join(part.strip() for part in text.split(' / ') if part.strip())


def watch_items(judgment, strategies=(), research_cards=()):
    judgment = judgment if isinstance(judgment, dict) else {}
    items = []
    for row in judgment.get('falsifiers') or []:
        items.append({'kind': 'falsifier', 'text': row.get('desc') or '复核已保存证伪条件',
            'series_id': row.get('series_id'), 'expression': row.get('expression'),
            'consecutive_periods': row.get('consecutive_periods', 1), 'unit': row.get('unit_confirmed') or ''})
    for row in judgment.get('thresholds') or []:
        items.append({'kind': 'threshold', 'text': row.get('name') or '观察已保存区间',
            'series_id': row.get('series_id'), 'operator': row.get('operator'), 'value': row.get('value')})
    for row in judgment.get('unstructured') or []:
        # The import protocol explicitly separates proposed research rules from
        # source-selection/unit diagnostics. Reasons stay in logic management.
        business = _plain(row.get('business_condition'))
        if business and row.get('rule_kind') in {'threshold', 'falsifier'}:
            items.append({'kind': 'observation', 'text': business, 'requires_manual_observation': True})
        elif ('business_condition' not in row and 'rule_kind' not in row
              and row.get('section') in {'thresholds', 'falsifiers'} and _plain(row.get('text'))):
            items.append({'kind': 'observation', 'text': row['text'], 'requires_manual_observation': True})
    for row in strategies:
        if row.get('status') in {'watching', 'open', 'draft'}:
            items.append({'kind': 'strategy', 'text': _plain(row.get('title')) + '：'
                + (_plain(row.get('invalidation')) or '复核入场、退出与失效条件')})
    for row in research_cards:
        conditions = [_plain(row.get('trigger'))]
        if _plain(row.get('invalidation')):
            conditions.append('失效时复核：' + row['invalidation'])
        if _plain(row.get('review_date')):
            conditions.append('复核日期 ' + row['review_date'])
        items.append({'kind': 'research_card', 'text': _plain(row.get('title')) + '：' + '；'.join(filter(None, conditions))})
    return items


def present_watch(items, names, units=None):
    units = units or {}
    result = []
    for item in items:
        row = dict(item)
        text = display_text(row.get('text'), names)
        identifier = row.get('series_id')
        name = display_text(names.get(identifier)) or '该指标'
        condition = ''
        if row.get('kind') == 'threshold':
            value = row.get('value')
            unit = _plain(units.get(identifier))
            if row.get('operator') == 'between' and isinstance(value, list) and len(value) == 2:
                condition = f'{name}处于 {value[0]} — {value[1]}{unit} 区间'
            elif row.get('operator') in _COMPARISON and type(value) in {int, float}:
                condition = f'{name}{_COMPARISON[row["operator"]]} {value}{unit}'
        elif row.get('kind') == 'falsifier':
            match = EXPRESSION.fullmatch(_plain(row.get('expression')))
            if match:
                subject = '较上一期变化' if match[1] == 'change' else '数值'
                condition = f'{name}{subject}{_COMPARISON[match[2]]} {match[3]}{row.get("unit") or ""}，连续 {row.get("consecutive_periods") or 1} 期'
        row['text'] = text + ('；验证：' + condition if condition else '')
        if row['text']:
            result.append(row)
    return result
