import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


REPO = Path(__file__).resolve().parents[2]


class LocalServicesTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.calls = self.root / "calls.jsonl"
        (self.root / "scripts").mkdir()
        for script in ("start-local.sh", "stop-local.sh", "run-local-service.sh", "check-local-docreader.py"):
            if (REPO / "scripts" / script).exists():
                shutil.copy(REPO / "scripts" / script, self.root / "scripts" / script)
        for file in (".build/weknora-local", ".venv/bin/structured-data", ".venv/bin/uvicorn",
                     "commodity-research/src/tin/web/app.py", "WeKnora/frontend/dist/index.html"):
            path = self.root / file
            path.parent.mkdir(parents=True, exist_ok=True)
            path.touch()
        (self.root / ".private").mkdir()
        (self.root / ".private/local.env").write_text("JWT_SECRET=fixture-only\nSYSTEM_AES_KEY=fixture-only\n")
        (self.root / ".private/research-service-token").write_text("fixture-service-token-not-a-secret\n")
        (self.root / ".private/docreader-service-token").write_text("fixture-docreader-token-not-a-secret\n")
        (self.root / "logs").mkdir()
        (self.root / ".venv/bin/python").symlink_to(sys.executable)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        recorder = """#!{python}
import json, os, sys
from pathlib import Path
with Path(os.environ['LOCAL_SERVICE_CALLS']).open('a') as out:
    out.write(json.dumps({{'command': Path(sys.argv[0]).name, 'args': sys.argv[1:]}}) + '\\n')
name = Path(sys.argv[0]).name
if name == 'systemctl':
    if 'is-active' in sys.argv: sys.exit(0 if os.environ.get('FAKE_ACTIVE') else 1)
    if 'show' in sys.argv: print(12345)
if name == 'curl':
    services = {{'18081':'backend', '18082':'structured-data', '18083':'commodity-research', '18080':'frontend'}}
    rows = [json.loads(line) for line in Path(os.environ['LOCAL_SERVICE_CALLS']).read_text().splitlines()]
    service = next((service for port, service in services.items() if port in sys.argv[-1]), '')
    started = any(row['command'] == 'systemd-run' and '--unit=weknora-workbench-' + service in row['args'] for row in rows)
    sys.exit(0 if started else 1)
if name == 'seq': print(1)
if name == 'python':
    if any('check-local-docreader.py' in arg for arg in sys.argv):
        rows = [json.loads(line) for line in Path(os.environ['LOCAL_SERVICE_CALLS']).read_text().splitlines()]
        started = any(row['command'] == 'systemd-run' and '--unit=weknora-workbench-docreader' in row['args'] for row in rows)
        sys.exit(0 if started and not os.environ.get('FAKE_DOCREADER_FAIL') else 1)
    if '-m' in sys.argv:
        print(json.dumps({{'args':sys.argv[1:], 'address':os.environ.get('DOCREADER_ADDR'),
                          'host':os.environ.get('DOCREADER_GRPC_HOST'), 'token_present':bool(os.environ.get('GRPC_AUTH_TOKEN'))}}))
""".format(python=sys.executable)
        for command in ("systemctl", "systemd-run", "curl", "seq", "sleep"):
            self.executable(self.bin / command, recorder)
        self.executable(self.root / "WeKnora/docreader/.venv/bin/python", recorder)
        self.executable(self.root / ".build/weknora-local", "#!%s\nimport json,os\nprint(json.dumps({'address':os.environ.get('DOCREADER_ADDR'), 'transport':os.environ.get('DOCREADER_TRANSPORT'), 'token_present':bool(os.environ.get('GRPC_AUTH_TOKEN')), 'ssrf_extra':os.environ.get('SSRF_WHITELIST_EXTRA'), 'no_proxy':os.environ.get('NO_PROXY'), 'lower_no_proxy':os.environ.get('no_proxy')}))\n" % sys.executable)
        self.env = {**os.environ, "PATH": str(self.bin) + ":" + os.environ["PATH"], "LOCAL_SERVICE_CALLS": str(self.calls)}

    def executable(self, path, content):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)
        path.chmod(0o755)

    def run_script(self, script, *args, **extra):
        return subprocess.run(["bash", str(self.root / "scripts" / script), *args],
                              env={**self.env, **extra}, capture_output=True, text=True, timeout=10)

    def records(self):
        return [json.loads(line) for line in self.calls.read_text().splitlines()]

    def test_starts_docreader_before_backend_and_records_pid(self):
        result = self.run_script("start-local.sh")
        self.assertEqual(result.returncode, 0, result.stderr)
        units = [arg.split("=", 1)[1] for row in self.records() if row["command"] == "systemd-run"
                 for arg in row["args"] if arg.startswith("--unit=")]
        self.assertEqual(units, ["weknora-workbench-docreader", "weknora-workbench-backend",
                                 "weknora-workbench-structured-data", "weknora-workbench-commodity-research",
                                 "weknora-workbench-frontend"])
        self.assertEqual((self.root / ".build/run/docreader.pid").read_text().strip(), "12345")

    def test_parser_failure_prevents_starting_backend(self):
        result = self.run_script("start-local.sh", FAKE_DOCREADER_FAIL="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("docreader", result.stderr)
        units = [row for row in self.records() if row["command"] == "systemd-run"]
        self.assertEqual(len(units), 1)
        self.assertIn("--unit=weknora-workbench-docreader", units[0]["args"])

    def test_missing_docreader_environment_fails_before_starting_services(self):
        (self.root / "WeKnora/docreader/.venv/bin/python").unlink()
        result = self.run_script("start-local.sh")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("docreader", result.stderr)
        self.assertFalse(self.calls.exists())

    def test_backend_receives_same_local_parser_address_and_auth(self):
        result = self.run_script("run-local-service.sh", "backend")
        self.assertEqual(result.returncode, 0, result.stderr)
        output = json.loads((self.root / "logs/backend.log").read_text())
        self.assertEqual(output["address"], "127.0.0.1:50051")
        self.assertEqual(output["transport"], "grpc")
        self.assertTrue(output["token_present"])

    def test_docreader_uses_existing_environment_and_loopback(self):
        result = self.run_script("run-local-service.sh", "docreader")
        self.assertEqual(result.returncode, 0, result.stderr)
        output = json.loads((self.root / "logs/docreader.log").read_text())
        self.assertEqual(output["args"], ["-m", "docreader.main"])
        self.assertEqual(output["host"], "127.0.0.1")
        self.assertTrue(output["token_present"])

    def test_local_overrides_keep_operator_egress_and_proxy_bypass(self):
        with (self.root / ".private/local.env").open("a") as output:
            output.write("SSRF_WHITELIST_EXTRA=trusted-api.invalid\nNO_PROXY=platform.invalid\nno_proxy=parser.invalid\n")
        result = self.run_script("run-local-service.sh", "backend")
        self.assertEqual(result.returncode, 0, result.stderr)
        output = json.loads((self.root / "logs/backend.log").read_text())
        self.assertEqual(set(output["ssrf_extra"].split(",")), {"trusted-api.invalid", "127.0.0.1", "localhost"})
        expected = {"platform.invalid", "parser.invalid", "127.0.0.1", "localhost", "::1"}
        self.assertEqual(set(output["no_proxy"].split(",")), expected)
        self.assertEqual(output["no_proxy"], output["lower_no_proxy"])

    def test_inherited_proxy_bypass_is_preserved_without_local_overrides(self):
        result = self.run_script("run-local-service.sh", "backend", SSRF_WHITELIST_EXTRA="inherited.invalid",
                                 NO_PROXY="upper.invalid", no_proxy="lower.invalid")
        self.assertEqual(result.returncode, 0, result.stderr)
        output = json.loads((self.root / "logs/backend.log").read_text())
        self.assertIn("inherited.invalid", output["ssrf_extra"].split(","))
        self.assertIn("upper.invalid", output["no_proxy"].split(","))
        self.assertIn("lower.invalid", output["no_proxy"].split(","))
        self.assertEqual(output["no_proxy"], output["lower_no_proxy"])

    def test_stops_docreader_after_backend_and_removes_stale_pid(self):
        run = self.root / ".build/run"
        run.mkdir(parents=True)
        (run / "docreader.pid").write_text("12345\n")
        result = self.run_script("stop-local.sh", FAKE_ACTIVE="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        units = [row["args"][-1] for row in self.records() if row["args"][:2] == ["--user", "stop"]]
        self.assertEqual(units[-2:], ["weknora-workbench-backend", "weknora-workbench-docreader"])
        self.assertFalse((run / "docreader.pid").exists())


if __name__ == "__main__":
    unittest.main()
