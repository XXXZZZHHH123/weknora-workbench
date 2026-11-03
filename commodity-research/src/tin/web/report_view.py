"""Readable views and Markdown from actual report records, without inference."""
from copy import deepcopy
from math import isfinite
from tin.web.research_display import human_text, indicator_name, condition_text


def _plain(value):
    return str(value) if isinstance(value, (str, int, float)) else ''


def _number(value):
    if value is None:
        return '—'
    if type(value) in (int, float):
        return (format(value, '.10g') if value else '0') if isfinite(value) else '—'
    return _plain(value)


def _judgment_view(payload, names, signals, preview=None):
    if not isinstance(payload, dict):
        return None
    row = deepcopy(payload)
    contradiction = row.get('contradiction')
    row['cases'] = []
    if isinstance(contradiction, dict):
        for key, label in (('bull', '支持依据'), ('bear', '反对依据')):
            side = contradiction.get(key)
            if not isinstance(side, dict):
                continue
            evidence = []
            for item in side.get('evidence') or []:
                if isinstance(item, dict):
                    identifier = item.get('series_id')
                    evidence.append({'text': human_text(item.get('desc'), names), 'series_id': identifier,
                                     'name': indicator_name(identifier, names) if identifier else None})
            row['cases'].append({'label': label, 'claim': human_text(side.get('claim'), names), 'evidence': evidence})
        row['contradiction'] = human_text(contradiction.get('text'), names)
    else:
        row['contradiction'] = human_text(contradiction, names)
    row['tone_note'] = human_text(row.get('tone_note'), names)
    states = {item.get('rule_id'): item for item in signals if isinstance(item, dict)}
    trial = {item.get('rule_id'): item for item in (preview or {}).get('signals') or [] if isinstance(item, dict)}
    for kind in ('thresholds', 'falsifiers'):
        rules = []
        for item in row.get(kind) or []:
            if not isinstance(item, dict):
                continue
            rule = dict(item)
            value = item.get('value')
            rule['name'] = human_text(item.get('name') or item.get('desc'), names) or '已保存观察条件'
            rule['series_name'] = indicator_name(item.get('series_id'), names)
            if kind == 'thresholds':
                if isinstance(value, dict) and value.get('kind') == 'sma':
                    comparison = {'>': '高于', '>=': '不低于', '<': '低于', '<=': '不高于',
                        'crosses_below': '下穿', 'crosses_above': '上穿'}.get(item.get('operator'), '')
                    rule['condition'] = comparison + ' MA' + str(value['window'])
                else:
                    rule['condition'] = (' – '.join(_plain(part) for part in value)
                        if item.get('operator') == 'between' and isinstance(value, list)
                        else _plain(item.get('operator')) + ' ' + _plain(value))
            else:
                rule['condition'] = condition_text(item.get('expression')) + ' · 连续 ' + str(item.get('consecutive_periods') or 1) + ' 期'
            saved = states.get(item.get('id')) or {}
            computed = trial.get(item.get('id'))
            signal = computed if computed is not None else saved
            rule['state'] = signal.get('state') or '所选日暂无已记录结果'
            rule['recorded_state'] = saved.get('state')
            rule['is_preview'] = computed is not None
            rule['evidence_dates'] = sorted({entry['as_of'] for entry in signal.get('evidence') or []
                if isinstance(entry, dict) and isinstance(entry.get('as_of'), str)})
            rule['current_value'] = signal.get('current_value')
            rule['current_value_text'] = _number(rule['current_value'])
            rule['gap_note'] = human_text(signal.get('gap_note'), names)
            rules.append(rule)
        row[kind] = rules
    return row


def _brief_view(item, names):
    row = deepcopy(item)
    claims = []
    for item in row.get('claims') or []:
        if not isinstance(item, dict):
            continue
        claim = {'text': _plain(item.get('text') or item.get('claim')), 'evidence': []}
        for evidence in item.get('evidence') or []:
            if not isinstance(evidence, dict):
                continue
            identifier = evidence.get('series_id')
            claim['evidence'].append({**evidence, 'name': names.get(identifier) or '指标名称待核对' if identifier else None})
        claims.append(claim)
    row['claims'] = claims
    row['unverified'] = [{'text': _plain(item.get('claim') or item.get('text')),
        'reason': '；'.join(map(str, item.get('reasons') or item.get('problems') or []))}
        for item in row.get('unverified') or [] if isinstance(item, dict)]
    return row


