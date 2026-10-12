"""In-process clock with durable schedules and an OS lock shared by workers."""
import logging
import threading
from concurrent.futures import ThreadPoolExecutor

from tin.config import settings
from tin.refresh.service import (RefreshBusy, current_run, execute_refresh, queue_run,
                                 system_session, utc_now, workspace_lock)

log = logging.getLogger(__name__)


class RefreshScheduler:
    def __init__(self):
        self._stop = threading.Event()
        self._thread = None
        self._pool = None
        self._guard = threading.Lock()
        self._submitted = set()

    def submit(self, tenant_id, run_id):
        key = (tenant_id, run_id)
        with self._guard:
            if key in self._submitted:
                return
            if self._pool is None:
                self._pool = ThreadPoolExecutor(max_workers=max(1, min(settings.refresh_workers, 8)),
                                                thread_name_prefix="research-refresh")
            self._submitted.add(key)
            future = self._pool.submit(execute_refresh, tenant_id, run_id)
        def completed(result):
            with self._guard:
                self._submitted.discard(key)
            try:
                result.result()
            except Exception:
                log.exception("Workspace %s refresh worker failed", tenant_id)
        future.add_done_callback(completed)

    def tick(self, now=None):
        now = now or utc_now()
        base = settings.data_dir / "workspaces"
        if not base.exists():
            return
        # No tenant header or arbitrary user id can register a background workspace.
        # Only databases already created by authenticated/migrated workspaces count.
        for folder in sorted(base.iterdir()):
            if not folder.is_dir() or folder.is_symlink() or not folder.name.isdecimal() or not (folder / "tin.db").is_file():
                continue
            tenant_id = int(folder.name)
            if tenant_id <= 0 or str(tenant_id) != folder.name:
                continue
            try:
                with workspace_lock(tenant_id):
                    with system_session(tenant_id) as session:
                        run, created = queue_run(session, trigger="scheduled", now=now)
                        run_id = run.id if run and run.status == "pending" else None
                if run_id:
                    self.submit(tenant_id, run_id)
            except RefreshBusy:
                continue
            except Exception:
                log.exception("Workspace %s refresh scheduling failed", tenant_id)

    def _loop(self):
        while not self._stop.is_set():
            try:
                self.tick()
            except Exception:
                log.exception("Research refresh clock failed; retrying on the next tick")
            self._stop.wait(max(1.0, settings.refresh_poll_seconds))

    def start(self):
        if not settings.weknora_api_url or not settings.refresh_scheduler_enabled:
            return
        if self._thread and self._thread.is_alive():
            return
        self._stop.clear()
        self._thread = threading.Thread(target=self._loop, name="research-refresh-clock", daemon=True)
        self._thread.start()

    def stop(self):
        self._stop.set()
        if self._thread:
            self._thread.join(timeout=2)
        with self._guard:
            pool, self._pool = self._pool, None
        if pool:
            pool.shutdown(wait=False, cancel_futures=True)


scheduler = RefreshScheduler()
start = scheduler.start
stop = scheduler.stop
