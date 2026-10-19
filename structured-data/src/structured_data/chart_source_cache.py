"""Bounded cache for source-verified native chart relationship metadata."""
from __future__ import annotations

from collections import OrderedDict
import copy
from dataclasses import dataclass, field
import hashlib
import json
import os
from pathlib import Path
import re
import threading

from openpyxl.utils.cell import range_boundaries

from . import native_charts

MAX_CACHE_ENTRIES = 4
MAX_CACHE_BYTES = 16 * 1024 * 1024
MAX_INDEX_BYTES = 16 * 1024 * 1024
MAX_CANDIDATE_PARTS = 32


class SourceEvidenceReadBudgetExceeded(RuntimeError):
    """A typed failure when original-source evidence exceeds bounded work."""

    def __init__(self, message='original-source evidence exceeded its read budget', *,
                 phase='source_read', scope=None):
        self.phase = phase
        self.scope = dict(scope) if isinstance(scope, dict) else {}
        super().__init__(message)


@dataclass
class _Flight:
    ready: threading.Event = field(default_factory=threading.Event)
    snapshot: dict | None = None
    error: BaseException | None = None


_lock = threading.Lock()
_cache: OrderedDict[tuple[str, str, tuple[str, ...] | None], tuple[dict, int]] = OrderedDict()
_inflight: dict[tuple[str, str, tuple[str, ...] | None], _Flight] = {}
_cache_bytes = 0


