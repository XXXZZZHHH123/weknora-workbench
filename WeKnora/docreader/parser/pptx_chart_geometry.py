"""Conservative, source-based endpoint estimates for cached EMF line charts.

Not a renderer or an Excel reader. Only unambiguous single linear-axis charts
with explicit pen colors, horizontal grid lines and matching legend swatches
are supported. Unsupported/ambiguous geometry returns no estimate.
"""
from __future__ import annotations

import math
import re
import statistics
import struct

_IDENTITY = (1., 0., 0., 1., 0., 0.)
_MAX_BYTES = 16 * 1024 * 1024
_MAX_POINTS = 50_000


def _compose(first, second):
    a, b, c, d, x, y = first
    e, f, g, h, u, v = second
    return (a*e+b*g, a*f+b*h, c*e+d*g, c*f+d*h, x*e+y*g+u, x*f+y*h+v)


def _point(state, x, y):
    matrix = state["matrix"]
    if matrix is None or state["map_mode"] not in (1, 7, 8):
        return None
    a, b, c, d, dx, dy = matrix
    x, y = x*a+y*c+dx, x*b+y*d+dy
    wx, wy = state["window_origin"]
    vx, vy = state["viewport_origin"]
    sx, sy = (1., 1.)
    if state["map_mode"] in (7, 8):
        we, ve = state["window_ext"], state["viewport_ext"]
        if not we[0] or not we[1]:
            return None
        sx, sy = ve[0]/we[0], ve[1]/we[1]
    result = ((x-wx)*sx+vx, (y-wy)*sy+vy)
    return result if all(math.isfinite(v) and abs(v) < 1e8 for v in result) else None


def _color(value):
    if value > 0xFFFFFF:  # Palette-index colors are not explicit RGB.
        return None
    return "#%02X%02X%02X" % (value & 255, value >> 8 & 255, value >> 16 & 255)


def _legend_code_hint(label: str) -> dict | None:
    # A four-digit numeric legend can also be an ordinary year. This is only
    # a conditional YYMM reading, never proof of a futures code or its century.
    if not re.fullmatch(r"\d{4}", label) or not 1 <= int(label[2:]) <= 12:
        return None
    return {"possible_format": "YYMM", "delivery_year_suffix": label[:2],
            "delivery_month": int(label[2:]), "full_delivery_year": None,
            "commodity_statistical_year": None,
            "interpretation": "Only if the report identifies this as a futures YYMM contract code. "
                              "YY is delivery-year suffix and MM is delivery month. "
                              "The century and any commodity/crop statistical year require explicit report context."}


