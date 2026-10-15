"""Slide-scoped PPTX image extraction, including cached OLE chart previews.

Only internal image relationships are read. An OLE link to an author's Excel
workbook must never be opened or refreshed while indexing.
"""
from __future__ import annotations

import base64
import io
import logging
import os
import posixpath
import re
import shutil
import signal
import struct
import subprocess
import tempfile
import time
import uuid
import zipfile
from pathlib import Path
from typing import Dict, List, Tuple
from xml.etree import ElementTree as ET

from docreader.parser.excel_convert import find_soffice
from docreader.parser.pptx_chart_geometry import estimate_emf_line_chart

logger = logging.getLogger(__name__)
_MARKDOWN_IMAGE = re.compile(r"!\[(.*?)\]\(([^)]+)\)", re.DOTALL)
_SLIDE_MARKER = re.compile(r"<!--\s*Slide number:\s*(\d+)\s*-->")
_RASTER_EXT = {".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp"}
_VECTOR_EXT = {".wmf", ".emf", ".svg"}
_MAX_MEDIA_BYTES = 16 * 1024 * 1024
_MAX_XML_BYTES = 8 * 1024 * 1024
_MAX_PNG_BYTES = 32 * 1024 * 1024
_MAX_PIXELS = 40_000_000
_MEDIA_BUDGET_SECONDS = 180
_OFFICE_PNG_FILTER = (
    'png:draw_png_Export:{"PixelWidth":{"type":"long","value":"2048"},'
    '"PixelHeight":{"type":"long","value":"2896"}}'
)
_NS = {
    "p": "http://schemas.openxmlformats.org/presentationml/2006/main",
    "a": "http://schemas.openxmlformats.org/drawingml/2006/main",
    "r": "http://schemas.openxmlformats.org/officeDocument/2006/relationships",
    "rel": "http://schemas.openxmlformats.org/package/2006/relationships",
}


def _run_conversion(command: list[str], timeout: float) -> bool:
    # soffice can start a child: kill the entire process group on timeout.
    # The Linux docreader image provides prlimit. Apply limits before soffice
    # starts (and inherit them in its children), without unsafe preexec_fn in
    # the threaded gRPC server. Other hosts still have input/time/pixel limits.
    prlimit = shutil.which("prlimit")
    if prlimit:
        command = [prlimit, "--as=1610612736", "--cpu=" + str(max(1, int(timeout) + 1)),
                   "--fsize=" + str(_MAX_PNG_BYTES), "--"] + command
    proc = subprocess.Popen(command, stdout=subprocess.DEVNULL,
                            stderr=subprocess.DEVNULL, start_new_session=True)
    try:
        return proc.wait(timeout=max(0.1, timeout)) == 0
    except subprocess.TimeoutExpired:
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        proc.wait()
        logger.warning("PPT image conversion timed out")
        return False


def _rasterize_with_pillow(data: bytes, *, trim_canvas: bool = False) -> bytes | None:
    try:
        from PIL import Image
        with Image.open(io.BytesIO(data)) as img:
            if img.width * img.height > _MAX_PIXELS:
                return None
            img.load()
            # Preserve alpha and enough resolution for chart legends.
            img = img.convert("RGBA" if "A" in img.getbands() or "transparency" in img.info else "RGB")
            if trim_canvas:
                from PIL import ImageChops
                bounds = ImageChops.difference(img.convert("RGB"), Image.new("RGB", img.size, "white")).getbbox()
                if not bounds:
                    return None
                left, top, right, bottom = bounds
                img = img.crop((max(0, left - 8), max(0, top - 8),
                                min(img.width, right + 8), min(img.height, bottom + 8)))
            img.thumbnail((4096, 4096))
            out = io.BytesIO()
            img.save(out, format="PNG")
            result = out.getvalue()
            return result if len(result) <= _MAX_PNG_BYTES else None
    except Exception as exc:
        logger.debug("Pillow could not open PPT media: %s", exc)
        return None


