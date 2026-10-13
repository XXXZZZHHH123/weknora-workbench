import base64
import io
import subprocess
import struct
import time
import unittest
import zipfile
from unittest.mock import patch

from docreader.parser import pptx_media as media

P = media._NS["p"]
A = media._NS["a"]
R = media._NS["r"]
REL = media._NS["rel"]


def picture(identifier, name, rid):
    return (f'<p:pic><p:nvPicPr><p:cNvPr id="{identifier}" name="{name}"/>'
            f'</p:nvPicPr><p:blipFill><a:blip r:embed="{rid}"/></p:blipFill></p:pic>')


def fixture():
    parts = {
        # Include an unused asset before the real pictures: it must never be mapped.
        "ppt/media/unrelated.emf": b"wrong-report",
        "ppt/media/chart-b.emf": b"chart-b",
        "ppt/media/logo.png": b"logo",
        "ppt/media/chart-a.emf": b"chart-a",
        "ppt/presentation.xml": (f'<p:presentation xmlns:p="{P}" xmlns:r="{R}">'
                                 '<p:sldIdLst><p:sldId r:id="first"/>'
                                 '<p:sldId r:id="second"/></p:sldIdLst></p:presentation>').encode(),
        "ppt/_rels/presentation.xml.rels": (f'<Relationships xmlns="{REL}">'
                                          '<Relationship Id="first" Target="slides/slide9.xml"/>'
                                          '<Relationship Id="second" Target="slides/slide2.xml"/>'
                                          '</Relationships>').encode(),
        "ppt/slides/slide9.xml": (f'<p:sld xmlns:p="{P}" xmlns:a="{A}" xmlns:r="{R}">'
                                 + picture(1, "Company logo", "logo")
                                 + '<p:graphicFrame><p:oleObj r:id="excel">'
                                 + picture(2, "01-05 chart", "chart")
                                 + '</p:oleObj></p:graphicFrame></p:sld>').encode(),
        "ppt/slides/_rels/slide9.xml.rels": (f'<Relationships xmlns="{REL}">'
                                           '<Relationship Id="logo" Target="../media/logo.png"/>'
                                           '<Relationship Id="chart" Target="../media/chart-a.emf"/>'
                                           '<Relationship Id="excel" TargetMode="External" '
                                           'Target="file:///C:/private.xlsx"/>'
                                           '</Relationships>').encode(),
        "ppt/slides/slide2.xml": (f'<p:sld xmlns:p="{P}" xmlns:a="{A}" xmlns:r="{R}">'
                                 + '<p:grpSp>' + picture(3, "Other chart", "chart")
                                 + '</p:grpSp>' + picture(4, "External image", "external")
                                 + '</p:sld>').encode(),
        "ppt/slides/_rels/slide2.xml.rels": (f'<Relationships xmlns="{REL}">'
                                           '<Relationship Id="chart" Target="../media/chart-b.emf"/>'
                                           '<Relationship Id="external" TargetMode="External" '
                                           'Target="https://example.test/private.png"/>'
                                           '</Relationships>').encode(),
    }
    out = io.BytesIO()
    with zipfile.ZipFile(out, "w") as archive:
        for name, data in parts.items():
            archive.writestr(name, data)
    return out.getvalue()


MARKDOWN = ("<!-- Slide number: 1 -->\n# First title\nFirst text\n"
            "![Company logo](data:image/png;base64,bG9nbw==)\n"
            "### Notes:\nFirst notes\n\n"
            "<!-- Slide number: 2 -->\n# Second title\nSecond text\n"
            "![Other chart](wrong.jpg)\n")


