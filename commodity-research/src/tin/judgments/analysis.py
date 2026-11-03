"""Validate a model-authored independent judgment draft against actual KB IDs."""
from dataclasses import dataclass
from copy import deepcopy
from datetime import date
from decimal import Decimal
import json
import math
import re
from pydantic import ValidationError
from tin.schemas.judgment import EXPRESSION, JudgmentIn, MovingAverageTarget
from tin.compute.moving_average import sma_source_window
from tin.judgments.service import _refs
from tin.judgments.numbers import (quoted_quantities, check_rule_number, _SCALAR, _arrays, _width,
    starts_chinese_scaled_quantity, chinese_quantity_continuation, observation_periods,
    period_matches_frequency, _COMPARISON_SUFFIXES, _logical_clause_boundary)
from tin.judgments.markdown_protocol import (
    exported_rules, quoted_rule_records, threshold_signature, falsifier_signature,
)

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


def _quote_continuation(original, end, quote, units):
    """Inspect only a quantity's adjoining token, never a later source amount."""
    tail = original[end:]
    if not tail:
        return ''
    units = {_width(unit).strip() for unit in units if isinstance(unit, str) and unit.strip()}
    units.update({'%', '‰', '‱'})
    normalized = _width(quote)
    following = _width(tail).lstrip(' \t')
    leading = len(tail) - len(tail.lstrip(' \t'))
    explicit = [unit for unit in units if normalized.rstrip().endswith(unit)]
    unit_prefix = [unit for unit in units if following.startswith(unit)]
    trimmed_end = end - (len(normalized) - len(normalized.rstrip()))
    extends_unit = any(len(actual) > len(short) and actual.startswith(short)
        and _width(original[trimmed_end - len(short):]).startswith(actual)
        for short in explicit for actual in units)
    if not extends_unit and _logical_clause_boundary(following):
        return ''
    if not extends_unit and unit_prefix:
        actual = max(unit_prefix, key=len)
        if _logical_clause_boundary(following[len(actual):]):
            # Retain the complete adjoining unit, then stop at punctuation;
            # numbers in another clause must never become evidence here.
            return tail[:leading + len(actual)]
    wrapped = following[:1] in {'(', '['} and (
        any(following[1:].lstrip().startswith(unit) for unit in units)
        or following[1:].lstrip()[:1].isascii() and following[1:].lstrip()[:1].isalpha())
    first = following[:1]
    clause = re.match(r'[^\r\n，。；;!?！？、]*', following[:256])[0]
    compound = any(char in clause for char in '/^*·×÷⁄∕')
    comparison = any(following.startswith(suffix) for suffix in _COMPARISON_SUFFIXES)
    # A bare number can inherit its selected catalog unit. Pure Chinese prose
    # does not prove an extra unit; model/user binding still owns that meaning.
    if not (explicit or unit_prefix or wrapped or compound or comparison or first and (
            first.isascii() and first.isalpha() or first.isnumeric()
            or first in '.,:+-/*^×÷⁄∕千万元亿')):
        return ''
    maximum = max(256, max(map(len, units), default=0) + 2)
    candidate = tail[:maximum]
    token = re.match(r'[ \t]*[^\s，。；;!?！？、"“”‘’()（）{}<>=]*', candidate)
    length = max(leading + 1, token.end())
    if compound or comparison:
        length = max(length, leading + len(clause))
    protected = []
    for unit in unit_prefix:
        protected.append((leading, leading + len(unit)))
        length = max(length, leading + len(unit) + 1)
    for short in explicit:
        start = trimmed_end - len(short)
        for unit in units:
            if len(unit) > len(short) and unit.startswith(short) and _width(original[start:]).startswith(unit):
                finish = start + len(unit) - end
                if finish > 0:
                    protected.append((0, finish))
                    length = max(length, finish + 1)
    raw = candidate[:min(length, maximum)]
    masked = list(raw)
    for index, char in enumerate(raw):
        if char.isnumeric() and not any(start <= index < finish for start, finish in protected):
            masked[index] = 'x'
    return ''.join(masked)


