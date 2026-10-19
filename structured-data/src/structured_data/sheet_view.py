"""Bounded, original-coordinate XLSX preview without loading other worksheets.

Worksheet and shared-string XML are parsed incrementally. Only the requested
window and intersecting merge anchors are retained. Small global metadata has
separate hard caps; no openpyxl workbook or global shared-string list is built.
"""
from __future__ import annotations

import math
import colorsys
import json
import posixpath
import re
import struct
import time
from dataclasses import dataclass
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any
from xml.etree import ElementTree as ET
from xml.parsers import expat
from zipfile import BadZipFile, ZipFile


_MAIN = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
_REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
_CELL = re.compile(r"([A-Z]{1,3})([1-9][0-9]*)\Z")
_MAX_ROW, _MAX_COL = 1_048_576, 16_384
_BUILTIN_FORMATS = {0: "General", 1: "0", 2: "0.00", 3: "#,##0", 4: "#,##0.00",
                    9: "0%", 10: "0.00%", 14: "mm-dd-yy", 15: "d-mmm-yy",
                    16: "d-mmm", 17: "mmm-yy", 18: "h:mm AM/PM", 19: "h:mm:ss AM/PM",
                    20: "h:mm", 21: "h:mm:ss", 22: "m/d/yy h:mm",
                    45: "mm:ss", 46: "[h]:mm:ss", 47: "mmss.0"}
# SpreadsheetML standard indexed palette (ECMA-376 18.8.27), RGB only.
_INDEXED_COLORS = (
    "000000", "FFFFFF", "FF0000", "00FF00", "0000FF", "FFFF00", "FF00FF", "00FFFF",
    "000000", "FFFFFF", "FF0000", "00FF00", "0000FF", "FFFF00", "FF00FF", "00FFFF",
    "800000", "008000", "000080", "808000", "800080", "008080", "C0C0C0", "808080",
    "9999FF", "993366", "FFFFCC", "CCFFFF", "660066", "FF8080", "0066CC", "CCCCFF",
    "000080", "FF00FF", "FFFF00", "00FFFF", "800080", "800000", "008080", "0000FF",
    "00CCFF", "CCFFFF", "CCFFCC", "FFFF99", "99CCFF", "FF99CC", "CC99FF", "FFCC99",
    "3366FF", "33CCCC", "99CC00", "FFCC00", "FF9900", "FF6600", "666699", "969696",
    "003366", "339966", "003300", "333300", "993300", "993366", "333399", "333333",
)


@dataclass(frozen=True)
class SheetViewLimits:
    """Hard resource ceilings, independent of the client request."""
    file_bytes: int = 512 * 1024 * 1024
    zip_directory_bytes: int = 4 * 1024 * 1024
    parts: int = 10_000
    metadata_bytes: int = 256 * 1024
    styles_bytes: int = 2 * 1024 * 1024
    sheet_xml_bytes: int = 128 * 1024 * 1024
    shared_strings_bytes: int = 64 * 1024 * 1024
    xml_token_bytes: int = 64 * 1024
    xml_depth: int = 64
    xml_events: int = 6_000_000
    sheet_cells: int = 1_000_000
    row_count: int = 160
    col_count: int = 64
    output_cells: int = 10_240
    cell_text_bytes: int = 32 * 1024
    output_text_bytes: int = 1024 * 1024
    merges: int = 2048
    style_items: int = 4096
    seconds: float = 20.0


class _Finished(Exception):
    """Internal early exit after all requested shared strings were found."""


def _tag(name: str) -> str:
    return name.rsplit("}", 1)[-1].rsplit(":", 1)[-1]


def _coordinate(cell: str) -> tuple[int, int]:
    match = _CELL.fullmatch(cell.replace("$", ""))
    if not match:
        raise ValueError(f"工作表包含无效单元格坐标：{cell!r}")
    column = 0
    for char in match[1]:
        column = column * 26 + ord(char) - 64
    row = int(match[2])
    if row > _MAX_ROW or column > _MAX_COL:
        raise ValueError("工作表坐标超过 Excel 行列范围")
    return row, column


def _cell_ref(row: int, column: int) -> str:
    label = ""
    while column:
        column, remainder = divmod(column - 1, 26)
        label = chr(65 + remainder) + label
    return f"{label}{row}"


