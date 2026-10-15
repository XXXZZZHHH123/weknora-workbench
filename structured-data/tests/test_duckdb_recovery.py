"""Committed indexed WALs survive recovery, queries, and incremental imports."""
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import textwrap
import threading

import duckdb
from openpyxl import Workbook
import pytest

from structured_data.snapshots import publish_parts
from structured_data.store import DataStore


@pytest.fixture
def dataset(tmp_path):
    source = tmp_path / "source.xlsx"
    book = Workbook()
    book.active.title = "records"
    book.active.append(["date", "value"])
    book.active.append(["2024-03-01", 7])
    book.active.append(["2024-03-02", 11])
    book.save(source)
    parsed = dict(
        source_file=str(source), sha256=hashlib.sha256(source.read_bytes()).hexdigest(),
        rule_version="wal-test-v1",
        series=[dict(series_id="measure", sheet="records", metric="value", unit="count",
                     dimensions={}, header_cells=["B1"])],
        observations=[dict(series_id="measure", date=date, value=value, source_cell=cell,
                           formula=None, value_status="valid", date_cell="A" + cell[1:],
                           raw_value=value)
                      for date, value, cell in [("2024-03-01", 7, "B2"), ("2024-03-02", 11, "B3")]],
        sheets=[dict(name="records")], excluded_regions=[], issues=[],
    )
    store = DataStore(tmp_path / "store")
    identifier = store.publish(parsed)["dataset_id"]
    return store, identifier, source, parsed


def leave_committed_indexed_wal(path):
    # A killed worker leaves committed data and ART-index blocks in the WAL.
    # Normal connection close checkpoints them and cannot reproduce this bug.
    script = textwrap.dedent("""
        import duckdb, os, sys
        con = duckdb.connect(sys.argv[1], config={"checkpoint_threshold": "1TB", "threads": "2"})
        con.execute("DROP INDEX observation_lookup")
        con.execute("CHECKPOINT")
        con.execute("INSERT INTO observations VALUES ('measure', '2024-03-03', 17, 'B4', NULL, 'valid', 'A4', '17', NULL, NULL, NULL)")
        con.execute("CREATE INDEX observation_lookup ON observations(series_id,date)")
        os._exit(0)
    """)
    result = subprocess.run([sys.executable, "-c", script, str(path)], capture_output=True, text=True, timeout=20)
    assert result.returncode == 0, result.stderr
    wal = Path(str(path) + ".wal")
    assert wal.is_file() and wal.stat().st_size > 0
    return wal


def test_indexed_wal_is_recovered_before_strict_read_only_query(dataset):
    store, identifier, _, _ = dataset
    folder = store._path(identifier)
    metadata = {name: (folder / name).read_bytes() for name in
                ["manifest.json", "series.json", "issues.json", "source.xlsx"]}
    wal = leave_committed_indexed_wal(folder / "data.duckdb")
    with pytest.raises(duckdb.IOException, match="Failure while replaying WAL"):
        duckdb.connect(str(folder / "data.duckdb"), read_only=True,
                       config={"enable_external_access": "false", "threads": "2", "memory_limit": "256MB"})

    with store._connect(identifier) as con:
        assert con.execute("SELECT value,source_cell FROM observations ORDER BY date").fetchall() == [
            (7, "B2"), (11, "B3"), (17, "B4")]
        assert con.execute("SELECT index_name FROM duckdb_indexes()").fetchall() == [("observation_lookup",)]
        with pytest.raises(duckdb.InvalidInputException):
            con.execute("DELETE FROM observations")
        with pytest.raises(duckdb.PermissionException):
            con.execute("SELECT * FROM read_csv('/etc/passwd')")
    assert not wal.exists()
    assert metadata == {name: (folder / name).read_bytes() for name in metadata}
    assert store.query_series(identifier, "measure", operation="sum")["aggregate"] == 35


