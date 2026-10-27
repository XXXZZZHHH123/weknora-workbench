"""Validate a model-authored independent judgment draft against actual KB IDs."""
from dataclasses import dataclass
from copy import deepcopy
from datetime import date
from decimal import Decimal, DecimalException, localcontext
import json
import math
import re
import unicodedata
from pydantic import ValidationError
from tin.schemas.judgment import EXPRESSION, JudgmentIn
from tin.judgments.service import _refs

SOURCE_BYTES = 48 * 1024
OUTPUT_BYTES = 96 * 1024
ALLOWED_FIELDS = frozenset({'contradiction', 'marginal_focus', 'cost_line', 'pricing_power',
    'time_dimension', 'survey_notes', 'whitelist', 'paradigm', 'tone', 'tone_note',
    'thresholds', 'priority_rule', 'falsifiers'})


class AnalysisError(ValueError):
    """A bounded, field-addressable error; never includes provider diagnostics."""
    def __init__(self, message, *, code='invalid_document', path='/', issues=None):
        super().__init__(message)
        self.issues = issues or [{'code': code, 'path': path, 'message': message}]


def _error(message, code, path):
    raise AnalysisError(message, code=code, path=path)


@dataclass
class AnalysisResult:
    payload: JudgmentIn
    binding_units: dict
    rule_quotes: dict
    unresolved: list


@dataclass
class DraftAssessment:
    document: dict
    result: AnalysisResult
    issues: list


def bounded(value, depth=0):
    if depth > 10:
        raise AnalysisError('研究逻辑结构过深')
    if isinstance(value, str) and len(value) > 4000:
        raise AnalysisError('单项研究说明过长')
    if isinstance(value, float) and not math.isfinite(value):
        raise AnalysisError('研究规则必须使用有限数值')
    if isinstance(value, list):
        if len(value) > 32: raise AnalysisError('研究条目超过本次容量')
        for item in value: bounded(item, depth + 1)
    elif isinstance(value, dict):
        if len(value) > 128: raise AnalysisError('研究字段超过本次容量')
        for key, item in value.items():
            if not isinstance(key, str) or len(key) > 128: raise AnalysisError('研究字段名无效')
            if key == 'series_id' and item == '': raise AnalysisError('未绑定指标请使用null，不能使用空ID')
            bounded(item, depth + 1)


# This is numeric source verification, never business-intent classification.
QUANTITY = re.compile(r"(?<![0-9A-Za-z_.,])([+-]?(?:[0-9]{1,3}(?:,[0-9]{3})+(?:\.[0-9]+)?|[0-9]+(?:\.[0-9]+)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?)(?![0-9,.])\s*(万亿|亿|万|千)?")
SCALES = {None: Decimal(1), '千': Decimal(1000), '万': Decimal(10000),
          '亿': Decimal(100000000), '万亿': Decimal(1000000000000)}


def quoted_quantities(quote):
    normalized = unicodedata.normalize('NFKC', quote).replace('−', '-')
    values = set()
    for match in QUANTITY.finditer(normalized):
        try:
            literal = match.group(1).replace(',', '')
            if len(literal) > 128: continue
            with localcontext() as context:
                context.prec = 128
                number = Decimal(literal) * SCALES[match.group(2)]
            if number.is_finite(): values.add(number)
        except DecimalException:
            continue
    return values


