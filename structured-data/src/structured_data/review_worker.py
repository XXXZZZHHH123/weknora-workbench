"""Run workbook work outside the web process, with bounded results and resources.

The caller must serialize tasks (the HTTP import lock does this). Linux uses
address-space/CPU limits as well as the parent's RSS/wall-clock watchdog. Windows
has no ``resource`` limits; its native RSS watchdog and wall-clock timeout are
the portable fallback, not an OS hard memory limit.
"""
from __future__ import annotations

from datetime import date, datetime
import json
from itertools import islice
import math
import multiprocessing
import os
from pathlib import Path
import re
import shutil
import sys
import tempfile
import time

from .review_errors import ReviewValidationError, error_location


DEFAULT_MEMORY_MB = 768
DEFAULT_TIMEOUT_SECONDS = 300
MAX_REQUEST_BYTES = 256 * 1024
MAX_RESULT_BYTES = 2 * 1024 * 1024
POLL_SECONDS = 0.05


def _json_default(value):
    if isinstance(value, (date, datetime)):
        return value.isoformat()
    raise TypeError("Unsupported task value")


def _compact(value, depth=0):
    """Limit display metadata, including per-series category provenance."""
    if depth > 8:
        return None
    if isinstance(value, str):
        return value[:500]
    if isinstance(value, dict):
        return {str(k)[:100]: _compact(v, depth + 1)
                for k, v in islice(value.items(), 64)
                if k not in {"observations", "category_provenance"}}
    if isinstance(value, (list, tuple)):
        return [_compact(v, depth + 1) for v in value[:64]]
    return value


def _bounded_preview(parsed):
    """Keep a preview contract; never send the parser's full observations back."""
    result = {k: parsed[k] for k in
              ("mapping", "series_count", "observation_count", "notice") if k in parsed}
    for field, maximum in (("series", 20), ("sample", 20), ("issues", 50),
                           ("excluded_regions", 50)):
        source = parsed.get(field, [])
        result[field] = _compact(source[:maximum])
        if len(source) > maximum:
            result[field + "_truncated"] = True
    blocks = []
    for block in parsed.get("blocks", [])[:64]:
        item = {k: _compact(block[k]) for k in
                ("name", "series_count", "observation_count") if k in block}
        for field, maximum in (("series", 20), ("sample", 20), ("issues", 20)):
            source = block.get(field, [])
            item[field] = _compact(source[:maximum])
            if len(source) > maximum:
                item[field + "_truncated"] = True
        blocks.append(item)
    result["blocks"] = blocks
    if len(parsed.get("blocks", [])) > len(blocks):
        result["blocks_truncated"] = True
    return result


def _apply_resource_limits(memory_bytes, timeout_seconds, resource_module=None):
    """Restrict child virtual address space; RSS is enforced by the parent."""
    if resource_module is None:
        try:
            import resource as resource_module
        except ImportError:
            if sys.platform.startswith("linux"):
                raise ValueError("当前环境无法设置工作进程资源限制，任务未启动。") from None
            return False
    # Native libraries reserve more virtual space than resident memory. The
    # separate RSS watchdog uses the exact user-configured resident budget.
    address_limit = memory_bytes * 3
    cpu_limit = max(1, math.ceil(timeout_seconds))
    try:
        for kind, desired in ((resource_module.RLIMIT_AS, address_limit),
                              (resource_module.RLIMIT_CPU, cpu_limit),
                              (resource_module.RLIMIT_CORE, 0)):
            soft, hard = resource_module.getrlimit(kind)
            values = [desired] + [v for v in (soft, hard)
                                  if v != resource_module.RLIM_INFINITY]
            limit = min(values)
            resource_module.setrlimit(kind, (limit, limit))
    except (OSError, ValueError):
        raise ValueError("无法设置工作进程资源限制，任务未启动。") from None
    return True


