"""Exercise the real shell scripts with isolated Docker and HTTP stand-ins.

These tests cover promotion/failure/backup/manual update decisions, not container
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
elif 'image' in a and 'inspect' in a: sys.exit(1)
elif 'config' in a and '--images' in a: print('ghcr.io/owner/repo-app:test\\nghcr.io/owner/repo-frontend:test')
elif 'load' in a: sys.stdin.read(); print('Loaded image')
elif 'ps' in a and 'postgres' in a and (r/'existing-db').exists(): print('existing-postgres')
elif 'ps' in a and 'commodity-research' in a and (r/'existing-research').exists(): print('existing-research')
elif 'exec' in a and 'commodity-research' in a:
 if (r/'fail-research-backup').exists(): sys.exit(1)
 import io, tarfile
 stream=io.BytesIO()
 with tarfile.open(fileobj=stream, mode='w:gz'): pass
 sys.stdout.buffer.write(stream.getvalue())
elif 'exec' in a and 'pg_dump' in ' '.join(a):
 if (r/'fail-backup').exists(): sys.exit(1)
 (r/'shared'/'backups'/a[-1]).write_text('database-backup')
elif 'up' in a and (r/'fail-up').exists(): sys.exit(1)
elif 'exec' in a and 'curl' in a:
 if '/research/static/' in a[-1]:
  print('404 text/plain' if (r/'bad-research-static').exists() else ('200 text/html' if (r/'research-static-spa').exists() else ('200 text/css' if a[-1].endswith('.css') else '200 text/javascript')), end='')
 else: print('200' if (r/'bad-api-proxy').exists() or ('http://frontend/research/api/board' in a and (r/'bad-research-proxy').exists()) else '401', end='')
elif 'exec' in a and 'structured-data' in a and 'python' in a:
 print((r/'public-origin').read_text() if (r/'public-origin').exists() else '')
''')
        # This test stand-in emits healthy structured JSON for the probe command.
        with (self.mockbin / 'docker').open('a') as f:
            f.write("elif 'exec' in a and 'frontend' in a:\n if '--header' in ' '.join(a) and (r/'bad-public-host').exists(): sys.exit(1)\n print('')\n")
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
        # Tests simulate server capacity independently of runner disk usage.
        self.executable('df', '#!/bin/sh\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted\\n/dev/test 41943040 10485760 31457280 25%% /test\\n"\n')
        self.executable('regctl', '''#!/usr/bin/env python3
import os, sys
from pathlib import Path
r=Path(os.environ['MOCK_ROOT'])
with (r/'exports.log').open('a') as f: f.write(' '.join(sys.argv[1:])+'\\n')
if (r/'fail-export').exists(): sys.exit(1)
print('test-image-archive')
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
            self.executable('sed', '''#!/usr/bin/env python3
import subprocess, sys
args=['-i','']+sys.argv[2:] if sys.argv[1]=='-i' else sys.argv[1:]
sys.exit(subprocess.call(['/usr/bin/sed']+args))
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

    def test_public_host_is_checked_before_promotion(self):
        old = self.previous()
        new = self.release()
        (self.root / 'public-origin').write_text('http://10.11.60.130:18090')
        (self.root / 'bad-public-host').touch()
        result = self.run_script('deploy.sh', TAG)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.root / 'current').resolve(), old)
        self.assertIn('sh 10.11.60.130:18090', self.log.read_text())
        (self.root / 'bad-public-host').unlink()
        result = self.run_script('deploy.sh', TAG)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.root / 'current').resolve(), new)

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

    def test_research_backup_is_completed_before_service_update(self):
        self.release()
        (self.root / 'existing-research').touch()
        result = self.run_script('deploy.sh', TAG)
        self.assertEqual(result.returncode, 0, result.stderr)
        backups = list((self.shared / 'backups').glob('*.research.tar.gz'))
        self.assertEqual(len(backups), 1)
        with tarfile.open(backups[0], 'r:gz') as archive:
            self.assertEqual(archive.getmembers(), [])
        log = self.log.read_text()
        self.assertLess(log.index('/app/deploy/container/backup.py'), log.index(' up '))

    def test_failed_research_backup_aborts_before_update(self):
        old = self.previous()
        self.release()
        (self.root / 'existing-research').touch()
        (self.root / 'fail-research-backup').touch()
        result = self.run_script('deploy.sh', TAG)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn(' up ', self.log.read_text())
        self.assertEqual((self.root / 'current').resolve(), old)
        self.assertEqual(list((self.shared / 'backups').glob('*.tmp')), [])

    def test_missing_research_stylesheet_cannot_promote_healthy_html(self):
        old = self.previous()
        self.release()
        (self.root / 'bad-research-static').touch()
        result = self.run_script('deploy.sh', TAG)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Research static proxy probe failed: app.css 404', result.stderr)
        self.assertEqual((self.root / 'current').resolve(), old)

    def test_research_asset_spa_fallback_cannot_promote(self):
        old = self.previous()
        self.release()
        (self.root / 'research-static-spa').touch()
        result = self.run_script('deploy.sh', TAG)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Research static proxy probe failed: app.css 200 text/html', result.stderr)
        self.assertEqual((self.root / 'current').resolve(), old)

    def test_research_route_must_reject_unauthenticated_data(self):
        old = self.previous()
        self.release()
        (self.root / 'bad-research-proxy').touch()
        result = self.run_script('deploy.sh', TAG)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Research auth proxy probe failed', result.stderr)
        self.assertEqual((self.root / 'current').resolve(), old)

    def test_full_docker_filesystem_prevents_image_download(self):
        old = self.previous()
        self.release()
        self.executable('df', '#!/bin/sh\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted\\n/dev/test 100 100 0 100%% /test\\n"\n')
        result = self.run_script('deploy.sh', TAG)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('15 GiB free', result.stderr)
        self.assertNotIn(' pull ', self.log.read_text())
        self.assertEqual((self.root / 'current').resolve(), old)

    def test_bootstrap_keeps_existing_secrets_on_second_run(self):
        (self.shared / '.env').unlink()
        result = self.run_script('bootstrap.sh', 'owner/repo')
        self.assertEqual(result.returncode, 0, result.stderr)
        first = (self.shared / '.env').read_text()
        config = dict(line.split('=', 1) for line in first.splitlines() if line and not line.startswith('#'))
        self.assertEqual(len(config['SYSTEM_AES_KEY']), 32)
        self.assertEqual(len(config['JWT_SECRET']), 64)
        self.assertEqual((self.shared / '.env').stat().st_mode & 0o777, 0o600)
        result = self.run_script('bootstrap.sh', 'owner/repo')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.shared / '.env').read_text(), first)
        token = self.shared / 'research-service-token'
        self.assertEqual(len(token.read_text().strip()), 64)
        self.assertEqual(token.stat().st_mode & 0o777, 0o600)

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

    def test_regctl_imports_each_image_without_daemon_pull(self):
        self.release()
        (self.shared / 'poller.json').write_text(json.dumps({'repository': 'owner/repo', 'image_transport': 'regctl'}))
        result = self.run_script('deploy.sh', TAG)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len((self.root / 'exports.log').read_text().splitlines()), 2)
        self.assertNotIn(' pull ', self.log.read_text())
        self.assertIn('--pull never', self.log.read_text())

    def test_failed_image_export_prevents_service_update(self):
        self.release()
        (self.shared / 'poller.json').write_text(json.dumps({'repository': 'owner/repo', 'image_transport': 'regctl'}))
        (self.root / 'fail-export').touch()
        result = self.run_script('deploy.sh', TAG)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn(' up ', self.log.read_text())

    def test_updater_reports_previous_failure_and_requires_explicit_retry(self):
        self.bundle()
        (self.root / 'fail-up').touch()
        result = self.run_script('poll.sh')
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertEqual((self.shared / 'failed-release').read_text().strip(), TAG)
        calls = self.log.read_text()
        result = self.run_script('poll.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('--retry-failed', result.stderr)
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
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('paused', result.stderr)
        self.assertFalse(self.http_log.exists())

    def test_installed_manual_update_defaults_to_its_server_directory(self):
        result = self.run_script('bootstrap.sh', 'owner/repo')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.release()
        (self.root / 'current').symlink_to(self.root / 'releases' / TAG)
        self.bundle()
        result = subprocess.run(['bash', str(self.root / 'bin' / 'update.sh')], env=self.env,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('Already running the latest release', result.stdout)
        self.assertIn('Already running the latest release', (self.shared / 'deploy.log').read_text())
        self.assertEqual(sum('/latest' in line for line in self.http_log.read_text().splitlines()), 1)
        self.assertNotIn(' up ', self.log.read_text())

    def test_manual_update_logs_failure_and_preserves_exit_status(self):
        self.bundle()
        (self.root / 'fail-up').touch()
        result = self.run_script('update.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Deployment failed', (self.shared / 'deploy.log').read_text())
        self.assertEqual((self.shared / 'failed-release').read_text().strip(), TAG)
        (self.root / 'fail-up').unlink()
        result = self.run_script('update.sh', '--retry-failed')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.root / 'current').resolve(), self.root / 'releases' / TAG)

    def test_linux_proxy_update_enters_session_and_preserves_poll_result(self):
        result=self.run_script('bootstrap.sh','owner/repo')
        self.assertEqual(result.returncode,0,result.stderr)
        (self.shared/'poller.json').write_text(json.dumps({'repository':'owner/repo','proxy':'http://127.0.0.1:17891'}))
        session=self.root/'bin/proxy-session.sh'
        session.write_text('#!/bin/sh\nprintf session > "$MOCK_ROOT/session-used"\n"$@"\n')
        (self.shared/'PAUSED').touch()
        result=subprocess.run(['bash',str(self.root/'bin/update.sh')],env=self.env,capture_output=True,text=True)
        self.assertEqual(result.returncode,1,result.stderr)
        self.assertTrue((self.root/'session-used').exists())
        self.assertIn('paused',(self.shared/'deploy.log').read_text())
        (self.shared/'PAUSED').unlink()
        self.release()
        (self.root/'current').symlink_to(self.root/'releases'/TAG)
        self.bundle()
        result=subprocess.run(['bash',str(self.root/'bin/update.sh')],env=self.env,capture_output=True,text=True)
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertIn('Already running the latest release',result.stdout)

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
