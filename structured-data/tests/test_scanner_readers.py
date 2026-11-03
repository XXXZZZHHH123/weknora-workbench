"""A scan may reuse readers, never source decisions or rows from another sheet."""
from copy import deepcopy
from datetime import datetime

import pytest
from openpyxl import Workbook, load_workbook

from structured_data import scanner, row_sections


def summary_book(tmp_path):
    path = tmp_path / 'summaries.xlsx'
    book = Workbook()
    for index, title in enumerate(['North', 'South']):
        sheet = book.active if index == 0 else book.create_sheet()
        sheet.title = title
        sheet.append([None, 'Report'])
        sheet.append([None, 'date', f'measure {index}'])
        sheet.append([None, datetime(2024, index + 1, 1), index + 10])
        sheet.append([None, datetime(2024, index + 1, 2), index + 20])
        # No cache is manufactured for this formula. It must still be inspected.
        sheet.append([None, '合计', '=SUM(C3:C4)'])
        sheet.append([None, '趋势'])
    book.save(path)
    return path


def track_readers(monkeypatch):
    readers = []

    def tracked(*args, **kwargs):
        book = load_workbook(*args, **kwargs)
        record = {'book': book, 'data_only': kwargs['data_only'], 'closed': 0}
        original = book.close

        def close():
            record['closed'] += 1
            return original()

        book.close = close
        readers.append(record)
        return book

    monkeypatch.setattr(scanner, 'load_workbook', tracked)
    return readers


def test_formula_reader_reused_without_changing_sheet_tail_decisions(tmp_path, monkeypatch):
    path = summary_book(tmp_path)
    readers = track_readers(monkeypatch)
    actual = scanner.scan_workbook(path, use_reviewed_mappings=False)
    assert [r['data_only'] for r in readers] == [True, False]
    assert all(r['closed'] == 1 for r in readers)
    # Independent standalone inspection must give exactly the same regions and
    # mappings, including uncached formulas, for each distinct sheet.
    values = load_workbook(path, read_only=True, data_only=True, keep_links=False)
    try:
        for index, sheet in enumerate(actual['sheets']):
            assert sheet['candidate_revision'] == row_sections.REVISION
            assert sheet['mapping']['blocks'][0]['columns'][0]['metric'] == f'measure {index}'
            original_block = deepcopy(sheet['mapping']['blocks'][0])
            original_block.pop('name')
            original_block['data_end_row'] = None
            expected = row_sections.discover_row_sections(values[sheet['name']], [original_block], path)
            assert expected == {'blocks': sheet['mapping']['blocks'], 'regions': sheet['regions']}
    finally:
        values.close()


def test_no_formula_reader_for_simple_daily_source(tmp_path, monkeypatch):
    path = tmp_path / 'daily.xlsx'
    book = Workbook()
    book.active.append(['date', 'quantity'])
    book.active.append([datetime(2024, 1, 1), 4])
    book.active.append([datetime(2024, 1, 2), 7])
    book.save(path)
    readers = track_readers(monkeypatch)
    result = scanner.scan_workbook(path, use_reviewed_mappings=False)
    assert result['sheets'][0]['status'] == 'auto_ready'
    assert [r['data_only'] for r in readers] == [True]
    assert readers[0]['closed'] == 1


def test_scan_failure_closes_readers_and_does_not_cache_next_scan(tmp_path, monkeypatch):
    path = summary_book(tmp_path)
    readers = track_readers(monkeypatch)
    original = row_sections.discover_row_sections

    def fail_second(ws, *args, **kwargs):
        if ws.title == 'South':
            raise RuntimeError('inspection interrupted')
        return original(ws, *args, **kwargs)

    monkeypatch.setattr(row_sections, 'discover_row_sections', fail_second)
    with pytest.raises(RuntimeError, match='inspection interrupted'):
        scanner.scan_workbook(path, use_reviewed_mappings=False)
    assert [r['data_only'] for r in readers] == [True, False]
    assert all(r['closed'] == 1 for r in readers)
    monkeypatch.setattr(row_sections, 'discover_row_sections', original)
    scanner.scan_workbook(path, use_reviewed_mappings=False)
    assert [r['data_only'] for r in readers] == [True, False, True, False]
    assert all(r['closed'] == 1 for r in readers)


def test_reviewed_region_metadata_reuses_original_values_reader(tmp_path, monkeypatch):
    path = summary_book(tmp_path)
    from structured_data.profiles import reviewed
    from structured_data import regions
    monkeypatch.setattr(reviewed, 'reviewed_mapping', lambda _: {'region_profiles': {'North': {}}})
    inspected = []

    def inspect(_path, worksheet):
        inspected.append(worksheet)
        assert worksheet['C3'].value == 10
        return {'checked': True}

    monkeypatch.setattr(regions, 'reviewed_regions', inspect)
    monkeypatch.setattr(regions, 'apply_profile', lambda sheet, profile: sheet.update(checked=profile['checked']))
    readers = track_readers(monkeypatch)
    result = scanner.scan_workbook(path)
    assert [r['data_only'] for r in readers] == [True, False]
    assert inspected == [readers[0]['book']['North']]
    assert result['sheets'][0]['checked'] is True
    assert all(r['closed'] == 1 for r in readers)