def _primitives(data):
    if len(data) < 88 or len(data) > _MAX_BYTES or data[40:44] != b" EMF":
        return None
    if struct.unpack_from("<I", data)[0] != 1:
        return None
    state = {"matrix": _IDENTITY, "map_mode": 1,
             "window_origin": (0, 0), "viewport_origin": (0, 0),
             "window_ext": (1, 1), "viewport_ext": (1, 1),
             "pen": {"color": "#000000", "width": 1, "geometric": False}, "align": 0}
    saved, pens, lines, texts, unsupported = [], {}, [], [], set()
    offset, records, total_points = 0, 0, 0

    def add_line(raw_points, bounds):
        nonlocal total_points
        total_points += len(raw_points)
        if total_points > _MAX_POINTS:
            raise ValueError("EMF point limit")
        pen = state["pen"]
        if not pen or not pen["color"]:
            return
        points = [_point(state, *point) for point in raw_points]
        if any(point is None for point in points) or not points:
            return
        matrix = state["matrix"]
        scale = max(abs(matrix[0]), abs(matrix[3])) if pen["geometric"] else 1
        width = max(1., pen["width"] * scale)
        # Bounds are in device coordinates; verify our supported transformation.
        tolerance = max(5., width+3)
        if any(not bounds[0]-tolerance <= x <= bounds[2]+tolerance or
               not bounds[1]-tolerance <= y <= bounds[3]+tolerance for x, y in points):
            unsupported.add(pen["color"])
            return
        lines.append({"points": points, "color": pen["color"], "width": width})

    try:
        while offset+8 <= len(data) and records < 100_000:
            kind, size = struct.unpack_from("<II", data, offset)
            if size < 8 or size % 4 or offset+size > len(data):
                return None
            record = memoryview(data)[offset:offset+size]
            if kind == 33:  # SaveDC
                saved.append(dict(state))
            elif kind == 34 and size >= 12:
                level = struct.unpack_from("<i", record, 8)[0]
                if level >= 0 or -level > len(saved):
                    return None
                state = saved[level]
                del saved[level:]
            elif kind == 35 and size >= 32:
                state["matrix"] = struct.unpack_from("<ffffff", record, 8)
            elif kind == 36 and size >= 36:
                update = struct.unpack_from("<ffffff", record, 8)
                mode = struct.unpack_from("<I", record, 32)[0]
                if mode == 1:
                    state["matrix"] = _IDENTITY
                elif mode == 4:
                    state["matrix"] = update
                elif state["matrix"] is not None and mode in (2, 3):
                    state["matrix"] = _compose(update, state["matrix"]) if mode == 2 else _compose(state["matrix"], update)
                else:
                    state["matrix"] = None
            elif kind in (9, 10, 11, 12) and size >= 16:
                key = {9: "window_ext", 10: "window_origin", 11: "viewport_ext", 12: "viewport_origin"}[kind]
                state[key] = struct.unpack_from("<ii", record, 8)
            elif kind == 17 and size >= 12:
                state["map_mode"] = struct.unpack_from("<I", record, 8)[0]
            elif kind in (31, 32, 115):  # Unsupported mapping/layout changes.
                return None
            elif kind == 22 and size >= 12:
                state["align"] = struct.unpack_from("<I", record, 8)[0]
            elif kind in (38, 95):
                if kind == 95 and size >= 52:
                    handle = struct.unpack_from("<I", record, 8)[0]
                    style, width, brush, color = struct.unpack_from("<IIII", record, 28)
                elif kind == 38 and size >= 28:
                    handle, style, width, _, color = struct.unpack_from("<IIiiI", record, 8)
                    brush = 0
                else:
                    return None
                pens[handle] = {"color": _color(color) if brush == 0 and style & 15 != 5 else None,
                                "width": abs(width), "geometric": bool(style & 0x10000)}
            elif kind == 40 and size >= 12:  # DeleteObject
                pens.pop(struct.unpack_from("<I", record, 8)[0], None)
            elif kind in (39, 82) and size >= 12:  # Brush/font handle cannot be a pen.
                pens.pop(struct.unpack_from("<I", record, 8)[0], None)
            elif kind == 37 and size >= 12:
                handle = struct.unpack_from("<I", record, 8)[0]
                if handle in pens:
                    state["pen"] = pens[handle]
                elif handle in (0x80000006, 0x80000007, 0x80000008):
                    state["pen"] = {"color": {0x80000006: "#FFFFFF", 0x80000007: "#000000", 0x80000008: None}[handle],
                                    "width": 1, "geometric": False}
            elif kind in (4, 87) and size >= 28:
                bounds = struct.unpack_from("<iiii", record, 8)
                count = struct.unpack_from("<I", record, 24)[0]
                stride, fmt = (4, "<hh") if kind == 87 else (8, "<ii")
                if count > _MAX_POINTS or 28+count*stride > size:
                    return None
                add_line([struct.unpack_from(fmt, record, 28+j*stride) for j in range(count)], bounds)
            elif kind in (7, 90) and size >= 32:
                bounds = struct.unpack_from("<iiii", record, 8)
                polys, count = struct.unpack_from("<II", record, 24)
                stride, fmt = (4, "<hh") if kind == 90 else (8, "<ii")
                start = 32+4*polys
                if polys > _MAX_POINTS or count > _MAX_POINTS or start+count*stride > size:
                    return None
                counts = struct.unpack_from("<"+"I"*polys, record, 32)
                if sum(counts) != count:
                    return None
                cursor = 0
                for length in counts:
                    add_line([struct.unpack_from(fmt, record, start+(cursor+j)*stride) for j in range(length)], bounds)
                    cursor += length
            elif kind in (2, 5, 6, 85, 88, 89):  # Beziers/continuation paths not guessed.
                if state["pen"] and state["pen"]["color"]:
                    unsupported.add(state["pen"]["color"])
            elif kind == 84 and size >= 76:
                x, y, chars, string_offset, flags = struct.unpack_from("<iiIII", record, 36)
                advance_offset = struct.unpack_from("<I", record, 72)[0]
                if chars > 4096 or string_offset < 76 or string_offset+chars*2 > size or flags & (0x10 | 0x2000):
                    offset += size
                    records += 1
                    continue
                text = bytes(record[string_offset:string_offset+chars*2]).decode("utf-16-le")
                text = re.sub(r"[\x00-\x1f\x7f]", " ", text).strip()
                point = _point(state, x, y)
                end = None
                if advance_offset >= 76 and advance_offset+chars*4 <= size:
                    advance = sum(struct.unpack_from("<"+"i"*chars, record, advance_offset))
                    end = _point(state, x+advance, y)
                if text and point and end and state["align"] & 6 == 0:  # Left-aligned only.
                    entry = {"text": text, "x": point[0], "y": point[1], "end": end[0], "align": state["align"]}
                    if texts and abs(texts[-1]["end"]-entry["x"]) < 1 and abs(texts[-1]["y"]-entry["y"]) < 1 and texts[-1]["align"] == entry["align"]:
                        texts[-1]["text"] += text
                        texts[-1]["end"] = end[0]
                    elif len(texts) < 4000:
                        texts.append(entry)
            offset += size
            records += 1
    except (ValueError, struct.error, UnicodeDecodeError, OverflowError):
        return None
    if offset != len(data):
        return None
    return lines, texts, unsupported


