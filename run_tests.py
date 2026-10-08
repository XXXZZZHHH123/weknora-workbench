#!/usr/bin/env python3
"""WeKnora API evidence collector. Uses only Python's standard library.

No model-based grading: preserve full replies and sources for verification.
Only explicitly listed files are uploaded; evaluator material stays local.
"""
import argparse
import datetime as dt
import hashlib
import json
import mimetypes
import os
from pathlib import Path
import re
import secrets
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parent


def save(path, obj, private=False):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    if private and not path.exists():
        path.touch(mode=0o600)
    path.write_text(json.dumps(obj, ensure_ascii=False, indent=2) + '\n')
    if private:
        path.chmod(0o600)


def events(lines):
    """Decode SSE frames, including comments and multiline data fields."""
    data = []
    for raw in lines:
        line = raw.decode('utf-8').rstrip('\r\n')
        if not line:
            if data:
                text = '\n'.join(data)
                if text != '[DONE]':
                    yield json.loads(text)
                data = []
        elif line.startswith('data:'):
            value = line[5:]
            data.append(value[1:] if value.startswith(' ') else value)
    if data:
        text = '\n'.join(data)
        if text != '[DONE]':
            yield json.loads(text)


class API:
    def __init__(self, base, token=''):
        self.base = base.rstrip('/')
        self.token = token
        # Local test traffic must not be sent through ambient proxy settings.
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def request(self, method, path, payload=None, body=None, content_type=None):
        headers = {'Accept-Language': 'zh-CN'}
        if self.token:
            headers['Authorization'] = 'Bearer ' + self.token
        if payload is not None:
            body = json.dumps(payload, ensure_ascii=False).encode()
            content_type = 'application/json'
        if content_type:
            headers['Content-Type'] = content_type
        return urllib.request.Request(self.base + path, data=body, method=method, headers=headers)

    def call(self, method, path, payload=None, **kwargs):
        req = self.request(method, path, payload, **kwargs)
        try:
            with self.opener.open(req, timeout=120) as response:
                data = response.read()
                return response.status, json.loads(data) if data else None
        except urllib.error.HTTPError as exc:
            data = exc.read()
            try:
                result = json.loads(data)
            except (ValueError, UnicodeDecodeError):
                result = {'error': data.decode(errors='replace')}
            return exc.code, result

    def ok(self, method, path, payload=None, **kwargs):
        code, result = self.call(method, path, payload, **kwargs)
        if not 200 <= code < 300 or (isinstance(result, dict) and result.get('success') is False):
            raise RuntimeError(f'{method} {path}: HTTP {code}; request failed')
        return result


def login(api, account):
    result = api.ok('POST', '/api/v1/auth/login', account)
    api.token = result['token']
    return result


def probe(api, output):
    """Real service smoke test, requiring no external AI models."""
    checks = []
    for path in ['/health', '/api/v1/auth/config']:
        code, result = api.call('GET', path)
        checks.append({'check': path, 'http_status': code, 'pass': code == 200})
    accounts_path = ROOT / '.private/accounts.json'
    if accounts_path.exists():
        accounts = json.loads(accounts_path.read_text())
    else:
        stamp = secrets.token_hex(5)
        accounts = [dict(username=f'eval_{letter}_{stamp}', email=f'eval_{letter}_{stamp}@example.com',
                         password='Ev9!' + secrets.token_hex(10)) for letter in ['a', 'b']]
        save(accounts_path, accounts, private=True)
    for account in accounts:
        # First try login: repeated probe runs reuse existing disposable accounts.
        code, result = api.call('POST', '/api/v1/auth/login', account)
        if code != 200:
            api.ok('POST', '/api/v1/auth/register', account)
    login_a = login(api, accounts[0])
    token_a = api.token
    session = api.ok('POST', '/api/v1/sessions', {'title': 'WeKnora evaluation isolation probe'})['data']
    sid = session['id']
    code, _ = api.call('GET', '/api/v1/sessions/' + sid)
    checks.append({'check': 'owner_reads_session', 'http_status': code, 'pass': code == 200})
    login_b = login(api, accounts[1])
    different_tenants = (login_a.get('active_tenant') or {}).get('id') != (login_b.get('active_tenant') or {}).get('id')
    checks.append({'check': 'distinct_test_workspaces', 'pass': different_tenants})
    code, _ = api.call('GET', '/api/v1/sessions/' + sid)
    checks.append({'check': 'other_user_cannot_read_session', 'http_status': code, 'pass': code in (403, 404)})
    api.token = ''
    code, _ = api.call('GET', '/api/v1/sessions/' + sid)
    checks.append({'check': 'anonymous_cannot_read_session', 'http_status': code, 'pass': code in (401, 403)})
    api.token = token_a
    result = {'kind': 'live_api_probe', 'model_quality_tested': False,
              'created_at': dt.datetime.now(dt.timezone.utc).isoformat(), 'checks': checks}
    save(output, result)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return all(c['pass'] for c in checks)


