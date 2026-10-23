import importlib.util
import dataclasses
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

    def monitored_env(self):
        proc = self.root / 'proc'
        monitor = proc / '7001'
        monitor.mkdir(parents=True, exist_ok=True)
        # /proc/stat field 22 is the immutable process start tick; comm may contain spaces/parentheses.
        (monitor / 'stat').write_text('7001 (memory monitor (local)) S ' + ' '.join(['0'] * 18 + ['12345']) + '\n')
        (monitor / 'cgroup').write_text('0::/monitor.scope\n')
        mocker = patch.object(guard, 'PROC_ROOT', proc, create=True)
        mocker.start()
        self.addCleanup(mocker.stop)
        return {'WEKNORA_BUILD_PROFILE': 'monitored-local-once',
                'WEKNORA_BUILD_MONITOR_PID': '7001',
                'WEKNORA_BUILD_MONITOR_STARTTIME': '12345',
                'WEKNORA_BUILD_MEMORY_MB': '3328', 'WEKNORA_BUILD_NODE_HEAP_MB': '2560'}

    def test_monitored_profile_has_finite_budget_and_keeps_other_profiles_unchanged(self):
        env = self.monitored_env()
        monitored = guard.calculate(5897, 4300, env)
        self.assertEqual((monitored.memory, monitored.heap, monitored.swap, monitored.reserve),
                         (3328, 2560, 128, 768))
        self.assertEqual(guard.calculate(5897, 5000, {}).reserve, 1024)
        for profile in ('default', 'large-frontend-once'):
            with self.subTest(profile=profile), self.assertRaises(guard.GuardError):
                guard.calculate(5897, 5000, {**env, 'WEKNORA_BUILD_PROFILE': profile,
                                            'WEKNORA_BUILD_RESERVE_MB': '768'})
            with self.subTest(host_profile=profile), self.assertRaises(guard.GuardError):
                guard.check_host({**env, 'WEKNORA_BUILD_PROFILE': profile,
                                  'WEKNORA_BUILD_HOST_RESERVE_MB': '768'}, probe=lambda: 10000)

    def test_monitored_profile_requires_matching_live_monitor(self):
        env = self.monitored_env()
        for changes in ({'WEKNORA_BUILD_MONITOR_PID': ''},
                        {'WEKNORA_BUILD_MONITOR_PID': str(os.getpid())},
                        {'WEKNORA_BUILD_MONITOR_PID': '7002'},
                        {'WEKNORA_BUILD_MONITOR_STARTTIME': '12346'}):
            with self.subTest(changes=changes), self.assertRaises(guard.GuardError):
                guard.calculate(5897, 5000, {**env, **changes})
        path = self.root / 'proc/7001/stat'
        path.write_text(path.read_text().replace(') S ', ') Z '))
        with self.assertRaises(guard.GuardError):
            guard.calculate(5897, 5000, env)

    def test_monitored_profile_rejects_foreign_or_unreadable_monitor_evidence(self):
        env = self.monitored_env()
        with patch.object(guard.os, 'getuid', return_value=os.getuid() + 1), self.assertRaises(guard.GuardError):
            guard.calculate(5897, 5000, env)
        (self.root / 'proc/7001/stat').write_text('invalid process evidence\n')
        with self.assertRaises(guard.GuardError):
            guard.calculate(5897, 5000, env)

    def test_monitored_profile_only_relaxes_host_growth_estimate_with_startup_margin(self):
        env = self.monitored_env()
        for starting in (True, False):
            self.assertEqual(guard.check_host(env, 3328, probe=lambda: 896, starting=starting), (896, 768))
            with self.assertRaises(guard.GuardError):
                guard.check_host(env, 3328, probe=lambda: 895, starting=starting)
        selected = {**env, 'WEKNORA_BUILD_HOST_RESERVE_MB': '512'}
        self.assertEqual(guard.check_host(selected, 3328, probe=lambda: 640), (640, 512))
        with self.assertRaises(guard.GuardError):
            guard.check_host({**selected, 'WEKNORA_BUILD_HOST_RESERVE_MB': '511'}, probe=lambda: 10000)

    def test_monitored_profile_cannot_raise_budget_heap_swap_or_reduce_guest_floor(self):
        env = self.monitored_env()
        for changes in ({'WEKNORA_BUILD_MEMORY_MB': '3841'},
                        {'WEKNORA_BUILD_MEMORY_MB': '3840', 'WEKNORA_BUILD_NODE_HEAP_MB': '3073'},
                        {'WEKNORA_BUILD_NODE_HEAP_MB': '2688'},
                        {'WEKNORA_BUILD_SWAP_MB': '129'},
                        {'WEKNORA_BUILD_RESERVE_MB': '767'}):
            with self.subTest(changes=changes), self.assertRaises(guard.GuardError):
                guard.calculate(5897, 5000, {**env, **changes})
        with self.assertRaises(guard.GuardError):
            guard.calculate(4096, 4096, env)
        with self.assertRaises(guard.GuardError):
            guard.calculate(5897, 3328 + 767, env)

    def test_monitored_verify_rechecks_monitor_scope_actual_limits_and_live_reserves(self):
        env = self.monitored_env()
        self.budget = guard.calculate(5897, 4300, env, host_available=1000, host_reserve=768)
        for name, value in self.actual_limits().items():
            (self.scope / name).write_text(str(value))
        with patch.object(guard, 'meminfo', return_value=(5897, 4300)), \
                patch.object(guard, 'is_wsl', return_value=True), \
                patch.object(guard, 'windows_available', return_value=896):
            self.assertEqual(self.verify(**env), self.budget)
            with patch.object(guard, 'windows_available', return_value=895), self.assertRaises(guard.GuardError):
                self.verify(**env)
            with patch.object(guard, 'meminfo', return_value=(5897, 3328 + 767)), self.assertRaises(guard.GuardError):
                self.verify(**env)
            original = self.budget
            for changes in ({'reserve': 767}, {'host_reserve': 511}, {'swap': 129}):
                with self.subTest(changes=changes), self.assertRaises(guard.GuardError):
                    self.budget = dataclasses.replace(original, **changes)
                    self.verify(**env)
            self.budget = original
            (self.root / 'proc/7001/cgroup').write_text('0::/build.slice/compiler.scope/child\n')
            with self.assertRaises(guard.GuardError):
                self.verify(**env)
            (self.root / 'proc/7001/cgroup').write_text('0::/monitor.scope\n')
            (self.parent / 'memory.max').write_text(str(2560 * guard.MIB))
            with self.assertRaises(guard.GuardError):
                self.verify(**env)
            (self.parent / 'memory.max').write_text('max')
            (self.scope / 'memory.oom.group').write_text('0')
            with self.assertRaises(guard.GuardError):
                self.verify(**env)
            (self.scope / 'memory.oom.group').write_text('1')
            (self.root / 'proc/7001/stat').unlink()
            with self.assertRaises(guard.GuardError):
                self.verify(**env)

    def test_default_budget_preserves_guest_reserve_and_finite_runtime_limits(self):
        self.assertEqual((self.budget.memory, self.budget.high, self.budget.swap,
                          self.budget.heap, self.budget.go, self.budget.reserve),
                         (2560, 2304, 128, 1792, 768, 1024))
        low = guard.calculate(4096, 3072, {})
        self.assertEqual((low.memory, low.heap), (2048, 1408))

    def test_larger_guest_can_fit_the_frontend_without_changing_four_gib_limits(self):
        large = guard.calculate(5897, 4800, {})
        self.assertEqual((large.memory, large.heap, large.swap, large.reserve),
                         (3584, 2816, 128, 1024))
        self.assertLessEqual(large.memory, large.total * 2 // 3)
        self.assertLessEqual(large.memory, large.available - large.reserve)
        for total in (3917, 4096):
            with self.subTest(total=total):
                legacy = guard.calculate(total, 3584, {})
                self.assertEqual((legacy.memory, legacy.heap), (2560, 1792))

    def test_larger_guest_still_uses_the_actual_available_memory(self):
        constrained = guard.calculate(5897, 4352, {})
        self.assertEqual((constrained.memory, constrained.heap), (3328, 2688))
        small = guard.calculate(5897, 3328, {})
        self.assertEqual((small.memory, small.heap), (2304, 1664))
        selected = guard.calculate(5897, 4800, {'WEKNORA_BUILD_MEMORY_MB': '3456'})
        self.assertEqual((selected.memory, selected.heap), (3456, 2816))

    def test_larger_guest_does_not_allow_unbounded_memory_or_heap(self):
        for env in ({'WEKNORA_BUILD_MEMORY_MB': '3585'},
                    {'WEKNORA_BUILD_NODE_HEAP_MB': '2817'},
                    {'NODE_OPTIONS': '--max-old-space-size=3072'}):
            with self.subTest(env=env), self.assertRaises(guard.GuardError):
                guard.calculate(5897, 5600, env)

    def test_guest_profile_boundary_uses_real_total_and_keeps_the_two_thirds_cap(self):
        at_boundary = guard.calculate(4096, 4096, {})
        above_boundary = guard.calculate(4097, 4096, {})
        self.assertEqual((at_boundary.memory, at_boundary.heap), (2560, 1792))
        self.assertEqual((above_boundary.memory, above_boundary.heap), (4097 * 2 // 3, 2048))
        ideal = guard.calculate(6144, 5600, {})
        self.assertEqual((ideal.memory, ideal.heap), (3584, 2816))

    def test_explicit_three_gib_profile_does_not_change_defaults(self):
        env = {'WEKNORA_BUILD_PROFILE': 'large-frontend-once',
               'WEKNORA_BUILD_MEMORY_MB': '3840', 'WEKNORA_BUILD_NODE_HEAP_MB': '3072'}
        raised = guard.calculate(5897, 5000, env)
        self.assertEqual((raised.memory, raised.heap, raised.swap, raised.reserve),
                         (3840, 3072, 128, 1024))
        self.assertEqual((guard.calculate(5897, 5000, {}).memory,
                          guard.calculate(5897, 5000, {}).heap), (3584, 2816))
        for total, settings in [(4096, env), (5897, {'WEKNORA_BUILD_PROFILE': 'unknown'}),
                                (5897, {**env, 'WEKNORA_BUILD_MEMORY_MB': '3841'}),
                                (5897, {**env, 'WEKNORA_BUILD_NODE_HEAP_MB': '3073'}),
                                (5897, {**env, 'WEKNORA_BUILD_MEMORY_MB': '3712'})]:
            with self.subTest(total=total, settings=settings), self.assertRaises(guard.GuardError):
                guard.calculate(total, 5000, settings)

    def test_three_gib_profile_requires_real_scope_and_full_native_overhead(self):
        env = {'WEKNORA_BUILD_PROFILE': 'large-frontend-once',
               'WEKNORA_BUILD_MEMORY_MB': '3840', 'WEKNORA_BUILD_NODE_HEAP_MB': '3072'}
        self.budget = guard.calculate(5897, 5000, env)
        for name, value in self.actual_limits().items():
            (self.scope / name).write_text(str(value))
        with patch.object(guard, 'meminfo', return_value=(5897, 5000)):
            self.assertEqual(self.verify(**env), self.budget)
            with self.assertRaises(guard.GuardError):
                self.verify()
            (self.parent / 'memory.max').write_text(str(3712 * guard.MIB))
            with self.assertRaises(guard.GuardError):
                self.verify(**env)
            (self.parent / 'memory.max').write_text(str(3840 * guard.MIB))
            self.assertEqual(self.verify(**env), self.budget)

    def test_three_gib_profile_keeps_live_guest_and_windows_reserves(self):
        env = {'WEKNORA_BUILD_PROFILE': 'large-frontend-once',
               'WEKNORA_BUILD_MEMORY_MB': '3840', 'WEKNORA_BUILD_NODE_HEAP_MB': '3072'}
        self.budget = guard.calculate(5897, 5000, env)
        for name, value in self.actual_limits().items():
            (self.scope / name).write_text(str(value))
        guest_minimum = 3840 + 1024
        host_minimum = 3840 + guard.HOST_RESERVE_MB
        with patch.object(guard, 'is_wsl', return_value=True), \
                patch.object(guard, 'meminfo', return_value=(5897, guest_minimum)):
            with patch.object(guard, 'windows_available', return_value=host_minimum):
                self.assertEqual(self.verify(**env), self.budget)
            with patch.object(guard, 'windows_available', return_value=host_minimum - 1):
                with self.assertRaises(guard.GuardError):
                    self.verify(**env)
        with patch.object(guard, 'meminfo', return_value=(5897, guest_minimum - 1)):
            with self.assertRaises(guard.GuardError):
                self.verify(**env)
        with patch.object(guard, 'is_wsl', return_value=True), \
                patch.object(guard, 'meminfo', return_value=(5897, 5000)):
            with patch.object(guard, 'windows_available', return_value=host_minimum + guard.HOST_GROWTH_OVERHEAD_MB):
                self.assertEqual(guard.plan(env).heap, 3072)
            with patch.object(guard, 'windows_available', return_value=host_minimum + guard.HOST_GROWTH_OVERHEAD_MB - 1):
                with self.assertRaises(guard.GuardError):
                    guard.plan(env)

    def test_forged_large_budget_fields_never_replace_real_guest_or_finite_limits(self):
        original = guard.calculate(5897, 4800, {})
        self.budget = original
        for name, value in self.actual_limits().items():
            (self.scope / name).write_text(str(value))
        for changes in ({'total': 8192}, {'memory': 3585}, {'heap': 2817},
                        {'reserve': 1023}, {'host_reserve': 1535}, {'swap': 129}):
            self.budget = dataclasses.replace(original, **changes)
            with self.subTest(changes=changes), \
                    patch.object(guard, 'meminfo', return_value=(5897, 5600)), self.assertRaises(guard.GuardError):
                self.verify()

    def test_larger_budget_requires_matching_actual_scope_and_live_memory(self):
        large = guard.calculate(5897, 4800, {})
        self.assertEqual(large.memory, 3584)
        self.budget = large
        with patch.object(guard, 'meminfo', return_value=(5897, 4800)):
            # A larger serialized budget must not pass the old actual scope.
            with self.assertRaises(guard.GuardError):
                self.verify()
            for name, value in self.actual_limits().items():
                (self.scope / name).write_text(str(value))
            self.assertEqual(self.verify(), large)
            (self.parent / 'memory.max').write_text(str(2560 * guard.MIB))
            with self.assertRaises(guard.GuardError):
                self.verify()
            (self.parent / 'memory.max').write_text('max')
        with patch.object(guard, 'meminfo', return_value=(5897, large.memory + large.reserve - 1)):
            with self.assertRaises(guard.GuardError):
                self.verify()
        with patch.object(guard, 'meminfo', return_value=(5897, large.memory + large.reserve)):
            self.assertEqual(self.verify(), large)
        with patch.object(guard, 'meminfo', return_value=(4096, 3584)):
            with self.assertRaises(guard.GuardError):
                self.verify()

    def test_larger_budget_requires_the_full_windows_reserve(self):
        large = guard.calculate(5897, 4800, {})
        self.assertEqual(large.memory, 3584)
        required = guard.HOST_RESERVE_MB + large.memory + guard.HOST_GROWTH_OVERHEAD_MB
        with patch.object(guard, 'meminfo', return_value=(5897, 4800)), \
                patch.object(guard, 'is_wsl', return_value=True):
            with patch.object(guard, 'windows_available', return_value=required - 1):
                with self.assertRaises(guard.GuardError):
                    guard.plan({})
            with patch.object(guard, 'windows_available', return_value=required):
                self.assertEqual(guard.plan({}).memory, large.memory)
        self.budget = large
        for name, value in self.actual_limits().items():
            (self.scope / name).write_text(str(value))
        with patch.object(guard, 'meminfo', return_value=(5897, 4800)), \
                patch.object(guard, 'is_wsl', return_value=True), \
                patch.object(guard, 'windows_available', return_value=required - guard.HOST_GROWTH_OVERHEAD_MB - 1):
            with self.assertRaises(guard.GuardError):
                self.verify()
        with patch.object(guard, 'meminfo', return_value=(5897, 4800)), \
                patch.object(guard, 'is_wsl', return_value=True), \
                patch.object(guard, 'windows_available', return_value=required - guard.HOST_GROWTH_OVERHEAD_MB):
            self.assertEqual(self.verify(), large)

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

    def test_cgroup_evidence_preserves_scope_and_ancestor_paths(self):
        actual = guard.read_cgroup()
        self.assertEqual(actual['_path'], str(self.scope))
        self.assertEqual(actual['_ancestors'][0]['_path'], str(self.parent))

    def test_insufficient_ancestor_memory_reports_limiting_file_and_required_budget(self):
        self.budget = guard.calculate(5897, 5000, {})
        for name, value in self.actual_limits().items():
            (self.scope / name).write_text(str(value))
        (self.parent / 'memory.max').write_text(str(2048 * guard.MIB))
        with self.assertRaises(guard.GuardError) as caught:
            guard.verify_limits(self.budget, guard.read_cgroup())
        message = str(caught.exception)
        for detail in (str(self.parent / 'memory.max'), '2048MiB', '3456MiB',
                       '2816MiB', '640MiB'):
            self.assertIn(detail, message)

    def test_insufficient_scope_memory_reports_scope_file(self):
        (self.scope / 'memory.max').write_text(str(2048 * guard.MIB))
        with self.assertRaises(guard.GuardError) as caught:
            self.verify()
        message = str(caught.exception)
        for detail in (str(self.scope / 'memory.max'), '2048MiB', '2432MiB'):
            self.assertIn(detail, message)

    def test_legacy_limit_evidence_without_paths_remains_supported(self):
        limits = self.actual_limits()
        guard.verify_limits(self.budget, limits)
        limits['_ancestors'] = [{'memory.max': 2048 * guard.MIB}]
        with self.assertRaises(guard.GuardError) as caught:
            guard.verify_limits(self.budget, limits)
        message = str(caught.exception)
        for detail in ('memory.max', '2048MiB', '2432MiB'):
            self.assertIn(detail, message)

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
        with self.assertRaises(guard.GuardError) as caught:
            self.verify()
        self.assertIn(str(self.parent / 'memory.max'), str(caught.exception))

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


class BuildMemoryFullPreflightTests(unittest.TestCase):
    def run_check(self, verify_exit):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        root = Path(directory.name)
        (root / 'scripts').mkdir()
        (root / 'bin').mkdir()
        shutil.copy(REPO / 'scripts/build-local.sh', root / 'scripts/build-local.sh')
        trace = root / 'trace'
        (root / 'scripts/build-memory-guard.py').write_text(
            "import os,sys\nfrom pathlib import Path\n"
            "with Path(os.environ['TEST_PREFLIGHT_TRACE']).open('a') as f: f.write(sys.argv[1]+'\\n')\n"
            "if sys.argv[1]=='verify' and os.environ['TEST_VERIFY_EXIT']!='0':\n"
            " print('祖先 cgroup 的有效内存上限不足以容纳本次构建预算',file=sys.stderr)\n"
            " raise SystemExit(int(os.environ['TEST_VERIFY_EXIT']))\n"
            "print('3584 3225 128 2816 768 5897 5022 1024 6246 1536')\n")
        systemd = root / 'bin/systemd-run'
        systemd.write_text("#!/usr/bin/bash\nset -eu\nprintf 'scope\\n' >> \"$TEST_PREFLIGHT_TRACE\"\n"
                           "while [[ \"$1\" != /usr/bin/env ]]; do shift; done\nexec \"$@\"\n")
        systemd.chmod(0o755)
        env = {**os.environ, 'PATH': str(root / 'bin')+':'+os.environ['PATH'],
               'TEST_PREFLIGHT_TRACE': str(trace), 'TEST_VERIFY_EXIT': str(verify_exit)}
        env.pop('WEKNORA_BUILD_LIMITED', None)
        env.pop('WEKNORA_BUILD_BUDGET', None)
        result = subprocess.run(['/usr/bin/bash', str(root / 'scripts/build-local.sh'), '--check-memory'],
                                env=env, capture_output=True, text=True, timeout=10)
        return root, trace.read_text().splitlines(), result

    def test_check_memory_enters_actual_scope_and_rejects_ancestor_limit(self):
        root, trace, result = self.run_check(27)
        self.assertEqual(result.returncode, 27, result.stdout+result.stderr)
        self.assertEqual(trace, ['plan', 'scope', 'verify'])
        self.assertNotIn('预检通过', result.stdout)
        self.assertFalse((root / '.build').exists(), 'preflight must not create build staging')

    def test_successful_check_memory_verifies_scope_without_starting_build(self):
        root, trace, result = self.run_check(0)
        self.assertEqual(result.returncode, 0, result.stdout+result.stderr)
        self.assertEqual(trace, ['plan', 'scope', 'verify'])
        self.assertIn('预检通过', result.stdout)
        self.assertFalse((root / '.build').exists())


if __name__ == '__main__':
    unittest.main()
