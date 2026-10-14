"""Publish immutable snapshots by copying unchanged tables and streaming changes."""
import csv
from contextlib import closing
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import shutil
import tempfile

import duckdb

from .streaming import read_json_array, write_json_array


FIELDS = ['series_id', 'date', 'value', 'source_cell', 'formula', 'value_status',
          'date_cell', 'raw_value', 'period', 'period_cell', 'metric_cell']
SCHEMA = ('series_id VARCHAR NOT NULL, date DATE, value DOUBLE, source_cell VARCHAR NOT NULL, '
          'formula VARCHAR, value_status VARCHAR NOT NULL, date_cell VARCHAR, raw_value VARCHAR, '
          'period VARCHAR, period_cell VARCHAR, metric_cell VARCHAR')


def publish_parts(store, source, sha256, parts, metadata, *, base_id=None, replace_sheets=()):
    """Parts are consumed individually; the last manifest is the publication boundary."""
    from .store import dump

    replaced = set(replace_sheets)
    base = store.manifest(base_id) if base_id else None
    if base and base['sha256'] != sha256:
        raise ValueError('源文件已改变，不能复用旧工作表。')
    with tempfile.TemporaryDirectory(prefix='.import-', dir=store.datasets) as temporary:
        work = Path(temporary)
        series, sheets, excluded = [], [], []
        if base:
            base_path = store._path(base_id)
            # A legacy snapshot may still have committed indexed data in its
            # WAL. Copy only after recovery, while the source read lock is held.
            with store._connect(base_id):
                shutil.copyfile(base_path / 'data.duckdb', work / 'data.duckdb')
            prior_series = json.loads((base_path / 'series.json').read_text())
            series = [s for s in prior_series if s['sheet'] not in replaced]
            sheets = [s for s in base['sheets'] if s['name'] not in replaced]
            excluded = [r for r in base.get('excluded_regions', [])
                        if r.get('sheet') not in replaced and r.get('range') != 'entire_sheet']
        issues_path = work / 'issues.jsonl'
        issue_count = 0
        with issues_path.open('w', encoding='utf-8') as issues:
            def add_issue(item):
                nonlocal issue_count
                issues.write(json.dumps(item, ensure_ascii=False, default=str) + '\n')
                issue_count += 1

            if base:
                for item in read_json_array(base_path / 'issues.json'):
                    if item.get('sheet') not in replaced:
                        add_issue(item)
            with closing(duckdb.connect(str(work / 'data.duckdb'))) as con:
                con.execute('CREATE TABLE IF NOT EXISTS observations(' + SCHEMA + ')')
                if base:
                    removed = [s['series_id'] for s in prior_series if s['sheet'] in replaced]
                    if removed:
                        con.execute('DELETE FROM observations WHERE series_id IN (SELECT unnest(?))', [removed])
                used = {s['series_id'] for s in series}
                for parsed in parts:
                    if parsed['sha256'] != sha256:
                        raise ValueError('读取期间源文件发生变化，未发布数据。')
                    new_ids = [s['series_id'] for s in parsed['series']]
                    if used.intersection(new_ids) or len(set(new_ids)) != len(new_ids):
                        raise ValueError('工作表指标重复，快照未发布。')
                    used.update(new_ids)
                    csv_path = work / 'observations.csv'
                    with csv_path.open('w', newline='', encoding='utf-8') as stream:
                        writer = csv.DictWriter(stream, fieldnames=FIELDS, extrasaction='ignore')
                        writer.writeheader()
                        writer.writerows(parsed['observations'])
                    columns = {name: ('DOUBLE' if name == 'value' else 'DATE' if name == 'date' else 'VARCHAR')
                               for name in FIELDS}
                    con.execute('INSERT INTO observations SELECT * FROM read_csv(?, header=true, '
                                'delim=\',\', quote=\'"\', escape=\'"\', nullstr=\'\', columns=?)',
                                [str(csv_path), columns])
                    csv_path.unlink()
                    duplicates = con.execute('SELECT series_id, coalesce(cast(date AS VARCHAR), period, source_cell), '
                                             'count(*) FROM observations WHERE series_id IN (SELECT unnest(?)) '
                                             'GROUP BY 1,2 HAVING count(*)>1 LIMIT 5', [new_ids]).fetchall()
                    if duplicates:
                        raise ValueError(f'发现重复日期和指标，快照未发布：{duplicates}')
                    series.extend(parsed['series'])
                    sheets.extend(parsed['sheets'])
                    excluded.extend(parsed.get('excluded_regions', []))
                    for item in parsed.get('issues', []):
                        add_issue(item)
                if not series:
                    return None
                con.execute('CREATE INDEX IF NOT EXISTS observation_lookup ON observations(series_id,date)')
                stats = con.execute('SELECT series_id,count(*),count(value),min(date),max(date),'
                                    'min(date) FILTER(WHERE value IS NOT NULL),max(date) FILTER(WHERE value IS NOT NULL) '
                                    'FROM observations GROUP BY series_id').fetchall()
                periods = dict(con.execute('SELECT series_id,list(DISTINCT period ORDER BY period) '
                                           'FROM observations WHERE period IS NOT NULL GROUP BY series_id').fetchall())
                con.execute('CHECKPOINT')
        summaries = {r[0]: dict(record_count=r[1], valid_count=r[2], first_date=r[3], last_date=r[4],
                               first_valid_date=r[5], last_valid_date=r[6]) for r in stats}
        series = [{**s, **summaries.get(s['series_id'], {}),
                   **({'periods': periods[s['series_id']]} if s['series_id'] in periods else {})} for s in series]
        # A signature describes actual approved mappings, not timestamps or UI state.
        signature = {'sheets': sorted(sheets, key=lambda s: s['name']), 'coverage': metadata['coverage'],
                     'parser_revision': 'incremental-stream-v1'}
        rule = 'workbook-review-v2:' + hashlib.sha256(json.dumps(signature, ensure_ascii=False,
                                                              sort_keys=True, default=str).encode()).hexdigest()[:16]
        dataset_id = hashlib.sha256((sha256 + ':' + rule).encode()).hexdigest()[:24]
        final = store.datasets / dataset_id
        if (final / 'manifest.json').exists():
            return {'dataset_id': dataset_id, 'reused': True, 'manifest': store.manifest(dataset_id)}
        dump(work / 'series.json', series)
        with issues_path.open(encoding='utf-8') as stream:
            write_json_array(work / 'issues.json', (json.loads(line) for line in stream))
        issues_path.unlink()
        shutil.copyfile(source, work / 'source.xlsx')
        with (work / 'source.xlsx').open('rb') as original:
            if hashlib.file_digest(original, 'sha256').hexdigest() != sha256:
                raise ValueError('读取期间原文件发生变化，未发布数据。')
        manifest = {**metadata, 'source_file': Path(source).name, 'sha256': sha256,
                    'rule_version': rule, 'sheets': sheets, 'excluded_regions': excluded,
                    'dataset_id': dataset_id, 'imported_at': dt.datetime.now(dt.timezone.utc).isoformat(),
                    'series_count': len(series), 'observation_count': sum(r[1] for r in stats),
                    'valid_count': sum(r[2] for r in stats), 'issue_count': issue_count,
                    'parent_dataset_id': base_id, 'changed_sheets': sorted(replaced) if base else [s['name'] for s in sheets]}
        manifest['excluded_regions'] += [{'sheet': s['name'], 'range': 'entire_sheet', 'reason': '; '.join(s['reasons'])}
                                         for s in metadata['coverage'] if s['status'] not in
                                         {'auto_ready', 'template_ready', 'confirmed', 'trusted_existing'}]
        dump(work / 'manifest.json', manifest)
        os.rename(work, final)
        return {'dataset_id': dataset_id, 'reused': False, 'manifest': manifest}