def _merge(ref: str) -> dict[str, int]:
    ends = ref.split(":")
    if len(ends) not in (1, 2):
        raise ValueError("工作表包含无效合并区域")
    r1, c1 = _coordinate(ends[0])
    r2, c2 = _coordinate(ends[-1])
    if r2 < r1 or c2 < c1:
        raise ValueError("工作表合并区域起止顺序错误")
    return {"start_row": r1, "end_row": r2, "start_col": c1, "end_col": c2}


def _intersects(rect: dict[str, int], window: dict[str, int]) -> bool:
    return not (rect["end_row"] < window["start_row"] or rect["start_row"] > window["end_row"]
                or rect["end_col"] < window["start_col"] or rect["start_col"] > window["end_col"])


def _part_name(name: str) -> str:
    if (not name or "\\" in name or name.startswith("/") or ":" in name
            or any(piece in ("..", "") for piece in name.split("/"))):
        raise ValueError("Excel 压缩包包含无效或越界路径")
    return name


def _target(base: str, target: str) -> str:
    if not target or "\\" in target or ":" in target or "?" in target or "#" in target:
        raise ValueError("Excel 包关系包含无效路径")
    resolved = posixpath.normpath(target.lstrip("/") if target.startswith("/") else
                                 posixpath.join(posixpath.dirname(base), target))
    return _part_name(resolved)


def _preflight(path: Path, limits: SheetViewLimits) -> None:
    if not path.is_file():
        raise ValueError("Excel 文件不存在")
    size = path.stat().st_size
    if size > limits.file_bytes:
        raise ValueError("Excel 文件超过原表预览大小上限")
    # ZipFile otherwise materializes an attacker-controlled central directory
    # before we can inspect its part count. Check the EOCD first.
    with path.open("rb") as stream:
        stream.seek(max(0, size - 65_557))
        tail = stream.read(65_557)
    offset = tail.rfind(b"PK\x05\x06")
    if offset < 0 or len(tail) - offset < 22:
        raise ValueError("文件不是有效的 XLSX 压缩包")
    _, disk, directory_disk, disk_parts, parts, directory_size, _, comment_size = struct.unpack(
        "<4s4H2IH", tail[offset:offset + 22])
    if offset + 22 + comment_size != len(tail):
        raise ValueError("Excel 压缩包目录不完整")
    if disk or directory_disk or disk_parts != parts or parts == 65535:
        raise ValueError("原表预览不支持分卷或超大 ZIP64 文件")
    if parts > limits.parts or directory_size > limits.zip_directory_bytes:
        raise ValueError("Excel 压缩包目录超过预览上限")


class _Reader:
    def __init__(self, package: ZipFile, limits: SheetViewLimits):
        self.package, self.limits = package, limits
        self.deadline = time.monotonic() + limits.seconds
        self.opened: list[str] = []
        self.color_warnings: list[str] = []

    def part(self, name: str, cap: int):
        name = _part_name(name)
        try:
            info = self.package.getinfo(name)
        except KeyError as exc:
            raise ValueError(f"Excel 缺少必要文件：{name}") from exc
        if info.flag_bits & 1:
            raise ValueError("原表预览不支持加密 Excel 文件")
        if info.file_size > cap:
            raise ValueError(f"{name} 解压后超过预览处理上限")
        self.opened.append(name)
        return self.package.open(info)

    def parse(self, name: str, cap: int, handler: Any) -> None:
        parser = expat.ParserCreate(namespace_separator="}")
        depth = events = read_bytes = last_event = 0
        def event():
            nonlocal events, last_event
            events += 1
            last_event = parser.CurrentByteIndex
            if events > self.limits.xml_events:
                raise ValueError("Excel XML 节点过多，超过预览处理上限")
        def start(tag, attrs):
            nonlocal depth
            event()
            depth += 1
            if depth > self.limits.xml_depth:
                raise ValueError("Excel XML 嵌套过深")
            if sum(len(k) + len(v) for k, v in attrs.items()) > self.limits.xml_token_bytes:
                raise ValueError("Excel XML 属性超过处理上限")
            handler.start(_tag(tag), attrs)
        def end(tag):
            nonlocal depth
            event()
            handler.end(_tag(tag))
            depth -= 1
        def text(value):
            event()
            handler.text(value)
        def prohibited(*_):
            raise ValueError("Excel XML 不允许 DTD 或外部实体")
        parser.StartElementHandler, parser.EndElementHandler = start, end
        parser.CharacterDataHandler = text
        parser.StartDoctypeDeclHandler = prohibited
        parser.EntityDeclHandler = prohibited
        parser.ExternalEntityRefHandler = prohibited
        try:
            with self.part(name, cap) as stream:
                while chunk := stream.read(16 * 1024):
                    read_bytes += len(chunk)
                    if read_bytes > cap or time.monotonic() > self.deadline:
                        raise ValueError("Excel 原表预览超出处理上限或超时")
                    parser.Parse(chunk, False)
                    if read_bytes - last_event > self.limits.xml_token_bytes:
                        raise ValueError("Excel XML 单个标记超过处理上限")
                parser.Parse(b"", True)
        except _Finished:
            return
        except expat.ExpatError as exc:
            raise ValueError(f"Excel XML 无法解析：{name}") from exc

    def metadata(self, name: str, cap: int) -> ET.Element:
        # Build only explicitly bounded small metadata using the same safe XML
        # parser so DTD/entity declarations cannot expand before a size check.
        class Tree:
            def __init__(self):
                self.builder = ET.TreeBuilder()
            def start(self, tag, attrs):
                self.builder.start(tag, attrs)
            def end(self, tag):
                self.builder.end(tag)
            def text(self, text):
                self.builder.data(text)
        tree = Tree()
        self.parse(name, cap, tree)
        return tree.builder.close()


