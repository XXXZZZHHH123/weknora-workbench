import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[2] / 'scripts/sync_weknora.py'
spec = importlib.util.spec_from_file_location('sync_weknora', SCRIPT)
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


def git(root, *args):
    return subprocess.check_output(['git', *args], cwd=root, text=True, stderr=subprocess.DEVNULL).strip()


def write(root, name, content):
    path = root / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content)


def commit(root):
    git(root, 'add', '.')
    git(root, 'commit', '-qm', 'fixture')
    return git(root, 'rev-parse', 'HEAD')


class UpstreamSyncTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.path = Path(self.temp.name)
        self.upstream = self.path / 'upstream'
        self.project = self.path / 'project'
        for root in (self.upstream, self.project):
            root.mkdir()
            git(root, 'init', '-q', '-b', 'main')
            git(root, 'config', 'user.name', 'Fixture')
            git(root, 'config', 'user.email', 'fixture@example.invalid')
        self.text = 'first\nsecond\nthird\nfourth\nfifth\nsixth\n'
        write(self.upstream, 'ui.txt', self.text)
        write(self.upstream, 'engine.txt', 'old engine\n')
        write(self.upstream, 'deleted.txt', 'old upstream file\n')
        write(self.upstream, 'website/index.html', 'excluded website\n')
        write(self.upstream, 'website-docs/readme.md', 'excluded docs\n')
        self.base = commit(self.upstream)
        self.state = {'repository': 'Tencent/WeKnora', 'directory': 'WeKnora', 'base_commit': self.base,
                      'last_release': 'v0.8.2', 'release_url': 'https://github.com/Tencent/WeKnora/releases/tag/v0.8.2',
                      'exclude': ['website', 'website-docs']}
        write(self.project, 'WeKnora/ui.txt', self.text.replace('first', 'custom first'))
        write(self.project, 'WeKnora/engine.txt', 'old engine\n')
        write(self.project, 'WeKnora/deleted.txt', 'old upstream file\n')
        write(self.project, 'WeKnora/custom-feature.py', 'custom MCP feature\n')
        write(self.project, 'WeKnora/website-docs/README.md', 'local documentation link\n')
        write(self.project, 'structured-data/service.py', 'independent service\n')
        write(self.project, '.gitignore', '.private/\n')
        write(self.project, sync.STATE, json.dumps(self.state))
        self.main = commit(self.project)
        write(self.project, '.private/token', 'local test credential, never tracked\n')

    def tearDown(self):
        self.temp.cleanup()

    def update(self, conflict=False):
        value = self.text.replace('first', 'upstream first') if conflict else self.text.replace('sixth', 'upstream sixth')
        write(self.upstream, 'ui.txt', value)
        write(self.upstream, 'engine.txt', 'new engine\n')
        write(self.upstream, 'added.txt', 'new upstream file\n')
        (self.upstream / 'deleted.txt').unlink()
        target = commit(self.upstream)
        return {'status': 'update', 'tag': 'v0.8.3', 'target_commit': target, 'base_commit': self.base,
                'release_url': 'https://github.com/Tencent/WeKnora/releases/tag/v0.8.3',
                'branch': sync.PREFIX + 'v0.8.3-' + target[:12]}

    def prepare(self, update):
        original = sync.git

        def local_git(root, *args, **kwargs):
            if args and args[0] == 'fetch':
                args = tuple(str(self.upstream) if arg == 'https://github.com/Tencent/WeKnora.git' else arg for arg in args)
            return original(root, *args, **kwargs)

        with patch.object(sync, 'git', side_effect=local_git):
            return sync.prepare(self.project, self.state, update)

    def test_three_way_merge_preserves_customization_and_other_project_data(self):
        report = self.prepare(self.update())
        ref = report['branch']
        self.assertEqual(report['status'], 'prepared')
        self.assertEqual(report['conflicts'], [])
        text = git(self.project, 'show', ref + ':WeKnora/ui.txt')
        self.assertIn('custom first', text)
        self.assertIn('upstream sixth', text)
        self.assertEqual(git(self.project, 'show', ref + ':WeKnora/engine.txt'), 'new engine')
        self.assertEqual(git(self.project, 'show', ref + ':WeKnora/custom-feature.py'), 'custom MCP feature')
        self.assertEqual(git(self.project, 'show', ref + ':structured-data/service.py'), 'independent service')
        self.assertEqual(git(self.project, 'show', ref + ':WeKnora/website-docs/README.md'), 'local documentation link')
        paths = git(self.project, 'ls-tree', '-r', '--name-only', ref).splitlines()
        self.assertIn('WeKnora/added.txt', paths)
        self.assertNotIn('WeKnora/deleted.txt', paths)
        self.assertNotIn('WeKnora/website/index.html', paths)
        self.assertNotIn('.private/token', paths)
        metadata = json.loads(git(self.project, 'show', ref + ':' + sync.STATE))
        self.assertEqual(metadata['base_commit'], report['target_commit'])
        self.assertEqual(git(self.project, 'rev-parse', 'HEAD'), self.main)
        self.assertEqual(git(self.project, 'status', '--porcelain'), '')
        self.assertIn('local test credential', (self.project / '.private/token').read_text())

    def test_conflicts_do_not_advance_baseline_and_cannot_pass_ci(self):
        report = self.prepare(self.update(conflict=True))
        self.assertEqual(report['status'], 'conflicts')
        self.assertEqual(report['conflicts'], ['ui.txt'])
        git(self.project, 'checkout', '-q', report['branch'])
        state = json.loads((self.project / sync.STATE).read_text())
        self.assertEqual(state['base_commit'], self.base)
        with self.assertRaisesRegex(ValueError, 'Unresolved'):
            sync.validate_state(self.project)
        with self.assertRaisesRegex(ValueError, 'Conflict markers'):
            sync.finalize(self.project)
        write(self.project, 'WeKnora/ui.txt', 'manually resolved\n')
        sync.finalize(self.project)
        sync.validate_state(self.project)
        self.assertEqual(json.loads((self.project / sync.STATE).read_text())['base_commit'], report['target_commit'])

    def test_dirty_worktree_and_existing_branch_are_not_overwritten(self):
        update = self.update()
        write(self.project, 'WeKnora/engine.txt', 'unfinished local edit\n')
        with self.assertRaisesRegex(ValueError, 'Commit or stash'):
            self.prepare(update)
        git(self.project, 'checkout', '--', 'WeKnora/engine.txt')
        self.prepare(update)
        with self.assertRaisesRegex(ValueError, 'already exists'):
            self.prepare(update)

    def test_current_and_older_releases_are_not_imported(self):
        state = dict(self.state, base_commit='a' * 40)

        class API:
            def __init__(self, target, status): self.target, self.status = target, status
            def request(self, path):
                if path.endswith('/latest'):
                    return {'tag_name': 'v0.8.2', 'draft': False, 'prerelease': False}
                if '/commits/' in path: return {'sha': self.target}
                return {'status': self.status}

        self.assertEqual(sync.latest_update(API('a' * 40, 'identical'), state)['status'], 'current')
        self.assertEqual(sync.latest_update(API('b' * 40, 'behind'), state)['status'], 'behind')
        self.assertEqual(sync.latest_update(API('b' * 40, 'diverged'), state)['status'], 'diverged')
        self.assertEqual(sync.latest_update(API('b' * 40, 'ahead'), state)['status'], 'update')

    def test_conflict_pr_is_draft_and_has_no_approval_or_merge_call(self):
        requests = []

        class API:
            def request(self, path, **kwargs):
                requests.append((path, kwargs))
                return {'html_url': 'https://github.com/owner/repo/pull/1'}

        report = dict(self.update(), conflicts=['ui.txt'])
        sync.create_pull_request(API(), 'owner/repo', report)
        self.assertEqual(len(requests), 1)
        path, kwargs = requests[0]
        self.assertEqual(path, 'repos/owner/repo/pulls')
        self.assertTrue(kwargs['payload']['draft'])
        self.assertEqual(kwargs['payload']['base'], 'main')
        self.assertIn('WeKnora/ui.txt', kwargs['payload']['body'])

    def test_existing_pr_is_not_rewritten_and_ci_is_not_repeated(self):
        report = self.update()
        requests = []

        class API:
            def request(self, path, **kwargs):
                requests.append((path, kwargs))
                if '/pulls?' in path:
                    return [{'head': {'ref': report['branch'], 'sha': 'a' * 40,
                                      'repo': {'full_name': 'owner/repo'}},
                             'html_url': 'https://github.com/owner/repo/pull/1'}]
                return {'workflow_runs': [{'head_sha': 'a' * 40}]}

        with patch.object(sync, 'prepare') as prepare:
            result = sync.publish_update(API(), self.project, 'owner/repo', self.state, report)
            prepare.assert_not_called()
        self.assertEqual(result['status'], 'existing-pr')
        self.assertFalse(result['ci_dispatched'])
        self.assertTrue(all(not kwargs for _, kwargs in requests))

    def test_missing_branch_ci_is_dispatched_without_production_or_merge_call(self):
        requests = []

        class API:
            def request(self, path, **kwargs):
                requests.append((path, kwargs))
                return {'workflow_runs': []} if '/runs?' in path else None

        branch = sync.PREFIX + 'v0.8.3-test'
        self.assertTrue(sync.ensure_ci(API(), 'owner/repo', branch, 'a' * 40))
        self.assertEqual(requests[-1][0], 'repos/owner/repo/actions/workflows/cicd.yml/dispatches')
        self.assertEqual(requests[-1][1]['payload'], {'ref': branch})


if __name__ == '__main__':
    unittest.main()
