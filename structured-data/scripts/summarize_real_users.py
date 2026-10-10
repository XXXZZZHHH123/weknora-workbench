#!/usr/bin/env python3
"""Summarize recorded evidence only; does not grade correctness."""
import json
from pathlib import Path
import sys


def unpack(text):
    if not isinstance(text,str):return None
    try:return json.loads(text[text.index('{'):])
    except (ValueError,TypeError):return None


def main():
    root=Path(sys.argv[1]); summaries=[]
    for p in sorted(root.rglob('U*.json')):
        r=json.loads(p.read_text()); tools=[]; plans=[]
        if 'status' not in r:continue  # Separate field-layer probes have a different schema.
        for e in r.get('events',[]):
            if e.get('response_type')=='tool_result':
                d=e.get('data') or {}; result=unpack(d.get('output'))
                tools.append({'name':d.get('tool_name'),'success':d.get('success'),'ms':d.get('duration_ms'),'error':d.get('error')})
                if isinstance(result,dict) and result.get('kind') in ['structured_field_resolution','structured_tool_notice']:
                    answer=result.get('answer') or {}
                    plans.append({'kind':result.get('kind'),'status':result.get('status'),'intent':result.get('intent'),
                         'question':result.get('question'),'query':result.get('query'),'date':result.get('date'),
                         'planning_error':result.get('planning_error'),'requires_note':result.get('requires_note'),
                         'message':result.get('message'),'retrieval':result.get('retrieval'),
                         'fields':[{'role':f.get('role'),'label':f.get('label'),'op':f.get('operation'),
                           'ids':[c.get('series_id') for c in f.get('candidates',[])]} for f in result.get('fields',[])],
                         'answer':answer})
        messages=r.get('persisted_messages',[])
        current=[m for m in messages if m.get('role')=='assistant']
        summaries.append({'id':r['id'],'path':str(p),'question':r['question'],'status':r['status'],
           'seconds':r.get('elapsed_seconds'),'answer':r.get('answer') or (current[-1].get('content') if current else None),
           'tools':tools,'plans':plans})
    (root/'summary.json').write_text(json.dumps(summaries,ensure_ascii=False,indent=2)+'\n')
    for s in summaries:
        print(s['id'],s['path'],s['status'],s['seconds'])
        for p in s['plans']:
            a=p['answer']; print(' ',p['status'],p['intent'],p['query'],p['date'],p['fields'],p['message'] or p['planning_error'] or p['question'])
            if a:
                print('  calculation', {k:a.get(k) for k in ['value','unit','expression','operation','notice','count'] if k in a})
                for role,d in a.get('sources',{}).items():
                    if not isinstance(d,dict):continue
                    print('  source',role,d.get('series',{}).get('series_id'),d.get('aggregate'),
                          [(r.get('date'),r.get('value'),r.get('source_cell')) for r in d.get('rows',[])[:5]],
                          'count',d.get('valid_count'),'filters',d.get('filters'))
        if not s['plans'] or not s['plans'][-1]['answer']: print('  prose',str(s['answer'])[:700])


if __name__=='__main__':main()
