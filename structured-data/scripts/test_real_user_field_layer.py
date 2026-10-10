#!/usr/bin/env python3
"""Compare original utterances at the real semantic field service layer.

Record metadata recall and model proposals to distinguish Agent routing from
field planning failures. Does not change prompts, data or implementation.
"""
import json
import os
from pathlib import Path
import sys
import time
ROOT=Path(__file__).resolve().parents[1]
sys.path.insert(0,str(ROOT/'src'))
os.environ['STRUCTURED_MODEL_CONFIG']=str(ROOT.parent/'.private/model-config.json')
from structured_data.store import DataStore
from structured_data.field_resolver import FieldResolver,ask_model

out=ROOT/'results/real-users-20261010/field-layer';out.mkdir(parents=True,exist_ok=True)
bank=json.loads((ROOT/'tests/real_user_cases_20261010.json').read_text())
cases={c['id']:c for g in bank['groups'] for c in g['cases']}
store=DataStore(ROOT/'data')
for cid in sys.argv[1:]:
    path=out/(cid+'.json')
    if path.exists():raise ValueError('Do not overwrite prior evidence')
    record=dict(cases[cid],model_calls=[])
    def call(payload):
        t=time.monotonic();raw=ask_model(payload,store.root)
        record['model_calls'].append({'payload':payload,'proposal':raw,'seconds':round(time.monotonic()-t,2)})
        path.write_text(json.dumps(record,ensure_ascii=False,indent=2)+'\n')
        return raw
    resolver=FieldResolver(store,call)
    start=time.monotonic()
    try:record['result']=resolver.tool_answer(cases[cid]['question'],dataset_id='424aed70d6a68ef7b97a067e')
    except Exception as exc:record['error_type']=type(exc).__name__
    record['seconds']=round(time.monotonic()-start,2)
    path.write_text(json.dumps(record,ensure_ascii=False,indent=2)+'\n')
    print(cid,record['seconds'],record.get('result',{}).get('status'),flush=True)
