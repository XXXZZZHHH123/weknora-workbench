"""Private files beside the active result DB, with cross-process export retention."""
from contextlib import contextmanager
import fcntl
import hashlib
import os
from pathlib import Path
import re
from tempfile import NamedTemporaryFile
from uuid import UUID

from tin.ppt.package import PptError


def storage_root(session):
    database = session.get_bind().url.database
    if not database or database == ':memory:':
        raise PptError('研究台私有文件目录不可用', 409)
    root = Path(database).resolve().parent / 'ppt'
    root.mkdir(mode=0o700, exist_ok=True)
    root.chmod(0o700)
    return root


def save_original(root, data):
    digest = hashlib.sha256(data).hexdigest()
    path = root / (digest + '.pptx')
    # Publish only a complete inode. Concurrent identical uploads never read a
    # partially written content-addressed original.
    with NamedTemporaryFile(dir=root, prefix='.upload-', delete=False) as stream:
        temporary = Path(stream.name)
        try:
            stream.write(data)
        except BaseException:
            temporary.unlink(missing_ok=True)
            raise
    try:
        try:
            os.link(temporary, path, follow_symlinks=False)
        except FileExistsError:
            read_original(root, digest)
            path.chmod(0o600)
    finally:
        temporary.unlink(missing_ok=True)
    return digest


def read_original(root, digest):
    if not isinstance(digest, str) or not re.fullmatch('[a-f0-9]{64}', digest):
        raise PptError('模板原件摘要无效', 409)
    path = root / (digest + '.pptx')
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(descriptor, 'rb') as stream:
            from tin.ppt.package import MAX_UPLOAD
            data = stream.read(MAX_UPLOAD + 1)
    except OSError:
        raise PptError('模板原件不存在或不可读', 409) from None
    if hashlib.sha256(data).hexdigest() != digest:
        raise PptError('模板原件摘要已改变', 409)
    return data


def export_path(root, template_id, export_id):
    try:
        if str(UUID(template_id)) != template_id or str(UUID(export_id)) != export_id:
            raise ValueError
    except (ValueError, TypeError, AttributeError):
        raise PptError('导出编号无效', 404) from None
    directory = root / 'exports'
    directory.mkdir(mode=0o700, exist_ok=True)
    directory.chmod(0o700)
    return directory / f'{template_id}-{export_id}.pptx'


@contextmanager
def export_lock(root, template_id):
    # Validate the filename before constructing even the lock path.
    path = export_path(root, template_id, template_id).with_suffix('.lock')
    descriptor = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, 'a') as stream:
        fcntl.flock(stream, fcntl.LOCK_EX)
        yield