def _quote_quantity_context(original, quote, values, unit, units):
    needed = {Decimal(str(value)) for value in values}
    normalized = _width(quote)
    matches = list(_SCALAR.finditer(normalized))
    owner = matches[-1].start() if matches else None
    if owner is not None:
        owner = next((start for start, end in _arrays(normalized) if start <= owner < end), owner)
    offset, occurrences = 0, 0
    while (start := original.find(quote, offset)) >= 0:
        occurrences += 1
        if occurrences > 128:
            return False
        offset = start + 1
        if starts_chinese_scaled_quantity(quote, unit=unit):
            preceding = _width(original[:start]).rstrip()
            if preceding and chinese_quantity_continuation(preceding[-1]):
                return False
        prefix = _width(original[max(0, start - 1):start])
        if _SCALAR.match(normalized) and not _SCALAR.match(prefix + normalized, len(prefix)):
            return False
        continuation = _quote_continuation(original, start + len(quote), quote, units)
        if not needed <= quoted_quantities(prefix + quote + continuation, unit=unit, units=units):
            return False
        if owner is not None:
            suffix = quote[owner:]
            owned = needed & quoted_quantities(suffix, unit=unit, units=units)
            before = _width(original[max(0, start + owner - 1):start + owner])
            if not owned <= quoted_quantities(before + suffix + continuation, unit=unit, units=units):
                return False
    return bool(occurrences)


def _period_quote_context(original, quote, record):
    full = observation_periods(original)
    offset, occurrences = 0, 0
    while (start := original.find(quote, offset)) >= 0:
        occurrences += 1
        if occurrences > 128:
            return False
        offset = start + 1
        expected = {**record, 'start': start + record['start'], 'end': start + record['end']}
        if expected not in full:
            return False
    return bool(occurrences)


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
    candidate_document: dict | None = None
    candidate_history: list | None = None


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


def rule_frequency_issues(kind, rule, quote, source_frequency):
    """Report cadence requirements independently of unit/number failures.

    This is diagnostic only: it never supplies source metadata or validates a
    rule. Both full validation and review use the same count/cadence syntax.
    """
    from tin.compute.rule_caliber import rule_frequency, frequency_kind
    if not isinstance(rule, dict) or not isinstance(quote, str):
        return []
    try:
        frequency = rule_frequency(rule, source_frequency)
    except ValueError:
        return []  # The full validator reports conflicting declarations.
    if kind == 'thresholds':
        target = rule.get('value')
        window = target.get('window') if isinstance(target, dict) and target.get('kind') == 'sma' else None
        if type(window) is not int or not 2 <= window <= 120:
            return []
        daily = re.search(rf'(?<![0-9.+-]){window}\s*日\s*(?:简单移动平均线|移动平均线|均线|线)', quote)
        if daily and frequency_kind(frequency) != 'day':
            return [{'code': 'moving_average_frequency', 'field': 'value',
                'message': '原文要求日均线，所选指标未确认日频，请核对统计周期'}]
    elif kind == 'falsifiers':
        count = rule.get('consecutive_periods', 1)
        if type(count) is not int or not 2 <= count <= 120:
            return []
        records = [record for record in observation_periods(quote) if record['count'] == count]
        if records and not any(period_matches_frequency(record, frequency) for record in records):
            return [{'code': 'period_frequency', 'field': 'consecutive_periods',
                'message': '原文连续周期与所选指标的统计频率不一致或频率未确认，请核对日、周、月等口径。'}]
    return []


