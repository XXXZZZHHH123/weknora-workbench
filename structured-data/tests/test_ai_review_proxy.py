"""The model endpoint follows the operator's proxy, without real model calls."""
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import threading

from structured_data.ai_review import AIRequestBudget, _request


def test_mapping_model_uses_configured_system_http_proxy(monkeypatch):
    requests = []

    class Proxy(BaseHTTPRequestHandler):
        def do_POST(self):
            requests.append((self.path, json.loads(self.rfile.read(int(self.headers['Content-Length'])))))
            response = json.dumps({'choices': [{'finish_reason': 'stop', 'message': {
                'content': json.dumps({'column_updates': [], 'block_updates': []})}}]}).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(response)))
            self.end_headers()
            self.wfile.write(response)

        def log_message(self, *args):
            pass

    server = HTTPServer(('127.0.0.1', 0), Proxy)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    for variable in ('HTTP_PROXY', 'http_proxy', 'HTTPS_PROXY', 'https_proxy', 'ALL_PROXY', 'all_proxy'):
        monkeypatch.setenv(variable, f'http://127.0.0.1:{server.server_port}')
    monkeypatch.setenv('NO_PROXY', '')
    monkeypatch.setenv('no_proxy', '')
    try:
        result = _request({'model': 'isolated-fixture', 'base_url': 'http://127.0.0.1:1/v1',
            'api_key': 'test-key'}, {'candidate_mapping': {}}, AIRequestBudget(1), None)
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)
    assert result == {'column_updates': [], 'block_updates': []}
    assert requests[0][0] == 'http://127.0.0.1:1/v1/chat/completions'
    assert requests[0][1]['model'] == 'isolated-fixture'
