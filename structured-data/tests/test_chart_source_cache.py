import hashlib
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
import threading
import time
from unittest.mock import patch

import pytest

from structured_data import chart_source_cache as cache


@pytest.fixture(autouse=True)
def reset_cache():
    cache._clear_native_chart_cache()
    yield
    cache._clear_native_chart_cache()


def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def sample_charts(label="chart"):
    return [{
        "chart_id": label,
        "sheet": "Chart sheet",
        "title": label,
        "axis_titles": ["Value"],
        "part": "xl/charts/chart1.xml",
        "curves": [{
            "label": "Series",
            "value_ref": "'Source data'!$G$85:$G$98",
            "category_ref": "'Source data'!$A$85:$A$98",
        }],
    }]


def test_cache_isolated_by_canonical_source_path_even_when_hash_matches(tmp_path):
    first = tmp_path / "first.xlsx"
    second = tmp_path / "second.xlsx"
    first.write_bytes(b"same source bytes")
    second.write_bytes(b"same source bytes")
    source_sha = digest(first)

    with patch.object(cache.native_charts, "extract", side_effect=[sample_charts("first"), sample_charts("second")]) as extract:
        first_result = cache.load_native_chart_snapshot(first, source_sha)
        second_result = cache.load_native_chart_snapshot(second, source_sha)

    assert extract.call_count == 2
    assert first_result["charts"][0]["chart_id"] == "first"
    assert second_result["charts"][0]["chart_id"] == "second"


def test_source_sha_change_uses_new_index_and_owner_mutation_is_not_cached(tmp_path):
    source = tmp_path / "source.xlsx"
    source.write_bytes(b"version one")
    sha_one = digest(source)
    with patch.object(cache.native_charts, "extract", side_effect=[sample_charts("v1"), sample_charts("v2"), sample_charts("retry")]) as extract:
        assert cache.load_native_chart_snapshot(source, sha_one)["charts"][0]["chart_id"] == "v1"

        source.write_bytes(b"version two")
        sha_two = digest(source)
        assert sha_one != sha_two
        assert cache.load_native_chart_snapshot(source, sha_two)["charts"][0]["chart_id"] == "v2"

        source.write_bytes(b"version three")
        sha_three = digest(source)
        assert sha_three not in (sha_one, sha_two)

        # The owner must reject a source that changes while extraction is underway.
        def mutate_during_extract(path):
            charts = sample_charts("unverified")
            Path(path).write_bytes(b"changed during extraction")
            return charts

        with patch.object(cache.native_charts, "extract", side_effect=mutate_during_extract):
            with pytest.raises(ValueError, match="发生变化"):
                cache.load_native_chart_snapshot(source, sha_three)
        assert cache._native_chart_cache_info()["entries"] == 2

        source.write_bytes(b"version three")
        with patch.object(cache.native_charts, "extract", return_value=sample_charts("retry")):
            retry = cache.load_native_chart_snapshot(source, sha_three)
        assert retry["charts"][0]["chart_id"] == "retry"
        assert extract.call_count == 2


def test_same_key_concurrent_misses_share_one_loader_without_losing_results(tmp_path):
    source = tmp_path / "concurrent.xlsx"
    source.write_bytes(b"concurrent source")
    source_sha = digest(source)
    workers = 8
    barrier = threading.Barrier(workers)
    original = sample_charts("shared")

    def extract(_path):
        time.sleep(0.05)
        return original

    def load(_):
        barrier.wait()
        return cache.load_native_chart_snapshot(source, source_sha)

    with patch.object(cache.native_charts, "extract", side_effect=extract) as mocked:
        with ThreadPoolExecutor(max_workers=workers) as pool:
            results = list(pool.map(load, range(workers)))

    assert mocked.call_count == 1
    assert all(result["charts"][0]["chart_id"] == "shared" for result in results)


def test_loader_exception_is_not_cached_and_later_call_can_retry(tmp_path):
    source = tmp_path / "retry.xlsx"
    source.write_bytes(b"retry source")
    source_sha = digest(source)
    with patch.object(cache.native_charts, "extract", side_effect=[RuntimeError("temporary parse failure"), sample_charts("retry")]) as extract:
        with pytest.raises(RuntimeError, match="temporary parse failure"):
            cache.load_native_chart_snapshot(source, source_sha)
        assert cache._native_chart_cache_info()["entries"] == 0
        result = cache.load_native_chart_snapshot(source, source_sha)
    assert result["charts"][0]["chart_id"] == "retry"
    assert extract.call_count == 2


def test_lru_entry_and_serialized_byte_budgets_and_deep_copy(tmp_path, monkeypatch):
    monkeypatch.setattr(cache, "MAX_CACHE_ENTRIES", 4)
    monkeypatch.setattr(cache, "MAX_CACHE_BYTES", 4096)
    paths = []
    for i in range(5):
        path = tmp_path / f"source-{i}.xlsx"
        path.write_bytes(f"source {i}".encode())
        paths.append(path)

    calls = []

    def extract(path):
        calls.append(str(path))
        return sample_charts(Path(path).stem)

    with patch.object(cache.native_charts, "extract", side_effect=extract):
        first_sha = digest(paths[0])
        first = cache.load_native_chart_snapshot(paths[0], first_sha)
        first["charts"][0]["title"] = "caller mutation"
        again = cache.load_native_chart_snapshot(paths[0], first_sha)
        assert again["charts"][0]["title"] == "source-0"

        for path in paths[1:]:
            cache.load_native_chart_snapshot(path, digest(path))
        info = cache._native_chart_cache_info()
        assert info["entries"] == 4
        assert info["bytes"] <= cache.MAX_CACHE_BYTES

        cache.load_native_chart_snapshot(paths[0], first_sha)
        assert calls.count(str(paths[0].resolve())) == 2, "least-recently-used source was evicted"

    cache._clear_native_chart_cache()
    monkeypatch.setattr(cache, "MAX_CACHE_BYTES", 100)
    large = tmp_path / "large-index.xlsx"
    large.write_bytes(b"large source")
    with patch.object(cache.native_charts, "extract", return_value=sample_charts("x" * 200)):
        result = cache.load_native_chart_snapshot(large, digest(large))
    assert result["charts"]
    assert cache._native_chart_cache_info()["entries"] == 0
    assert cache._native_chart_cache_info()["bytes"] == 0


def test_index_filters_by_real_referenced_sheet_and_preserves_relationship_order(tmp_path):
    source = tmp_path / "index.xlsx"
    source.write_bytes(b"index source")
    source_sha = digest(source)
    with patch.object(cache.native_charts, "extract", return_value=sample_charts()):
        snapshot = cache.load_native_chart_snapshot(source, source_sha)
    assert snapshot["curves_by_source_sheet"]["Source data"] == [(0, 0)]
    assert snapshot["parsed_references"][0][0]["value"]["column"] == "G"
    assert "Chart sheet" not in snapshot["curves_by_source_sheet"]
