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
                     'WeKnora/docker/Dockerfile.app', 'WeKnora/docker/Dockerfile.docreader',
                     'WeKnora/docreader/parser/pptx_media.py', 'deploy/Dockerfile.frontend',
                     'deploy/Dockerfile.runtime', 'structured-data/pyproject.toml',
                     'structured-data/Dockerfile', 'structured-data/src/server.py',
                     'structured-data/deploy/structured-proxy.conf',
                     'commodity-research/deploy/research-proxy.conf', 'commodity-research/Dockerfile',
                     'commodity-research/src/tin/web/app.py', 'commodity-research/alembic/versions/initial.py',
                     'commodity-research/deploy/container/backup.py', 'commodity-research/scripts/run_workspace.py', '.dockerignore'):
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

    def test_research_source_and_migration_only_rebuild_research(self):
        self.write('commodity-research/src/tin/web/app.py', 'changed')
        self.write('commodity-research/alembic/versions/initial.py', 'new migration')
        self.commit()
        self.assertEqual([row['service'] for row in self.rows() if not row['reuse_image']], ['commodity-research'])

    def test_manual_workspace_runner_only_rebuilds_research(self):
        self.write('commodity-research/scripts/run_workspace.py', 'changed')
        self.commit()
        self.assertEqual([row['service'] for row in self.rows() if not row['reuse_image']], ['commodity-research'])

    def test_research_proxy_only_rebuilds_frontend(self):
        self.write('commodity-research/deploy/research-proxy.conf', 'changed')
        self.commit()
        self.assertEqual([row['service'] for row in self.rows() if not row['reuse_image']], ['frontend'])

    def test_build_manifest_and_production_compose_use_the_same_seven_services(self):
        import re
        compose = (ROOT / 'compose.yml').read_text().split('\nvolumes:')[0]
        services = re.findall(r'^  ([a-z-]+):$', compose, re.MULTILINE)
        self.assertEqual(sorted(services), sorted(planner.SERVICES))
        self.assertEqual(len(services), 7)
        self.assertEqual(sorted(row['service'] for row in self.recipes), sorted(services))

    def test_frontend_source_excluded_from_backend_context_only_rebuilds_frontend(self):
        self.write('WeKnora/frontend/index.ts', 'changed')
        self.commit()
        self.assertEqual([row['service'] for row in self.rows() if not row['reuse_image']], ['frontend'])

    def test_native_excel_size_limit_is_consistent_across_deployed_services(self):
        import re
        compose = (ROOT / 'compose.yml').read_text().split('\nvolumes:')[0]
        limits = {}
        for service in ('frontend', 'app', 'structured-data', 'docreader'):
            block = re.search(r'^  ' + service + r':\n(.*?)(?=^  [a-z-]+:|\Z)', compose, re.MULTILINE | re.DOTALL)
            self.assertIsNotNone(block, service + ' must be present in the deployment')
            setting = re.search(r'^      MAX_FILE_SIZE_MB: "(\d+)"$', block.group(1), re.MULTILINE)
            self.assertIsNotNone(setting, service + ' must set the native original size limit explicitly')
            limits[service] = int(setting.group(1))
        self.assertEqual(limits, dict.fromkeys(limits, 100))
        local_compose = (ROOT.parent / 'structured-data' / 'compose.yml').read_text()
        self.assertIn('      MAX_FILE_SIZE_MB: "100"', local_compose)

    def test_docreader_parser_changes_rebuild_parser_image(self):
        self.write('WeKnora/docreader/parser/pptx_media.py', 'new page-bound parsing')
        self.commit()
        self.assertEqual([row['service'] for row in self.rows() if not row['reuse_image']], ['app', 'docreader'])

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

    def test_release_records_seven_independent_references_and_rejects_missing_service(self):
        coordinates = self.root / 'coordinates'
        coordinates.mkdir()
        for row in self.rows():
            (coordinates / (row['service'] + '.json')).write_text(json.dumps({
                'service': row['service'], 'image': row['reuse_image'], 'input_hash': row['input_hash']}))
        output = self.root / 'release.env'
        planner.write_release(coordinates, REPO, TAG, output)
        self.assertIn('STRUCTURED_DATA_IMAGE=ghcr.io/owner/workbench-structured-data:', output.read_text())
        self.assertIn('COMMODITY_RESEARCH_IMAGE=ghcr.io/owner/workbench-commodity-research:', output.read_text())
        (coordinates / 'redis.json').unlink()
        with self.assertRaises(ValueError):
            planner.write_release(coordinates, REPO, TAG, output)


if __name__ == '__main__':
    unittest.main()