def upload(api, args):
    manifest = json.loads(Path(args.manifest).read_text())
    records = []
    # Manifest is an explicit allow-list, never recursively import a folder.
    for item in manifest['files']:
        path = Path(item['path']).resolve()
        if re.search(r'1[befg]-|测试用例|执行记录|测试报告|横向对比|cases\.json', path.name):
            raise ValueError('Evaluator material must not enter the knowledge base: ' + path.name)
        raw = path.read_bytes()
        digest = hashlib.sha256(raw).hexdigest()
        if item.get('sha256') and item['sha256'] != digest:
            raise ValueError('Input file changed: ' + path.name)
        boundary = 'weknora-eval-' + secrets.token_hex(16)
        filename = path.name.replace('"', '_').replace('\r', '_').replace('\n', '_')
        mime = mimetypes.guess_type(filename)[0] or 'application/octet-stream'
        head = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{filename}"\r\n'
                f'Content-Type: {mime}\r\n\r\n').encode()
        tail = f'\r\n--{boundary}\r\nContent-Disposition: form-data; name="enable_multimodel"\r\n\r\n{str(args.vision).lower()}\r\n--{boundary}--\r\n'.encode()
        start = time.monotonic()
        code, result = api.call('POST', f'/api/v1/knowledge-bases/{args.kb}/knowledge/file',
                                body=head + raw + tail, content_type='multipart/form-data; boundary=' + boundary)
        records.append({'file': str(path), 'sha256': digest, 'http_status': code,
                        'seconds': time.monotonic() - start, 'response': result})
        save(args.output, records)
        print(f'{path.name}: HTTP {code}', flush=True)
        if code not in (200, 201, 409):
            return False
    return True