def _workbook(reader: _Reader, sheet_name: str) -> dict[str, Any]:
    workbook = reader.metadata("xl/workbook.xml", reader.limits.metadata_bytes)
    relations = reader.metadata("xl/_rels/workbook.xml.rels", reader.limits.metadata_bytes)
    rels = {item.get("Id"): item for item in relations}
    sheet = next((item for item in workbook.iter("sheet") if item.get("name") == sheet_name), None)
    if sheet is None:
        raise ValueError(f"工作表不存在：{sheet_name}")
    relation_id = sheet.get(f"{_REL}}}id") or sheet.get("r:id")
    relation = rels.get(relation_id)
    if relation is None or relation.get("TargetMode") == "External":
        raise ValueError("工作表关系缺失或指向外部文件")
    if not relation.get("Type", "").endswith("/worksheet"):
        raise ValueError("所选对象不是普通工作表")
    props = workbook.find("workbookPr")
    output = {"part": _target("xl/workbook.xml", relation.get("Target", "")),
              "date1904": props is not None and props.get("date1904") in ("1", "true")}
    for kind in ("styles", "sharedStrings", "theme"):
        relation = next((item for item in relations if item.get("Type", "").endswith("/" + kind)), None)
        output[kind] = (_target("xl/workbook.xml", relation.get("Target", ""))
                        if relation is not None and relation.get("TargetMode") != "External" else None)
    return output