def validate_analysis(document, original, catalog, *, variety, actor, written_at=None, caliber_confirmations=None):
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
    # The model never supplies this authority. Only an explicit review mutation
    # can attach a server-authored declaration to one rule in this preview.
    from tin.schemas.judgment import CaliberConfirmation
    from tin.compute.rule_caliber import unit_present, rule_frequency
    known_units = {row['id']: row.get('unit') for row in catalog}
    known_frequencies = {row['id']: row.get('frequency') for row in catalog}
    local_units = {}
    for kind in ('thresholds', 'falsifiers'):
        for index, rule in enumerate(fields.get(kind, []) if isinstance(fields.get(kind, []), list) else []):
            if not isinstance(rule, dict) or rule.get('caliber_confirmation') is None:
                continue
            path = f'/judgment/{kind}/{index}/caliber_confirmation'
            supplied = rule['caliber_confirmation']
            if (not isinstance(caliber_confirmations, dict)
                    or caliber_confirmations.get(rule.get('id')) != supplied):
                _error('人工口径必须由当前用户在确认页面明确核对，不能由模型或导入稿代填', 'unit_confirmation_authority', path)
            try:
                declaration = CaliberConfirmation.model_validate(supplied)
            except ValueError:
                _error('人工口径的单位、依据或确认记录不完整', 'unit_confirmation_schema', path)
            sid = rule.get('series_id')
            if declaration.series_id != sid or declaration.confirmed_by != actor:
                _error('人工口径不属于当前用户和此指标绑定', 'unit_confirmation_scope', path)
            if unit_present(known_units.get(sid)) and known_units[sid] != declaration.unit:
                _error('人工口径不能覆盖来源已登记的不同单位', 'unit_mismatch', path)
            try:
                rule_frequency(rule, known_frequencies.get(sid))
            except ValueError:
                _error('人工统计频率不能覆盖来源已登记的不同口径，请核对本条规则', 'frequency_confirmation_conflict', path)
            if sid in local_units and local_units[sid] != declaration.unit:
                _error('同一研判版本的同一指标不能声明相互矛盾的单位', 'unit_confirmation_conflict', path)
            local_units[sid] = declaration.unit
    for kind in ('thresholds', 'falsifiers'):
        for index, rule in enumerate(fields.get(kind, []) if isinstance(fields.get(kind, []), list) else []):
            if (isinstance(rule, dict) and rule.get('series_id') in local_units
                    and not unit_present(known_units.get(rule.get('series_id')))
                    and rule.get('caliber_confirmation') is None):
                _error('此规则尚未人工确认缺失的来源单位；其他条目的声明不能代替此绑定',
                    'unit_metadata_missing', f'/judgment/{kind}/{index}')
    catalog = [{**row, 'unit': local_units.get(row['id'], row.get('unit'))} for row in catalog]
    for name in ('thresholds', 'falsifiers'):
        rules = fields.get(name, [])
        if not isinstance(rules, list) or len(rules) > 24: raise AnalysisError('研究规则超过本次容量')
        for index, rule in enumerate(rules):
            path = f'/judgment/{name}/{index}'
            if not isinstance(rule, dict): raise AnalysisError('研究规则格式无效')
            value = rule.get('value')
            moving = name == 'thresholds' and isinstance(value, dict) and value.get('kind') == 'sma'
            if moving:
                try:
                    MovingAverageTarget.model_validate(value)
                except ValueError:
                    _error('均线窗口必须是2至120的整数，且仅支持简单移动平均', 'rule_moving_average', path + '/value')
            if name == 'thresholds' and not moving and any(type(number) not in (int, float) for number in (value if isinstance(value, list) else [value])):
                _error('研究规则必须使用有限数字，不能使用布尔值或字符串', 'rule_number_type', path + '/value')
            if name == 'thresholds' and not moving:
                try:
                    for number in value if isinstance(value, list) else [value]:
                        check_rule_number(number)
                except ValueError:
                    _error('研究规则数值超出当前存储精度，未采用舍入值', 'number_precision', path + '/value')
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
        from tin.compute.rule_caliber import unit_present
        if sid in quantitative and not unit_present(expected):
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
    explicit = exported_rules(original)
    for index, rule in enumerate(payload.thresholds):
        moving = isinstance(rule.value, MovingAverageTarget)
        if moving:
            source_records = explicit['thresholds'].get(rule.series_id)
            signature = threshold_signature(rule.model_dump())
            quoted = quoted_rule_records(original, quotes[rule.id])
            if (source_records and not any(record[0] == signature and (record[1] is None or record[1] == units[rule.series_id]) for record in source_records)
                    or quoted is None or quoted and not any(kind == 'thresholds' and sid == rule.series_id
                        and record[0] == signature and (record[1] is None or record[1] == units[rule.series_id])
                        for kind, sid, record in quoted)):
                _error('均线规则与原文明确记录的指标、条件或周期不一致',
                       'exported_rule_mismatch', f'/judgment/thresholds/{index}')
            if not quoted and not sma_source_window(quotes[rule.id], rule.value.window, original):
                _error('均线周期没有完整的原文依据，请核对MA周期或原文均线条件',
                       'number_not_in_source', f'/judgment/thresholds/{index}/value')
            for issue in rule_frequency_issues('thresholds', rule.model_dump(), quotes[rule.id], known[rule.series_id].get('frequency')):
                _error(issue['message'], issue['code'], f'/judgment/thresholds/{index}/' + issue['field'])
            continue
        values = rule.value if isinstance(rule.value, list) else [rule.value]
        source_numbers = quoted_quantities(quotes[rule.id], unit=known[rule.series_id].get('unit'),
                                           units=[row.get('unit') for row in catalog])
        records = explicit['thresholds'].get(rule.series_id)
        signature = threshold_signature(rule.model_dump())
        quoted = quoted_rule_records(original, quotes[rule.id])
        matches_source = not records or any(source_signature == signature
            and (unit is None or unit == units[rule.series_id]) for source_signature, unit in records)
        matches_quote = quoted is not None and (
            not records and not quoted or any(kind == 'thresholds' and sid == rule.series_id
                and record[0] == signature and (record[1] is None or record[1] == units[rule.series_id])
                for kind, sid, record in quoted))
        if not matches_source or not matches_quote:
            _error('规则与引文所在行明确记录的指标、条件、区间或类型不一致，或引文多处定位不一致，未改写或执行',
                   'exported_rule_mismatch', f'/judgment/thresholds/{index}')
        if any(Decimal(str(value)) not in source_numbers for value in values):
            _error('阈值数字或单位无法从原文依据核验，请核对指标单位和原文条件', 'number_not_in_source', f'/judgment/thresholds/{index}/value')
        if not _quote_quantity_context(original, quotes[rule.id], values, known[rule.series_id].get('unit'),
                [row.get('unit') for row in catalog]):
            _error('引文可能截断了原文数字或单位，请核对完整条件及所选指标单位；该条件保留为待确认',
                   'unit_context', f'/judgment/thresholds/{index}/value')
    for index, rule in enumerate(payload.falsifiers):
        match = EXPRESSION.fullmatch(rule.expression)
        directional_zero = bool(match and match.group(1) == 'change' and Decimal(match.group(3)) == 0)
        source_numbers = quoted_quantities(quotes[rule.id], unit=known[rule.series_id].get('unit'),
                                           units=[row.get('unit') for row in catalog])
        records = explicit['falsifiers'].get(rule.series_id)
        signature = falsifier_signature(rule.model_dump())
        quoted = quoted_rule_records(original, quotes[rule.id])
        matches_source = not records or signature in records
        matches_quote = quoted is not None and (
            not records and not quoted or ('falsifiers', rule.series_id, signature) in quoted)
        if not matches_source or not matches_quote:
            _error('证伪条件与引文所在行明示的指标、比较方式、连续期数、单位或证伪对象不一致，或引文多处定位不一致，未执行',
                   'falsifier_source_mismatch', f'/judgment/falsifiers/{index}')
        if not match or not directional_zero and Decimal(match.group(3)) not in source_numbers:
            value = match.group(3) if match else rule.expression
            unit = known[rule.series_id].get('unit') or '单位未确认'
            _error(f'证伪数字{value}（{unit}）暂不能从原文依据核验。确认单位不能代替数值核对，请核对原文中的数量与倍率。',
                   'number_not_in_source', f'/judgment/falsifiers/{index}/expression')
        period_records = [record for record in observation_periods(quotes[rule.id])
            if record['count'] == rule.consecutive_periods] if rule.consecutive_periods > 1 else []
        if period_records:
            for issue in rule_frequency_issues('falsifiers', rule.model_dump(), quotes[rule.id], known[rule.series_id].get('frequency')):
                _error(issue['message'], issue['code'], f'/judgment/falsifiers/{index}/' + issue['field'])
            compatible = [record for record in period_records
                if period_matches_frequency(record, rule_frequency(rule.model_dump(), known[rule.series_id].get('frequency')))]
            if not any(_period_quote_context(original, quotes[rule.id], record) for record in compatible):
                _error('引文可能截断了连续次数或统计周期，请选取完整原文条件。',
                    'unit_context', f'/judgment/falsifiers/{index}/consecutive_periods')
        if rule.consecutive_periods > 1 and not period_records:
            # Preserve the precise truncation error, but never borrow a
            # measurement number as a count of observations.
            if (Decimal(rule.consecutive_periods) in quoted_quantities(quotes[rule.id])
                    and not _quote_quantity_context(original, quotes[rule.id], [rule.consecutive_periods], None, [])):
                _error('引文可能截断了连续次数或统计周期，请选取完整原文条件。',
                    'unit_context', f'/judgment/falsifiers/{index}/consecutive_periods')
            _error(f'原文依据暂不能核验连续{rule.consecutive_periods}期，请核对原文中的连续次数或统计周期。',
                   'number_not_in_source', f'/judgment/falsifiers/{index}/consecutive_periods')
        if not directional_zero and not _quote_quantity_context(original, quotes[rule.id], [Decimal(match.group(3))],
                known[rule.series_id].get('unit'), [row.get('unit') for row in catalog]):
            _error('引文可能截断了原文数字或单位，请核对完整条件及所选指标单位；该条件保留为待确认',
                   'unit_context', f'/judgment/falsifiers/{index}/expression')
    return AnalysisResult(payload, units, quotes,
                          [item.model_dump() for item in payload.unstructured])


