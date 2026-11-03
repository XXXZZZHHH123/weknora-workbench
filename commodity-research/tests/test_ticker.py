"""Exercise the shipped ticker controller without browser or service dependencies."""
from pathlib import Path
import shutil
import subprocess


def test_ticker_scroll_interactions():
    node = shutil.which('node')
    assert node, 'Node is required for the ticker interaction checks'
    result = subprocess.run([node, str(Path(__file__).with_name('ticker.test.cjs'))],
                            capture_output=True, text=True, timeout=15)
    assert result.returncode == 0, result.stdout + result.stderr
