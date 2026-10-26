"""Deterministic readable Markdown and plain-text diffs; no model or HTML rendering."""
import difflib
import json
from tin.schemas.judgment import JudgmentIn

FIELDS = {'contradiction': '主矛盾', 'marginal_focus': '短线边际', 'cost_line': '成本线',
          'pricing_power': '边际定价权', 'time_dimension': '时间维度', 'survey_notes': '调研信息差',
          'whitelist': '指标白名单', 'paradigm': '编排范式', 'tone': '基调', 'tone_note': '基调附注',
          'thresholds': '阈值', 'priority_rule': '冲突优先级', 'falsifiers': '证伪条件',
          'unstructured': '待确认项'}


def cell(value):
    return str(value if value is not None else '待确认').replace('|', '\\|').replace('\n', ' ')


def dump_markdown(payload: JudgmentIn):
    j = payload.model_dump(mode='json', by_alias=True)
    lines = ['# 研究逻辑', '', f"品种：{j['variety']} · 撰写日期：{j['written_at']} · 复核周期：{j['review_period_days']}天", '', '## 主矛盾']
    for side, label in [('bull', '支持依据'), ('bear', '反对依据')]:
        data = j['contradiction'][side]
        lines.extend(['', '### ' + label, data['claim'] or '待确认'])
        lines.extend('- ' + e['desc'] + (' · 指标 `' + e['series_id'] + '`' if e.get('series_id') else ' · 未绑定')
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
            lines.extend('- ' + item['desc'] + (' · 指标 `' + item['series_id'] + '`' if item.get('series_id') else ' · 未绑定') for item in value)
        elif key == 'whitelist': lines.extend('- `' + sid + '`' for sid in value)
        elif isinstance(value, dict):
            for name, content in value.items():
                if content is None or content == []: continue
                if name == 'items':
                    lines.extend('- ' + item['desc'] + (' · 指标 `' + item['series_id'] + '`' if item.get('series_id') else '') for item in content)
                else: lines.append('- ' + {'type':'成本口径','value':'数值','source':'来源','note':'附注','category':'类型','short':'短期','long':'中长期','key':'关键','sources':'来源'}.get(name,name) + '：' + ('；'.join(map(str, content)) if isinstance(content, list) else str(content)))
        elif value: lines.append(str(value))
        if not value: lines.append('待确认')
    lines.extend(['', '## 阈值', '', '| 名称 | 指标 | 条件 | 类型 | 周期 |', '| --- | --- | --- | --- | --- |'])
    for t in j['thresholds']:
        lines.append('| ' + ' | '.join(map(cell, [t['name'], t['series_id'], t['operator'] + ' ' + str(t['value']), t['type'], t['horizon']])) + ' |')
    lines.extend(['', '## 冲突优先级', ' > '.join((j['priority_rule'] or {}).get('order', [])) or '待确认', '', '## 证伪条件'])
    lines.extend('- ' + f['desc'] + ' · `' + f['series_id'] + '` · ' + f['expression'] + ' ' + f['unit_confirmed']
                 + ' · 连续' + str(f['consecutive_periods']) + '期' for f in j['falsifiers'])
    lines.extend(['', '## 待确认项'])
    lines.extend('- ' + item['section'] + '：' + item['text'] + '（' + item['reason'] + '）' for item in j['unstructured'])
    return '\n'.join(lines).rstrip() + '\n'


def version_diff(before, after):
    left = before.model_dump(mode='json', by_alias=True) if before else {}
    right = after.model_dump(mode='json', by_alias=True)
    fields = [{'field': name, 'label': label, 'before': left.get(name), 'after': right.get(name)}
              for name, label in FIELDS.items() if left.get(name) != right.get(name)]
    return {'fields': fields, 'markdown': ''.join(difflib.unified_diff(
        dump_markdown(before).splitlines(keepends=True) if before else [],
        dump_markdown(after).splitlines(keepends=True), fromfile='上一版本.md', tofile='新草稿.md'))}
