import unittest
from unittest.mock import patch

from docreader.parser import pptx_chart_geometry, pptx_media
from docreader.tests.test_pptx_media import MARKDOWN, fixture


class TestPptxMediaIndexGeometry(unittest.TestCase):
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
