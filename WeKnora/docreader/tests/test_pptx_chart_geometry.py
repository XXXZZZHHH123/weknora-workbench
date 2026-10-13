import struct
import unittest

from docreader.parser.pptx_chart_geometry import estimate_emf_line_chart


def record(kind, body=b""):
    size = (8+len(body)+3)//4*4
    return struct.pack("<II", kind, size) + body + b"\0"*(size-8-len(body))


def pen(handle, color):
    # EXTCREATEPEN: handle/offBmi/cbBmi/offBits/cbBits followed by EXTLOGPEN.
    return record(95, struct.pack("<IIIIIIIIIIII", handle, 56, 0, 56, 0,
                                  0x10000, 2, 0, color, 0, 0, 0))


def select(handle):
    return record(37, struct.pack("<I", handle))


def polyline(points):
    xs, ys = zip(*points)
    body = struct.pack("<iiiiI", min(xs), min(ys), max(xs), max(ys), len(points))
    return record(87, body+b"".join(struct.pack("<hh", x, y) for x, y in points))


def text(value, x, y):
    encoded = value.encode("utf-16-le")
    count = len(encoded)//2
    advance = (76+len(encoded)+3)//4*4
    size = advance+count*4
    raw = bytearray(size)
    struct.pack_into("<II", raw, 0, 84, size)
    struct.pack_into("<iiIII", raw, 36, x, y, count, 76, 0)
    struct.pack_into("<I", raw, 72, advance)
    raw[76:76+len(encoded)] = encoded
    struct.pack_into("<"+"i"*count, raw, advance, *([5]*count))
    return bytes(raw)


def chart(*, nonlinear=False, ambiguous_legend=False, secondary_axis=False, unsupported=False,
          legend_names=None, short_curve=False):
    items = [record(22, struct.pack("<I", 24)), pen(1, 0x999999), select(1)]
    for y in (100, 200, 300):
        items.append(polyline([(100, y), (400, y)]))
    labels = ("1000", "100", "0") if nonlinear else ("100", "50", "0")
    for value, y in zip(labels, (106, 206, 306)):
        items.append(text(value, 55, y))
        if secondary_axis:
            items.append(text(value, 420, y))
    # Different colors and generic names: no commodity/color/year assumptions.
    names = legend_names or ("Alpha", "Beta")
    series = [(names[0], 0x00AA00, [150]*7+[140]), (names[1], 0xAA00AA, [250]*7+[240])]
    for index, (label, color, ys) in enumerate(series, 2):
        items += [pen(index, color), select(index),
                  polyline(list(zip((100, 140, 180, 220, 260, 300, 350, 400), ys)))]
        legend_y = 340+(index-2)*25
        items += [polyline([(100, legend_y), (130, legend_y)]), text(label, 135, legend_y+6)]
        if ambiguous_legend and index == 2:
            items += [polyline([(250, legend_y), (280, legend_y)]), text("Other", 285, legend_y+6)]
        if unsupported and index == 2:
            items.append(record(85, struct.pack("<iiiiI", 100, 100, 400, 300, 0)))
    if short_curve:
        items += [pen(4, 0x0000AA), select(4), polyline([(100, 190), (150, 180), (175, 180)]),
                  polyline([(100, 390), (130, 390)]), text("Gamma", 135, 396)]
    items.append(text("(kg)", 100, 80))
    items.append(record(14, struct.pack("<III", 0, 0, 20)))
    header = bytearray(88)
    struct.pack_into("<II", header, 0, 1, 88)
    header[40:44] = b" EMF"
    total = len(header)+sum(len(item) for item in items)
    struct.pack_into("<II", header, 48, total, len(items)+1)
    return bytes(header)+b"".join(items)


class TestPptxChartGeometry(unittest.TestCase):
    def test_estimate_uses_grid_calibration_color_legend_and_actual_last_point(self):
        result = estimate_emf_line_chart(chart())
        self.assertIsNotNone(result)
        self.assertEqual(result["unit"], "kg")
        self.assertEqual(result["axis_max_fit_residual"], 0)
        series = {item["legend"]: item for item in result["series"]}
        self.assertEqual(series["Alpha"]["color"], "#00AA00")
        self.assertEqual(series["Alpha"]["approximate_endpoint"], 80)
        self.assertEqual(series["Beta"]["approximate_endpoint"], 30)
        self.assertEqual(series["Alpha"]["endpoint_rank"], 1)
        self.assertEqual(series["Beta"]["endpoint_rank"], 2)
        self.assertLess(series["Alpha"]["estimate_low"], 80)
        self.assertGreater(series["Alpha"]["estimate_high"], 80)
        self.assertIn("not original Excel", result["comparison_basis"])
        self.assertEqual(result["series_count"], 2)
        self.assertEqual(result["other_series_count"], 1)
        self.assertIn("not a same-date", result["comparison_basis"])

    def test_yymm_hint_does_not_invent_century_or_crop_statistical_year(self):
        result = estimate_emf_line_chart(chart(legend_names=("2510", "2610")))
        hint = result["series"][0]["contract_code_hint"]
        self.assertEqual(hint["delivery_year_suffix"], "25")
        self.assertEqual(hint["delivery_month"], 10)
        self.assertIsNone(hint["full_delivery_year"])
        self.assertIsNone(hint["commodity_statistical_year"])
        self.assertIn("Only if", hint["interpretation"])
        for name in ("Alpha", "2599"):
            result = estimate_emf_line_chart(chart(legend_names=(name, "Beta")))
            self.assertIsNone(result["series"][0]["contract_code_hint"])

    def test_short_current_curve_cannot_disappear_from_total_or_comparison(self):
        self.assertIsNone(estimate_emf_line_chart(chart(short_curve=True)))

    def test_nonlinear_axis_is_not_approximated_as_linear(self):
        self.assertIsNone(estimate_emf_line_chart(chart(nonlinear=True)))

    def test_ambiguous_same_color_legend_fails_closed(self):
        self.assertIsNone(estimate_emf_line_chart(chart(ambiguous_legend=True)))

    def test_secondary_y_axis_fails_closed(self):
        self.assertIsNone(estimate_emf_line_chart(chart(secondary_axis=True)))

    def test_unsupported_curve_cannot_be_omitted_from_comparison(self):
        self.assertIsNone(estimate_emf_line_chart(chart(unsupported=True)))

    def test_malformed_records_and_point_counts_fail_closed(self):
        self.assertIsNone(estimate_emf_line_chart(b"not an EMF"))
        self.assertIsNone(estimate_emf_line_chart(chart()[:-10]))
        broken = bytearray(chart())
        offset = 88
        while offset+8 < len(broken):
            kind, size = struct.unpack_from("<II", broken, offset)
            if kind == 87:
                struct.pack_into("<I", broken, offset+24, 1_000_000)
                break
            offset += size
        self.assertIsNone(estimate_emf_line_chart(bytes(broken)))


if __name__ == "__main__":
    unittest.main()
