#!/usr/bin/env python3
"""Calculate and verify the local build's memory boundary (standard library only)."""
import dataclasses
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys

MIB = 1024 * 1024
MIN_BUILD_MB = 2048
MAX_BUILD_MB = 2560
MAX_SWAP_MB = 128
HOST_RESERVE_MB = 1536
HOST_GROWTH_OVERHEAD_MB = 128
CGROUP_ROOT = Path('/sys/fs/cgroup')
CGROUP_MEMBERSHIP = Path('/proc/self/cgroup')
COREDUMP_FILTER = Path('/proc/self/coredump_filter')


class GuardError(Exception):
    pass


@dataclasses.dataclass(frozen=True)
class Budget:
    memory: int
    high: int
    swap: int
    heap: int
    go: int
    total: int
    available: int
    reserve: int
    host_available: int = -1
    host_reserve: int = 1536

    def values(self):
        return [getattr(self, field.name) for field in dataclasses.fields(self)]


def integer(env, key, default, minimum=0):
    value = env.get(key, str(default))
    if not re.fullmatch(r"\d+", value) or int(value) < minimum:
        raise GuardError(f"{key} 必须是至少 {minimum} 的整数（MiB）。")
    return int(value)


def meminfo():
    info = {}
    for line in Path('/proc/meminfo').read_text().splitlines():
        key, value = line.split(':', 1)
        if key in ('MemTotal', 'MemAvailable'):
            info[key] = int(value.split()[0]) // 1024
    if not all(key in info for key in ('MemTotal', 'MemAvailable')):
        raise GuardError('无法读取 Linux 内存余量，已停止构建。')
    return info['MemTotal'], info['MemAvailable']


def is_wsl():
    return 'microsoft' in Path('/proc/sys/kernel/osrelease').read_text().lower()


def windows_available():
    executable = '/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe'
    # Query the kernel directly; CIM may be inaccessible or slow during pressure.
    command = r'''$ErrorActionPreference='Stop'
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class BuildHostMemoryProbe {
 [StructLayout(LayoutKind.Sequential)] public struct Status {
  public uint Length, Load;
  public ulong TotalPhysical, AvailablePhysical, TotalPageFile, AvailablePageFile;
  public ulong TotalVirtual, AvailableVirtual, AvailableExtendedVirtual;
 }
 [DllImport("kernel32.dll", SetLastError=true)]
 private static extern bool GlobalMemoryStatusEx(ref Status value);
 public static ulong Available() {
  var value = new Status(); value.Length = (uint)Marshal.SizeOf(typeof(Status));
  if (!GlobalMemoryStatusEx(ref value)) throw new System.ComponentModel.Win32Exception();
  return value.AvailablePhysical;
 }
}
'@
[Console]::WriteLine([BuildHostMemoryProbe]::Available())
'''
    try:
        result = subprocess.run([executable, '-NoLogo', '-NoProfile', '-NonInteractive',
                                 '-Command', command], capture_output=True, timeout=15)
        output = result.stdout.decode('utf-8', errors='replace').strip()
        if result.returncode or not re.fullmatch(r'\d+', output):
            raise ValueError('invalid Windows memory response')
        return int(output) // MIB  # GlobalMemoryStatusEx reports bytes.
    except (OSError, ValueError, subprocess.TimeoutExpired) as exc:
        raise GuardError('无法读取 Windows 宿主可用内存，保护检查未通过；请确认 WSL 互操作和 Windows 查询可用后重试。') from exc


def check_host(env, memory=MIN_BUILD_MB, probe=None, *, starting=True):
    reserve = integer(env, 'WEKNORA_BUILD_HOST_RESERVE_MB', HOST_RESERVE_MB, HOST_RESERVE_MB)
    available = (probe or windows_available)()
    if not isinstance(available, int) or isinstance(available, bool) or available < 0:
        raise GuardError('Windows 可用内存查询结果无效，已停止构建。')
    # No credit is taken for guest cache reclamation: the whole build limit may
    # become new host residency. Verification still precedes compiler allocation;
    # only the outer startup allowance is omitted on that second check.
    required = reserve + memory + (HOST_GROWTH_OVERHEAD_MB if starting else 0)
    if available < required:
        raise GuardError(f'Windows 当前可用 {available}MiB；本次构建需要 {required}MiB（保留 {reserve}MiB' +
                         f'，构建最多新增 {memory}MiB' +
                         (f'，额外余量 {HOST_GROWTH_OVERHEAD_MB}MiB' if starting else '') +
                         '）。余量不足，已停止，现有构建未替换。')
    return available, reserve