def _read_png(path: str, *, trim_canvas: bool = False) -> bytes | None:
    if not os.path.isfile(path) or os.path.getsize(path) > _MAX_PNG_BYTES:
        return None
    return _rasterize_with_pillow(Path(path).read_bytes(), trim_canvas=trim_canvas)


def _rasterize_with_imagemagick(data: bytes, suffix: str, timeout: float = 20) -> bytes | None:
    convert = shutil.which("convert")
    if not convert:
        return None
    with tempfile.TemporaryDirectory() as temp_dir:
        src = os.path.join(temp_dir, "input" + suffix)
        dst = os.path.join(temp_dir, "output.png")
        Path(src).write_bytes(data)
        try:
            ok = _run_conversion([convert, "-limit", "memory", "256MiB", "-limit", "map", "256MiB",
                                  "-limit", "disk", "512MiB", src + "[0]", dst], timeout)
            return _read_png(dst) if ok else None
        except OSError as exc:
            logger.warning("PPT ImageMagick conversion failed: %s", exc)
            return None


def _rasterize_with_libreoffice(data: bytes, suffix: str, timeout: float = 30) -> bytes | None:
    soffice = find_soffice()
    if not soffice:
        return None
    # Convert the standalone cache, never the presentation or linked workbook.
    with tempfile.TemporaryDirectory() as temp_dir, tempfile.TemporaryDirectory() as profile:
        src = os.path.join(temp_dir, "input" + suffix)
        Path(src).write_bytes(data)
        try:
            ok = _run_conversion([soffice, "-env:UserInstallation=" + Path(profile).as_uri(),
                                  "--headless", "--convert-to", _OFFICE_PNG_FILTER,
                                  "--outdir", temp_dir, src], timeout)
            return _read_png(os.path.join(temp_dir, "input.png"), trim_canvas=True) if ok else None
        except OSError as exc:
            logger.warning("PPT LibreOffice image conversion failed: %s", exc)
            return None


def rasterize_media_bytes(name: str, data: bytes, *, timeout: float = 30) -> bytes | None:
    ext = posixpath.splitext(name)[1].lower()
    if not data or len(data) > _MAX_MEDIA_BYTES:
        return None
    if ext in _RASTER_EXT:
        return _rasterize_with_pillow(data)
    if ext not in _VECTOR_EXT:
        return None
    # LibreOffice handles Office EMF/WMF caches where ImageMagick lacks a decoder.
    if ext in {".emf", ".wmf"}:
        started = time.monotonic()
        png = _rasterize_with_libreoffice(data, ext, timeout=timeout)
        if png:
            return png
        timeout -= time.monotonic() - started
        if timeout <= 0:
            return None
    if ext == ".svg":
        # No external resources/scripts may be fetched by a vector renderer.
        try:
            root = ET.fromstring(data)
            for element in root.iter():
                if element.tag.rsplit("}", 1)[-1] in {"script", "foreignObject", "style"}:
                    return None
                for key, value in element.attrib.items():
                    if key.rsplit("}", 1)[-1] in {"href", "src"} and not value.startswith("#"):
                        return None
                    if "url(" in value.lower() and not re.fullmatch(r"url\(#[^)]+\)", value):
                        return None
        except ET.ParseError:
            return None
    return _rasterize_with_imagemagick(data, ext, timeout=min(20, timeout))


def _read_member(archive: zipfile.ZipFile, name: str, limit: int) -> bytes:
    info = archive.getinfo(name)
    if info.file_size > limit:
        raise ValueError("PPTX part exceeds extraction limit: " + name)
    return archive.read(info)


def list_pptx_media(pptx_bytes: bytes) -> List[Tuple[str, bytes]]:
    """Legacy utility; not used for matching images to slide references."""
    with zipfile.ZipFile(io.BytesIO(pptx_bytes)) as archive:
        return [(name, _read_member(archive, name, _MAX_MEDIA_BYTES))
                for name in archive.namelist()
                if name.startswith("ppt/media/") and not name.endswith("/")]


def extract_pptx_media_rasterized(pptx_bytes: bytes) -> List[bytes]:
    return [png for path, raw in list_pptx_media(pptx_bytes)
            if (png := rasterize_media_bytes(path, raw)) is not None]


