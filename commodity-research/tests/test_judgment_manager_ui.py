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


def test_judgment_manager_management_and_caliber_interactions():
    node = shutil.which('node')
    if node is None:
        pytest.skip('Node is required for the dialog interaction checks')
    root = Path(__file__).resolve().parents[1]
    result = subprocess.run([node, str(root / 'tests' / 'judgment-manager-management.test.cjs')],
                            cwd=root, capture_output=True, text=True, timeout=20)
    assert result.returncode == 0, result.stdout + result.stderr
    assert 'management/caliber scenarios passed' in result.stdout


@pytest.mark.parametrize('script', [
    'judgment-manager-empty-first.test.cjs',
    'judgment-manager-latest-result.test.cjs',
    'judgment-manager-pending-group.test.cjs',
    'judgment-manager-source-caliber.test.cjs',
    'judgment-manager-multiple-bindings.test.cjs',
    'judgment-manager-item-confirmation.test.cjs',
    'chart-palette.test.cjs',
])
def test_judgment_manager_review_and_recovery_interactions(script):
    node = shutil.which('node')
    if node is None:
        pytest.skip('Node is required for the dialog interaction checks')
    root = Path(__file__).resolve().parents[1]
    result = subprocess.run([node, str(root / 'tests' / script)],
        cwd=root, capture_output=True, text=True, timeout=30)
    assert result.returncode == 0, result.stdout + result.stderr
