import base64
import io
import hashlib
import json
import os
import unittest
from dataclasses import replace
from types import SimpleNamespace
from unittest.mock import Mock, patch

from PIL import Image

import docreader.parser.pdf_parser as pdf_parser
from docreader.main import DocReaderServicer
from docreader.models.document import Document
from docreader.parser.stream import ParsedDocumentStream
from docreader.parser.pptx_figure import FIGURE_GEOMETRY_ENGINE
from docreader.parser.source_media_png import SOURCE_MEDIA_PNG_ENGINE
from docreader.proto.docreader_pb2 import ReadConfig, ReadRequest
from docreader.tests.test_pptx_figure import original_deck
from docreader.tests.test_pptx_media import fixture
from docreader.tests.test_source_media_png import png_bytes


def _scanned_pdf(page_count=2):
    buffer = io.BytesIO()
    pages = [Image.new("RGB", (64, 64), color) for color in ("white", "black")]
    sequence = (pages * ((page_count // len(pages)) + 1))[:page_count]
    sequence[0].save(
        buffer,
        format="PDF",
        save_all=True,
        append_images=sequence[1:],
    )
    return buffer.getvalue()


class _NoopPreprocessRunner:
    def __init__(self):
        self.limits = SimpleNamespace(max_workers=1)

    def run(self, *args, **kwargs):
        return None

    def close(self):
        pass


class ReadStreamPDFTest(unittest.TestCase):
    def test_metadata_precedes_lazy_scanned_page_rendering(self):
        config = ReadConfig()
        config.parser_engine_overrides["pdf_force_scanned"] = "true"
        request = ReadRequest(
            file_content=_scanned_pdf(2),
            file_name="scan.pdf",
            file_type="pdf",
            config=config,
        )
        service = DocReaderServicer(preprocess_runner=_NoopPreprocessRunner())
        real_render = pdf_parser._render_page_to_jpeg
        stream = None

        try:
            with patch.object(
                pdf_parser,
                "CONFIG",
                replace(pdf_parser.CONFIG, pdf_render_parallelism=1),
            ):
                with patch.object(
                    pdf_parser,
                    "_render_page_to_jpeg",
                    wraps=real_render,
                ) as render:
                    stream = service.ReadStream(request, None)
                    meta = next(stream).meta
                    self.assertEqual(meta.image_count, 2)
                    self.assertEqual(render.call_count, 0)

                    first = next(stream).image
                    self.assertEqual(first.filename, "scan_page_1.jpg")
                    self.assertTrue(first.image_data.startswith(b"\xff\xd8"))
                    self.assertEqual(render.call_count, 1)

                    second = next(stream).image
                    self.assertEqual(second.filename, "scan_page_2.jpg")
                    self.assertTrue(second.image_data.startswith(b"\xff\xd8"))
                    self.assertEqual(render.call_count, 2)

                    with self.assertRaises(StopIteration):
                        next(stream)
        finally:
            if stream is not None:
                stream.close()
            service.close()

    def test_cancelling_after_metadata_removes_temporary_pdf(self):
        config = ReadConfig()
        config.parser_engine_overrides["pdf_force_scanned"] = "true"
        request = ReadRequest(
            file_content=_scanned_pdf(2),
            file_name="scan.pdf",
            file_type="pdf",
            config=config,
        )
        service = DocReaderServicer(preprocess_runner=_NoopPreprocessRunner())
        real_write = pdf_parser._write_pdf_temp_file
        temp_paths = []
        stream = None

        def record_temp_path(content):
            path = real_write(content)
            temp_paths.append(path)
            return path

        try:
            with patch.object(
                pdf_parser,
                "CONFIG",
                replace(pdf_parser.CONFIG, pdf_render_parallelism=1),
            ):
                with patch.object(
                    pdf_parser,
                    "_write_pdf_temp_file",
                    side_effect=record_temp_path,
                ):
                    stream = service.ReadStream(request, None)
                    self.assertEqual(next(stream).meta.image_count, 2)
                    self.assertEqual(len(temp_paths), 1)
                    self.assertTrue(os.path.exists(temp_paths[0]))
                    stream.close()
                    self.assertFalse(os.path.exists(temp_paths[0]))
        finally:
            if stream is not None:
                stream.close()
            service.close()


class ReadStreamSelectedSourceTest(unittest.TestCase):
    def setUp(self):
        self.service = DocReaderServicer.__new__(DocReaderServicer)
        self.service.parser = Mock()
        self.service.parser.parse_file_stream.side_effect = AssertionError(
            "A selected original must not parse the complete presentation"
        )

    def request(self, engine, source, **overrides):
        options = {"source_page": "1", "source_part": "ppt/media/chart-a.emf"}
        options.update(overrides)
        return ReadRequest(
            file_content=source,
            file_name="synthetic.pptx",
            file_type="pptx",
            config=ReadConfig(
                parser_engine=engine,
                parser_engine_overrides=options,
            ),
        )

    def test_selected_geometry_stream_preserves_source_hash_and_model_legend(self):
        source = original_deck()
        request = self.request(
            FIGURE_GEOMETRY_ENGINE, source, series_json='["Gamma"]'
        )

        frames = list(self.service.ReadStream(request, None))

        self.assertEqual(len(frames), 1)
        meta = frames[0].meta
        self.assertFalse(meta.error)
        self.assertEqual(meta.image_count, 0)
        self.assertEqual(meta.metadata["evidence_version"], FIGURE_GEOMETRY_ENGINE)
        self.assertEqual(
            meta.metadata["source_file_sha256"], hashlib.sha256(source).hexdigest()
        )
        self.assertEqual(meta.metadata["source_page"], "1")
        self.assertEqual(meta.metadata["source_part"], "ppt/media/chart-a.emf")
        geometry = json.loads(meta.metadata["geometry_json"])
        self.assertEqual(geometry["series_count"], 1)
        self.assertEqual(geometry["omitted_series"], ["Alpha", "Beta"])
        self.assertIn("Gamma", meta.markdown_content)
        self.service.parser.parse_file_stream.assert_not_called()
        self.service.parser.parse_file.assert_not_called()

    def test_selected_media_stream_preserves_png_and_source_identity(self):
        source = fixture()
        png = png_bytes()
        request = self.request(SOURCE_MEDIA_PNG_ENGINE, source)
        with patch(
            "docreader.parser.source_media_png.rasterize_media_bytes", return_value=png
        ):
            frames = list(self.service.ReadStream(request, None))

        self.assertEqual(len(frames), 1)
        meta = frames[0].meta
        self.assertFalse(meta.error)
        self.assertEqual(meta.image_count, 0)
        self.assertEqual(meta.metadata["evidence_version"], SOURCE_MEDIA_PNG_ENGINE)
        self.assertEqual(
            meta.metadata["source_file_sha256"], hashlib.sha256(source).hexdigest()
        )
        self.assertEqual(meta.metadata["source_part"], "ppt/media/chart-a.emf")
        self.assertEqual(base64.b64decode(meta.metadata["rendered_png_base64"]), png)
        self.assertEqual(
            meta.metadata["rendered_png_sha256"], hashlib.sha256(png).hexdigest()
        )
        self.service.parser.parse_file_stream.assert_not_called()
        self.service.parser.parse_file.assert_not_called()

    def test_invalid_source_scope_and_url_never_fall_back_to_deck_parser(self):
        for engine in (SOURCE_MEDIA_PNG_ENGINE, FIGURE_GEOMETRY_ENGINE):
            for invalid in ("url", "foreign_part"):
                with self.subTest(engine=engine, invalid=invalid):
                    request = self.request(engine, original_deck())
                    if invalid == "url":
                        request.url = "https://example.invalid/deck.pptx"
                    else:
                        request.config.parser_engine_overrides["source_part"] = (
                            "ppt/media/unrelated.emf"
                        )
                    frames = list(self.service.ReadStream(request, None))
                    self.assertEqual(len(frames), 1)
                    self.assertTrue(frames[0].meta.error)
                    self.assertFalse(frames[0].meta.markdown_content)
                    self.assertEqual(frames[0].meta.image_count, 0)
        self.service.parser.parse_file_stream.assert_not_called()
        self.service.parser.parse_file.assert_not_called()
        self.service.parser.parse_url.assert_not_called()

    def test_ordinary_presentation_still_uses_streaming_parser(self):
        self.service.parser.parse_file_stream.side_effect = None
        self.service.parser.parse_file_stream.return_value = ParsedDocumentStream(
            document=Document(content="ordinary presentation"), image_count=0
        )
        request = self.request("markitdown", b"ordinary stored bytes")

        frames = list(self.service.ReadStream(request, None))

        self.assertFalse(frames[0].meta.error)
        self.assertEqual(frames[0].meta.markdown_content, "ordinary presentation")
        self.service.parser.parse_file_stream.assert_called_once_with(
            "synthetic.pptx",
            "pptx",
            b"ordinary stored bytes",
            parser_engine="markitdown",
            engine_overrides={
                "source_page": "1",
                "source_part": "ppt/media/chart-a.emf",
            },
        )
        self.service.parser.parse_file.assert_not_called()


if __name__ == "__main__":
    unittest.main()
