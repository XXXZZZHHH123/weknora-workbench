"""Immutable PDF extraction drafts and explicitly reviewed logical tables.

This module never decides which pages belong together. Callers supply a scoped
root and a model/user plan; it validates source identities, coverage and values.
A reviewed plan is not a claim that OCR or the original PDF has been verified.
Row/column indexes in plans and provenance are zero based.
"""
from __future__ import annotations

from copy import deepcopy
from decimal import Decimal, InvalidOperation
import hashlib
import json
import math
import os
from pathlib import Path
import re
import tempfile
import threading

import duckdb

MAX_ROWS = 50000
MAX_COLUMNS = 256
MAX_BUNDLE_BYTES = 32 * 1024 * 1024
MAX_OUTPUT_BYTES = 200000
MAX_CELL_CHARS = 20000
_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,159}\Z")
_SHA = re.compile(r"[0-9a-f]{64}\Z")
_NUMBER = re.compile(r"[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?\Z", re.ASCII)


def _json(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(',', ':'), allow_nan=False)


def _hash(value):
    return hashlib.sha256(_json(value).encode()).hexdigest()


def _identifier(value):
    if not isinstance(value, str) or not _ID.fullmatch(value) or value in ('.', '..'):
        raise ValueError('Invalid source or table identifier')
    return value


def _index(value, size):
    if type(value) is not int or not 0 <= value < size:
        raise ValueError('Source row or column index is out of bounds')
    return value


def _reason(value):
    if not isinstance(value, str) or not value.strip() or len(value) > 2000:
        raise ValueError('Explicit nonempty review reason is required')


def _bbox(value):
    if value is None:
        return
    if (not isinstance(value, list) or len(value) != 4
            or any(type(x) not in (int, float) or not math.isfinite(x) or not 0 <= x <= 1 for x in value)
            or value[0] > value[2] or value[1] > value[3]):
        raise ValueError('bbox must use finite normalized coordinates [x0,y0,x1,y1]')


def _span_regions(fragment):
    """Validate expanded source spans, without copying a merged value to blanks."""
    rows = fragment['rows']
    occupied = {}
    regions = []
    for ri, row in enumerate(rows):
        for ci, cell in enumerate(row):
            rs, cs = cell.get('rowspan', 1), cell.get('colspan', 1)
            owner = occupied.get((ri, ci))
            if owner is not None:
                if rs != 1 or cs != 1:
                    raise ValueError('Overlapping source cell spans')
                if cell['text'] != '':
                    raise ValueError('Covered source span slot must be an explicit blank placeholder')
                continue
            if rs == cs == 1:
                continue
            if ri + rs > len(rows) or any(ci + cs > len(rows[r]) for r in range(ri, ri + rs)):
                raise ValueError('Source cell span is out of grid bounds')
            positions = {(r, c) for r in range(ri, ri + rs) for c in range(ci, ci + cs)}
            if any(p in occupied for p in positions):
                raise ValueError('Overlapping source cell spans')
            for position in positions - {(ri, ci)}:
                occupied[position] = (ri, ci)
            regions.append(dict(anchor=(ri, ci), positions=positions))
    return regions


