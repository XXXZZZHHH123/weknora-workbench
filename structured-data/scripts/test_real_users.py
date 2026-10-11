#!/usr/bin/env python3
"""Bounded, real local Agent SSE evaluation. No ingestion or model grading.

Each group owns one session; follow-ups remain ordered. Captures raw evidence
without credentials. Completion is deliberately named ungraded.
"""
import argparse
import concurrent.futures
import datetime as dt
import json
from pathlib import Path
import sys
import time
import urllib.request
import threading

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT.parent))
sys.path.insert(0,str(ROOT/'src'))
from structured_data.model_settings import public_model
from run_tests import API, events, login, save


def run_group(group, output, agent_id, account, model_id=None, stop=None):
    api = API('http://127.0.0.1:18081')
    login(api, account)
    sid = api.ok('POST', '/api/v1/sessions', {
        'title': '真实用户测试 ' + group['id'], 'agent_id': agent_id})['data']['id']
    pending=None;pending_tool=None
    for case in group['cases']:
        if stop and stop.is_set():break
        target = output / (case['id'] + '.json')
        record = dict(case, group=group['id'], session_id=sid, events=[],
                      timestamp=dt.datetime.now(dt.timezone.utc).isoformat(), status='started')
        profile=public_model(ROOT/'data')
        record['field_model']={k:profile.get(k) for k in ('model','base_url')}
        save(target, record)
        print(case['id'], 'started', flush=True)
        start = time.monotonic()
        try:
            query=case['question']
            if (case.get('confirm_series_ids') or case.get('confirm_note')) and pending and pending.get('status')=='needs_clarification':
                if not pending or pending.get('status')!='needs_clarification':raise ValueError('No genuine pending choice for this test confirmation')
                selections={}
                for f in pending['fields']:
                    matching=[c['series_id'] for c in f['candidates'] if c['series_id'] in case.get('confirm_series_ids',[])]
                    if len(matching)==1:selections[f['role']]=matching[0]
                    elif len(f['candidates'])==1:selections[f['role']]=f['candidates'][0]['series_id']
                    elif not case.get('confirm_note'):raise ValueError('Named test choice is absent or ambiguous; never select the first alternative')
                query='字段确认提交：'+json.dumps({'resolution_id':pending['resolution_id'],'source_tool':pending_tool,'selections':selections,'note':case.get('confirm_note',''),'summary':case['question']},ensure_ascii=False)
            payload = {'query': query, 'agent_id': agent_id,
                       'knowledge_base_ids': [], 'disable_title': True,
                       'web_search_enabled': False, 'local_browser_enabled': False}
            if model_id:payload['summary_model_id']=model_id
            record['request']=payload
            req = api.request('POST', '/api/v1/agent-chat/' + sid, payload)
            with api.opener.open(req, timeout=190) as response:
                for event in events(response):
                    record['events'].append(event)
                    if event.get('response_type')=='tool_result':
                        data=event.get('data') or {};text=data.get('output') or ''
                        try:
                            result=json.loads(text[text.index('{'):])
                            if result.get('kind')=='structured_field_resolution':pending=result;pending_tool=data.get('tool_name')
                            if 'Arrearage' in str(result.get('message','')) and stop:stop.set()
                        except (ValueError,AttributeError):pass
                    if event.get('response_type') in ('tool_result', 'tool_call', 'error'):
                        save(target, record)
                    if event.get('response_type') == 'error':
                        record['status'] = 'stream_error'
                        break
                    if event.get('response_type') == 'complete':
                        record['answer'] = (event.get('data') or {}).get('final_content')
                        record['status'] = 'completed_ungraded'
                        break
                    if time.monotonic() - start > 180:
                        raise TimeoutError('Case exceeded 180 seconds')
                else:
                    record['status'] = 'incomplete_stream'
        except Exception as exc:
            record.update(status='execution_error', error_type=type(exc).__name__)
        record['elapsed_seconds'] = round(time.monotonic() - start, 2)
        code, messages = api.call('GET', '/api/v1/messages/' + sid + '/load')
        if code == 200:
            record['persisted_messages'] = messages.get('data', [])
        save(target, record)
        print(case['id'], record['status'], record['elapsed_seconds'], flush=True)
        if record['status'] != 'completed_ungraded':
            break  # Follow-ups must not inherit an unknown unfinished state.


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--groups', default='all')
    p.add_argument('--output', required=True)
    p.add_argument('--workers', type=int, choices=[1, 2], default=2)
    p.add_argument('--cases',type=Path,default=ROOT/'tests/real_user_cases_20261010.json')
    p.add_argument('--model-id')
    args = p.parse_args()
    output = Path(args.output).resolve()
    bank = json.loads(args.cases.read_text())
    groups = [g for g in bank['groups'] if args.groups == 'all' or g['id'] in args.groups.split(',')]
    if any((output / (c['id'] + '.json')).exists() for g in groups for c in g['cases']):
        raise ValueError('Existing evidence must not be overwritten')
    account = json.loads((ROOT.parent / '.private/accounts.json').read_text())[0]
    api = API('http://127.0.0.1:18081'); login(api, account)
    agent = next(a for a in api.ok('GET', '/api/v1/agents')['data'] if a['name'] == '表格数据分析（结构化）')
    save(output / ('run-' + str(int(time.time())) + '.json'), {
        'groups': [g['id'] for g in groups], 'agent_id': agent['id'],
        'model_id': args.model_id or agent['config']['model_id'], 'workers': args.workers,
        'max_case_seconds': 180, 'web_search_enabled': False,
        'grading': 'independent source review required'})
    code, health=api.call('GET','/health')
    if code!=200:raise RuntimeError('Local application is not healthy; no test submitted')
    import urllib.request
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open('http://127.0.0.1:18082/health',timeout=5) as response:
        if response.status!=200:raise RuntimeError('Local structured service is not healthy; no test submitted')
    stop=threading.Event()
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
        futures = [pool.submit(run_group, g, output, agent['id'], account,args.model_id,stop) for g in groups]
        for f in futures:
            f.result()


if __name__ == '__main__':
    main()
