import base64
import hashlib
import io
import logging
import zipfile
import unittest
from unittest.mock import patch

from PIL import Image

from docreader.parser import source_media_png as media_png
from docreader.tests.test_pptx_media import fixture


def png_bytes(width=32, height=24):
    output = io.BytesIO()
    Image.new("RGB", (width, height), "white").save(output, format="PNG")
    return output.getvalue()


def deck_with_oversized_media():
    source = fixture()
    output = io.BytesIO()
    with zipfile.ZipFile(io.BytesIO(source)) as original, zipfile.ZipFile(output, "w") as rebuilt:
        for name in original.namelist():
            data = original.read(name)
            if name == "ppt/media/chart-a.emf":
                data = b"x" * (media_png._MAX_MEDIA_BYTES + 1)
            rebuilt.writestr(name, data)
    return output.getvalue()


def deck_with_unicode_media_name():
    source = fixture()
    output = io.BytesIO()
    with zipfile.ZipFile(io.BytesIO(source)) as original, zipfile.ZipFile(output, "w") as rebuilt:
        for name in original.namelist():
            renamed = name.replace("chart-a.emf", "海岸图表.emf")
            data = original.read(name)
            if name.endswith("slide9.xml.rels"):
                data = data.replace(b"chart-a.emf", "海岸图表.emf".encode("utf-8"))
            rebuilt.writestr(renamed, data)
    return output.getvalue()