def _read_rss_bytes(pid):
    if sys.platform.startswith("linux"):
        try:
            with open(f"/proc/{pid}/status", encoding="ascii") as stream:
                for line in stream:
                    if line.startswith("VmRSS:"):
                        return int(line.split()[1]) * 1024
        except (OSError, ValueError):
            return None
    elif os.name == "nt":
        # No psutil dependency is needed on Windows.
        import ctypes
        from ctypes import wintypes

        class ProcessMemoryCounters(ctypes.Structure):
            _fields_ = [("cb", wintypes.DWORD), ("PageFaultCount", wintypes.DWORD)] + [
                (name, ctypes.c_size_t) for name in
                ("PeakWorkingSetSize", "WorkingSetSize", "QuotaPeakPagedPoolUsage",
                 "QuotaPagedPoolUsage", "QuotaPeakNonPagedPoolUsage",
                 "QuotaNonPagedPoolUsage", "PagefileUsage", "PeakPagefileUsage")]

        kernel = ctypes.WinDLL("kernel32", use_last_error=True)
        psapi = ctypes.WinDLL("psapi", use_last_error=True)
        kernel.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
        kernel.OpenProcess.restype = wintypes.HANDLE
        kernel.CloseHandle.argtypes = [wintypes.HANDLE]
        psapi.GetProcessMemoryInfo.argtypes = [wintypes.HANDLE,
                                               ctypes.POINTER(ProcessMemoryCounters),
                                               wintypes.DWORD]
        handle = kernel.OpenProcess(0x0400 | 0x0010, False, pid)
        if not handle:
            return None
        try:
            counters = ProcessMemoryCounters()
            counters.cb = ctypes.sizeof(counters)
            if psapi.GetProcessMemoryInfo(handle, ctypes.byref(counters), counters.cb):
                return counters.WorkingSetSize
        finally:
            kernel.CloseHandle(handle)
    return None