def _relationships(archive: zipfile.ZipFile, part: str) -> dict[str, str]:
    directory, name = posixpath.split(part)
    rel_path = posixpath.join(directory, "_rels", name + ".rels")
    if rel_path not in archive.namelist():
        return {}
    root = ET.fromstring(_read_member(archive, rel_path, _MAX_XML_BYTES))
    targets = {}
    for rel in root:
        if rel.get("TargetMode", "").lower() == "external":
            continue
        target = rel.get("Target", "")
        if not target or ":" in target or "\\" in target or "#" in target or "?" in target:
            continue
        resolved = posixpath.normpath(target.lstrip("/") if target.startswith("/")
                                     else posixpath.join(directory, target))
        if resolved.startswith("../") or resolved not in archive.namelist():
            continue
        targets[rel.get("Id", "")] = resolved
    return targets


def _slide_parts(archive: zipfile.ZipFile) -> list[str]:
    # Neither package filename nor ZIP order necessarily equals display order.
    root = ET.fromstring(_read_member(archive, "ppt/presentation.xml", _MAX_XML_BYTES))
    relationships = _relationships(archive, "ppt/presentation.xml")
    slides = []
    for slide in root.findall("p:sldIdLst/p:sldId", _NS):
        part = relationships.get(slide.get("{" + _NS["r"] + "}id", ""))
        if not part or not part.startswith("ppt/slides/"):
            raise ValueError("PPTX slide relationship cannot be resolved")
        slides.append(part)
    if len(slides) > 1000:
        raise ValueError("PPTX exceeds 1000 slide extraction limit")
    return slides


def _slide_pictures(archive: zipfile.ZipFile, part: str) -> list[tuple[str, str, str]]:
    root = ET.fromstring(_read_member(archive, part, _MAX_XML_BYTES))
    relationships = _relationships(archive, part)
    pictures = []
    seen = set()
    def position(shape):
        transform = shape.find("p:spPr/a:xfrm", _NS)
        if transform is None:
            transform = shape.find("p:grpSpPr/a:xfrm", _NS)
        if transform is None:
            transform = shape.find("p:xfrm", _NS)
        offset = transform.find("a:off", _NS) if transform is not None else None
        return (int(offset.get("y", "0")), int(offset.get("x", "0"))) if offset is not None else (0, 0)

    def visit(container):
        # Match MarkItDown's positional order, including nested groups; resolve
        # each placement by its own relationship, never a shared name or ZIP order.
        for shape in sorted(container, key=position):
            if shape.tag == "{" + _NS["p"] + "}pic":
                props = shape.find("p:nvPicPr/p:cNvPr", _NS)
                name = props.get("name", "") if props is not None else ""
                label = (props.get("descr") or name or "图像") if props is not None else "图像"
                placeholder = re.sub(r"\W", "", name) + ".jpg" if name else ""
                blip = shape.find("p:blipFill/a:blip", _NS)
                rid = blip.get("{" + _NS["r"] + "}embed", "") if blip is not None else ""
                target = relationships.get(rid, "")
                key = (props.get("id", "") if props is not None else label, rid)
                if key in seen:
                    continue  # Choice/Fallback can repeat the same cached picture.
                seen.add(key)
                pictures.append((label, target if target.startswith("ppt/media/") else "", placeholder))
            else:
                # Includes OLE cached previews, without opening the workbook or links.
                visit(shape)
    visit(root)
    return pictures


