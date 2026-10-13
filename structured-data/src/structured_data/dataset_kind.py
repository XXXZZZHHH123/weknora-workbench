"""Explicit source classification; names never determine business visibility."""
import json
from pathlib import Path


def dataset_kind(document, root=None):
    explicit = document.get('dataset_kind')
    if explicit in {'business', 'sample'}:
        return explicit
    paths = [Path(__file__).parent / 'reviewed_mappings' / 'source-classifications.json']
    if root is not None:
        paths.append(Path(root) / 'source-classifications.json')
    classifications = {}
    for path in paths:
        if path.is_file():
            classifications.update(json.loads(path.read_text()))
    record = classifications.get(document.get('sha256'), {})
    kind = record.get('dataset_kind') if isinstance(record, dict) else None
    return kind if kind in {'business', 'sample'} else 'business'
