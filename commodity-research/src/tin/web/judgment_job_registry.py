"""Small process-independent scheduler metadata; research content stays in its WB."""
from contextlib import contextmanager
from datetime import datetime, timezone
import json
from pathlib import Path
import secrets
import sqlite3
import time

from fastapi import HTTPException

from tin.config import settings

ACTIVE = frozenset({'queued', 'running'})
TERMINAL = frozenset({'succeeded', 'failed', 'interrupted', 'expired', 'applied', 'superseded'})
RESULTS = frozenset({'succeeded', 'applied'})
TTL = 24 * 3600
LEASE = 60
GLOBAL_RUNNING = 2
GLOBAL_PENDING = 10
MAX_RECORDS = 512


def iso(value=None):
    return datetime.fromtimestamp(time.time() if value is None else value, timezone.utc).isoformat()


class Registry:
    def __init__(self, path=None):
        self.path = Path(path) if path else None

    @contextmanager
    def transaction(self):
        path = self.path or Path(settings.data_dir) / 'analysis-jobs.sqlite'
        path.parent.mkdir(parents=True, exist_ok=True)
        connection = sqlite3.connect(path, timeout=10, isolation_level=None)
        try:
            connection.execute('PRAGMA busy_timeout=10000')
            connection.execute('CREATE TABLE IF NOT EXISTS source_mappings ('
                'id VARCHAR(64) PRIMARY KEY, config JSON NOT NULL, version INTEGER NOT NULL, '
                'confirmed_by VARCHAR(128) NOT NULL, confirmed_at DATETIME NOT NULL)')
            path.chmod(0o600)
            connection.execute('BEGIN IMMEDIATE')
            yield connection
            connection.commit()
        except BaseException:
            connection.rollback()
            raise
        finally:
            connection.close()

    def rows(self, connection):
        values = []
        for raw, actor in connection.execute('SELECT config,confirmed_by FROM source_mappings'):
            value = json.loads(raw)
            if value.get('kind') == 'judgment-analysis-index.v1' and value.get('user_id') == actor:
                values.append(value)
        return values

    def write(self, connection, value):
        connection.execute('INSERT INTO source_mappings(id,config,version,confirmed_by,confirmed_at) '
            'VALUES(?,?,1,?,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config, '
            'version=source_mappings.version+1,confirmed_at=excluded.confirmed_at',
            (value['id'], json.dumps(value, separators=(',', ':')), value['user_id'], iso()))

    def order(self, row):
        return row.get('created_order', 0), row['created_at'], row['id']

    def scope_key(self, row):
        return row['tenant_id'], row['user_id'], row['workbench_id'], row.get('purpose', 'judgments')

    def compact(self, connection):
        groups = {}
        for row in self.rows(connection):
            if row['state'] in RESULTS:
                groups.setdefault(self.scope_key(row), []).append(row)
        retired = []
        for rows in groups.values():
            newest = max(rows, key=self.order)
            for row in rows:
                if row['id'] == newest['id']:
                    continue
                row.update(state='superseded', phase='superseded', superseded_by=newest['id'],
                    message='已有更新的分析结果，请打开最新结果', dismissed=True,
                    dismissed_at=row.get('dismissed_at') or iso(), updated_at=iso(), payload_pruned=False)
                self.write(connection, row)
                retired.append(row)
        return retired

    def expire(self, connection):
        now = time.time()
        for row in self.rows(connection):
            state = row['state']
            if row['expires_unix'] <= now:
                row.update(state='expired', phase='expired', message='分析结果已过期，请重新分析')
            elif state in ACTIVE and row['lease_until'] <= now:
                row.update(state='interrupted', phase='interrupted', message='分析已中断，请重新发起')
            if state != row['state']:
                row.update(updated_at=iso(), finished_at=iso())
                self.write(connection, row)
        self.compact(connection)

    def create(self, scope, request_hash, idempotency_hash, worker_id, purpose='judgments'):
        if purpose not in {'judgments', 'diagram'}:
            raise ValueError('Unsupported analysis task purpose')
        with self.transaction() as connection:
            self.expire(connection)
            rows = self.rows(connection)
            for row in rows:
                if (row['tenant_id'], row['user_id'], row['workbench_id'], row['idempotency_hash'], row.get('purpose', 'judgments')) == (
                        scope.identity.tenant_id, scope.identity.user_id, scope.workbench_id, idempotency_hash, purpose):
                    if row['request_hash'] != request_hash:
                        raise HTTPException(409, '重复请求标识已用于不同资料，请重新发起')
                    return row, False
            active = [row for row in rows if row['state'] in ACTIVE]
            # Independent workbenches and task purposes may run for one actor.
            # Idempotency above handles retries; claim() reserves shared running
            # slots atomically, so admission must not serialize an entire actor.
            if len(active) >= GLOBAL_PENDING:
                raise HTTPException(429, '分析队列已满，请稍后重试')
            if len(rows) >= MAX_RECORDS:
                raise HTTPException(429, '分析任务记录已达容量，请稍后重试')
            now = time.time()
            row = {'kind': 'judgment-analysis-index.v1', 'id': secrets.token_hex(16),
                **({'purpose': 'diagram'} if purpose == 'diagram' else {}),
                'tenant_id': scope.identity.tenant_id, 'user_id': scope.identity.user_id,
                'workbench_id': scope.workbench_id, 'storage_legacy': scope.metadata.get('workspace_mode') == 'legacy',
                'request_hash': request_hash, 'idempotency_hash': idempotency_hash,
                'created_order': max((row.get('created_order', 0) for row in rows), default=0) + 1,
                'worker_id': worker_id, 'fence': secrets.token_hex(16), 'lease_until': now + LEASE,
                'state': 'queued', 'phase': 'queued', 'created_at': iso(now), 'updated_at': iso(now),
                'finished_at': None, 'expires_at': iso(now + TTL), 'expires_unix': now + TTL}
            self.write(connection, row)
            return row, True

    def owned(self, value, identity, workbench_id=None):
        if (value is None or value.get('kind') != 'judgment-analysis-index.v1'
                or value.get('tenant_id') != identity.tenant_id or value.get('user_id') != identity.user_id
                or workbench_id is not None and value.get('workbench_id') != workbench_id):
            raise HTTPException(404, '分析任务不存在或无权访问')
        return value

    def fetch(self, connection, job_id):
        row = connection.execute('SELECT config,confirmed_by FROM source_mappings WHERE id=?', (job_id,)).fetchone()
        value = json.loads(row[0]) if row else None
        return value if value and value.get('user_id') == row[1] and value.get('id') == job_id else None

    def get(self, identity, job_id, workbench_id=None):
        with self.transaction() as connection:
            self.expire(connection)
            return self.owned(self.fetch(connection, job_id), identity, workbench_id)

    def guard_result(self, identity, job_id, workbench_id, purpose):
        # A private preview transaction may already hold its WB write lock.
        # This read must never take the scheduler's BEGIN IMMEDIATE write lock:
        # completion takes that lock before the WB lock. Each SELECT releases
        # its read lock immediately, avoiding an inverted lock order.
        path = self.path or Path(settings.data_dir) / 'analysis-jobs.sqlite'
        if not path.exists():
            raise HTTPException(404, '分析任务不存在或无权访问')
        connection = sqlite3.connect(path.resolve().as_uri() + '?mode=ro', uri=True,
            timeout=10, isolation_level=None)
        try:
            row = self.owned(self.fetch(connection, job_id), identity, workbench_id)
            if row.get('purpose', 'judgments') != purpose:
                raise HTTPException(404, '分析任务不存在或无权访问')
            newer = any(self.scope_key(other) == self.scope_key(row)
                and other['state'] in RESULTS and self.order(other) > self.order(row)
                for other in self.rows(connection))
            if row['state'] in {'superseded', 'expired'} or row['expires_unix'] <= time.time() or newer:
                raise HTTPException(410, '已有更新的分析结果或结果已过期，请打开最新结果')
        finally:
            connection.close()

    def listing(self, identity, limit=20):
        with self.transaction() as connection:
            self.expire(connection)
            rows = [row for row in self.rows(connection) if row.get('tenant_id') == identity.tenant_id
                and row.get('user_id') == identity.user_id and row.get('kind') == 'judgment-analysis-index.v1']
            return sorted(rows, key=self.order, reverse=True)[:limit]

    def latest(self, identity, workbench_id, purpose):
        if purpose not in {'judgments', 'diagram'}:
            raise ValueError('Unsupported analysis task purpose')
        with self.transaction() as connection:
            self.expire(connection)
            rows = [row for row in self.rows(connection) if self.scope_key(row) == (
                identity.tenant_id, identity.user_id, workbench_id, purpose)
                and row['state'] == 'succeeded']
            return max(rows, key=self.order, default=None)

    def discard_result(self, identity, job_id, workbench_id, purpose):
        """Retire this actor's unsaved result on request. It becomes a superseded record without a successor,
        so the normal pruning removes its private preview; a repeated request returns the same record."""
        with self.transaction() as connection:
            self.expire(connection)
            row = self.owned(self.fetch(connection, job_id), identity, workbench_id)
            if row.get('purpose', 'judgments') != purpose:
                raise HTTPException(404, '分析任务不存在或无权访问')
            if row['state'] == 'superseded':
                return row
            if row['state'] in ACTIVE:
                raise HTTPException(409, '分析仍在进行，完成后才能舍弃结果')
            if row['state'] == 'applied':
                raise HTTPException(409, '此分析已保存为草稿，不能舍弃；可在版本记录中管理草稿')
            if row['state'] != 'succeeded':
                raise HTTPException(410, '这份结果已过期或已失效，无需舍弃')
            row.update(state='superseded', phase='superseded', superseded_by=None, discarded=True,
                message='已舍弃暂存结果', dismissed=True, dismissed_at=row.get('dismissed_at') or iso(),
                updated_at=iso(), finished_at=row.get('finished_at') or iso(), payload_pruned=False)
            self.write(connection, row)
            return row

    def dismiss(self, identity, job_id):
        with self.transaction() as connection:
            self.expire(connection)
            row = self.owned(self.fetch(connection, job_id), identity)
            if row['state'] in ACTIVE:
                raise HTTPException(409, '分析仍在进行，完成后可忽略提示')
            if not row.get('dismissed'):
                row.update(dismissed=True, dismissed_at=iso())
                self.write(connection, row)
            return row

    def dismiss_all(self, identity):
        """Acknowledge only this actor's terminal notices, without pruning results."""
        with self.transaction() as connection:
            self.expire(connection)
            rows = sorted((row for row in self.rows(connection)
                if (row['tenant_id'], row['user_id']) == (identity.tenant_id, identity.user_id)
                and row['state'] in TERMINAL), key=self.order)
            for row in rows:
                if not row.get('dismissed'):
                    row.update(dismissed=True, dismissed_at=iso())
                    self.write(connection, row)
            # Already acknowledged IDs are included so retries can reconcile a
            # successful request whose response was lost. Active IDs never enter.
            return [row['id'] for row in rows]

    def claim(self, job_id, worker_id, fence):
        with self.transaction() as connection:
            self.expire(connection)
            row = self.fetch(connection, job_id)
            if not self.matches(row, worker_id, fence):
                raise HTTPException(409, '分析任务已中断，请重新发起')
            if row['state'] == 'queued':
                if sum(other['state'] == 'running' for other in self.rows(connection)) >= GLOBAL_RUNNING:
                    return False
                row.update(state='running', phase='selecting', updated_at=iso(), lease_until=time.time() + LEASE)
                self.write(connection, row)
            return True

    def matches(self, row, worker_id, fence):
        return bool(row and row['state'] in ACTIVE and row['worker_id'] == worker_id
            and row['fence'] == fence and row['lease_until'] > time.time())

    def touch(self, job_id, worker_id, fence, phase=None):
        with self.transaction() as connection:
            row = self.fetch(connection, job_id)
            if not self.matches(row, worker_id, fence):
                raise HTTPException(409, '分析任务已中断，请重新发起')
            row.update(lease_until=time.time() + LEASE, updated_at=iso())
            if phase:
                row['phase'] = phase
            self.write(connection, row)

    @contextmanager
    def fenced(self, job_id, worker_id, fence):
        with self.transaction() as connection:
            row = self.fetch(connection, job_id)
            if not self.matches(row, worker_id, fence):
                raise HTTPException(409, '分析任务已中断，请重新发起')
            yield connection, row

    def finish(self, job_id, worker_id, fence, state, message='', *, diagnostic=None):
        with self.transaction() as connection:
            row = self.fetch(connection, job_id)
            if not row or row['worker_id'] != worker_id or row['fence'] != fence or row['state'] not in ACTIVE:
                return
            if diagnostic is not None and row.get('purpose', 'judgments') == 'judgments' and state == 'failed':
                from tin.web.judgment_failures import safe_diagnostic, render_failure
                safe = safe_diagnostic(diagnostic)
                if safe is not None:
                    row['failure'] = safe
                message = render_failure(safe)
            row.update(state=state, phase=state, message=message, updated_at=iso(), finished_at=iso())
            self.write(connection, row)
            self.compact(connection)

    def applied(self, identity, job_id, workbench_id):
        with self.transaction() as connection:
            row = self.owned(self.fetch(connection, job_id), identity, workbench_id)
            if row['state'] in {'expired', 'superseded'}:
                return
            row.update(state='applied', phase='applied', updated_at=iso(), finished_at=iso())
            self.write(connection, row)
            self.compact(connection)

    def reconcile(self, identity, job_id, workbench_id, state):
        with self.transaction() as connection:
            row = self.owned(self.fetch(connection, job_id), identity, workbench_id)
            if (state not in RESULTS | {'superseded'} or row['expires_unix'] <= time.time()
                    or row['state'] in {'expired', 'superseded'}):
                return row
            row.update(state=state, phase=state, updated_at=iso(), finished_at=row.get('finished_at') or iso())
            if state == 'superseded':
                row.update(dismissed=True, dismissed_at=row.get('dismissed_at') or iso())
            self.write(connection, row)
            self.compact(connection)
            return self.fetch(connection, job_id)

    def expired(self):
        with self.transaction() as connection:
            self.expire(connection)
            return [row for row in self.rows(connection) if row['expires_unix'] <= time.time()]

    def discarded(self):
        with self.transaction() as connection:
            self.expire(connection)
            return [row for row in self.rows(connection) if row['state'] == 'expired'
                or row['state'] == 'superseded' and not row.get('payload_pruned')]

    def mark_pruned(self, job_id):
        with self.transaction() as connection:
            row = self.fetch(connection, job_id)
            if row and row['state'] == 'superseded':
                row['payload_pruned'] = True
                self.write(connection, row)

    def remove(self, job_id):
        with self.transaction() as connection:
            connection.execute('DELETE FROM source_mappings WHERE id=? AND '
                "json_extract(config,'$.state')='expired'", (job_id,))


def metadata(row):
    result = {key: row.get(key) for key in ('id', 'workbench_id', 'state', 'phase',
        'created_at', 'updated_at', 'finished_at', 'expires_at', 'message',
        'dismissed_at', 'superseded_by')}
    purpose = row.get('purpose', 'judgments')
    if purpose == 'judgments' and row.get('state') == 'failed' and 'failure' in row:
        from tin.web.judgment_failures import safe_diagnostic, render_failure
        safe = safe_diagnostic(row['failure'])
        result['message'] = render_failure(safe)
        if safe is not None:
            result['failure'] = safe
    return {**result, 'job_id': row['id'], 'purpose': purpose,
        'title': '产业图配置生成' if purpose == 'diagram' else '研究逻辑分析',
        'result_available': row['state'] == 'succeeded', 'dismissed': row.get('dismissed') is True,
        'discarded': row.get('discarded') is True}
