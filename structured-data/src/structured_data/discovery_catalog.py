"""Bounded discovery of published Excel headers, before full source inspection.

This disposable index contains only saved metadata. It does not open workbooks,
build chart views, inspect observations, or choose the source of an answer.
Indexes live in the verified workspace's exact snapshot directory; authorization
and the current/latest snapshot inventory are still checked on every request.
"""
from contextlib import closing
import json
import os
from pathlib import Path
import sqlite3
import tempfile

from .field_index import normalize, tokens

VERSION = 'discovery-headers-v1'
SHEET_PREVIEW_LIMIT = 8
FIELD_LIMIT = 24


def _stamp(folder):
    def signature(name):
        try:
            stat = (folder / name).stat()
            return [stat.st_size, stat.st_mtime_ns, stat.st_ctime_ns]
        except FileNotFoundError:
            return None
    return json.dumps([VERSION, signature('manifest.json'), signature('series.json'),
                       signature('source.xlsx')])


def _read_index(path, stamp):
    if not path.is_file():
        return None
    try:
        con = sqlite3.connect(path.as_uri() + '?mode=ro', uri=True)
        if con.execute('SELECT stamp FROM metadata').fetchone() == (stamp,):
            return con
        con.close()
    except sqlite3.DatabaseError:
        if 'con' in locals():
            con.close()
    return None


def _build(folder, path, stamp):
    # One read per snapshot revision, never one whole-catalogue enrichment per
    # question. No question text or model output enters this persisted index.
    fields = json.loads((folder / 'series.json').read_text())
    fd, temporary = tempfile.mkstemp(prefix='.discovery-', suffix='.sqlite', dir=folder)
    os.close(fd)
    try:
        with closing(sqlite3.connect(temporary)) as con, con:
            con.execute('CREATE TABLE metadata(stamp TEXT NOT NULL)')
            con.execute('INSERT INTO metadata VALUES (?)', (stamp,))
            con.execute('CREATE TABLE fields(doc TEXT NOT NULL, metric TEXT NOT NULL, '
                        'sheet TEXT NOT NULL, search_text TEXT NOT NULL)')
            con.execute('CREATE VIRTUAL TABLE terms USING fts5(metric, context)')
            for field in fields:
                item = {key: field[key] for key in (
                    'series_id', 'sheet', 'metric', 'unit', 'unit_status', 'date_mode',
                    'dimensions', 'header_cells', 'record_count', 'valid_count',
                    'first_date', 'last_date', 'first_valid_date', 'last_valid_date') if key in field}
                metric = str(field['metric'])
                context = json.dumps({key: field.get(key) for key in (
                    'sheet', 'dimensions', 'unit', 'aliases')}, ensure_ascii=False)
                con.execute('INSERT INTO fields VALUES (?,?,?,?)', (
                    json.dumps(item, ensure_ascii=False), normalize(metric),
                    normalize(field['sheet']), normalize(metric + ' ' + context)))
                con.execute('INSERT INTO terms VALUES (?,?)', (
                    ' '.join(tokens(metric)), ' '.join(tokens(context))))
        # Refuse a mixed revision if the published metadata changed during build.
        if _stamp(folder) != stamp:
            raise ValueError('目录版本已变化，请重新读取当前数据集目录。')
        os.replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)


def prepare_index(folder):
    """Publish atomically; reuse across requests, workers, and process restarts."""
    from .store import database_lock
    folder = Path(folder).resolve()
    path = folder / 'discovery.sqlite'
    stamp = _stamp(folder)
    cached = _read_index(path, stamp)
    if cached is not None:
        cached.close()
        return path
    with database_lock(path, exclusive=True):
        stamp = _stamp(folder)
        cached = _read_index(path, stamp)
        if cached is not None:
            cached.close()
        else:
            _build(folder, path, stamp)
    return path


def search_fields(store, dataset_id, question, *, search='', limit=FIELD_LIMIT):
    path = prepare_index(store._path(dataset_id))
    query = normalize(search or question)
    query_tokens = tokens(query)
    with closing(sqlite3.connect(path.as_uri() + '?mode=ro', uri=True)) as con:
        if search:
            # Explicit model-supplied terms are literal recall constraints, not
            # an inferred intent or authorization to select a business source.
            clauses = ['instr(f.search_text, ?) > 0' for _ in search.split()]
            condition = ' AND '.join(clauses) or '0'
            params = [normalize(term) for term in search.split()]
            prefix = ''
            join = ''
            rank = 'f.rowid'
        else:
            prefix = ('WITH hits AS (SELECT rowid,bm25(terms,5,1) AS score FROM terms '
                      'WHERE terms MATCH ?) ')
            match = ' OR '.join('"' + token.replace('"', '""') + '"' for token in query_tokens)
            params = [match or '""', query, query]
            join = 'LEFT JOIN hits ON hits.rowid=f.rowid '
            condition = ('hits.rowid IS NOT NULL OR (length(f.metric)>=3 AND instr(?,f.metric)>0) '
                         'OR (length(f.sheet)>0 AND instr(?,f.sheet)>0)')
            rank = 'coalesce(hits.score,0),f.rowid'
        body = 'FROM fields f ' + join + 'WHERE ' + condition
        total = con.execute(prefix + 'SELECT count(*) ' + body, params).fetchone()[0]
        rows = con.execute(prefix + 'SELECT f.doc ' + body +
            ' ORDER BY (length(f.metric)>=3 AND instr(?,f.metric)>0) DESC,'
            '(length(f.sheet)>0 AND instr(?,f.sheet)>0) DESC,' + rank + ' LIMIT ?',
            [*params, query, query, limit]).fetchall()
    fields = [json.loads(row[0]) for row in rows]
    return fields, total


