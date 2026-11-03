"""Finite, application-owned explanations for detached judgment failures.

Never render exception text, provider bodies, issue messages or JSON pointers.
Only known validation codes and known schema fields can affect the explanation.
"""
from fastapi import HTTPException

STAGES = frozenset({'queued', 'selecting', 'analyzing', 'validating', 'unknown'})
FIELD_LABELS = {
    'contradiction': '多空逻辑', 'bull_claim': '多方观点', 'bear_claim': '空方观点',
    'bull_evidence': '多方依据', 'bear_evidence': '空方依据',
    'marginal_focus': '边际关注', 'cost_line': '成本线', 'pricing_power': '定价权',
    'time_dimension': '时间维度', 'survey_notes': '调研备注', 'whitelist': '指标范围',
    'paradigm': '研判范式', 'tone': '研究基调', 'tone_note': '基调说明',
    'thresholds': '阈值条件', 'falsifiers': '证伪条件', 'priority_rule': '条件优先级',
    'unresolved': '补充说明', 'binding_units': '指标单位', 'rule_quotes': '原文依据',
    'source_coverage': '原文覆盖', 'bindings': '指标绑定', 'variety': '品种代码', 'review_period_days': '复核周期',
    'written_at': '撰写日期', 'author': '作者',
}
MESSAGES = {
    'timeout': '研究分析超时，原文已保留，请稍后重新分析。',
    'output_limit': '模型输出达到长度上限，尚未生成完整研判；请精炼输出要求或更换模型后重试。',
    'selection_invalid': '模型推荐的指标未能与当前授权目录核对，请重新分析。',
    'source_coverage': '原文内容尚未完整核对，未生成可导入结果；原文已保留，请重新分析。',
    'field_schema': '研究内容的字段格式不符合要求，未生成可导入结果，请重新分析。',
    'number_validation': '规则数值尚未通过原文核验，未生成可导入结果，请重新分析。',
    'unit_validation': '指标单位与来源口径尚未通过核验，未生成可导入结果，请重新分析。',
    'period_validation': '规则的连续次数或统计周期尚未通过原文核验，请重新分析。',
    'quote_validation': '规则的完整原文依据尚未通过核验，请重新分析。',
    'relation_validation': '模型生成的条目关联不完整，未生成可导入结果，请重新分析。',
    'invalid_json': '模型未返回完整的结构化研判，原文已保留，请重新分析。',
    'validation_failed': '研究结果未通过校验，原文已保留，请重新分析。',
    'request_invalid': '分析资料或参数格式不正确，请核对后重新提交。',
    'auth_expired': '登录已过期，请重新登录后发起分析。',
    'permission_denied': '当前权限不能继续分析，请核对工作台和来源权限。',
    'resource_missing': '分析所需的来源或模型已不存在，请刷新后重新选择。',
    'source_changed': '来源、授权或研判版本已改变，请刷新后重新分析。',
    'capacity': '资料或目录超过本次分析容量，请缩小范围后重试。',
    'rate_limited': '模型请求受限，请稍后重试。',
    'service_unavailable': '模型服务暂时不可用，原文已保留，请稍后重试。',
    'analysis_failed': '研究分析未完成，原文已保留，请重新发起。',
}
ISSUE_REASONS = {
    'source_coverage': 'source_coverage', 'source_mapping_invalid': 'source_coverage',
    'field_schema': 'field_schema', 'document_schema': 'invalid_json',
    'invalid_document': 'validation_failed', 'document_capacity': 'capacity',
    'unresolved_capacity': 'capacity', 'condition_text_capacity': 'capacity',
    'unknown_series_id': 'selection_invalid',
    'rule_number_type': 'number_validation', 'number_precision': 'number_validation',
    'number_not_in_source': 'number_validation', 'rule_number_context': 'number_validation',
    'number_review_required': 'number_validation',
    'quote_quantity_boundary': 'quote_validation', 'unit_context': 'quote_validation',
    'exported_rule_mismatch': 'quote_validation', 'falsifier_source_mismatch': 'quote_validation',
    'unit_schema': 'unit_validation', 'unit_coverage': 'unit_validation',
    'unit_metadata_missing': 'unit_validation', 'unit_mismatch': 'unit_validation',
    'unit_confirmation_authority': 'unit_validation', 'unit_confirmation_schema': 'unit_validation',
    'unit_confirmation_scope': 'unit_validation', 'unit_confirmation_conflict': 'unit_validation',
    'unit_confirmation_required': 'unit_validation',
    'rule_periods': 'period_validation', 'rule_periods_context': 'period_validation',
    'periods_not_in_source': 'period_validation', 'period_frequency_mismatch': 'period_validation',
    'period_quote_boundary': 'period_validation', 'period_frequency': 'period_validation', 'rule_moving_average': 'period_validation',
    'moving_average_not_in_source': 'period_validation', 'moving_average_context': 'period_validation',
    'moving_average_frequency': 'period_validation',
    'quote_coverage': 'quote_validation', 'quote_not_in_source': 'quote_validation',
    'business_condition_quote': 'quote_validation', 'business_condition_schema': 'field_schema',
    'business_condition_kind': 'field_schema', 'business_condition_missing': 'field_schema',
    'business_condition_not_in_source': 'quote_validation', 'rule_id': 'relation_validation',
    'rule_id_duplicate': 'relation_validation', 'priority_reference': 'relation_validation',
    'cost_fact_not_allowed': 'field_schema', 'anchor_binding': 'selection_invalid',
}
# Exact application-authored strings only. Similar strings with appended secrets
# or provider diagnostics do not match and fall back to a finite explanation.
STRING_REASONS = {
    '连接模型服务超时，请检查网络或代理设置': 'timeout',
    '模型未给出有效的真实指标选择': 'selection_invalid',
    '模型输出达到长度上限，未生成完整研究逻辑；请精炼输出要求或更换模型后重试': 'output_limit',
    '模型没有返回完整有效的配置，请调整资料后重试': 'invalid_json',
    '研究品种代码格式无效': 'request_invalid',
    '研究逻辑请求格式无效': 'request_invalid',
}
STATUS_REASONS = {400: 'request_invalid', 401: 'auth_expired', 403: 'permission_denied',
    404: 'resource_missing', 409: 'source_changed', 413: 'capacity',
    422: 'validation_failed', 429: 'rate_limited', 503: 'service_unavailable'}


