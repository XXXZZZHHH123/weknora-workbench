"""Bounded live checks: synthetic source metadata, real configured chat model."""
import json, os, tempfile, time, sys
from pathlib import Path
from structured_data.field_resolver import FieldResolver, ask_model
from structured_data.store import DataStore
ROOT=Path(__file__).resolve().parents[1]
os.environ['COTTON_MODEL_CONFIG']=str(ROOT.parent/'.private/model-config.json')

def main():
    report=[]
    with tempfile.TemporaryDirectory() as directory:
        store=DataStore(Path(directory))
        series=[dict(series_id=sid,sheet='经营日报',metric=name,unit=unit,dimensions={},header_cells=[cell]) for sid,name,unit,cell in [('stock','期末结存','吨','B1'),('sales','本期出库','吨','C1'),('usd','美元兑人民币','人民币/美元','D1'),('eur','欧元兑人民币','人民币/欧元','E1')]]
        rows=[dict(series_id=sid,date='2023-09-07',value=value,raw_value=value,source_cell=cell,date_cell='A2',formula=None,value_status='valid') for sid,value,cell in [('stock',120,'B2'),('sales',80,'C2'),('usd',7.3,'D2'),('eur',7.8,'E2')]]
        ds=store.publish(dict(source_file='通用语义测试.xlsx',sha256='b'*64,rule_version='semantic-test',series=series,observations=rows,sheets=[],excluded_regions=[],issues=[]))['dataset_id']
        def recorded_model(payload):
            raw=ask_model(payload)
            with (ROOT/'results/semantic-model-raw.jsonl').open('a') as f:f.write(json.dumps({'question':payload['question'],'response':raw},ensure_ascii=False)+'\n')
            return raw
        resolver=FieldResolver(store,recorded_model)
        questions=['2023年9月7日把本期出库量加上期末结存，一共多少吨？'] if '--calculation-only' in sys.argv else ['2023年9月7日还剩多少货？','2023年9月7日的汇率是多少？']
        for question in questions:
            t=time.monotonic();plan=resolver.resolve(ds,question,context_sheet='经营日报',date='2023-09-07')
            item=dict(question=question,elapsed_seconds=round(time.monotonic()-t,2),plan=plan)
            if plan['status']=='resolved':item['result']=resolver.execute(plan['resolution_id'])
            elif any(c['series_id']=='eur' for f in plan['fields'] for c in f['candidates']):
                choices={f['role']:('eur' if any(c['series_id']=='eur' for c in f['candidates']) else f['candidates'][0]['series_id']) for f in plan['fields'] if f['candidates']}
                if not plan['requires_note']:
                    chosen=resolver.confirm(plan['resolution_id'],choices);item['selected_result']=resolver.execute(chosen['resolution_id'])
            report.append(item)
            print(json.dumps(item,ensure_ascii=False),flush=True)
            (ROOT/('results/semantic-calculation-live.json' if '--calculation-only' in sys.argv else 'results/semantic-model-live.json')).write_text(json.dumps(report,ensure_ascii=False,indent=2))
if __name__=='__main__':main()
