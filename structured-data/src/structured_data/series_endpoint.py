"""Evidence for a bound field's last valid observation as of a source date."""
import json
import os

from .result_contract import excel_value_unit, typed_sha256


MAX_ENDPOINT_ROWS = 16
MAX_ENDPOINT_ROW_BYTES = 12_000
_VALID_VALUE = 'value IS NOT NULL AND isfinite(value)'


def last_valid_enabled():
    return os.environ.get('WEKNORA_STRUCTURED_LAST_VALID', 'true').strip().lower() not in {
        'false', '0', 'no', 'off',
    }


def formula_cache_status(value, raw_value, value_status, is_formula):
    if value_status == 'formula_no_cache':
        return 'missing'
    if not is_formula:
        return 'not_formula'
    if value is not None:
        return 'cached_numeric'
    if value_status == 'excel_error':
        return 'cached_error'
    return 'missing' if raw_value is None else 'cached_non_numeric'


def observation_date_bounds(con, where, params):
    return {sid: tuple(bounds) for sid, *bounds in con.execute(f'''
        SELECT series_id, count(*), min(date), max(date),
               min(date) FILTER (WHERE {_VALID_VALUE}),
               max(date) FILTER (WHERE {_VALID_VALUE})
        FROM observations WHERE {where} GROUP BY series_id
    ''', params).fetchall()}


def read_endpoint_coverage(con, series, *, where, params, start, end, period=None,
                           columns=(), date_bounds=None):
    """Read the same bound observations through end, before paging or user SQL.

    The caller composes the scope from the validated field/curve, upper date,
    period and filters. The requested lower date classifies an earlier endpoint.
    For a batch of fields the reads select date bounds once and then only the
    last recorded and last valid dates. Each date group includes missing rows.
    """
    ordered = {s.get('source_series_id', sid) for sid, s in series.items()
               if s.get('date_mode') not in ('period', 'none')}
    if date_bounds is None:
        date_bounds = observation_date_bounds(con, where, params) if ordered else {}

    dates = sorted({(sid, day) for sid in ordered & date_bounds.keys()
                    for day in (date_bounds[sid][2], date_bounds[sid][4]) if day is not None})
    groups = {}
    if dates:
        # arg_min retains a bounded number of complete source rows per group.
        # min/max and count still examine every row at each terminal date.
        target = ' OR '.join('(series_id = ? AND date = ?)' for _ in dates)
        optional = ['period', 'period_cell', 'metric_cell']
        optional_sql = ', '.join(name if name in columns else f'NULL AS {name}' for name in optional)
        rows = con.execute(f'''
            WITH matched AS (
                SELECT series_id, date,
                       CASE WHEN {_VALID_VALUE} THEN value END AS value,
                       source_cell, date_cell, raw_value, value_status,
                       coalesce(trim(formula) <> '', false) AS is_formula,
                       {optional_sql}
                FROM observations WHERE ({target}) AND ({where})
            )
            SELECT series_id, date, count(*), min(value) <> max(value),
                   arg_min(struct_pack(date := date, value := value, raw_value := raw_value,
                                       source_cell := source_cell, date_cell := date_cell,
                                       value_status := value_status, is_formula := is_formula,
                                       period := period, period_cell := period_cell,
                                       metric_cell := metric_cell),
                           (source_cell, date_cell, value, raw_value, value_status,
                            is_formula, period, period_cell, metric_cell), ?)
            FROM matched GROUP BY series_id, date
        ''', [part for key in dates for part in key] + list(params) + [MAX_ENDPOINT_ROWS]).fetchall()
        groups = {(sid, day): (count, conflict, records) for sid, day, count, conflict, records in rows}

    return {sid: _coverage(s, s.get('source_series_id', sid), date_bounds, groups, start, end, period)
            for sid, s in series.items()}


def _group_rows(series, group):
    count, conflict, records = group or (0, False, [])
    rows = []
    remaining = MAX_ENDPOINT_ROW_BYTES
    for record in records:
        row = dict(record)
        row['date'] = row['date'].isoformat()
        row['formula_cache_status'] = formula_cache_status(
            row['value'], row['raw_value'], row['value_status'], row.pop('is_formula'))
        row['category_cell'] = (series.get('category_provenance') or {}).get(row['source_cell'])
        remaining -= len(json.dumps(row, ensure_ascii=False).encode('utf-8')) + 2
        if remaining < 0:
            break
        rows.append(row)
    return count, bool(conflict), rows


def _coverage(series, storage_id, date_bounds, groups, start, end, period):
    mode = series.get('date_mode')
    ordered = mode not in ('period', 'none')
    if ordered:
        scope = 'through_requested_end' if end else 'snapshot'
    else:
        scope = 'requested_period' if mode == 'period' and period else 'undated'
    bounds = date_bounds.get(storage_id) if ordered else None
    _, first, last, valid_first, valid_last = bounds or (0, None, None, None, None)
    record_group = _group_rows(series, groups.get((storage_id, last)))
    record_count, _, record_rows = record_group
    count, conflict, rows = (record_group if valid_last == last else
                             _group_rows(series, groups.get((storage_id, valid_last))))

    record_status = 'no_record' if ordered else 'not_ordered'
    if last is not None:
        record_status = 'truncated' if record_count > len(record_rows) else 'observed'
    endpoint_status = 'no_valid' if ordered else 'not_ordered'
    direction = None
    if valid_last is not None:
        direction = 'before_range' if start and valid_last < start else 'in_range'
        endpoint_status = 'truncated' if count > len(rows) else 'conflict' if conflict else 'observed'
    return {
        'scope': scope,
        'as_of_date': _date_text(end) if ordered else None,
        'period': period,
        'record_first_date': _date_text(first),
        'record_last_date': _date_text(last),
        'valid_first_date': _date_text(valid_first),
        'valid_last_date': _date_text(valid_last),
        'complete': True,
        'last_record': {
            'status': record_status, 'date': _date_text(last),
            'rows': record_rows, 'row_count': record_count,
        },
        'last_valid': {
            'status': endpoint_status, 'direction': direction, 'date': _date_text(valid_last),
            'rows': rows, 'row_count': count, 'conflict': conflict,
        },
    }


def bind_coverage_sources(store, dataset_id, manifest, manifest_sha256, series, coverages):
    """Bind every endpoint to both the query snapshot and the actual sheet version."""
    versions = {dataset_id: (manifest['sha256'], manifest_sha256)}
    for sid, coverage in coverages.items():
        field = series[sid]
        source_dataset_id = store.sheet_source_dataset(dataset_id, field['sheet'], manifest=manifest)
        if source_dataset_id not in versions:
            retained = store.manifest(source_dataset_id)
            versions[source_dataset_id] = (retained['sha256'], typed_sha256(retained))
        source_sha256, source_manifest_sha256 = versions[source_dataset_id]
        coverage['source'] = {'dataset_id': source_dataset_id, 'sha256': source_sha256,
                              'manifest_sha256': source_manifest_sha256}
        coverage['last_valid'].update(
            series_id=sid, sheet=field['sheet'], dimensions=field.get('dimensions') or {},
            unit=excel_value_unit({sid: field}, source_sha256), source_sha256=source_sha256,
            manifest_sha256=manifest_sha256, source_dataset_id=source_dataset_id,
            source_manifest_sha256=source_manifest_sha256,
        )


def _date_text(value):
    return value.isoformat() if value is not None else None
