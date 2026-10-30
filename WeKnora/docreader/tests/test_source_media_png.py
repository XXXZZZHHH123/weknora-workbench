import base64
import hashlib
import io
import logging
import zipfile
import unittest
import threading
import time
from concurrent.futures import ThreadPoolExecutor
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
    def setUp(self):
        with media_png._STATE_LOCK:
            self.assertFalse(media_png._FLIGHTS)
            media_png._CACHE.clear()
            media_png._CACHE_BYTES = 0

    def test_invalid_selector_has_typed_unary_and_streaming_error_metadata(self):
        from docreader.proto.docreader_pb2 import ReadRequest

        handlers = logging.getLogger().handlers[:]
        try:
            from docreader.main import DocReaderServicer

            servicer = DocReaderServicer.__new__(DocReaderServicer)
            servicer.parser = object()
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


class TestSharedSourceMediaRender(unittest.TestCase):
    """Concurrency tests use synthetic bytes and never invoke Office/model APIs."""

    def setUp(self):
        TestSourceMediaPng.setUp(self)
        self.source = fixture()

    def read(self, context=None, page="1"):
        with media_png.source_media_request_context(context):
            return media_png.read_source_media_png(self.source, "pptx", {
                "source_page": page, "source_part": "ppt/media/chart-a.emf"})

    def wait_for(self, predicate):
        deadline = time.monotonic() + 2
        while not predicate():
            if time.monotonic() > deadline:
                self.fail("Concurrent operation did not reach expected state")
            time.sleep(0.005)

    def test_same_image_merges_and_cache_still_checks_page_relation(self):
        entered, release = threading.Event(), threading.Event()
        def render(*args, **kwargs):
            entered.set()
            self.assertTrue(release.wait(2))
            return png_bytes()
        with patch.object(media_png, "rasterize_media_bytes", side_effect=render) as raster, ThreadPoolExecutor(2) as pool:
            first = pool.submit(self.read)
            self.assertTrue(entered.wait(2))
            second = pool.submit(self.read)
            self.wait_for(lambda: any(f.peak_waiters == 2 for f in list(media_png._FLIGHTS.values())))
            release.set()
            self.assertEqual(first.result().metadata, second.result().metadata)
            self.read()
            with self.assertRaises(media_png.SourceMediaSelectorError):
                self.read(page="2")
            self.assertEqual(raster.call_count, 1)

    def test_one_waiter_cancel_does_not_cancel_shared_conversion(self):
        class Context:
            active = True
            def is_active(self):
                return self.active
        context = Context()
        entered, release = threading.Event(), threading.Event()
        def render(*args, **kwargs):
            from docreader.parser.pptx_media import _conversion_is_cancelled
            entered.set()
            self.assertTrue(release.wait(2))
            self.assertFalse(_conversion_is_cancelled())
            return png_bytes()
        with patch.object(media_png, "rasterize_media_bytes", side_effect=render) as raster, ThreadPoolExecutor(2) as pool:
            first = pool.submit(self.read, context)
            self.assertTrue(entered.wait(2))
            second = pool.submit(self.read)
            self.wait_for(lambda: any(f.peak_waiters == 2 for f in list(media_png._FLIGHTS.values())))
            context.active = False
            with self.assertRaises(media_png.SourceMediaRenderError) as error:
                first.result(timeout=2)
            self.assertEqual(error.exception.code, "cancelled")
            release.set()
            self.assertTrue(second.result(timeout=2).metadata)
            self.assertEqual(raster.call_count, 1)

    def test_last_cancel_stops_conversion_without_caching_failure(self):
        class Context:
            active = True
            def is_active(self):
                return self.active
        context = Context()
        entered, stopped = threading.Event(), threading.Event()
        def render(*args, **kwargs):
            from docreader.parser.pptx_media import _conversion_is_cancelled
            entered.set()
            self.wait_for(_conversion_is_cancelled)
            stopped.set()
            return None
        with patch.object(media_png, "rasterize_media_bytes", side_effect=render), ThreadPoolExecutor(1) as pool:
            future = pool.submit(self.read, context)
            self.assertTrue(entered.wait(2))
            context.active = False
            with self.assertRaises(media_png.SourceMediaRenderError):
                future.result(timeout=2)
            self.assertTrue(stopped.wait(2))
            self.wait_for(lambda: not media_png._FLIGHTS)
            self.assertFalse(media_png._CACHE)
        with patch.object(media_png, "rasterize_media_bytes", return_value=png_bytes()) as raster:
            self.read()
            raster.assert_called_once()

    def test_queue_budget_is_independent_and_capacity_is_bounded(self):
        slot = threading.BoundedSemaphore(1)
        slot.acquire()
        with patch.object(media_png, "_RENDER_SLOTS", slot), patch.object(media_png, "_QUEUE_TIMEOUT_SECONDS", .08), patch.object(media_png, "rasterize_media_bytes") as raster:
            with self.assertRaises(media_png.SourceMediaRenderError) as error:
                self.read()
            self.assertEqual(error.exception.code, "busy")
            raster.assert_not_called()
        slot.release()
        slot.acquire()
        with patch.object(media_png, "_RENDER_SLOTS", slot), patch.object(media_png, "rasterize_media_bytes", return_value=png_bytes()) as raster, ThreadPoolExecutor(1) as pool:
            future = pool.submit(self.read)
            self.wait_for(lambda: bool(media_png._FLIGHTS))
            time.sleep(.06)
            slot.release()
            future.result(timeout=2)
            self.assertEqual(raster.call_args.kwargs["timeout"], 30)

    def test_cache_has_expiry_and_size_bounds_and_failure_is_not_cached(self):
        with media_png._STATE_LOCK, patch.object(media_png, "_CACHE_MAX_ITEMS", 2), patch.object(media_png, "_CACHE_MAX_BYTES", 4):
            media_png._save_png("a", b"12")
            media_png._save_png("b", b"34")
            self.assertEqual(media_png._cached_png("a"), b"12")
            media_png._save_png("c", b"56")
            self.assertIsNone(media_png._cached_png("b"))
            self.assertEqual(media_png._CACHE_BYTES, 4)
            with patch.object(media_png, "_CACHE_TTL_SECONDS", 0):
                media_png._save_png("expired", b"7")
            self.assertIsNone(media_png._cached_png("expired"))
        with patch.object(media_png, "rasterize_media_bytes", side_effect=[None, png_bytes()]) as raster:
            with self.assertRaises(media_png.SourceMediaRenderError):
                self.read()
            self.read()
            self.assertEqual(raster.call_count, 2)

    def test_pending_capacity_and_thread_creation_failure_are_bounded(self):
        with patch.object(media_png, "_MAX_PENDING_RENDERS", 0), patch.object(media_png, "rasterize_media_bytes") as raster:
            with self.assertRaises(media_png.SourceMediaRenderError) as error:
                self.read()
            self.assertEqual(error.exception.code, "busy")
            raster.assert_not_called()
        with patch.object(media_png.threading.Thread, "start", side_effect=RuntimeError("thread capacity")):
            with self.assertRaises(media_png.SourceMediaRenderError) as error:
                self.read()
            self.assertEqual(error.exception.code, "busy")
            self.assertFalse(media_png._FLIGHTS)

    def test_changed_source_bytes_cannot_reuse_previous_source_cache(self):
        with patch.object(media_png, "rasterize_media_bytes", return_value=png_bytes()) as raster:
            previous = self.read()
            changed = media_png.read_source_media_png(self.source + b"changed source", "pptx", {
                "source_page": "1", "source_part": "ppt/media/chart-a.emf"})
            self.assertNotEqual(previous.metadata["source_file_sha256"], changed.metadata["source_file_sha256"])
            self.assertEqual(raster.call_count, 2)

    def test_operational_status_is_preserved_by_both_rpc_methods(self):
        from docreader.proto.docreader_pb2 import ReadRequest
        from docreader.main import DocReaderServicer
        servicer = DocReaderServicer.__new__(DocReaderServicer)
        servicer.parser = object()
        for code in ("busy", "timeout", "cancelled", "render_failed"):
            with patch.object(servicer, "_parse_request", side_effect=media_png.SourceMediaRenderError(code)):
                unary = servicer.Read(ReadRequest(), None)
                frames = list(servicer.ReadStream(ReadRequest(), None))
                self.assertEqual(unary.metadata["source_media_error_code"], code)
                self.assertEqual(frames[0].meta.metadata["source_media_error_code"], code)


if __name__ == "__main__":
    unittest.main()
