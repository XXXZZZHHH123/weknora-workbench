"""An internal source-bound scan avoids repeated workbook-wide discovery only."""
from copy import deepcopy
from datetime import datetime
import os
from zipfile import ZipFile

from openpyxl import Workbook
import pytest

from structured_data import generic_parser
from structured_data.scanner import scan_workbook


@pytest.fixture
def scanned(tmp_path):
    path = tmp_path / 'source.xlsx'
    book = Workbook()
    ws = book.active
    ws.title = 'measurements'
    ws.append(['date', 'measurement'])
    ws.append([datetime(2025, 1, 1), 0])
    ws.append([datetime(2025, 1, 2), 42])
    book.create_sheet('other-sheet')['A1'] = 'background'
    book.save(path)
    with ZipFile(path, 'a') as archive:
        archive.comment = b'a' * 16
    scan = scan_workbook(path, use_reviewed_mappings=False)
    return path, scan, scan['sheets'][0]['mapping']


def test_internal_complete_scan_reuses_discovery_but_still_parses_values(scanned, monkeypatch):
    path, scan, mapping = scanned
    monkeypatch.setattr(generic_parser, 'scan_workbook', lambda *_a, **_kw: pytest.fail('must not rescan all sheets'))
    result = generic_parser.parse_mapped_workbook(path, [mapping], scan=scan)
    assert [row['value'] for row in result['observations']] == [0, 42]
    assert result['sha256'] == scan['sha256']


def test_equal_size_equal_mtime_changed_content_still_rejected(scanned, monkeypatch):
    path, scan, mapping = scanned
    before = path.stat()
    with ZipFile(path, 'a') as archive:
        archive.comment = b'b' * 16
    os.utime(path, ns=(before.st_atime_ns, before.st_mtime_ns))
    assert path.stat().st_size == before.st_size
    assert path.stat().st_mtime_ns == before.st_mtime_ns
    monkeypatch.setattr(generic_parser, 'scan_workbook', lambda *_a, **_kw: pytest.fail('hash mismatch must reject directly'))
    with pytest.raises(ValueError, match='源文件已变化'):
        generic_parser.parse_mapped_workbook(path, [mapping], scan=scan)


def test_reusing_scan_does_not_bypass_mapping_fingerprint(scanned):
    path, scan, mapping = scanned
    with pytest.raises(ValueError, match='工作表结构指纹变化'):
        generic_parser.parse_mapped_workbook(path, [{**mapping, 'fingerprint': '0' * 64}], scan=scan)


def test_default_path_still_scans_fresh_source(scanned, monkeypatch):
    path, scan, mapping = scanned
    calls = []

    def fresh(*args, **kwargs):
        calls.append((args, kwargs))
        return scan_workbook(*args, **kwargs)

    monkeypatch.setattr(generic_parser, 'scan_workbook', fresh)
    generic_parser.parse_mapped_workbook(path, [mapping])
    assert len(calls) == 1


@pytest.mark.parametrize('missing', ['sha256', 'fingerprint', 'rows', 'charts'])
def test_incomplete_scan_is_not_accepted_as_cached_evidence(scanned, monkeypatch, missing):
    path, scan, mapping = scanned
    partial = deepcopy(scan)
    if missing == 'sha256':
        partial.pop(missing)
    else:
        partial['sheets'][0].pop(missing)
    calls = []

    def fresh(*args, **kwargs):
        calls.append(kwargs)
        return scan_workbook(*args, **kwargs)

    monkeypatch.setattr(generic_parser, 'scan_workbook', fresh)
    result = generic_parser.parse_mapped_workbook(path, [mapping], scan=partial)
    assert len(calls) == 1 and calls[0] == {'use_reviewed_mappings': False}
    assert len(result['observations']) == 2


def test_fresh_original_mode_never_loads_reviewed_profile(scanned, monkeypatch):
    path, scan, mapping = scanned
    monkeypatch.setattr('structured_data.regions.reviewed_regions',
                        lambda *_a, **_kw: pytest.fail('fresh mode cannot reuse specialized profile'))
    generic_parser.parse_mapped_workbook(path, [mapping], scan=scan)


def test_invalid_or_duplicate_scan_shapes_force_real_discovery(scanned, monkeypatch):
    path, scan, mapping = scanned
    copies = []
    for patch in ({'rows': True}, {'columns': 16_385}, {'fingerprint': 'not-a-fingerprint'}):
        copy = deepcopy(scan)
        copy['sheets'][0].update(patch)
        copies.append(copy)
    copy = deepcopy(scan)
    copy['sheets'].append(deepcopy(copy['sheets'][0]))
    copies.append(copy)
    calls = []

    def fresh(*args, **kwargs):
        calls.append(True)
        return scan_workbook(*args, **kwargs)

    monkeypatch.setattr(generic_parser, 'scan_workbook', fresh)
    for copy in copies:
        generic_parser.parse_mapped_workbook(path, [mapping], scan=copy)
    assert len(calls) == len(copies)


def test_operation_readers_reused_but_released_between_requests(scanned, monkeypatch):
    path, scan, mapping = scanned
    opened = []
    load = generic_parser.load_workbook
    def tracked(*args, **kwargs):
        book = load(*args, **kwargs)
        opened.append(book)
        return book
    monkeypatch.setattr(generic_parser, 'load_workbook', tracked)
    with generic_parser.mapped_workbook_session(path):
        first = generic_parser.parse_mapped_workbook(path, [mapping], scan=scan)
        second = generic_parser.parse_mapped_workbook(path, [mapping], scan=scan)
        assert first == second
        assert len(opened) == 2
        assert all(book._archive.fp is not None for book in opened)
    assert all(book._archive.fp is None for book in opened)
    generic_parser.parse_mapped_workbook(path, [mapping], scan=scan)
    assert len(opened) == 4


def test_active_readers_reject_source_replaced_even_with_new_scan(scanned):
    path, scan, mapping = scanned
    with generic_parser.mapped_workbook_session(path):
        generic_parser.parse_mapped_workbook(path, [mapping], scan=scan)
        book = Workbook()
        ws = book.active
        ws.title = 'measurements'
        ws.append(['date', 'measurement'])
        ws.append([datetime(2025, 1, 1), 999])
        ws.append([datetime(2025, 1, 2), 1000])
        book.save(path)
        current = scan_workbook(path, use_reviewed_mappings=False)
        with pytest.raises(ValueError, match='源文件已变化'):
            generic_parser.parse_mapped_workbook(path, [current['sheets'][0]['mapping']], scan=current)


def test_second_reader_open_failure_closes_first_and_clears_session(scanned, monkeypatch):
    path, scan, mapping = scanned
    load = generic_parser.load_workbook
    opened = []
    def fail_formula(*args, **kwargs):
        if not kwargs['data_only']:
            raise OSError('cannot open formula reader')
        book = load(*args, **kwargs)
        opened.append(book)
        return book
    monkeypatch.setattr(generic_parser, 'load_workbook', fail_formula)
    with pytest.raises(OSError, match='formula reader'):
        generic_parser.parse_mapped_workbook(path, [mapping], scan=scan)
    assert opened[0]._archive.fp is None
    assert generic_parser._workbook_session.get() is None
