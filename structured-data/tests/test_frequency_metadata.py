"""Frequency is explicit source metadata, never inferred from observation gaps."""
from copy import deepcopy
from datetime import datetime

from openpyxl import Workbook
import pytest

from structured_data.imports import ImportManager


@pytest.fixture
def source(tmp_path):
    path = tmp_path / "observations.xlsx"
    book = Workbook(); sheet = book.active; sheet.title = "Records"
    sheet.append(["name", "Quantity"]); sheet.append(["code", "external-field"])
    sheet.append(["unit", "吨"]); sheet.append(["frequency", "周"])
    sheet.append([datetime(2024, 1, 1), 4]); sheet.append([datetime(2024, 1, 2), 5])
    book.save(path)
    manager = ImportManager(tmp_path / "data")
    result = manager.ingest(path, use_reviewed_mappings=False)
    mapping = deepcopy(manager.sheet(result["import_id"], "Records")["mapping"])
    block = mapping["blocks"][0]; block["header_rows"] = [1]
    column = block["columns"][0]
    column.update(metric="Quantity", unit="吨", header_cells=["B1"], frequency="周", frequency_cells=["B4"])
    return manager, result, mapping


def test_confirm_preserves_explicit_frequency_and_source_cell(source):
    manager, first, mapping = source
    preview = manager.preview(first["import_id"], mapping)
    assert preview["series"][0]["frequency"] == "周"
    assert preview["series"][0]["frequency_cells"] == ["B4"]
    saved = manager.confirm(first["import_id"], mapping)
    spec = manager.store.list_series(saved["dataset_id"])["series"][0]
    assert spec["frequency"] == "周" and spec["frequency_cells"] == ["B4"]


@pytest.mark.parametrize("changes", [{"frequency": "月"}, {"frequency_cells": ["B5"]},
                                   {"frequency_cells": []}, {"frequency_cells": ["ZZ4"]}])
def test_mismatched_or_invented_frequency_metadata_cannot_publish(source, changes):
    manager, first, mapping = source
    mapping["blocks"][0]["columns"][0].update(changes)
    with pytest.raises(ValueError):
        manager.confirm(first["import_id"], mapping)
    assert manager.get(first["import_id"])["dataset_id"] == first["dataset_id"]


def test_frequency_is_not_guessed_from_daily_dates(source):
    manager, first, mapping = source
    column = mapping["blocks"][0]["columns"][0]
    column.pop("frequency"); column.pop("frequency_cells")
    spec = manager.preview(first["import_id"], mapping)["series"][0]
    assert "frequency" not in spec and "frequency_cells" not in spec
