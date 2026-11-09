"""Explicit, bounded precision evidence for directly readable indicator pairs.

The metadata catalog never opens worksheet XML. This opt-in read uses the same
hard limits as original-sheet preview and retains only selected value cells.
"""
from collections import defaultdict
from decimal import Decimal, InvalidOperation
import math
import re
from zipfile import ZipFile, BadZipFile

from fastapi import HTTPException

from . import sheet_view


def display_decimals(raw, code):
    """Return a raw-value rounding scale justified by a simple Excel display.

Percent display shifts the scale by two, since the observations retain the
unscaled numeric value. Unsupported formats remain explicitly unverifiable.
"""
    try:
        number = Decimal(str(raw).strip())
        if not number.is_finite():
            return None
    except (InvalidOperation, ValueError, TypeError):
        return None
    if code == 'General':
        places = max(0, -number.as_tuple().exponent)
    else:
        if not isinstance(code, str):
            return None
        match = re.fullmatch(r'(?:#,##)?0(?:\.(0{0,15}#{0,15}))?(%)?', code)
        if match is None:
            return None
        fraction = match[1] or ''
        minimum, maximum = fraction.count('0'), len(fraction)
        shift = 2 if match[2] else 0
        visible = number * (100 if shift else 1)
        needed = max(0, -visible.normalize().as_tuple().exponent)
        places = max(minimum, min(maximum, needed)) + shift
    return places if 0 <= places <= 15 else None


def _selected_formats(path, wanted):
    limits = sheet_view.SheetViewLimits(output_cells=1000)
    sheet_view._preflight(path, limits)
    result = {}
    with ZipFile(path) as package:
        infos = package.infolist()
        if len(infos) > limits.parts or len({part.filename for part in infos}) != len(infos):
            raise ValueError('Excel 压缩包目录无效')
        for part in infos:
            sheet_view._part_name(part.filename.rstrip('/') if part.is_dir() else part.filename)
        reader = sheet_view._Reader(package, limits)
        for name, cells in wanted.items():
            workbook = sheet_view._workbook(reader, name)
            anchors = {sheet_view._coordinate(cell) for cell in cells}
            selected = sheet_view._Sheet({'start_row':0,'end_row':0,'start_col':0,'end_col':0},
                limits, anchors=anchors, metadata=False)
            reader.parse(workbook['part'], limits.sheet_xml_bytes, selected)
            styles = sheet_view._styles(reader, workbook['styles'],
                {cell['style_id'] for cell in selected.cells} or {0}, workbook['theme'])
            for cell in selected.cells:
                result[(name, cell['cell'])] = styles[str(cell['style_id'])]['number_format'].get('code')
    return result


def precision_rows(store, info, mapping, records):
    wanted = defaultdict(set)
    sheets = info.get('_series_sheets', {})
    for source, _, _, _, _, raw_low, raw_high, cell_low, cell_high in records:
        if raw_low == raw_high and cell_low == cell_high and isinstance(cell_low, str) and sheets.get(source):
            wanted[sheets[source]].add(cell_low)
    try:
        formats = _selected_formats(store._path(info['dataset_id']) / 'source.xlsx', wanted) if wanted else {}
    except (ValueError, OSError, KeyError, BadZipFile):
        raise HTTPException(409, '原表显示精度无法在读取上限内核验，请核对来源后重试') from None
    rows = []
    for source, when, low, high, finite, raw_low, raw_high, cell_low, cell_high in records:
        if finite is not True or not math.isfinite(low) or not math.isfinite(high):
            raise HTTPException(503, '页面指标包含非有限数值')
        if low != high:
            raise HTTPException(409, '同一来源指标日期存在冲突数值，请在知识库核对')
        key = (sheets.get(source), cell_low)
        unambiguous = raw_low == raw_high and cell_low == cell_high and key in formats
        places = display_decimals(raw_low, formats[key]) if unambiguous else None
        rows.append({'series_id':mapping[source], 'date':when, 'value':low, 'value_status':'valid',
            # Unknown formatting must not fall back to an invented precision.
            'raw_value':raw_low if places is not None else None, 'display_decimals':places})
    return rows
