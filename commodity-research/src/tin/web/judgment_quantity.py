"""Compare rule literals with authorized observations during human review."""
from datetime import date, datetime, timedelta
from decimal import Decimal
from typing import Literal

from fastapi import HTTPException
from pydantic import BaseModel, ConfigDict, Field

from tin.config import SHANGHAI
from tin.judgments.numbers import check_rule_number, exact_float
from tin.judgments.quantity_alignment import (MAX_SCALE_EXPONENT, MIN_SCALE_EXPONENT,
    decimals, magnitude_warning, quantity_text, scaled_values)
from tin.schemas.judgment import EXPRESSION


# One point per date. The bound covers three calendar years, including leap days.
HISTORY_POINTS = 1100
LEGACY_HISTORY_PAGE = 121
RULE_KINDS = {'thresholds', 'falsifiers'}


class QuantityAlignment(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    mode: Literal['convert', 'keep']
    exponent: int | None = Field(default=None, ge=MIN_SCALE_EXPONENT, le=MAX_SCALE_EXPONENT)
    reason: str | None = Field(default=None, max_length=1000)


def source_value(item):
    value = item['value']
    if not isinstance(value, dict):
        return None
    if item['kind'] == 'thresholds':
        target = value.get('value')
        try:
            if isinstance(target, list) and target:
                return [check_rule_number(number) for number in target]
            return check_rule_number(target)
        except ValueError:
            return None
    if item['kind'] == 'falsifiers':
        expression = value.get('expression')
        match = EXPRESSION.fullmatch(expression.strip()) if isinstance(expression, str) else None
        try:
            return exact_float(match.group(3)) if match else None
        except ValueError:
            return None
    return None


def _start_date(day):
    try:
        return day.replace(year=day.year - 3)
    except ValueError:
        return day.replace(year=day.year - 3, day=28)


def legacy_histories(scope, identifiers, catalog, day, start):
    from tin.web.judgment_radar import legacy_series

    values = legacy_series(scope, identifiers, day, catalog, LEGACY_HISTORY_PAGE, include_invalid=True)
    result = {}
    for sid, entry in values.items():
        points = list(entry['points'])
        page = points
        while len(page) == LEGACY_HISTORY_PAGE:
            oldest = min(point['date'] for point in page)
            if oldest <= start:
                break
            cutoff = date.fromisoformat(oldest) - timedelta(days=1)
            page = legacy_series(scope, [sid], cutoff, catalog, LEGACY_HISTORY_PAGE,
                                 include_invalid=True)[sid]['points']
            points.extend(page)
        result[sid] = [point for point in points if point['value'] is not None]
    without_value = [sid for sid, points in result.items() if not points and values[sid]['points']]
    if without_value:
        # The latest valid sample can precede a long run of missing rows.
        latest = legacy_series(scope, without_value, day, catalog, 1)
        result.update((sid, entry['points']) for sid, entry in latest.items())
    return result


def read_histories(scope, identifiers, catalog):
    identifiers = list(dict.fromkeys(identifiers))
    if not identifiers:
        return {}
    from tin.integration.page_facts import bounded_facts

    day = datetime.now(SHANGHAI).date()
    start, end = _start_date(day).isoformat(), day.isoformat()
    result = {}
    batch_size = 20000 // HISTORY_POINTS
    for index in range(0, len(identifiers), batch_size):
        batch = identifiers[index:index + batch_size]
        if scope.scope_version == 2:
            # These IDs are from the authorized preview directory. Bound the
            # read again using the current direct source authorization.
            rows = bounded_facts(scope, batch, 'indicators', day=end, points=HISTORY_POINTS,
                                 direct_only=True)['rows']
            series = {identifier: [] for identifier in batch}
            for row in rows:
                series[row['series_id']].append(row)
        else:
            series = legacy_histories(scope, batch, catalog, day, start)
        for sid, points in series.items():
            latest = max(points, key=lambda point: point['date'], default=None)
            values = [Decimal(str(point['value'])) for point in points if point['date'] >= start]
            result[sid] = {
                'latest': {'date': latest['date'], 'value': str(latest['value'])} if latest else None,
                'range': {'min': str(min(values)), 'max': str(max(values))} if values else None,
            }
    return result


def diagnostic(item, history, exponent=None):
    source = source_value(item)
    result = {**history, 'warning': magnitude_warning(source, history['range']),
        'source_value': source_literals(item)}
    confirmation = item.get('quantity_confirmation') or {}
    if confirmation.get('series_id') == history.get('series_id'):
        result['alignment'] = confirmation['alignment']
    if exponent is not None and source is not None:
        converted = scaled_values(source, Decimal(10) ** exponent)
        converted_value = _formatted_values(converted, source)
        interval = history['range']
        overlaps = (max(converted) >= Decimal(interval['min'])
            and min(converted) <= Decimal(interval['max'])) if interval is not None else None
        result.update(exponent=exponent, converted_value=converted_value,
            converted_text=quantity_text(converted_value), converted_overlaps=overlaps,
            converted_warning=magnitude_warning(converted_value, interval))
    return result


def _formatted_values(values, original):
    literals = [format(number, 'f') for number in values]
    return literals if isinstance(original, list) else literals[0]


def source_literals(item):
    source = source_value(item)
    return _formatted_values(decimals(source), source) if source is not None else None


def confirmed_rule(item, rule):
    confirmation = item.get('quantity_confirmation')
    if confirmation and confirmation['alignment']['mode'] == 'convert':
        rule.update(confirmation['execution'])
    return rule


def alignment_error(message):
    raise HTTPException(422, {'code': 'quantity_alignment', 'message': message,
        'attention_fields': ['condition']})


def confirm_quantity(item, choice, history):
    warning = magnitude_warning(source_value(item), history['range'])
    if choice is None:
        if warning and not item.get('quantity_confirmation'):
            alignment_error(warning + '。请选择换算或填写仍按原值的原因。')
        return
    if not warning and not item.get('quantity_confirmation'):
        alignment_error('当前规则没有需要确认的数量级差异。')
    if choice.mode == 'convert' and (choice.exponent is None or choice.reason is not None):
        alignment_error('请选择换算倍率')
    if choice.mode == 'keep' and (choice.exponent is not None or not (choice.reason or '').strip()):
        alignment_error('数值无误，仍按原值时必须填写原因')
    confirmation = {'series_id': item['series_id'], 'source_quote': item['source_quote'],
        'range': history['range'], 'alignment': choice.model_dump(exclude_none=True)}
    if choice.mode == 'keep':
        confirmation['alignment']['reason'] = choice.reason.strip()
    else:
        source = source_value(item)
        if source is None:
            alignment_error('此规则没有可换算的原文数字。')
        scale = Decimal(10) ** choice.exponent
        literals = [str(value) for value in scaled_values(source, scale)]
        try:
            values = [exact_float(literal) for literal in literals]
        except ValueError as error:
            alignment_error(str(error))
        execution = {'source_value': source, 'scale': float(scale),
            'value': values if isinstance(source, list) else values[0]}
        if item['kind'] == 'falsifiers':
            match = EXPRESSION.fullmatch(item['value']['expression'].strip())
            execution['expression'] = f'{match.group(1)} {match.group(2)} {literals[0]}'
        confirmation['execution'] = execution
    if confirmation != item.get('quantity_confirmation'):
        item.pop('number_confirmation', None)
    item['quantity_confirmation'] = confirmation


def pending_alignment(scope, catalog, items):
    selected = [item for item in items if item['kind'] in RULE_KINDS and item['series_id']
        and item['series_id'] in catalog and not item.get('quantity_confirmation')
        and source_value(item) is not None]
    histories = read_histories(scope, [item['series_id'] for item in selected], list(catalog.values()))
    result = {}
    for item in selected:
        history = histories.get(item['series_id'], {})
        warning = magnitude_warning(source_value(item), history.get('range'))
        if warning:
            result[item['id']] = warning
    return result
