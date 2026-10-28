"""Run the actual on-demand inbox interaction without browser or network."""
from pathlib import Path
import shutil
import subprocess

import pytest


def test_affairs_interactions():
    node = shutil.which('node')
    if node is None:
        pytest.skip('Node is required for the affairs interaction checks')
    root = Path(__file__).resolve().parents[1]
    result = subprocess.run([node, str(root / 'tests' / 'affairs-ui.cjs')],
        cwd=root, capture_output=True, text=True, timeout=20)
    assert result.returncode == 0, result.stdout + result.stderr
    assert 'isolated affairs UI interaction checks passed' in result.stdout
