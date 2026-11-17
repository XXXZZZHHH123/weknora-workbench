"""Actual monitoring browser flow, using local Chromium and isolated API fixtures."""
import os
from pathlib import Path
import shutil
import subprocess

import pytest


def test_chartkit_monitor_date_markers():
    node = shutil.which('node')
    if not node:
        pytest.skip('Node is required for the ChartKit event marker checks')
    root = Path(__file__).resolve().parents[1]
    result = subprocess.run([node, '--test', str(root / 'tests' / 'chart-kit-render.test.cjs')],
        cwd=root, capture_output=True, text=True, timeout=30)
    assert result.returncode == 0, result.stdout + result.stderr


def test_monitor_browser_flow():
    node = shutil.which('node')
    if not node or not os.environ.get('RESEARCH_PLAYWRIGHT'):
        pytest.skip('Set RESEARCH_PLAYWRIGHT and provide Node/Chromium for the monitoring browser flow')
    root = Path(__file__).resolve().parents[1]
    result = subprocess.run([node, str(root / 'tests' / 'monitors-ui.cjs')], cwd=root,
        capture_output=True, text=True, timeout=90)
    assert result.returncode == 0, result.stdout + result.stderr
    assert 'PASS: monitor sentence' in result.stdout