def estimate_emf_line_chart(data: bytes) -> dict | None:
    """Return geometric evidence only if axis, legend and traces all verify."""
    primitives = _primitives(data)
    if not primitives:
        return None
    lines, texts, unsupported = primitives
    if unsupported:
        # Do not rank only the supported subset while silently omitting a
        # Bezier/continuation curve from the same graphic.
        return None
    candidates = []
    for line in lines:
        points = line["points"]
        if len(points) < 8 or line["color"] in unsupported:
            continue
        if any(points[i][0] < points[i-1][0]-.5 for i in range(1, len(points))):
            continue
        span = points[-1][0]-points[0][0]
        if span > 0:
            candidates.append((span, line))
    if len(candidates) < 2 or len(candidates) > 32:
        return None
    max_span = max(span for span, _ in candidates)
    traces = [line for span, line in candidates if span >= max_span*.8]
    if len(traces) != len(candidates) or len(traces) < 2 or len({line["color"] for line in traces}) != len(traces):
        return None
    left = min(line["points"][0][0] for line in traces)
    right = max(line["points"][-1][0] for line in traces)
    grid = []
    for line in lines:
        points = line["points"]
        if len(points) == 2 and abs(points[0][1]-points[1][1]) < .5:
            x0, x1 = sorted((points[0][0], points[1][0]))
            if x1-x0 >= max_span*.8 and x0 <= left+2 and x1 >= right-2:
                grid.append((x0, x1, points[0][1]))
    if len(grid) < 3 or max(x[0] for x in grid)-min(x[0] for x in grid) > 3 or max(x[1] for x in grid)-min(x[1] for x in grid) > 3:
        return None
    plot_left, plot_right = statistics.median(g[0] for g in grid), statistics.median(g[1] for g in grid)
    ys = []
    for y in sorted(g[2] for g in grid):
        if not ys or y-ys[-1] > 1:
            ys.append(y)
    if len(ys) < 3 or len(ys) > 100:
        return None
    spacing = min(ys[i]-ys[i-1] for i in range(1, len(ys)))
    height, width = ys[-1]-ys[0], plot_right-plot_left
    if spacing <= 2 or height <= 0 or width <= 0:
        return None
    numeric = []
    for text in texts:
        if re.fullmatch(r"[-+]?\d+(?:,\d{3})*(?:\.\d+)?", text["text"]):
            value = float(text["text"].replace(",", ""))
            if math.isfinite(value):
                nearest = min(range(len(ys)), key=lambda i: abs(ys[i]-text["y"]))
                if abs(ys[nearest]-text["y"]) <= spacing*.4:
                    numeric.append((text, nearest, value))
    # A second numeric scale on the right is not silently treated as the first.
    if len({i for text, i, _ in numeric if plot_right < text["x"] < plot_right+width*.3}) >= 3:
        return None
    ticks = {}
    offsets = []
    for text, index, value in numeric:
        if plot_left-width*.35 < text["x"] and text["end"] < plot_left-3:
            if index in ticks and ticks[index] != value:
                return None
            ticks[index] = value
            offsets.append(text["y"]-ys[index])
    if len(ticks) < max(3, math.ceil(len(ys)*.8)):
        return None
    pairs = sorted((ys[i], value) for i, value in ticks.items())
    if pairs[-1][0]-pairs[0][0] < height*.6:
        return None
    values = [value for _, value in pairs]
    if any(values[i] >= values[i-1] for i in range(1, len(values))):
        return None
    center_y, center_v = statistics.mean(y for y, _ in pairs), statistics.mean(values)
    slope = sum((y-center_y)*(v-center_v) for y, v in pairs) / sum((y-center_y)**2 for y, _ in pairs)
    intercept = center_v-slope*center_y
    tick_step = min(abs(values[i]-values[i-1]) for i in range(1, len(values)))
    residual = max(abs(slope*y+intercept-v) for y, v in pairs)
    baseline = statistics.median(offsets)
    if slope >= 0 or residual > tick_step*.02 or max(offsets)-min(offsets) > spacing*.15:
        return None
    unit_strings = {text["text"][1:-1] for text in texts
                    if re.fullmatch(r"[（(][^()（）]{1,20}[）)]", text["text"])}
    unit = next(iter(unit_strings)) if len(unit_strings) == 1 else ""
    legend_labels = {}
    for line in lines:
        pts = line["points"]
        if len(pts) != 2 or abs(pts[0][1]-pts[1][1]) > .5:
            continue
        x0, x1 = sorted((pts[0][0], pts[1][0]))
        y = pts[0][1]
        if not width*.01 < x1-x0 < width*.2 or ys[0]-3 <= y <= ys[-1]+3:
            continue
        for text in texts:
            if 0 <= text["x"]-x1 <= width*.08 and abs(text["y"]-y-baseline) <= max(3, spacing*.1):
                legend_labels.setdefault(line["color"], set()).add(text["text"])
    # A new/short curve must not disappear from the denominator merely because
    # it has fewer than eight points or a shorter span than historical traces.
    if set(legend_labels) != {trace["color"] for trace in traces}:
        return None
    series = []
    for trace in traces:
        points = trace["points"]
        if points[-1][0] < plot_right-width*.05 or any(y < ys[0]-2 or y > ys[-1]+2 for _, y in points):
            return None
        labels = legend_labels[trace["color"]]
        if len(labels) != 1:
            return None
        label = next(iter(labels))
        raw_value = slope*points[-1][1]+intercept
        resolution = 10**math.floor(math.log10(tick_step/20))
        approx = round(raw_value/resolution)*resolution
        uncertainty = residual + abs(slope)*(trace["width"]/2+.5)
        margin = max(resolution, math.ceil(uncertainty/resolution)*resolution)
        series.append({"legend": label, "color": trace["color"], "contract_code_hint": _legend_code_hint(label),
                       "approximate_endpoint": approx,
                       "estimate_low": approx-margin, "estimate_high": approx+margin,
                       "endpoint_x": round(points[-1][0], 3), "endpoint_y": round(points[-1][1], 3)})
    if len({item["legend"] for item in series}) != len(series):
        return None
    order = sorted(series, key=lambda item: item["approximate_endpoint"], reverse=True)
    for item in series:
        item["endpoint_rank"] = 1 + sum(other["approximate_endpoint"] > item["approximate_endpoint"] for other in order)
        others = [other for other in series if other is not item]
        item["endpoint_rank_range"] = [
            1 + sum(other["estimate_low"] > item["estimate_high"] for other in others),
            1 + sum(other["estimate_high"] >= item["estimate_low"] for other in others),
        ]
    return {"method": "original_emf_polyline_and_linear_axis", "unit": unit,
            "axis_tick_min": min(values), "axis_tick_max": max(values),
            "axis_max_fit_residual": round(residual, 6),
            "series_count": len(series), "other_series_count": len(series)-1,
            "series": series,
            "comparison_basis": "Each curve's own drawn right endpoint; dates may differ. "
                                "This is not a same-date cross-section or ranking. "
                                "Values are geometric estimates, not original Excel observations.",
            "legend_interpretation": "Copy original legend identifiers. A possible YYMM code identifies only "
                                     "delivery year/month when confirmed by report context; it does not define "
                                     "a commodity/crop statistical year. Total curves include the selected curve; "
                                     "comparisons with other curves exclude it."}
