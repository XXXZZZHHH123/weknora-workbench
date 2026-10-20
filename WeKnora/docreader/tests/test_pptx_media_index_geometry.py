import unittest
import json
from types import SimpleNamespace
from unittest.mock import patch

from docreader.parser import pptx_chart_geometry, pptx_media
from docreader.tests.test_pptx_media import MARKDOWN, fixture


class TestPptxMediaIndexGeometry(unittest.TestCase):
    def test_deferred_vectors_survive_the_complete_markitdown_pipeline(self):
        from docreader.parser.markitdown_parser import MarkitdownParser, StdMarkitdownParser
        with (
            patch.object(StdMarkitdownParser, "_convert_markitdown", return_value=SimpleNamespace(text_content=MARKDOWN)),
            patch.object(pptx_media, "_rasterize_vector_batches", return_value={}),
            patch.object(pptx_media, "rasterize_media_bytes", return_value=None),
        ):
            result = MarkitdownParser(file_name="synthetic.pptx").parse_into_text(fixture())
        sources = json.loads(result.metadata['pptx_image_sources_v1'])
        self.assertEqual(len(sources), 2)
        self.assertEqual(len(result.images), 2)
        for source in sources:
            self.assertIn(source['original_ref'], result.images)
            self.assertIn(']('+source['original_ref']+')', result.content)

    def test_preview_budget_exhaustion_retains_original_vectors_and_bindings(self):
        import base64
        sources = []
        with (
            patch.object(pptx_media, "_rasterize_vector_batches", return_value={}),
            patch.object(pptx_media, "rasterize_media_bytes", return_value=None),
        ):
            text, images = pptx_media.attach_pptx_media_to_markdown(MARKDOWN, fixture(), image_sources=sources)
        self.assertEqual([(s['page'],s['source_part']) for s in sources],[(1,'ppt/media/chart-a.emf'),(2,'ppt/media/chart-b.emf')])
        self.assertEqual({base64.b64decode(v) for v in images.values()},{b'chart-a',b'chart-b'})
        self.assertTrue(all(s['original_ref'].endswith('.emf') for s in sources))
        self.assertTrue(all(']('+s['original_ref']+')' in text for s in sources))
        self.assertNotIn('原图曲线几何证据',text)

    def test_parser_transports_relationships_as_metadata(self):
        from docreader.parser.markitdown_parser import StdMarkitdownParser
        parser = StdMarkitdownParser(file_name="synthetic.pptx")
        with (
            patch.object(parser, "_convert_markitdown", return_value=SimpleNamespace(text_content=MARKDOWN)),
            patch.object(pptx_media, "_rasterize_vector_batches", return_value={
                "ppt/media/chart-a.emf": b"a", "ppt/media/chart-b.emf": b"b"}),
            patch.object(pptx_media, "rasterize_media_bytes", return_value=b"png"),
        ):
            result = parser.parse_into_text(fixture())
        sources = json.loads(result.metadata['pptx_image_sources_v1'])
        self.assertEqual(len(sources), 3)
        self.assertTrue(all(s['original_ref'] in result.images for s in sources))

    def test_structured_relationships_come_from_exact_slide_parts(self):
        sources = []
        with (
            patch.object(pptx_media, "_rasterize_vector_batches", return_value={
                "ppt/media/chart-a.emf": b"a", "ppt/media/chart-b.emf": b"b"}),
            patch.object(pptx_media, "rasterize_media_bytes", return_value=b"png"),
        ):
            text, images = pptx_media.attach_pptx_media_to_markdown(MARKDOWN, fixture(), image_sources=sources)
        self.assertEqual([(s['page'], s['source_part']) for s in sources], [
            (1, 'ppt/media/logo.png'), (1, 'ppt/media/chart-a.emf'), (2, 'ppt/media/chart-b.emf')])
        for source in sources:
            self.assertIn(source['original_ref'], images)
            self.assertIn('](' + source['original_ref'] + ')', text)
        self.assertFalse(any('unrelated' in s['source_part'] or '..' in s['source_part'] for s in sources))
        self.assertNotIn('原图曲线几何证据', text)

    def test_index_keeps_source_text_and_image_binding_without_geometry_estimates(self):
        markdown = MARKDOWN.replace(
            "First text",
            "First text\n\n| Transect | Count |\n| --- | --- |\n| North ridge | 6 |",
        )
        vector_targets = {
            "ppt/media/chart-a.emf": b"cached-chart-a",
            "ppt/media/chart-b.emf": b"cached-chart-b",
        }
        with (
            patch.object(pptx_media, "_rasterize_vector_batches", return_value=vector_targets),
            patch.object(pptx_media, "rasterize_media_bytes", return_value=b"mock-png"),
            patch.object(pptx_media, "extract_emf_labels", return_value=["Rainfall trend", "mm/month"]),
            patch.object(pptx_media, "estimate_emf_line_chart", create=True) as line_estimator,
            patch.object(pptx_media, "estimate_emf_chart_trends", create=True) as trend_estimator,
        ):
            indexed_text, images = pptx_media.attach_pptx_media_to_markdown(markdown, fixture())

        first_page, second_page = indexed_text.split("<!-- Slide number: 2 -->")
        self.assertIn("| Transect | Count |", first_page)
        self.assertIn("North ridge | 6", first_page)
        self.assertIn("ppt/media/chart-a.emf", first_page)
        self.assertNotIn("ppt/media/chart-b.emf", first_page)
        self.assertIn("ppt/media/chart-b.emf", second_page)
        self.assertNotIn("ppt/media/chart-a.emf", second_page)
        self.assertIn("Rainfall trend", first_page)
        self.assertIn("mm/month", first_page)
        self.assertIn("原图图例和坐标刻度是标识，不是观察值", first_page)
        self.assertNotIn("原图曲线几何证据", indexed_text)
        self.assertNotIn("原图采样", indexed_text)
        self.assertEqual(len(images), 3)
        self.assertEqual(set(images.values()), {"bW9jay1wbmc=", "Y2FjaGVkLWNoYXJ0LWE=", "Y2FjaGVkLWNoYXJ0LWI="})
        line_estimator.assert_not_called()
        trend_estimator.assert_not_called()


if __name__ == "__main__":
    unittest.main()
