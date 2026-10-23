from datetime import datetime
import hashlib
import json

from openpyxl import Workbook
import pytest

from structured_data.imports import ImportManager
from structured_data import review_worker


def make_source(root):
    root.mkdir()
    source = root / "source.xlsx"
    workbook = Workbook()
    sheet = workbook.active
    sheet.title = "简单表"
    sheet.append(["日期", "价格"])
    sheet.append([datetime(2023, 1, 1), 100])
    sheet.append([datetime(2023, 2, 1), 120])
    sheet = workbook.create_sheet("复杂表")
    sheet.append(["日期", "现货"])
    sheet.append([None, "品种甲"])
    sheet.append([datetime(2023, 1, 1), 10])
    sheet.append([datetime(2023, 2, 1), 12])
    workbook.save(source)
    return source


def test_spawn_import_preview_confirm_and_failed_confirm_preserves_snapshot(tmp_path):
    root = tmp_path / "data"
    source = make_source(root)
    imported = review_worker.run_review_task(root, "import", path=source)
    manager = ImportManager(root)
    old_dataset = imported["dataset_id"]
    mapping = manager.sheet(imported["import_id"], "复杂表")["mapping"]
    preview = review_worker.run_review_task(root, "preview", import_id=imported["import_id"],
                                          mapping=mapping)
    assert preview["observation_count"] == 2
    assert len(preview["sample"]) == 2
    assert "observations" not in preview
    assert str(root) not in json.dumps(preview, ensure_ascii=False)
    assert manager.get(imported["import_id"])["dataset_id"] == old_dataset

    invalid = {**mapping, "blocks": [{**mapping["blocks"][0], "date_mode": "year"}]}
    with pytest.raises(ValueError):
        review_worker.run_review_task(root, "confirm", import_id=imported["import_id"],
                                     mapping=invalid)
    assert manager.get(imported["import_id"])["dataset_id"] == old_dataset
    assert manager.sheet(imported["import_id"], "复杂表")["status"] == "needs_review"
    assert manager.store.manifest(old_dataset)["observation_count"] == 2

    confirmed = review_worker.run_review_task(root, "confirm", import_id=imported["import_id"],
                                            mapping=mapping, save_template=False)
    assert confirmed["dataset_id"] != old_dataset
    assert manager.store.manifest(confirmed["dataset_id"])["observation_count"] == 4
    assert manager.store.manifest(old_dataset)["observation_count"] == 2
    assert not manager.list_templates()
    assert not list(root.glob(".review-task-*"))
    assert not list((root / "datasets").glob(".import-*"))


def test_publish_guard_runs_at_worker_commit_boundary_and_denial_keeps_old_version(tmp_path):
    root = tmp_path / "data"
    source = make_source(root)
    calls = []
    imported = review_worker.run_review_task(
        root, "import", path=source, publish_guard=lambda: calls.append("import"))
    assert calls == ["import"]
    manager = ImportManager(root)
    old_dataset = imported["dataset_id"]
    mapping = manager.sheet(imported["import_id"], "复杂表")["mapping"]
    before_denial = manager.get(imported["import_id"])

    def revoked():
        calls.append("denied-confirm")
        raise ValueError("编辑权限已撤销，未发布。")

    with pytest.raises(ValueError, match="权限已撤销"):
        review_worker.run_review_task(root, "confirm", import_id=imported["import_id"],
            mapping=mapping, save_template=False, publish_guard=revoked)
    assert calls == ["import", "denied-confirm"]
    assert manager.get(imported["import_id"]) == before_denial
    assert len(manager.store.list_datasets()) == 1

    confirmed = review_worker.run_review_task(root, "confirm", import_id=imported["import_id"],
        mapping=mapping, save_template=False, publish_guard=lambda: calls.append("confirm"))
    assert calls[-1] == "confirm"
    assert confirmed["dataset_id"] != old_dataset


def test_publish_guard_runs_after_snapshot_is_fully_staged(tmp_path):
    root = tmp_path / "data"
    source = make_source(root)
    manager = ImportManager(root)
    observed = []

    def inspect_commit_boundary():
        staged = list(manager.store.datasets.glob(".import-*"))
        publications = [item for item in staged if (item / "manifest.json").is_file()]
        assert len(publications) == 1
        assert manager.store.list_datasets() == []
        observed.append(publications[0].name)

    result = manager.ingest(source, before_publish=inspect_commit_boundary)
    assert observed and manager.store.manifest(result["dataset_id"])


