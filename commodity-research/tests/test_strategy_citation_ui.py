"""Keep native strategy reference editing checks in the ordinary Python CI."""
from pathlib import Path
import shutil
import subprocess

import pytest


def test_native_strategy_citation_editor():
    node = shutil.which('node')
    if node is None:
        pytest.skip('Node is required for the native strategy editor checks')
    root = Path(__file__).resolve().parents[1]
    result = subprocess.run([node, str(root / 'tests' / 'research-strategy-citations.test.cjs')],
                            cwd=root, capture_output=True, text=True, timeout=20)
    assert result.returncode == 0, result.stdout + result.stderr
