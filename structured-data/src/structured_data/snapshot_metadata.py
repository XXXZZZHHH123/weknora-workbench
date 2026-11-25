"""Bounded immutable-snapshot metadata cache; never an authorization cache."""
from collections import OrderedDict
from datetime import datetime
import hashlib
import json
from pathlib import Path
import threading
from xml.etree import ElementTree
from zipfile import BadZipFile, ZIP_DEFLATED, ZIP_STORED, ZipFile
from zlib import error as DeflateError

from .sheet_view import SheetViewLimits, _preflight

_lock = threading.RLock()
_hashes = OrderedDict()
_documents = OrderedDict()
MAX_METADATA_BYTES = 32 * 1024 * 1024
MAX_CACHED_BYTES = 16 * 1024 * 1024


def identity(path):
    path = Path(path)
    if path.is_symlink():
        raise ValueError('Snapshot metadata cannot be a symbolic link')
    resolved = path.resolve(strict=True)
    info = resolved.stat()
    return (str(resolved), info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def hashes(path):
    key = identity(path)
    with _lock:
        cached = _hashes.get(key)
        if cached is not None:
            _hashes.move_to_end(key)
            return cached
    md5, sha = hashlib.md5(), hashlib.sha256()
    with Path(path).open('rb') as source:
        while chunk := source.read(1024 * 1024):
            md5.update(chunk); sha.update(chunk)
    if identity(path) != key:
        raise ValueError('Snapshot changed while verifying its digest')
    value = (md5.hexdigest(), sha.hexdigest())
    with _lock:
        _hashes[key] = value
        while len(_hashes) > 4096:
            _hashes.popitem(last=False)
    return value


class _CorePropertiesTree(ElementTree.TreeBuilder):
    def doctype(self, *_):
        raise ValueError('OOXML core properties cannot declare a DTD')


def workbook_last_saved_at(path, sha256):
    """Read the matching workbook's own OOXML dcterms:modified declaration."""
    try:
        key = identity(path)
        _preflight(path, SheetViewLimits())
        if hashes(path)[1] != sha256:
            return None
        with ZipFile(path) as workbook:
            workbook.getinfo('xl/workbook.xml')
            part = workbook.getinfo('docProps/core.xml')
            if (part.compress_type not in (ZIP_STORED, ZIP_DEFLATED)
                    or max(part.file_size, part.compress_size) > 64 * 1024):
                return None
            with workbook.open(part) as stream:
                root = ElementTree.fromstring(stream.read(64 * 1024 + 1),
                                              ElementTree.XMLParser(target=_CorePropertiesTree()))
        if root.tag != '{http://schemas.openxmlformats.org/package/2006/metadata/core-properties}coreProperties':
            return None
        modified = root.findall('{http://purl.org/dc/terms/}modified')
        if len(modified) != 1 or len(modified[0]) or identity(path) != key:
            return None
        stamp = (modified[0].text or '').strip()
        if 10 <= len(stamp) <= 40 and (len(stamp) == 10 or stamp[10] == 'T'):
            if datetime.fromisoformat(stamp).date().isoformat() == stamp[:10]:
                return stamp
    except (BadZipFile, DeflateError, ElementTree.ParseError, EOFError, LookupError, OSError, RuntimeError, ValueError):
        return None


def document(path):
    key = identity(path)
    if key[3] > MAX_METADATA_BYTES:
        raise ValueError('Snapshot metadata exceeds its explicit size budget')
    with _lock:
        cached = _documents.get(key)
        if cached is not None:
            _documents.move_to_end(key)
            return cached
    value = json.loads(Path(path).read_text())
    if identity(path) != key:
        raise ValueError('Snapshot metadata changed during its read')
    with _lock:
        _documents[key] = value
        while sum(entry[3] for entry in _documents) > MAX_CACHED_BYTES:
            _documents.popitem(last=False)
    return value
