"""Cold native chart caches coordinate real dataset/version paths across workers."""
from concurrent.futures import ThreadPoolExecutor
import json
import multiprocessing
import threading

import pytest

from structured_data import native_charts
from structured_data.store import DataStore


DATASET = 'a' * 24


def _store(root, dataset=DATASET):
    store = DataStore(root)
    folder = store.datasets / dataset
    folder.mkdir(parents=True, exist_ok=True)
    (folder / 'manifest.json').write_text(json.dumps({'dataset_id': dataset}))
    return store


def _snapshot(store, dataset):
    return {'version': native_charts.VERSION, 'dataset_id': dataset,
            'chart_count': 1, 'charts': [{'title': str(store.root)}],
            'views': [], 'curve_views': []}


def _process_read(root, barrier, results, count, entered, release):
    def build(bound_store, dataset):
        with count.get_lock():
            count.value += 1
        entered.set()
        assert release.wait(timeout=10)
        return _snapshot(bound_store, dataset)

    native_charts.build = build
    try:
        barrier.wait(timeout=10)
        result = native_charts.index(DataStore(root), DATASET)
        results.put(('ok', result))
    except BaseException as exc:
        results.put(('error', type(exc).__name__))


def test_concurrent_threads_build_one_cold_index(tmp_path, monkeypatch):
    store = _store(tmp_path)
    entered, release = threading.Event(), threading.Event()
    barrier = threading.Barrier(8)
    calls = []

    def build(bound_store, dataset):
        calls.append(dataset)
        entered.set()
        assert release.wait(timeout=5)
        return _snapshot(bound_store, dataset)

    monkeypatch.setattr(native_charts, 'build', build)

    def read():
        barrier.wait(timeout=5)
        return native_charts.index(store, DATASET)

    with ThreadPoolExecutor(max_workers=8) as pool:
        futures = [pool.submit(read) for _ in range(8)]
        try:
            assert entered.wait(timeout=5)
        finally:
            release.set()
        results = [future.result(timeout=5) for future in futures]
    assert calls == [DATASET]
    assert results == [_snapshot(store, DATASET)] * 8
    # Cache reads deserialize independently; caller mutation cannot poison it.
    results[0]['charts'][0]['title'] = 'caller mutation'
    assert native_charts.index(store, DATASET) == _snapshot(store, DATASET)


def test_concurrent_processes_share_the_same_build_lock(tmp_path):
    store = _store(tmp_path)
    # Start independent interpreters, as for server workers. Forking the pytest
    # process could inherit unrelated live library threads or held locks.
    ctx = multiprocessing.get_context('spawn')
    count = ctx.Value('i', 0)
    entered, release = ctx.Event(), ctx.Event()
    barrier = ctx.Barrier(4)
    results = ctx.Queue()

    workers = [ctx.Process(target=_process_read,
                           args=(str(tmp_path), barrier, results, count, entered, release))
               for _ in range(4)]
    try:
        for worker in workers:
            worker.start()
        assert entered.wait(timeout=10)
        release.set()
        actual = [results.get(timeout=10) for _ in workers]
        for worker in workers:
            worker.join(timeout=10)
            assert worker.exitcode == 0
        assert count.value == 1
        assert actual == [('ok', _snapshot(store, DATASET))] * 4
    finally:
        release.set()
        for worker in workers:
            if worker.is_alive():
                worker.terminate()
            worker.join(timeout=5)
        results.close()


def test_failed_builder_releases_lock_without_publishing(tmp_path, monkeypatch):
    store = _store(tmp_path)
    calls = []

    def build(bound_store, dataset):
        calls.append(dataset)
        if len(calls) == 1:
            raise RuntimeError('synthetic source read failure')
        return _snapshot(bound_store, dataset)

    monkeypatch.setattr(native_charts, 'build', build)
    path = store._path(DATASET) / f'{native_charts.VERSION}.json'
    with pytest.raises(RuntimeError, match='synthetic source'):
        native_charts.index(store, DATASET)
    assert not path.exists()
    assert not list(path.parent.glob('.charts-*'))
    assert native_charts.index(store, DATASET) == _snapshot(store, DATASET)
    assert calls == [DATASET, DATASET]