def test_concurrent_readers_recover_one_committed_wal(dataset):
    store, identifier, _, _ = dataset
    wal = leave_committed_indexed_wal(store._path(identifier) / "data.duckdb")
    barrier = threading.Barrier(6)

    def query(_):
        # Separate DataStore instances model independently constructed services.
        with DataStore(store.root)._connect(identifier) as con:
            if os.name != "nt":
                barrier.wait(timeout=10)
            return con.execute("SELECT count(*),sum(value) FROM observations").fetchone()

    with ThreadPoolExecutor(max_workers=6) as pool:
        assert list(pool.map(query, range(6))) == [(3, 35)] * 6
    assert not wal.exists()


def test_separate_processes_recover_and_query_the_same_snapshot(dataset):
    store, identifier, _, _ = dataset
    wal = leave_committed_indexed_wal(store._path(identifier) / "data.duckdb")
    script = textwrap.dedent("""
        import json, sys, time
        from structured_data.store import DataStore
        with DataStore(sys.argv[1])._connect(sys.argv[2]) as con:
            time.sleep(0.05)
            print(json.dumps(con.execute("SELECT count(*),sum(value) FROM observations").fetchone()))
    """)
    package_root = Path(sys.modules[DataStore.__module__].__file__).resolve().parents[1]
    environment = {**os.environ, "PYTHONPATH": str(package_root)}

    def query(_):
        result = subprocess.run([sys.executable, "-c", script, str(store.root), identifier],
                                capture_output=True, text=True, timeout=20, env=environment)
        assert result.returncode == 0, result.stderr
        return json.loads(result.stdout)

    with ThreadPoolExecutor(max_workers=4) as pool:
        assert list(pool.map(query, range(4))) == [[3, 35]] * 4
    assert not wal.exists()


def test_incremental_snapshot_preserves_commits_from_base_wal(dataset):
    store, identifier, source, parsed = dataset
    wal = leave_committed_indexed_wal(store._path(identifier) / "data.duckdb")
    part = dict(
        sha256=parsed["sha256"], series=[dict(series_id="extra", sheet="extra", metric="other",
                                            unit="count", dimensions={}, header_cells=["C1"])],
        observations=[dict(series_id="extra", date="2024-03-01", value=23, source_cell="C2",
                           formula=None, value_status="valid", date_cell="A2", raw_value=23)],
        sheets=[dict(name="extra")], issues=[], excluded_regions=[],
    )
    result = publish_parts(store, source, parsed["sha256"], [part],
                           {"coverage": [{"name": name, "status": "confirmed", "reasons": []}
                                         for name in ["records", "extra"]]},
                           base_id=identifier, replace_sheets=["extra"])
    assert result["manifest"]["observation_count"] == 4
    assert store.query_series(result["dataset_id"], "measure", operation="sum")["aggregate"] == 35
    assert store.query_series(identifier, "measure", operation="sum")["aggregate"] == 35
    assert not wal.exists()
    assert not (store._path(result["dataset_id"]) / "data.duckdb.wal").exists()


def test_failed_recovery_preserves_database_and_committed_wal(dataset, monkeypatch):
    store, identifier, _, _ = dataset
    path = store._path(identifier) / "data.duckdb"
    wal = leave_committed_indexed_wal(path)
    original_db, original_wal = path.read_bytes(), wal.read_bytes()
    real_connect = duckdb.connect

    def denied_recovery(*args, **kwargs):
        if not kwargs.get("read_only", False):
            raise duckdb.IOException("recovery write lock unavailable")
        return real_connect(*args, **kwargs)

    monkeypatch.setattr(duckdb, "connect", denied_recovery)
    with pytest.raises(duckdb.IOException, match="recovery write lock unavailable"):
        with store._connect(identifier):
            pass
    assert path.read_bytes() == original_db
    assert wal.read_bytes() == original_wal


def test_invalid_dataset_cannot_start_recovery(tmp_path):
    store = DataStore(tmp_path / "store")
    with pytest.raises(ValueError, match="ID"):
        with store._connect("../outside"):
            pass
    assert list(store.datasets.iterdir()) == []
