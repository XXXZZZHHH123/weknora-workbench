"""Deployment-boundary tests; no models, API keys or parser runtime required."""
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace, ModuleType
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location('mineru_deployment_entrypoint', Path(__file__).with_name('entrypoint.py'))
entrypoint = importlib.util.module_from_spec(spec)
spec.loader.exec_module(entrypoint)


class DeploymentTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.settings_file = Path(self.temporary.name) / 'settings.json'
        self.clean_environment = patch.dict(os.environ, {}, clear=True)
        self.clean_environment.start()
        self.addCleanup(self.clean_environment.stop)
        os.environ['TMPDIR'] = str(Path(self.temporary.name) / 'scratch')
        self.saved_tempdir = tempfile.tempdir
        self.addCleanup(setattr, tempfile, 'tempdir', self.saved_tempdir)

    def settings(self, **fields):
        payload = {'api_key': 'synthetic-service-key-' * 3, 'vlm_mode': 'local', **fields}
        self.settings_file.write_text(json.dumps(payload))
        return entrypoint.load_settings(self.settings_file)

    def test_default_local_settings_and_resource_limits(self):
        settings = self.settings()
        self.assertEqual(settings['vlm_mode'], 'local')
        self.assertEqual(settings['parse_concurrency'], 1)
        self.assertEqual(settings['vlm_concurrency'], 2)
        self.assertEqual(settings['processing_window_size'], 8)
        for name, bad in [('MINERU_PARSE_CONCURRENCY', '0'), ('MINERU_PARSE_CONCURRENCY', '3'),
                          ('MINERU_VLM_CONCURRENCY', '100'), ('MINERU_RETENTION_SECONDS', '-1'),
                          ('MINERU_PROCESSING_WINDOW_SIZE', '0'), ('MINERU_PROCESSING_WINDOW_SIZE', '64')]:
            with self.subTest(name=name, value=bad), patch.dict(os.environ, {name: bad}):
                with self.assertRaises(ValueError):
                    self.settings()

    def test_render_scratch_is_writable_and_used_by_temporary_files(self):
        entrypoint.prepare_scratch(self.settings())
        scratch = Path(os.environ['TMPDIR'])
        with tempfile.TemporaryDirectory(prefix='docvortex-render-') as directory:
            self.assertEqual(Path(directory).parent, scratch)
            (Path(directory) / 'page.pixels').write_bytes(b'rendered-page')
        self.assertEqual(list(scratch.iterdir()), [])
        self.assertEqual(os.environ['MINERU_PROCESSING_WINDOW_SIZE'], '8')

    def test_invalid_scratch_prevents_model_start(self):
        os.environ['TMPDIR'] = str(self.settings_file)
        settings = self.settings()
        with self.assertRaises(OSError):
            entrypoint.prepare_scratch(settings)
        os.environ['TMPDIR'] = 'relative'
        with self.assertRaises(ValueError):
            entrypoint.prepare_scratch(settings)

    def test_local_mode_rejects_remote_values_and_unusable_bearer_keys(self):
        for fields in ({'api_key': ' ' * 40}, {'api_key': 'x' * 31}, {'api_key': 'x' * 40 + '\n'},
                       {'vlm_model': 'generic-vision-model'}, {'vlm_server_url': 'https://example.test'},
                       {'unknown': True}):
            with self.subTest(fields=list(fields)), self.assertRaises(ValueError):
                self.settings(**fields)

    def test_explicit_remote_mode_requires_complete_non_redirectable_origin(self):
        fields = dict(vlm_mode='remote', vlm_server_url='https://dedicated.example/prefix/v1',
                      vlm_api_key='fixture-remote-key', vlm_model='actual-dedicated-model')
        self.assertEqual(self.settings(**fields)['vlm_model'], 'actual-dedicated-model')
        for bad in ('https://user:password@example.test', 'https://example.test/?key=secret',
                    'https://example.test/#secret', 'file:///tmp/model', 'https://example.test:99999'):
            with self.subTest(url=bad), self.assertRaises(ValueError):
                self.settings(**{**fields, 'vlm_server_url': bad})

    def test_local_application_uses_official_engine_and_preloads_without_fallback(self):
        settings = self.settings()
        fake_config = ModuleType('mineru.config')
        fake_config.config = SimpleNamespace(model=SimpleNamespace(small_backend='onnx', source='local'),
            llm_aided=SimpleNamespace(features=SimpleNamespace(cross_page_table_cell_merge=False, title_leveling=False)))
        fake_config.VlmConfig = Mock(return_value=SimpleNamespace(validate_environment=Mock()))
        fake_api = ModuleType('mineru.parser.api_server')
        fake_api.create_app = Mock(return_value='application')
        packages = {'mineru.config': fake_config, 'mineru.parser.api_server': fake_api}
        def version(name):
            return {'mineru': '4.0.12', 'mineru-llama-cpp': '0.1.4'}[name]
        with patch.dict(sys.modules, packages), patch.object(entrypoint, 'version', side_effect=version):
            self.assertEqual(entrypoint.create_application(settings), 'application')
        self.assertEqual(fake_config.VlmConfig.call_args.kwargs['engine'], 'llama-cpp')
        self.assertEqual(fake_config.VlmConfig.call_args.kwargs['server_url'], '')
        args = fake_api.create_app.call_args.kwargs
        self.assertEqual(args['tier'], 'standard')
        self.assertTrue(args['preload_models'])
        self.assertTrue(args['no_flash'])
        self.assertTrue(args['no_advanced'])
        self.assertFalse(args['allow_local_source'])
        self.assertFalse(args['allow_http_source'])
        self.assertEqual(args['concurrency'], 1)
        self.assertEqual(os.environ['MINERU_MODEL_SOURCE'], 'local')

    def health_responses(self, health=None, tiers=None):
        health = health or {'status': 'ok', 'version': '4.0.12'}
        tiers = tiers if tiers is not None else ['basic', 'standard']
        bodies = [health, {'data': [{'id': item} for item in tiers]}]
        opener = Mock()
        def open_response(*args, **kwargs):
            response = io.BytesIO(json.dumps(bodies.pop(0)).encode())
            response.status = 200
            return response
        opener.open.side_effect = open_response
        return patch.object(entrypoint, 'build_opener', return_value=opener)

    def test_health_requires_exact_version_ready_and_standard_tier(self):
        with self.health_responses():
            self.assertEqual(entrypoint.healthcheck()['tiers'], ['basic', 'standard'])
        for health, tiers in [({'status': 'ok', 'version': '4.0.11'}, ['basic', 'standard']),
                              ({'status': 'failed', 'version': '4.0.12'}, ['basic', 'standard']),
                              (None, ['basic']), (None, ['basic', 'standard', 'advanced'])]:
            with self.subTest(health=health, tiers=tiers), self.health_responses(health, tiers):
                with self.assertRaises(ValueError):
                    entrypoint.healthcheck()

    def test_failed_checks_do_not_print_private_exception_values(self):
        marker = 'private-secret-never-display'
        stderr = io.StringIO()
        with patch.object(sys, 'argv', ['entrypoint.py', '--check']), \
             patch.object(entrypoint, 'load_settings', side_effect=ValueError(marker)), \
             patch.object(sys, 'stderr', stderr):
            self.assertEqual(entrypoint.main(), 1)
        self.assertNotIn(marker, stderr.getvalue())
        self.assertIn('ValueError', stderr.getvalue())


if __name__ == '__main__':
    unittest.main()
