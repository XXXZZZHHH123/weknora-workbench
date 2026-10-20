import shutil
import subprocess
from pathlib import Path


def test_navigation_feedback_preserves_native_links_and_recovers_after_history_restore():
    node = shutil.which('node')
    assert node, 'Node is required by the research web interaction tests'
    script = Path(__file__).resolve().parents[1] / 'src/tin/web/static/navigation.js'
    checks = Path(__file__).with_name('navigation-ui.cjs')
    result = subprocess.run([node, str(checks), str(script)], capture_output=True, text=True, timeout=10)
    assert result.returncode == 0, result.stdout + result.stderr