class _Sheet:
    def __init__(self, window, limits, anchors=None, metadata=True):
        self.window, self.limits, self.anchors, self.metadata = window, limits, anchors or set(), metadata
        self.cells, self.merges, self.columns, self.rows = [], [], {}, {}
        self.total_rows = self.total_cols = self.cell_count = self.merge_count = self.output_bytes = 0
        self.default_row_height, self.default_column_width = 15.0, 8.43
        self.cell, self.field, self.pieces, self.piece_bytes, self.stack = None, None, [], 0, []
        self.row = self.previous_column = 0
        self.dimension = None

    def start(self, tag, attrs):
        self.stack.append(tag)
        if tag == "dimension" and self.metadata and attrs.get("ref"):
            bounds = _merge(attrs["ref"])
            self.dimension = attrs["ref"]
            self.total_rows, self.total_cols = bounds["end_row"], bounds["end_col"]
        elif tag == "sheetFormatPr" and self.metadata:
            self.default_row_height = _finite_float(attrs.get("defaultRowHeight", 15))
            self.default_column_width = _finite_float(attrs.get("defaultColWidth", 8.43))
        elif tag == "row":
            self.row = int(attrs.get("r", self.row + 1))
            self.previous_column = 0
            if not 1 <= self.row <= _MAX_ROW:
                raise ValueError("工作表行号无效")
            self.total_rows = max(self.total_rows, self.row)
            if self.metadata and self.window["start_row"] <= self.row <= self.window["end_row"]:
                if "ht" in attrs:
                    self.rows[str(self.row)] = _finite_float(attrs["ht"])
        elif tag == "col" and self.metadata:
            first, last = int(attrs.get("min", 0)), int(attrs.get("max", 0))
            if first < 1 or last < first or last > _MAX_COL:
                raise ValueError("工作表列宽范围无效")
            for col in range(max(first, self.window["start_col"]), min(last, self.window["end_col"]) + 1):
                if "width" in attrs:
                    self.columns[str(col)] = _finite_float(attrs["width"])
        elif tag == "mergeCell" and self.metadata:
            self.merge_count += 1
            if self.merge_count > self.limits.merges:
                raise ValueError("工作表合并区域过多，超过预览上限")
            bounds = _merge(attrs.get("ref", ""))
            self.total_rows = max(self.total_rows, bounds["end_row"])
            self.total_cols = max(self.total_cols, bounds["end_col"])
            if _intersects(bounds, self.window):
                self.merges.append(bounds)
        elif tag == "c":
            self.cell_count += 1
            if self.cell_count > self.limits.sheet_cells:
                raise ValueError("单张工作表单元格数超过预览处理上限")
            ref = attrs.get("r") or _cell_ref(self.row, self.previous_column + 1)
            row, col = _coordinate(ref)
            self.previous_column = col
            self.total_rows, self.total_cols = max(self.total_rows, row), max(self.total_cols, col)
            visible = (self.window["start_row"] <= row <= self.window["end_row"] and
                       self.window["start_col"] <= col <= self.window["end_col"])
            self.cell = ({"row": row, "column": col, "cell": ref, "type": attrs.get("t", "n"),
                          "style_id": int(attrs.get("s", 0)), "raw_value": None, "formula": None,
                          "inline": "", "merge_anchor": (row, col) in self.anchors}
                         if visible or (row, col) in self.anchors else None)
        elif tag in ("v", "f", "t"):
            self.field, self.pieces, self.piece_bytes = tag, [], 0

    def text(self, value):
        if self.field is not None:
            # Limit individual XML cell fields even outside the requested
            # window: a giant unrelated cell must not grow Expat/Python memory.
            self.piece_bytes += len(value.encode("utf-8"))
            if self.piece_bytes > self.limits.cell_text_bytes:
                raise ValueError("工作表单元格文字超过预览上限")
            if self.cell is not None and "rPh" not in self.stack:
                self.pieces.append(value)

    def end(self, tag):
        if tag == self.field:
            value = "".join(self.pieces)
            if self.cell is not None:
                if tag == "v":
                    self.cell["raw_value"] = value
                elif tag == "f":
                    self.cell["formula"] = value
                else:
                    self.cell["inline"] += value
                    if len(self.cell["inline"].encode("utf-8")) > self.limits.cell_text_bytes:
                        raise ValueError("工作表单元格文字超过预览上限")
            self.field, self.pieces = None, []
        if tag == "c" and self.cell is not None:
            self.output_bytes += sum(len(str(value).encode("utf-8")) for value in self.cell.values())
            if self.output_bytes > self.limits.output_text_bytes or len(self.cells) >= self.limits.output_cells:
                raise ValueError("预览窗口内容超过输出上限")
            self.cells.append(self.cell)
            self.cell = None
        self.stack.pop()


class _Strings:
    def __init__(self, wanted, limits):
        self.wanted, self.limits = wanted, limits
        self.result, self.index, self.pieces, self.size, self.total, self.stack = {}, -1, [], 0, 0, []
    def start(self, tag, attrs):
        self.stack.append(tag)
        if tag == "si":
            self.index += 1
            self.pieces, self.size = [], 0
    def text(self, value):
        if self.stack and self.stack[-1] == "t" and "rPh" not in self.stack:
            self.size += len(value.encode("utf-8"))
            if self.size > self.limits.cell_text_bytes:
                raise ValueError("Excel 共享字符串超过单元格文字上限")
            if self.index in self.wanted:
                self.pieces.append(value)
    def end(self, tag):
        if tag == "si" and self.index in self.wanted:
            self.total += self.size
            if self.total > self.limits.output_text_bytes:
                raise ValueError("Excel 共享字符串超过预览输出上限")
            self.result[self.index] = "".join(self.pieces)
            if len(self.result) == len(self.wanted):
                raise _Finished()
        self.stack.pop()