def unique_condition_kinds(items):
    """Use only an exact, unambiguous classification from a validated inventory.

    Callers validate source quotations independently. Other kinds participate
    in ambiguity detection; a non-rule classification is not silently ignored.
    """
    claims = {}
    for item in items if isinstance(items, (list, tuple)) else ():
        if not isinstance(item, dict):
            continue
        quote = item.get('quote')
        if not isinstance(quote, str) or not quote.strip():
            continue
        kind = item.get('kind')
        claims.setdefault(quote, set()).add(kind if isinstance(kind, str) else None)
    return {quote: next(iter(kinds)) for quote, kinds in claims.items()
            if len(kinds) == 1 and next(iter(kinds)) in {'threshold', 'falsifier'}}


def _normalize_pending_types(document, original, condition_kinds, issues):
    """Keep literal untyped conditions as prose, never infer a rule from words."""
    from collections.abc import Mapping

    if not isinstance(original, str):
        return document
    result = document
    for index, item in enumerate(document['unresolved']):
        if not isinstance(item, dict) or item.get('rule_kind') is not None:
            continue
        condition = item.get('business_condition')
        if not isinstance(condition, str) or not condition.strip() or condition not in original:
            continue
        if result is document:
            result = deepcopy(document)
        target = result['unresolved'][index]
        kind = condition_kinds.get(condition) if isinstance(condition_kinds, Mapping) else None
        if isinstance(kind, str) and kind in {'threshold', 'falsifier'}:
            target['rule_kind'] = kind
            issues.append({'code': 'condition_type_completed', 'path': f'/unresolved/{index}/rule_kind',
                'message': '依据独立原文清单补全待核对条件类型；仅保留为文字，未生成量化规则。',
                'severity': 'normalized'})
            continue
        text = target.get('text')
        if not isinstance(text, str):
            # Invalid descriptive shapes still belong to the strict validator.
            continue
        if condition not in text:
            text += ('\n\n' if text else '') + condition
        if len(text) > 4000:
            _error('待核对说明与完整条件合并超过4000字，请精简说明；未截断原文条件',
                   'condition_text_capacity', f'/unresolved/{index}/text')
        target.update(text=text, business_condition=None, rule_kind=None)
        issues.append({'code': 'condition_type_pending', 'path': f'/unresolved/{index}/rule_kind',
            'message': '原文条件尚未明确是观察阈值还是证伪条件，已保留为待核对文字，当前不执行。',
            'severity': 'warning'})
    return result