def field_category(path):
    """Translate only an existing schema prefix, never the dynamic suffix."""
    if not isinstance(path, str) or len(path) > 2048 or not path.startswith('/'):
        return None
    parts = path.split('/')[1:]
    if parts and parts[0] == 'judgment':
        parts = parts[1:]
    if not parts:
        return None
    field = 'unresolved' if parts[0] == 'unstructured' else parts[0]
    if field == 'contradiction' and len(parts) > 1 and parts[1] in {'bull', 'bear'}:
        if len(parts) > 2 and parts[2] in {'claim', 'evidence'}:
            return parts[1] + '_' + parts[2]
        return 'contradiction'
    return field if field in FIELD_LABELS else None


def safe_diagnostic(value):
    """Defence in depth for index storage/metadata; ignore malformed records."""
    if (not isinstance(value, dict) or set(value) != {'code', 'stage', 'fields'}
            or not isinstance(value.get('code'), str) or value['code'] not in MESSAGES
            or not isinstance(value.get('stage'), str) or value['stage'] not in STAGES
            or not isinstance(value.get('fields'), list) or len(value['fields']) > 4
            or any(not isinstance(field, str) or field not in FIELD_LABELS for field in value['fields'])
            or len(set(value['fields'])) != len(value['fields'])):
        return None
    return {'code': value['code'], 'stage': value['stage'], 'fields': list(value['fields'])}


def render_failure(diagnostic):
    safe = safe_diagnostic(diagnostic)
    if safe is None:
        return MESSAGES['analysis_failed']
    labels = [FIELD_LABELS[field] for field in safe['fields']]
    return ('、'.join(labels) + '：' if labels else '') + MESSAGES[safe['code']]


def failure_for(error, stage='unknown'):
    reason, fields = 'analysis_failed', []
    if isinstance(error, TimeoutError):
        reason = 'timeout'
    elif isinstance(error, HTTPException):
        reason = STATUS_REASONS.get(error.status_code, reason)
        detail = error.detail
        if error.status_code in {422, 503} and isinstance(detail, str):
            reason = STRING_REASONS.get(detail, reason)
        elif error.status_code == 422 and isinstance(detail, dict):
            wrapper = detail.get('code')
            if wrapper == 'number_precision':
                reason = 'number_validation'
            elif wrapper == 'judgment_validation' and isinstance(detail.get('issues'), list):
                reasons = []
                for issue in detail['issues'][:32]:
                    if not isinstance(issue, dict) or issue.get('severity') == 'warning':
                        continue
                    code = issue.get('code')
                    if not isinstance(code, str) or code not in ISSUE_REASONS:
                        continue
                    reasons.append(ISSUE_REASONS[code])
                    field = ('bindings' if ISSUE_REASONS[code] == 'selection_invalid'
                        else field_category(issue.get('path')))
                    if field is not None and field not in fields and len(fields) < 4:
                        fields.append(field)
                if reasons:
                    reason = reasons[0]
    diagnostic = {'code': reason, 'stage': stage if isinstance(stage, str) and stage in STAGES else 'unknown',
        'fields': fields}
    return {'message': render_failure(diagnostic), 'diagnostic': diagnostic}