def _finite_float(value) -> float:
    number = float(value)
    if not math.isfinite(number) or number < 0:
        raise ValueError("工作表尺寸包含无效数字")
    return number


def _theme_colors(reader, part):
    if not part:
        return {}
    root = reader.metadata(part, min(reader.limits.metadata_bytes, 256 * 1024))
    scheme = next(root.iter("clrScheme"), None)
    if scheme is None:
        return {}
    names = ("lt1", "dk1", "lt2", "dk2", "accent1", "accent2", "accent3", "accent4",
             "accent5", "accent6", "hlink", "folHlink")
    colors = {}
    for index, name in enumerate(names):
        node = scheme.find(name)
        if node is None or not len(node):
            continue
        color = node[0]
        raw = color.get("val") if color.tag == "srgbClr" else color.get("lastClr") if color.tag == "sysClr" else None
        if raw and re.fullmatch(r"[0-9a-fA-F]{6}", raw):
            colors[index] = raw.upper()
    return colors


def _color(element, theme, indexed, warnings) -> dict[str, str] | None:
    if element is None:
        return None
    output = dict(element.attrib)
    rgb = output.get("rgb")
    if rgb and re.fullmatch(r"[0-9a-fA-F]{6}|[0-9a-fA-F]{8}", rgb):
        rgb = rgb[-6:].upper()
    elif "theme" in output:
        rgb = theme.get(int(output["theme"]))
    elif "indexed" in output:
        index = int(output["indexed"])
        rgb = indexed[index][-6:] if 0 <= index < len(indexed) else None
    else:
        rgb = None
    if rgb:
        tint = float(output.get("tint", "0"))
        if not math.isfinite(tint) or not -1 <= tint <= 1:
            raise ValueError("Excel 颜色 tint 无效")
        if tint:
            channels = [int(rgb[i:i + 2], 16) / 255 for i in (0, 2, 4)]
            hue, lightness, saturation = colorsys.rgb_to_hls(*channels)
            lightness = lightness * (1 + tint) if tint < 0 else lightness * (1 - tint) + tint
            rgb = ''.join(f"{min(255, max(0, round(value * 255))):02X}"
                          for value in colorsys.hls_to_rgb(hue, lightness, saturation))
        output["rgb"] = (output.get("rgb", "FF000000")[:2] if len(output.get("rgb", "")) == 8 else "FF") + rgb
    else:
        warning = "部分自动颜色、系统颜色或未定义主题色无法解析，已保留原始颜色元信息。"
        if warning not in warnings:
            warnings.append(warning)
    return output


