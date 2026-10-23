"""Small durable job ledger. One scheduler process owns the database and data directory."""
import json
import sqlite3
import time
import uuid

ACTIVE = ('queued', 'running')
TERMINAL = ('completed', 'failed', 'cancelled')


class QueueFull(Exception):
    pass


class RetryConflict(Exception):
    pass


class RetryOwnerMismatch(Exception):
    pass


class Store:
    def __init__(self, path, global_limit=16, owner_limit=4, retention=86400, scope_limit=8):
        self.db = sqlite3.connect(str(path), isolation_level=None)
        self.db.row_factory = sqlite3.Row
        self.db.executescript('''
          PRAGMA journal_mode=WAL;
          PRAGMA synchronous=FULL;
          CREATE TABLE IF NOT EXISTS jobs (
            id TEXT PRIMARY KEY, scope TEXT NOT NULL, owner TEXT NOT NULL,
            digest TEXT NOT NULL, profile TEXT NOT NULL, status TEXT NOT NULL,
            created REAL NOT NULL, queued REAL NOT NULL, started REAL, finished REAL,
            execution_used REAL NOT NULL DEFAULT 0, size INTEGER NOT NULL,
            progress TEXT NOT NULL DEFAULT '{}', error TEXT,
            UNIQUE(scope,digest,profile));
          CREATE TABLE IF NOT EXISTS owners (owner TEXT PRIMARY KEY, served REAL NOT NULL);
          CREATE TABLE IF NOT EXISTS leases (id TEXT PRIMARY KEY, job TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1);
          CREATE TABLE IF NOT EXISTS retry_requests (
            job TEXT NOT NULL, key_hash TEXT NOT NULL, owner TEXT NOT NULL,
            lease_id TEXT NOT NULL, created REAL NOT NULL, PRIMARY KEY(job,key_hash));
          CREATE INDEX IF NOT EXISTS jobs_status ON jobs(status,queued);
        ''')
        self.global_limit, self.owner_limit, self.retention = global_limit, owner_limit, retention
        self.scope_limit = scope_limit

    def lease(self, job_id):
        lease_id = uuid.uuid4().hex
        self.db.execute('INSERT INTO leases(id,job) VALUES(?,?)', (lease_id, job_id))
        return lease_id

    def detach(self, job_id, lease_id):
        row = self.db.execute('SELECT id FROM leases WHERE id=? AND job=?', (lease_id, job_id)).fetchone()
        if not row:
            return None
        self.db.execute('UPDATE leases SET active=0 WHERE id=?', (lease_id,))
        return self.db.execute('SELECT count(*) FROM leases WHERE job=? AND active=1', (job_id,)).fetchone()[0]

    def close(self):
        self.db.close()

    def get(self, job_id, scope=None):
        row = self.db.execute('SELECT * FROM jobs WHERE id=?', (job_id,)).fetchone()
        if row is None or (scope is not None and row['scope'] != scope):
            return None
        if row['finished'] and row['finished'] < time.time() - self.retention:
            return None
        return dict(row)

    def find(self, scope, digest, profile):
        row = self.db.execute('SELECT id FROM jobs WHERE scope=? AND digest=? AND profile=?',
                              (scope, digest, profile)).fetchone()
        return self.get(row['id'], scope) if row else None

    def check_capacity(self, owner, scope=None):
        total = self.db.execute("SELECT count(*) FROM jobs WHERE status IN ('queued','running')").fetchone()[0]
        own = self.db.execute("SELECT count(*) FROM jobs WHERE owner=? AND status IN ('queued','running')", (owner,)).fetchone()[0]
        scoped = self.db.execute("SELECT count(*) FROM jobs WHERE scope=? AND status IN ('queued','running')", (scope,)).fetchone()[0]
        if total >= self.global_limit or own >= self.owner_limit or scoped >= self.scope_limit:
            raise QueueFull()

    def insert(self, scope, owner, digest, profile, size):
        existing = self.find(scope, digest, profile)
        if existing:
            return existing, True
        self.check_capacity(owner, scope)
        job_id = uuid.uuid4().hex
        now = time.time()
        self.db.execute('INSERT INTO jobs(id,scope,owner,digest,profile,status,created,queued,size) VALUES(?,?,?,?,?,?,?,?,?)',
                        (job_id, scope, owner, digest, profile, 'queued', now, now, size))
        return self.get(job_id), False

    def recover(self):
        # Checkpoints survive; consumed execution budget survives restart as well.
        self.db.execute("UPDATE jobs SET status='queued',queued=?,started=NULL WHERE status='running'", (time.time(),))

    def take(self):
        row = self.db.execute('''SELECT j.* FROM jobs j LEFT JOIN owners o ON o.owner=j.owner
          WHERE j.status='queued' ORDER BY COALESCE(o.served,0),j.queued,j.id LIMIT 1''').fetchone()
        if not row:
            return None
        now = time.time()
        self.db.execute("UPDATE jobs SET status='running',started=? WHERE id=?", (now, row['id']))
        self.db.execute('INSERT INTO owners(owner,served) VALUES(?,?) ON CONFLICT(owner) DO UPDATE SET served=excluded.served', (row['owner'], now))
        return self.get(row['id'])

    def progress(self, job_id, progress, elapsed):
        self.db.execute("UPDATE jobs SET progress=?,execution_used=? WHERE id=? AND status='running'",
                        (json.dumps(progress, ensure_ascii=False), elapsed, job_id))

    def finish(self, job_id, status, error=None, elapsed=None):
        assert status in TERMINAL
        self.db.execute("UPDATE jobs SET status=?,error=?,finished=?,execution_used=COALESCE(?,execution_used) WHERE id=? AND status IN ('queued','running')",
                        (status, json.dumps(error) if error else None, time.time(), elapsed, job_id))

    def retry(self, job_id, key_hash, owner):
        # The manual action key is durable. A lost HTTP response or a native task
        # retry must never buy a second execution for the same user action.
        self.db.execute('BEGIN IMMEDIATE')
        try:
            job = self.get(job_id)
            previous = self.db.execute(
                'SELECT owner,lease_id FROM retry_requests WHERE job=? AND key_hash=?',
                (job_id, key_hash)).fetchone()
            if previous:
                if previous['owner'] != owner:
                    raise RetryOwnerMismatch()
                self.db.execute('COMMIT')
                return job, previous['lease_id'], True
            if not job or job['status'] not in ('failed', 'cancelled'):
                raise RetryConflict()
            self.check_capacity(owner, job['scope'])
            self.db.execute('UPDATE leases SET active=0 WHERE job=?', (job_id,))
            self.db.execute("UPDATE jobs SET status='queued',owner=?,queued=?,started=NULL,finished=NULL,error=NULL,execution_used=0 WHERE id=?", (owner, time.time(), job_id))
            lease_id = self.lease(job_id)
            self.db.execute('INSERT INTO retry_requests(job,key_hash,owner,lease_id,created) VALUES(?,?,?,?,?)',
                            (job_id, key_hash, owner, lease_id, time.time()))
            self.db.execute('COMMIT')
            return self.get(job_id), lease_id, False
        except BaseException:
            if self.db.in_transaction:
                self.db.execute('ROLLBACK')
            raise

    def requeue(self, job_id):
        self.db.execute("UPDATE jobs SET status='queued',queued=?,started=NULL WHERE id=? AND status='running'", (time.time(), job_id))

    def expire_queue(self, budget):
        ids = [r[0] for r in self.db.execute("SELECT id FROM jobs WHERE status='queued' AND queued<?", (time.time()-budget,))]
        for job_id in ids:
            self.finish(job_id, 'failed', {'code': 'QUEUE_TIMEOUT', 'message': '排队等待超时，请稍后手动重试。'})

    def expired(self):
        return [r[0] for r in self.db.execute('SELECT id FROM jobs WHERE finished IS NOT NULL AND finished<?', (time.time()-self.retention,))]

    def delete(self, job_id):
        self.db.execute('DELETE FROM retry_requests WHERE job=?', (job_id,))
        self.db.execute('DELETE FROM leases WHERE job=?', (job_id,))
        self.db.execute('DELETE FROM jobs WHERE id=? AND finished IS NOT NULL', (job_id,))

    def counts(self):
        return dict(self.db.execute('SELECT status,count(*) FROM jobs GROUP BY status').fetchall())