def run_cases(api, args):
    cases = json.loads(Path(args.cases).read_text())['cases']
    if args.ids:
        selected = set(args.ids.split(','))
        available = {c['id'] for c in cases}
        if selected - available:
            raise ValueError('Unknown case IDs: ' + ', '.join(sorted(selected - available)))
        cases = [c for c in cases if c['id'] in selected]
    if not cases:
        raise ValueError('No test cases selected')
    out = Path(args.output)
    out.mkdir(parents=True, exist_ok=True)
    all_ok = True
    for case in cases:
        kb = args.conflict_kb if case['id'] in ('KB-13', 'KB-14') else args.kb
        for repeat in range(1, args.repeats + 1):
            target = out / f'{case["id"]}-run{repeat}.json'
            if target.exists():
                raise ValueError('Evidence already exists; choose a new output directory: ' + str(target))
            record = {'case_id': case['id'], 'question': case['question'], 'knowledge_base_id': kb,
                      'mode': args.mode, 'chat_model_id': args.model, 'agent_id': args.agent,
                      'created_at': dt.datetime.now(dt.timezone.utc).isoformat(),
                      'status': 'not_run', 'answer': '', 'events': [], 'score': None,
                      'requires_source_review': True, 'first_answer_seconds': None}
            if not kb:
                record['status'] = 'blocked_missing_conflict_kb'
                all_ok = False
                save(target, record)
                continue
            start = time.monotonic()
            try:
                session = api.ok('POST', '/api/v1/sessions', {'title': f'eval {case["id"]} repeat {repeat}'})['data']
                record['session_id'] = session['id']
                payload = {'query': case['question'], 'knowledge_base_ids': [kb],
                           'summary_model_id': args.model, 'disable_title': True,
                           'web_search_enabled': False, 'local_browser_enabled': False}
                if case.get('knowledge_ids'):
                    payload['knowledge_ids'] = case['knowledge_ids']
                if args.agent:
                    payload['agent_id'] = args.agent
                endpoint = 'agent-chat' if args.mode == 'agent' else 'knowledge-chat'
                record['request'] = payload
                req = api.request('POST', f'/api/v1/{endpoint}/{session["id"]}', payload)
                chat_start = time.monotonic()
                complete = False
                stream_error = False
                truncated = False
                with api.opener.open(req, timeout=120) as response:
                    if 'text/event-stream' not in response.headers.get('Content-Type', ''):
                        raise RuntimeError('Expected SSE response')
                    for event in events(response):
                        elapsed = time.monotonic() - chat_start
                        record['events'].append({'seconds': elapsed, 'event': event})
                        kind = event.get('response_type')
                        if kind == 'answer':
                            content = event.get('content') or ''
                            record['answer'] += content
                            if content and record['first_answer_seconds'] is None:
                                record['first_answer_seconds'] = elapsed
                            truncated = truncated or bool((event.get('data') or {}).get('truncated'))
                        if kind == 'error':
                            stream_error = True
                        if kind == 'complete':
                            complete = True
                        if complete or stream_error:
                            break
                        if elapsed > args.timeout:
                            raise TimeoutError('Test case exceeded time budget')
                record['chat_seconds'] = time.monotonic() - chat_start
                record['status'] = ('stream_error' if stream_error else 'incomplete_stream' if not complete
                                    else 'truncated' if truncated else 'empty_answer' if not record['answer'].strip()
                                    else 'answered_ungraded')
            except Exception as exc:
                record['status'] = 'execution_error'
                # Do not print upstream response bodies which may contain credentials.
                record['error'] = {'type': type(exc).__name__, 'http_status': getattr(exc, 'code', None)}
            record['total_seconds'] = time.monotonic() - start
            all_ok = all_ok and record['status'] == 'answered_ungraded'
            save(target, record)
            print(f'{case["id"]} #{repeat}: {record["status"]}, {record["total_seconds"]:.2f}s', flush=True)
    return all_ok


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--base', default='http://127.0.0.1:18081')
    sub = p.add_subparsers(dest='command', required=True)
    pr = sub.add_parser('probe', help='Live login/session isolation tests without models')
    pr.add_argument('--output', default=str(ROOT / 'results/api-probe.json'))
    for name in ('run', 'upload'):
        parser = sub.add_parser(name)
        parser.add_argument('--account-index', type=int, default=0)
        parser.add_argument('--kb', required=True)
        parser.add_argument('--output', required=True)
        if name == 'upload':
            parser.add_argument('--manifest', required=True)
            parser.add_argument('--vision', action='store_true')
        else:
            parser.add_argument('--cases', default=str(ROOT / 'cases.json'))
            parser.add_argument('--ids')
            parser.add_argument('--model', required=True)
            parser.add_argument('--agent', default='')
            parser.add_argument('--mode', choices=['rag', 'agent'], default='rag')
            parser.add_argument('--conflict-kb')
            parser.add_argument('--repeats', type=int, choices=range(1, 6), default=1)
            parser.add_argument('--timeout', type=int, default=600)
    args = p.parse_args()
    if args.command == 'run' and args.mode == 'agent' and not args.agent:
        p.error('Agent tests require an explicitly configured --agent ID')
    api = API(args.base)
    if args.command == 'probe':
        ok = probe(api, args.output)
    else:
        accounts = json.loads((ROOT / '.private/accounts.json').read_text())
        login(api, accounts[args.account_index])
        ok = upload(api, args) if args.command == 'upload' else run_cases(api, args)
    raise SystemExit(0 if ok else 1)


if __name__ == '__main__':
    main()
