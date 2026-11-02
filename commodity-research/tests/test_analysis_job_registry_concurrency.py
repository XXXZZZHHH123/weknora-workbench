"""Independent requests share bounded execution, not a per-account mutex."""
from concurrent.futures import ThreadPoolExecutor
from threading import Barrier
from types import SimpleNamespace

from fastapi import HTTPException
import pytest

from tin.web.judgment_job_registry import GLOBAL_PENDING, GLOBAL_RUNNING, Registry


def scope(workbench='board-a'):
    return SimpleNamespace(identity=SimpleNamespace(user_id='synthetic-owner', tenant_id=71),
        workbench_id=workbench, metadata={})


def test_same_account_can_admit_multiple_workbenches_and_purposes_but_shares_execution_slots(tmp_path):
    path = tmp_path / 'index.sqlite'
    first, second = Registry(path), Registry(path)
    rows = [index.create(board, 'request-' + str(number), 'key-' + str(number),
        'worker-' + str(number), purpose)[0] for number, (index, board, purpose) in enumerate([
            (first, scope(), 'judgments'), (second, scope(), 'diagram'),
            (first, scope('board-b'), 'diagram')])]
    assert len({row['id'] for row in rows}) == 3
    assert all(row['state'] == 'queued' for row in rows)
    assert all(first.claim(row['id'], row['worker_id'], row['fence']) for row in rows[:GLOBAL_RUNNING])
    waiting = rows[GLOBAL_RUNNING]
    assert not second.claim(waiting['id'], waiting['worker_id'], waiting['fence'])
    running = rows[0]
    first.finish(running['id'], running['worker_id'], running['fence'], 'failed')
    assert second.claim(waiting['id'], waiting['worker_id'], waiting['fence'])
    assert first.get(scope().identity, rows[1]['id'])['state'] == 'running'


def test_idempotent_retry_remains_scoped_and_conflicting_input_is_rejected_with_parallel_jobs(tmp_path):
    index, owner = Registry(tmp_path / 'index.sqlite'), scope()
    first, created = index.create(owner, 'same-source', 'same-key', 'worker-a')
    assert created
    retry, created = index.create(owner, 'same-source', 'same-key', 'worker-b')
    assert not created and retry['id'] == first['id'] and retry['worker_id'] == 'worker-a'
    with pytest.raises(HTTPException) as conflict:
        index.create(owner, 'changed-source', 'same-key', 'worker-b')
    assert conflict.value.status_code == 409
    other_purpose, created = index.create(owner, 'same-source', 'same-key', 'worker-b', 'diagram')
    assert created and other_purpose['id'] != first['id']
    other_board, created = index.create(scope('board-b'), 'same-source', 'same-key', 'worker-c')
    assert created and other_board['id'] not in {first['id'], other_purpose['id']}


def test_same_account_parallel_admissions_obey_atomic_global_queue_capacity(tmp_path):
    path, attempts = tmp_path / 'index.sqlite', GLOBAL_PENDING + 4
    ready = Barrier(attempts)

    def admit(number):
        index = Registry(path)
        ready.wait(timeout=10)
        try:
            row, created = index.create(scope(), 'request-' + str(number), 'key-' + str(number),
                'worker-' + str(number), 'diagram' if number % 2 else 'judgments')
            assert created
            return row['id']
        except HTTPException as error:
            assert error.status_code == 429 and error.detail == '分析队列已满，请稍后重试'
            return None

    with ThreadPoolExecutor(max_workers=attempts) as pool:
        outcomes = list(pool.map(admit, range(attempts)))
    accepted = [identifier for identifier in outcomes if identifier]
    assert len(accepted) == len(set(accepted)) == GLOBAL_PENDING
    index = Registry(path)
    rows = index.listing(scope().identity)
    assert {row['id'] for row in rows} == set(accepted)
    ready = Barrier(len(rows))

    def claim(row):
        ready.wait(timeout=10)
        return Registry(path).claim(row['id'], row['worker_id'], row['fence'])

    with ThreadPoolExecutor(max_workers=len(rows)) as pool:
        assert sum(pool.map(claim, rows)) == GLOBAL_RUNNING
    assert sum(row['state'] == 'running' for row in index.listing(scope().identity)) == GLOBAL_RUNNING