def _bundle(bundle):
    if not isinstance(bundle, dict) or bundle.get('schema_version') != 'pdf_tables_v1':
        raise ValueError('Expected pdf_tables_v1 extraction bundle')
    source = bundle.get('source', {})
    if not isinstance(source, dict) or not _SHA.fullmatch(str(source.get('sha256', ''))):
        raise ValueError('source.sha256 must be a lowercase SHA256')
    if not isinstance(source.get('file_name'), str) or not source['file_name'] or len(source['file_name']) > 1000:
        raise ValueError('source.file_name is required')
    parser = bundle.get('parser', {})
    if (not isinstance(parser, dict) or not isinstance(parser.get('engine'), str) or not parser['engine']
            or 'version' not in parser or (parser['version'] is not None and
                                           (not isinstance(parser['version'], str) or not parser['version']))):
        raise ValueError('parser.engine and parser.version (null if unavailable) are required')
    fragments = bundle.get('fragments')
    if not isinstance(fragments, list) or not 1 <= len(fragments) <= 1000:
        raise ValueError('Expected 1 to 1000 raw table fragments')
    ids = set()
    count = 0
    for f in fragments:
        if not isinstance(f, dict):
            raise ValueError('Invalid fragment')
        ident = _identifier(f.get('id'))
        if ident in ids:
            raise ValueError('Duplicate fragment ID')
        ids.add(ident)
        if type(f.get('page')) is not int or f['page'] < 1:
            raise ValueError('Fragment page must be a positive integer')
        _bbox(f.get('bbox'))
        rows = f.get('rows')
        if not isinstance(rows, list) or not rows:
            raise ValueError('Fragment rows are required')
        count += len(rows)
        for row in rows:
            if not isinstance(row, list) or not 1 <= len(row) <= MAX_COLUMNS:
                raise ValueError('Raw row width must be 1 to 256 cells')
            for cell in row:
                if not isinstance(cell, dict) or not isinstance(cell.get('text'), str) or len(cell['text']) > MAX_CELL_CHARS:
                    raise ValueError('Raw cell text must be a bounded string; preserve blanks')
                _bbox(cell.get('bbox'))
                for key in ('rowspan', 'colspan'):
                    if type(cell.get(key, 1)) is not int or not 1 <= cell.get(key, 1) <= 10000:
                        raise ValueError('Cell spans must be positive bounded integers')
        _span_regions(f)
    if count > MAX_ROWS:
        raise ValueError('Raw input exceeds 50000 rows; split extraction explicitly')
    contexts = bundle.get('page_context', [])
    if not isinstance(contexts, list) or len(contexts) > 5000:
        raise ValueError('Invalid bounded PDF page context')
    context_bytes = 0
    for context in contexts:
        if not isinstance(context, dict) or not isinstance(context.get('text'), str):
            raise ValueError('PDF page context requires source text')
        page = context.get('page')
        if page is not None and (type(page) is not int or page < 1):
            raise ValueError('PDF page context has an invalid page')
        _bbox(context.get('bbox'))
        parts = context.get('text_parts')
        if not isinstance(parts, list) or any(not isinstance(part, str) for part in parts) or ''.join(parts) != context['text']:
            raise ValueError('PDF page context text disagrees with source parts')
        size = len(context['text'].encode('utf-8'))
        if size > 16 * 1024:
            raise ValueError('PDF page context block exceeds text budget')
        context_bytes += size
    if context_bytes > 1024 * 1024:
        raise ValueError('PDF page context exceeds text budget')
    if 'page_context' in bundle and bundle.get('page_context_coverage') not in ('complete', 'incomplete'):
        raise ValueError('PDF page context coverage must be explicit')
    try:
        size = len(_json(bundle).encode())
    except (ValueError, TypeError) as exc:
        raise ValueError('Bundle must contain finite JSON values') from exc
    if size > MAX_BUNDLE_BYTES:
        raise ValueError('Raw extraction bundle exceeds storage budget')
    return deepcopy(bundle)


def selected_page_context(bundle, pages):
    """Return source-page evidence; this does not bind a unit or note to a table."""
    if 'page_context' not in bundle:
        return {}
    return dict(page_context=[deepcopy(c) for c in bundle['page_context'] if c.get('page') in pages or c.get('page') is None],
                page_context_scope='page_candidates_not_table_binding',
                page_context_coverage=bundle.get('page_context_coverage', 'incomplete'),
                page_context_warnings=deepcopy(bundle.get('page_context_warnings', [])))


def _numeric(text):
    stripped = text.strip()
    if not stripped:
        return None
    if not _NUMBER.fullmatch(stripped):
        raise ValueError('Decimal conversion requires an explicit plain numeric value: ' + repr(text[:80]))
    try:
        value = Decimal(stripped)
    except InvalidOperation:
        raise ValueError('Invalid decimal value') from None
    # DuckDB decimal(38,18) is exact for this explicitly limited range. Reject
    # excess precision rather than rounding, scaling or replacing values by zero.
    if not value.is_finite() or value.adjusted() >= 20 or value.as_tuple().exponent < -18:
        raise ValueError('Decimal value exceeds exact DECIMAL(38,18) range')
    return value


