"""Opt-in real model test of the user's two-turn price / exchange-rate query."""
import json
import argparse
from pathlib import Path
import sys
import time

ROOT=Path(__file__).resolve().parents[1]
sys.path.insert(0,str(ROOT.parent))
from run_tests import API, events, login, save


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--output',default='results/fx-followup-live.json')
    target=ROOT/parser.parse_args().output
    if target.exists():
        raise ValueError('Evidence already exists; preserve it before another run')
    integration=json.loads((ROOT/'results/integration.json').read_text())
    api=API('http://127.0.0.1:18081')
    login(api,json.loads((ROOT.parent/'.private/accounts.json').read_text())[0])
    sid=api.ok('POST','/api/v1/sessions',{'title':'汇率同义检索连续追问复测','agent_id':integration['agent_id']})['data']['id']
    report={'session_id':sid,'turns':[]}
    for question in ['2023/9/7美国 EMOT M 1-1/8价格是多少','根据当天汇率，人民币是多少']:
        record={'question':question,'answer':'','events':[],'status':'started'}
        report['turns'].append(record);start=time.monotonic()
        payload={'query':question,'agent_id':integration['agent_id'],'knowledge_base_ids':[],
                 'disable_title':True,'web_search_enabled':False,'local_browser_enabled':False}
        try:
            with api.opener.open(api.request('POST','/api/v1/agent-chat/'+sid,payload),timeout=120) as response:
                for event in events(response):
                    record['events'].append(event)
                    kind=event.get('response_type')
                    if kind=='answer':record['answer']+=event.get('content') or ''
                    if kind=='error':record['status']='stream_error';break
                    if kind=='complete':
                        record['answer']=(event.get('data') or {}).get('final_content') or record['answer']
                        record['status']='answered_ungraded';break
                    if time.monotonic()-start>180:raise TimeoutError()
        except Exception as exc:
            record['status']='execution_error';record['error_type']=type(exc).__name__
        record['elapsed_seconds']=round(time.monotonic()-start,3)
        save(target,report)
        print(json.dumps({k:v for k,v in record.items() if k!='events'},ensure_ascii=False),flush=True)
        if record['status']!='answered_ungraded':break
        if len(report['turns'])==1 and '99.7' not in record['answer']:
            print('First answer failed source check; do not continue an invalid follow-up.',flush=True)
            break


if __name__=='__main__':main()
