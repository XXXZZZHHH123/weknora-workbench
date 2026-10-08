"""Registration safety checks. No real WeKnora, model, or MCP calls."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[1] / 'scripts/register_weknora.py'
spec = importlib.util.spec_from_file_location('register_weknora', SCRIPT)
registration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(registration)


class FakeAPI:
    services = []
    agents = []
    calls = []
    connect = True
    extra_tool = False

    def __init__(self, base):
        self.base = base
        self.token = ''

    def call(self, method, path, payload=None):
        type(self).calls.append((method, path))
        if path.endswith('/auth/login'):
            return {'token': 'login-secret', 'active_tenant': {'id': 10000}}
        if method == 'GET' and path.endswith('/models'):
            return {'data': [{'id': 'model-id', 'name': 'qwen-max', 'type': 'KnowledgeQA', 'status': 'active'}]}
        for endpoint, items, record_id in [('/mcp-services', self.services, 'service-id'), ('/agents', self.agents, 'agent-id')]:
            if path == '/api/v1' + endpoint:
                if method == 'GET':
                    return {'data': items}
                if method == 'POST':
                    record = dict(payload, id=record_id)
                    items.append(record)
                    return {'data': record}
            if path == '/api/v1' + endpoint + '/' + record_id and method == 'PUT':
                items[0].update(payload)
                return {'data': items[0]}
        if path.endswith('/credentials'):
            return {'success': True}
        if path.endswith('/test'):
            return {'data': {'success': self.connect}}
        if path.endswith('/metadata/refresh') or path.endswith('/metadata'):
            names = set(registration.EXPECTED_TOOLS)
            if self.extra_tool:
                names.add('delete_dataset')
            return {'data': {'tools': [{'name': name} for name in names]}}
        raise AssertionError((method, path))


class RegistrationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        (root / 'accounts.json').write_text('[{"email":"test","password":"account-secret"}]')
        (root / 'token').write_text('service-secret-' + 'a' * 32)
        (root / 'token').chmod(0o600)
        self.args = SimpleNamespace(api='http://127.0.0.1:18081', url='http://cotton-data:18082/mcp', accounts=root/'accounts.json', account_index=0, state=root/'integration.json', token_file=root/'token', model_id=None, apply=False)
        FakeAPI.services, FakeAPI.agents, FakeAPI.calls = [], [], []
        FakeAPI.connect, FakeAPI.extra_tool = True, False

    def tearDown(self):
        self.temp.cleanup()

    def run_script(self):
        with patch.object(registration, 'API', FakeAPI), contextlib.redirect_stdout(io.StringIO()) as output:
            registration.run(self.args)
        return output.getvalue()

    def test_dry_run_is_read_only_and_redacted(self):
        output = self.run_script()
        self.assertFalse(self.args.state.exists())
        self.assertEqual([p for m, p in FakeAPI.calls if m != 'GET'], ['/api/v1/auth/login'])
        self.assertNotIn('account-secret', output)
        self.assertNotIn('service-secret', output)
        self.assertNotIn('model-id', output)

    def test_apply_twice_does_not_duplicate_records_or_save_secrets(self):
        self.args.apply = True
        self.run_script()
        self.run_script()
        self.assertEqual(len(FakeAPI.services), 1)
        self.assertEqual(len(FakeAPI.agents), 1)
        self.assertEqual(sum(m == 'POST' and p.endswith('/metadata/refresh') for m, p in FakeAPI.calls), 2)
        saved = self.args.state.read_text()
        self.assertNotIn('service-secret', saved)
        self.assertNotIn('login-secret', saved)
        self.assertEqual(json.loads(saved)['status'], 'registered')

    def test_failed_connection_does_not_create_agent(self):
        self.args.apply = True
        FakeAPI.connect = False
        with self.assertRaises(registration.Failure):
            self.run_script()
        self.assertFalse(FakeAPI.agents)
        self.assertEqual(json.loads(self.args.state.read_text())['status'], 'connection_failed')

    def test_unexpected_tool_prevents_agent_creation(self):
        self.args.apply = True
        FakeAPI.extra_tool = True
        with self.assertRaises(registration.Failure):
            self.run_script()
        self.assertFalse(FakeAPI.agents)

    def test_does_not_take_over_same_name(self):
        self.args.apply = True
        FakeAPI.services.append({'id': 'unowned', 'name': '结构化表格数据', 'description': 'user-owned'})
        with self.assertRaises(registration.Failure):
            self.run_script()
        self.assertFalse(self.args.state.exists())
        self.assertFalse(FakeAPI.agents)


if __name__ == '__main__':
    unittest.main()
