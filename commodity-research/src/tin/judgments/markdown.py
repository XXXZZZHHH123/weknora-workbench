"""Deterministic readable Markdown and plain-text diffs; no model or HTML rendering."""
import difflib
import json
from tin.schemas.judgment import JudgmentIn
from tin.judgments.markdown_protocol import TARGET_LABELS
from tin.judgments.quantity_alignment import conversion_text

FIELDS = {'contradiction': '主矛盾', 'marginal_focus': '短线边际', 'cost_line': '成本线',
          'pricing_power': '边际定价权', 'time_dimension': '时间维度', 'survey_notes': '调研信息差',
          'whitelist': '指标白名单', 'paradigm': '编排范式', 'tone': '基调', 'tone_note': '基调附注',
          'thresholds': '阈值', 'priority_rule': '冲突优先级', 'falsifiers': '证伪条件',
          'unstructured': '待确认项'}


def cell(value):
    return str(value if value is not None else '待确认').replace('|', '\\|').replace('\n', ' ')


def related_text(item):
    ids = item.get('related_series_ids', [])
    return ' · 关联指标 ' + '、'.join('`' + sid + '`' for sid in ids) if ids else ''


def dimension_text(item, dimensions):
    from tin.web.judgment_dimension_view import dimension_name
    identifier = item.get('dimension')
    return ('维度：' + dimension_name(identifier, dimensions) + '（`' + identifier + '`）') if identifier else ''


def dump_markdown(payload: JudgmentIn, *, dimensions=None):
    from tin.web.research_dimensions import default_dimensions
    dimensions = default_dimensions() if dimensions is None else dimensions
    j = payload.model_dump(mode='json', by_alias=True)
    lines = ['# 研究逻辑', '', f"品种：{j['variety']} · 撰写日期：{j['written_at']} · 复核周期：{j['review_period_days']}天", '', '## 主矛盾']
    for side, label in [('bull', '支持依据'), ('bear', '反对依据')]:
        data = j['contradiction'][side]
        lines.extend(['', '### ' + label, data['claim'] or '待确认'])
        lines.extend('- ' + e['desc'] + (' · 指标 `' + e['series_id'] + '`' if e.get('series_id') else ' · 未绑定') + related_text(e)
                     + (' · ' + dimension_text(e, dimensions) if e.get('dimension') else '')
                     for e in data['evidence'])
    if j['contradiction'].get('text'):
        lines.extend(['', '### 主逻辑补充', j['contradiction']['text']])
    if j['contradiction'].get('window'):
        w = j['contradiction']['window']
        lines.extend(['', '验证窗口：' + ' / '.join(str(w.get(k) or '待确认') for k in ('from', 'to', 'note'))])
    for key in ('marginal_focus', 'cost_line', 'pricing_power', 'time_dimension', 'survey_notes', 'whitelist', 'paradigm', 'tone', 'tone_note'):
        lines.extend(['', '## ' + FIELDS[key]])
        value = j[key]
        if key == 'marginal_focus':
            lines.extend('- ' + item['desc'] + (' · 指标 `' + item['series_id'] + '`' if item.get('series_id') else ' · 未绑定') + related_text(item) for item in value)
        elif key == 'whitelist': lines.extend('- `' + sid + '`' for sid in value)
        elif isinstance(value, dict):
            section_start = len(lines)
            for name, content in value.items():
                if content is None or content == []: continue
                if name == 'items':
                    lines.extend('- ' + item['desc'] + (' · 指标 `' + item['series_id'] + '`' if item.get('series_id') else '') + related_text(item) for item in content)
                else: lines.append('- ' + {'type':'成本口径','value':'数值','source':'来源','note':'附注','category':'类型','short':'短期','long':'中长期','key':'关键','sources':'来源'}.get(name,name) + '：' + ('；'.join(map(str, content)) if isinstance(content, list) else str(content)))
            if len(lines) == section_start: lines.append('未提供')
        elif value: lines.append(str(value))
        if not value: lines.append('待确认')
    lines.extend(['', '## 阈值', '', '| 名称 | 指标 | 条件 | 类型 | 周期 | 用途说明 | 补充说明 |',
                  '| --- | --- | --- | --- | --- | --- | --- |'])
    for t in j['thresholds']:
        lines.append('| ' + ' | '.join(map(cell, [t['name'], t['series_id'],
            t['operator'] + ' ' + json.dumps(t['value'], allow_nan=False), t['type'], t['horizon'],
            t['action_hint'], t['note']])) + ' |')
    for t in j['thresholds']:
        if t.get('dimension'):
            lines.append('阈值维度：`' + t['id'] + '`（' + cell(t['name']) + '） · ' + dimension_text(t, dimensions))
        if t.get('related_series_ids'):
            lines.append('阈值关联：`' + t['id'] + '`（' + cell(t['name']) + '）' + related_text(t))
        if converted := conversion_text(t):
            lines.append('阈值换算：`' + t['id'] + '` · ' + converted)
    lines.extend(['', '## 冲突优先级', ' > '.join((j['priority_rule'] or {}).get('order', [])) or '待确认'])
    if j['priority_rule'] and j['priority_rule']['note']:
        lines.append('说明：' + j['priority_rule']['note'])
    lines.extend(['', '## 证伪条件'])
    for f in j['falsifiers']:
        lines.append('- ' + cell(f['desc']) + ' · `' + f['series_id'] + '` · 条件：' + f['expression']
            + ' · 单位：' + cell(f['unit_confirmed']) + ' · 连续' + str(f['consecutive_periods'])
            + '期 · 证伪对象：' + TARGET_LABELS[f['side']])
        if f.get('dimension'):
            lines.append('  - ' + dimension_text(f, dimensions))
        if f.get('related_series_ids'):
            lines.append('  - 关联说明：' + related_text(f).removeprefix(' · '))
        if converted := conversion_text(f):
            lines.append('  - 数值换算：' + converted)
        if f['note']:
            lines.append('  - 说明：' + cell(f['note']))
    lines.extend(['', '## 待确认项'])
    lines.extend('- ' + item['section'] + '：' + item['text'] + '（' + item['reason'] + '）'
                 + (' · 指标 `' + item['series_id'] + '`' if item.get('series_id') else '') + related_text(item)
                 for item in j['unstructured'])
    return '\n'.join(lines).rstrip() + '\n'


