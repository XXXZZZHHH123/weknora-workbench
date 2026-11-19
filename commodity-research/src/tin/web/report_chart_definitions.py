"""Turn confirmed, evidence-grounded report structures into ordinary charts.

Labels never select an indicator or a time mode. The recognition model
proposes a type and evidence-grounded shape; the researcher can change the
type. Explicit candidate IDs and current catalog metadata resolve bindings.
"""
from copy import deepcopy
from decimal import Decimal
from math import isfinite

from fastapi import HTTPException
from pydantic import ValidationError

from tin.compute.cycles import BUILTIN_TEMPLATES
from tin.integration.workbench_scope import artifact_session
from tin.schemas.chart import MAX_VISIBLE_CYCLES, ChartDefinition
from tin.web.chart_data import catalog_fields, cutoff, definition_series_ids, validate_definition
from tin.web.charts import artifact_query, decode_definition, definition_data
from tin.web.report_chart_suggestions import tick_number

CHART_TYPES = frozenset(('trend', 'seasonal', 'comparison'))
RANGE_SPAN_FACTOR = 5


def frequency_template(fields):
    """Choose the existing calendar template for year-separated comparisons.

    Daily, weekly, monthly, quarterly and annual observations all retain their
    actual dates in the natural-year template; unknown frequencies do too.
    The only other built-in is a Spring Festival event window. Frequency alone
    cannot establish that event meaning, so it must never select that template.
    No sampling, interpolation or unit conversion is introduced here.
    """
    return deepcopy(BUILTIN_TEMPLATES['自然年'])


def mapping_warnings(mappings):
    """Describe duplicate bindings without inferring anything from their names."""
    selected = [identifier for identifier in mappings if isinstance(identifier, str) and identifier]
    if len(selected) == len(set(selected)):
        return []
    return [f'重复映射已合并：{len(selected)}条原文系列映射到'
            f'{len(set(selected))}个指标，定义已去重；请核对指标口径']


def shape_warnings(chart, chart_type=None):
    if ((chart_type or chart.get('chart_type')) == 'seasonal'
            and chart.get('shape', {}).get('period_kind') == 'contract'):
        return ['原图按合约分年（如 1905），重画按年度对齐，曲线分段可能不同']
    return []


def _seasonal_time(chart, fields):
    template = frequency_template(fields)
    time = {'mode': 'cycle', 'template': template, 'cycles': 8, 'transform': 'raw'}
    shape = chart.get('shape', {})
    month = shape.get('season_start_month')
    if month is not None and month != 1 and shape.get('period_kind') != 'contract':
        template.update(start=f'{month:02d}-01', name=f'{month}月起始年度')
    labels = shape.get('period_labels')
    if labels:
        count = min(len(labels), MAX_VISIBLE_CYCLES)
        time.update(cycles=count, visible_cycles=count)
    return time


def build_definition(scope, chart, mappings, chart_type, group, origin_ref):
    if chart_type not in CHART_TYPES:
        raise HTTPException(422, '图型只能选择走势、同期对比或多指标对比')
    legends = chart.get('series')
    if (not isinstance(legends, list) or not legends or not isinstance(mappings, list)
            or len(mappings) != len(legends)):
        raise HTTPException(422, '请为每条原文图例确认一个候选指标')
    for legend, identifier in zip(legends, mappings):
        if identifier is None or identifier == '':
            raise HTTPException(422, '指标库中未找到，请手动选择或跳过')
        shown = {row.get('id') for row in legend.get('candidates', []) if isinstance(row, dict)}
        if not isinstance(identifier, str) or not identifier or identifier not in shown:
            raise HTTPException(422, '只能选择该图例本次展示过的候选指标，请重新确认映射')

    fields = catalog_fields(scope, mappings)
    if any(identifier not in fields for identifier in mappings):
        raise HTTPException(422, '映射指标已不在当前研究台来源目录，请重新识别')
    units, curves, seen = [], [], set()
    for legend, identifier in zip(legends, mappings):
        # Every legend is independently grounded and authorized above. Once
        # confirmed, repeated real bindings must not draw identical curves.
        if identifier in seen:
            continue
        seen.add(identifier)
        field = fields[identifier]
        unit = (field.get('unit') or '').strip()
        if unit and unit not in units:
            units.append(unit)
        if len(units) > 2:
            raise HTTPException(422, '候选图超过两种真实单位，请调整映射；不会换算单位')
        curves.append({'series_id': identifier, 'label': legend.get('name', ''),
            'kind': 'line', 'axis': 'right' if unit and units.index(unit) == 1 else 'left',
            'descriptor': {key: field.get(key) or '' for key in ('source_file', 'sheet', 'metric', 'column')}})
    time = (_seasonal_time(chart, list(fields.values())) if chart_type == 'seasonal'
            else {'mode': 'recent', 'points': 120, 'transform': 'raw'})
    try:
        definition = ChartDefinition.model_validate({'title': chart.get('title'), 'group': group,
            'note': '', 'curves': curves, 'time': time, 'origin': {'kind': 'ppt', 'ref': origin_ref}})
    except ValidationError as error:
        details = '；'.join(f"{'.'.join(str(part) for part in row['loc'])}: {row['msg']}"
                           for row in error.errors(include_url=False, include_input=False))
        raise HTTPException(422, '图表定义校验失败：' + details) from None
    validate_definition(scope, definition)
    return definition


def _redraw_points(definition, payload, *, axis=None):
    if definition.time.mode != 'cycle':
        for curve in payload['curves']:
            if axis is None or curve['axis'] == axis:
                yield from curve['points']
    elif len(definition.curves) == 1:
        for segment in payload['segments']:
            yield from segment['points']
    else:
        # Multi-curve seasonal imports show only each curve's common current
        # period. Historical segments are kept in the payload for statistics.
        for curve in payload['curves']:
            if axis is None or curve['axis'] == axis:
                for segment in curve['cycle']['segments']:
                    if segment['current']:
                        yield from segment['points']


def _redraw_values(definition, payload, *, axis=None):
    for point in _redraw_points(definition, payload, axis=axis):
        value = point['value']
        if type(value) in (float, int) and isfinite(value):
            yield value


def has_observations(definition, payload):
    return next(_redraw_values(definition, payload), None) is not None


def range_warning(scope, chart, definition, *, payload=None):
    ticks = chart.get('shape', {}).get('y_ticks')
    if not ticks:
        return None
    lower, upper = (tick_number(ticks[key]) for key in ('min', 'max'))
    if payload is None:
        payload = definition_data(scope, definition, cutoff(), details=False)
    values = list(_redraw_values(definition, payload, axis='left'))
    if not values:
        return None
    minimum, maximum = min(values), max(values)
    original_span = Decimal(str(upper)) - Decimal(str(lower))
    current_span = Decimal(str(maximum)) - Decimal(str(minimum))
    if (maximum < lower or minimum > upper or current_span > original_span * RANGE_SPAN_FACTOR
            or current_span * RANGE_SPAN_FACTOR < original_span):
        return (f"数值范围与原图刻度不符（原图 {ticks['min']}–{ticks['max']}，"
                f'当前数据 {minimum:.12g}–{maximum:.12g}），请核对指标口径或单位')
    return None


def duplicate_signature(definition):
    """Compare real bindings and time mode; titles and old report text do not decide."""
    return frozenset(definition_series_ids(definition)), ('cycle' if definition.time.mode == 'cycle' else 'trend')


def duplicate_ids(scope, definition):
    signature = duplicate_signature(definition)
    with artifact_session(scope) as session:
        return [row.id for row in session.scalars(artifact_query())
                if duplicate_signature(decode_definition(row)) == signature]
