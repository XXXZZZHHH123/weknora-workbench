"""Bounded facts retained across turns; no conversation or intent inference."""
from copy import deepcopy


def result_evidence(answer):
    evidence = {k:deepcopy(answer[k]) for k in (
        'intent','operation','date','start_date','end_date','period','status',
        'value','rounded_value','unit','expression','explanation','outputs','analysis',
        'coverage_complete','notice','scope_notice','resolution_id','summary','count','groups','unit_groups','file'
    ) if k in answer}
    evidence['sources'] = {}
    for role, source in answer.get('sources', {}).items():
        facts = {k:deepcopy(source[k]) for k in (
            'dataset_id','status','series','filters','source','aggregate','record_count',
            'scope_record_count','valid_count','missing_count','actual_first_date',
            'actual_last_date','valid_first_date','valid_last_date','coverage','warnings',
            'rows_are','extremum_count','truncated','source_semantics'
        ) if k in source}
        facts['rows'] = deepcopy(source.get('rows', [])[:20])
        facts['evidence_rows_truncated'] = len(source.get('rows', [])) > 20
        evidence['sources'][role] = facts
    return evidence
