"""Disposable, versioned read models; authorization is always checked upstream."""
from contextlib import closing, contextmanager
from collections import OrderedDict
import fcntl
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import secrets
import sqlite3
import stat
import sys
import threading
import time
from urllib.parse import quote

from fastapi import HTTPException

from tin.config import settings

FORMAT = 1
MAX_DISK_BYTES = 4 * 1024 ** 3
MAX_METADATA_BYTES = 16 * 1024 ** 2
MAX_CACHED_METADATA_BYTES = 16 * 1024 ** 2
LOCK_SECONDS = 120
_verified = OrderedDict()
_lock = threading.Lock()
_CACHE_NAME = re.compile(r'[0-9a-f]{64}\.facts\.sqlite\Z')
_INDEXES = {'_kb_obs_lookup', '_kb_indicator_lookup'}
_EVIDENCE_COLUMNS = ['id', 'series_id', 'value_cell', 'date_cell', 'raw_value', 'formula', 'source_date']


def readonly_uri(path):
    return 'file:' + quote(str(path.resolve()), safe='/') + '?mode=ro&immutable=1'


def _private_directory(path):
    if path.is_symlink():
        raise OSError('Fact cache must be a private directory')
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    current = path.lstat()
    if not stat.S_ISDIR(current.st_mode) or current.st_uid != os.getuid():
        raise OSError('Fact cache must be owned by the service')
    if current.st_mode & 0o077:
        path.chmod(0o700)


def _directory(scope):
    # Include service identities so switching an upstream cannot reuse its data.
    key = [FORMAT, scope.knowledge_base_tenant_id, scope.knowledge_base_id,
           settings.weknora_api_url, settings.structured_data_url]
    digest = hashlib.sha256(json.dumps(key, separators=(',', ':')).encode()).hexdigest()
    parent = Path(settings.data_dir) / 'cache'
    if parent.is_symlink():
        raise OSError('Fact cache must not traverse a symbolic link')
    root = parent / 'knowledge-facts'
    _private_directory(root)
    directory = root / digest
    _private_directory(directory)
    return directory, key