def test_failed_atomic_serialization_keeps_cache_absent(tmp_path, monkeypatch):
    store = _store(tmp_path)
    monkeypatch.setattr(native_charts, 'build', _snapshot)
    real_dump = native_charts.json.dump

    def partial_dump(result, stream, **kwargs):
        stream.write('{"partial":')
        raise OSError('synthetic disk write failure')

    monkeypatch.setattr(native_charts.json, 'dump', partial_dump)
    path = store._path(DATASET) / f'{native_charts.VERSION}.json'
    with pytest.raises(OSError, match='synthetic disk'):
        native_charts.index(store, DATASET)
    assert not path.exists()
    assert not list(path.parent.glob('.charts-*'))
    monkeypatch.setattr(native_charts.json, 'dump', real_dump)
    assert native_charts.index(store, DATASET) == _snapshot(store, DATASET)


def test_waiter_budget_does_not_cancel_owner_or_launch_duplicate(tmp_path, monkeypatch):
    store = _store(tmp_path)
    entered, release = threading.Event(), threading.Event()
    calls = []

    def build(bound_store, dataset):
        calls.append(dataset)
        entered.set()
        assert release.wait(timeout=5)
        return _snapshot(bound_store, dataset)

    monkeypatch.setattr(native_charts, 'build', build)
    monkeypatch.setattr(native_charts, 'INDEX_BUILD_WAIT_SECONDS', 0)
    with ThreadPoolExecutor(max_workers=1) as pool:
        owner = pool.submit(native_charts.index, store, DATASET)
        try:
            assert entered.wait(timeout=5)
            with pytest.raises(native_charts.ChartIndexBuildPending, match='still being prepared'):
                native_charts.index(store, DATASET)
        finally:
            release.set()
        assert owner.result(timeout=5) == _snapshot(store, DATASET)
    assert native_charts.index(store, DATASET) == _snapshot(store, DATASET)
    assert calls == [DATASET]


def test_real_cache_key_keeps_workspaces_and_datasets_independent(tmp_path, monkeypatch):
    first = _store(tmp_path / 'workspace-one')
    second = _store(tmp_path / 'workspace-two')
    third = _store(first.root, 'b' * 24)
    barrier = threading.Barrier(3)
    calls = []

    def build(bound_store, dataset):
        calls.append((str(bound_store.root), dataset))
        # An accidental global build lock would deadlock these independent keys.
        barrier.wait(timeout=5)
        return _snapshot(bound_store, dataset)

    monkeypatch.setattr(native_charts, 'build', build)
    sources = [(first, DATASET), (second, DATASET), (third, 'b' * 24)]
    with ThreadPoolExecutor(max_workers=3) as pool:
        futures = [pool.submit(native_charts.index, store, dataset)
                   for store, dataset in sources]
        assert [future.result(timeout=5) for future in futures] == [
            _snapshot(store, dataset) for store, dataset in sources]
    assert len(calls) == 3


def test_index_versions_have_distinct_cache_and_lock_keys(tmp_path, monkeypatch):
    store = _store(tmp_path)
    first_version = native_charts.VERSION
    calls = []

    def build(bound_store, dataset):
        calls.append(native_charts.VERSION)
        return _snapshot(bound_store, dataset)

    monkeypatch.setattr(native_charts, 'build', build)
    original = native_charts.index(store, DATASET)
    monkeypatch.setattr(native_charts, 'VERSION', first_version + '-test-next')
    updated = native_charts.index(store, DATASET)
    assert updated['version'] != original['version']
    assert native_charts.index(store, DATASET) == updated
    monkeypatch.setattr(native_charts, 'VERSION', first_version)
    assert native_charts.index(store, DATASET) == original
    assert calls == [first_version, first_version + '-test-next']
    assert len(list(store._path(DATASET).glob('*.json.lock'))) == 2


@pytest.mark.parametrize('damaged', ['{invalid', json.dumps({'dataset_id': 'b' * 24}),
                                    json.dumps({'dataset_id': DATASET, 'version': 'outdated'})])
def test_damaged_or_wrong_identity_cache_fails_closed(tmp_path, monkeypatch, damaged):
    store = _store(tmp_path)
    path = store._path(DATASET) / f'{native_charts.VERSION}.json'
    path.write_text(damaged)

    def forbidden_build(*args):
        raise AssertionError('a damaged cache must remain an explicit failure')

    monkeypatch.setattr(native_charts, 'build', forbidden_build)
    with pytest.raises(ValueError):
        native_charts.index(store, DATASET)
    assert path.read_text() == damaged
