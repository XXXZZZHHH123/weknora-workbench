"""Source-backed layout corrections used before assisted confirmation."""
from __future__ import annotations

from copy import deepcopy
from hashlib import file_digest
from math import isfinite
import re
from xml.etree import ElementTree
from zipfile import BadZipFile

from openpyxl.utils import column_index_from_string as colno, get_column_letter, range_boundaries

from .generic_parser import _mapped_readers
from .native_charts import ChartIndexBuildPending, ChartReadBudgetExceeded, extract_parts, index, reference
from .scanner import _UNITS, complete_date


MAX_CELLS = 500_000
MAX_ASSIST_BLOCKS = 128


def _verified_source(manager, doc):
    path = manager.source_path(doc)
    with path.open('rb') as source:
        if file_digest(source, 'sha256').hexdigest() != doc['sha256']:
            raise ValueError('原文件已改变，请重新扫描后再核对。')
    return path


def _charts(path, sheet):
    inventory = sheet.get('charts') or {}
    parts = inventory.get('chart_parts') or []
    if not parts:
        return [], False
    try:
        charts, problems = extract_parts(path, parts)
    except (ChartReadBudgetExceeded, BadZipFile, ElementTree.ParseError, KeyError, ValueError):
        return [], False
    local = [chart for chart in charts if chart['sheet'] == sheet['name']]
    return local, not problems and len(local) == inventory.get('chart_count')


def _chart_sources(manager, doc, sheet, charts, path):
    if (sheet.get('charts') or {}).get('image_count') or not charts:
        return []
    refs = [curve['reference'] for chart in charts for curve in chart['curves']]
    if any(not chart['curves'] for chart in charts) or any(
            not ref or ref['sheet'] == sheet['name'] for ref in refs):
        return []
    actual = {item['name']: item for item in manager.publication_view(doc)['sheets']
              if item.get('published_in_dataset') and item['name'] != sheet['name']}
    if any(ref['sheet'] not in actual or colno(ref['column']) > actual[ref['sheet']]['columns']
           or ref['start_row'] > actual[ref['sheet']]['rows'] for ref in refs):
        return []
    try:
        linked = {chart['part']: chart['curves'] for chart in index(manager.store, doc['dataset_id'])['charts']
                  if chart['sheet'] == sheet['name']}
    except (ChartIndexBuildPending, OSError, ValueError):
        return []
    ranges = {}
    for chart in charts:
        indexed = linked.get(chart['part'], [])
        if len(indexed) != len(chart['curves']) or any(
                known['status'] != 'linked' or known['reference'] != curve['reference']
                for known, curve in zip(indexed, chart['curves'])):
            return []
        for known in indexed:
            ref = known['reference']
            ranges.setdefault(ref['sheet'], set()).add(
                (colno(ref['column']), ref['start_row'], min(ref['end_row'], actual[ref['sheet']]['rows']),
                 known['valid_count']))
    if sum(max(r[0] for r in specs) * max(r[2] for r in specs) for specs in ranges.values()) > MAX_CELLS:
        return []
    # A curve linked to some observations can still omit genuine source values.
    with _mapped_readers(path, doc['sha256']) as (values, _):
        for name, specs in ranges.items():
            right = max(r[0] for r in specs)
            top, bottom = min(r[1] for r in specs), max(r[2] for r in specs)
            rows = list(values[name].iter_rows(min_row=top, max_row=bottom, max_col=right))
            if any(sum(_numeric(row[col - 1]) for row in rows[start - top:end - top + 1]) != count
                   for col, start, end, count in specs):
                return []
    return list(dict.fromkeys(ref['sheet'] for ref in refs))