def validate_analysis(document, original, catalog, *, variety, actor, written_at=None):
    if not isinstance(original, str) or not original.strip() or len(original.encode('utf-8')) > SOURCE_BYTES:
        raise AnalysisError('请提供48 KiB以内的完整研究逻辑文本')
    if not isinstance(document, dict) or set(document) != {'judgment', 'binding_units', 'rule_quotes', 'unresolved'}:
        raise AnalysisError('模型未返回完整的研究逻辑协议')
    try:
        encoded = json.dumps(document, ensure_ascii=False, allow_nan=False).encode('utf-8')
    except (ValueError, TypeError, UnicodeError, RecursionError):
        raise AnalysisError('研究逻辑包含无效数值或结构') from None
    if len(encoded) > OUTPUT_BYTES: raise AnalysisError('研究逻辑输出超过本次容量')
    bounded(document)
    fields = document['judgment']
    if not isinstance(fields, dict) or set(fields) - ALLOWED_FIELDS:
        raise AnalysisError('模型包含不允许的判断字段')
    for name in ('thresholds', 'falsifiers'):
        rules = fields.get(name, [])
        if not isinstance(rules, list) or len(rules) > 24: raise AnalysisError('研究规则超过本次容量')
        for index, rule in enumerate(rules):
            path = f'/judgment/{name}/{index}'
            if not isinstance(rule, dict): raise AnalysisError('研究规则格式无效')
            value = rule.get('value')
            if name == 'thresholds' and any(type(number) not in (int, float) for number in (value if isinstance(value, list) else [value])):
                _error('研究规则必须使用有限数字，不能使用布尔值或字符串', 'rule_number_type', path + '/value')
            if not re.fullmatch(r'[A-Za-z][A-Za-z0-9_-]{0,63}', str(rule.get('id', ''))):
                _error('研究规则局部ID无效', 'rule_id', path + '/id')
            if name == 'falsifiers' and (type(rule.get('consecutive_periods', 1)) is not int or not 1 <= rule.get('consecutive_periods', 1) <= 120):
                _error('证伪连续期数必须为1至120的整数', 'rule_periods', path + '/consecutive_periods')
    contradiction = fields.get('contradiction', {})
    if not isinstance(contradiction, dict): raise AnalysisError('主矛盾结构无效')
    for side in ('bull', 'bear'):
        data = contradiction.get(side, {})
        if not isinstance(data, dict): raise AnalysisError('多空结构无效')
        if not isinstance(data.get('evidence', []), list): raise AnalysisError('研究证据格式无效')
        for evidence in data.get('evidence', []):
            if not isinstance(evidence, dict) or any(evidence.get(key) is not None for key in ('value', 'as_of', 'source')):
                raise AnalysisError('模型不能填入未经核验的实际观测值或来源')
    cost = fields.get('cost_line')
    if cost is not None:
        if not isinstance(cost, dict):
            raise AnalysisError('实际成本值须从知识库读取，不能由模型填造')
        if not isinstance(cost.get('items', []), list): raise AnalysisError('成本条目格式无效')
        if any(not isinstance(item, dict) for item in cost.get('items', [])):
            raise AnalysisError('实际成本值须从知识库读取，不能由模型填造')
        forbidden_paths = [f'/judgment/cost_line/{key}' for key in ('value', 'source')
                           if cost.get(key) is not None]
        forbidden_paths.extend(f'/judgment/cost_line/items/{index}/value'
                               for index, item in enumerate(cost.get('items', []))
                               if item.get('value') is not None)
        if forbidden_paths:
            message = '实际成本值须从知识库读取，不能由模型填造'
            raise AnalysisError(message, issues=[{'code': 'cost_fact_not_allowed', 'path': path,
                'message': message + '；此字段必须为JSON null，不得把原文或目录当作已核验事实填写'}
                for path in forbidden_paths])
    unresolved = document['unresolved']
    if not isinstance(unresolved, list): raise AnalysisError('待确认项格式无效')
    try:
        payload = JudgmentIn.model_validate({**fields, 'variety': variety, 'author': actor,
            'written_at': written_at or date.today(), 'contradiction': contradiction,
            'source_note': original, 'unstructured': unresolved})
    except ValidationError as error:
        issues = [{'code': 'field_schema', 'path': '/judgment/' + '/'.join(map(str, item['loc'])),
                   'message': '字段类型、必填项或枚举不符合研究逻辑协议'}
                  for item in error.errors(include_url=False)[:16]]
        raise AnalysisError('研究逻辑字段或规则格式无效，请调整资料后重试', issues=issues) from None
    except (TypeError, ValueError):
        raise AnalysisError('研究逻辑字段或规则格式无效，请调整资料后重试') from None
    for index, item in enumerate(payload.unstructured):
        path = f'/unresolved/{index}'
        if item.business_condition is None:
            if item.rule_kind is not None:
                _error('业务条件类型必须同时提供完整原文中的逐字条件',
                       'business_condition_missing', path + '/business_condition')
        else:
            if not item.business_condition.strip() or item.business_condition not in original:
                _error('待观察业务条件必须是完整原文中的非空逐字摘录',
                       'business_condition_not_in_source', path + '/business_condition')
            if item.rule_kind is None:
                _error('待观察业务条件必须明确声明threshold或falsifier类型',
                       'business_condition_kind', path + '/rule_kind')
    if len(payload.whitelist) != len(set(payload.whitelist)):
        raise AnalysisError('指标白名单不能重复')
    if payload.priority_rule and len(payload.priority_rule.order) != len(set(payload.priority_rule.order)):
        raise AnalysisError('规则优先级不能重复')
    bounded({key: value for key, value in payload.model_dump(mode='python').items() if key in ALLOWED_FIELDS})
    known = {row['id']: row for row in catalog}
    wanted = {sid for _, _, sid in _refs(payload)}
    units = document['binding_units']
    if not isinstance(units, dict):
        _error('绑定单位必须是对象', 'unit_schema', '/binding_units')
    if wanted - set(known):
        _error('引用指标不属于本次已授权真实目录', 'unknown_series_id', '/judgment')
    if set(units) != wanted:
        _error('绑定单位必须覆盖且仅覆盖实际引用ID', 'unit_coverage', '/binding_units')
    quantitative = {rule.series_id for rule in [*payload.thresholds, *payload.falsifiers]}
    for sid, unit in units.items():
        expected = known[sid].get('unit')
        if sid in quantitative and (not isinstance(expected, str) or not expected.strip()):
            _error('量化规则引用指标的目录单位尚未确认', 'unit_metadata_missing', '/binding_units/' + sid)
        if unit != expected or (unit is not None and not isinstance(unit, str)):
            _error('绑定单位与知识库原单位不一致', 'unit_mismatch', '/binding_units/' + sid)
    for index, falsifier in enumerate(payload.falsifiers):
        if falsifier.unit_confirmed != units[falsifier.series_id]:
            _error('证伪条件的单位与知识库原单位不一致', 'unit_mismatch', f'/judgment/falsifiers/{index}/unit_confirmed')
    quotes = document['rule_quotes']
    identifiers = {rule.id for rule in [*payload.thresholds, *payload.falsifiers]}
    if not isinstance(quotes, dict) or set(quotes) != identifiers:
        _error('规则依据必须覆盖且仅覆盖全部量化规则', 'quote_coverage', '/rule_quotes')
    for identifier, quote in quotes.items():
        if not isinstance(quote, str) or not quote.strip() or quote not in original:
            _error('阈值或证伪条件缺少完整原文中的逐字依据', 'quote_not_in_source', '/rule_quotes/' + identifier)
    for index, rule in enumerate(payload.thresholds):
        values = rule.value if isinstance(rule.value, list) else [rule.value]
        source_numbers = quoted_quantities(quotes[rule.id])
        if any(Decimal(str(value)) not in source_numbers for value in values):
            _error('阈值数字无法从原文摘录核验；请将缺少明确数字的规则列为待确认项', 'number_not_in_source', f'/judgment/thresholds/{index}/value')
    for index, rule in enumerate(payload.falsifiers):
        match = EXPRESSION.fullmatch(rule.expression)
        source_numbers = quoted_quantities(quotes[rule.id])
        if not match or Decimal(match.group(3)) not in source_numbers or (
                rule.consecutive_periods > 1 and Decimal(rule.consecutive_periods) not in source_numbers):
            _error('证伪数字或连续期数无法从原文摘录核验；请列为待确认项', 'number_not_in_source', f'/judgment/falsifiers/{index}/expression')
    return AnalysisResult(payload, units, quotes,
                          [item.model_dump() for item in payload.unstructured])