def _source_digest(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def _build_snapshot(charts: list[dict]) -> dict:
    """Keep exact extracted relations and pre-index curves by referenced sheet."""
    by_source_sheet: dict[str, list[tuple[int, int]]] = {}
    parsed_references: list[list[dict[str, dict]]] = []
    seen_by_sheet: dict[str, set[tuple[int, int]]] = {}

    for chart_index, chart in enumerate(charts):
        chart_curves = chart.get("curves", [])
        parsed_chart_curves = []
        for curve_index, curve in enumerate(chart_curves):
            refs: dict[str, dict] = {}
            for role, field_name in (("value", "value_ref"), ("category", "category_ref")):
                parsed = native_charts.reference(curve.get(field_name))
                if parsed:
                    refs[role] = parsed
            parsed_chart_curves.append(refs)
            for parsed in refs.values():
                source_sheet = parsed["sheet"]
                key = (chart_index, curve_index)
                seen = seen_by_sheet.setdefault(source_sheet, set())
                if key not in seen:
                    by_source_sheet.setdefault(source_sheet, []).append(key)
                    seen.add(key)
        parsed_references.append(parsed_chart_curves)

    return {
        "charts": charts,
        "parsed_references": parsed_references,
        "curves_by_source_sheet": by_source_sheet,
    }


def _serialized_size(snapshot: dict) -> int:
    return len(json.dumps(snapshot, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))


def candidate_chart_parts(path: str | Path, expected_sha256: str, sheet: str,
                          requested_bounds: list[tuple[str, tuple[int, int, int, int]]],
                          dataset_id: str | None = None) -> dict:
    """Use the persisted chart index only to recall candidate package parts.

    No chart facts are returned from this sidecar. Its references are reparsed
    and intersected here, then selected parts are checked against live OOXML
    relationships and reparsed from the SHA-verified source by the caller.
    Legacy indexes lack a source digest, so even an empty result is incomplete.
    """
    source_path = Path(path).resolve(strict=True)
    index_path = source_path.parent / f"{native_charts.VERSION}.json"
    scope = {'complete': False, 'reasons': [], 'candidate_parts': []}
    if not isinstance(expected_sha256, str) or not re.fullmatch(r"[0-9a-f]{64}", expected_sha256):
        raise ValueError("A verified source SHA256 is required for chart metadata")
    if not index_path.is_file():
        scope['reasons'].append('candidate_index_missing')
        return scope
    try:
        with index_path.open('rb') as stream:
            raw_index = stream.read(MAX_INDEX_BYTES + 1)
        if len(raw_index) > MAX_INDEX_BYTES:
            raise SourceEvidenceReadBudgetExceeded(
                'chart candidate index exceeds source evidence budget',
                phase='chart_candidate_index', scope={'kind': 'serialized_index'})
        index = json.loads(raw_index.decode('utf-8'))
    except SourceEvidenceReadBudgetExceeded:
        raise
    except (OSError, UnicodeError, json.JSONDecodeError):
        scope['reasons'].append('candidate_index_unreadable')
        return scope
    expected_dataset_id = dataset_id or source_path.parent.name
    if (not isinstance(index, dict) or index.get('version') != native_charts.VERSION
            or index.get('dataset_id') != expected_dataset_id
            or not isinstance(index.get('charts'), list)):
        scope['reasons'].append('candidate_index_identity_or_schema_mismatch')
        return scope

    charts = index['charts']
    if index.get('chart_count') != len(charts):
        scope['reasons'].append('candidate_index_count_mismatch')
    # Existing sidecars do not bind their serialized references to the source
    # digest. They are recall hints only, never complete evidence inventories.
    if index.get('source_sha256') != expected_sha256:
        scope['reasons'].append('candidate_index_source_sha_unbound')
    candidates = set()
    unresolved = False
    invalid_parts = False
    for chart in charts:
        if not isinstance(chart, dict):
            unresolved = True
            continue
        raw_part = chart.get('part')
        chart_part_valid = native_charts.is_safe_chart_part(raw_part)
        if not chart_part_valid:
            invalid_parts = True
        curves = chart.get('curves')
        if not isinstance(curves, list):
            unresolved = True
            continue
        for curve in curves:
            if not isinstance(curve, dict):
                unresolved = True
                continue
            refs = []
            for field in ('value_ref', 'category_ref'):
                raw_ref = curve.get(field)
                parsed = native_charts.reference(raw_ref)
                if raw_ref is not None and parsed is None:
                    unresolved = True
                if parsed:
                    refs.append(parsed)
            if not refs:
                unresolved = True
            hit = False
            for parsed in refs:
                if parsed['sheet'] != sheet:
                    continue
                ref_bounds = range_boundaries(
                    f"{parsed['column']}{parsed['start_row']}:{parsed['column']}{parsed['end_row']}")
                if any(max(bounds[0], ref_bounds[0]) <= min(bounds[2], ref_bounds[2])
                       and max(bounds[1], ref_bounds[1]) <= min(bounds[3], ref_bounds[3])
                       for _, bounds in requested_bounds):
                    hit = True
                    break
            if hit and chart_part_valid:
                candidates.add(raw_part)
    if unresolved:
        scope['reasons'].append('candidate_index_has_unresolved_references')
    if invalid_parts:
        scope['reasons'].append('candidate_index_has_invalid_parts')
    if len(candidates) > MAX_CANDIDATE_PARTS:
        raise SourceEvidenceReadBudgetExceeded(
            'candidate chart part count exceeds source evidence budget',
            phase='chart_candidate_selection',
            scope={'kind': 'chart_parts', 'candidate_count': len(candidates)})
    scope['candidate_parts'] = sorted(candidates)
    return scope


def load_native_chart_snapshot(path: str | Path, expected_sha256: str,
                               parts: set[str] | tuple[str, ...] | list[str] | None = None) -> dict:
    """Return a deep copy keyed by canonical path and verified source SHA.

    The caller verifies the manifest/source digest before and after inspection.
    A cache miss additionally verifies the source after relationship extraction
    before publishing a cache entry. Same-key cold reads share one loader.
    """
    if not isinstance(expected_sha256, str) or not re.fullmatch(r"[0-9a-f]{64}", expected_sha256):
        raise ValueError("A verified source SHA256 is required for chart metadata")
    source_path = Path(path).resolve(strict=True)
    selection = None if parts is None else tuple(sorted(set(parts)))
    if selection is not None and any(not native_charts.is_safe_chart_part(part) for part in selection):
        raise ValueError('Invalid chart part selection')
    key = (os.path.normcase(str(source_path)), expected_sha256, selection)

    with _lock:
        cached = _cache.get(key)
        if cached is not None:
            _cache.move_to_end(key)
            snapshot = cached[0]
            flight = None
            owner = False
        else:
            snapshot = None
            flight = _inflight.get(key)
            owner = flight is None
            if owner:
                flight = _Flight()
                _inflight[key] = flight

    if snapshot is not None:
        return copy.deepcopy(snapshot)

    assert flight is not None
    if not owner:
        flight.ready.wait()
        if flight.error is not None:
            raise flight.error
        if flight.snapshot is None:
            raise RuntimeError("Native chart metadata load completed without a result")
        return copy.deepcopy(flight.snapshot)

    try:
        if selection is None:
            charts = native_charts.extract(source_path)
            parse_reasons = []
        elif not selection:
            charts = []
            parse_reasons = []
        else:
            try:
                charts, parse_reasons = native_charts.extract_parts(source_path, selection)
            except native_charts.ChartReadBudgetExceeded as exc:
                raise SourceEvidenceReadBudgetExceeded(
                    str(exc), phase='chart_part_read',
                    scope={'kind': 'chart_parts', 'selected_count': len(selection)}) from exc
        if _source_digest(source_path) != expected_sha256:
            raise ValueError("原 Excel 在图表来源索引期间发生变化，拒绝缓存结果")
        snapshot = _build_snapshot(charts)
        snapshot['scope_complete'] = not parse_reasons
        snapshot['scope_reasons'] = parse_reasons
        size = _serialized_size(snapshot)
        flight.snapshot = snapshot

        if size <= MAX_CACHE_BYTES:
            global _cache_bytes
            with _lock:
                while _cache and (len(_cache) >= MAX_CACHE_ENTRIES or _cache_bytes + size > MAX_CACHE_BYTES):
                    _, (_, evicted_size) = _cache.popitem(last=False)
                    _cache_bytes -= evicted_size
                if MAX_CACHE_ENTRIES > 0 and size <= MAX_CACHE_BYTES:
                    _cache[key] = (snapshot, size)
                    _cache_bytes += size
    except BaseException as exc:
        flight.error = exc
        raise
    finally:
        with _lock:
            _inflight.pop(key, None)
            flight.ready.set()

    return copy.deepcopy(snapshot)


def _clear_native_chart_cache() -> None:
    """Reset process cache; intended for isolated tests and controlled maintenance."""
    global _cache_bytes
    with _lock:
        _cache.clear()
        _cache_bytes = 0


def _native_chart_cache_info() -> dict:
    with _lock:
        return {"entries": len(_cache), "bytes": _cache_bytes, "inflight": len(_inflight)}
