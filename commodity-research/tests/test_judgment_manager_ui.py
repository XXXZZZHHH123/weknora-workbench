"""Exercise the actual dialog script without a browser or external network."""
from pathlib import Path
import shutil
import subprocess

import pytest


def test_judgment_manager_interactions():
    node = shutil.which('node')
    if node is None:
        pytest.skip('Node is required for the dialog interaction checks')
    root = Path(__file__).resolve().parents[1]
    result = subprocess.run([node, str(root / 'tests' / 'judgment-manager-ui.cjs')],
                            cwd=root, capture_output=True, text=True, timeout=20)
    assert result.returncode == 0, result.stdout + result.stderr
    assert 'isolated judgment manager interaction checks passed' in result.stdout
