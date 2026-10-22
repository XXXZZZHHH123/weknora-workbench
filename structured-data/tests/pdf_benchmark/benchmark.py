"""Explicit sequential bridge capture and offline scoring for authored PDF cases."""
from __future__ import annotations

import argparse
import asyncio
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import sys
import time

from score import score_fixture

DEFAULT_ROOT=Path('/mnt/weknora-storage/weknora-dev/private/pdf-complex-20261022')


def sha(path):return hashlib.sha256(path.read_bytes()).hexdigest()


def dump(path,data):
    path.write_text(json.dumps(data,ensure_ascii=False,indent=2)+'\n')
    path.chmod(0o600)


def load_gold(root):
    gold=json.loads((root/'golden.json').read_text())
    if gold.get('schema_version')!='authored_pdf_benchmark_v1':raise ValueError('Unknown authored gold version')
    for f in gold['fixtures']:
        path=root/f['file']
        if path.resolve().parent!=root.resolve() or sha(path)!=f['sha256']:
            raise ValueError('Authored source PDF path/hash mismatch')
    return gold


def score_saved(gold,artifacts):
    scores=[]
    for fixture in gold['fixtures']:
        path=artifacts/(fixture['id']+'.bundle.json')
        if not path.is_file():continue
        score=score_fixture(fixture,json.loads(path.read_text()))
        dump(artifacts/(fixture['id']+'.score.json'),score)
        scores.append(score)
    summary=dict(gold_schema=gold['schema_version'],scored_fixture_count=len(scores),
                 table_cells_exact=sum(s['cells_exact'] for s in scores),table_cells_expected=sum(s['cells_expected'] for s in scores),
                 formula_structures_exact=sum(s['formulas_exact'] for s in scores),formula_structures_expected=sum(s['formulas_expected'] for s in scores),
                 structural_exact_fixture_count=sum(s['table_structure_exact'] for s in scores),
                 acceptance_complete=False,logical_review_executed=False,store_publication_executed=False,
                 scores=scores)
    dump(artifacts/'scores.json',summary)
    return summary


async def run(args,gold,artifacts):
    if not args.server_config or not args.url:
        raise ValueError('run requires --server-config private JSON and --url; no API key CLI flags')
    private=Path(args.server_config)
    if private.stat().st_size>16384:raise ValueError('Private server configuration exceeds budget')
    api_key=json.loads(private.read_text()).get('api_key')
    if not isinstance(api_key,str) or not api_key.strip():raise ValueError('Server configuration has no bearer key')
    # Import only at explicit run boundary. Generation/scoring never infer or
    # contact the service, and this module does not start/manage containers.
    src=Path(__file__).resolve().parents[2]/'src'
    sys.path.insert(0,str(src))
    from structured_data.pdf_parser_bridge import ParserConfig,parse_pdf
    cfg=ParserConfig('mineru_v1',args.url,api_key=api_key,tier=args.tier,
                     timeout_seconds=args.timeout,max_polls=100,poll_interval_seconds=min(30,max(1,args.timeout/99)))
    selected=[f for f in gold['fixtures'] if not args.fixtures or f['id'] in args.fixtures]
    if not selected or (args.fixtures and set(args.fixtures)-{f['id'] for f in selected}):
        raise ValueError('Unknown or empty fixture selection')
    results=[]
    for fixture in selected:
        start=time.monotonic()
        try:
            bundle=await parse_pdf(args.root/fixture['file'],source={'sha256':fixture['sha256'],'file_name':fixture['file']},config=cfg)
            dump(artifacts/(fixture['id']+'.raw.json'),bundle.get('raw',{}))
            dump(artifacts/(fixture['id']+'.bundle.json'),bundle)
            score=score_fixture(fixture,bundle)
            dump(artifacts/(fixture['id']+'.score.json'),score)
            result=dict(fixture_id=fixture['id'],transport_completed=True,elapsed_seconds=round(time.monotonic()-start,3),
                        table_structure_exact=score['table_structure_exact'],cell_accuracy=score['cell_accuracy'],
                        formula_structure_exact=score['formula_structure_exact'],acceptance_complete=False)
        except Exception as exc:
            # Do not persist arbitrary HTTP errors that may contain auth values.
            result=dict(fixture_id=fixture['id'],transport_completed=False,elapsed_seconds=round(time.monotonic()-start,3),
                        exception_type=type(exc).__name__,error='Bridge capture failed; inspect parser private job logs.',acceptance_complete=False)
        results.append(result)
        dump(artifacts/'run-status.json',dict(tier=args.tier,results=results,acceptance_complete=False))
        print(json.dumps(result),flush=True)
        if not result['transport_completed'] and not args.continue_on_error:break
    score_saved(gold,artifacts)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command',choices=('inventory','run','score'))
    parser.add_argument('--root',type=Path,default=DEFAULT_ROOT)
    parser.add_argument('--artifacts',type=Path)
    parser.add_argument('--server-config',type=Path)
    parser.add_argument('--url')
    parser.add_argument('--tier',choices=('basic','standard'),default='standard')
    parser.add_argument('--timeout',type=int,default=1800)
    parser.add_argument('--fixtures',nargs='+')
    parser.add_argument('--continue-on-error',action='store_true')
    args=parser.parse_args()
    gold=load_gold(args.root)
    if args.command=='inventory':
        print(json.dumps(dict(fixtures=[dict(id=f['id'],pages=f['page_count'],sha256=f['sha256']) for f in gold['fixtures']],
                              pages=gold['page_count'],inference_run=False),indent=2));return
    if args.command=='score':
        if not args.artifacts:raise ValueError('score requires --artifacts containing saved bundles')
        print(json.dumps({k:v for k,v in score_saved(gold,args.artifacts).items() if k!='scores'}));return
    artifacts=args.artifacts or args.root/'runs'/datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
    artifacts.mkdir(parents=True,exist_ok=False)
    os.chmod(artifacts,0o700)
    dump(artifacts/'golden.copy.json',gold)
    asyncio.run(run(args,gold,artifacts))


if __name__=='__main__':main()
