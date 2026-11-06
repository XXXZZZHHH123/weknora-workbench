"""Bounded chart reads through the same page projection as indicator history."""
from datetime import date, datetime
import math
from statistics import median

from fastapi import HTTPException
from sqlalchemy import func, select

from tin.compute.engine import day_end
from tin.config import SHANGHAI
from tin.integration.page_facts import SERIES, catalog_page, page_read_scope
from tin.models import Indicator, Observation


def cutoff(value=None):
    try:
        day = date.fromisoformat(value) if value is not None else datetime.now(SHANGHAI).date()
        if value is not None and day.isoformat() != value:
            raise ValueError
        return day
    except (TypeError, ValueError):
        raise HTTPException(400, '日期格式应为YYYY-MM-DD') from None


def catalog_fields(scope, identifiers):
    """Only requested real IDs; never infer an ID from a descriptor or label."""
    identifiers = list(dict.fromkeys(identifiers))
    if any(not isinstance(sid, str) or not SERIES.fullmatch(sid) for sid in identifiers):
        raise HTTPException(422, '图表需要当前来源目录的真实指标ID')
    fields, version = {}, None
    for offset in range(0, len(identifiers), 200):
        batch = identifiers[offset:offset + 200]
        page = catalog_page(scope, series_ids=batch, limit=200, version=version)
        if version is None:
            version = page['version']
        if page['version'] != version:
            raise HTTPException(409, '图表目录在读取期间已变化，请刷新')
        if (page.get('next_offset') is not None or page.get('truncated') or page['total'] != len(page['series'])
                or any(field['series_id'] not in batch or field['series_id'] in fields for field in page['series'])):
            raise HTTPException(503, '图表目录核验不完整')
        fields.update((field['series_id'], field) for field in page['series'])
    return fields


def curve_series_ids(curve):
    """The explicit operands are the only bindings of a computed curve."""
    return [curve.series_id] if curve.series_id is not None else list(dict.fromkeys([curve.derived.a, curve.derived.b]))


def definition_series_ids(definition):
    return list(dict.fromkeys(sid for curve in definition.curves for sid in curve_series_ids(curve)))


def curve_unit(curve, fields, *, reject=False):
    """Resolve units from current catalog metadata, never from names or labels.

    Read-only callers may inspect a broken saved definition. Validation callers
    opt into errors, so a changed operand unit cannot produce a false spread.
    """
    if any(sid not in fields for sid in curve_series_ids(curve)):
        return ''
    if curve.derived is None:
        return (fields[curve.series_id].get('unit') or '').strip()
    formula = curve.derived
    a, b = [(fields[sid].get('unit') or '').strip() for sid in (formula.a, formula.b)]
    error = None
    if formula.op == 'div':
        return '倍'
    if a and b and a != b:
        error = '相减的两个指标单位必须一致；程序不会换算单位'
    elif (not a or not b) and not formula.unit_confirmed:
        error = '相减指标单位缺失，请研究员核对后明确确认单位一致'
    if error:
        if reject:
            raise HTTPException(422, error)
        return ''
    return a or b


def unit_notices(definition, fields, *, reject=False):
    units, notices, unit_axes = {}, [], {}
    for curve in definition.curves:
        missing = [sid for sid in curve_series_ids(curve) if sid not in fields]
        if missing:
            notices.append({'code': 'missing', 'series_id': missing[0], 'label': curve.label})
            continue
        try:
            unit = curve_unit(curve, fields, reject=True)
        except HTTPException as exc:
            if reject:
                raise
            notices.append({'code': 'derived_unit_invalid', 'label': curve.label, 'message': exc.detail})
            continue
        if not unit:
            notices.append({'code': 'unit_missing', 'series_id': curve.series_id, 'label': curve.label})
            continue
        if curve.derived and curve.derived.op == 'sub' and any(
                not (fields[sid].get('unit') or '').strip() for sid in curve_series_ids(curve)):
            notices.append({'code': 'unit_confirmed', 'label': curve.label})
        if definition.time.transform == 'rebase':
            unit = '起点=100'
        units.setdefault(curve.axis, set()).add(unit)
        unit_axes.setdefault(unit, set()).add(curve.axis)
    if len(set().union(*units.values())) > 2:
        if reject:
            raise HTTPException(422, '超过两种单位，请改用change或rebase变换；change保留原单位仍需符合双轴规则，rebase起点=100；不会自动换算单位')
        notices.append({'code': 'too_many_units'})
    for axis, values in units.items():
        if len(values) > 1:
            if reject:
                raise HTTPException(422, f'{axis}轴的曲线单位不一致，请分别使用左右轴；不会自动换算单位')
            notices.append({'code': 'axis_unit_mismatch', 'axis': axis, 'units': sorted(values)})
    for unit, axes in unit_axes.items():
        if len(axes) > 1:
            if reject:
                raise HTTPException(422, '相同单位的曲线请使用同一根纵轴')
            notices.append({'code': 'unit_axis_split', 'unit': unit})
    return notices


