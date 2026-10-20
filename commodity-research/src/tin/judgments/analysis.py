"""Validate a model-authored independent judgment draft against actual KB IDs."""
from dataclasses import dataclass
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
    pass


@dataclass
class AnalysisResult:
    payload: JudgmentIn
    binding_units: dict
    rule_quotes: dict
    unresolved: list


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
    except (ValueError, TypeError, RecursionError):
        raise AnalysisError('研究逻辑包含无效数值或结构') from None
    if len(encoded) > OUTPUT_BYTES: raise AnalysisError('研究逻辑输出超过本次容量')
    bounded(document)
    fields = document['judgment']
    if not isinstance(fields, dict) or set(fields) - ALLOWED_FIELDS:
        raise AnalysisError('模型包含不允许的判断字段')
    for name in ('thresholds', 'falsifiers'):
        rules = fields.get(name, [])
        if not isinstance(rules, list) or len(rules) > 24: raise AnalysisError('研究规则超过本次容量')
        for rule in rules:
            if not isinstance(rule, dict): raise AnalysisError('研究规则格式无效')
            value = rule.get('value')
            if name == 'thresholds' and any(type(number) not in (int, float) for number in (value if isinstance(value, list) else [value])):
                raise AnalysisError('研究规则必须使用有限数字，不能使用布尔值或字符串')
            if not re.fullmatch(r'[A-Za-z][A-Za-z0-9_-]{0,63}', str(rule.get('id', ''))):
                raise AnalysisError('研究规则局部ID无效')
            if name == 'falsifiers' and (type(rule.get('consecutive_periods', 1)) is not int or not 1 <= rule.get('consecutive_periods', 1) <= 120):
                raise AnalysisError('证伪连续期数必须为1至120的整数')
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
        if not isinstance(cost, dict) or cost.get('value') is not None or cost.get('source') is not None:
            raise AnalysisError('实际成本值须从知识库读取，不能由模型填造')
        if not isinstance(cost.get('items', []), list): raise AnalysisError('成本条目格式无效')
        if any(not isinstance(item, dict) or item.get('value') is not None for item in cost.get('items', [])):
            raise AnalysisError('实际成本值须从知识库读取，不能由模型填造')
    unresolved = document['unresolved']
    if not isinstance(unresolved, list): raise AnalysisError('待确认项格式无效')
    try:
        payload = JudgmentIn.model_validate({**fields, 'variety': variety, 'author': actor,
            'written_at': written_at or date.today(), 'contradiction': contradiction,
            'source_note': original, 'unstructured': unresolved})
    except (ValidationError, TypeError, ValueError):
        raise AnalysisError('研究逻辑字段或规则格式无效，请调整资料后重试') from None
    if len(payload.whitelist) != len(set(payload.whitelist)):
        raise AnalysisError('指标白名单不能重复')
    if payload.priority_rule and len(payload.priority_rule.order) != len(set(payload.priority_rule.order)):
        raise AnalysisError('规则优先级不能重复')
    bounded({key: value for key, value in payload.model_dump(mode='python').items() if key in ALLOWED_FIELDS})
    known = {row['id']: row for row in catalog}
    wanted = {sid for _, _, sid in _refs(payload)}
    units = document['binding_units']
    if (not isinstance(units, dict) or set(units) != wanted or any(sid not in known for sid in wanted)
            or any(not isinstance(unit, str) or unit != known[sid].get('unit') for sid, unit in units.items())):
        raise AnalysisError('指标不存在、绑定单位不一致或单位确认不完整')
    for falsifier in payload.falsifiers:
        if falsifier.unit_confirmed != units[falsifier.series_id]:
            raise AnalysisError('证伪条件的单位与知识库原单位不一致')
    quotes = document['rule_quotes']
    identifiers = {rule.id for rule in [*payload.thresholds, *payload.falsifiers]}
    if (not isinstance(quotes, dict) or set(quotes) != identifiers or
            any(not isinstance(quote, str) or not quote.strip() or quote not in original for quote in quotes.values())):
        raise AnalysisError('阈值或证伪条件缺少完整原文中的明确依据')
    for rule in payload.thresholds:
        values = rule.value if isinstance(rule.value, list) else [rule.value]
        source_numbers = quoted_quantities(quotes[rule.id])
        if any(Decimal(str(value)) not in source_numbers for value in values):
            raise AnalysisError('阈值数字无法从原文摘录核验；请将缺少明确数字的规则列为待确认项')
    for rule in payload.falsifiers:
        match = EXPRESSION.fullmatch(rule.expression)
        source_numbers = quoted_quantities(quotes[rule.id])
        if not match or Decimal(match.group(3)) not in source_numbers or (
                rule.consecutive_periods > 1 and Decimal(rule.consecutive_periods) not in source_numbers):
            raise AnalysisError('证伪数字或连续期数无法从原文摘录核验；请列为待确认项')
    return AnalysisResult(payload, units, quotes,
                          [item.model_dump() for item in payload.unstructured])
