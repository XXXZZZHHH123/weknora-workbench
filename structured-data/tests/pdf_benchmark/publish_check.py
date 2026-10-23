"""Offline, authored-human-plan publication checks against real captured bundles.

No inference, network, service changes, production records, or golden mutation.
Fixture-specific plans/values live only in this isolated acceptance harness.
"""
from __future__ import annotations

import argparse
from decimal import Decimal
import json
from pathlib import Path
import sys

from benchmark import DEFAULT_ROOT, load_gold, dump
from score import pair_by_page_geometry, score_fixture

sys.path.insert(0,str(Path(__file__).resolve().parents[2]/'src'))
from structured_data.pdf_tables import PDFTableStore

CASES=('continued_three_pages','split_text_row','body_spans_refusal','different_units_blank_control')


def author_plan(draft, gold_tables, actual_tables, *, numeric_columns):
    ids=[f['id'] for f in actual_tables]
    width=len(gold_tables[0]['rows'][0])
    return dict(bundle_sha256=draft['bundle_sha256'],fragment_ids=ids,reviewed=True,
        review_note='Isolated synthetic acceptance: explicit authored operator plan, not an inferred business plan',
        header_rows={actual['id']:[dict(row=r,reason='Authored operator identifies source header row') for r in gold['header_rows']]
                     for gold,actual in zip(gold_tables,actual_tables)},
        columns=[dict(name=f'c{ci}',type='decimal' if ci in numeric_columns else 'text',
                      source_columns={ident:ci for ident in ids},
                      **({'unit':gold_tables[0]['unit']} if ci in numeric_columns and gold_tables[0].get('unit') else {}))
                 for ci in range(width)],boundary_merges=[],excluded_rows=[])


def sums_from_gold(gold_tables, numeric_columns):
    result={}
    for ci in numeric_columns:
        values=[Decimal(row[ci]) for table in gold_tables for ri,row in enumerate(table['rows'])
                if ri not in table['header_rows'] and row[ci]!='']
        result[f'c{ci}']=sum(values) if values else None
    return result


def query_sums(store,table_id,expected):
    names=list(expected)
    query=store.query(table_id,'SELECT '+','.join('sum("'+name+'") AS "sum_'+name+'"' for name in names)+' FROM data')
    got={name:None if value is None else Decimal(value) for name,value in zip(names,query['rows'][0])}
    if got!=expected:raise AssertionError('Query sum differs from independently authored decimal values')
    if not query['input_complete']:raise AssertionError('Complete selected-row input unexpectedly unavailable')
    if query['provenance']['sql_result_lineage_verified']:raise AssertionError('Aggregate lineage was invented')
    return dict(expected={k:None if v is None else str(v) for k,v in expected.items()},actual={k:None if v is None else str(v) for k,v in got.items()},
                input_count=query['input_count'],source=query['source'],provenance=query['provenance'])