def _chart_regions(charts, name):
    """Return chart-proven side ranges with their own same-sheet category axis."""
    for chart in charts:
        groups = {}
        for curve in chart['curves']:
            axis, value = reference(curve['category_ref']), curve['reference']
            if (not axis or not value or axis['sheet'] != name or value['sheet'] != name
                    or axis['column'] == value['column']
                    or (axis['start_row'], axis['end_row']) != (value['start_row'], value['end_row'])):
                continue
            key = (colno(axis['column']), axis['start_row'], axis['end_row'])
            groups.setdefault(key, set()).add(colno(value['column']))
        for (axis, start, end), columns in groups.items():
            positions = sorted({axis, *columns})
            if end > start and positions == list(range(positions[0], positions[-1] + 1)):
                yield axis, start, end, positions


def _text(value):
    return value.strip() if isinstance(value, str) else ''


def _title(value):
    text = _text(value)
    return text if text and text not in _UNITS and complete_date(text) is None else ''


def _header(rows, merges, column, header_rows):
    refs, names, units = [], [], {}
    for row in header_rows:
        source_row, source_col = next(((top, left) for left, top, right, bottom in merges
                                      if left <= column <= right and top <= row <= bottom), (row, column))
        if source_row not in header_rows:
            continue
        cell = rows[source_row - 1][source_col - 1]
        value = cell.value
        if value is None or cell.data_type == 'e':
            continue
        ref = f'{get_column_letter(source_col)}{source_row}'
        refs.append(ref)
        if label := _title(value):
            names.append(label)
        for literal in [_text(value), *re.findall(r'[（(]([^()（）]+)[）)]', _text(value))]:
            if literal in _UNITS:
                units.setdefault(literal, []).append(ref)
    entry = {'column': get_column_letter(column), 'metric': ' / '.join(dict.fromkeys(names)),
             'header_cells': list(dict.fromkeys(refs))}
    if len(units) == 1:
        entry['unit'], refs = next(iter(units.items()))
        entry['unit_cells'] = list(dict.fromkeys(refs))
    return entry


def _has_content(rows, formulas, row, column):
    value = rows[row - 1][column - 1].value
    return (value is not None and (not isinstance(value, str) or bool(value.strip()))
            or formulas[row - 1][column - 1].data_type == 'f')


def _numeric(cell):
    value = cell.value
    return type(value) in (int, float) and isfinite(value)


def _source_header_issues(blocks, rows, merges):
    """A structural repair cannot approve a label borrowed from another column."""
    from .ai_review import _literal_supported, _verbatim_supported

    issues = []
    for index, block in enumerate(blocks):
        if not _eligible_date_block(block, len(rows), len(rows[0])):
            continue
        headers = block['header_rows']
        for column in block['columns']:
            number = colno(column['column'])
            original = _header(rows, merges, number, headers)
            refs = column.get('header_cells', original['header_cells'])
            available = {}
            for ref in original['header_cells']:
                letter, row = re.fullmatch(r'([A-Z]+)([0-9]+)', ref).groups()
                available[ref] = {'value': rows[int(row) - 1][colno(letter) - 1].value}
            unit_refs = column.get('unit_cells') or refs
            if (isinstance(refs, list) and all(isinstance(r, str) and r in available for r in refs)
                    and _literal_supported(column.get('metric'), refs, available, composite=True)
                    and (not column.get('unit') or isinstance(unit_refs, list)
                         and all(isinstance(r, str) and r in available for r in unit_refs)
                         and _verbatim_supported(column['unit'], unit_refs, available))):
                continue
            cell = f'{column["column"]}{headers[-1]}'
            issues.append({'code': 'unverified_source_header', 'block_index': index,
                           'field': 'header_cells', 'column': column['column'], 'cells': [cell],
                           'message': f'{column["column"]} 列的名称或单位缺少本列表头依据，请核对 {cell} 附近的原表。'})
            break
    return issues


