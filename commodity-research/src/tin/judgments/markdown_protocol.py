"""Read explicit exported rule fields, never infer intent from prose keywords."""
from decimal import Decimal
import json
import re

from tin.schemas.judgment import EXPRESSION


TARGET_LABELS = {'bull': '多方原假设', 'bear': '空方原假设'}


def _numeric(value):
    if isinstance(value, list):
        if len(value) != 2:
            raise ValueError('invalid interval')
        return tuple(_numeric(item) for item in value)
    if type(value) not in (int, float, Decimal):
        raise ValueError('invalid numeric type')
    number = Decimal(str(value))
    if not number.is_finite():
        raise ValueError('nonfinite rule')
    return number


def threshold_signature(rule):
    return (rule['operator'], _numeric(rule['value']), rule['type'], rule['horizon'])


def falsifier_signature(rule):
    match = EXPRESSION.fullmatch(rule['expression'].strip())
    if match is None:
        raise ValueError('invalid expression')
    return (*match.groups()[:2], Decimal(match.group(3)), rule.get('consecutive_periods', 1),
            rule['side'], rule['unit_confirmed'])


def _exported_rule_lines(text):
    """Yield explicit records with their exact source line spans."""
    header = None
    offset = 0
    for raw in text.splitlines(keepends=True):
        line = raw.splitlines()[0]
        start, end = offset, offset + len(line)
        offset += len(raw)
        if line.startswith('|') and line.endswith('|'):
            cells = [item.strip() for item in re.split(r'(?<!\\)\|', line)[1:-1]]
            if {'名称', '指标', '条件', '类型', '周期'} <= set(cells) and len(cells) == len(set(cells)):
                header = cells
                continue
            if header and len(cells) == len(header) and not all(re.fullmatch(r':?-+:?', item) for item in cells):
                row = dict(zip(header, cells, strict=True))
                sid = row['指标'].strip('`')
                try:
                    operator, separator, raw = row['条件'].partition(' ')
                    if not separator or len(raw) > 4096 or operator not in {'>', '>=', '<', '<=', 'between'}:
                        raise ValueError('invalid operator')
                    value = json.loads(raw, parse_float=Decimal, parse_int=Decimal,
                                       parse_constant=lambda _: (_ for _ in ()).throw(ValueError()))
                    signature = threshold_signature({'operator': operator, 'value': value,
                        'type': row['类型'], 'horizon': row['周期']})
                except (ValueError, ArithmeticError, TypeError, RecursionError):
                    signature = None
                yield start, end, 'thresholds', sid, (signature, row.get('单位'))
            continue
        header = None
        if not line.startswith('- '):
            continue
        parts = line[2:].rsplit(' · ', 5)
        if (len(parts) != 6 or not parts[1].startswith('`') or not parts[1].endswith('`')
                or not parts[2].startswith('条件：') or not parts[3].startswith('单位：')
                or parts[5] not in {f'证伪对象：{label}' for label in TARGET_LABELS.values()}):
            continue
        sid = parts[1][1:-1]
        try:
            periods = re.fullmatch(r'连续([0-9]+)期', parts[4])
            if periods is None or not 1 <= int(periods[1]) <= 120:
                raise ValueError('invalid periods')
            side = next(key for key, label in TARGET_LABELS.items() if parts[5] == f'证伪对象：{label}')
            signature = falsifier_signature({'expression': parts[2][3:], 'unit_confirmed': parts[3][3:],
                'consecutive_periods': int(periods[1]), 'side': side})
        except (ValueError, ArithmeticError):
            signature = None
        yield start, end, 'falsifiers', sid, signature


def exported_rules(text):
    """Recognize the renderer's table/label protocol, not arbitrary language.

    An explicit record is evidence to check a model plan against; it never
    chooses an indicator, fills a rule or grants source access.
    """
    records = {'thresholds': {}, 'falsifiers': {}}
    for _, _, kind, sid, record in _exported_rule_lines(text):
        records[kind].setdefault(sid, []).append(record)
    return records


def quoted_rule_records(text, quote):
    """Read only explicit records on lines touched by this verbatim quote.

    A short array excerpt can use the complete source row and its table header.
    Repeated excerpts must resolve to identical records at every occurrence;
    None means missing or inconsistent locations, never a guessed source row.
    Empty records leave ordinary unstructured prose to the model's plan.
    """
    lines = list(_exported_rule_lines(text))
    if not quote:
        return None
    context, cursor = None, 0
    position = text.find(quote)
    while position >= 0:
        limit = position + len(quote)
        while cursor < len(lines) and lines[cursor][1] <= position:
            cursor += 1
        index, current = cursor, set()
        while index < len(lines) and lines[index][0] < limit:
            _, _, kind, sid, record = lines[index]
            current.add((kind, sid, record))
            index += 1
        current = frozenset(current)
        if context is not None and context != current:
            return None
        context = current
        position = text.find(quote, position + 1)
    return context
