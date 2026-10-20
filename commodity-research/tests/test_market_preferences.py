import os
from pathlib import Path
import shutil
import subprocess


def test_core_selection_order_auto_favorites_isolation_limits_and_storage_failure():
    node = shutil.which('node')
    assert node, 'Node is required by the research interaction tests'
    result = subprocess.run([node, str(Path(__file__).with_name('indicator-preferences.test.cjs'))],
        env={**os.environ, 'NODE_OPTIONS': '--max-old-space-size=128 --v8-pool-size=1'},
        capture_output=True, text=True, timeout=10)
    assert result.returncode == 0, result.stdout + result.stderr