def check_case(case,gold,bundle,store,out):
    extraction=score_fixture(gold,bundle)
    if not extraction['all_table_cells_exact']:
        return dict(fixture_id=case,status='not_accepted',reason='Raw extraction does not exactly match authored table cells/spans; no publication attempted',extraction_score=extraction)
    pairs,_=pair_by_page_geometry(gold['tables'],bundle['fragments'])
    golden=[pair[0] for pair in pairs];actual=[pair[1] for pair in pairs]
    draft=store.create_draft(bundle)
    if case=='body_spans_refusal':
        plan=author_plan(draft,golden,actual,numeric_columns=[2]);dump(out/(case+'.plan.json'),plan)
        try:store.publish(draft['draft_id'],plan)
        except ValueError as exc:
            if 'Merged data cell' not in str(exc):raise
            return dict(fixture_id=case,status='passed',publication_rejected=True,reason=str(exc),draft_id=draft['draft_id'],
                        expected_data_spans_preserved=True)
        raise AssertionError('Body merged cells were incorrectly published as scalar data')
    if case=='different_units_blank_control':
        ids=[];tables=[]
        for g,a in pairs:
            plan=author_plan(draft,[g],[a],numeric_columns=[1]);dump(out/(case+'.'+g['id']+'.plan.json'),plan)
            table=store.publish(draft['draft_id'],plan);ids.append(table['table_id'])
            if table['columns'][1].get('unit')!=g['unit']:raise AssertionError('Explicit authored unit was lost')
            if len(table['fragments'])!=1 or table['fragments'][0]['page']!=g['page']:raise AssertionError('Independent unit tables merged')
            tables.append(dict(table_id=table['table_id'],unit=table['columns'][1]['unit'],fragments=table['fragments'],
                               sums=query_sums(store,table['table_id'],sums_from_gold([g],[1])),
                               logical_row_count=table['coverage']['logical_row_count']))
        if len(set(ids))!=2:raise AssertionError('Different explicit plans shared a table identity')
        if tables[0]['logical_row_count']!=3 or tables[1]['logical_row_count']!=2:raise AssertionError('Authored blank row was dropped or added')
        return dict(fixture_id=case,status='passed',separate_unit_tables=tables,
                    authored_blank_row_preserved=True,business_merge_inference_executed=False)
    numeric=[1,2] if case=='continued_three_pages' else [1]
    plan=author_plan(draft,golden,actual,numeric_columns=numeric)
    if case=='split_text_row':
        op=gold['boundary_operations'][0]
        mapping={g['id']:a['id'] for g,a in pairs}
        plan['boundary_merges']=[dict(upper={'fragment_id':mapping[op['upper'][0]],'row':op['upper'][1]},
            lower={'fragment_id':mapping[op['lower'][0]],'row':op['lower'][1]},reason='Authored operator verified the split text row and blank lower numeric cell',
            cells=[dict(column='c'+str(c['column']),operation=c['operation'],**({'separator':c['separator']} if 'separator' in c else {})) for c in op['cells']])]
    dump(out/(case+'.plan.json'),plan)
    table=store.publish(draft['draft_id'],plan)
    sums=query_sums(store,table['table_id'],sums_from_gold(golden,numeric))
    if case=='split_text_row':
        merged=[r for r in table['rows'] if len(r['source_rows'])==2]
        if len(merged)!=1 or merged[0]['values']['c0']!='Northern distribution annex warehouse':raise AssertionError('Split text row not reconstructed exactly')
        pages=[s['page'] for s in merged[0]['source_cells']['c1']]
        if pages!=[1,2]:raise AssertionError('Split numeric provenance lost its blank lower-page span')
        if table['coverage']['logical_row_count']!=3 or table['coverage']['covered_data_source_row_count']!=4:raise AssertionError('Split row coverage changed')
    else:
        pages={s['page'] for r in table['rows'] for s in r['source_cells']['c1']}
        if pages!={1,2,3} or table['coverage']['logical_row_count']!=7:raise AssertionError('Continuation rows/pages are incomplete')
    dump(out/(case+'.table.json'),table)
    return dict(fixture_id=case,status='passed',table_id=table['table_id'],coverage=table['coverage'],sums=sums,
                page_context_scope=table.get('page_context_scope'),page_context_coverage=table.get('page_context_coverage'),
                source_pages=sorted({s['page'] for r in table['rows'] for s in r['source_cells']['c1']}))


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root',type=Path,default=DEFAULT_ROOT)
    parser.add_argument('--artifacts',type=Path,default=DEFAULT_ROOT/'runs/standard-full')
    parser.add_argument('--output',type=Path,required=True)
    args=parser.parse_args()
    gold=load_gold(args.root);selected={f['id']:f for f in gold['fixtures'] if f['id'] in CASES}
    missing=[case for case in CASES if not (args.artifacts/(case+'.bundle.json')).is_file()]
    if missing:
        print(json.dumps(dict(status='waiting_for_real_bundles',missing=missing)));return 2
    args.output.mkdir(parents=True,exist_ok=False);args.output.chmod(0o700)
    store=PDFTableStore(args.output/'isolated-store');results=[]
    for case in CASES:
        bundle=json.loads((args.artifacts/(case+'.bundle.json')).read_text())
        try:result=check_case(case,selected[case],bundle,store,args.output)
        except Exception as exc:result=dict(fixture_id=case,status='failed',exception_type=type(exc).__name__,error=str(exc))
        results.append(result)
    passed=all(r['status']=='passed' for r in results)
    report=dict(status='passed' if passed else 'not_accepted',checks=results,real_captured_bundles=True,
                authored_operator_plans=True,model_logical_review_executed=False,production_records_written=False,
                inference_started=False,full_business_acceptance=False)
    dump(args.output/'publication-checks.json',report)
    print(json.dumps(report,ensure_ascii=False,indent=2))
    return 0 if passed else 1


if __name__=='__main__':raise SystemExit(main())
