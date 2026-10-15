"""Structured review locations; keep messages compatible with ValueError callers."""
from __future__ import annotations
import re

_FIELDS = {'header_rows', 'data_start_row', 'data_end_row', 'date_column', 'date_cell',
           'category_column', 'category_name', 'metric', 'unit', 'column', 'period',
           'row_label_column', 'columns', 'date_mode', 'date_format', 'header_cells', 'frequency', 'frequency_cells'}


def clean_target(target):
    if not isinstance(target, dict):
        return {}
    out = {}
    if isinstance(target.get('sheet'), str):
        out['sheet'] = target['sheet'][:200]
    index = target.get('block_index')
    if type(index) is int and index >= 0:
        out['block_index'] = index
    if isinstance(target.get('field'), str) and target['field'] in _FIELDS:
        out['field'] = target['field']
    if isinstance(target.get('column'), str) and re.fullmatch(r'[A-Z]{1,3}', target['column']):
        out['column'] = target['column']
    cells = target.get('cells', [])
    if isinstance(cells, list):
        out['cells'] = [x for x in cells[:16] if isinstance(x, str) and re.fullmatch(r'[A-Z]{1,3}[1-9][0-9]{0,6}', x)]
    return out


class ReviewValidationError(ValueError):
    def __init__(self, message, *, target=None, **location):
        super().__init__(message)
        self.review_target = clean_target({**(target if isinstance(target, dict) else {}), **location})


def error_location(exc):
    target = clean_target(getattr(exc, 'review_target', None))
    return {'review_target': target} if target else {}
