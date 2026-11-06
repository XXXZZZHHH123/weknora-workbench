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


def unit_notices(definition, fields, *, reject=False):
    units, notices = {}, []
    for curve in definition.curves:
        field = fields.get(curve.series_id)
        if field is None:
            notices.append({'code': 'missing', 'series_id': curve.series_id, 'label': curve.label})
            continue
        unit = (field.get('unit') or '').strip()
        if not unit:
            notices.append({'code': 'unit_missing', 'series_id': curve.series_id, 'label': curve.label})
            continue
        units.setdefault(curve.axis, set()).add(unit)
    for axis, values in units.items():
        if len(values) > 1:
            if reject:
                raise HTTPException(422, f'{axis}轴的曲线单位不一致，请分别使用左右轴；不会自动换算单位')
            notices.append({'code': 'axis_unit_mismatch', 'axis': axis, 'units': sorted(values)})
    return notices


def validate_definition(scope, definition):
    fields = catalog_fields(scope, [curve.series_id for curve in definition.curves])
    if any(curve.series_id not in fields for curve in definition.curves):
        raise HTTPException(422, '曲线指标不存在于当前研究台来源目录，请重新选择真实指标')
    return unit_notices(definition, fields, reject=True)


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