def assess_draft(document, original, catalog, *, variety, actor, written_at=None, condition_kinds=None):
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
    issues = []
    original_candidate = deepcopy(document)
    # Model responses may spell an absent optional descriptive section as
    # JSON null. Its existing empty-object default carries no classification,
    # evidence, binding or number; normalize only that absence at this boundary.
    # Typed imports and every non-null invalid shape remain strict.
    if document['judgment'].get('pricing_power', {}) is None:
        document = deepcopy(document)
        document['judgment']['pricing_power'] = {}
        issues.append({'code': 'empty_description', 'path': '/judgment/pricing_power',
            'message': '未提供定价权说明，保留为空；未推断分类或补写研究观点', 'severity': 'normalized'})
    known = {row['id']: row for row in catalog}
    fields, declared = document['judgment'], document['binding_units']

    def check_ids(value, path='/judgment'):
        if isinstance(value, dict):
            for key, child in value.items():
                if key == 'series_id' and child is not None and (not isinstance(child, str) or child not in known):
                    _error('引用指标不属于本次已授权真实目录', 'unknown_series_id', path + '/series_id')
                if key == 'related_series_ids' and (not isinstance(child, list) or len(child) > 7
                        or any(not isinstance(sid, str) or sid not in known for sid in child)):
                    _error('附加指标不属于本次已授权真实目录或超过7项', 'unknown_series_id', path + '/related_series_ids')
                check_ids(child, path + '/' + key)
        elif isinstance(value, list):
            for index, child in enumerate(value): check_ids(child, path + '/' + str(index))

    check_ids(fields)
    check_ids(document['unresolved'], '/unresolved')
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

    # Only the model-draft boundary tolerates missing condition metadata. Typed
    # imports remain strict and only verified rules enter executable arrays.
    document = _normalize_pending_types(document, original, condition_kinds, issues)

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
            'message': '描述字段类型或枚举不符合协议', 'error_type': item['type']}
            for item in error.errors(include_url=False, include_input=False, include_context=False)[:16]]) from None
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
            for related_id in rule.get('related_series_ids', []):
                if related_id in declared:
                    trial['binding_units'][related_id] = declared[related_id]
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
    final_ids.update(sid for rules in valid.values() for rule in rules for sid in rule.get('related_series_ids', []))
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
    return DraftAssessment(cleaned, result, issues, original_candidate, [deepcopy(original_candidate)])