def node_heap(env, ceiling):
    selected = integer(env, 'WEKNORA_BUILD_NODE_HEAP_MB', ceiling, 512)
    if selected > ceiling:
        raise GuardError(f'Node 堆上限最多可设为 {ceiling}MiB，请降低 WEKNORA_BUILD_NODE_HEAP_MB。')
    try:
        options = shlex.split(env.get('NODE_OPTIONS', ''))
    except ValueError as exc:
        raise GuardError('NODE_OPTIONS 引号不完整。') from exc
    requested = None
    for index, option in enumerate(options):
        normalized = option.replace('_', '-')
        if normalized == '--max-old-space-size-percentage' or normalized.startswith('--max-old-space-size-percentage='):
            raise GuardError('NODE_OPTIONS 不允许百分比 Node 堆上限；请使用预算内的固定 MiB 数值。')
        match = re.fullmatch(r'--max-old-space-size(?:=(.*))?', normalized)
        if match:
            value = match.group(1) or (options[index + 1] if index + 1 < len(options) else '')
            if not value.isdigit() or not 512 <= int(value) <= selected:
                raise GuardError(f'NODE_OPTIONS 中的堆上限超过本次安全预算 {selected}MiB 或格式无效；请删除该覆盖或降低数值。')
            requested = int(value)
    return selected if requested is None else requested