def extract_emf_labels(data: bytes) -> list[str]:
    """Extract original Unicode text, joining only physically adjacent runs.

    This is source transcription, not OCR or interpretation: axis ticks and
    legend strings must never be presented as measured observations. Unknown
    transforms, glyph-index text and malformed records are skipped safely.
    """
    if len(data) > _MAX_MEDIA_BYTES or len(data) < 88:
        return []
    # EMR_HEADER and EMF signature; do not treat arbitrary bytes as records.
    if struct.unpack_from("<I", data)[0] != 1 or data[40:44] != b" EMF":
        return []
    matrix = (1.0, 0.0, 0.0, 1.0, 0.0, 0.0)
    saved = []
    labels = []
    last_end = None
    offset = 0
    count = 0
    total_chars = 0
    while offset + 8 <= len(data) and count < 100_000 and total_chars < 16_000:
        kind, size = struct.unpack_from("<II", data, offset)
        if size < 8 or size % 4 or offset + size > len(data):
            break
        record = memoryview(data)[offset:offset + size]
        if kind == 33:  # EMR_SAVEDC
            saved.append(matrix)
        elif kind == 34 and size >= 12:  # EMR_RESTOREDC, normally relative -1.
            level = struct.unpack_from("<i", record, 8)[0]
            if level < 0 and -level <= len(saved):
                matrix = saved[level]
                del saved[level:]
            else:
                matrix = None
        elif kind == 35 and size >= 32:  # EMR_SETWORLDTRANSFORM
            matrix = struct.unpack_from("<ffffff", record, 8)
        elif kind == 36 and size >= 36:  # EMR_MODIFYWORLDTRANSFORM
            mode = struct.unpack_from("<I", record, 32)[0]
            update = struct.unpack_from("<ffffff", record, 8)
            if mode == 1:
                matrix = (1.0, 0.0, 0.0, 1.0, 0.0, 0.0)
            elif mode == 4:
                matrix = update
            elif matrix is not None and mode in {2, 3}:
                # Windows XFORM uses row vectors. MWT_LEFTMULTIPLY applies
                # the supplied matrix before the current transformation.
                first, second = (update, matrix) if mode == 2 else (matrix, update)
                a, b, c, d, x, y = first
                e, f, g, h, u, v = second
                matrix = (a * e + b * g, a * f + b * h,
                          c * e + d * g, c * f + d * h,
                          x * e + y * g + u, x * f + y * h + v)
            else:
                matrix = None
        elif kind == 84 and size >= 76:  # EMR_EXTTEXTOUTW
            x, y, chars, string_offset, flags = struct.unpack_from("<iiIII", record, 36)
            if chars <= 4096 and string_offset >= 76 and string_offset + chars * 2 <= size and not flags & 0x10:
                try:
                    text = bytes(record[string_offset:string_offset + chars * 2]).decode("utf-16-le")
                except UnicodeDecodeError:
                    text = ""
                text = re.sub(r"[\x00-\x1f\x7f]", " ", text).strip()
                if text:
                    current = None
                    end = None
                    # Joining is safe only with a simple horizontal transform
                    # and explicit advances, not guessed font/glyph widths.
                    advance_offset = struct.unpack_from("<I", record, 72)[0]
                    if matrix is not None and matrix[1] == 0 and matrix[2] == 0 and not flags & 0x2000:
                        current = (x * matrix[0] + matrix[4], y * matrix[3] + matrix[5])
                        if advance_offset >= 76 and advance_offset + chars * 4 <= size:
                            advance = sum(struct.unpack_from("<" + "i" * chars, record, advance_offset))
                            end = (current[0] + advance * matrix[0], current[1])
                    if current and last_end and abs(current[0] - last_end[0]) < 1 and abs(current[1] - last_end[1]) < 1:
                        labels[-1] += text
                    else:
                        labels.append(text)
                    total_chars += len(text)
                    last_end = end
        offset += size
        count += 1
    return labels


