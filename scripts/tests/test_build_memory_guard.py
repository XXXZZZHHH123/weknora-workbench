import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


REPO = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('build_memory_guard_tested', REPO / 'scripts/build-memory-guard.py')
guard = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = guard
SPEC.loader.exec_module(guard)


class BuildMemoryGuardTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.hierarchy = self.root / 'cgroup'
        self.parent = self.hierarchy / 'build.slice'
        self.scope = self.parent / 'compiler.scope'
        self.scope.mkdir(parents=True)
        self.membership = self.root / 'membership'
        self.membership.write_text('0::/build.slice/compiler.scope\n')
        self.coredump = self.root / 'coredump_filter'
        self.coredump.write_text('00000000\n')
        self.budget = guard.calculate(4096, 3584, {})
        for name, value in self.actual_limits().items():
            (self.scope / name).write_text(str(value))
        for name in ('memory.max', 'memory.high', 'memory.swap.max'):
            (self.parent / name).write_text('max')
        for name, value in [('CGROUP_ROOT', self.hierarchy), ('CGROUP_MEMBERSHIP', self.membership),
                            ('COREDUMP_FILTER', self.coredump)]:
            mocker = patch.object(guard, name, value)
            mocker.start()
            self.addCleanup(mocker.stop)
        self.memory = patch.object(guard, 'meminfo', return_value=(4096, 3584))
        self.memory.start()
        self.addCleanup(self.memory.stop)
        self.wsl = patch.object(guard, 'is_wsl', return_value=False)
        self.wsl.start()
        self.addCleanup(self.wsl.stop)

    def actual_limits(self):
        return {'memory.max': self.budget.memory * guard.MIB,
                'memory.high': self.budget.high * guard.MIB,
                'memory.swap.max': self.budget.swap * guard.MIB,
                'memory.oom.group': 1}

    def verify(self, **env):
        return guard.verify(env, [str(value) for value in self.budget.values()])

    def test_default_budget_preserves_guest_reserve_and_finite_runtime_limits(self):
        self.assertEqual((self.budget.memory, self.budget.high, self.budget.swap,
                          self.budget.heap, self.budget.go, self.budget.reserve),
                         (2560, 2304, 128, 1792, 768, 1024))
        low = guard.calculate(4096, 3072, {})
        self.assertEqual((low.memory, low.heap), (2048, 1408))

    def test_insufficient_guest_memory_and_raised_budgets_fail_closed(self):
        for total, available, env in [(4096, 3071, {}), (2048, 2048, {}),
                                      (4096, 3072, {'WEKNORA_BUILD_MEMORY_MB': '2049'}),
                                      (4096, 3584, {'WEKNORA_BUILD_MEMORY_MB': '2561'}),
                                      (4096, 3584, {'WEKNORA_BUILD_RESERVE_MB': '1023'}),
                                      (4096, 3584, {'WEKNORA_BUILD_SWAP_MB': '129'})]:
            with self.subTest(total=total, available=available, env=env), self.assertRaises(guard.GuardError):
                guard.calculate(total, available, env)

    def test_invalid_heap_environment_is_never_silently_accepted(self):
        for value in ('511', '1793', '-1', '1.5', 'off', '2GiB', ''):
            with self.subTest(value=value), self.assertRaises(guard.GuardError):
                guard.node_heap({'WEKNORA_BUILD_NODE_HEAP_MB': value}, 1792)
        self.assertEqual(guard.node_heap({'WEKNORA_BUILD_NODE_HEAP_MB': '1024'}, 1792), 1024)

    def test_percentage_and_excessive_node_options_cannot_override_the_budget(self):
        invalid = ['--max-old-space-size-percentage=90', '--max_old_space_size_percentage 90',
                   '--max-old-space-size=4096', '--max_old_space_size 4096',
                   '--max-old-space-size=512 --max-old-space-size=4096',
                   '--max-old-space-size', '--max-old-space-size=511', '--require "unfinished']
        for value in invalid:
            with self.subTest(value=value), self.assertRaises(guard.GuardError):
                guard.node_heap({'NODE_OPTIONS': value}, 1792)
        self.assertEqual(guard.node_heap({'NODE_OPTIONS': '--trace-warnings --max_old_space_size=1024'}, 1792), 1024)

    def test_go_runtime_limit_is_finite_and_inside_the_memory_boundary(self):
        for value in ('off', 'unlimited', '4GiB', '127MiB', '768MB', '-1MiB'):
            with self.subTest(value=value), self.assertRaises(guard.GuardError):
                guard.go_limit({'GOMEMLIMIT': value}, 2560)
        self.assertEqual(guard.go_limit({'GOMEMLIMIT': '512MiB'}, 2560), 512)

    def test_verify_requires_a_complete_numeric_budget_despite_a_limited_marker(self):
        for values in ([], ['1'], ['0'] * 9, ['0'] * 11, ['bad'] * 10):
            with self.subTest(values=values), self.assertRaises(guard.GuardError):
                guard.verify({'WEKNORA_BUILD_LIMITED': '1'}, values)

    def test_verify_checks_actual_cgroup_not_the_environment_marker(self):
        (self.scope / 'memory.max').write_text('max')
        with self.assertRaises(guard.GuardError):
            self.verify(WEKNORA_BUILD_LIMITED='1')

    def test_actual_cgroup_limits_must_be_finite_and_no_higher_than_the_budget(self):
        for name in ('memory.max', 'memory.high', 'memory.swap.max'):
            original = (self.scope / name).read_text()
            for value in ('max', '-1', str(int(original) + 1)):
                with self.subTest(name=name, value=value), self.assertRaises(guard.GuardError):
                    (self.scope / name).write_text(value)
                    self.verify()
            (self.scope / name).write_text(original)

    def test_oom_group_protection_is_required(self):
        for value in ('0', '2', 'max'):
            with self.subTest(value=value), self.assertRaises(guard.GuardError):
                (self.scope / 'memory.oom.group').write_text(value)
                self.verify()

    def test_missing_or_malformed_cgroup_evidence_prevents_verification(self):
        (self.scope / 'memory.high').unlink()
        with self.assertRaises(guard.GuardError):
            self.verify()
        (self.scope / 'memory.high').write_text('not-a-number')
        with self.assertRaises(guard.GuardError):
            self.verify()
        self.membership.write_text('1:memory:/legacy\n')
        with self.assertRaises(guard.GuardError):
            guard.read_cgroup()

    def test_cgroup_membership_cannot_escape_the_verified_hierarchy(self):
        self.membership.write_text('0::/../../outside\n')
        with self.assertRaises(guard.GuardError):
            guard.read_cgroup()

    def test_ancestor_memory_ceiling_must_allow_the_selected_heap_and_native_overhead(self):
        minimum = max(guard.MIN_BUILD_MB, self.budget.heap + 640) * guard.MIB
        (self.parent / 'memory.max').write_text(str(minimum - 1))
        with self.assertRaises(guard.GuardError):
            self.verify()
        (self.parent / 'memory.max').write_text(str(minimum))
        self.assertEqual(self.verify(), self.budget)

    def test_zero_ancestor_high_and_malformed_ancestor_limits_are_rejected(self):
        (self.parent / 'memory.high').write_text('0')
        with self.assertRaises(guard.GuardError):
            self.verify()
        (self.parent / 'memory.high').write_text('invalid')
        with self.assertRaises(guard.GuardError):
            self.verify()

    def test_a_more_distant_ancestor_cannot_be_hidden_by_a_valid_immediate_parent(self):
        nested = self.scope / 'nested.scope'
        nested.mkdir()
        for name in self.actual_limits():
            shutil.copy(self.scope / name, nested / name)
        self.membership.write_text('0::/build.slice/compiler.scope/nested.scope\n')
        (self.parent / 'memory.max').write_text(str((guard.MIN_BUILD_MB - 1) * guard.MIB))
        actual = guard.read_cgroup()
        self.assertEqual(len(actual['_ancestors']), 2)
        self.assertEqual(actual['_ancestors'][0]['memory.max'], self.budget.memory * guard.MIB)
        with self.assertRaises(guard.GuardError):
            self.verify()

    def test_verified_scope_accepts_stricter_swap_and_all_ancestor_evidence(self):
        (self.parent / 'memory.swap.max').write_text('0')
        actual = guard.read_cgroup()
        self.assertEqual(actual['_ancestors'][0]['memory.swap.max'], 0)
        self.assertEqual(self.verify(), self.budget)

    def test_verify_rejects_raised_runtime_options_and_core_dump_leakage(self):
        for env in ({'NODE_OPTIONS': '--max-old-space-size=4096'}, {'GOMEMLIMIT': '1024MiB'}):
            with self.subTest(env=env), self.assertRaises(guard.GuardError):
                self.verify(**env)
        self.coredump.write_text('00000033')
        with self.assertRaises(guard.GuardError):
            self.verify()

    def test_windows_reserve_and_whole_build_growth_are_required(self):
        required = guard.HOST_RESERVE_MB + self.budget.memory + guard.HOST_GROWTH_OVERHEAD_MB
        with self.assertRaises(guard.GuardError):
            guard.check_host({}, self.budget.memory, probe=lambda: required - 1)
        self.assertEqual(guard.check_host({}, self.budget.memory, probe=lambda: required), (required, 1536))
        with self.assertRaises(guard.GuardError):
            guard.check_host({'WEKNORA_BUILD_HOST_RESERVE_MB': '1535'}, probe=lambda: 10000)
        for value in (True, -1, '8192'):
            with self.subTest(value=value), self.assertRaises(guard.GuardError):
                guard.check_host({}, probe=lambda: value)

    def test_windows_probe_failure_stops_planning_without_a_build_fallback(self):
        with patch.object(guard, 'is_wsl', return_value=True), \
                patch.object(guard, 'windows_available', side_effect=guard.GuardError('probe failed')):
            with self.assertRaisesRegex(guard.GuardError, 'probe failed'):
                guard.plan({})

    def test_windows_kernel_probe_is_bounded_and_validates_its_response(self):
        output = subprocess.CompletedProcess([], 0, str(8192 * guard.MIB).encode(), b'')
        with patch.object(guard.subprocess, 'run', return_value=output) as run:
            self.assertEqual(guard.windows_available(), 8192)
        self.assertEqual(run.call_args.kwargs['timeout'], 15)
        self.assertIn('GlobalMemoryStatusEx', run.call_args.args[0][-1])
        invalid = [subprocess.CompletedProcess([], 1, b'8192', b'failure'),
                   subprocess.CompletedProcess([], 0, b'not a number', b''),
                   subprocess.CompletedProcess([], 0, b'-1', b'')]
        for response in invalid:
            with self.subTest(response=response), patch.object(guard.subprocess, 'run', return_value=response), self.assertRaises(guard.GuardError):
                guard.windows_available()
        for failure in (OSError('interop unavailable'), subprocess.TimeoutExpired('powershell', 15)):
            with self.subTest(failure=failure), patch.object(guard.subprocess, 'run', side_effect=failure), self.assertRaises(guard.GuardError):
                guard.windows_available()

    def test_verify_rechecks_host_headroom_before_allocation(self):
        required = guard.HOST_RESERVE_MB + self.budget.memory
        with patch.object(guard, 'is_wsl', return_value=True), patch.object(guard, 'windows_available', return_value=required - 1):
            with self.assertRaises(guard.GuardError):
                self.verify()
        with patch.object(guard, 'is_wsl', return_value=True), patch.object(guard, 'windows_available', return_value=required):
            self.assertEqual(self.verify(), self.budget)

    def test_verify_rejects_guest_memory_lost_after_planning_even_with_valid_host_and_cgroup(self):
        with patch.object(guard, 'meminfo', return_value=(4096, 3072)), \
                patch.object(guard, 'is_wsl', return_value=True), \
                patch.object(guard, 'windows_available', return_value=8192):
            with self.assertRaises(guard.GuardError):
                self.verify()

    def test_verify_enforces_the_live_guest_reserve_boundary(self):
        required = self.budget.memory + self.budget.reserve
        with patch.object(guard, 'meminfo', return_value=(4096, required - 1)):
            with self.assertRaises(guard.GuardError):
                self.verify()
        with patch.object(guard, 'meminfo', return_value=(4096, required)):
            self.assertEqual(self.verify(), self.budget)


class BuildMemoryShellBoundaryTests(unittest.TestCase):
    def test_forged_limited_environment_without_a_budget_never_calls_the_guard_or_compiler(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'scripts').mkdir()
            shutil.copy(REPO / 'scripts/build-local.sh', root / 'scripts/build-local.sh')
            (root / 'scripts/build-memory-guard.py').write_text("from pathlib import Path\nPath(__file__).with_name('unexpected-guard-call').touch()\nraise SystemExit(91)\n")
            env = {**os.environ, 'WEKNORA_BUILD_LIMITED': '1'}
            env.pop('WEKNORA_BUILD_BUDGET', None)
            result = subprocess.run(['/usr/bin/bash', str(root / 'scripts/build-local.sh'), '--check-memory'],
                                    env=env, capture_output=True, text=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('缺少内存预算', result.stderr)
            self.assertFalse((root / 'scripts/unexpected-guard-call').exists())
            self.assertFalse((root / '.build').exists())


if __name__ == '__main__':
    unittest.main()
