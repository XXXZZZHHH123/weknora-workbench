"""Structural native-chart conversion: identifiers, real dates, kinds and axes."""
from calendar import monthrange
from collections import Counter
from datetime import date, timedelta
from statistics import median

from pydantic import ValidationError

from tin.compute.cycles import repeat_bounds
from tin.schemas.chart import ChartDefinition

CHART_KINDS = {'lineChart': 'line', 'barChart': 'bar', 'areaChart': 'area'}


def propose_chart(native, fields, latest_dates=None, *, start_override=None):
    """Return a reviewable suggestion. Labels are copied, never interpreted."""
    source = native.model_dump(mode='json') if hasattr(native, 'model_dump') else native
    missing, available = [], {}
    for index, curve in enumerate(source['curves']):
        reason = None
        if curve['status'] != 'linked' or not curve.get('series_id'):
            reason = curve.get('reason') or curve['status']
        elif curve['series_id'] not in fields:
            reason = '指标不存在于当前来源目录'
        elif curve['chart_type'] not in CHART_KINDS:
            reason = '当前图型不支持转换'
        elif not curve.get('first_date') or not curve.get('last_date'):
            reason = '缺少真实日期区间'
        else:
            try:
                first, last = date.fromisoformat(curve['first_date']), date.fromisoformat(curve['last_date'])
                if first > last:
                    raise ValueError
            except (ValueError, TypeError):
                reason = '日期区间无效'
        if reason:
            missing.append({'index': index, 'label': curve['label'], 'reason': reason})
        else:
            available[index] = curve
    result = {key: source[key] for key in ('chart_key', 'knowledge_id', 'source_file', 'sheet', 'title')}
    result.update(convertible=False, mode=None, missing_curves=missing, warnings=[], definition=None,
                  source_curves=source['curves'], axes=source.get('axes', []))
    if not available:
        result['reason'] = '没有可用曲线'
        return result
    groups = {}
    for curve in available.values():
        groups.setdefault(curve['series_id'], []).append(curve)
    # Dict order and max's first tie preserve the group's first chart appearance.
    dominant = max(groups.values(), key=len)
    cyclic = len(dominant) >= 3 and len({curve['first_date'] for curve in dominant}) == len(dominant)
    if cyclic:
        missing.extend({'index': index, 'label': curve['label'], 'reason': '周期图一期只保留一个指标'}
                       for index, curve in available.items() if curve['series_id'] != dominant[0]['series_id'])
        missing.sort(key=lambda item: item['index'])
    usable = dominant if cyclic else list(available.values())
    title = source['title'][:120] or '未命名图表'
    intervals = sorted(usable, key=lambda curve: curve['first_date'])
    note = ('缺失曲线：' + '；'.join(f"{item['label']}（{item['reason']}）" for item in missing)) if missing else ''
    curves = []
    for curve in usable:
        descriptor, style = curve.get('descriptor') or {}, curve.get('style') or {}
        metric = fields[curve['series_id']].get('metric') or title
        curves.append({'series_id': curve['series_id'], 'label': metric if cyclic else curve['label'] or metric,
            'kind': CHART_KINDS[curve['chart_type']], 'axis': 'right' if curve.get('axis') == 'secondary' else 'left',
            'style': {'color': style.get('color'), 'width': style.get('width_pt'), 'dash': style.get('dash')},
            'descriptor': {'source_file': source['source_file'], 'sheet': descriptor.get('sheet') or source['sheet'],
                           'metric': descriptor.get('metric') or '', 'column': descriptor.get('column') or ''}})
    if cyclic:
        gaps = [(date.fromisoformat(right['first_date']) - date.fromisoformat(left['first_date'])).days
                for left, right in zip(intervals, intervals[1:])]
        if 330 <= median(gaps) <= 400 and all(300 <= gap <= 430 for gap in gaps):
            months = Counter(curve['first_date'][5:7] for curve in intervals)
            start = start_override or months.most_common(1)[0][0] + '-01'
            template = {'kind': 'repeat', 'name': '原表周期', 'start': start, 'period': 'year'}
        else:
            ends = {curve['first_date']: min(date.fromisoformat(curve['last_date']),
                date.fromisoformat(intervals[index + 1]['first_date']) - timedelta(days=1))
                if index + 1 < len(intervals) else date.fromisoformat(curve['last_date'])
                for index, curve in enumerate(intervals)}
            template = {'kind': 'segments', 'name': '原表分段', 'segments': [
                {'label': c['label'], 'start': c['first_date'], 'end': ends[c['first_date']].isoformat()} for c in usable]}
            if start_override is not None:
                result['warnings'].append({'code': 'start_override_not_applicable', 'message': '手工分段保持原始日期，不套用年度起点'})
        time = {'mode': 'cycle', 'template': template, 'cycles': len(usable), 'band_cycles': 5, 'transform': 'raw',
                'segment_styles': [{'label': original['label'], **converted['style']}
                                   for original, converted in zip(usable, curves)]}
        curves = curves[:1]
        result['mode'] = 'cycle'
        latest = (latest_dates or {}).get(curves[0]['series_id'])
        if latest and template['kind'] == 'repeat':
            latest = date.fromisoformat(latest) if isinstance(latest, str) else latest
            latest_start, _ = repeat_bounds(template, latest)
            last = date.fromisoformat(intervals[-1]['first_date'])
            threshold = last.replace(year=last.year + 1, day=min(last.day, monthrange(last.year + 1, last.month)[1]))
            if latest >= threshold:
                label = str(latest_start.year) if template['start'] == '01-01' else f'{latest_start.year % 100:02d}/{(latest_start.year + 1) % 100:02d}'
                result['warnings'].append({'code': 'latest_cycle_missing', 'label': label,
                    'message': f'原图未包含 {label}，转换后会显示'})
    else:
        result['mode'] = 'trend'
        time = {'mode': 'since', 'start': min(curve['first_date'] for curve in usable)}
    definition = {'format': 'chart.v1', 'title': title,
                  'group': source['sheet'][:80], 'note': note[:600], 'curves': curves, 'time': time,
                  'origin': {'kind': 'native', 'ref': source['chart_key']}}
    try:
        definition = ChartDefinition.model_validate(definition)
    except ValidationError as exc:
        result['reason'] = '图表定义无法保存：' + '; '.join(error['msg'] for error in exc.errors())
        return result
    units = {}
    for curve in definition.curves:
        unit = (fields[curve.series_id].get('unit') or '').strip()
        if unit:
            units.setdefault(curve.axis, set()).add(unit)
        else:
            result['warnings'].append({'code': 'unit_missing', 'series_id': curve.series_id, 'label': curve.label})
    if any(len(values) > 1 for values in units.values()):
        result['reason'] = '同一纵轴单位不一致，需先核对左右轴'
        return result
    result.update(convertible=True, definition=definition.model_dump(mode='json'))
    return result


def group_suggestions(suggestions):
    groups = {}
    for item in suggestions:
        key = (item['knowledge_id'], item['sheet'])
        group = groups.setdefault(key, {'knowledge_id': key[0], 'source_file': item['source_file'], 'sheet': key[1],
                                       'charts': [], 'total': 0, 'cycles': 0, 'trends': 0, 'unconvertible': 0})
        group['charts'].append(item)
        group['total'] += 1
        group['unconvertible' if not item['convertible'] else 'cycles' if item['mode'] == 'cycle' else 'trends'] += 1
    for group in groups.values():
        periods = Counter()
        for item in group['charts']:
            if item['convertible'] and item['mode'] == 'cycle':
                time = item['definition']['time']
                start = time['template'].get('start') or '原表分段'
                periods[start] += 1
        details = '、'.join(f'{"原表分段" if start == "原表分段" else "起点 " + start} {count} 张'
                           for start, count in periods.items())
        group['summary'] = (f"本表 {group['total']} 张：{group['cycles']} 张周期图" + (f'（{details}）' if details else '')
                            + f"，{group['trends']} 张走势图，{group['unconvertible']} 张无法转换")
    return list(groups.values())
