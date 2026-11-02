"""Canonical series colors shared by native HTML, SVG and chart scripts."""
from functools import lru_cache
import json
from pathlib import Path


@lru_cache(maxsize=1)
def _colors():
    return tuple(json.loads((Path(__file__).parent / 'static' / 'chart-palette.json').read_text()))


def chart_palette():
    return list(_colors())