def _styles(reader, part, ids, theme_part=None):
    if part is None:
        if ids - {0}:
            raise ValueError("工作表样式文件缺失")
        return {"0": {"font": {}, "fill": {}, "number_format": {"id": 0, "code": "General"}, "alignment": {}}}
    root = reader.metadata(part, reader.limits.styles_bytes)
    groups = {item.tag: item for item in root}
    if any(len(item) > reader.limits.style_items for item in groups.values()):
        raise ValueError("Excel 样式数量超过预览上限")
    formats = {int(item.get("numFmtId")): item.get("formatCode", "") for item in groups.get("numFmts", [])}
    theme = _theme_colors(reader, theme_part)
    indexed = list(_INDEXED_COLORS)
    color_group = groups.get("colors")
    custom_indexed = color_group.find("indexedColors") if color_group is not None else None
    if custom_indexed is not None and len(custom_indexed):
        if len(custom_indexed) > reader.limits.style_items:
            raise ValueError("Excel 自定义调色板数量超过预览上限")
        indexed = []
        for item in custom_indexed:
            rgb = item.get("rgb", "")
            if not re.fullmatch(r"[0-9a-fA-F]{6}|[0-9a-fA-F]{8}", rgb):
                raise ValueError("Excel 自定义调色板颜色无效")
            indexed.append(rgb[-6:].upper())
    xfs, fonts, fills = groups.get("cellXfs", []), groups.get("fonts", []), groups.get("fills", [])
    output = {}
    for style_id in ids:
        if style_id < 0 or style_id >= len(xfs):
            raise ValueError("工作表包含无效样式编号")
        xf = xfs[style_id]
        font_id, fill_id, format_id = int(xf.get("fontId", 0)), int(xf.get("fillId", 0)), int(xf.get("numFmtId", 0))
        if font_id >= len(fonts) or fill_id >= len(fills) or font_id < 0 or fill_id < 0:
            raise ValueError("工作表字体或填充样式编号无效")
        font, fill = fonts[font_id], fills[fill_id].find("patternFill")
        font_props = {"bold": False, "italic": False, "underline": False}
        for source_name, output_name in (("name", "name"), ("sz", "size"), ("b", "bold"),
                                         ("i", "italic"), ("u", "underline")):
            item = font.find(source_name)
            if item is not None:
                raw = item.get("val", "1")
                font_props[output_name] = (_finite_float(raw) if output_name == "size" else
                                           raw not in ("0", "false") if output_name in ("bold", "italic") else
                                           raw if output_name == "underline" else raw)
        font_props["color"] = _color(font.find("color"), theme, indexed, reader.color_warnings)
        alignment = xf.find("alignment")
        output[str(style_id)] = {
            "font": font_props,
            "fill": {"pattern": fill.get("patternType", "none") if fill is not None else "none",
                     "color": _color(fill.find("fgColor"), theme, indexed, reader.color_warnings) if fill is not None else None},
            "number_format": {"id": format_id, "code": formats.get(format_id, _BUILTIN_FORMATS.get(format_id))},
            "alignment": ({**alignment.attrib, "wrap_text": alignment.get("wrapText") in ("1", "true")}
                          if alignment is not None else {}),
        }
    return output


def _number(raw: str | None):
    if raw is None or raw == "":
        return None
    try:
        if re.fullmatch(r"[-+]?\d+", raw):
            integer = int(raw)
            return integer if abs(integer) <= 9_007_199_254_740_991 else raw
        number = float(raw)
        return number if math.isfinite(number) else raw
    except ValueError:
        return raw


def _date_kind(number_format):
    format_id, code = number_format["id"], number_format.get("code") or ""
    cleaned = re.sub(r'"[^"]*"|\\.|\[[^]]*\]', "", code).lower()
    custom_date = bool(re.search(r"y{2,4}|d{3,4}|(?:d{1,4}[^a-z]*m{1,5}|m{1,5}[^a-z]*d{1,4})", cleaned))
    date_format = format_id in (14, 15, 16, 17, 22) or (format_id >= 164 and custom_date)
    custom_time = bool(re.search(r"h{1,2}[^a-z]*m{1,2}|m{1,2}[^a-z]*s{1,2}", cleaned) or
                       re.search(r"\[[hms]+\]", code.lower()))
    time_format = format_id in (18, 19, 20, 21, 22, 45, 46, 47) or (format_id >= 164 and custom_time)
    return date_format, time_format


def _date_value(value, number_format, date1904):
    if type(value) not in (float, int) or not _date_kind(number_format)[0]:
        return None
    if not date1904 and 60 <= value < 61:
        return None  # Excel's fictitious leap day is not a usable real date.
    if value < 0 or (not date1904 and value < 1):
        return None  # Excel 1900 serial zero has no unambiguous calendar date.
    try:
        base = datetime(1904, 1, 1) if date1904 else datetime(1899, 12, 31)
        serial = value if date1904 or value < 60 else value - 1
        moment = base + timedelta(days=serial)
        return (moment.date().isoformat() if moment.time().isoformat() == "00:00:00" else
                moment.isoformat(timespec="seconds"))
    except (OverflowError, ValueError):
        return None