def _span(fragment, row, column, source):
    cell = fragment['rows'][row][column]
    return dict(source_sha256=source['sha256'], fragment_id=fragment['id'], page=fragment['page'],
                row=row, column=column, text=cell['text'], bbox=cell.get('bbox'),
                fragment_bbox=fragment.get('bbox'), rowspan=cell.get('rowspan', 1),
                colspan=cell.get('colspan', 1))


class PDFTableStore:
    """Storage under a caller-authorized tenant/KB directory (no auth inference)."""

    def __init__(self, root):
        self.root = Path(root).resolve()
        self.root.mkdir(parents=True, exist_ok=True)
        for name in ('drafts', 'tables'):
            path = self.root / name
            path.mkdir(exist_ok=True)
            if path.is_symlink() or path.resolve().parent != self.root:
                raise ValueError('PDF storage directory escapes scoped root')

    def _path(self, kind, ident):
        _identifier(ident)
        directory = self.root / kind
        path = directory / (ident + '.json')
        if directory.is_symlink() or directory.resolve().parent != self.root or path.is_symlink() or path.resolve().parent != directory.resolve():
            raise ValueError('PDF storage path escapes scoped root')
        return path

    def _write(self, kind, ident, value):
        path = self._path(kind, ident)
        payload = _json(value)
        if path.exists():
            if path.read_text() != payload:
                raise ValueError('Immutable PDF artifact conflict')
            return
        fd, name = tempfile.mkstemp(prefix='.pdf-', dir=path.parent)
        try:
            with os.fdopen(fd, 'w') as f:
                f.write(payload)
                f.flush()
                os.fsync(f.fileno())
            # Link creates the immutable final path without overwriting a racing
            # writer; concurrent identical publications are idempotent.
            try:
                os.link(name, path)
            except FileExistsError:
                if path.read_text() != payload:
                    raise ValueError('Immutable PDF artifact conflict') from None
        finally:
            os.unlink(name)

    def create_draft(self, bundle):
        bundle = _bundle(bundle)
        digest = _hash(bundle)
        ident = 'pdfd_' + digest
        record = dict(draft_id=ident, bundle_sha256=digest, status='draft',
                      extraction_verified=False, bundle=bundle)
        self._write('drafts', ident, record)
        return self.read_draft(ident)

    def read_draft(self, draft_id):
        path = self._path('drafts', draft_id)
        if not path.exists():
            raise ValueError('PDF draft does not exist in this scope')
        record = json.loads(path.read_text())
        bundle = _bundle(record.get('bundle'))
        digest = _hash(bundle)
        if record.get('draft_id') != draft_id or draft_id != 'pdfd_' + digest or record.get('bundle_sha256') != digest:
            raise ValueError('Stored PDF bundle identity/hash mismatch')
        return dict(draft_id=draft_id, bundle_sha256=digest, status='draft', extraction_verified=False, bundle=bundle)

    def list_drafts(self, source_sha256=None):
        if source_sha256 is not None and (not isinstance(source_sha256, str) or not _SHA.fullmatch(source_sha256)):
            raise ValueError('Invalid source SHA256')
        items = []
        for path in sorted((self.root / 'drafts').glob('pdfd_*.json')):
            record = self.read_draft(path.stem)
            bundle = record.pop('bundle')
            if source_sha256 is None or bundle['source']['sha256'] == source_sha256:
                record.update(source=deepcopy(bundle['source']), parser=deepcopy(bundle['parser']),
                              fragment_count=len(bundle['fragments']),
                              raw_row_count=sum(len(f['rows']) for f in bundle['fragments']))
                items.append(record)
        return dict(drafts=items)

    def validate_plan(self, draft_id, plan):
        draft = self.read_draft(draft_id)
        if not isinstance(plan, dict) or plan.get('bundle_sha256') != draft['bundle_sha256']:
            raise ValueError('Plan must bind the exact raw bundle SHA256')
        allowed = {'bundle_sha256', 'fragment_ids', 'header_rows', 'columns', 'excluded_rows', 'boundary_merges', 'reviewed', 'title', 'review_note'}
        if set(plan) - allowed:
            raise ValueError('Unknown PDF plan fields')
        bundle = draft['bundle']
        all_fragments = {f['id']: f for f in bundle['fragments']}
        ids = plan.get('fragment_ids')
        if not isinstance(ids, list) or not ids or any(not isinstance(i, str) or i not in all_fragments for i in ids) or len(set(ids)) != len(ids):
            raise ValueError('Plan requires distinct real fragment IDs')
        fragments = {i: all_fragments[i] for i in ids}
        headers = plan.get('header_rows')
        if not isinstance(headers, dict) or set(headers) != set(ids):
            raise ValueError('Explicit header_rows (including empty lists) are required for each fragment')
        omitted = set()
        for ident, entries in headers.items():
            if not isinstance(entries, list):
                raise ValueError('header_rows must be lists of {row,reason}')
            for entry in entries:
                if not isinstance(entry, dict) or set(entry) != {'row', 'reason'}:
                    raise ValueError('Every header removal requires explicit row and reason')
                row = _index(entry['row'], len(fragments[ident]['rows']))
                _reason(entry['reason'])
                key = (ident, row)
                if key in omitted:
                    raise ValueError('Duplicate source row classification')
                omitted.add(key)
        exclusions = plan.get('excluded_rows', [])
        if not isinstance(exclusions, list):
            raise ValueError('excluded_rows must be a list')
        for entry in exclusions:
            if not isinstance(entry, dict) or set(entry) != {'fragment_id', 'row', 'reason'} or entry['fragment_id'] not in fragments:
                raise ValueError('Invalid excluded source row')
            _reason(entry['reason'])
            key = (entry['fragment_id'], _index(entry['row'], len(fragments[entry['fragment_id']]['rows'])))
            if key in omitted:
                raise ValueError('Duplicate source row classification')
            omitted.add(key)
        for ident, fragment in fragments.items():
            for region in _span_regions(fragment):
                if any((ident, row) not in omitted for row, _ in region['positions']):
                    raise ValueError('Merged data cell regions require explicit future mapping; only wholly reviewed header/excluded regions are supported')
        columns = plan.get('columns')
        if not isinstance(columns, list) or not 1 <= len(columns) <= MAX_COLUMNS:
            raise ValueError('Plan requires 1 to 256 explicit columns')
        names = set()
        mapped = {i: set() for i in ids}
        for col in columns:
            if not isinstance(col, dict) or set(col) - {'name', 'type', 'source_columns', 'unit'}:
                raise ValueError('Invalid logical column fields')
            name = col.get('name')
            if not isinstance(name, str) or not name.strip() or len(name) > 200 or name.lower() in names or name.lower().startswith('_') or '\x00' in name:
                raise ValueError('Column names must be distinct and cannot use reserved underscore names')
            names.add(name.lower())
            if col.get('type') not in ('text', 'decimal'):
                raise ValueError('Column type must be text or decimal')
            mapping = col.get('source_columns')
            if not isinstance(mapping, dict) or set(mapping) != set(ids):
                raise ValueError('Each logical column must map each fragment explicitly')
            for ident in ids:
                widths = {len(r) for r in fragments[ident]['rows']}
                if len(widths) != 1:
                    raise ValueError('Ragged raw grid requires explicit parser normalization before review')
                index = _index(mapping[ident], next(iter(widths)))
                if index in mapped[ident]:
                    raise ValueError('Duplicate source column mapping')
                mapped[ident].add(index)
        for ident in ids:
            if mapped[ident] != set(range(len(fragments[ident]['rows'][0]))):
                raise ValueError('All source columns must be mapped exactly once; columns cannot silently disappear')
        rows = []
        by_key = {}
        for ident in ids:
            f = fragments[ident]
            for ri, raw_row in enumerate(f['rows']):
                if (ident, ri) in omitted:
                    continue
                values, spans = {}, {}
                for col in columns:
                    ci = col['source_columns'][ident]
                    values[col['name']] = raw_row[ci]['text']
                    spans[col['name']] = [_span(f, ri, ci, bundle['source'])]
                row = dict(values=values, source_cells=spans, source_rows=[dict(fragment_id=ident, row=ri)])
                rows.append(row)
                by_key[(ident, ri)] = row
        merges = plan.get('boundary_merges', [])
        if not isinstance(merges, list):
            raise ValueError('boundary_merges must be a list')
        merged_keys = set()
        for merge in merges:
            if not isinstance(merge, dict) or set(merge) != {'upper', 'lower', 'cells', 'reason'}:
                raise ValueError('Boundary merge requires upper, lower, cells and reason')
            _reason(merge['reason'])
            keys = []
            for side in ('upper', 'lower'):
                ref = merge[side]
                if not isinstance(ref, dict) or set(ref) != {'fragment_id', 'row'}:
                    raise ValueError('Invalid boundary row reference')
                ident = ref['fragment_id']
                if ident not in fragments:
                    raise ValueError('Unknown boundary fragment')
                key = (ident, _index(ref['row'], len(fragments[ident]['rows'])))
                if key not in by_key or key in merged_keys:
                    raise ValueError('Boundary source row is missing, excluded, or reused')
                keys.append(key)
            upper_key, lower_key = keys
            upper, lower = (by_key[k] for k in keys)
            ui, li = (ids.index(k[0]) for k in keys)
            if li != ui + 1 or fragments[lower_key[0]]['page'] <= fragments[upper_key[0]]['page']:
                raise ValueError('Boundary merge must join explicitly ordered cross-page neighboring fragments')
            candidates_upper = [k for k in by_key if k[0] == upper_key[0]]
            candidates_lower = [k for k in by_key if k[0] == lower_key[0]]
            if upper_key != candidates_upper[-1] or lower_key != candidates_lower[0]:
                raise ValueError('Only the last/first data rows at a fragment boundary can be joined')
            ops = merge['cells']
            if not isinstance(ops, list) or len(ops) != len(columns) or any(not isinstance(x, dict) for x in ops) or {x.get('column') for x in ops} != {c['name'] for c in columns}:
                raise ValueError('Every boundary cell requires exactly one explicit operation')
            for col in columns:
                name = col['name']
                op = next(x for x in ops if x['column'] == name)
                if set(op) - {'column', 'operation', 'separator'}:
                    raise ValueError('Unknown boundary cell operation fields')
                a, b = upper['values'][name], lower['values'][name]
                mode = op.get('operation')
                if mode == 'take_upper' and (not b.strip() or a == b):
                    value = a
                elif mode == 'take_lower' and (not a.strip() or a == b):
                    value = b
                elif mode == 'concat' and col['type'] == 'text' and a.strip() and b.strip():
                    # Even text-typed numeric pieces may be a split decimal/sign,
                    # not text continuation. Such ambiguous repairs need re-OCR.
                    if _NUMBER.fullmatch(a.strip()) or _NUMBER.fullmatch(b.strip()):
                        raise ValueError('Ambiguous numeric split cells cannot be concatenated')
                    separator = op.get('separator')
                    if not isinstance(separator, str) or len(separator) > 8:
                        raise ValueError('Text concat requires an explicit bounded separator')
                    value = a + separator + b
                else:
                    raise ValueError('Boundary operation would discard a differing source value or invent a numeric value')
                upper['values'][name] = value
                upper['source_cells'][name] += lower['source_cells'][name]
            upper['source_rows'] += lower['source_rows']
            rows.remove(lower)
            merged_keys.update(keys)
        for row in rows:
            row['_row_id'] = 'pdfr_' + _hash(dict(draft_id=draft_id, source_rows=row['source_rows']))[:32]
            row['raw_values'] = deepcopy(row['values'])
            for col in columns:
                if col['type'] == 'decimal':
                    number = _numeric(row['values'][col['name']])
                    row['values'][col['name']] = None if number is None else str(number)
        coverage = dict(raw_fragment_count=len(bundle['fragments']), selected_fragment_count=len(ids),
                        selected_raw_row_count=sum(len(fragments[i]['rows']) for i in ids),
                        header_row_count=sum(len(x) for x in headers.values()), excluded_row_count=len(exclusions),
                        covered_data_source_row_count=sum(len(r['source_rows']) for r in rows), logical_row_count=len(rows),
                        selected_rows_accounted=True, selected_columns_accounted=True,
                        extraction_ambiguity_count=len(bundle.get('ambiguities', [])),
                        extraction_requires_review=bool(bundle.get('requires_review')),
                        table_input_complete=not exclusions and not bundle.get('ambiguities') and not bundle.get('requires_review'),
                        source_document_complete=False,
                        source_document_completeness='unknown', extraction_verified=False)
        canonical_plan = deepcopy(plan)
        canonical_plan.pop('reviewed', None)
        title = plan.get('title')
        if title is not None and (not isinstance(title, str) or len(title) > 2000):
            raise ValueError('Plan title must be a bounded explicit string')
        return dict(draft_id=draft_id, bundle_sha256=draft['bundle_sha256'], plan=canonical_plan,
                    plan_sha256=_hash(canonical_plan), source=deepcopy(bundle['source']), parser=deepcopy(bundle['parser']),
                    title=title, fragments=[{key: deepcopy(fragments[i].get(key)) for key in
                                           ('id', 'page', 'caption', 'footnotes', 'bbox')} for i in ids],
                    columns=deepcopy(columns), rows=rows, coverage=coverage,
                    status='validated_draft', extraction_verified=False,
                    **selected_page_context(bundle, {fragments[i]['page'] for i in ids}))

    def publish(self, draft_id, plan):
        if not isinstance(plan, dict) or plan.get('reviewed') is not True:
            raise ValueError('Publishing requires explicit reviewed:true')
        record = self.validate_plan(draft_id, plan)
        ident = 'pdft_' + _hash(dict(draft_id=draft_id, plan_sha256=record['plan_sha256']))
        record.update(table_id=ident, status='reviewed', extraction_verified=False)
        record['record_sha256'] = _hash(record)
        self._write('tables', ident, record)
        return self.read_table(ident, limit=20)

    def _read_table(self, table_id):
        path = self._path('tables', table_id)
        if not path.exists():
            raise ValueError('PDF table does not exist in this scope')
        stored = json.loads(path.read_text())
        digest = stored.pop('record_sha256', None)
        if _hash(stored) != digest:
            raise ValueError('Stored PDF table hash mismatch')
        expected_id = 'pdft_' + _hash(dict(draft_id=stored.get('draft_id'), plan_sha256=stored.get('plan_sha256')))
        if stored.get('table_id') != table_id or expected_id != table_id:
            raise ValueError('Stored PDF table identity mismatch')
        # Revalidate against the immutable raw artifact, not only a table checksum.
        check = self.validate_plan(stored['draft_id'], stored['plan'])
        if any(stored.get(k) != v for k, v in check.items() if k != 'status') or stored.get('status') != 'reviewed':
            raise ValueError('Stored table no longer agrees with its raw source and plan')
        stored['record_sha256'] = digest
        return stored

    def list_tables(self, source_sha256=None):
        if source_sha256 is not None and (not isinstance(source_sha256, str) or not _SHA.fullmatch(source_sha256)):
            raise ValueError('Invalid source SHA256')
        items = []
        for path in sorted((self.root / 'tables').glob('pdft_*.json')):
            record = self._read_table(path.stem)
            if source_sha256 is None or record['source']['sha256'] == source_sha256:
                items.append({k: deepcopy(record[k]) for k in ('table_id', 'draft_id', 'source', 'title', 'fragments', 'columns', 'coverage', 'status', 'extraction_verified', 'plan_sha256')})
        return dict(tables=items)

    def read_table(self, table_id, *, offset=0, limit=100):
        if type(offset) is not int or offset < 0 or offset > MAX_ROWS or type(limit) is not int or not 1 <= limit <= 500:
            raise ValueError('Read offset/limit is out of bounds')
        record = self._read_table(table_id)
        count = len(record['rows'])
        record['rows'] = record['rows'][offset:offset + limit]
        record.update(total_row_count=count, offset=offset, output_truncated=offset + limit < count)
        return record

    def query(self, table_id, sql='SELECT * FROM data', *, limit=100, budget_seconds=5):
        if type(limit) is not int or not 1 <= limit <= 500:
            raise ValueError('Query limit must be 1 to 500')
        if type(budget_seconds) not in (int, float) or not math.isfinite(budget_seconds) or not 0.05 <= budget_seconds <= 8:
            raise ValueError('Query time budget must be 0.05 to 8 seconds')
        if not isinstance(sql, str) or not sql.strip() or len(sql) > 16000:
            raise ValueError('Expected a single bounded SELECT/WITH query')
        record = self._read_table(table_id)
        def quote(name):
            return '"' + name.replace('"', '""') + '"'
        with duckdb.connect(config={'enable_external_access': 'false', 'threads': '2', 'memory_limit': '256MB'}) as con:
            try:
                statements = con.extract_statements(sql)
                if len(statements) != 1 or statements[0].type != duckdb.StatementType.SELECT:
                    raise ValueError('Only a single read-only SELECT/WITH query is allowed')
                if con.get_table_names(sql) - {'data'}:
                    raise ValueError('Query may only reference the scoped data table')
                definitions = ['_row_id VARCHAR'] + [quote(c['name']) + (' DECIMAL(38,18)' if c['type'] == 'decimal' else ' VARCHAR') for c in record['columns']]
                con.execute('CREATE TABLE data (' + ','.join(definitions) + ')')
                if record['rows']:
                    values = [[r['_row_id']] + [r['values'][c['name']] for c in record['columns']] for r in record['rows']]
                    con.executemany('INSERT INTO data VALUES (' + ','.join('?' for _ in definitions) + ')', values)
                timer = threading.Timer(budget_seconds, con.interrupt)
                timer.start()
                try:
                    result = con.execute(sql)
                    names = [c[0] for c in result.description]
                    if len(names) > MAX_COLUMNS + 1:
                        raise ValueError('Query result exceeds column budget')
                    values = result.fetchmany(limit + 1)
                finally:
                    timer.cancel()
            except duckdb.Error as exc:
                raise ValueError('Scoped PDF SQL failed: ' + str(exc)[:800]) from None
        def encode(v):
            if isinstance(v, Decimal):
                return str(v)
            if isinstance(v, (list, tuple)):
                return [encode(x) for x in v]
            if isinstance(v, dict):
                return {str(k): encode(x) for k, x in v.items()}
            if isinstance(v, float) and not math.isfinite(v):
                return None
            if hasattr(v, 'isoformat'):
                return v.isoformat()
            if v is None or type(v) in (str, bool, int, float):
                return v
            raise ValueError('Query result type is not JSON-safe; cast the result to VARCHAR')
        output = encode(values[:limit])
        if len(_json(output).encode()) > MAX_OUTPUT_BYTES:
            raise ValueError('Query output exceeds byte budget; aggregate or reduce requested output')
        indexed = {r['_row_id']: r for r in record['rows']}
        positions = [i for i, name in enumerate(names) if name == '_row_id']
        row_provenance = []
        for result_row in output:
            refs = [indexed[result_row[i]] for i in positions if isinstance(result_row[i], str) and result_row[i] in indexed]
            row_provenance.append(dict(scope='returned_row_id_references' if refs else 'all_loaded_inputs_not_sql_lineage',
                                       source_rows=[deepcopy(r) for r in refs], table_id=table_id,
                                       bundle_sha256=record['bundle_sha256'], sql_result_lineage_verified=False))
        if len(_json(row_provenance).encode()) > MAX_OUTPUT_BYTES:
            raise ValueError('Result provenance exceeds byte budget; reduce output rows or columns')
        source = deepcopy(record['source'])
        source['file'] = source['file_name']
        return dict(kind='structured_analysis', status='ok', table_id=table_id, sql=sql, columns=names, rows=output,
                    row_provenance=row_provenance, returned_row_count=len(output), output_truncated=len(values) > limit,
                    output_complete=len(values) <= limit, input_complete=record['coverage']['table_input_complete'],
                    input_count=len(record['rows']), coverage=record['coverage'], source=source,
                    provenance=dict(scope='all_loaded_inputs_before_user_sql', draft_id=record['draft_id'],
                                    bundle_sha256=record['bundle_sha256'], plan_sha256=record['plan_sha256'],
                                    table_record_sha256=record['record_sha256'], input_row_count=len(record['rows']),
                                    row_source_access='read_table(table_id, offset, limit); read_draft(draft_id)',
                                    sql_result_lineage_verified=False), extraction_verified=False,
                    notice='Reviewed plan only; extraction and full-document completeness are unverified. SQL runs over every selected logical row. Output limits do not truncate calculation inputs. Row IDs identify source references only; arbitrary SQL expression/aggregate lineage is not inferred.')
