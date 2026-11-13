"""Preview performance contracts preserve document evidence and source selectors."""
import base64
import io
import json
import time
import unittest
import zipfile
from pathlib import Path
from unittest.mock import patch

from PIL import Image
from docreader.parser import pptx_media as media
from docreader.tests.test_pptx_media import fixture, MARKDOWN


class PreviewBudgetTests(unittest.TestCase):
    def setUp(self):
        media._RENDER_CACHE.clear()
        media._RENDER_CACHE_BYTES = 0

    def test_cpu_quota_limits_concurrency_for_v1_and_v2(self):
        def read_v1(path):
            if str(path).endswith('cpu.max'):
                raise FileNotFoundError
            return '35000' if str(path).endswith('quota_us') else '100000'
        for reader in (lambda _: '35000 100000', read_v1):
            with patch.object(Path, 'read_text', reader), patch.dict(media.os.environ, {'DOCREADER_VECTOR_RENDER_WORKERS': '4'}):
                self.assertEqual(media._vector_render_workers(), 1)
                self.assertEqual(media._cpu_capacity(), .35)

    def test_no_same_converter_retry_and_exact_originals_retained(self):
        stats, sources = {}, []
        with patch.object(media, '_rasterize_vector_batches', return_value={}), \
                patch.object(media, 'rasterize_media_bytes', return_value=None) as single:
            text, images = media.attach_pptx_media_to_markdown(MARKDOWN, fixture(), processing_stats=stats, image_sources=sources)
        self.assertEqual([call.args[0] for call in single.call_args_list], ['ppt/media/logo.png'])
        self.assertEqual({base64.b64decode(data) for data in images.values()}, {b'chart-a', b'chart-b'})
        self.assertEqual([(s['page'], s['source_part']) for s in sources], [(1, 'ppt/media/chart-a.emf'), (2, 'ppt/media/chart-b.emf')])
        self.assertEqual(stats['preview_status'], 'deferred')
        self.assertEqual(stats['deferred_vector_previews'], 2)
        self.assertIn('First text', text)
        self.assertIn('尚未生成预览', text)

    def test_valid_partial_batch_outputs_are_kept_without_retrying_failures(self):
        def convert(command, timeout):
            inputs = command[command.index('--outdir') + 2:]
            Image.new('RGB', (8, 8), 'black').save(Path(inputs[0]).with_suffix('.png'))
            return False  # Timeout after the first output was completely written.
        with zipfile.ZipFile(io.BytesIO(fixture())) as archive, \
                patch.object(media, 'find_soffice', return_value='/test/soffice'), \
                patch.object(media, '_cpu_capacity', return_value=4), \
                patch.object(media, '_run_conversion', side_effect=convert) as run:
            outputs = media._rasterize_vector_batches(archive, ['ppt/media/chart-a.emf', 'ppt/media/chart-b.emf'], time.monotonic()+10)
        self.assertEqual(list(outputs), ['ppt/media/chart-a.emf'])
        self.assertEqual(run.call_count, 1)

    def test_cache_reuses_only_same_content_format_and_renderer_version(self):
        first = io.BytesIO()
        with zipfile.ZipFile(first, 'w') as z:
            z.writestr('ppt/media/a.emf', b'original')
            z.writestr('ppt/media/b.emf', b'original')
        def convert(command, timeout):
            for src in command[command.index('--outdir') + 2:]:
                Image.new('RGB', (8, 8), 'black').save(Path(src).with_suffix('.png'))
            return True
        with zipfile.ZipFile(io.BytesIO(first.getvalue())) as z, \
                patch.object(media, 'find_soffice', return_value='/test/soffice'), \
                patch.object(media, '_run_conversion', side_effect=convert) as run:
            a = media._rasterize_vector_batches(z, z.namelist(), time.monotonic()+10)
            b = media._rasterize_vector_batches(z, z.namelist(), time.monotonic()+10)
            self.assertEqual(a, b)
            self.assertEqual(len(a), 2)
            self.assertEqual(run.call_count, 1)
            self.assertEqual(len(run.call_args.args[0][run.call_args.args[0].index('--outdir')+2:]), 1)
            with patch.object(media, '_RENDER_CACHE_VERSION', 'changed-renderer'):
                media._rasterize_vector_batches(z, z.namelist(), time.monotonic()+10)
            self.assertEqual(run.call_count, 2)
        self.assertNotEqual(media._render_key('a.emf', b'original'), media._render_key('a.emf', b'changed'))
        self.assertNotEqual(media._render_key('a.emf', b'original'), media._render_key('a.wmf', b'original'))

    def test_failure_is_not_negative_cached_and_cache_is_bounded(self):
        with patch.object(media, '_rasterize_media_uncached', side_effect=[None, b'png']) as render:
            self.assertIsNone(media.rasterize_media_bytes('a.emf', b'original'))
            self.assertEqual(media.rasterize_media_bytes('a.emf', b'original'), b'png')
            self.assertEqual(render.call_count, 2)
        with patch.object(media, '_RENDER_CACHE_MAX_BYTES', 4), patch.object(media, '_RENDER_CACHE_MAX_ITEMS', 1):
            media._cache_png('a', b'123')
            media._cache_png('b', b'123')
            self.assertIsNone(media._cached_png('a'))
            self.assertLessEqual(media._RENDER_CACHE_BYTES, 4)

    def test_expired_budget_launches_no_converter_and_parser_reports_deferred(self):
        with zipfile.ZipFile(io.BytesIO(fixture())) as z, patch.object(media, '_run_conversion') as run:
            self.assertEqual(media._rasterize_vector_batches(z, ['ppt/media/chart-a.emf'], time.monotonic()-1), {})
            run.assert_not_called()
        from types import SimpleNamespace
        from docreader.parser.markitdown_parser import StdMarkitdownParser
        with patch.object(StdMarkitdownParser, '_convert_markitdown', return_value=SimpleNamespace(text_content=MARKDOWN)), \
                patch.dict(media.os.environ, {'DOCREADER_PPTX_PREVIEW_BUDGET_SECONDS': '0'}), \
                patch.object(media, 'rasterize_media_bytes', return_value=None):
            doc = StdMarkitdownParser(file_name='synthetic.pptx', file_type='pptx').parse_into_text(fixture())
        status = json.loads(doc.metadata['pptx_media_processing_v1'])
        self.assertEqual(status['preview_status'], 'deferred')
        self.assertEqual(status['vector_parts'], 2)