def _compact_dataset(source, question):
    item = {key: source[key] for key in (
        'dataset_id', 'source_file', 'sha256', 'imported_at', 'is_latest_for_source',
        'series_count', 'import_id', 'use_reviewed_mappings', 'parent_dataset_id') if key in source}
    sheets = [sheet['name'] for sheet in source['sheets']]
    # Literal label matches only rank metadata previews; no source is excluded.
    if question:
        query = normalize(question)
        sheets.sort(key=lambda name: (normalize(name) not in query,
                                      -len(name) if normalize(name) in query else 0))
    item.update(sheet_count=len(sheets), sheets=sheets[:SHEET_PREVIEW_LIMIT],
                sheets_truncated=len(sheets) > SHEET_PREVIEW_LIMIT)
    if source['is_latest_for_source']:
        for key in ('pending_sheets', 'pending_sheet_states', 'empty_sheets', 'unimported_sheet_states'):
            entries = source.get(key, [])
            item[key] = entries[:SHEET_PREVIEW_LIMIT]
            item[key + '_count'] = len(entries)
            item[key + '_truncated'] = len(entries) > SHEET_PREVIEW_LIMIT
        item['coverage_notice'] = source.get('coverage_notice', '仅查询已导入范围。')
    return item


def list_datasets(store, question=None, *, search='', limit=8, offset=0, latest_only=True):
    if type(limit) is not int or not 1 <= limit <= 20:
        raise ValueError('limit 必须为 1..20 的整数。')
    if type(offset) is not int or offset < 0:
        raise ValueError('offset 必须为非负整数。')
    if type(latest_only) is not bool:
        raise ValueError('latest_only 必须为布尔值。')
    sources = store.list_datasets()
    selected = [source for source in sources if not latest_only or source['is_latest_for_source']]
    page = selected[offset:offset + limit]
    result = {'datasets': [_compact_dataset(source, search or question) for source in page],
        'dataset_count': len(selected), 'total_dataset_count': len(sources),
        'offset': offset, 'limit': limit, 'latest_only': latest_only,
        'datasets_truncated': offset + limit < len(selected),
        'next_offset': offset + limit if offset + limit < len(selected) else None,
        'directory_notice': '分页目录仅召回实际文件、表名与已发布表头；不判断业务口径，不读取观测值。'
            '默认每份来源的最新版本，历史版本用latest_only=false。表名预览、字段候选或数据集分页未完整展示不表示没有数据。',
        'next_step': '模型依据完整问题生成search字面检索词；用next_offset继续文件目录，或按候选dataset_id调用'
            'list_series指定sheet/search并分页，核对真实字段、单位、日期及同名来源上下文后，再生成查询计划或SQL。'
            '原生图表标题与曲线未在此展开，按需用list_charts查看。'}
    if question or search:
        candidates, candidate_count = [], 0
        for source in page:
            fields, count = search_fields(store, source['dataset_id'], question or '', search=search)
            candidate_count += count
            candidates.append([{**field, 'dataset_id': source['dataset_id'],
                                'source_file': source['source_file']} for field in fields])
        # Fair interleaving prevents one wide workbook consuming every preview.
        fields = [group[i] for i in range(FIELD_LIMIT) for group in candidates if i < len(group)][:FIELD_LIMIT]
        result.update(field_candidates=fields, field_candidate_count=candidate_count,
            field_candidates_truncated=candidate_count > len(fields),
            field_candidate_scope='current_dataset_page', field_candidate_stage='published_headers',
            field_candidate_next_step='按真实dataset_id调用list_series，传入模型生成的search或已见sheet；用其next_offset继续字段分页。')
        query = normalize(question or search)
        literals = [field for field in fields if len(normalize(field['metric'])) >= 3
                    and normalize(field['metric']) in query]
        result.update(literal_fields=literals, literal_field_count=len(literals),
            literal_fields_truncated=result['datasets_truncated'] or result['field_candidates_truncated'],
            literal_field_notice='原表表头字面召回仅供模型查证，不选择业务口径。此阶段未展开同名列的公式与来源上下文；'
                '必须调用list_series核对，再由模型根据完整问题提交绑定计划。')
    return result