def test_denied_first_import_removes_staged_source_review_and_snapshot(tmp_path):
    root = tmp_path / "data"
    source = make_source(root)

    def revoked():
        raise ValueError("编辑权限已撤销，未发布。")

    with pytest.raises(ValueError, match="权限已撤销"):
        review_worker.run_review_task(root, "import", path=source, publish_guard=revoked)
    manager = ImportManager(root)
    assert manager.list() == []
    assert manager.store.list_datasets(include_samples=True) == []
    assert not list(manager.directory.glob("*/review.json"))
    assert not [path for path in manager.directory.iterdir() if path.is_dir()]


def test_native_binding_is_part_of_guarded_first_import_commit(tmp_path):
    root = tmp_path / "data"
    source = make_source(root)
    native = {"source_knowledge_id": "source-test", "file_name": "source.xlsx",
              "file_size": source.stat().st_size,
              "file_hash": hashlib.md5(source.read_bytes()).hexdigest(),
              "tenant_id": 10000, "knowledge_base_id": "kb-test"}

    with pytest.raises(ValueError, match="权限已撤销"):
        review_worker.run_review_task(root, "import", path=source, native_source=native,
            publish_guard=lambda: (_ for _ in ()).throw(ValueError("编辑权限已撤销，未发布。")))
    manager = ImportManager(root)
    assert manager.list() == []
    assert manager.store.list_datasets(include_samples=True) == []

    result = review_worker.run_review_task(root, "import", path=source,
        native_source=native, publish_guard=lambda: None)
    assert manager.get(result["import_id"])["native_source_bindings"] == [native]


def test_existing_import_adds_native_binding_only_after_final_guard(tmp_path):
    root = tmp_path / "data"
    source = make_source(root)
    manager = ImportManager(root)
    imported = manager.ingest(source)
    review = manager.directory / imported["import_id"] / "review.json"
    before = review.read_bytes()
    native = {"source_knowledge_id": "source-test", "file_name": "source.xlsx",
              "file_size": source.stat().st_size,
              "file_hash": hashlib.md5(source.read_bytes()).hexdigest(),
              "tenant_id": 10000, "knowledge_base_id": "kb-test"}

    with pytest.raises(ValueError, match="权限已撤销"):
        review_worker.run_review_task(root, "import", path=source, native_source=native,
            publish_guard=lambda: (_ for _ in ()).throw(ValueError("编辑权限已撤销，未绑定。")))
    assert review.read_bytes() == before
    assert manager.get(imported["import_id"]).get("native_source_bindings") in (None, [])


def test_native_source_binding_rechecks_permission_before_review_write(tmp_path):
    root = tmp_path / "data"
    source = make_source(root)
    manager = ImportManager(root)
    imported = manager.ingest(source)
    review = manager.directory / imported["import_id"] / "review.json"
    before = review.read_bytes()
    native = {"source_knowledge_id": "source-test", "file_name": "source.xlsx",
              "file_size": source.stat().st_size, "file_hash": "0" * 32,
              "tenant_id": 10000, "knowledge_base_id": "kb-test"}

    def revoked():
        raise ValueError("编辑权限已撤销，未绑定。")

    with pytest.raises(ValueError, match="权限已撤销"):
        manager.bind_native_source(imported["import_id"], native, before_commit=revoked)
    assert review.read_bytes() == before
    assert manager.get(imported["import_id"]).get("native_source_bindings") in (None, [])

    calls = []
    manager.bind_native_source(imported["import_id"], native,
                               before_commit=lambda: calls.append("checked"))
    assert calls == ["checked"]
    assert manager.get(imported["import_id"])["native_source_bindings"] == [native]


def test_internal_worker_cannot_run_unguarded_confirm_all(tmp_path):
    with pytest.raises(ValueError, match="不支持"):
        review_worker.run_review_task(tmp_path, "confirm_all", import_id="a" * 24,
                                      mapping={})


def test_import_and_confirm_prepare_chart_index_before_worker_completes(tmp_path, monkeypatch):
    from structured_data.native_charts import VERSION

    root = tmp_path / "data"
    source = make_source(root)
    imported = review_worker.run_review_task(root, "import", path=source)
    assert imported["chart_catalog_preparation"]["status"] == "ready"
    assert imported["chart_catalog_preparation"]["purpose"] == "derived_catalog_only"

    manager = ImportManager(root)
    assert manager.get(imported["import_id"])["chart_catalog_preparation"]["status"] == "ready"
    first_id = imported["dataset_id"]
    assert (manager.store._path(first_id) / (VERSION + ".json")).is_file()
    mapping = manager.sheet(imported["import_id"], "复杂表")["mapping"]
    confirmed = review_worker.run_review_task(root, "confirm", import_id=imported["import_id"],
                                              mapping=mapping, save_template=False)
    second_id = confirmed["dataset_id"]
    assert second_id != first_id
    assert confirmed["chart_catalog_preparation"]["status"] == "ready"
    assert (manager.store._path(second_id) / (VERSION + ".json")).is_file()

    def unexpected_build(*args, **kwargs):
        raise AssertionError("a completed import must already have its chart index")

    monkeypatch.setattr("structured_data.native_charts.build", unexpected_build)
    assert manager.store.chart_index(second_id)["dataset_id"] == second_id