def _content_rows(rows, names):
    output = deepcopy(rows or [])
    fields = {'title', 'thesis', 'trigger', 'response_action', 'invalidation', 'watch_for', 'reason'}
    for row in output:
        for key in fields & row.keys():
            row[key] = human_text(row[key], names)
    return output


def _unit(value):
    return value if isinstance(value, str) and value.strip() and value != '未注明' else ''


def _card_monitor_view(row, names):
    """Readable state of a card's cited conditions and indicators, from the saved monitor result."""
    monitor = row.get('monitor') if isinstance(row.get('monitor'), dict) else None
    conditions, indicators = [], []
    for item in (monitor or {}).get('conditions') or []:
        unit = _unit(item.get('unit'))
        value = item.get('value')
        conditions.append({'title': human_text(item.get('title'), names), 'state': item.get('state') or '无法核对',
            'field_label': item.get('field_label') or '', 'condition': human_text(item.get('condition'), names),
            'value_text': (_number(value) + (' ' + unit if unit else '')) if value is not None else '',
            'as_of': item.get('as_of') or '', 'note': human_text(item.get('note'), names),
            'outdated': item.get('outdated') is True, 'chart': item.get('chart')})
    for item in (monitor or {}).get('indicators') or []:
        chart = item.get('chart') if isinstance(item.get('chart'), dict) else {}
        unit = _unit(chart.get('unit'))
        change = chart.get('change')
        fresh = chart.get('freshness') if isinstance(chart.get('freshness'), dict) else {}
        indicators.append({'title': human_text(item.get('title'), names), 'field_label': item.get('field_label') or '',
            'value_text': (_number(chart.get('latest')) + (' ' + unit if unit else '')) if chart.get('latest') is not None else '暂无可用观测',
            'change_text': (('+' if type(change) in (int, float) and change > 0 else '') + _number(change)) if change is not None else '',
            'as_of': str(chart.get('date') or '')[:10], 'chart': chart or None,
            'stale_note': f"最新数据距今 {fresh['age_days']} 天，可能已停更" if fresh.get('stale') else '',
            'note': human_text(item.get('note'), names)})
    return {'monitored': monitor is not None, 'conditions': conditions, 'indicators': indicators,
            'triggered': sum(item['state'] == '触发' for item in conditions)}


def _strategy_overview(view):
    cards = view['research_cards']
    triggered = [(card['title'], item) for card in cards for item in card['monitor_view']['conditions'] if item['state'] == '触发']
    parts = []
    if cards:
        parts.append(f'{len(cards)} 个策略方案')
    if view['strategies']:
        parts.append(f"{len(view['strategies'])} 个交易计划")
    if not parts:
        return {'text': '尚无已保存策略', 'triggered': []}
    conditions = sum(len(card['monitor_view']['conditions']) for card in cards)
    indicators = sum(len(card['monitor_view']['indicators']) for card in cards)
    if conditions:
        parts.append(f'{len(triggered)} 项所引条件已触发' if triggered else f'所引 {conditions} 项条件暂无触发')
    elif indicators:
        parts.append(f'跟踪 {indicators} 个引用指标')
    return {'text': ' · '.join(parts), 'triggered': [title + '：' + item['title'] for title, item in triggered]}


