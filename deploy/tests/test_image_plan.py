import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('image_plan', ROOT / 'image_plan.py')
planner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(planner)
REPO = 'owner/workbench'
TAG = 'production-' + 'a' * 40 + '-1-1'


class ImagePlanTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.git('init', '-q')
        self.git('config', 'user.email', 'test@example.invalid')
        self.git('config', 'user.name', 'Test')
        for name in ('WeKnora/main.go', 'WeKnora/frontend/index.ts',
                     'WeKnora/docker/Dockerfile.app', 'deploy/Dockerfile.frontend',
                     'deploy/Dockerfile.runtime', 'structured-data/pyproject.toml',
                     'structured-data/Dockerfile', 'structured-data/src/server.py',
                     'structured-data/deploy/structured-proxy.conf', '.dockerignore'):
            self.write(name, 'initial')
        self.write('WeKnora/.dockerignore', 'frontend/\nWeKnora-Chrome-Extension/\n')
        self.commit()
        self.recipes = json.loads((ROOT / 'image-builds.json').read_text())
        self.baseline = {'repository': REPO, 'commit': self.git('rev-parse', 'HEAD').strip(),
                         'release_id': TAG, 'recipes': self.recipes}

    def tearDown(self):
        self.temp.cleanup()

    def git(self, *args):
        return subprocess.check_output(['git', *args], cwd=self.root, text=True, stderr=subprocess.DEVNULL)

    def write(self, name, value):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(value)

    def commit(self):
        self.git('add', '.')
        self.git('commit', '-qm', 'test')

    def rows(self, **kwargs):
        return planner.plan(self.root, REPO, self.recipes, self.baseline, **kwargs)['include']

    def test_docs_and_tests_do_not_change_runtime_images(self):
        self.write('deploy/README.md', 'documentation')
        self.write('structured-data/tests/test_example.py', 'test')
        self.commit()
        self.assertTrue(all(row['reuse_image'] for row in self.rows()))

    def test_structured_change_only_builds_that_service(self):
        self.write('structured-data/src/server.py', 'changed')
        self.commit()
        self.assertEqual([row['service'] for row in self.rows() if not row['reuse_image']], ['structured-data'])

    def test_frontend_proxy_change_does_not_rebuild_dependency_images(self):
        self.write('structured-data/deploy/structured-proxy.conf', 'changed')
        self.commit()
        self.assertEqual([row['service'] for row in self.rows() if not row['reuse_image']], ['frontend'])

    def test_frontend_source_excluded_from_backend_context_only_rebuilds_frontend(self):
        self.write('WeKnora/frontend/index.ts', 'changed')
        self.commit()
        self.assertEqual([row['service'] for row in self.rows() if not row['reuse_image']], ['frontend'])

    def test_base_image_version_change_rebuilds_only_that_dependency(self):
        self.recipes = [dict(row) for row in self.recipes]
        next(row for row in self.recipes if row['service'] == 'redis')['base_image'] = 'redis:7.2-alpine'
        self.assertEqual([row['service'] for row in self.rows() if not row['reuse_image']], ['redis'])

    def test_previous_modern_release_wins_over_initial_baseline(self):
        rows = self.rows()
        previous = {}
        tag = 'production-' + 'b' * 40 + '-2-1'
        for row in rows:
            previous[planner.key(row['service']) + '_IMAGE'] = f"ghcr.io/{REPO}-{row['service']}:{tag}"
            previous[planner.key(row['service']) + '_INPUT_HASH'] = row['input_hash']
        self.assertTrue(all(row['reuse_image'].endswith(tag) for row in self.rows(previous=previous)))
        self.assertTrue(all(not row['reuse_image'] for row in self.rows(previous=previous, force=True)))

    def test_fork_does_not_reuse_private_original_repository_images(self):
        rows = planner.plan(self.root, 'someone/fork', self.recipes, self.baseline)['include']
        self.assertTrue(all(not row['reuse_image'] for row in rows))

    def test_release_records_six_independent_references_and_rejects_missing_service(self):
        coordinates = self.root / 'coordinates'
        coordinates.mkdir()
        for row in self.rows():
            (coordinates / (row['service'] + '.json')).write_text(json.dumps({
                'service': row['service'], 'image': row['reuse_image'], 'input_hash': row['input_hash']}))
        output = self.root / 'release.env'
        planner.write_release(coordinates, REPO, TAG, output)
        self.assertIn('STRUCTURED_DATA_IMAGE=ghcr.io/owner/workbench-structured-data:', output.read_text())
        (coordinates / 'redis.json').unlink()
        with self.assertRaises(ValueError):
            planner.write_release(coordinates, REPO, TAG, output)


if __name__ == '__main__':
    unittest.main()