def _partition_dates(block, rows, formulas, merges):
    """Split only when a recurring header pattern is between dated data runs."""
    axis = colno(block['date_column'])
    start = block['data_start_row']
    end = block.get('data_end_row') or len(rows)
    columns = [colno(item['column']) for item in block['columns']]
    sample_header = block['header_rows'][-1]
    pattern = {c: _title(rows[sample_header - 1][c - 1].value) for c in columns}
    pattern = {c: title for c, title in pattern.items() if title}
    if not pattern:
        return [block]
    effective_headers = block['header_rows']
    sections = []

    def date_at(row):
        return complete_date(rows[row - 1][axis - 1].value)

    def build(first, last, headers):
        result = {**block, 'data_start_row': first, 'data_end_row': last, 'header_rows': headers}
        labels = list(dict.fromkeys(_title(rows[r - 1][axis - 1].value) for r in headers))
        label = ' / '.join(label for label in labels if label)
        if label and not (headers == block['header_rows'] and block.get('name')):
            result['name'] = label
        if headers != block['header_rows']:
            result.pop('header_expectations', None)
            result['columns'] = [_header(rows, merges, c, headers) for c in columns
                                 if any(_has_content(rows, formulas, r, c) for r in range(first, last + 1))
                                 or _header(rows, merges, c, headers)['metric']]
        return result

    first = start
    row = start + 1
    while row < end:
        if date_at(row) or not date_at(row - 1):
            row += 1
            continue
        stop = row
        while stop <= end and stop < row + len(block['header_rows']) and not date_at(stop):
            stop += 1
        if stop > end or not date_at(stop):
            row = stop
            continue
        header_rows = list(range(row, stop))
        # A footer or invalid date beside numeric values is not a repeated header.
        local = [rows[r - 1][c - 1] for r in header_rows for c in (axis, *columns)]
        matched = sum(_title(rows[stop - 2][c - 1].value) == label for c, label in pattern.items())
        if (matched >= min(len(pattern), max(2, (len(pattern) + 1) // 2))
                and all(cell.value is None or (_text(cell.value) and cell.data_type != 'e') for cell in local)
                and not any(formulas[r - 1][c - 1].data_type == 'f'
                            for r in header_rows for c in (axis, *columns))
                and any(_numeric(rows[stop - 1][c - 1]) for c in columns)):
            sections.append(build(first, row - 1, effective_headers))
            first, effective_headers = stop, header_rows
        row = stop + 1
    return [*sections, build(first, end, effective_headers)] if sections else [block]


def _annual_label(cell):
    value = _text(cell.value)
    return value if cell.data_type != 'e' and re.fullmatch(
        r'\d{4}(?:年|[/\-]\d{2,4}(?:年度)?|年\d{1,2}月)?', value) else None


def _vertical_period(rows, merges):
    """Find a unique, contiguous period run with adjacent named value columns."""
    candidates = []
    for axis in range(1, len(rows[0])):
        for start in (2, 3):
            if start > len(rows) or not _annual_label(rows[start - 1][axis - 1]):
                continue
            if any(_annual_label(row[axis - 1]) for row in rows[:start - 1]):
                continue
            periods = []
            for row in rows[start - 1:]:
                label = _annual_label(row[axis - 1])
                if label is None:
                    break
                periods.append(label)
            if len(periods) < 2 or len(set(periods)) != len(periods):
                continue
            end = start + len(periods) - 1
            headers = list(range(1, start))
            columns = []
            for col in range(axis + 1, len(rows[0]) + 1):
                column = _header(rows, merges, col, headers)
                values = [row[col - 1] for row in rows[start - 1:end]]
                if (not column['metric'] or not any(_numeric(cell) for cell in values)
                        or any(cell.value is not None and not _numeric(cell) and cell.data_type != 'e' for cell in values)):
                    break
                columns.append(column)
            if columns:
                candidates.append({'date_mode': 'period', 'date_column': get_column_letter(axis),
                                   'header_rows': headers, 'data_start_row': start, 'data_end_row': end,
                                   'columns': columns})
    return candidates[0] if len(candidates) == 1 else None


def _eligible_date_block(block, height, width):
    from .generic_parser import _column
    if not isinstance(block, dict) or block.get('layout') == 'matrix' or block.get('date_mode') != 'date':
        return False
    start, end = block.get('data_start_row'), block.get('data_end_row')
    headers, columns = block.get('header_rows'), block.get('columns')
    if (type(start) is not int or not 1 <= start <= height
            or (end is not None and (type(end) is not int or not start <= end <= height))
            or not isinstance(headers, list) or not headers
            or any(type(row) is not int or not 1 <= row < start for row in headers)
            or not isinstance(columns, list) or not columns):
        return False
    try:
        numbers = [_column(item.get('column'), width) for item in columns if isinstance(item, dict)]
        axis = _column(block.get('date_column'), width)
    except ValueError:
        return False
    return len(set(numbers)) == len(columns) and axis not in numbers


def prepare_assist(manager, import_id, mapping):
    """Check the original, including formulas, before choosing candidate boundaries."""
    doc = manager.get(import_id)
    sheet = manager.sheet(import_id, mapping['sheet'])
    if sheet['fingerprint'] != mapping['fingerprint']:
        raise ValueError('当前草稿与原工作表身份不一致，请刷新后重试。')
    result = {'mapping': mapping, 'diagnostics': []}
    blocks = mapping['blocks']
    height, width = sheet['rows'], sheet['columns']
    if (not height or not width or height * width > MAX_CELLS
            or (blocks and not any(_eligible_date_block(b, height, width) for b in blocks))):
        return result
    path = _verified_source(manager, doc)
    charts, complete_charts = _charts(path, sheet)
    if not blocks and (sheet.get('charts') or {}).get('chart_count'):
        from .batch_ai_import import inspect_sheet_data
        if inspect_sheet_data(manager, import_id, sheet['name'])['status'] == 'no_indicator_data':
            source_names = _chart_sources(manager, doc, sheet, charts, path) if complete_charts else []
            if source_names:
                names = '、'.join(f'「{name}」' for name in source_names)
                return {**result, 'reference': {'code': 'published_chart_source',
                        'message': f'本页数据来自 {names} 表，已发布，无需重复导入。',
                        'source_sheets': source_names}}
            return {**result, 'chart_pending': '曲线的原表和范围尚未全部核对为已发布，请先检查曲线来源。'}
    with _mapped_readers(path, doc['sha256']) as (values, formula_book):
        rows = list(values[sheet['name']].iter_rows(max_row=height, max_col=width))
        formulas = list(formula_book[sheet['name']].iter_rows(max_row=height, max_col=width))
    if not rows or len(rows) != height or len(formulas) != height:
        return result
    merges = [range_boundaries(ref) for ref in (sheet.get('charts') or {}).get('merged_ranges', [])]
    diagnostics = result['diagnostics']
    if not blocks:
        period = _vertical_period(rows, merges)
        if not period:
            return result
        axis = colno(period['date_column'])
        right = colno(period['columns'][-1]['column'])
        for r, row in enumerate(rows, 1):
            cells = [f'{get_column_letter(c)}{r}' for c, cell in enumerate(row, 1)
                     if not (axis <= c <= right and r <= period['data_end_row'])
                     and (_numeric(cell) or formulas[r - 1][c - 1].data_type == 'f')]
            if cells:
                diagnostics.append({'code': 'additional_source_region', 'cells': cells[:16],
                                    'message': f'原表还有其他数值区域（如 {cells[0]}），请单独核对。'})
                break
        return {**result, 'mapping': {**mapping, 'blocks': [period]}, 'structured': True}

    corrected = []
    regions = list(_chart_regions(charts, sheet['name']))
    scanned_blocks = (sheet.get('mapping') or {}).get('blocks', [])
    axes = [colno(block['date_column']) for block in blocks if _eligible_date_block(block, height, width)]
    for block in blocks:
        if not _eligible_date_block(block, height, width):
            corrected.append(block)
            continue
        for section in _partition_dates(block, rows, formulas, merges):
            start = section['data_start_row']
            end = section.get('data_end_row') or height
            axis = colno(section['date_column'])
            pending, kept = [], []
            for item in section['columns']:
                col = colno(item['column'])
                own = _header(rows, merges, col, section['header_rows'])
                if (not own['metric']
                        and any(_has_content(rows, formulas, r, col) for r in range(start, end + 1))):
                    pending.append(item['column'])
                else:
                    kept.append(item)
            bi = len(corrected)
            section_columns = {colno(item['column']) for item in section['columns']}
            if block in scanned_blocks:
                # Formula-only columns can be invisible in the cached-value
                # scan. Inspect the original region without changing an edited selection.
                right = min((c for c in axes if c > axis), default=width + 1)
                missing = [c for c in range(axis + 1, right) if c not in section_columns
                           and any(_has_content(rows, formulas, r, c) for r in range(start, end + 1))
                           and not _header(rows, merges, c, section['header_rows'])['metric']]
                pending.extend(get_column_letter(c) for c in missing)
                section_columns.update(missing)
            side_tables = []
            for cat, first, last, positions in regions:
                if (cat == axis or not set(positions) <= section_columns or first < start or last > end
                        or not any(_text(rows[r - 1][cat - 1].value)
                                   and not complete_date(rows[r - 1][cat - 1].value)
                                   for r in range(first, last + 1))
                        or any(_has_content(rows, formulas, r, c) for c in positions
                               for r in range(start, end + 1) if not first <= r <= last)):
                    continue
                span = f'{get_column_letter(positions[0])}{first}:{get_column_letter(positions[-1])}{last}'
                side_tables.append({'code': 'side_table', 'block_index': bi, 'range': span,
                                    'message': f'图表引用显示 {span} 是旁边的小表，请单独核对。'})
                kept = [item for item in kept if colno(item['column']) not in positions]
            if kept and (pending or side_tables):
                if pending:
                    pending.sort(key=colno)
                    diagnostics.append({'code': 'unnamed_source_columns', 'block_index': bi,
                                        'columns': pending, 'cells': [f'{c}{section["header_rows"][-1]}' for c in pending[:16]],
                                        'message': f'其余 {len(pending)} 列缺表头，尚未选作指标（{"、".join(pending)} 列）。'})
                diagnostics.extend(side_tables)
                section = {**section, 'columns': kept}
            corrected.append(section)
    if len(corrected) > MAX_ASSIST_BLOCKS:
        return {**result, 'diagnostics': [{'code': 'too_many_source_sections',
                'message': '原表重复表头较多，请分段核对区块。'}]}
    structured = corrected != blocks or bool(diagnostics)
    return {**result, 'mapping': {**mapping, 'blocks': corrected}, 'structured': structured,
            'blocked': _source_header_issues(corrected, rows, merges) if structured or sheet.get('partial_review') else []}


def mark_reference(manager, import_id, mapping, *, before_write):
    """Persist a reference decision after rechecking the live snapshot and source."""
    with manager.lock:
        doc = manager.get(import_id)
        sheet = next(s for s in doc['sheets'] if s['name'] == mapping['sheet'])
        if any(s['name'] == sheet['name'] and s.get('published_in_dataset')
               for s in manager.publication_view(doc)['sheets']):
            raise ValueError('本页已有已发布数据，请人工核对后再调整。')
        checked = prepare_assist(manager, import_id, mapping).get('reference')
        if not checked:
            raise ValueError('曲线来源尚未全部发布，或本页还有内容需要核对。')
        before_write()
        sheet['automatic_skip'] = {**checked, 'verified_source_sha256': doc['sha256'],
                                   'verified_fingerprint': sheet['fingerprint']}
        sheet['import_state'] = 'skipped'
        manager._save(doc)
        return checked