class TestSourceMediaPng(unittest.TestCase):
    def test_invalid_selector_has_typed_unary_and_streaming_error_metadata(self):
        from docreader.proto.docreader_pb2 import ReadRequest

        handlers = logging.getLogger().handlers[:]
        try:
            from docreader.main import DocReaderServicer

            servicer = DocReaderServicer.__new__(DocReaderServicer)
            request = ReadRequest(request_id="preview-selector-error")
            expected = {"evidence_version": "source_media_png_v1", "source_media_error_code": "invalid_selector"}
            with patch.object(servicer, "_parse_request", side_effect=media_png.SourceMediaSelectorError("Unrelated source part")):
                unary = servicer.Read(request, None)
                frames = list(servicer.ReadStream(request, None))
            self.assertTrue(unary.error)
            self.assertEqual(dict(unary.metadata), expected)
            self.assertEqual(dict(frames[0].meta.metadata), expected)
            self.assertFalse(unary.markdown_content)
            with patch.object(servicer, "_parse_request", side_effect=ValueError("Renderer failed")):
                ordinary = servicer.Read(request, None)
            self.assertEqual(dict(ordinary.metadata), {})
        finally:
            logging.getLogger().handlers[:] = handlers

    def test_renders_only_selected_relation_and_returns_bound_hashes(self):
        source = fixture()
        rendered = png_bytes()
        with patch.object(media_png, "rasterize_media_bytes", return_value=rendered) as raster:
            result = media_png.read_source_media_png(
                source,
                "pptx",
                {"source_page": "1", "source_part": "ppt/media/chart-a.emf"},
            )

        self.assertTrue(result.content)
        self.assertEqual(
            {
                "evidence_version": "source_media_png_v1",
                "source_file_sha256": hashlib.sha256(source).hexdigest(),
                "source_media_sha256": hashlib.sha256(b"chart-a").hexdigest(),
                "source_page": "1",
                "source_part": "ppt/media/chart-a.emf",
                "rendered_png_sha256": hashlib.sha256(rendered).hexdigest(),
                "rendered_png_base64": base64.b64encode(rendered).decode("ascii"),
            },
            result.metadata,
        )
        raster.assert_called_once()
        args, kwargs = raster.call_args
        self.assertEqual(args, ("ppt/media/chart-a.emf", b"chart-a"))
        self.assertGreater(kwargs["timeout"], 0)
        self.assertLessEqual(kwargs["timeout"], media_png._RENDER_TIMEOUT_SECONDS)

    def test_page_part_size_and_file_type_are_fail_closed_before_render(self):
        source = fixture()
        invalid = [
            ("pptx", {"source_page": "2", "source_part": "ppt/media/chart-a.emf"}),
            ("pptx", {"source_page": "0", "source_part": "ppt/media/chart-a.emf"}),
            ("pptx", {"source_page": "1", "source_part": "ppt/media/../chart-a.emf"}),
            ("ppt", {"source_page": "1", "source_part": "ppt/media/chart-a.emf"}),
        ]
        with patch.object(media_png, "rasterize_media_bytes") as raster:
            for file_type, options in invalid:
                with self.subTest(file_type=file_type, options=options), self.assertRaises(ValueError):
                    media_png.read_source_media_png(source, file_type, options)
            with self.assertRaises(ValueError):
                media_png.read_source_media_png(
                    source + b"x" * (media_png._MAX_PRESENTATION_BYTES + 1),
                    "pptx",
                    {"source_page": "1", "source_part": "ppt/media/chart-a.emf"},
                )
            with self.assertRaisesRegex(ValueError, "extraction limit"):
                media_png.read_source_media_png(
                    deck_with_oversized_media(),
                    "pptx",
                    {"source_page": "1", "source_part": "ppt/media/chart-a.emf"},
                )
        raster.assert_not_called()

    def test_unicode_media_basename_is_supported_and_unsafe_names_are_rejected(self):
        source = deck_with_unicode_media_name()
        with patch.object(media_png, "rasterize_media_bytes", return_value=png_bytes()) as raster:
            result = media_png.read_source_media_png(
                source,
                "pptx",
                {"source_page": "1", "source_part": "ppt/media/海岸图表.emf"},
            )
        self.assertEqual(result.metadata["source_part"], "ppt/media/海岸图表.emf")
        raster.assert_called_once()

        invalid_parts = [
            "ppt/media/.emf",
            "ppt/media/..emf",
            "ppt/media/...emf",
            "ppt/media/   .emf",
            "ppt/media/folder/chart.emf",
            "ppt/media/folder\\chart.emf",
            "ppt/media/line\x01break.emf",
            "ppt/media/" + ("海" * 400) + ".emf",
            "ppt/media/../chart.emf",
        ]
        with patch.object(media_png, "rasterize_media_bytes") as raster:
            for part in invalid_parts:
                with self.subTest(part=part), self.assertRaises(ValueError):
                    media_png.read_source_media_png(
                        fixture(),
                        "pptx",
                        {"source_page": "1", "source_part": part},
                    )
        raster.assert_not_called()

    def test_render_failure_and_oversized_or_invalid_png_return_no_document(self):
        source = fixture()
        options = {"source_page": "1", "source_part": "ppt/media/chart-a.emf"}
        failures = [
            None,
            b"not a png",
            b"\x89PNG\r\n\x1a\n" + b"x" * media_png._MAX_PNG_BYTES,
            png_bytes(width=media_png._MAX_DIMENSION + 1, height=1),
        ]
        for rendered in failures:
            with self.subTest(rendered_size=None if rendered is None else len(rendered)), patch.object(
                media_png, "rasterize_media_bytes", return_value=rendered
            ):
                with self.assertRaises(ValueError):
                    media_png.read_source_media_png(source, "pptx", options)

    def test_parse_request_uses_stored_bytes_and_refuses_url(self):
        from docreader.proto.docreader_pb2 import ReadConfig, ReadRequest

        root_logger = logging.getLogger()
        original_handlers = root_logger.handlers[:]
        try:
            from docreader.main import DocReaderServicer

            servicer = DocReaderServicer.__new__(DocReaderServicer)
            source = fixture()
            options = {"source_page": "1", "source_part": "ppt/media/chart-a.emf"}
            with patch("docreader.parser.source_media_png.read_source_media_png") as reader:
                from docreader.models.document import Document

                reader.return_value = Document(content="rendered")
                request = ReadRequest(
                    file_content=source,
                    file_name="deck.pptx",
                    file_type="pptx",
                    config=ReadConfig(
                        parser_engine=media_png.SOURCE_MEDIA_PNG_ENGINE,
                        parser_engine_overrides={key: value for key, value in options.items()},
                    ),
                )
                result, source_desc = servicer._parse_request(request)
                self.assertEqual(result.content, "rendered")
                self.assertEqual(source_desc, "deck.pptx")
                reader.assert_called_once_with(source, "pptx", options)

                request.url = "https://example.invalid/deck.pptx"
                with self.assertRaisesRegex(ValueError, "stored file bytes"):
                    servicer._parse_request(request)
                reader.assert_called_once()
        finally:
            root_logger.handlers[:] = original_handlers


if __name__ == "__main__":
    unittest.main()
