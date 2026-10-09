"""Exercise the real shell scripts with isolated Docker and HTTP stand-ins.

These tests cover promotion/failure/backup/polling decisions, not container
compatibility or application correctness on the production host.
"""
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
TAG = 'production-' + 'a' * 40 + '-123-1'
OLD = 'production-' + 'b' * 40 + '-122-1'


class DeploymentTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name).resolve() / 'workbench'
        self.shared = self.root / 'shared'
        self.shared.mkdir(parents=True)
        (self.root / 'releases').mkdir()
        (self.shared / 'backups').mkdir()
        (self.shared / '.env').write_text('DB_USER=test\nDB_PASSWORD=test\nDB_NAME=test\nREDIS_PASSWORD=test\nSTRUCTURED_TENANT_ID=10000\n')
        (self.shared / 'poller.json').write_text(json.dumps({'repository': 'owner/repo', 'proxy': ''}))
        self.mockbin = self.root / 'mockbin'
        self.mockbin.mkdir()
        self.log = self.root / 'docker.log'
        self.http_log = self.root / 'http.log'
        self.env = dict(os.environ, PATH=str(self.mockbin) + ':' + os.environ['PATH'], MOCK_ROOT=str(self.root))
        self.executable('docker', '''#!/usr/bin/env python3
import os, sys
from pathlib import Path
r=Path(os.environ['MOCK_ROOT']); a=sys.argv[1:]
with (r/'docker.log').open('a') as f: f.write(' '.join(a)+'\\n')
if 'info' in a: print(str(r))
elif 'ps' in a and (r/'existing-db').exists(): print('existing-postgres')
elif 'exec' in a and 'pg_dump' in ' '.join(a):
 if (r/'fail-backup').exists(): sys.exit(1)
 print('database-backup')
elif 'up' in a and (r/'fail-up').exists(): sys.exit(1)
elif 'exec' in a and 'curl' in a: print('200' if (r/'bad-api-proxy').exists() else '401', end='')
''')
        # This test stand-in emits healthy structured JSON for the probe command.
        with (self.mockbin / 'docker').open('a') as f:
            f.write("elif 'exec' in a and 'frontend' in a: print('')\n")
        self.executable('curl', '''#!/usr/bin/env python3
import os, sys
from pathlib import Path
r=Path(os.environ['MOCK_ROOT']); a=sys.argv[1:]; url=a[-1]
with (r/'http.log').open('a') as f: f.write(('auth ' if '--config' in a else 'anonymous ')+url+'\\n')
out=Path(a[a.index('-o')+1])
if url.endswith('/latest'):
 out.write_bytes((r/'latest.json').read_bytes()); print('200', end='')
elif '/releases/assets/' in url:
 aid=url.rsplit('/',1)[1]
 Path(a[a.index('-D')+1]).write_text('Location: https://release-assets.githubusercontent.com/'+aid+'\\r\\n')
 out.write_bytes(b''); print('302', end='')
elif 'release-assets.githubusercontent.com/' in url:
 out.write_bytes((r/('bundle.tar.gz' if url.endswith('/1') else 'bundle.sha256')).read_bytes())
else: sys.exit(1)
''')
        if not shutil.which('flock'):
            self.executable('flock', '#!/bin/sh\nexit 0\n')
        # Production uses GNU mv on Linux. Keep tests runnable on macOS too.
        if os.uname().sysname == 'Darwin':
            self.executable('mv', '''#!/usr/bin/env python3
import os, subprocess, sys
if sys.argv[1]=='-Tf': os.replace(sys.argv[2],sys.argv[3])
else: sys.exit(subprocess.call(['/bin/mv']+sys.argv[1:]))
''')

    def tearDown(self):
        self.temp.cleanup()

    def executable(self, name, text):
        path = self.mockbin / name
        path.write_text(text)
        path.chmod(0o700)

    def release(self, tag=TAG):
        release = self.root / 'releases' / tag
        release.mkdir(exist_ok=True)
        for name in ('compose.yml', 'deploy.sh'):
            shutil.copy(SCRIPTS / name, release / name)
        (release / 'release.env').write_text('IMAGE_PREFIX=ghcr.io/owner/repo\nIMAGE_TAG=' + tag + '\n')
        return release

    def previous(self):
        old = self.release(OLD)
        (self.root / 'current').symlink_to(old)
        return old

    def run_script(self, name, *args):
        return subprocess.run(['bash', str(SCRIPTS / name), str(self.root)] + list(args), env=self.env,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

    def bundle(self, malicious=False):
        data = {}
        for name in ('compose.yml', 'deploy.sh'):
            data[name] = (SCRIPTS / name).read_bytes()
        data['release.env'] = ('IMAGE_PREFIX=ghcr.io/owner/repo\nIMAGE_TAG=' + TAG + '\n').encode()
        if malicious:
            data['../outside'] = b'bad'
        bundle = self.root / 'bundle.tar.gz'
        with tarfile.open(str(bundle), 'w:gz') as tar:
            for name, body in data.items():
                member = tarfile.TarInfo(name)
                member.size = len(body)
                tar.addfile(member, io.BytesIO(body))
        (self.root / 'bundle.sha256').write_text(hashlib.sha256(bundle.read_bytes()).hexdigest() + '  deployment.tar.gz\n')
        (self.root / 'latest.json').write_text(json.dumps({'tag_name': TAG, 'draft': False, 'prerelease': False, 'assets': [
            {'name': 'deployment.tar.gz', 'url': 'https://api.github.com/repos/owner/repo/releases/assets/1'},
            {'name': 'deployment.tar.gz.sha256', 'url': 'https://api.github.com/repos/owner/repo/releases/assets/2'}]}))

    def test_success_promotes_and_preserves_previous(self):
        old = self.previous()
        new = self.release()
        result = self.run_script('deploy.sh', TAG)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.root / 'current').resolve(), new)
        self.assertEqual((self.root / 'previous').resolve(), old)

    def test_unhealthy_release_does_not_promote(self):
        old = self.previous()
        self.release()
        (self.root / 'fail-up').touch()
        result = self.run_script('deploy.sh', TAG)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.root / 'current').resolve(), old)
        self.assertIn('current pointer was not changed', result.stderr)

    def test_spa_fallback_at_api_route_is_not_healthy(self):
        old = self.previous()
        self.release()
        (self.root / 'bad-api-proxy').touch()
        result = self.run_script('deploy.sh', TAG)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('API proxy probe failed', result.stderr)
        self.assertEqual((self.root / 'current').resolve(), old)

    def test_failed_backup_prevents_service_update(self):
        old = self.previous()
        self.release()
        (self.root / 'existing-db').touch()
        (self.root / 'fail-backup').touch()
        result = self.run_script('deploy.sh', TAG)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn(' up ', self.log.read_text())
        self.assertEqual((self.root / 'current').resolve(), old)

    def test_existing_database_is_backed_up_before_update(self):
        self.release()
        (self.root / 'existing-db').touch()
        result = self.run_script('deploy.sh', TAG)
        self.assertEqual(result.returncode, 0, result.stderr)
        backups = list((self.shared / 'backups').glob('*.pg.dump'))
        self.assertEqual(len(backups), 1)
        self.assertIn('database-backup', backups[0].read_text())
        log = self.log.read_text()
        self.assertLess(log.index('pg_dump'), log.index(' up '))

    def test_poller_records_failure_then_suppresses_retries(self):
        self.bundle()
        (self.root / 'fail-up').touch()
        result = self.run_script('poll.sh')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertEqual((self.shared / 'failed-release').read_text().strip(), TAG)
        calls = self.log.read_text()
        result = self.run_script('poll.sh')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.log.read_text(), calls)
        (self.root / 'fail-up').unlink()
        result = self.run_script('poll.sh', '--retry-failed')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.shared / 'failed-release').exists())

    def test_unchanged_and_paused_versions_do_not_restart(self):
        self.release()
        (self.root / 'current').symlink_to(self.root / 'releases' / TAG)
        self.bundle()
        result = self.run_script('poll.sh')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.log.exists())
        self.http_log.unlink()
        (self.shared / 'PAUSED').touch()
        result = self.run_script('poll.sh')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.http_log.exists())

    def test_corrupt_bundle_cannot_run_deployment(self):
        self.bundle()
        (self.root / 'bundle.tar.gz').write_bytes(b'corrupt')
        result = self.run_script('poll.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.log.exists())

    def test_path_traversal_bundle_is_rejected(self):
        self.bundle(malicious=True)
        result = self.run_script('poll.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'outside').exists())
        self.assertFalse(self.log.exists())

    def test_redirect_drops_authentication(self):
        self.bundle()
        (self.shared / 'github-token').write_text('ghp_test_token')
        (self.root / 'fail-up').touch()
        self.run_script('poll.sh')
        lines = self.http_log.read_text().splitlines()
        self.assertTrue(any(line.startswith('auth https://api.github.com/') for line in lines))
        self.assertTrue(any(line.startswith('anonymous https://release-assets.githubusercontent.com/') for line in lines))
        self.assertFalse(any(line.startswith('auth https://release-assets.githubusercontent.com/') for line in lines))