def validate_definition(scope, definition):
    identifiers = definition_series_ids(definition)
    fields = catalog_fields(scope, identifiers)
    if any(sid not in fields for sid in identifiers):
        raise HTTPException(422, '曲线指标不存在于当前研究台来源目录，请重新选择真实指标')
    return unit_notices(definition, fields, reject=True)


def resolve_curves(definition, fields, data):
    """Evaluate formulas on the intersection of observed dates, without filling.

    The result is ephemeral. Only ``derived`` is stored in a chart definition.
    Zero denominators and non-finite arithmetic remain explicit missing points.
    """
    result = []
    for curve in definition.curves:
        ids = curve_series_ids(curve)
        sources = [data.get(sid) for sid in ids]
        item = {**curve.model_dump(mode='json'), 'unit': curve_unit(curve, fields),
                'status': 'linked', 'points': [], 'freq_days': None,
                'limited': any(source and source.get('limited') for source in sources), 'warnings': []}
        if any(sid not in fields or data.get(sid) is None for sid in ids):
            item['status'] = 'missing'
            result.append(item)
            continue
        try:
            curve_unit(curve, fields, reject=True)
        except HTTPException as exc:
            item.update(status='invalid', warnings=[{'code': 'derived_unit_invalid', 'message': exc.detail}])
            result.append(item)
            continue
        if curve.derived is None:
            item.update({key: data[curve.series_id].get(key) for key in
                ('points', 'freq_days', 'limited', 'point_limit', 'source_version')})
        else:
            formula = curve.derived
            observations = []
            for sid in (formula.a, formula.b):
                observations.append({point['date']: point['value'] for point in data[sid]['points']
                    if type(point['value']) in {int, float} and math.isfinite(point['value'])})
            for stamp in sorted(observations[0].keys() & observations[1].keys()):
                a, b = observations[0][stamp], observations[1][stamp]
                value = a - b if formula.op == 'sub' else a / b if b != 0 else None
                if value is None:
                    item['warnings'].append({'code': 'division_by_zero', 'date': stamp})
                elif not math.isfinite(value):
                    value = None
                    item['warnings'].append({'code': 'derived_non_finite', 'date': stamp})
                item['points'].append({'date': stamp, 'value': value})
            dates = [date.fromisoformat(point['date']) for point in item['points'] if point['value'] is not None]
            gaps = [(b - a).days for a, b in zip(dates, dates[1:])]
            item['freq_days'] = median(gaps) if gaps else None
            item['point_limit'] = min(source.get('point_limit', 2000) for source in sources)
            versions = {source.get('source_version') for source in sources if source.get('source_version')}
            if len(versions) > 1:
                raise HTTPException(409, '计算序列的来源在读取期间已变化，请刷新')
            item['source_version'] = next(iter(versions), None)
        result.append(item)
    return result


def read_series(identifiers, day, *, limit=2000):
    """Read only the active SessionLocal, including local pre-KB indicators.

    Rank revisions before validating values, so invalid new revisions never
    resurrect an older value. Multiple intraday times keep the latest real time.
    """
    from tin.web import app as web
    identifiers = list(dict.fromkeys(identifiers))
    if not identifiers:
        return {}
    result = {}
    for offset in range(0, len(identifiers), 256):
        batch = identifiers[offset:offset + 256]
        # Also respect the existing page's aggregate observation budget.
        batch_size = min(256, max(1, 20000 // limit))
        for start in range(0, len(batch), batch_size):
            selected = batch[start:start + batch_size]
            with page_read_scope('indicators', series_ids=selected, day=day, points_per_series=limit):
                with web.SessionLocal() as session:
                    totals = session.info.get('knowledge_facts_series_totals', {})
                    for sid in selected:
                        spec = session.get(Indicator, sid)
                        if spec is None:
                            continue
                        ranked = select(Observation.as_of, Observation.value, Observation.status,
                            func.row_number().over(partition_by=Observation.as_of,
                                order_by=(Observation.revision.desc(), Observation.id.desc())).label('rank')).where(
                            Observation.series_id == sid, Observation.as_of < day_end(day)).subquery()
                        records = session.execute(select(ranked).where(ranked.c.rank == 1)
                            .order_by(ranked.c.as_of.desc()).limit(limit)).mappings().all()
                        by_day = {}
                        for row in records:
                            stamp = row['as_of'].astimezone(SHANGHAI).date()
                            if stamp in by_day:
                                continue
                            value = row['value']
                            valid = row['status'] == '正常' and type(value) in {int, float} and math.isfinite(value)
                            by_day[stamp] = float(value) if valid else None
                        points = [{'date': stamp.isoformat(), 'value': value} for stamp, value in sorted(by_day.items())]
                        dates = [date.fromisoformat(point['date']) for point in points if point['value'] is not None]
                        gaps = [(b - a).days for a, b in zip(dates, dates[1:])]
                        total = totals.get(sid)
                        result[sid] = {'series_id': sid, 'name': spec.name, 'unit': spec.unit,
                            'points': points, 'freq_days': median(gaps) if gaps else None,
                            'limited': total > limit if type(total) is int else len(records) == limit,
                            'point_limit': limit, 'source_version': session.info.get('knowledge_facts_version')}
    return result
