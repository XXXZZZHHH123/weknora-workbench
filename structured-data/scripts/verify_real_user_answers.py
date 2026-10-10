#!/usr/bin/env python3
"""Independent workbook evidence and explicit task obligations for the case bank.

Run with the bundled openpyxl runtime. Never uses the parser, store or resolver
for expected numbers. A clarification requires human semantic review; this
checker validates its source IDs, task constraints and absence of fake answers.
"""
import argparse
from collections import Counter
import datetime as dt
import json
import math
from pathlib import Path
import re
import openpyxl


def number(value):
    if isinstance(value,(int,float)) and not isinstance(value,bool):return float(value)
    if isinstance(value,str):
        try:return float(value.replace(',','').replace('%','').strip())
        except ValueError:pass
    return None


def main():
    parser=argparse.ArgumentParser();parser.add_argument('results',type=Path);parser.add_argument('truth',type=Path)
    args=parser.parse_args();truth=json.loads(args.truth.read_text());records=json.loads((args.results/'summary.json').read_text())
    workbook=openpyxl.load_workbook(truth['source'],read_only=True,data_only=True);sheets={}
    def cell(sheet,ref):
        if sheet not in sheets:sheets[sheet]=list(workbook[sheet].iter_rows(values_only=True))
        col,row=openpyxl.utils.cell.coordinate_from_string(ref)
        data=sheets[sheet];index=openpyxl.utils.column_index_from_string(col)-1
        return data[row-1][index] if row<=len(data) and index<len(data[row-1]) else None
    def equal(actual,expected):return actual is not None and expected is not None and math.isclose(float(actual),float(expected),rel_tol=1e-9,abs_tol=1e-7)
    expected_value={'U02':truth['stock_max'][0]['value']-truth['stock_min'][0]['value'],
       'U10':truth['settle_minus_close'],'U13':truth['settle_minus_close'],'U14':truth['settle_over_close_percent'],
       'U15':truth['day_change'],'U16':truth['inventory_ratio_percent'],
       'U28':truth['points']['settle01']['2023-09-22']['value']-truth['points']['settle05']['2023-09-22']['value'],
       'U42':truth['close_max'][0]['value']-truth['close_min'][0]['value'],
       'U61':truth['close_max'][0]['value']-truth['close_min'][0]['value'],
       'U62':truth['close_max'][0]['value']-truth['positive_close_min'][0]['value'],
       'U63':max(r['value'] for r in truth['september_close'])-min(r['value'] for r in truth['september_close']),
       'U70':sum(truth['balance_2022_23_sum'].values()),
       'U73':truth['points']['settle01']['2023-09-22']['value']-truth['points']['settle05']['2023-09-22']['value']}
    expected_aggregate={'U01':truth['stock_max'][0]['value'],'U17':truth['last5_mean'],'U18':truth['september_mean'],
       'U20':truth['positive_close_min'][0]['value'],'U21':truth['2023_days_gt17000'],'U30':truth['november_cumulative_entry_sum'],
       'U44':truth['last5_mean'],'U46':truth['positive_close_min'][0]['value'],'U47':truth['2023_days_gt17000'],
       'U48':truth['september_mean'],'U66':truth['last5_mean']}
    rows=[]
    for record in records:
        issues=[];id=record['id'];plan=record['plans'][-1] if record['plans'] else {};answer=plan.get('answer',{});sources=answer.get('sources',{})
        if record['status']!='completed_ungraded':issues.append('stream did not finish')
        if not plan:issues.append('no structured outcome')
        status=plan.get('status')
        if status not in ('resolved','needs_clarification','no_data') and not (id=='U38' and status=='query_unavailable'):issues.append('system failure: '+str(status))
        row_count=0
        for source in sources.values():
            if not isinstance(source,dict) or not source.get('series'):continue
            sheet=source['series']['sheet']
            for row in (source.get('window_rows') or source.get('rows',[])):
                ref=row.get('source_cell');value=row.get('value')
                if not ref:issues.append('missing source cell');continue
                original=number(cell(sheet,ref));row_count+=1
                if value is not None and not equal(value,original):issues.append('saved value mismatch: '+sheet+'/'+ref)
                if value is None and original is not None:issues.append('numeric cell reported missing: '+sheet+'/'+ref)
                if row.get('date_cell') and row.get('date'):
                    original_day=cell(sheet,row['date_cell'])
                    if isinstance(original_day,(dt.date,dt.datetime)) and original_day.strftime('%Y-%m-%d')!=row['date']:issues.append('date cell mismatch: '+sheet+'/'+row['date_cell'])
        if status=='needs_clarification':
            if answer:issues.append('unconfirmed alternatives produced an answer')
            if not plan.get('fields') and not plan.get('requires_note'):issues.append('no real choices or definition to clarify')
            if plan.get('planning_error'):issues.append('technical plan error presented as business ambiguity')
            if id in ('U06','U60') and ({f['op'] for f in plan['fields']}!={'max','min'} or len(plan['fields'])!=2):issues.append('extrema choice omitted an operation')
        elif status in ('resolved','no_data'):
            if id in expected_value and not equal(answer.get('value'),expected_value[id]):issues.append('requested calculation differs from independent original workbook')
            if id in expected_aggregate and (len(sources)!=1 or not equal(next(iter(sources.values())).get('aggregate'),expected_aggregate[id])):issues.append('aggregate differs from original workbook task scope')
            if id in ('U07','U64') and not any(equal(r.get('value'),truth['points']['settle01']['2009-01-06']['value']) and r.get('date')=='2009-01-06' for s in sources.values() for r in s.get('rows',[])):issues.append('wrong contract/date point')
            if id in ('U09','U41') and not any(r.get('date')==truth['latest']['close']['date'] and equal(r.get('value'),truth['latest']['close']['value']) for s in sources.values() for r in s.get('rows',[])):issues.append('latest point is an older copy')
            if id in ('U24','U72') and not any(r.get('date')==truth['latest']['spot']['date'] and equal(r.get('value'),truth['latest']['spot']['value']) for s in sources.values() for r in s.get('rows',[])):issues.append('spot latest source/date wrong')
            if id=='U04' and not any(r.get('date')=='2022-11-21' and r.get('value') is None and r.get('source_cell')=='E56' for s in sources.values() for r in s.get('rows',[])):issues.append('missing-date follow-up changed topic or filled zero')
            if id in ('U27','U74') and (status!='no_data' or answer.get('value') is not None or not any(s.get('valid_count')==1 and s['series']['series_id']=='futures.close.CF.CZC' for s in sources.values())):issues.append('basis missing input handling or source wrong')
            if id in ('U44','U66'):
                window=next(iter(sources.values())).get('window_rows',[]) if sources else []
                if [(r['date'],r['value']) for r in window]!=[(r['date'],r['value']) for r in truth['last5']]:issues.append('five trading-day evidence differs from original')
            if id in ('U19','U45') and [(r['date'],r['value']) for s in sources.values() for r in s.get('rows',[])]!=[(r['date'],r['value']) for r in truth['top3_close_records']]:issues.append('top three dates/values incomplete')
            if id in ('U33','U50','U68'):
                analysis=answer.get('analysis',{})
                if not equal(analysis.get('value'),truth['2023_price_load_pearson']) or analysis.get('sample_count')!=truth['2023_price_load_joint_count']:issues.append('Pearson inputs or joint dates differ from original workbook')
            if id in ('U32','U49','U67'):
                analysis=answer.get('analysis',{})
                if not equal(analysis.get('change'),truth['september_first_last_difference']) or not equal(analysis.get('max_drawdown_percent'),truth['september_max_drawdown_percent']) or analysis.get('sample_count')!=len(truth['september_close']):issues.append('trend/drawdown date scope incomplete')
            if id in ('U34','U51') and ('不能' not in answer.get('analysis',{}).get('notice','') or answer.get('analysis',{}).get('value') is not None):issues.append('stale history returned as future price')
            if id in ('U35','U52') and '不能' not in answer.get('analysis',{}).get('notice',''):issues.append('price series presented as causal proof')
            if id in ('U40','U75'):
                points=[(r['date'],r['value']) for s in answer.get('chart',{}).get('series',[]) for r in s['rows']]
                if points!=[(r['date'],r['value']) for r in truth['september_close']]:issues.append('chart omitted or source dates/values incomplete')
            if id=='U11' and (answer.get('count')!=2 or {g['metric'] for g in answer.get('groups',[])}!={'收盘价','结算价'}):issues.append('price definitions confused with contracts')
            if id=='U12':
                for group in answer.get('groups',[]):
                    if re.search(r'负荷|开机|订单|成交|上市|出库|入库|产量|需求|价格|报价',group['metric']) and not re.search(r'库存|结存|在库|仓单|储备量',group['metric']):issues.append('non-stock category in inventory directory')
                if not answer.get('unit_groups'):issues.append('missing original unit classification')
        category='failed' if issues else 'clarification_requires_semantic_review' if status=='needs_clarification' else 'correct_input_boundary' if id=='U38' else 'source_and_task_checks_passed'
        rows.append({'id':id,'category':category,'issues':issues,'checked_source_rows':row_count,'seconds':record['seconds']})
    workbook.close()
    result={'source_sha256':truth['sha256'],'expected_source':'original saved workbook values only','categories':dict(Counter(r['category'] for r in rows)),'cases':rows,'clarifications':'Human review still required to establish whether alternatives represent genuine business differences.'}
    (args.results/'verification.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
    print(json.dumps(result['categories'],ensure_ascii=False))
    for r in rows:
        if r['issues']:print(r['id'],r['issues'])


if __name__=='__main__':main()