class TestPptxMedia(unittest.TestCase):
    def test_trim_applies_only_to_converted_canvas_and_preserves_alpha(self):
        from PIL import Image, ImageDraw
        canvas = Image.new("RGB", (100, 120), "white")
        ImageDraw.Draw(canvas).rectangle((30, 40, 59, 69), fill="black")
        raw = io.BytesIO()
        canvas.save(raw, format="PNG")
        original = Image.open(io.BytesIO(media._rasterize_with_pillow(raw.getvalue())))
        cropped = Image.open(io.BytesIO(media._rasterize_with_pillow(raw.getvalue(), trim_canvas=True)))
        self.assertEqual(original.size, (100, 120))
        self.assertEqual(cropped.size, (46, 46))
        logo = Image.new("RGBA", (8, 8), (255, 0, 0, 128))
        raw = io.BytesIO()
        logo.save(raw, format="PNG")
        preserved = Image.open(io.BytesIO(media._rasterize_with_pillow(raw.getvalue())))
        self.assertEqual(preserved.getpixel((0, 0)), (255, 0, 0, 128))

    def test_emf_exact_title_joins_only_adjacent_original_text_runs(self):
        header = bytearray(88)
        struct.pack_into("<II", header, 0, 1, 88)
        header[40:44] = b" EMF"

        def text_record(text, x, y, width, flags=0):
            encoded = text.encode("utf-16-le")
            chars = len(encoded) // 2
            advance_offset = (76 + len(encoded) + 3) // 4 * 4
            size = advance_offset + chars * 4
            record = bytearray(size)
            struct.pack_into("<II", record, 0, 84, size)
            struct.pack_into("<iiIII", record, 36, x, y, chars, 76, flags)
            struct.pack_into("<I", record, 72, advance_offset)
            record[76:76 + len(encoded)] = encoded
            struct.pack_into("<" + "i" * chars, record, advance_offset, *([width] * chars))
            return bytes(record)

        emf = bytes(header) + b"".join([
            text_record("郑棉", 346, 40, 25), text_record("01", 396, 40, 13),
            text_record("-", 422, 40, 8), text_record("05", 430, 40, 13),
            text_record("价差", 456, 40, 25), text_record("2401", 800, 423, 10),
            text_record("glyph indices", 1, 1, 10, flags=0x10),
        ])
        self.assertEqual(media.extract_emf_labels(emf), ["郑棉01-05价差", "2401"])
        self.assertEqual(media.extract_emf_labels(b"untrusted bytes"), [])
        self.assertEqual(media.extract_emf_labels(emf[:-10]), ["郑棉01-05价差", "2401"])
        # Excel emits runs at (0,0), positioned with MWT_LEFTMULTIPLY.
        def transform(mode, x=0, y=0):
            return struct.pack("<IIffffffI", 36, 36, 1, 0, 0, 1, x, y, mode)
        positioned = bytes(header) + b"".join([
            transform(2, 346, 40), text_record("郑棉", 0, 0, 25),
            transform(1), transform(2, 396, 40), text_record("01", 0, 0, 13),
        ])
        self.assertEqual(media.extract_emf_labels(positioned), ["郑棉01"])

    def test_batch_converts_only_supplied_internal_caches_and_validates_outputs(self):
        from PIL import Image
        from pathlib import Path
        calls = []

        def convert(command, timeout):
            calls.append(command)
            for value in command[command.index("--outdir") + 2:]:
                Image.new("RGB", (8, 8)).save(Path(value).with_suffix(".png"))
            return True

        with zipfile.ZipFile(io.BytesIO(fixture())) as archive, \
                patch.object(media, "find_soffice", return_value="/test/soffice"), \
                patch.object(media, "_run_conversion", side_effect=convert):
            images = media._rasterize_vector_batches(
                archive, ["ppt/media/chart-a.emf", "ppt/media/chart-b.emf"], time.monotonic() + 10)
        self.assertEqual(set(images), {"ppt/media/chart-a.emf", "ppt/media/chart-b.emf"})
        self.assertEqual(len(calls), 1)
        self.assertNotIn("unrelated", " ".join(calls[0]))
        self.assertNotIn("xlsx", " ".join(calls[0]))

    def test_relationships_keep_each_chart_on_correct_display_page(self):
        calls = []
        def raster(name, data, **kwargs):
            calls.append((name, data))
            return b"png:" + data
        with patch.object(media, "rasterize_media_bytes", side_effect=raster), \
                patch.object(media, "_rasterize_vector_batches", return_value={}):
            text, images = media.attach_pptx_media_to_markdown(MARKDOWN, fixture())
        first, second = text.split("<!-- Slide number: 2 -->")
        self.assertIn("# 第1页", first)
        self.assertIn("## First title", first)
        self.assertIn("First notes", first)
        self.assertIn("ppt/media/chart-a.emf", first)
        self.assertNotIn("chart-b.emf", first)
        self.assertIn("# 第2页", second)
        self.assertIn("ppt/media/chart-b.emf", second)
        self.assertNotIn("chart-a.emf", second)
        self.assertEqual(first.count("![第1页 · Company logo]"), 1)
        self.assertNotIn("data:image", text)
        self.assertNotIn("wrong.jpg", text)
        self.assertNotIn("C:/private", text)
        self.assertNotIn("https://", text)
        self.assertIn("未能解析", second)  # Explicit external image unavailability.
        self.assertEqual([data for _, data in calls], [b"logo", b"chart-a", b"chart-b"])
        self.assertEqual({base64.b64decode(v) for v in images.values()},
                         {b"png:logo", b"png:chart-a", b"png:chart-b"})

    def test_inconsistent_page_markers_cannot_guess_image_assignment(self):
        with self.assertRaisesRegex(ValueError, "refusing"):
            media.attach_pptx_media_to_markdown("one unidentified slide", fixture())
        with self.assertRaisesRegex(ValueError, "refusing"):
            media.attach_pptx_media_to_markdown(MARKDOWN.replace("number: 2", "number: 9"), fixture())

    def test_failed_cache_is_explicit_and_text_survives(self):
        with patch.object(media, "rasterize_media_bytes", return_value=None), \
                patch.object(media, "_rasterize_vector_batches", return_value={}):
            text, images = media.attach_pptx_media_to_markdown(MARKDOWN, fixture())
        self.assertEqual(images, {})
        self.assertIn("First text", text)
        self.assertIn("不能据此判断图例或数值", text)
        self.assertNotIn("images/", text)

    def test_exhausted_vector_budget_preserves_existing_raster_logo(self):
        with patch.object(media, "_MEDIA_BUDGET_SECONDS", -1), \
                patch.object(media, "_rasterize_vector_batches", return_value={}), \
                patch.object(media, "rasterize_media_bytes", return_value=b"logo-png") as raster:
            text, images = media.attach_pptx_media_to_markdown(MARKDOWN, fixture())
        self.assertEqual(len(images), 1)
        self.assertIn("![第1页 · Company logo]", text)
        self.assertIn("01-05 chart”未能解析", text)
        self.assertEqual(raster.call_args.args[:2], ("ppt/media/logo.png", b"logo"))
        self.assertEqual(raster.call_count, 1)

    def test_ole_without_markdown_placeholder_still_triggers_attachment(self):
        self.assertTrue(media.markdown_needs_pptx_media_attach(
            "<!-- Slide number: 12 -->\nWorksheet\n"))

    def test_emf_prefers_standalone_libreoffice_not_imagemagick(self):
        with patch.object(media, "_rasterize_with_libreoffice", return_value=b"png") as office, \
                patch.object(media, "_rasterize_with_imagemagick") as imagemagick:
            self.assertEqual(media.rasterize_media_bytes("image84.emf", b"emf"), b"png")
        office.assert_called_once()
        imagemagick.assert_not_called()

    def test_size_and_extension_limits_before_launching_converter(self):
        with patch.object(media, "_rasterize_with_libreoffice") as office:
            self.assertIsNone(media.rasterize_media_bytes("large.emf", b"0" * (media._MAX_MEDIA_BYTES + 1)))
            self.assertIsNone(media.rasterize_media_bytes("ole.bin", b"private workbook"))
        office.assert_not_called()

    def test_svg_external_reference_cannot_be_fetched(self):
        with patch.object(media, "_rasterize_with_imagemagick") as converter:
            self.assertIsNone(media.rasterize_media_bytes(
                "external.svg", b'<svg><image href="https://example.test/private.png"/></svg>'))
            self.assertIsNone(media.rasterize_media_bytes(
                "external.svg", b'<svg><style>@import url(https://example.test/style);</style></svg>'))
        converter.assert_not_called()

    def test_conversion_timeout_kills_process_group(self):
        with patch.object(media.subprocess, "Popen") as popen, patch.object(media.os, "killpg") as kill:
            proc = popen.return_value
            proc.pid = 1234
            proc.wait.side_effect = [subprocess.TimeoutExpired("soffice", 1), 0]
            self.assertFalse(media._run_conversion(["soffice", "input.emf"], 1))
        kill.assert_called_once_with(1234, media.signal.SIGKILL)
        self.assertTrue(popen.call_args.kwargs["start_new_session"])

    def test_generated_deck_preserves_text_notes_and_shared_logo_once_per_page(self):
        from PIL import Image
        from pptx import Presentation
        from pptx.util import Inches
        from docreader.parser.markitdown_parser import MarkitdownParser

        png = io.BytesIO()
        Image.new("RGBA", (8, 8), (255, 0, 0, 128)).save(png, format="PNG")
        deck = Presentation()
        for number in (1, 2):
            slide = deck.slides.add_slide(deck.slide_layouts[6])
            slide.shapes.add_textbox(Inches(1), Inches(1), Inches(3), Inches(1)).text = f"正文 {number}"
            slide.shapes.add_picture(io.BytesIO(png.getvalue()), Inches(1), Inches(2))
            slide.notes_slide.notes_text_frame.text = f"备注 {number}"
        package = io.BytesIO()
        deck.save(package)
        doc = MarkitdownParser(file_type="pptx").parse_into_text(package.getvalue())
        self.assertEqual(len(doc.images), 1)  # Same asset, each slide retains its own reference.
        for number in (1, 2):
            self.assertIn(f"# 第{number}页", doc.content)
            self.assertIn(f"正文 {number}", doc.content)
            self.assertIn(f"备注 {number}", doc.content)
            self.assertEqual(doc.content.count(f"![第{number}页 ·"), 1)
        self.assertNotIn("data:image", doc.content)


if __name__ == "__main__":
    unittest.main()
