"""Check the series identities in model-authored indicator suggestions."""
from fastapi import HTTPException


def filter_series_suggestions(rows, catalog, invalid_detail, *, allowed_for=None):
    kept, ignored = [], []
    for row in rows:
        if not isinstance(row, dict):
            raise HTTPException(422, invalid_detail)
        identifier = row.get('series_id')
        if (isinstance(identifier, str) and identifier in catalog
                and (allowed_for is None or identifier in allowed_for(row))):
            kept.append(row)
        else:
            ignored.append(row)
    if ignored and not kept:
        raise HTTPException(422, invalid_detail)
    return kept, ignored


def ignored_series_summary(count):
    return {'ignored_count': count,
            'ignored_reason': f'模型返回 {count} 个不在候选目录的指标，已忽略' if count else ''}