def _limit_duckdb(memory_bytes):
    """Apply a smaller DuckDB budget inside this disposable process only."""
    import duckdb

    original_connect = duckdb.connect
    database_mb = max(16, memory_bytes // (4 * 1024 * 1024))

    def connect(*args, **kwargs):
        config = dict(kwargs.pop("config", {}) or {})
        config.update(memory_limit=f"{database_mb}MB", threads="2")
        return original_connect(*args, config=config, **kwargs)

    duckdb.connect = connect


def _safe_business_error(exc, request):
    message = str(exc)[:2000]
    for private in (request.get("root"), request.get("path")):
        if private:
            variants = {str(private), str(private).replace("\\", "/"),
                        str(private).replace("/", "\\")}
            for variant in sorted(variants, key=len, reverse=True):
                message = re.sub(re.escape(variant), "[本地文件]", message, flags=re.I)
    return message or "工作表任务未通过检查，原有版本仍可使用。"


def _write_result(path, payload):
    encoded = json.dumps(payload, ensure_ascii=False, allow_nan=False,
                         default=_json_default).encode("utf-8")
    if len(encoded) > MAX_RESULT_BYTES:
        encoded = json.dumps({"ok": False, "error": "预览结果过大，请缩小区块范围后重试。"},
                             ensure_ascii=False).encode("utf-8")
    Path(path).write_bytes(encoded)


def _review_child(request, result_path, memory_bytes, timeout_seconds):
    try:
        _apply_resource_limits(memory_bytes, timeout_seconds)
        _limit_duckdb(memory_bytes)
        # Heavy imports and workbook parsing happen only after child limits.
        from .imports import ImportManager

        manager = ImportManager(request["root"])
        action = request["action"]
        if action == "preview":
            result = _bounded_preview(manager.preview(request["import_id"], request["mapping"]))
        elif action == "confirm":
            result = manager.confirm(request["import_id"], request["mapping"],
                                     request["save_template"])
        else:
            result = manager.ingest(request["path"], use_reviewed_mappings=request.get("use_reviewed_mappings", True))
        _write_result(result_path, {"ok": True, "result": result})
    except MemoryError:
        _write_result(result_path, {"ok": False, "error": "任务超过内存限制，原有版本仍可使用。"})
    except ValueError as exc:
        _write_result(result_path, {"ok": False, "error": _safe_business_error(exc, request), **error_location(exc)})
    except Exception:
        # Unexpected library errors may contain absolute paths or other private
        # runtime details; return a stable notice instead of raw exception text.
        _write_result(result_path, {"ok": False, "error": "工作表任务失败，原有版本仍可使用。请检查文件或缩小范围后重试。"})


def _stop_process(process):
    if process.is_alive():
        process.terminate()
        process.join(timeout=1)
        if process.is_alive():
            process.kill()
            process.join(timeout=1)


def _monitor_worker(process, deadline, memory_bytes, rss_reader=None):
    rss_reader = rss_reader or _read_rss_bytes
    while process.is_alive():
        if time.monotonic() >= deadline:
            _stop_process(process)
            raise ValueError("任务超过时间限制，已停止；原有版本仍可使用。")
        rss = rss_reader(process.pid)
        if rss is not None and rss > memory_bytes:
            _stop_process(process)
            raise ValueError("任务超过内存限制，已停止；原有版本仍可使用。请缩小工作表或区块范围。")
        process.join(timeout=POLL_SECONDS)


def _temporary_snapshots(root):
    directory = root / "datasets"
    return {p.name for p in directory.glob(".import-*") if p.is_dir()}


def _cleanup_temporary_snapshots(root, previous):
    # Only new unpublished directories belonging to this serialized task are
    # eligible. Never delete a dataset ID, a symlink, or anything outside root.
    root = root.resolve()
    candidate = root / "datasets"
    directory = candidate.resolve()
    if candidate.is_symlink() or directory.parent != root:
        return
    for path in directory.glob(".import-*"):
        if path.name in previous or not path.is_dir() or path.is_symlink():
            continue
        resolved = path.resolve()
        if resolved.parent == directory and resolved.name.startswith(".import-"):
            try:
                shutil.rmtree(resolved)
            except OSError:
                # Unpublished directories are also excluded from dataset lists.
                # A cleanup failure must not expose a private absolute path or
                # overwrite the original task failure.
                pass


def run_review_task(root, action, *, import_id=None, mapping=None,
                    save_template=True, path=None, timeout=None,
                    memory_limit_mb=None, use_reviewed_mappings=True):
    """Return a bounded task result or raise ValueError with a safe Chinese notice.

    Defaults: STRUCTURED_REVIEW_MEMORY_MB=768 and
    STRUCTURED_REVIEW_TIMEOUT_SECONDS=300. RSS checks run every 50 ms, so the
    budget is a termination threshold, not a zero-overshoot allocation promise.
    The caller must hold its cross-action import/confirmation lock throughout.
    """
    if action not in {"import", "preview", "confirm"}:
        raise ValueError("不支持的工作表任务。")
    if type(use_reviewed_mappings) is not bool:
        raise ValueError("原件核对模式必须明确为布尔值。")
    try:
        if timeout is None:
            timeout = float(os.environ.get("STRUCTURED_REVIEW_TIMEOUT_SECONDS", DEFAULT_TIMEOUT_SECONDS))
        if memory_limit_mb is None:
            memory_limit_mb = int(os.environ.get("STRUCTURED_REVIEW_MEMORY_MB", DEFAULT_MEMORY_MB))
        if isinstance(timeout, bool) or not math.isfinite(timeout) or timeout <= 0:
            raise ValueError
        if type(memory_limit_mb) is not int or not 32 <= memory_limit_mb <= 65536:
            raise ValueError
    except (TypeError, ValueError):
        raise ValueError("工作表任务资源限制配置无效。") from None
    root = Path(root).resolve()
    request = {"root": str(root), "action": action, "import_id": import_id,
               "mapping": mapping, "save_template": bool(save_template),
               "use_reviewed_mappings":use_reviewed_mappings,
               "path": str(Path(path).resolve()) if path is not None else None}
    if action == "import" and not request["path"]:
        raise ValueError("导入任务缺少源文件。")
    if action in {"preview", "confirm"} and (not import_id or not isinstance(mapping, dict)):
        raise ValueError("工作表任务缺少导入记录或映射。")
    try:
        encoded = json.dumps(request, ensure_ascii=False, allow_nan=False).encode("utf-8")
    except (TypeError, ValueError):
        raise ValueError("工作表任务参数无效。") from None
    if len(encoded) > MAX_REQUEST_BYTES:
        raise ValueError("工作表配置过大，请减少区块或字段后重试。")
    # Freeze the validated request before spawn serializes it.
    request = json.loads(encoded)
    root.mkdir(parents=True, exist_ok=True)
    previous = _temporary_snapshots(root)
    process = None
    try:
        with tempfile.TemporaryDirectory(prefix=".review-task-", dir=root) as temporary:
            result_path = Path(temporary) / "result.json"
            context = multiprocessing.get_context("spawn")
            process = context.Process(target=_review_child,
                                      args=(request, str(result_path),
                                            memory_limit_mb * 1024 * 1024, timeout),
                                      daemon=True)
            deadline = time.monotonic() + timeout
            process.start()
            _monitor_worker(process, deadline, memory_limit_mb * 1024 * 1024)
            if process.exitcode != 0 or not result_path.is_file():
                raise ValueError("工作进程已停止，任务未能完成；原有版本仍可使用。")
            if result_path.stat().st_size > MAX_RESULT_BYTES:
                raise ValueError("工作表任务结果超过大小限制。")
            try:
                payload = json.loads(result_path.read_text(encoding="utf-8"))
            except (OSError, ValueError):
                raise ValueError("工作表任务没有返回完整结果，请重试。") from None
            if not isinstance(payload, dict) or not payload.get("ok"):
                raise ReviewValidationError(payload.get("error", "工作表任务失败。")
                                 if isinstance(payload, dict) else "工作表任务结果无效。",
                                 target=payload.get("review_target") if isinstance(payload, dict) else None)
            result = payload.get("result")
            if not isinstance(result, dict) or "observations" in result:
                raise ValueError("工作表任务结果无效。")
            return result
    finally:
        if process is not None:
            _stop_process(process)
            process.close()
        _cleanup_temporary_snapshots(root, previous)
