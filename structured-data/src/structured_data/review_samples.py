"""Bounded samples for review; this never approves or publishes a mapping."""
from __future__ import annotations

from datetime import datetime
import hashlib
import math
import re
import time

from openpyxl.utils import column_index_from_string, get_column_letter
from .generic_parser import _date, validate_mapping
from .sheet_view import read_sheet_view


def preview_mapping_samples(path, sheet_scan, mapping):
    checked = validate_mapping(mapping, sheet_scan)
    deadline = time.monotonic() + 20
    cache = {}
    reads = 0

    def cell_at(row, column):
        nonlocal reads
        col = column_index_from_string(column)
        # Share a small row window across adjacent values and their date sources.
        row_start = ((row - 1) // 3) * 3 + 1
        col_start = ((col - 1) // 16) * 16 + 1
        key = (row_start, col_start)
        if key not in cache:
            if reads >= 32 or time.monotonic() > deadline:
                raise ValueError("样本范围过于分散，请缩小区块后查看样本，或执行完整检查。")
            view = read_sheet_view(path, checked["sheet"], row_start, col_start, 3, 16)
            cache[key] = {(c["row"], c["column"]): {**c, "date1904": view["date1904"]} for c in view["cells"]}
            reads += 1
        return cache[key].get((row, col), {})

    def date_at(cell, block):
        value = cell.get("date_value") if block["date_mode"] in ("date", "shared_cell") else cell.get("value")
        if value is None:
            value = cell.get("value")
        if isinstance(value, str) and "T" in value:
            try:
                value = datetime.fromisoformat(value)
            except ValueError:
                pass
        mode = "custom" if block.get("date_format") else ("date" if block["date_mode"] == "shared_cell" else block["date_mode"])
        # Excel-serial mode uses the explicit workbook epoch supplied by the reader.
        from openpyxl.utils.datetime import CALENDAR_MAC_1904, CALENDAR_WINDOWS_1900
        epoch = CALENDAR_MAC_1904 if cell.get("date1904") else CALENDAR_WINDOWS_1900
        return _date(value, mode, epoch, block.get("date_format"))

    blocks = []
    for bi, block in enumerate(checked["blocks"][:8]):
        end = block.get("data_end_row") or sheet_scan["rows"]
        start = block["data_start_row"]
        sample_rows = sorted(set(range(start, min(end + 1, start + 2))) | {end})
        samples, series = [], []
        matrix = block.get("layout") == "matrix"
        for rn in sample_rows:
            for ci, column in enumerate(block["columns"][:4]):
                source = cell_at(rn, column["column"])
                raw = source.get("value")
                category = None
                if block.get("category_column"):
                    category = cell_at(rn, block["category_column"]).get("value")
                metric = column.get("metric")
                unit = column.get("unit")
                period = None
                date = None
                date_cell = None
                if matrix:
                    metric = cell_at(rn, block["row_label_column"]).get("value")
                    rule = next((rule for rule in block.get("row_rules", []) if rule["row"] == rn), {})
                    metric = rule.get("metric") or metric
                    unit = rule.get("unit")
                    period = column["period"]
                elif block["date_mode"] == "period":
                    label = cell_at(rn, block["date_column"]).get("value")
                    period = None if label is None else str(int(label)) if isinstance(label, float) and label.is_integer() else str(label)
                elif block["date_mode"] != "none":
                    date_cell = block["date_cell"] if block["date_mode"] == "shared_cell" else f'{block["date_column"]}{rn}'
                    match = re.fullmatch(r"([A-Z]+)([0-9]+)", date_cell)
                    date = date_at(cell_at(int(match[2]), match[1]), block)
                sid = "sample." + hashlib.sha256(f"{bi}:{rn}:{ci}:{metric}:{category}".encode()).hexdigest()[:16]
                series.append({"series_id": sid, "metric": str(metric or "未命名指标"), "unit": unit, "dimensions": {"category": str(category)} if category is not None else {}})
                valid = isinstance(raw, (int, float)) and not isinstance(raw, bool) and math.isfinite(raw)
                status = "valid" if valid else "excel_error" if source.get("data_type") == "e" else "formula_no_cache" if raw is None and source.get("formula") else "blank" if raw is None else "unexpected_text"
                samples.append({"series_id": sid, "date": date, "period": period, "category": category,
                                "value": raw if valid else None, "value_status": status,
                                "source_cell": f'{column["column"]}{rn}', "date_cell": date_cell,
                                "period_cell": (column.get("header_cells") or [None])[-1] if matrix else f'{block["date_column"]}{rn}' if block["date_mode"] == "period" else None,
                                "category_cell": f'{block["category_column"]}{rn}' if block.get("category_column") else None,
                                "metric_cell": f'{block["row_label_column"]}{rn}' if matrix else None})
        blocks.append({"name": block.get("name") or f"数据区块 {bi + 1}", "series": series, "sample": samples, "issues": []})
    return {"sample_only": True, "blocks": blocks, "mapping": checked,
            "notice": "这里只展示选定区块的首尾小样本，最多 8 个区块、每块 4 个指标。完整检查通过后才能导入。"}
