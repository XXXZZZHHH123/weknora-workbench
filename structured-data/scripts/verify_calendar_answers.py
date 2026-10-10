#!/usr/bin/env python3
"""Grade the calendar clarification replay against the original saved workbook.

Read-only; expected values and month coverage come from openpyxl, not the
structured store or the model. Run with the bundled Python dependency runtime.
"""
import argparse
import datetime as dt
import hashlib
import json
import math
from pathlib import Path

import openpyxl


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('results',type=Path);parser.add_argument('workbook',type=Path)
    args=parser.parse_args()
    workbook=openpyxl.load_workbook(args.workbook,read_only=True,data_only=True)
    original=[]
    for n,row in enumerate(workbook['中国棉花平衡表'].iter_rows(max_col=4,values_only=True),1):
        if isinstance(row[0],(dt.date,dt.datetime)):
            original.append({'date':row[0].strftime('%Y-%m-%d'),'balance.primary.C':row[2],'balance.primary.D':row[3],'row':n})
    workbook.close();checks=[]
    for path in sorted(args.results.glob('C[0-9]*.json')):
        record=json.loads(path.read_text());plans=[];issues=[]
        if record.get('status')!='completed_ungraded':issues.append('Chat did not finish')
        for event in record.get('events',[]):
            if event.get('response_type')!='tool_result':continue
            output=(event.get('data') or {}).get('output','')
            try:plan=json.loads(output[output.index('{'):])
            except (ValueError,TypeError):continue
            if plan.get('kind')=='structured_field_resolution':plans.append(plan)
        if not plans:issues.append('No structured result')
        else:
            plan=plans[-1];answer=plan.get('answer',{})
            if record['id'] in ('C01','C03','C05','C08'):
                if plan['status']!='needs_clarification' or plan.get('clarification',{}).get('type')!='time_range':issues.append('Expected only a time-range clarification')
                if any(len(f['candidates'])!=1 for f in plan['fields']):issues.append('Unique fields needlessly made ambiguous')
            else:
                start,end=('2022-09-01','2023-08-31') if record['id']=='C09' else ('2022-01-01','2023-12-31')
                if plan['status']!='resolved' or answer.get('start_date')!=start or answer.get('end_date')!=end:issues.append('Clarification lost requested date range')
                selected=[r for r in original if start<=r['date']<=end]
                expected={sid:sum(r[sid] for r in selected if isinstance(r[sid],(int,float))) for sid in ('balance.primary.C','balance.primary.D')}
                if {s.get('series',{}).get('series_id') for s in answer.get('sources',{}).values()}!=set(expected):issues.append('Missing or incorrect source series')
                if not math.isclose(answer.get('value',float('nan')),sum(expected.values()),abs_tol=1e-7):issues.append('Calculated value differs from original workbook')
                for source in answer.get('sources',{}).values():
                    sid=source['series']['series_id']
                    if sid not in expected or not math.isclose(source.get('aggregate',float('nan')),expected[sid],abs_tol=1e-7):issues.append('Wrong source subtotal')
                    for row in source.get('rows',[]):
                        found=next((r for r in selected if r['date']==row['date']),None)
                        col='C' if sid=='balance.primary.C' else 'D'
                        if found is None or row['source_cell']!=col+str(found['row']) or row['date_cell']!='A'+str(found['row']) or row['value']!=found[sid]:issues.append('Row provenance mismatch')
                    coverage=source.get('coverage',{})
                    if coverage.get('valid_month_count')!=len(selected):issues.append('Actual month coverage differs from original')
                    missing=['2023-11','2023-12'] if end=='2023-12-31' else []
                    if coverage.get('expected_month_count')!=len(selected)+len(missing):issues.append('Requested month count wrong')
                    if coverage.get('missing_months')!=missing:issues.append('Missing months not disclosed')
                if end=='2023-12-31' and (answer.get('coverage_complete') is not False or '已记录月份小计' not in answer.get('summary','')):issues.append('Partial result presented as full two-year total')
        checks.append({'id':record['id'],'issues':issues,'passed':not issues,'field_model':record.get('field_model')})
    expected_ids={f'C{i:02d}' for i in range(1,10)}
    if {c['id'] for c in checks}!=expected_ids:raise ValueError('Calendar replay did not include all nine cases')
    result={'source_sha256':hashlib.sha256(args.workbook.read_bytes()).hexdigest(),'cases':checks,'passed':all(c['passed'] for c in checks)}
    (args.results/'calendar-verification.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
    print(json.dumps(result,ensure_ascii=False,indent=2))
    if not result['passed']:raise SystemExit(1)


if __name__=='__main__':main()
