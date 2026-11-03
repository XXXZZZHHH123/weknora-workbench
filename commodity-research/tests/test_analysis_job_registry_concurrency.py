"""Independent requests share bounded execution, not a per-account mutex."""
from concurrent.futures import ThreadPoolExecutor
from threading import Barrier
from types import SimpleNamespace

from fastapi import HTTPException
import pytest

from tin.web.judgment_job_registry import ACTOR_ACTIVE, GLOBAL_PENDING, GLOBAL_RUNNING, Registry


def scope(workbench='board-a', user='synthetic-owner'):
    return SimpleNamespace(identity=SimpleNamespace(user_id=user, tenant_id=71),
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


def test_parallel_admissions_from_many_accounts_obey_atomic_global_queue_capacity(tmp_path):
    path, attempts = tmp_path / 'index.sqlite', GLOBAL_PENDING + 4
    ready = Barrier(attempts)

    def owner(number):
        return scope('board-' + str(number % 3), user='synthetic-owner-' + str(number))

    def admit(number):
        index = Registry(path)
        ready.wait(timeout=10)
        try:
            row, created = index.create(owner(number), 'request-' + str(number), 'key-' + str(number),
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
    rows = [row for number in range(attempts) for row in index.listing(owner(number).identity)]
    assert {row['id'] for row in rows} == set(accepted)
    ready = Barrier(len(rows))

    def claim(row):
        ready.wait(timeout=10)
        return Registry(path).claim(row['id'], row['worker_id'], row['fence'])

    with ThreadPoolExecutor(max_workers=len(rows)) as pool:
        assert sum(pool.map(claim, rows)) == GLOBAL_RUNNING


def test_one_account_runs_both_purposes_in_two_workbenches_but_leaves_queue_for_others(tmp_path):
    assert 4 <= ACTOR_ACTIVE < GLOBAL_PENDING
    path = tmp_path / 'index.sqlite'
    index = Registry(path)
    mine = [index.create(scope(board), 'request-' + board + purpose, 'key-' + board + purpose,
        'worker-' + board + purpose, purpose)[0]
        for board in ('board-a', 'board-b') for purpose in ('judgments', 'diagram')]
    for number in range(len(mine), ACTOR_ACTIVE):
        mine.append(index.create(scope('board-' + str(number)), 'request-' + str(number),
            'key-' + str(number), 'worker-' + str(number))[0])
    with pytest.raises(HTTPException) as busy:
        index.create(scope('board-z'), 'request-extra', 'key-extra', 'worker-extra', 'diagram')
    assert busy.value.status_code == 429 and busy.value.detail == '当前账号进行中的分析任务较多，请等待已有任务完成'
    retry, created = index.create(scope('board-a'), 'request-board-ajudgments', 'key-board-ajudgments', 'worker-retry')
    assert not created and retry['id'] == mine[0]['id']
    other, created = index.create(scope(user='synthetic-other'), 'request-other', 'key-other', 'worker-other')
    assert created
    index.finish(mine[0]['id'], mine[0]['worker_id'], mine[0]['fence'], 'failed')
    again, created = index.create(scope('board-z'), 'request-extra', 'key-extra', 'worker-extra', 'diagram')
    assert created and again['state'] == 'queued'


def test_same_account_parallel_admissions_obey_atomic_per_account_capacity(tmp_path):
    path, attempts = tmp_path / 'index.sqlite', ACTOR_ACTIVE + 4
    ready = Barrier(attempts)

    def admit(number):
        index = Registry(path)
        ready.wait(timeout=10)
        try:
            row, created = index.create(scope('board-' + str(number)), 'request-' + str(number),
                'key-' + str(number), 'worker-' + str(number), 'diagram' if number % 2 else 'judgments')
            assert created
            return row['id']
        except HTTPException as error:
            assert error.status_code == 429 and error.detail == '当前账号进行中的分析任务较多，请等待已有任务完成'
            return None

    with ThreadPoolExecutor(max_workers=attempts) as pool:
        outcomes = list(pool.map(admit, range(attempts)))
    accepted = [identifier for identifier in outcomes if identifier]
    assert len(accepted) == len(set(accepted)) == ACTOR_ACTIVE
    assert {row['id'] for row in Registry(path).listing(scope().identity)} == set(accepted)
