"""Bounded OPC package reads. No extraction, external XML or external targets."""
from contextlib import contextmanager
from io import BytesIO
import posixpath
import re
import stat
from urllib.parse import unquote, urlsplit
from zipfile import BadZipFile, ZipFile
import zlib

from lxml import etree as ET

MAX_UPLOAD = 60 * 1024 * 1024
MAX_EXPANDED = 500 * 1024 * 1024
MAX_ENTRIES = 5000
NS = {
    'p': 'http://schemas.openxmlformats.org/presentationml/2006/main',
    'a': 'http://schemas.openxmlformats.org/drawingml/2006/main',
    'r': 'http://schemas.openxmlformats.org/officeDocument/2006/relationships',
    'c': 'http://schemas.openxmlformats.org/drawingml/2006/chart',
    'rel': 'http://schemas.openxmlformats.org/package/2006/relationships',
    'ct': 'http://schemas.openxmlformats.org/package/2006/content-types',
    'svg': 'http://schemas.microsoft.com/office/drawing/2016/SVG/main',
}


class PptError(ValueError):
    def __init__(self, message, status=400):
        super().__init__(message)
        self.status = status


def qn(prefix, name):
    return '{' + NS[prefix] + '}' + name


def xml(data):
    try:
        parser = ET.XMLParser(resolve_entities=False, load_dtd=False, no_network=True,
                              remove_blank_text=False, huge_tree=False)
        root = ET.fromstring(data, parser)
        if root.getroottree().docinfo.doctype:
            raise PptError('PPT XML 不允许 DTD 或实体声明')
        return root
    except ET.XMLSyntaxError:
        raise PptError('PPT 中存在无效或超出解析限制的 XML') from None


def xml_bytes(root):
    return ET.tostring(root, encoding='UTF-8', xml_declaration=True)


def safe_entry(name):
    parts = (name[:-1] if name.endswith('/') else name).split('/')
    return (bool(name) and not name.startswith('/') and '\\' not in name
            and not re.search(r'[\x00-\x1f\x7f:?#%]', name)
            and all(part and part not in {'.', '..'} for part in parts))


@contextmanager
def package(source):
    """Validate the entire directory before reading even the first XML part."""
    if isinstance(source, bytes):
        if len(source) > MAX_UPLOAD:
            raise PptError('PPT 原件不能超过60MB', 413)
        source = BytesIO(source)
    try:
        with ZipFile(source) as archive:
            entries = archive.infolist()
            if len(entries) > MAX_ENTRIES or sum(item.file_size for item in entries) > MAX_EXPANDED:
                raise PptError('PPT 解压大小或条目数超过限制', 413)
            names = set()
            for item in entries:
                if (not safe_entry(item.filename) or item.orig_filename != item.filename or item.filename in names
                        or item.flag_bits & 1 or stat.S_ISLNK(item.external_attr >> 16)):
                    raise PptError('PPT 包含异常、重复或加密的 ZIP 条目')
                names.add(item.filename)
            if not {'ppt/presentation.xml', '[Content_Types].xml'} <= names:
                raise PptError('文件不是包含演示文稿的 PPTX ZIP')
            # Validate all XML, including parts not used by the first-phase parser.
            for item in entries:
                if item.filename.lower().endswith(('.xml', '.rels')):
                    xml(archive.read(item))
            yield archive
    except PptError:
        raise
    except (BadZipFile, RuntimeError, NotImplementedError, EOFError, OSError, UnicodeError, zlib.error):
        raise PptError('无法读取 PPTX ZIP 包') from None


def rels_part(part):
    directory, name = posixpath.split(part)
    return posixpath.join(directory, '_rels', name + '.rels')


def resolve_target(part, target):
    """OPC relationships may use ../; the resolved target must stay in the ZIP."""
    try:
        parsed = urlsplit(target)
    except ValueError:
        raise PptError('PPT 内部关系目标无效') from None
    if parsed.scheme or parsed.netloc or parsed.query or parsed.fragment:
        raise PptError('PPT 内部关系目标无效')
    target = unquote(parsed.path)
    if '\\' in target or re.search(r'[\x00-\x1f\x7f]', target):
        raise PptError('PPT 内部关系目标无效')
    resolved = posixpath.normpath(target.lstrip('/') if target.startswith('/')
                                 else posixpath.join(posixpath.dirname(part), target))
    if not safe_entry(resolved):
        raise PptError('PPT 内部关系超出包范围')
    return resolved


def relationships(archive, part):
    path = rels_part(part)
    if path not in archive.namelist():
        return {}
    result = {}
    for rel in xml(archive.read(path)).findall(qn('rel', 'Relationship')):
        identifier = rel.get('Id')
        if not identifier or identifier in result:
            raise PptError('PPT 关系编号缺失或重复')
        result[identifier] = {
            'type': rel.get('Type', '').rsplit('/', 1)[-1],
            'target': None if rel.get('TargetMode') == 'External'
            else resolve_target(part, rel.get('Target', '')),
        }
    return result
