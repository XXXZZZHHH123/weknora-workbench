"""A disposable metadata-only index; current grants and file identities remain authoritative."""
from contextlib import contextmanager
import fcntl
import json
import os
from pathlib import Path
import sqlite3
import tempfile

from fastapi import HTTPException

from .snapshot_metadata import identity
from .research_facts import _hash, read_facts


def _fingerprint(store, manager, sources):
    files = []
    try:
        for path in sorted(manager.directory.glob('*/review.json')):
            files.append(identity(path))
        for folder in sorted(store.datasets.iterdir()):
            if folder.is_dir():
                for name in ('manifest.json','series.json','source.xlsx'):
                    path = folder / name
                    if path.is_file(): files.append(identity(path))
    except (OSError,ValueError):
        raise HTTPException(409,'来源目录无法稳定核验，请刷新后重试') from None
    return _hash({'format':1,'sources':sources,'files':files})


def _readonly(path):
    from urllib.parse import quote
    connection=sqlite3.connect('file:'+quote(str(path.resolve()),safe='/')+'?mode=ro&immutable=1',uri=True)
    connection.execute('PRAGMA cache_size=-2048')
    connection.execute('PRAGMA query_only=ON')
    return connection


def _metadata(connection, fingerprint):
    try:
        value = connection.execute('SELECT value FROM metadata WHERE key=\'manifest\'').fetchone()
        data = json.loads(value[0]) if value else None
        return data if isinstance(data,dict) and data.get('fingerprint')==fingerprint else None
    except (sqlite3.DatabaseError,ValueError,TypeError):
        return None


@contextmanager
def current_index(store, manager, sources):
    root = store.root / '.research-catalog'
    if root.is_symlink(): raise HTTPException(409,'目录索引路径无效')
    root.mkdir(mode=0o700,exist_ok=True)
    path = root / 'catalog.sqlite'
    if path.is_symlink(): raise HTTPException(409,'目录索引路径无效')
    connection = None
    with (root / 'build.lock').open('a') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX)
        with manager.lock:
            fingerprint = _fingerprint(store,manager,sources)
            if path.is_file():
                try:
                    connection = _readonly(path)
                    metadata = _metadata(connection,fingerprint)
                except sqlite3.DatabaseError:
                    if connection is not None: connection.close()
                    connection=None
                if connection is not None and metadata is None:
                    connection.close();connection=None
            if connection is None:
                prepared = read_facts(store,manager,{'sources':sources,'catalog_only':True},_prepare_only=True)
                if len(prepared['indicators']) > 25000:
                    raise HTTPException(413,'单个来源目录超过25000项，请拆分来源范围')
                metadata = {'fingerprint':fingerprint,'version':prepared['version'],'datasets':prepared['datasets']}
                descriptor, temporary = tempfile.mkstemp(prefix='catalog-',suffix='.sqlite',dir=root)
                os.close(descriptor)
                try:
                    with sqlite3.connect(temporary) as writer:
                        writer.execute('CREATE TABLE metadata (key TEXT PRIMARY KEY,value TEXT NOT NULL)')
                        writer.execute('CREATE TABLE fields (series_id TEXT PRIMARY KEY,search_text TEXT NOT NULL,payload TEXT NOT NULL)')
                        size = 0
                        for field in prepared['indicators']:
                            raw = json.dumps(field,ensure_ascii=False,separators=(',',':'))
                            size += len(raw.encode())
                            if size > 32*1024*1024:
                                raise HTTPException(413,'单个来源目录超过32MiB，请拆分来源范围')
                            text = json.dumps({key:field.get(key) for key in ('metric','name','series_id','source_series_id','source_file','unit','frequency','dimensions')},ensure_ascii=False).casefold()
                            writer.execute('INSERT INTO fields VALUES (?,?,?)',(field['series_id'],text,raw))
                        writer.execute('INSERT INTO metadata VALUES (?,?)',('manifest',json.dumps(metadata,ensure_ascii=False)))
                    if _fingerprint(store,manager,sources) != fingerprint:
                        raise HTTPException(409,'来源目录在索引期间变化，请重试')
                    os.replace(temporary,path)
                finally:
                    Path(temporary).unlink(missing_ok=True)
                connection = _readonly(path)
                del prepared
    try:
        yield connection,metadata
        with manager.lock:
            if _fingerprint(store,manager,sources)!=metadata['fingerprint']:
                raise HTTPException(409,'来源确认或快照在读取期间变化，请重新读取')
    finally:
        connection.close()


def page(connection, *, search='', series_ids=None, offset=0, limit=200):
    clauses, values = [], []
    if search:
        clauses.append('instr(search_text,?)>0');values.append(search.casefold().strip())
    if series_ids is not None:
        if not series_ids: return [],0,set()
        clauses.append('series_id IN ('+','.join('?' for _ in series_ids)+')');values.extend(series_ids)
    where = ' WHERE '+' AND '.join(clauses) if clauses else ''
    count = connection.execute('SELECT count(*) FROM fields'+where,values).fetchone()[0]
    rows = [json.loads(row[0]) for row in connection.execute('SELECT payload FROM fields'+where+' ORDER BY series_id LIMIT ? OFFSET ?',[*values,limit,offset])]
    present = set()
    if series_ids:
        present = {row[0] for row in connection.execute('SELECT series_id FROM fields WHERE series_id IN ('+','.join('?' for _ in series_ids)+')',series_ids)}
    return rows,count,present