def _rasterize_vector_batches(archive: zipfile.ZipFile, targets: list[str], deadline: float) -> dict[str, bytes]:
    """Avoid one LibreOffice startup per chart in image-heavy reports.

    Eight standalone caches per isolated process keeps temporary disk/input
    bounded and still validates each output separately. Only referenced image
    parts are converted, never OLE workbooks or presentation links.
    """
    soffice = find_soffice()
    if not soffice:
        return {}
    results = {}
    for offset in range(0, len(targets), 8):
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        with tempfile.TemporaryDirectory() as temp_dir, tempfile.TemporaryDirectory() as profile:
            inputs = []
            parts = []
            for target in targets[offset:offset + 8]:
                try:
                    raw = _read_member(archive, target, _MAX_MEDIA_BYTES)
                except (KeyError, ValueError) as exc:
                    logger.warning("PPT cached image unavailable: %s", exc)
                    continue
                src = os.path.join(temp_dir, f"asset{len(inputs)}" + posixpath.splitext(target)[1])
                Path(src).write_bytes(raw)
                inputs.append(src)
                parts.append(target)
            if not inputs:
                continue
            try:
                _run_conversion([soffice, "-env:UserInstallation=" + Path(profile).as_uri(),
                                 "--headless", "--convert-to", _OFFICE_PNG_FILTER, "--outdir", temp_dir] + inputs,
                                min(30, remaining))
                for target, src in zip(parts, inputs):
                    png = _read_png(os.path.splitext(src)[0] + ".png", trim_canvas=True)
                    if png:
                        results[target] = png
            except OSError as exc:
                logger.warning("PPT cached image batch conversion failed: %s", exc)
    return results


def _is_unresolved_image_ref(url: str) -> bool:
    return bool(url) and not url.startswith(("data:", "images/", "http://", "https://"))


def _geometry_markdown(result: dict, page_number: int, target: str) -> str:
    def escape(value):
        return re.sub(r"([\\`*\[\]<>!#_|])", r"\\\1", str(value))

    unit = escape(result["unit"]) if result["unit"] else "原图纵轴单位"
    series = result["series"]
    lines = [
        f"第{page_number}页原图几何估读 · {target}：",
        "下表从原始缓存EMF的曲线颜色、图例线段、实际绘制端点及线性纵轴网格计算。"
        "这是图上近似读数，范围包含线宽与网格拟合误差；不是原Excel行情数值。"
        "比较的是各曲线自身绘制的最右端，结束日期可能不同，不能当作同日截面。",
        f"图中共{result['series_count']}条已核验曲线，包含当前被询问的曲线；每条曲线与另外{result['other_series_count']}条比较。"
        "这里的排序是各自端点估读排序，不能称为同日或同期排名。",
        "图例标识应保持原文。若报告确认四位数字是期货YYMM合约代码，YY只表示交割年尾码，MM表示交割月份，"
        "世纪应结合报告日期确认；合约标识或交割年不能自动改写成商品或作物的跨年统计年度。"
        "原图未明确提供产业统计年度时，该口径未被证明。",
        "",
        f"| 原图图例 | 条件性的YYMM读法（仅交割年/月） | 曲线RGB颜色 | 最右端约值（{unit}） | 估读范围（{unit}） | 自身端点估读位置（全图{len(series)}条，从高到低） |",
        "| --- | --- | --- | --- | --- | --- |",
    ]
    for item in series:
        rank_low, rank_high = item["endpoint_rank_range"]
        rank = str(rank_low) if rank_low == rank_high else f"约{item['endpoint_rank']}（可能{rank_low}～{rank_high}）"
        hint = item["contract_code_hint"]
        hint_text = (f"若为合约码：交割年尾码{hint['delivery_year_suffix']}、{hint['delivery_month']}月；产业年度未提供"
                     if hint else "原文标识，不推断产业年度")
        lines.append(f"| {escape(item['legend'])} | {hint_text} | {item['color']} | {item['approximate_endpoint']:g} | "
                     f"{item['estimate_low']:g}～{item['estimate_high']:g} | {rank} |")
    return "\n".join(lines)