def present_snapshot(payload, names=None):
    from tin.web.report_watch import present_watch, watch_items
    fallback = {item['series_id']: item['name'] for item in payload.get('observations') or []
                if isinstance(item, dict) and isinstance(item.get('series_id'), str)
                and isinstance(item.get('name'), str) and item['name'].strip()}
    names = {**fallback, **(names or {})}
    units = {row['series_id']: row.get('unit') or '' for row in payload.get('observations') or []
             if isinstance(row, dict) and isinstance(row.get('series_id'), str)}
    observations = []
    for item in payload.get('observations', []):
        row = dict(item)
        row['name'] = indicator_name(item['series_id'], names)
        observations.append(row)
    changes = []
    for item in payload.get('changes') or []:
        first, last = item.get('first') or {}, item.get('last') or {}
        changes.append({**item, 'name': human_text(item.get('name'), names) or indicator_name(item['series_id'], names),
            'first_text': _number(first.get('value')), 'last_text': _number(last.get('value')),
            'change_text': _number(item.get('change')), 'change_pct_text': _number(item.get('change_pct'))})
    sections = {key: payload.get(key) or [] for key in ('derived', 'signals', 'gaps')}
    briefs = [_brief_view(item, names) for item in payload.get('briefs') or []]
    judgment = _judgment_view(payload.get('judgment'), names, sections['signals'], payload.get('rule_preview'))
    replay = None
    if not briefs and judgment and (_plain(judgment.get('tone')) or _plain(judgment.get('tone_note'))):
        replay = {'label': '已保存研判回放', 'tone': _plain(judgment.get('tone')),
                  'tone_note': _plain(judgment.get('tone_note')),
                  'version': judgment.get('version'), 'status': judgment.get('status')}
    result = {**sections, 'observations': observations,
            'judgment': judgment, 'judgment_replay': replay,
            'rule_preview': payload.get('rule_preview'),
            'period': payload.get('period'), 'changes': changes, 'briefs': briefs,
            'strategies': _content_rows(payload.get('strategies'), names), 'strategy_events': _content_rows(payload.get('strategy_events'), names),
            'research_cards': [{**row, 'monitor_view': _card_monitor_view(row, names)}
                               for row in _content_rows(payload.get('research_cards'), names)],
            'research_events': payload.get('research_events') or [],
            'coverage': payload.get('coverage') or {},
            'next_review': present_watch(watch_items(payload.get('judgment'),
                payload.get('strategies') or [], payload.get('research_cards') or []), names, units),
            'counts': {'observations': len(observations),
                       **{key: len(rows) for key, rows in sections.items()}}}
    from tin.web.report_watch import without_aggregates
    result['pending_conditions'] = without_aggregates([row for row in result['next_review'] if row.get('kind') == 'observation'])
    result['latest_values'] = {row['series_id']: {'value_text': _number(row.get('value')) + (
        ' ' + _unit(row.get('unit')) if _unit(row.get('unit')) else ''), 'as_of': str(row.get('as_of') or '')[:10]}
        for row in observations if row.get('value') is not None}
    result['strategy_overview'] = _strategy_overview(result)
    result['conclusion'] = _conclusion(result)
    result.update(_workflow_view(result, payload, names))
    return result


def _conclusion(view):
    """The one-line research conclusion already saved, for the report lead; never generated."""
    for brief in view['briefs']:
        if _plain(brief.get('summary')):
            return {'tone': _plain(brief.get('tone')), 'text': _plain(brief.get('summary')), 'source': '已保存日报结论'}
    replay = view['judgment_replay']
    if replay:
        return {'tone': replay['tone'], 'text': replay['tone_note'], 'source': replay['label']}
    return None


def _workflow_view(view, payload, names):
    from tin.web.report_drafts import FORMAT, SECTION_LABELS
    from tin.web.report_watch import display_text
    period = payload.get('period') or {'kind': 'daily', 'end': payload.get('as_of')}
    manual = payload.get('manual_report')
    saved = manual.get('draft') if isinstance(manual, dict) else None
    if not isinstance(saved, dict) or saved.get('format') != FORMAT:
        saved = None
    panels = {}
    references = (saved or {}).get('reference_snapshots') or []
    for key, label in SECTION_LABELS.items():
        selected = (saved.get('sections') or {}).get(key) if saved else None
        # Notes are additions beside saved 研判 and 策略; they start empty and a
        # saved empty note stays empty. The saved records are always shown.
        panels[key] = {'label': label,
            'visible': selected.get('visible') is True if selected is not None else True,
            'text': display_text(_plain(selected.get('text')), names) if selected is not None else '',
            # Quoted versions sit with their own kind: 研判 under 研判, 策略 under 策略.
            'references': [row for row in references if
                (row.get('kind') == 'strategy') == (key == 'watch') and key in {'research', 'watch'}]}
    default_ids = payload.get('report_figure_selection')
    if not isinstance(default_ids, list):
        default_ids = [row['series_id'] for row in view['observations'][:6]]
    figures = deepcopy(saved.get('figures') or []) if saved else [
        {'series_id': identifier, 'caption': ''} for identifier in default_ids[:6]]
    draft = ({key: deepcopy(saved[key]) for key in ('format', 'period', 'date', 'title', 'sections', 'figures', 'references')}
             if saved else None)
    if draft is None:
        draft = {'format': FORMAT, 'period': period.get('kind', 'daily'),
            'date': period.get('end') or payload.get('as_of'), 'title': '研究周报' if period.get('kind') == 'weekly' else '研究日报',
            'sections': {key: {'visible': row['visible'], 'text': row['text'][:4000]} for key, row in panels.items()},
            'figures': figures, 'references': []}
    return {'panels': panels, 'is_manual_report': saved is not None, 'manual_report': manual if saved else None,
        'editable_draft': draft, 'selected_figures': figures,
        'figure_options': [{'series_id': row['series_id'], 'name': row['name'], 'unit': row.get('unit') or ''}
            for row in view['observations']],
        'report_title': saved.get('title') if saved else None}