def _display(value, raw, number_format, date1904):
    if value is None:
        return ""
    if isinstance(value, bool):
        return "TRUE" if value else "FALSE"
    if not isinstance(value, (float, int)):
        return str(value)
    format_id, code = number_format["id"], number_format.get("code") or ""
    # A plain number is never guessed a date.
    date_format, time_format = _date_kind(number_format)
    if date_format or time_format:
        if date_format and (value < 0 or (not date1904 and value < 1)):
            return raw or str(value)
        # Serial 60 denotes Excel's fictitious 1900-02-29; preserve it explicitly.
        if not date1904 and 60 <= value < 61:
            return "1900-02-29" if date_format else _time_display(value)
        try:
            base = datetime(1904, 1, 1) if date1904 else datetime(1899, 12, 31)
            serial = value if date1904 or value < 60 else value - 1
            moment = base + timedelta(days=serial)
            has_date = date_format and not (format_id in (18, 19, 20, 21))
            has_time = time_format
            if has_date:
                return moment.strftime("%Y-%m-%d %H:%M:%S" if has_time else "%Y-%m-%d")
            return _time_display(value, elapsed=format_id == 46 or "[h]" in code.lower())
        except (OverflowError, ValueError):
            return raw or str(value)
    if code in ("0%", "0.00%"):
        return f"{value * 100:.{2 if '.00' in code else 0}f}%"
    if code in ("0", "0.00", "#,##0", "#,##0.00"):
        precision = 2 if ".00" in code else 0
        return format(value, f",.{precision}f" if "," in code else f".{precision}f")
    return raw or str(value)


def _time_display(serial, elapsed=False):
    seconds = round(serial * 86400)
    if not elapsed:
        seconds %= 86400
    return f"{seconds // 3600:02}:{seconds % 3600 // 60:02}:{seconds % 60:02}"


