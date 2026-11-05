"""The actual local executable tool contract, with no business-data cache."""
import hashlib
import json
from pathlib import Path


PROTOCOL_VERSION = 2


def build_fingerprint():
    root = Path(__file__).resolve().parent
    digest = hashlib.sha256()
    for path in sorted(root.rglob('*.py')):
        name = path.relative_to(root).as_posix().encode('utf-8')
        content = path.read_bytes()
        digest.update(str(len(name)).encode() + b':' + name)
        digest.update(str(len(content)).encode() + b':' + content)
    return 'source-sha256:' + digest.hexdigest()


def manifest(tools, build_version):
    rows = sorted([dict(name=t.name, description=t.description,
                        inputSchema=t.inputSchema) for t in tools], key=lambda t: t['name'])
    document = dict(protocol_version=PROTOCOL_VERSION, build_version=build_version, tools=rows)
    encoded = json.dumps(document, sort_keys=True, ensure_ascii=True,
                         separators=(',', ':'), allow_nan=False).encode('utf-8')
    return dict(document, manifest_sha256=hashlib.sha256(encoded).hexdigest(),
                manifest_canonicalization='json-sort-keys-ascii-v1')