def _md(value):
    # Source text is content in exported Markdown, never active HTML or table syntax.
    return _plain(value).replace('&', '&amp;').replace('<', '&lt;').replace('>', '&gt;').replace('|', '\\|').replace('\n', ' ')


def _md_lines(text):
    return [_md(line) for line in _plain(text).splitlines() if line.strip()]


def _changes_table(view):
    lines = ['| 指标 | 首个有效值 | 末个有效值 | 变化 | 单位 | 数据日期 |', '| --- | --- | --- | --- | --- | --- |']
    notes = []
    if view['changes']:
        for item in view['changes']:
            first, last = item.get('first') or {}, item.get('last') or {}
            cells = [item['name'], item['first_text'], item['last_text'], item['change_text'],
                     item.get('unit'), _plain(first.get('as_of')) + ' → ' + _plain(last.get('as_of'))]
            lines.append('| ' + ' | '.join(_md(value) if value is not None else '—' for value in cells) + ' |')
            if item.get('note'):
                notes.append('- ' + _md(item.get('name') or item['series_id']) + '：' + _md(item['note']))
    else:
        for row in view['observations']:
            lines.append('| ' + ' | '.join(_md(value) if value is not None else '—' for value in
                [row['name'], None, row.get('value'), None, row.get('unit'), row.get('as_of')]) + ' |')
    return lines + ([''] + notes if notes else [])


def _note_lines(panel, heading):
    lines = []
    if panel['text']:
        lines += ['> **' + heading + '**', '>'] + ['> ' + line for line in _md_lines(panel['text'])] + ['']
    for ref in panel['references']:
        lines += ['#### ' + _md(ref['title']) + ' · 引用 v' + str(ref['revision']),
                  '> 引用已保存记录的明确版本；原文数字不据此变成核验事实。', '',
                  *_md_lines(ref.get('body_text') or ref.get('summary')), '']
    return lines