def test_publish_prepares_only_the_dataset_returned_by_each_publish(tmp_path, monkeypatch):
    from structured_data.store import DataStore

    root = tmp_path / "data"
    source = make_source(root)
    manager = ImportManager(root)
    warmed = []
    original = DataStore.chart_index

    def track(store, dataset_id):
        warmed.append(dataset_id)
        return original(store, dataset_id)

    monkeypatch.setattr(DataStore, "chart_index", track)
    imported = manager.ingest(source)
    assert warmed == [imported["dataset_id"]]

    warmed.clear()
    mapping = manager.sheet(imported["import_id"], "复杂表")["mapping"]
    confirmed = manager.confirm(imported["import_id"], mapping, save_template=False)
    assert confirmed["dataset_id"] != imported["dataset_id"]
    assert warmed == [confirmed["dataset_id"]]


def test_chart_catalog_warm_failure_does_not_rollback_published_versions(tmp_path, monkeypatch, caplog):
    from structured_data.store import DataStore

    root = tmp_path / "data"
    source = make_source(root)
    manager = ImportManager(root)
    imported = manager.ingest(source)
    old_id = imported["dataset_id"]
    old_series = manager.store.list_series(old_id)["series"][0]["series_id"]
    old_rows = manager.store.query_series(old_id, old_series)["rows"]

    def fail_warm(store, dataset_id):
        raise OSError("private path and source details must not escape")

    monkeypatch.setattr(DataStore, "chart_index", fail_warm)
    mapping = manager.sheet(imported["import_id"], "复杂表")["mapping"]
    confirmed = manager.confirm(imported["import_id"], mapping, save_template=False)
    new_id = confirmed["dataset_id"]

    assert new_id != old_id
    assert confirmed["chart_catalog_preparation"]["status"] == "failed"
    assert confirmed["chart_catalog_preparation"]["error_type"] == "OSError"
    assert "private path and source details" not in caplog.text
    assert manager.get(imported["import_id"])["chart_catalog_preparation"]["status"] == "failed"
    assert manager.store.query_series(old_id, old_series)["rows"] == old_rows
    assert manager.store.manifest(new_id)["dataset_id"] == new_id
    new_series = manager.store.list_series(new_id)["series"]
    assert len(new_series) == 2
    assert manager.store.query_series(new_id, new_series[-1]["series_id"])["rows"]


def test_spawn_timeout_is_stopped_and_parent_can_continue(tmp_path):
    root = tmp_path / "data"
    source = make_source(root)
    with pytest.raises(ValueError, match="时间限制"):
        review_worker.run_review_task(root, "import", path=source, timeout=0.001)
    assert not list(root.glob(".review-task-*"))
    result = review_worker.run_review_task(root, "import", path=source)
    assert result["dataset_id"]


def test_spawn_low_memory_budget_stops_child_and_parent_can_continue(tmp_path):
    root = tmp_path / "data"
    source = make_source(root)
    with pytest.raises(ValueError, match="内存限制|工作进程已停止"):
        review_worker.run_review_task(root, "import", path=source, memory_limit_mb=32)
    assert not list(root.glob(".review-task-*"))
    assert not list((root / "datasets").glob(".import-*"))
    result = review_worker.run_review_task(root, "import", path=source)
    assert result["dataset_id"]


def test_preview_samples_are_bounded_and_provenance_is_not_copied():
    series = [{"series_id": f"s{i}", "category_provenance": {f"B{n}": f"A{n}" for n in range(100)},
               "metric": "x" * 1000} for i in range(100)]
    parsed = {"mapping": {"blocks": []}, "series_count": 100, "observation_count": 1000,
              "series": series, "observations": [{}] * 1000, "sample": [{"value": i} for i in range(100)],
              "blocks": [{"series": series, "sample": [{}] * 100, "issues": [{}] * 100}],
              "issues": [{}] * 100}
    preview = review_worker._bounded_preview(parsed)
    assert "observations" not in preview
    assert "category_provenance" not in preview["series"][0]
    assert len(preview["series"]) == len(preview["sample"]) == 20
    assert len(preview["blocks"][0]["series"]) == 20
    assert preview["series_truncated"] and preview["sample_truncated"]
    assert len(preview["series"][0]["metric"]) == 500