def assess_draft(document, original, catalog, *, variety, actor, written_at=None):
    """Keep verified rules; explicitly quarantine invalid rules without making them executable.

    Envelope/identity/observation failures remain fatal. This is not a JSON repair
    parser: the caller must supply a complete, strictly decoded model document.
    """
    if (not isinstance(document, dict) or set(document) != {'judgment', 'binding_units', 'rule_quotes', 'unresolved'}
            or not isinstance(document.get('judgment'), dict)
            or not isinstance(document.get('binding_units'), dict)
            or not isinstance(document.get('rule_quotes'), dict)
            or not isinstance(document.get('unresolved'), list)):
        _error('模型未返回完整的研究逻辑对象协议', 'document_schema', '/')
    try:
        if len(json.dumps(document, ensure_ascii=False, allow_nan=False).encode()) > OUTPUT_BYTES:
            _error('研究逻辑输出超过本次容量', 'document_capacity', '/')
    except (TypeError, ValueError, UnicodeError, RecursionError):
        _error('研究逻辑包含无效数值或结构', 'document_schema', '/')
    bounded(document)
    known = {row['id']: row for row in catalog}
    fields, declared = document['judgment'], document['binding_units']
    issues = []

    def check_ids(value, path='/judgment'):
        if isinstance(value, dict):
            for key, child in value.items():
                if key == 'series_id' and child is not None and (not isinstance(child, str) or child not in known):
                    _error('引用指标不属于本次已授权真实目录', 'unknown_series_id', path + '/series_id')
                check_ids(child, path + '/' + key)
        elif isinstance(value, list):
            for index, child in enumerate(value): check_ids(child, path + '/' + str(index))

    check_ids(fields)
    if any(sid not in known for sid in declared):
        _error('单位声明包含未知指标ID', 'unknown_series_id', '/binding_units')
    whitelist = fields.get('whitelist', [])
    if not isinstance(whitelist, list) or any(not isinstance(sid, str) or sid not in known for sid in whitelist):
        _error('白名单包含未知指标ID或格式无效', 'unknown_series_id', '/judgment/whitelist')
    ids = []
    for kind in ('thresholds', 'falsifiers'):
        rules = fields.get(kind, [])
        if not isinstance(rules, list) or len(rules) > 24 or any(not isinstance(rule, dict) for rule in rules):
            _error('研究规则必须是本次容量内的对象数组', 'document_schema', '/judgment/' + kind)
        for index, rule in enumerate(rules):
            if not isinstance(rule.get('id'), str) or not re.fullmatch(r'[A-Za-z][A-Za-z0-9_-]{0,63}', rule['id']):
                _error('研究规则局部ID格式无效', 'rule_id', f'/judgment/{kind}/{index}/id')
        ids.extend(rule.get('id') for rule in rules if isinstance(rule.get('id'), str))
    if len(ids) != len(set(ids)):
        _error('阈值和证伪的局部ID不能重复', 'rule_id_duplicate', '/judgment')

    # Validate descriptive structure and fact protections independently of rules.
    skeleton = deepcopy(document)
    skeleton['judgment'].update(thresholds=[], falsifiers=[], priority_rule=None)
    skeleton['rule_quotes'] = {}
    try:
        descriptive = JudgmentIn.model_validate({**skeleton['judgment'], 'variety': variety, 'author': actor,
            'written_at': written_at or date.today(), 'source_note': original,
            'contradiction': skeleton['judgment'].get('contradiction', {}),
            'unstructured': skeleton['unresolved']})
    except ValidationError as error:
        raise AnalysisError('研究逻辑描述字段格式无效', issues=[{
            'code': 'field_schema', 'path': '/judgment/' + '/'.join(map(str, item['loc'])),
            'message': '描述字段类型或枚举不符合协议'} for item in error.errors(include_url=False)[:16]]) from None
    descriptive_ids = {sid for _, _, sid in _refs(descriptive)}
    skeleton['binding_units'] = {sid: known[sid].get('unit') for sid in descriptive_ids}
    for sid in descriptive_ids:
        expected = known[sid].get('unit')
        if sid not in declared or declared[sid] != expected:
            issues.append({'code': 'descriptive_unit_metadata', 'path': '/binding_units/' + sid,
                'message': '描述性引用仅采用目录原单位；模型的单位声明未采信，未据此计算', 'severity': 'warning'})
        if expected is None or expected == '':
            issues.append({'code': 'unit_metadata_missing', 'path': '/binding_units/' + sid,
                'message': '目录单位尚未确认；保留描述性引用，不允许据此执行量化规则', 'severity': 'warning'})
    validate_analysis(skeleton, original, catalog, variety=variety, actor=actor, written_at=written_at)

    valid = {'thresholds': [], 'falsifiers': []}
    for kind in valid:
        for index, rule in enumerate(fields.get(kind, [])):
            trial = deepcopy(skeleton)
            trial['judgment'][kind] = [rule]
            sid = rule.get('series_id')
            if sid is not None:
                trial['binding_units'].pop(sid, None)
                if sid in declared: trial['binding_units'][sid] = declared[sid]
            identifier = rule.get('id')
            if isinstance(identifier, str) and identifier in document['rule_quotes']:
                trial['rule_quotes'] = {identifier: document['rule_quotes'][identifier]}
            try:
                validate_analysis(trial, original, catalog, variety=variety, actor=actor, written_at=written_at)
            except AnalysisError as error:
                for issue in error.issues:
                    path = issue['path'].replace(f'/judgment/{kind}/0', f'/judgment/{kind}/{index}')
                    issues.append({**issue, 'path': path, 'rule_id': identifier if isinstance(identifier, str) else None,
                                   'severity': 'error'})
            else:
                valid[kind].append(deepcopy(rule))

    cleaned = deepcopy(document)
    # This path assesses a current model response, not a historical typed
    # import. Missing business metadata cannot acquire legacy display semantics.
    for item in cleaned['unresolved']:
        if isinstance(item, dict):
            item.setdefault('business_condition', None)
            item.setdefault('rule_kind', None)
    cleaned['judgment'].update(valid)
    priority = fields.get('priority_rule')
    if priority is not None:
        from tin.schemas.judgment import PriorityRule
        try:
            parsed = PriorityRule.model_validate(priority)
        except ValidationError:
            _error('规则优先级格式无效', 'field_schema', '/judgment/priority_rule')
        if len(parsed.order) != len(set(parsed.order)) or set(parsed.order) - {r.get('id') for r in fields.get('thresholds', [])}:
            _error('优先级引用不存在或重复的阈值', 'priority_reference', '/judgment/priority_rule/order')
        remaining = {rule['id'] for rule in valid['thresholds']}
        kept = [identifier for identifier in parsed.order if identifier in remaining]
        cleaned['judgment']['priority_rule'] = {**priority, 'order': kept} if kept else None
    final_ids = descriptive_ids | {r['series_id'] for rules in valid.values() for r in rules}
    extra = set(declared) - final_ids
    if extra:
        issues.append({'code': 'redundant_unit_declaration', 'path': '/binding_units',
                       'message': '已清理不再被最终草稿引用的单位声明', 'severity': 'normalized'})
    cleaned['binding_units'] = {sid: known[sid].get('unit') for sid in sorted(final_ids)}
    rule_ids = {r['id'] for rules in valid.values() for r in rules}
    cleaned['rule_quotes'] = {identifier: quote for identifier, quote in document['rule_quotes'].items() if identifier in rule_ids}

    # Every demotion remains visible. The complete original stays source_note;
    # candidate rule values remain explicitly non-executable in the audit preview.
    notes = []
    for kind, rules in valid.items():
        accepted = {rule['id'] for rule in rules}
        for index, rule in enumerate(fields.get(kind, [])):
            if rule.get('id') in accepted: continue
            related = [item for item in issues if item['path'].startswith(f'/judgment/{kind}/{index}') or item.get('rule_id') == rule.get('id')]
            quote = document['rule_quotes'].get(rule.get('id'))
            verified_quote = quote if isinstance(quote, str) and quote.strip() and quote in original else None
            source = verified_quote or '完整原文保留在研究逻辑来源中；候选引文未获逐字核验。'
            candidate = json.dumps(rule, ensure_ascii=False, separators=(',', ':'))
            notes.append({'section': f'{kind}[{index}]待确认',
                'text': ('原文依据：' + source + '\n未通过校验的模型候选（不执行）：' + candidate)[:4000],
                'reason': '；'.join(dict.fromkeys(item['message'] for item in related)) or '规则未通过逐项校验',
                'business_condition': verified_quote,
                'rule_kind': ('threshold' if kind == 'thresholds' else 'falsifier') if verified_quote else None})
    missing_units = [item for item in issues if item['code'] == 'unit_metadata_missing' and item['severity'] == 'warning']
    if missing_units:
        notes.append({'section': '描述性引用单位待确认', 'text': '\n'.join(item['path'] for item in missing_units)[:4000],
                      'reason': '这些真实指标仅作描述性引用；目录单位确认前不用于量化规则。'})
    if len(cleaned['unresolved']) + len(notes) > 32:
        _error('待确认条目超过32项，请缩小资料后重试；未静默丢失规则', 'unresolved_capacity', '/unresolved')
    cleaned['unresolved'].extend(notes)
    result = validate_analysis(cleaned, original, catalog, variety=variety, actor=actor, written_at=written_at)
    return DraftAssessment(cleaned, result, issues)
