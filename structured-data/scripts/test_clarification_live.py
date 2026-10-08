"""One ambiguous real chat, leaves user choice for actual browser test."""
import json, sys, time
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1];sys.path.insert(0,str(ROOT.parent))
from run_tests import API, events, login, save
integration=json.loads((ROOT/'results/integration.json').read_text())
api=API('http://127.0.0.1:18081');login(api,json.loads((ROOT.parent/'.private/accounts.json').read_text())[0])
sid=api.ok('POST','/api/v1/sessions',{'title':'字段语义与选择弹窗验收','agent_id':integration['agent_id']})['data']['id']
question=sys.argv[1] if len(sys.argv)>1 else '2023年9月7日美国棉花的报价是多少？'
target=ROOT/'results'/('clarification-live-'+sid[:8]+'.json')
report={'session_id':sid,'question':question,'events':[]}
save(target,report);print('session_id='+sid,flush=True)
with api.opener.open(api.request('POST','/api/v1/agent-chat/'+sid,{'query':question,'agent_id':integration['agent_id'],'knowledge_base_ids':[],'disable_title':True,'web_search_enabled':False,'local_browser_enabled':False}),timeout=120) as response:
    for event in events(response):
        report['events'].append(event)
        if event.get('response_type')=='complete':report['answer']=(event.get('data') or {}).get('final_content');break
        if event.get('response_type')=='error':break
save(target,report)
print(report.get('answer'),flush=True)