class FakeProcess:
    pid = 123

    def __init__(self):
        self.alive = True
        self.terminated = False

    def is_alive(self):
        return self.alive

    def terminate(self):
        self.terminated = True
        self.alive = False

    def join(self, timeout=None):
        pass


def test_rss_watchdog_stops_worker_at_budget():
    process = FakeProcess()
    with pytest.raises(ValueError, match="内存限制"):
        review_worker._monitor_worker(process, float("inf"), 512, rss_reader=lambda pid: 513)
    assert process.terminated and not process.is_alive()


def test_closed_publish_control_channel_does_not_mask_child_completion():
    class CompletingProcess(FakeProcess):
        def join(self, timeout=None):
            self.alive = False

    class ClosedControl:
        def poll(self):
            return True

        def recv(self):
            raise EOFError

        def send(self, value):
            raise AssertionError("a closed child channel must not receive a reply")

    review_worker._monitor_worker(
        CompletingProcess(), float("inf"), 512,
        rss_reader=lambda pid: 0, control=ClosedControl(), publish_guard=lambda: None)


def test_resource_limits_apply_without_increasing_existing_limits():
    class FakeResource:
        RLIMIT_AS, RLIMIT_CPU, RLIMIT_CORE, RLIM_INFINITY = 1, 2, 3, -1

        def __init__(self):
            self.applied = {}

        def getrlimit(self, kind):
            return (100, 200) if kind == self.RLIMIT_AS else (-1, -1)

        def setrlimit(self, kind, limits):
            self.applied[kind] = limits

    resource = FakeResource()
    assert review_worker._apply_resource_limits(1024, 2.2, resource)
    assert resource.applied == {1: (100, 100), 2: (3, 3), 3: (0, 0)}


def test_cleanup_only_removes_new_unpublished_task_directories(tmp_path):
    directory = tmp_path / "datasets"
    directory.mkdir()
    old = directory / ".import-before"
    old.mkdir()
    before = review_worker._temporary_snapshots(tmp_path)
    unfinished = directory / ".import-current"
    unfinished.mkdir()
    (unfinished / "manifest.json").write_text("{}")
    published = directory / ("a" * 24)
    published.mkdir()
    review_worker._cleanup_temporary_snapshots(tmp_path, before)
    assert old.is_dir() and published.is_dir()
    assert not unfinished.exists()


def test_cleanup_does_not_follow_dataset_directory_symlink(tmp_path):
    external = tmp_path / "external"
    external.mkdir()
    unfinished = external / ".import-external"
    unfinished.mkdir()
    root = tmp_path / "data"
    root.mkdir()
    try:
        (root / "datasets").symlink_to(external, target_is_directory=True)
    except OSError:
        pytest.skip("Directory symlinks are not available on this host")
    review_worker._cleanup_temporary_snapshots(root, set())
    assert unfinished.is_dir()


def test_task_error_redacts_paths_and_invalid_configuration(tmp_path, monkeypatch):
    error = ValueError(f"无法读取 {tmp_path}/source.xlsx")
    assert str(tmp_path) not in review_worker._safe_business_error(
        error, {"root": str(tmp_path), "path": str(tmp_path / "source.xlsx")})
    monkeypatch.setenv("STRUCTURED_REVIEW_MEMORY_MB", "invalid")
    with pytest.raises(ValueError, match="配置无效"):
        review_worker.run_review_task(tmp_path, "import", path=tmp_path / "source.xlsx")


def test_oversized_request_is_rejected_before_start(tmp_path):
    with pytest.raises(ValueError, match="配置过大"):
        review_worker.run_review_task(tmp_path, "preview", import_id="a" * 24,
                                     mapping={"text": "x" * review_worker.MAX_REQUEST_BYTES})


def test_spawn_preserves_structured_error_location_and_existing_data(tmp_path):
    from structured_data.review_errors import ReviewValidationError
    root = tmp_path / 'data'
    source = make_source(root)
    imported = review_worker.run_review_task(root, 'import', path=source)
    manager = ImportManager(root)
    mapping = manager.sheet(imported['import_id'], '复杂表')['mapping']
    mapping['blocks'][0]['header_rows'] = []
    with pytest.raises(ReviewValidationError) as caught:
        review_worker.run_review_task(root, 'preview', import_id=imported['import_id'], mapping=mapping)
    assert caught.value.review_target == {'sheet': '复杂表', 'block_index': 0, 'field': 'header_rows', 'cells': []}
    assert manager.get(imported['import_id'])['dataset_id'] == imported['dataset_id']