def go_limit(env, budget):
    default = min(768, budget // 3)
    value = env.get('GOMEMLIMIT', f'{default}MiB')
    match = re.fullmatch(r'(\d+)(B|KiB|MiB|GiB)?', value)
    if not match:
        raise GuardError('GOMEMLIMIT 必须使用有限数值，例如 768MiB。')
    scale = {'B': 1, None: 1, 'KiB': 1024, 'MiB': MIB, 'GiB': 1024*MIB}[match.group(2)]
    selected = int(match.group(1)) * scale // MIB
    if not 128 <= selected <= budget - 640:
        raise GuardError('GOMEMLIMIT 超过本次安全构建预算，或低于 128MiB。')
    return selected


def calculate(total, available, env, host_available=-1, host_reserve=1536):
    reserve = integer(env, 'WEKNORA_BUILD_RESERVE_MB', 1024, 1024)
    ceiling = min(MAX_BUILD_MB, total * 2 // 3, available - reserve)
    if ceiling < MIN_BUILD_MB:
        raise GuardError(f'WSL/Linux 当前可用 {available}MiB，保留 {reserve}MiB 后不足最低构建预算 {MIN_BUILD_MB}MiB；已停止，现有构建未替换。')
    memory = integer(env, 'WEKNORA_BUILD_MEMORY_MB', ceiling, MIN_BUILD_MB)
    if memory > ceiling:
        raise GuardError(f'WEKNORA_BUILD_MEMORY_MB={memory} 超过本次安全预算 {ceiling}MiB（可用 {available}MiB，保留 {reserve}MiB）。')
    swap = integer(env, 'WEKNORA_BUILD_SWAP_MB', MAX_SWAP_MB)
    if swap > MAX_SWAP_MB:
        raise GuardError('构建 swap 上限最多为 128MiB，避免长时间交换拖慢系统。')
    heap = node_heap(env, min(1792, (memory - 640) // 128 * 128))
    return Budget(memory, memory * 9 // 10, swap, heap, go_limit(env, memory),
                  total, available, reserve, host_available, host_reserve)


def plan(env):
    total, available = meminfo()
    budget = calculate(total, available, env)
    if is_wsl():
        host_available, host_reserve = check_host(env, budget.memory)
        budget = dataclasses.replace(budget, host_available=host_available, host_reserve=host_reserve)
    return budget


def read_cgroup():
    unified = [line[3:] for line in CGROUP_MEMBERSHIP.read_text().splitlines() if line.startswith('0::')]
    if len(unified) != 1:
        raise GuardError('构建需要可验证的 cgroup v2 内存控制；未找到统一 cgroup，已停止。')
    root = CGROUP_ROOT.resolve()
    directory = (root / unified[0].lstrip('/')).resolve()
    if not directory.is_relative_to(root):
        raise GuardError('无法验证本进程的 cgroup 路径。')
    limits = {}
    def read_limit(path):
        value = path.read_text().strip()
        if value == 'max':
            return None
        if not re.fullmatch(r'\d+', value):
            raise ValueError('invalid cgroup limit')
        return int(value)
    try:
        for name in ('memory.max', 'memory.high', 'memory.swap.max', 'memory.oom.group'):
            limits[name] = read_limit(directory / name)
        ancestors = []
        for parent in directory.parents:
            if parent == root:
                break  # The unified root has no memory.max limit file.
            if not parent.is_relative_to(root):
                raise ValueError('cgroup ancestor escaped the unified hierarchy')
            ancestors.append({name: read_limit(parent / name)
                              for name in ('memory.max', 'memory.high', 'memory.swap.max')})
        limits['_ancestors'] = ancestors
    except (OSError, ValueError) as exc:
        raise GuardError('无法读取实际 cgroup 内存限制，已停止构建。') from exc
    return limits


def verify_limits(budget, limits):
    if budget.memory < MIN_BUILD_MB or budget.memory > min(MAX_BUILD_MB, budget.total * 2 // 3):
        raise GuardError('构建预算不符合内存保留策略。')
    if budget.reserve < 1024 or not 0 < budget.high <= budget.memory * 9 // 10 or not 0 <= budget.swap <= MAX_SWAP_MB:
        raise GuardError('构建限制参数无效。')
    if not 512 <= budget.heap <= min(1792, (budget.memory - 640) // 128 * 128):
        raise GuardError('Node 堆上限不符合本次构建预算。')
    effective = {}
    for name, maximum in [('memory.max', budget.memory*MIB),
                          ('memory.high', budget.high*MIB),
                          ('memory.swap.max', budget.swap*MIB)]:
        actual = limits.get(name)
        if actual is None or actual < 0 or actual > maximum:
            raise GuardError(f'未检测到有效的 {name} 构建约束；环境标记不能代替实际 cgroup 限额，已停止。')
        finite = [actual]
        for ancestor in limits.get('_ancestors', []):
            value = ancestor.get(name)
            if value is not None:
                if not isinstance(value, int) or value < 0:
                    raise GuardError('祖先 cgroup 内存限制无效，已停止。')
                finite.append(value)
        effective[name] = min(finite)
    if effective['memory.max'] < max(MIN_BUILD_MB, budget.heap + 640) * MIB:
        raise GuardError('祖先 cgroup 的有效内存上限不足以容纳本次构建预算，已停止。')
    if effective['memory.high'] <= 0:
        raise GuardError('有效 memory.high 为零，已停止构建。')
    if limits.get('memory.oom.group') != 1:
        raise GuardError('cgroup 未启用整组 OOM 保护，已停止构建。')


def verify(env, values):
    if len(values) != len(dataclasses.fields(Budget)) or not all(re.fullmatch(r'-?\d+', x) for x in values):
        raise GuardError('内部构建预算缺失或无效，已停止。')
    budget = Budget(*(int(x) for x in values))
    total, available = meminfo()
    if (total != budget.total or not 0 <= budget.available <= total or
            budget.memory > min(MAX_BUILD_MB, total*2//3, budget.available-budget.reserve) or
            budget.host_reserve < HOST_RESERVE_MB or
            not 128 <= budget.go <= budget.memory - 640):
        raise GuardError('内部构建预算不符合本机内存策略。')
    if budget.memory > available - budget.reserve:
        raise GuardError(f'WSL/Linux 当前可用 {available}MiB，保留 {budget.reserve}MiB 后不足已规划的 '
                         f'{budget.memory}MiB 构建预算；已停止，现有构建未替换。')
    verify_limits(budget, read_cgroup())
    if node_heap(env, budget.heap) > budget.heap or go_limit(env, budget.memory) > budget.go:
        raise GuardError('实际 Node/Go 环境参数超过本次预算，已停止构建。')
    if is_wsl():
        check_host(env, budget.memory, starting=False)
    if int(COREDUMP_FILTER.read_text().strip(), 16) != 0:
        raise GuardError('构建进程转储过滤未生效，已停止。')
    return budget


def main():
    try:
        mode = sys.argv[1] if len(sys.argv) > 1 else 'plan'
        if mode not in ('plan', 'verify') or (mode == 'plan' and len(sys.argv) > 2):
            raise GuardError('仅支持 plan 或 verify 模式。')
        budget = verify(os.environ, sys.argv[2:]) if mode == 'verify' else plan(os.environ)
        print(' '.join(str(value) for value in budget.values()))
    except (GuardError, OSError) as exc:
        print(f'构建内存保护：{exc}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