def _judgment_lines(view):
    lines = ['### 研究结论', '']
    if view['briefs']:
        for brief in view['briefs']:
            lines += ['#### ' + _md(brief.get('trade_date')) + ' · ' + _md(brief.get('status')), '',
                      _md(brief.get('summary')) or '未填写已保存结论', '',
                      '- 基调：' + (_md(brief.get('tone')) or '未填写'),
                      '- 操作倾向：' + (_md(brief.get('stance')) or '未填写'), '']
            for claim in brief['claims']:
                lines.append('- ' + _md(claim['text']))
                for evidence in claim['evidence']:
                    lines.append('  - ' + _md(evidence.get('name') or '已保存证据') + '：' + _md(evidence.get('value'))
                                 + ' · ' + _md(evidence.get('as_of')))
            lines.append('')
    elif view['judgment_replay']:
        replay = view['judgment_replay']
        lines += ['#### ' + replay['label'] + ' · ' + _md(replay['status']), '',
                  '- 基调：' + (_md(replay['tone']) or '未填写'), '',
                  _md(replay['tone_note']) or '当前保存记录未填写基调说明。', '',
                  '> 回放当前已保存研究判断，供本次数据核对；没有据此生成新的日报结论。', '']
    else:
        lines += ['尚无已保存研究结论；本报告只呈现事实和已保存研究逻辑。', '']
    judgment = view['judgment']
    lines += ['### 研究判断', '']
    if not judgment:
        return lines + ['未登记研究判断。', '']
    lines += ['- 研究状态：' + _md(judgment.get('status')), '- 基调：' + _md(judgment.get('tone')), '']
    if judgment.get('contradiction'):
        lines += [_md(judgment['contradiction']), '']
    latest = view['latest_values']
    for side in judgment['cases']:
        lines += ['#### ' + side['label'], _md(side['claim']) or '待补充']
        for item in side['evidence']:
            value = latest.get(item.get('series_id')) if item.get('series_id') else None
            lines.append('- ' + _md(item['text']) + (' · ' + _md(item.get('name')) if item.get('name') else '')
                         + (' · 最新 ' + _md(value['value_text']) + (' · ' + _md(value['as_of']) if value['as_of'] else '')
                            if value else ''))
        lines.append('')
    for key, heading in [('thresholds', '区间与阈值'), ('falsifiers', '证伪条件')]:
        if not judgment[key]:
            continue
        lines += ['#### ' + heading]
        if view['rule_preview']:
            lines += ['> ' + _md(view['rule_preview']['label']) + '：' + _md(view['rule_preview']['notice'])]
        lines += ['- ' + _md(item['name']) + ' · ' + _md(item['series_name']) + '：' + _md(item['condition']) + ' · ' + _md(item['state'])
                  + ('（当前规则试算）' if item['is_preview'] else '')
                  + (' · 已记录：' + _md(item['recorded_state']) if item['recorded_state'] else '')
                  + (' · 依据日期：' + '、'.join(_md(value) for value in item['evidence_dates']) if item['evidence_dates'] else '')
                  for item in judgment[key]]
        lines.append('')
    if view['pending_conditions']:
        lines += ['#### 待验证条件', '', *['- [ ] ' + _md(item['text']) for item in view['pending_conditions']],
                  '', '> 尚未绑定可计算规则，需研究员在研判中核对后才纳入监测。', '']
    return lines


def _strategy_lines(view):
    lines = []
    if view['research_cards']:
        lines += ['### 当前保存的人工研究策略', '',
                  '以下为当前人工研究方案，不代表历史当时已知观点或实际成交；正文数字未逐项核验；所引条件按截至报告日的可读数据试算。', '']
    for card in view['research_cards']:
        monitor = card['monitor_view']
        lines += ['#### ' + _md(card['title']) + ' · ' + _md(card['direction_label']),
            '- 适用 / 触发：' + _md(card['trigger']),
            '- 应对：' + (_md(card.get('response_action')) or '尚未填写 · 观察方案待补应对'),
            '- 暂停 / 撤回：' + _md(card['invalidation']), '- 复核日期：' + _md(card['review_date'])]
        if card.get('thesis'):
            lines.append('- 适用范围：' + _md(card['thesis']))
        if card.get('watch_for'):
            lines.append('- 执行前核对：' + _md(card['watch_for']))
        for item in monitor['conditions']:
            facts = [item['value_text'] and '当前 ' + item['value_text'], item['condition'] and '条件 ' + item['condition'],
                     item['as_of'] and '数据 ' + item['as_of'], item['note']]
            lines.append('- 所引条件「' + _md(item['title']) + '」（' + _md(item['field_label']) + '）：'
                         + _md('已触发' if item['state'] == '触发' else item['state'])
                         + ('；' + _md(' · '.join(filter(None, facts))) if any(facts) else ''))
        for item in monitor['indicators']:
            facts = ['最新 ' + item['value_text'], item['change_text'] and '较前值 ' + item['change_text'],
                     item['as_of'] and '数据 ' + item['as_of'], item['stale_note']]
            lines.append('- 所引指标「' + _md(item['title']) + '」：' + _md(' · '.join(filter(None, facts))))
        lines.append('')
    for item in view['strategies']:
        lines += ['### 交易计划 · ' + _md(item['title']) + ' · ' + _md(item['status_label']),
                  '- 类型：' + _md(item['mode_label']), '- 依据：' + _md(item.get('thesis')),
                  '- 失效/复核：' + _md(item.get('invalidation')), '']
    if not view['strategies'] and not view['research_cards']:
        lines += ['尚无已保存策略；请先由研究员确认研究逻辑和具体合约。', '']
    if view['strategy_events']:
        lines += ['### 报告期内策略事件', '', '策略条件触发不等于实际成交记录。']
        lines += ['- ' + _md(item['at']) + ' · ' + _md(item['title']) + ' · ' + _md(item['label']) + '：' + _md(item['reason'])
                  for item in view['strategy_events']]
        lines.append('')
    return lines