def read_sheet_view(path, sheet_name, start_row=1, start_col=1, row_count=60, col_count=24,
                    *, limits: SheetViewLimits | None = None) -> dict[str, Any]:
    """Return original cells and basic layout for one bounded worksheet window.

    Empty coordinates remain empty; clients construct the grid from ``range``.
    Intersecting merges include their anchor cell even outside the window.
    Date display uses explicit style/epoch metadata. Formula caches are read
    without evaluation; images/charts/conditional formatting are not rendered.
    ``ValueError`` describes invalid files, missing sheets and resource limits.
    """
    limits = limits or SheetViewLimits()
    requested = (start_row, start_col, row_count, col_count)
    if any(type(value) is not int or value < 1 for value in requested):
        raise ValueError("预览行列参数必须是正整数")
    if (row_count > limits.row_count or col_count > limits.col_count
            or row_count * col_count > limits.output_cells):
        raise ValueError("预览窗口超过行列或单元格上限")
    if start_row + row_count - 1 > _MAX_ROW or start_col + col_count - 1 > _MAX_COL:
        raise ValueError("预览窗口超过 Excel 行列范围")
    if not isinstance(sheet_name, str) or not sheet_name:
        raise ValueError("工作表名称不能为空")
    window = {"start_row": start_row, "end_row": start_row + row_count - 1,
              "start_col": start_col, "end_col": start_col + col_count - 1,
              "row_count": row_count, "col_count": col_count}
    source = Path(path)
    _preflight(source, limits)
    try:
        with ZipFile(source) as package:
            infos = package.infolist()
            if len(infos) > limits.parts or len({item.filename for item in infos}) != len(infos):
                raise ValueError("Excel 压缩包文件数量超限或含重复路径")
            for item in infos:
                _part_name(item.filename.rstrip("/") if item.is_dir() else item.filename)
            reader = _Reader(package, limits)
            workbook = _workbook(reader, sheet_name)
            sheet = _Sheet(window, limits)
            reader.parse(workbook["part"], limits.sheet_xml_bytes, sheet)
            present = {(cell["row"], cell["column"]) for cell in sheet.cells}
            anchors = {(merge["start_row"], merge["start_col"]) for merge in sheet.merges} - present
            if anchors:
                anchor_sheet = _Sheet({"start_row": 0, "end_row": 0, "start_col": 0, "end_col": 0},
                                      limits, anchors=anchors, metadata=False)
                reader.parse(workbook["part"], limits.sheet_xml_bytes, anchor_sheet)
                sheet.cells.extend(anchor_sheet.cells)
            if len(sheet.cells) > limits.output_cells:
                raise ValueError("预览窗口与合并锚点超过单元格上限")
            wanted = set()
            for cell in sheet.cells:
                if cell["type"] == "s":
                    try:
                        index = int(cell["raw_value"])
                    except (TypeError, ValueError) as exc:
                        raise ValueError("工作表共享字符串编号无效") from exc
                    if index < 0:
                        raise ValueError("工作表共享字符串编号无效")
                    cell["string_id"] = index
                    wanted.add(index)
            strings = {}
            if wanted:
                if not workbook["sharedStrings"]:
                    raise ValueError("Excel 缺少共享字符串文件")
                string_reader = _Strings(wanted, limits)
                reader.parse(workbook["sharedStrings"], limits.shared_strings_bytes, string_reader)
                strings = string_reader.result
                if set(strings) != wanted:
                    raise ValueError("工作表引用了不存在的共享字符串")
            styles = _styles(reader, workbook["styles"], {cell["style_id"] for cell in sheet.cells} or {0}, workbook["theme"])
            output_size = uncached_formulas = 0
            for cell in sheet.cells:
                if time.monotonic() > reader.deadline:
                    raise ValueError("Excel 原表预览超出处理上限或超时")
                kind, raw = cell.pop("type"), cell["raw_value"]
                cell["data_type"] = kind
                inline = cell.pop("inline")
                if kind == "s":
                    value = strings[cell.pop("string_id")]
                elif kind == "inlineStr":
                    value = inline
                elif kind == "b":
                    if raw not in (None, "", "0", "1"):
                        raise ValueError("工作表布尔值无效")
                    value = raw == "1" if raw in ("0", "1") else None
                elif kind in ("str", "e", "d"):
                    value = raw
                else:
                    value = _number(raw)
                cell["value"] = value
                number_format = styles[str(cell["style_id"])]["number_format"]
                cell["display"] = _display(value, raw, number_format, workbook["date1904"])
                cell["date_value"] = _date_value(value, number_format, workbook["date1904"])
                if kind == "d" and raw:
                    try:
                        explicit_date = datetime.fromisoformat(raw.replace("Z", "+00:00"))
                        cell["date_value"] = (explicit_date.date().isoformat() if "T" not in raw else
                                              explicit_date.isoformat(timespec="seconds"))
                    except ValueError:
                        pass
                if cell["formula"] is not None and raw in (None, ""):
                    uncached_formulas += 1
                    cell["display"] = "=" + cell["formula"] if cell["formula"] else "（公式结果未缓存）"
                output_size += sum(len(str(value).encode("utf-8")) for value in cell.values())
                if output_size > limits.output_text_bytes:
                    raise ValueError("预览窗口内容超过输出上限")
            total_rows, total_cols = max(1, sheet.total_rows), max(1, sheet.total_cols)
            window["end_row"], window["end_col"] = min(window["end_row"], total_rows), min(window["end_col"], total_cols)
            window["row_count"] = max(0, window["end_row"] - start_row + 1)
            window["col_count"] = max(0, window["end_col"] - start_col + 1)
            warnings = ["原表预览保留网格与基础样式；公式使用缓存值，图表、图片及条件格式不显示。"]
            if uncached_formulas:
                warnings.append(f"当前窗口 {uncached_formulas} 个公式缺少缓存结果，显示公式文本。")
            warnings.extend(reader.color_warnings)
            output = {"sheet_name": sheet_name, "total_rows": total_rows, "total_cols": total_cols,
                    "dimension": sheet.dimension, "range": window,
                    "has_more_rows": window["end_row"] < total_rows,
                    "has_more_cols": window["end_col"] < total_cols,
                    "next_row": window["end_row"] + 1 if window["end_row"] < total_rows else None,
                    "next_col": window["end_col"] + 1 if window["end_col"] < total_cols else None,
                    "cells": sorted(sheet.cells, key=lambda cell: (cell["row"], cell["column"])),
                    "merges": sheet.merges, "styles": styles,
                    "column_widths": {column: width for column, width in sheet.columns.items()
                                       if int(column) <= total_cols},
                    "row_heights": sheet.rows,
                    "default_row_height": sheet.default_row_height,
                    "default_column_width": sheet.default_column_width,
                    "date1904": workbook["date1904"],
                    "warnings": warnings}
            # Cell estimates alone do not bound style, metadata and JSON key
            # overhead. Enforce the cap on the complete serialized schema.
            if len(json.dumps(output, ensure_ascii=False, allow_nan=False).encode("utf-8")) > limits.output_text_bytes:
                raise ValueError("原表预览完整响应超过输出大小上限")
            return output
    except (BadZipFile, OSError, RuntimeError, ET.ParseError, NotImplementedError, EOFError) as exc:
        raise ValueError("Excel 文件无法读取或压缩包损坏") from exc