@contextmanager
def _build_lock(path, *, shared=False, wait=True):
    descriptor = os.open(path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    stream = os.fdopen(descriptor, 'a')
    acquired = False
    waited = False
    try:
        current = os.fstat(stream.fileno())
        if not stat.S_ISREG(current.st_mode) or current.st_uid != os.getuid() or current.st_mode & 0o077:
            raise OSError('Fact cache lock must be a private regular file')
        deadline = time.monotonic() + LOCK_SECONDS
        while True:
            try:
                fcntl.flock(stream, (fcntl.LOCK_SH if shared else fcntl.LOCK_EX) | fcntl.LOCK_NB)
                acquired = True
                break
            except BlockingIOError:
                if not wait:
                    yield None
                    return
                if time.monotonic() >= deadline:
                    raise HTTPException(503, '知识库事实正在准备，请稍后重试') from None
                waited = True
                time.sleep(0.02)
        yield {'waited': waited}
    finally:
        if acquired:
            fcntl.flock(stream, fcntl.LOCK_UN)
        stream.close()


def _file_signature(path):
    current = path.lstat()
    if (not stat.S_ISREG(current.st_mode) or current.st_uid != os.getuid()
            or current.st_mode & 0o077):
        raise ValueError('Invalid private cache file')
    return current.st_dev, current.st_ino, current.st_size, current.st_mtime_ns, current.st_ctime_ns


def _signature_path(path):
    return path.with_name(path.name + '.sig')


def _service_key():
    with Path(settings.service_token_file).open('rb') as stream:
        key = stream.read(65537)
    if not key or len(key) > 65536:
        raise ValueError('Invalid service signing key')
    return key


def _signature_payload(expected, digest):
    return json.dumps({'key': expected, 'digest': digest}, sort_keys=True,
                      separators=(',', ':'), ensure_ascii=False).encode('utf-8')


def _digest(path):
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        while block := stream.read(1024 * 1024):
            digest.update(block)
    return digest.hexdigest()


def _fingerprint(path, key):
    return (_file_signature(path), _file_signature(_signature_path(path)),
            hashlib.sha256(key).digest())


def _write_signature(path, expected):
    key = _service_key()
    digest = _digest(path)
    payload = {'digest': digest, 'mac': hmac.new(key, _signature_payload(expected, digest), hashlib.sha256).hexdigest()}
    descriptor = os.open(_signature_path(path), os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, 'w', encoding='utf-8') as stream:
        json.dump(payload, stream, separators=(',', ':'))
        stream.flush()
        os.fsync(stream.fileno())
    return key


def _verify_signature(path, expected, key):
    with _signature_path(path).open('rb') as stream:
        raw = stream.read(4097)
    if len(raw) > 4096:
        raise ValueError('Invalid fact cache signature')
    def unique_pairs(items):
        document = {}
        for name, value in items:
            if name in document:
                raise ValueError('Duplicate signature field')
            document[name] = value
        return document
    try:
        signature = json.loads(raw, object_pairs_hook=unique_pairs)
    except (ValueError, RecursionError):
        raise ValueError('Invalid fact cache signature') from None
    if (not isinstance(signature, dict) or set(signature) != {'digest', 'mac'}
            or any(not isinstance(value, str) or re.fullmatch('[0-9a-f]{64}', value) is None
                   for value in signature.values())):
        raise ValueError('Invalid fact cache signature')
    expected_mac = hmac.new(key, _signature_payload(expected, signature['digest']), hashlib.sha256).hexdigest()
    if not hmac.compare_digest(signature['mac'], expected_mac):
        raise ValueError('Fact cache signature does not match the current scope and service')
    if not hmac.compare_digest(signature['digest'], _digest(path)):
        raise ValueError('Fact cache content does not match its signature')


def _retained_size(value):
    seen, pending, size = set(), [value], 0
    while pending:
        item = pending.pop()
        if id(item) in seen:
            continue
        seen.add(id(item))
        size += sys.getsizeof(item)
        if isinstance(item, dict):
            pending.extend(item.keys())
            pending.extend(item.values())
        elif isinstance(item, (tuple, list, set, frozenset)):
            pending.extend(item)
    return size


def _remember(path, signature, expected, info):
    retained = _retained_size((expected, info))
    with _lock:
        _verified.pop(str(path), None)
        if retained > MAX_CACHED_METADATA_BYTES:
            return
        _verified[str(path)] = signature, expected, info, retained
        while len(_verified) > 32 or sum(item[3] for item in _verified.values()) > MAX_CACHED_METADATA_BYTES:
            _verified.popitem(last=False)


def _metadata(path, expected, tables, columns):
    signing_key = _service_key()
    signature = _fingerprint(path, signing_key)
    with _lock:
        saved = _verified.get(str(path))
        if saved and saved[0] == signature and saved[1] == expected:
            _verified.move_to_end(str(path))
            return saved[2]
    _verify_signature(path, expected, signing_key)
    with closing(sqlite3.connect(readonly_uri(path), uri=True)) as db:
        objects = db.execute("SELECT type,name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").fetchall()
        names = {name for kind, name in objects if kind == 'table'}
        indexes = {name for kind, name in objects if kind == 'index'}
        if (names != tables | {'_kb_evidence', '_kb_cache_meta'} or indexes != _INDEXES
                or any(kind not in {'table', 'index'} for kind, _ in objects)):
            raise ValueError('Unexpected cache schema')
        for table, expected_columns in {**columns, '_kb_evidence': _EVIDENCE_COLUMNS,
                                        '_kb_cache_meta': ['payload']}.items():
            if [row[1] for row in db.execute('PRAGMA table_info("' + table + '")')] != expected_columns:
                raise ValueError('Unexpected cache columns')
        if db.execute('PRAGMA quick_check').fetchall() != [('ok',)]:
            raise ValueError('Invalid cache database')
        lengths = db.execute('SELECT length(CAST(payload AS BLOB)) FROM _kb_cache_meta LIMIT 2').fetchall()
        if len(lengths) != 1 or type(lengths[0][0]) is not int or not 0 < lengths[0][0] <= MAX_METADATA_BYTES:
            raise ValueError('Invalid cache metadata')
        payload = db.execute('SELECT payload FROM _kb_cache_meta LIMIT 1').fetchone()[0]
        if not isinstance(payload, str):
            raise ValueError('Invalid cache metadata encoding')
        try:
            metadata = json.loads(payload)
        except (ValueError, RecursionError):
            raise ValueError('Invalid cache metadata JSON') from None
        if not isinstance(metadata, dict) or set(metadata) != {'key', 'info'} or metadata['key'] != expected:
            raise ValueError('Cache scope or version mismatch')
        info = metadata['info']
        if (not isinstance(info, dict) or set(info) != {'knowledge_facts', 'knowledge_facts_version',
                'knowledge_facts_catalog', 'knowledge_facts_datasets'}
                or info.get('knowledge_facts') is not True
                or info.get('knowledge_facts_version') != expected[-1]
                or not isinstance(info.get('knowledge_facts_catalog'), dict)
                or not isinstance(info.get('knowledge_facts_datasets'), list)):
            raise ValueError('Invalid cached facts')
    if _fingerprint(path, _service_key()) != signature:
        raise ValueError('Cache changed during validation')
    _remember(path, signature, expected, info)
    return info


def _version_lock(path):
    return path.with_name(path.stem + '.lock')


def _prune(directory, keep):
    # Attach happens while holding a shared version lock. Cleanup never waits
    # for an active reader/builder, and cannot remove a file before ATTACH.
    root = directory.parent
    with _build_lock(root / '.cleanup.lock', wait=False) as cleanup:
        if cleanup is None:
            return
        candidates = []
        for path in root.glob('*/*.facts.sqlite'):
            if path.parent.is_symlink() or not _CACHE_NAME.fullmatch(path.name):
                continue
            try:
                signature = _file_signature(path)
                candidates.append((signature[3], path))
            except (FileNotFoundError, ValueError):
                continue
        files = [path for _, path in sorted(candidates, key=lambda entry: entry[0], reverse=True)]
        size, versions = 0, {}
        for path in [keep, *[item for item in files if item != keep]]:
            try:
                length = _file_signature(path)[2]
                count = versions.get(path.parent, 0)
                remove = path != keep and (count >= 2 or size + length > MAX_DISK_BYTES)
                if remove:
                    with _build_lock(_version_lock(path), wait=False) as lock:
                        if lock is not None:
                            _file_signature(path)
                            path.unlink(missing_ok=True)
                            _signature_path(path).unlink(missing_ok=True)
                            with _lock:
                                _verified.pop(str(path), None)
                            continue
                # Temporarily exceed the budget rather than evict an active
                # reader. A later publication can prune it after release.
                size += length
                versions[path.parent] = count + 1
            except (FileNotFoundError, ValueError):
                continue


def _snapshot(connection, scope, sources, version, tables, columns, build, verify, path, key):
    temporary = path.with_name('.building-' + secrets.token_hex(12))
    try:
        info = build(connection, scope, sources, version)
        driver = connection.connection.driver_connection
        driver.set_authorizer(None)
        for table in tables:
            connection.exec_driver_sql('DROP VIEW temp."' + table[4:] + '"')
        connection.commit()
        if driver.in_transaction:
            raise RuntimeError('Cannot snapshot an open transaction')
        objects = connection.exec_driver_sql('SELECT type,name FROM sqlite_temp_master').fetchall()
        if ({name for kind, name in objects if kind == 'table'} != tables | {'_kb_evidence'}
                or {name for kind, name in objects if kind == 'index'} != _INDEXES
                or any(kind not in {'table', 'index'} for kind, _ in objects)):
            raise ValueError('Unexpected temporary object in shared facts')
        connection.commit()
        descriptor = os.open(temporary, os.O_CREAT | os.O_EXCL | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        os.close(descriptor)
        with closing(sqlite3.connect(temporary)) as target:
            driver.backup(target, name='temp')
            target.execute('CREATE TABLE _kb_cache_meta(payload TEXT NOT NULL)')
            payload = json.dumps({'key': key, 'info': info}, ensure_ascii=False)
            if len(payload.encode('utf-8')) > MAX_METADATA_BYTES:
                raise ValueError('Cache metadata exceeds the private read-model limit')
            target.execute('INSERT INTO _kb_cache_meta VALUES (?)', (payload,))
            target.commit()
        signing_key = _write_signature(temporary, key)
        _metadata(temporary, key, tables, columns)
        verify()
        with temporary.open('rb') as stream:
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        os.replace(_signature_path(temporary), _signature_path(path))
        descriptor = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)
        # Release the large in-memory projection before serving any page.
        for table in tables | {'_kb_evidence'}:
            connection.exec_driver_sql('DROP TABLE temp."' + table + '"')
        connection.commit()
        _remember(path, _fingerprint(path, signing_key), key, info)
        return info
    finally:
        temporary.unlink(missing_ok=True)
        _signature_path(temporary).unlink(missing_ok=True)
        with _lock:
            _verified.pop(str(temporary), None)


@contextmanager
def get_projection(connection, scope, sources, version, tables, build, verify):
    """Keep the selected version pinned until the caller completes its ATTACH."""
    directory, key = _directory(scope)
    # Schema is part of the key: adding a fact column cannot reopen an old file.
    schema, columns = [], {}
    for table in sorted(tables):
        rows = [list(row) for row in connection.exec_driver_sql('PRAGMA main.table_info("' + table[4:] + '")')]
        schema.extend(rows)
        columns[table] = [row[1] for row in rows]
    key += [hashlib.sha256(json.dumps(schema).encode()).hexdigest(), version]
    path = directory / (hashlib.sha256(json.dumps(key).encode()).hexdigest() + '.facts.sqlite')
    lock_path = _version_lock(path)
    with _build_lock(lock_path, shared=True) as lock:
        if lock['waited']:
            # A cache hit may have waited for a builder, too.
            verify()
        try:
            info = _metadata(path, key, tables, columns)
        except (FileNotFoundError, ValueError, sqlite3.DatabaseError):
            pass
        else:
            yield path, info
            return
    published = False
    with _build_lock(lock_path):
        # Another request may have spent seconds constructing this version.
        # Re-check current permissions/sources/version after waiting for it.
        verify()
        try:
            info = _metadata(path, key, tables, columns)
        except (FileNotFoundError, ValueError, sqlite3.DatabaseError):
            info = _snapshot(connection, scope, sources, version, tables, columns, build, verify, path, key)
            published = True
        yield path, info
    if published:
        _prune(directory, path)
