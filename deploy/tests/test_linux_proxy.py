"""Proxy isolation and private configuration checks, without external services."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import shutil
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('linux_proxy_config', SCRIPTS/'linux-proxy-config.py')
config = importlib.util.module_from_spec(spec)
spec.loader.exec_module(config)


class LinuxProxyTests(unittest.TestCase):
    def check_with_mock_http(self, directory, github_code, authenticated=False):
        root=Path(directory)
        mock=root/'mockbin';mock.mkdir()
        docker=mock/'docker'
        docker.write_text('#!/bin/sh\n[ "$1" != inspect ] || echo isolated-image\n')
        docker.chmod(0o700)
        curl=mock/'curl'
        curl.write_text('''#!/usr/bin/env python3
import json,os,sys
from pathlib import Path
a=sys.argv[1:]
if a[-1]=='https://api.github.com':
 auth=Path(a[a.index('--config')+1]).read_text()
 Path(os.environ['HOME'],'auth-used').write_text(str('Bearer isolated_test_token' in auth))
 if 'isolated_test_token' in ' '.join(a): sys.exit(99)
 code=os.environ['TEST_GITHUB_CODE']
 Path(a[a.index('-D')+1]).write_text('X-RateLimit-Remaining: 0\\n' if code=='403' else '')
 Path(a[a.index('-o')+1]).write_text(json.dumps({'message':'API rate limit exceeded'} if code=='403' else {}))
 print(code,end='')
else: print('401',end='')
''')
        curl.chmod(0o700)
        if authenticated:
            token=root/'weknora-workbench/shared/github-token';token.parent.mkdir(parents=True)
            token.write_text('isolated_test_token\n')
        env=dict(os.environ,HOME=directory,PATH=str(mock)+':'+os.environ['PATH'],
                 WEKNORA_PROXY_SESSION='1',TEST_GITHUB_CODE=str(github_code))
        return subprocess.run(['bash',str(SCRIPTS/'linux-proxy.sh'),'check'],env=env,capture_output=True,text=True)

    def test_github_rate_limit_is_reported_without_skipping_ghcr_check(self):
        with tempfile.TemporaryDirectory() as directory:
            result=self.check_with_mock_http(directory,403)
            self.assertEqual(result.returncode,1,result.stderr)
            self.assertIn('not a proxy disconnection',result.stderr)
            self.assertIn('GHCR: HTTP 401',result.stdout)

    def test_check_uses_private_token_without_exposing_it_in_arguments_or_output(self):
        with tempfile.TemporaryDirectory() as directory:
            result=self.check_with_mock_http(directory,200,authenticated=True)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual((Path(directory)/'auth-used').read_text(),'True')
            self.assertNotIn('isolated_test_token',result.stdout+result.stderr)
            self.assertFalse(list((Path(directory)/'.local/share/weknora-proxy').glob('.github-auth.*')))

    def test_invalid_authentication_remains_a_failed_check(self):
        with tempfile.TemporaryDirectory() as directory:
            result=self.check_with_mock_http(directory,401,authenticated=True)
            self.assertEqual(result.returncode,1,result.stderr)
            self.assertIn('verify authentication',result.stderr)

    def test_latency_includes_failed_nodes_and_sorts_successful_nodes(self):
        with tempfile.TemporaryDirectory() as directory:
            mock=Path(directory)/'mockbin';mock.mkdir()
            for name,body in {
                'docker':'#!/bin/sh\necho isolated-image\n',
                'curl':'''#!/usr/bin/env python3
import json,sys
from pathlib import Path
a=sys.argv[1:]
result={'all':['slow','failed','fast']} if a[-1].endswith('/proxies/PROXY') else {'slow':200,'fast':50}
Path(a[a.index('-o')+1]).write_text(json.dumps(result))
''',
            }.items():
                path=mock/name;path.write_text(body);path.chmod(0o700)
            env=dict(os.environ,HOME=directory,PATH=str(mock)+':'+os.environ['PATH'],WEKNORA_PROXY_SESSION='1')
            result=subprocess.run(['bash',str(SCRIPTS/'linux-proxy.sh'),'latency'],env=env,capture_output=True,text=True)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual(result.stdout.splitlines()[1:],['50 ms\tfast','200 ms\tslow','timeout\tfailed'])

    def run_proxy_with_mock_docker(self, directory, action, state):
        root=Path(directory)
        mock=root/'mockbin'; mock.mkdir()
        docker=mock/'docker'
        docker.write_text('''#!/bin/sh
case "$1" in
 inspect)
   case "$3" in
     *Image*) echo isolated-image ;;
     *State.Status*) printf "%s\\n" "$TEST_PROXY_STATE" ;;
   esac ;;
 stop) printf "%s\\n" "$2" > "$HOME/stopped-container" ;;
 *) exit 1 ;;
esac
''')
        docker.chmod(0o700)
        if not shutil.which('flock'):
            lock=mock/'flock'
            lock.write_text('''#!/usr/bin/env python3
import fcntl,sys
flags=fcntl.LOCK_SH if '-s' in sys.argv else fcntl.LOCK_EX
if any('n' in a for a in sys.argv[1:-1]): flags |= fcntl.LOCK_NB
try: fcntl.flock(int(sys.argv[-1]),flags)
except BlockingIOError: sys.exit(1)
''')
            lock.chmod(0o700)
        env=dict(os.environ,HOME=directory,PATH=str(mock)+':'+os.environ['PATH'],TEST_PROXY_STATE=state)
        return subprocess.run(['bash',str(SCRIPTS/'linux-proxy.sh'),action],env=env,capture_output=True,text=True)

    def test_temporary_stop_keeps_deployment_configuration_and_backup(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            stage=root/'.local/share/weknora-proxy';stage.mkdir(parents=True)
            backup=stage/'deployment.before.json';backup.write_text('{"proxy":"previous-proxy"}')
            settings=root/'weknora-workbench/shared/poller.json';settings.parent.mkdir(parents=True)
            settings.write_text('{"proxy":"http://127.0.0.1:17891"}')
            result=self.run_proxy_with_mock_docker(directory,'stop','running')
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual((root/'stopped-container').read_text().strip(),'weknora-egress-proxy')
            self.assertEqual(settings.read_text(),'{"proxy":"http://127.0.0.1:17891"}')
            self.assertEqual(backup.read_text(),'{"proxy":"previous-proxy"}')

    def test_stopped_status_does_not_require_proxy_controller(self):
        with tempfile.TemporaryDirectory() as directory:
            result=self.run_proxy_with_mock_docker(directory,'status','exited')
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertIn('Stopped',result.stdout)
            self.assertNotIn('Current node',result.stdout)

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
