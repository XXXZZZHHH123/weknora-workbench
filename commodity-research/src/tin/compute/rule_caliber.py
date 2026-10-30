"""Apply an explicit rule-local human unit only when the source has none."""
from tin.schemas.judgment import CaliberConfirmation


def unit_present(value):
    return isinstance(value, str) and value.strip() not in {'', '—', '未注明'}


def rule_unit(rule, source_unit):
    declaration = rule.get('caliber_confirmation')
    if declaration is None:
        return source_unit if unit_present(source_unit) else None
    declaration = CaliberConfirmation.model_validate(declaration)
    if declaration.series_id != rule['series_id']:
        raise ValueError('人工口径不属于此指标绑定')
    if unit_present(source_unit) and source_unit != declaration.unit:
        raise ValueError('当前来源单位与人工口径不一致，请重新核对')
    return declaration.unit


def declared_points(points, unit):
    """Read-only projection; never relabel a known source unit or scale a value."""
    result = []
    for point in points:
        get = point.get if isinstance(point, dict) else lambda name, default=None: getattr(point, name, default)
        source = get('unit') or (get('caliber_snapshot', {}) or {}).get('unit')
        result.append({'value': get('value'), 'status': get('status', '正常'),
            'unit': source if unit_present(source) else unit})
    return result
