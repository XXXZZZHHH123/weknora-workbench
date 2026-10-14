"""Proxy isolation and private configuration checks, without external services."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('linux_proxy_config', SCRIPTS/'linux-proxy-config.py')
config = importlib.util.module_from_spec(spec)
spec.loader.exec_module(config)


class LinuxProxyTests(unittest.TestCase):
    def test_subscription_import_cannot_control_host_network_settings(self):
        result = config.build_config('https://example.invalid/sub?tun=true&allow-lan=true')
        self.assertFalse(result['allow-lan'])
        self.assertEqual(result['bind-address'], '127.0.0.1')
        self.assertFalse(result['dns']['enable'])
        self.assertFalse(result['tun']['enable'])
        self.assertFalse(result['tun']['auto-route'])
        self.assertFalse(result['tun']['auto-redirect'])
        self.assertEqual(result['redir-port'], 0)
        self.assertEqual(result['tproxy-port'], 0)
        self.assertEqual(result['proxy-providers']['subscription']['type'], 'http')

    def test_private_subscription_is_not_printed_and_files_are_owner_only(self):
        with tempfile.TemporaryDirectory() as directory:
            url = 'https://example.invalid/sub?token=isolated-test-secret'
            result = subprocess.run(['python3', str(SCRIPTS/'linux-proxy-config.py'), directory],
                                    input=url, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertNotIn(url, result.stdout+result.stderr)
            self.assertNotIn('isolated-test-secret', result.stdout+result.stderr)
            for name in ('config.json', 'control.curl'):
                self.assertEqual((Path(directory)/'config'/name).stat().st_mode & 0o777, 0o600)
            stored = json.loads((Path(directory)/'config'/'config.json').read_text())
            self.assertEqual(stored['proxy-providers']['subscription']['url'], url)
        for url in ('http://example.invalid/sub', 'file:///tmp/sub', 'https://example.invalid/sub\nmalicious'):
            with self.assertRaises(ValueError):
                config.build_config(url)

    def test_wrapper_scopes_proxy_to_child_and_propagates_failure(self):
        env = dict(os.environ, HTTPS_PROXY='https://old-proxy.invalid:123', NO_PROXY='existing.invalid')
        result = subprocess.run(['bash', str(SCRIPTS/'with-proxy.sh'), 'sh', '-c',
                                 'printf "%s|%s" "$HTTPS_PROXY" "$NO_PROXY"; exit 7'],
                                env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 7)
        self.assertTrue(result.stdout.startswith('http://127.0.0.1:17891|'))
        self.assertIn('10.0.0.0/8', result.stdout)
        self.assertEqual(env['HTTPS_PROXY'], 'https://old-proxy.invalid:123')
        self.assertEqual(env['NO_PROXY'], 'existing.invalid')