def readable_markdown(payload, *, title='研究报告', names=None):
    """The same collection as the HTML page: saved 研判, then saved 策略, with notes and charts as aids."""
    view = present_snapshot(payload, names)
    manual = view['manual_report']
    if manual and manual.get('unavailable_references'):
        from fastapi import HTTPException
        raise HTTPException(409, '报告引用已不可读，请调整引用后再导出')
    saved = manual['draft'] if manual else None
    period = view.get('period') or {'kind': 'daily', 'start': payload.get('as_of'), 'end': payload.get('as_of')}
    weekly = period.get('kind') == 'weekly'
    panels = view['panels']
    lines = ['# ' + _md(saved['title'] if saved else title), '',
             '> 报告期：' + str(period.get('start')) + ' — ' + str(period.get('end')),
             '> 依据当前已确认来源回放；研究逻辑和策略为当前保存版本，不代表当时已形成的判断。'
             + ('按语为个人报告 v' + str(manual['revision']) + ' 的人工内容，原始指标事实保持只读。' if manual else ''), '']
    if panels['summary']['visible']:
        lines += ['## 导语', '']
        lines += _md_lines(panels['summary']['text']) + ([''] if panels['summary']['text'] else [])
        conclusion = view['conclusion']
        lines += ['- 研判结论：' + (_md((conclusion['tone'] + '，' if conclusion['tone'] else '') + conclusion['text'])
                                    + '（' + conclusion['source'] + '）' if conclusion else '暂无已保存研究结论'),
                  '- 策略概况：' + _md(view['strategy_overview']['text'])]
        lines += ['  - ' + _md(item) for item in view['strategy_overview']['triggered']]
        lines.append('')
    if panels['research']['visible']:
        lines += ['## 研判', ''] + _note_lines(panels['research'], '研究员按语') + _judgment_lines(view)
        if panels['dashboard']['visible']:
            lines += ['### ' + ('本周主要变化' if weekly else '主要变化'), ''] + _note_lines(panels['dashboard'], '图表说明')
            lines += _changes_table(view)
            figure_names = {row['series_id']: row['name'] for row in view['observations']}
            for figure in view['selected_figures'] if saved else []:
                lines.append('- 选图：' + _md(figure_names.get(figure['series_id']) or '已引用指标')
                             + (' · ' + _md(figure.get('caption')) if figure.get('caption') else ''))
            lines.append('')
    if panels['watch']['visible']:
        lines += ['## 策略', ''] + _note_lines(panels['watch'], '研究员按语') + _strategy_lines(view)
    lines += ['<details><summary>数据缺口与来源</summary>', '', '## 数据缺口与来源', '']
    if manual:
        lines += ['- 个人报告记录：`' + _md(manual.get('id')) + '` · v' + str(manual.get('revision')),
                  '- 来源版本：`' + _md(saved.get('source_revision')) + '`']
        lines += ['- 引用：' + _md(ref['kind']) + ' `' + _md(ref['id']) + '` · v' + str(ref['revision'])
                  for ref in saved.get('references') or []]
    for item in view['gaps']:
        lines.append('- ' + _md(item.get('name') or item.get('series_id')) + '：' + _md(item.get('reason')))
    for item in view['observations']:
        lines.append('- ' + _md(item['name']) + ' · `' + _md(item['series_id']).replace('`', '') + '` · '
                     + _md(item.get('as_of')) + ' · ' + _md(item.get('source')))
    lines += ['', '</details>', '']
    return '\n'.join(lines)
