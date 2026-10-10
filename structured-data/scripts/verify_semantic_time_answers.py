#!/usr/bin/env python3
"""Independently grade semantic time replays against saved Excel cells.

Expected question scopes belong to the test bank, never application logic.
All source values, missing months and cell coordinates are read from Excel.
"""
import argparse
import datetime as dt
import hashlib
import json
import math
from pathlib import Path

import openpyxl


def months(start,end):
    a,b=dt.date.fromisoformat(start),dt.date.fromisoformat(end)
    return [f'{n//12:04d}-{n%12+1:02d}' for n in range(a.year*12+a.month-1,b.year*12+b.month)]


def main():
    p=argparse.ArgumentParser()
    p.add_argument('results',type=Path);p.add_argument('workbook',type=Path)
    p.add_argument('--cases',type=Path,default=Path(__file__).resolve().parents[1]/'tests/semantic_time_cases_20261010.json')
    args=p.parse_args();bank=json.loads(args.cases.read_text())
    wb=openpyxl.load_workbook(args.workbook,read_only=True,data_only=True)
    rows=[]
    for n,row in enumerate(wb['中国棉花平衡表'].iter_rows(max_col=4,values_only=True),1):
        if isinstance(row[0],(dt.date,dt.datetime)):
            rows.append(dict(date=row[0].strftime('%Y-%m-%d'),row=n,values={'balance.primary.C':row[2],'balance.primary.D':row[3]}))
    wb.close();checks=[]
    for case in [c for g in bank['groups'] for c in g['cases']]:
        path=args.results/(case['id']+'.json');issues=[];detail={}
        if not path.exists():checks.append(dict(id=case['id'],passed=False,issues=['Missing replay']));continue
        record=json.loads(path.read_text());plans=[]
        for event in record.get('events',[]):
            if event.get('response_type')!='tool_result':continue
            output=(event.get('data') or {}).get('output','')
            try:plan=json.loads(output[output.index('{'):])
            except (ValueError,TypeError):continue
            if plan.get('kind')=='structured_field_resolution':plans.append(plan)
        if record.get('status')!='completed_ungraded':issues.append('Chat did not finish')
        if not plans:issues.append('No structured resolution')
        else:
            plan=plans[-1];answer=plan.get('answer',{});scope=plan.get('task_requirements',{}).get('time_scope',{})
            detail.update(scope=scope,status=plan['status'])
            if case.get('expect')=='clarification':
                if plan['status']!='needs_clarification' or plan.get('clarification',{}).get('type')!='time_range':issues.append('Expected time-only clarification')
                if any(len(f['candidates'])!=1 for f in plan.get('fields',[])):issues.append('Unnecessary field selection')
                if answer:issues.append('Calculated before unresolved scope was answered')
                if '2021/23' in case['question'] and '两个完整自然年' in plan['question']:issues.append('Wrong fixed two-year question')
                if '两年整年的' in plan.get('clarification',{}).get('placeholder',''):issues.append('Canned answer injected into placeholder')
            else:
                start,end=case['expected_start'],case['expected_end']
                if plan['status']!='resolved' or answer.get('start_date')!=start or answer.get('end_date')!=end:issues.append('Executed scope differs from user request')
                if scope.get('start_date')!=start or scope.get('end_date')!=end:issues.append('Semantic scope differs from request')
                selected=[r for r in rows if start<=r['date']<=end]
                expected={sid:sum(r['values'][sid] for r in selected if isinstance(r['values'][sid],(int,float))) for sid in ('balance.primary.C','balance.primary.D')}
                expected_months=months(start,end)
                detail.update(expected_value=sum(expected.values()),actual_value=answer.get('value'),requested_months=len(expected_months),source_rows=[r['row'] for r in selected])
                if not math.isclose(answer.get('value',float('nan')),sum(expected.values()),abs_tol=1e-7):issues.append('Total differs from saved Excel')
                sources=answer.get('sources',{})
                if {s.get('series',{}).get('series_id') for s in sources.values()}!=set(expected):issues.append('Wrong or missing source series')
                incomplete=False
                for source in sources.values():
                    sid=source.get('series',{}).get('series_id')
                    if sid not in expected:continue
                    if not math.isclose(source.get('aggregate',float('nan')),expected[sid],abs_tol=1e-7):issues.append('Wrong source subtotal')
                    valid=[r for r in selected if isinstance(r['values'][sid],(int,float))]
                    actual_months={r['date'][:7] for r in valid};missing=sorted(set(expected_months)-actual_months)
                    incomplete|=bool(missing)
                    if source.get('valid_count')!=len(valid):issues.append('Wrong source sample count')
                    for row in source.get('rows',[]):
                        original=next((r for r in selected if r['date']==row['date']),None)
                        col=sid.rsplit('.',1)[-1]
                        if original is None or row['source_cell']!=col+str(original['row']) or row['date_cell']!='A'+str(original['row']) or row['value']!=original['values'][sid]:issues.append('Cell provenance mismatch')
                    coverage=source.get('coverage',{})
                    if coverage.get('expected_month_count')!=len(expected_months) or coverage.get('missing_months')!=missing:issues.append('Missing-month coverage differs from Excel')
                if incomplete and (answer.get('coverage_complete') is not False or '已记录月份小计' not in answer.get('summary','')):issues.append('Partial observations presented as full total')
                if not incomplete and answer.get('coverage_complete') is False:issues.append('Complete scope incorrectly marked incomplete')
        checks.append(dict(id=case['id'],passed=not issues,issues=issues,detail=detail,field_model=record.get('field_model')))
    result=dict(source_sha256=hashlib.sha256(args.workbook.read_bytes()).hexdigest(),passed=all(c['passed'] for c in checks),cases=checks)
    (args.results/'semantic-time-verification.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
    print(json.dumps({'passed':result['passed'],'cases':[{'id':c['id'],'passed':c['passed'],'issues':c['issues']} for c in checks]},ensure_ascii=False,indent=2))
    if not result['passed']:raise SystemExit(1)


if __name__=='__main__':main()