def _changed_values(before, after):
    if isinstance(before, dict) and isinstance(after, dict):
        old, new = {}, {}
        for key in dict.fromkeys([*before, *after]):
            if before.get(key) != after.get(key):
                old[key], new[key] = _changed_values(before.get(key), after.get(key))
        return old, new
    if isinstance(before, list) and isinstance(after, list):
        old, new = [], []
        left, right = ([json.dumps(item, sort_keys=True) for item in values] for values in (before, after))
        for action, start, end, first, last in difflib.SequenceMatcher(None, left, right, autojunk=False).get_opcodes():
            if action != 'equal':
                old.extend(before[start:end])
                new.extend(after[first:last])
        return old, new
    return before, after


def version_diff(before, after, *, dimensions=None, items_only=False):
    left = before.model_dump(mode='json', by_alias=True) if before else {}
    right = after.model_dump(mode='json', by_alias=True)
    fields = [{'field': name, 'label': label, 'before': left.get(name), 'after': right.get(name)}
              for name, label in FIELDS.items() if left.get(name) != right.get(name)]
    if items_only:
        for field in fields:
            field['before'], field['after'] = _changed_values(field['before'], field['after'])
    return {'fields': fields, 'markdown': ''.join(difflib.unified_diff(
        dump_markdown(before, dimensions=dimensions).splitlines(keepends=True) if before else [],
        dump_markdown(after, dimensions=dimensions).splitlines(keepends=True), fromfile='上一版本.md',
        tofile='新草稿.md', n=0 if items_only else 3))}