def attach_pptx_media_to_markdown(markdown: str, pptx_bytes: bytes) -> Tuple[str, Dict[str, str]]:
    """Add page headings and pictures resolved by the owning slide relationships.

    Replace MarkItDown picture placeholders/data URIs per slide so no asset can
    be assigned by archive order. Preserve text, native chart tables and notes.
    Include cached OLE chart previews which MarkItDown omits entirely.
    """
    markers = list(_SLIDE_MARKER.finditer(markdown))
    images: Dict[str, str] = {}
    sections = [markdown[:markers[0].start()]] if markers and markers[0].start() else []
    cache: dict[str, str | None] = {}
    deadline = time.monotonic() + _MEDIA_BUDGET_SECONDS
    with zipfile.ZipFile(io.BytesIO(pptx_bytes)) as archive:
        parts = _slide_parts(archive)
        if len(markers) != len(parts) or [int(m.group(1)) for m in markers] != list(range(1, len(parts) + 1)):
            raise ValueError("PPTX page markers missing or inconsistent; refusing to assign images to guessed pages")
        pictures_by_page = [_slide_pictures(archive, part) for part in parts]
        vector_targets = list(dict.fromkeys(target for pictures in pictures_by_page
                                           for _, target, _ in pictures
                                           if posixpath.splitext(target)[1].lower() in {".emf", ".wmf"}))
        vector_images = _rasterize_vector_batches(archive, vector_targets, deadline)
        for index, (marker, part) in enumerate(zip(markers, parts), 1):
            end = markers[index].start() if index < len(markers) else len(markdown)
            body = markdown[marker.end():end].strip()
            body = _MARKDOWN_IMAGE.sub("", body)
            # Slide headings are siblings; original titles sit beneath them.
            body = re.sub(r"(?m)^# ", "## ", body)
            note_marker = re.search(r"(?m)^### Notes:", body)
            notes = body[note_marker.start():] if note_marker else ""
            if note_marker:
                body = body[:note_marker.start()].strip()
            page = [f"<!-- Slide number: {index} -->\n# 第{index}页", body]
            for label, target, placeholder in pictures_by_page[index - 1]:
                label = re.sub(r"[\r\n\[\]]", " ", label).strip()
                source_labels = []
                geometry = None
                if target.lower().endswith(".emf"):
                    try:
                        raw_emf = _read_member(archive, target, _MAX_MEDIA_BYTES)
                        source_labels = extract_emf_labels(raw_emf)
                        geometry = estimate_emf_line_chart(raw_emf)
                    except (KeyError, ValueError):
                        pass
                if target and target not in cache:
                    png = vector_images.pop(target, None)
                    remaining = deadline - time.monotonic()
                    # The budget limits expensive vector rendering. An earlier
                    # failed vector must not hide already embedded raster logos.
                    native_raster = posixpath.splitext(target)[1].lower() in _RASTER_EXT
                    if png is None and (remaining > 0 or native_raster):
                        try:
                            png = rasterize_media_bytes(target, _read_member(archive, target, _MAX_MEDIA_BYTES),
                                                        timeout=max(0.1, min(30, remaining)))
                        except (KeyError, ValueError) as exc:
                            logger.warning("PPT image unavailable %s: %s", target, exc)
                    if png:
                        ref = f"images/{uuid.uuid4()}.png"
                        images[ref] = base64.b64encode(png).decode()
                        cache[target] = ref
                    else:
                        cache[target] = None
                ref = cache.get(target)
                if ref:
                    page.append(f"![第{index}页 · {label}]({ref})\n\n来源：第{index}页 · {target}（原文件中的图像或缓存预览，未刷新外部链接）。")
                else:
                    page.append(f"![{label}]({placeholder})\n\n第{index}页图像“{label}”未能解析；请查看原文件，不能据此判断图例或数值。")
                if source_labels:
                    # Escape Markdown syntax from untrusted source text; retain
                    # exact strings (especially chart titles) for retrieval.
                    labels_text = re.sub(r"([\\`*\[\]<>!#_])", r"\\\1", " | ".join(source_labels))
                    page.append(f"第{index}页原图文字 · {target}（缓存EMF文本记录，刻度和图例不是观察值）：\n{labels_text}")
                if geometry:
                    page.append(_geometry_markdown(geometry, index, target))
            if notes:
                page.append(notes)
            sections.append("\n\n".join(item for item in page if item))
    return "\n\n".join(sections), images


def markdown_needs_pptx_media_attach(markdown: str) -> bool:
    # OLE pictures can be absent entirely. Attachment is unconditional for PPTX.
    return bool(_SLIDE_MARKER.search(markdown)) or any(
        _is_unresolved_image_ref(m.group(2)) for m in _MARKDOWN_IMAGE.finditer(markdown)
    )
