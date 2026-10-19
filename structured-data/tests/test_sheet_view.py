"""Synthetic-only tests for original-structure, bounded single-sheet preview."""
from __future__ import annotations

import tempfile
import unittest
import json
import warnings
from dataclasses import replace
from pathlib import Path
from unittest.mock import patch
from zipfile import ZIP_DEFLATED, ZipFile

from structured_data import sheet_view
from structured_data.sheet_view import SheetViewLimits, read_sheet_view


NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
PKG = "http://schemas.openxmlformats.org/package/2006/relationships"


def fixture(path, *, sheet=None, strings=None, styles=None, date1904=False, extra=None, theme=None):
    workbook = f'''<workbook xmlns="{NS}" xmlns:r="{REL}"><workbookPr date1904="{int(date1904)}"/>
      <sheets><sheet name="待确认" sheetId="1" r:id="r1"/><sheet name="无关表" sheetId="2" r:id="r2"/>
      <sheet name="损坏表" sheetId="3" r:id="r3"/></sheets></workbook>'''
    rels = f'''<Relationships xmlns="{PKG}"><Relationship Id="r1" Type="{REL}/worksheet" Target="worksheets/sheet1.xml"/>
      <Relationship Id="r2" Type="{REL}/worksheet" Target="worksheets/sheet2.xml"/>
      <Relationship Id="r3" Type="{REL}/worksheet" Target="worksheets/sheet3.xml"/>
      <Relationship Id="r4" Type="{REL}/sharedStrings" Target="sharedStrings.xml"/>
      <Relationship Id="r5" Type="{REL}/styles" Target="styles.xml"/>
      {f'<Relationship Id="r6" Type="{REL}/theme" Target="theme/theme1.xml"/>' if theme else ''}</Relationships>'''
    if sheet is None:
        sheet = f'''<worksheet xmlns="{NS}"><dimension ref="A1:F9"/>
          <sheetFormatPr defaultRowHeight="16" defaultColWidth="9"/>
          <cols><col min="1" max="1" width="12.5"/><col min="2" max="3" width="20"/></cols><sheetData>
          <row r="1" ht="28"><c r="A1" t="s" s="3"><v>0</v></c><c r="C1" t="inlineStr"><is><t xml:space="preserve"> 备注 </t></is></c></row>
          <row r="2"><c r="A2" s="1"><v>45292</v></c><c r="B2" t="s"><v>130</v></c><c r="C2" s="2"><f>SUM(C4:C5)</f><v>30</v></c></row>
          <row r="4"><c r="C4"><v>10</v></c></row><row r="5"><c r="C5"><v>20</v></c></row>
          <row r="6" ht="26"><c r="A6" t="inlineStr" s="3"><is><r><t>第二块</t></r><r><t>（库存）</t></r></is></c></row>
          <row r="8"><c r="B8" t="b"><v>1</v></c><c r="C8" t="s"><v>131</v></c></row>
          </sheetData><mergeCells count="1"><mergeCell ref="A6:C7"/></mergeCells></worksheet>'''
    if strings is None:
        values = ["日期", "指标"] + [f"无关{i}" for i in range(128)] + ["中文销售额", " 库 存 "]
        strings = f'<sst xmlns="{NS}">' + ''.join(f'<si><t xml:space="preserve">{value}</t></si>' for value in values) + '</sst>'
    if styles is None:
        styles = f'''<styleSheet xmlns="{NS}"><numFmts count="1"><numFmt numFmtId="164" formatCode="#,##0"/></numFmts>
          <fonts count="2"><font><name val="Microsoft YaHei"/><sz val="11"/></font>
          <font><name val="Microsoft YaHei"/><sz val="12"/><b/><color rgb="FFB91C1C"/></font></fonts>
          <fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FFFFEEEE"/></patternFill></fill></fills>
          <cellXfs count="4"><xf fontId="0" fillId="0" numFmtId="0"/><xf fontId="0" fillId="0" numFmtId="14"/>
          <xf fontId="0" fillId="0" numFmtId="164"/><xf fontId="1" fillId="1" numFmtId="0"><alignment horizontal="center" wrapText="1"/></xf></cellXfs></styleSheet>'''
    with ZipFile(path, "w", ZIP_DEFLATED) as package:
        package.writestr("xl/workbook.xml", workbook)
        package.writestr("xl/_rels/workbook.xml.rels", rels)
        package.writestr("xl/worksheets/sheet1.xml", sheet)
        package.writestr("xl/worksheets/sheet2.xml", f'<worksheet xmlns="{NS}"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>另一张</t></is></c></row></sheetData></worksheet>')
        package.writestr("xl/worksheets/sheet3.xml", "<worksheet><BROKEN")
        package.writestr("xl/sharedStrings.xml", strings)
        package.writestr("xl/styles.xml", styles)
        if theme:
            package.writestr("xl/theme/theme1.xml", theme)
        for name, content in (extra or {}).items():
            package.writestr(name, content)


class SheetViewTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / "synthetic.xlsx"
        fixture(self.path)

    def test_only_chosen_worksheet_is_opened(self):
        opened = []
        class TrackingZipFile(ZipFile):
            def open(self, name, *args, **kwargs):
                opened.append(name.filename if hasattr(name, "filename") else name)
                return super().open(name, *args, **kwargs)
        with patch.object(sheet_view, "ZipFile", TrackingZipFile):
            result = read_sheet_view(self.path, "待确认")
        self.assertEqual([part for part in opened if "/worksheets/" in part], ["xl/worksheets/sheet1.xml"])
        self.assertEqual(result["sheet_name"], "待确认")
        with self.assertRaisesRegex(ValueError, "XML 无法解析"):
            read_sheet_view(self.path, "损坏表")

    def test_sparse_original_grid_text_and_dimensions(self):
        result = read_sheet_view(self.path, "待确认")
        cells = {cell["cell"]: cell for cell in result["cells"]}
        self.assertEqual((result["total_rows"], result["total_cols"]), (9, 6))
        self.assertEqual((result["range"]["row_count"], result["range"]["col_count"]), (9, 6))
        self.assertNotIn("A3", cells)
        self.assertNotIn("F9", cells)
        self.assertEqual(cells["C1"]["value"], " 备注 ")
        self.assertEqual(cells["B2"]["value"], "中文销售额")
        self.assertEqual(cells["C8"]["value"], " 库 存 ")
        self.assertEqual(cells["A6"]["value"], "第二块（库存）")
        self.assertIs(cells["B8"]["value"], True)

    def test_basic_layout_and_style_metadata(self):
        result = read_sheet_view(self.path, "待确认")
        self.assertEqual(result["column_widths"], {"1": 12.5, "2": 20.0, "3": 20.0})
        self.assertEqual(result["row_heights"], {"1": 28.0, "6": 26.0})
        self.assertEqual(result["default_row_height"], 16.0)
        self.assertEqual(result["default_column_width"], 9.0)
        style = result["styles"]["3"]
        self.assertEqual(style["font"]["name"], "Microsoft YaHei")
        self.assertEqual(style["font"]["size"], 12)
        self.assertIs(style["font"]["bold"], True)
        self.assertEqual(style["font"]["color"], {"rgb": "FFB91C1C"})
        self.assertEqual(style["fill"]["color"], {"rgb": "FFFFEEEE"})
        self.assertIs(style["alignment"]["wrap_text"], True)

    def test_full_column_style_does_not_extend_used_dimension(self):
        sheet = f'''<worksheet xmlns="{NS}"><dimension ref="A1:C4"/>
          <cols><col min="1" max="16384" width="11.5"/></cols>
          <sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData></worksheet>'''
        fixture(self.path, sheet=sheet)

        result = read_sheet_view(self.path, "待确认")
        self.assertEqual(result["total_cols"], 3)
        self.assertEqual(result["range"]["end_col"], 3)
        self.assertFalse(result["has_more_cols"])
        self.assertIsNone(result["next_col"])
        self.assertEqual(result["column_widths"], {"1": 11.5, "2": 11.5, "3": 11.5})

        next_window = read_sheet_view(self.path, "待确认", start_col=2, col_count=2)
        self.assertEqual(next_window["column_widths"], {"2": 11.5, "3": 11.5})

    def test_full_column_style_without_dimension_keeps_real_cell_and_merge_extent(self):
        sheet = f'''<worksheet xmlns="{NS}">
          <cols><col min="1" max="16384" width="9.25"/></cols>
          <sheetData><row r="5"><c r="C5"><v>7</v></c></row></sheetData>
          <mergeCells count="1"><mergeCell ref="D6:F6"/></mergeCells></worksheet>'''
        fixture(self.path, sheet=sheet)

        result = read_sheet_view(self.path, "待确认", start_row=5, start_col=3, row_count=2, col_count=4)
        self.assertIsNone(result["dimension"])
        self.assertEqual((result["total_rows"], result["total_cols"]), (6, 6))
        self.assertEqual((result["range"]["end_row"], result["range"]["end_col"]), (6, 6))
        self.assertFalse(result["has_more_cols"])
        self.assertEqual(result["column_widths"], {"3": 9.25, "4": 9.25, "5": 9.25, "6": 9.25})
        self.assertEqual(result["merges"], [{"start_row": 6, "end_row": 6, "start_col": 4, "end_col": 6}])

    def test_cross_window_merge_fetches_outside_anchor(self):
        result = read_sheet_view(self.path, "待确认", start_row=7, start_col=2, row_count=2, col_count=2)
        cells = {cell["cell"]: cell for cell in result["cells"]}
        self.assertEqual(result["merges"], [{"start_row": 6, "end_row": 7, "start_col": 1, "end_col": 3}])
        self.assertEqual(cells["A6"]["value"], "第二块（库存）")
        self.assertIs(cells["A6"]["merge_anchor"], True)
        self.assertNotIn("A1", cells)
        self.assertEqual(result["range"]["start_row"], 7)

    def test_formula_cache_dates_and_no_date_guessing(self):
        result = read_sheet_view(self.path, "待确认")
        cells = {cell["cell"]: cell for cell in result["cells"]}
        self.assertEqual(cells["C2"]["formula"], "SUM(C4:C5)")
        self.assertEqual(cells["C2"]["value"], 30)
        self.assertEqual(cells["C2"]["raw_value"], "30")
        self.assertEqual(cells["C2"]["display"], "30")
        self.assertEqual(cells["A2"]["value"], 45292)
        self.assertEqual(cells["A2"]["data_type"], "n")
        self.assertEqual(cells["A2"]["date_value"], "2024-01-01")
        self.assertEqual(cells["A2"]["display"], "2024-01-01")
        self.assertIsNone(cells["C2"]["date_value"])
        self.assertFalse(result["date1904"])

    def test_date1904_epoch_and_fictitious_leap_day(self):
        sheet = f'<worksheet xmlns="{NS}"><sheetData><row r="1"><c r="A1" s="1"><v>0.5</v></c><c r="B1" s="1"><v>60</v></c></row></sheetData></worksheet>'
        fixture(self.path, sheet=sheet, date1904=True)
        cells = read_sheet_view(self.path, "待确认")["cells"]
        self.assertEqual(cells[0]["date_value"], "1904-01-01T12:00:00")
        self.assertEqual(cells[1]["date_value"], "1904-03-01")
        fixture(self.path, sheet=sheet, date1904=False)
        cells = read_sheet_view(self.path, "待确认")["cells"]
        self.assertIsNone(cells[1]["date_value"])
        self.assertEqual(cells[1]["display"], "1900-02-29")

    def test_window_paging_bounds(self):
        result = read_sheet_view(self.path, "待确认", row_count=2, col_count=2)
        self.assertTrue(result["has_more_rows"])
        self.assertTrue(result["has_more_cols"])
        self.assertEqual((result["next_row"], result["next_col"]), (3, 3))
        self.assertEqual({cell["cell"] for cell in result["cells"]}, {"A1", "A2", "B2"})
        empty = read_sheet_view(self.path, "待确认", start_row=20)
        self.assertEqual(empty["cells"], [])
        self.assertEqual(empty["range"]["row_count"], 0)

    def test_selects_another_sheet(self):
        result = read_sheet_view(self.path, "无关表")
        self.assertEqual(result["cells"][0]["value"], "另一张")

    def test_missing_sheet_and_invalid_window(self):
        with self.assertRaisesRegex(ValueError, "工作表不存在"):
            read_sheet_view(self.path, "不存在")
        for kwargs in ({"row_count": 10000}, {"col_count": 65}, {"start_row": 0},
                       {"start_col": True}, {"start_row": 1_048_576, "row_count": 2}):
            with self.subTest(kwargs=kwargs), self.assertRaises(ValueError):
                read_sheet_view(self.path, "待确认", **kwargs)

    def test_limits_bytes_cells_text_merges_and_styles(self):
        for limits in (replace(SheetViewLimits(), sheet_xml_bytes=100),
                       replace(SheetViewLimits(), metadata_bytes=100),
                       replace(SheetViewLimits(), shared_strings_bytes=100),
                       replace(SheetViewLimits(), style_items=1),
                       replace(SheetViewLimits(), sheet_cells=1),
                       replace(SheetViewLimits(), cell_text_bytes=3),
                       replace(SheetViewLimits(), output_text_bytes=50),
                       replace(SheetViewLimits(), merges=0),
                       replace(SheetViewLimits(), seconds=-1)):
            with self.subTest(limits=limits), self.assertRaises(ValueError):
                read_sheet_view(self.path, "待确认", limits=limits)

    def test_forbids_zip_paths_and_duplicate_parts(self):
        fixture(self.path, extra={"../../escape.xml": "oops"})
        with self.assertRaisesRegex(ValueError, "越界路径"):
            read_sheet_view(self.path, "待确认")
        fixture(self.path)
        with warnings.catch_warnings(), ZipFile(self.path, "a") as package:
            warnings.simplefilter("ignore", UserWarning)
            package.writestr("xl/workbook.xml", "<workbook/>")
        with self.assertRaisesRegex(ValueError, "重复路径"):
            read_sheet_view(self.path, "待确认")

    def test_forbids_dtd_entities_and_invalid_cell_references(self):
        for sheet in ('<!DOCTYPE worksheet [<!ENTITY x "expanded">]><worksheet>&x;</worksheet>',
                      '<worksheet><sheetData><row r="1"><c r="XFE1"><v>1</v></c></row></sheetData></worksheet>'):
            fixture(self.path, sheet=sheet)
            with self.assertRaises(ValueError):
                read_sheet_view(self.path, "待确认")

    def test_long_unselected_text_cannot_allocate_unbounded(self):
        sheet = f'<worksheet xmlns="{NS}"><sheetData><row r="999"><c r="A999" t="inlineStr"><is><t>{"x" * 40000}</t></is></c></row></sheetData></worksheet>'
        fixture(self.path, sheet=sheet)
        with self.assertRaisesRegex(ValueError, "文字超过"):
            read_sheet_view(self.path, "待确认", row_count=2)

    def test_large_unclosed_attribute_is_bounded(self):
        sheet = '<worksheet enormous="' + 'x' * 80000 + '"><sheetData/></worksheet>'
        fixture(self.path, sheet=sheet)
        with self.assertRaisesRegex(ValueError, "标记超过"):
            read_sheet_view(self.path, "待确认")

    def test_strings_not_referenced_are_not_retained(self):
        wanted = {0, 130}
        handler = sheet_view._Strings(wanted, SheetViewLimits())
        with ZipFile(self.path) as package:
            reader = sheet_view._Reader(package, SheetViewLimits())
            reader.parse("xl/sharedStrings.xml", SheetViewLimits().shared_strings_bytes, handler)
        self.assertEqual(set(handler.result), wanted)
        self.assertEqual(handler.result[130], "中文销售额")

    def test_numeric_precision_and_unknown_format_are_not_guessed(self):
        self.assertEqual(sheet_view._number("9007199254740993"), "9007199254740993")
        self.assertIsNone(sheet_view._date_value(45292, {"id": 164, "code": '0.00 "days"'}, False))
        self.assertIsNone(sheet_view._date_value(45292, {"id": 164, "code": 'USD0.00'}, False))
        self.assertEqual(sheet_view._display(45292, '45292', {"id": 164, "code": 'USD0.00'}, False), '45292')

    def test_iso_date_type_and_missing_formula_cache(self):
        sheet = f'<worksheet xmlns="{NS}"><sheetData><row r="1"><c r="A1" t="d"><v>2024-01-02T12:00:00Z</v></c><c r="B1"><f>1+2</f></c></row></sheetData></worksheet>'
        fixture(self.path, sheet=sheet)
        result = read_sheet_view(self.path, "待确认")
        self.assertEqual(result["cells"][0]["date_value"], "2024-01-02T12:00:00+00:00")
        self.assertEqual(result["cells"][1]["display"], "=1+2")
        self.assertIsNone(result["cells"][1]["value"])
        self.assertIn("缺少缓存结果", result["warnings"][1])

    def test_actual_custom_theme_tint_and_namespace_attributes(self):
        with ZipFile(self.path) as package:
            styles = package.read("xl/styles.xml").decode().replace('rgb="FFB91C1C"', 'theme="4" tint="0.5"').replace('rgb="FFFFEEEE"', 'theme="1"')
        theme = '''<a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:themeElements><a:clrScheme name="Custom">
          <a:dk1><a:sysClr val="windowText" lastClr="112233"/></a:dk1><a:lt1><a:srgbClr val="FEDCBA"/></a:lt1>
          <a:accent1><a:srgbClr val="000000"/></a:accent1></a:clrScheme></a:themeElements></a:theme>'''
        fixture(self.path, styles=styles, theme=theme)
        style = read_sheet_view(self.path, "待确认")["styles"]["3"]
        self.assertEqual(style["font"]["color"], {"theme": "4", "tint": "0.5", "rgb": "FF808080"})
        self.assertEqual(style["fill"]["color"], {"theme": "1", "rgb": "FF112233"})
        # Negative and positive tints operate on actual supplied RGB/HLS.
        element = sheet_view.ET.fromstring('<color rgb="FFAA2200" tint="-1"/>')
        self.assertEqual(sheet_view._color(element, {}, [], [])["rgb"], "FF000000")
        element.set("tint", "1")
        self.assertEqual(sheet_view._color(element, {}, [], [])["rgb"], "FFFFFFFF")

    def test_standard_and_custom_indexed_palette_and_unresolved_system_colors(self):
        with ZipFile(self.path) as package:
            original = package.read("xl/styles.xml").decode()
        styles = original.replace('rgb="FFB91C1C"', 'indexed="2"')
        fixture(self.path, styles=styles)
        self.assertEqual(read_sheet_view(self.path, "待确认")["styles"]["3"]["font"]["color"], {"indexed": "2", "rgb": "FFFF0000"})
        styles = styles.replace('</styleSheet>', '<colors><indexedColors><rgbColor rgb="000000"/><rgbColor rgb="FFFFFF"/><rgbColor rgb="123456"/></indexedColors></colors></styleSheet>')
        fixture(self.path, styles=styles)
        self.assertEqual(read_sheet_view(self.path, "待确认")["styles"]["3"]["font"]["color"]["rgb"], "FF123456")
        fixture(self.path, styles=original.replace('rgb="FFB91C1C"', 'indexed="64"'))
        result = read_sheet_view(self.path, "待确认")
        self.assertEqual(result["styles"]["3"]["font"]["color"], {"indexed": "64"})
        self.assertIn("无法解析", result["warnings"][-1])

    def test_negative_style_id_and_whole_response_byte_cap(self):
        with ZipFile(self.path) as package:
            sheet = package.read("xl/worksheets/sheet1.xml").decode().replace('s="3"', 's="-1"')
        fixture(self.path, sheet=sheet)
        with self.assertRaisesRegex(ValueError, "样式编号"):
            read_sheet_view(self.path, "待确认")
        fixture(self.path)
        result = read_sheet_view(self.path, "待确认")
        size = len(json.dumps(result, ensure_ascii=False, allow_nan=False).encode('utf-8'))
        with self.assertRaisesRegex(ValueError, "完整响应"):
            read_sheet_view(self.path, "待确认", limits=replace(SheetViewLimits(), output_text_bytes=size - 1))

    def test_serial_zero_is_not_guessed_and_theme_size_is_capped(self):
        self.assertIsNone(sheet_view._date_value(0, {"id": 14, "code": "mm-dd-yy"}, False))
        self.assertEqual(sheet_view._date_value(0, {"id": 14, "code": "mm-dd-yy"}, True), "1904-01-01")
        fixture(self.path, theme='<theme>' + (' ' * (256 * 1024)) + '</theme>')
        with self.assertRaisesRegex(ValueError, "解压后超过"):
            read_sheet_view(self.path, "待确认")


if __name__ == "__main__":
    unittest.main()
