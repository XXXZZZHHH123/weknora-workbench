"""Run native diagram layout, routing and task-state checks in Python CI."""
from pathlib import Path
import shutil
import subprocess

import pytest


def test_native_diagram_geometry():
    node = shutil.which('node')
    if node is None:
        pytest.skip('Node is required for the native diagram geometry checks')
    root = Path(__file__).resolve().parents[1]
    result = subprocess.run([node, str(root / 'tests' / 'diagram-geometry.test.cjs')],
        cwd=root, capture_output=True, text=True, timeout=20)
    assert result.returncode == 0, result.stdout + result.stderr


def test_native_diagram_generation_terminal_states():
    node = shutil.which('node')
    if node is None:
        pytest.skip('Node is required for the native diagram task-state checks')
    root = Path(__file__).resolve().parents[1]
    result = subprocess.run([node, str(root / 'tests' / 'diagram-generation-state.test.cjs')],
        cwd=root, capture_output=True, text=True, timeout=20)
    assert result.returncode == 0, result.stdout + result.stderr
