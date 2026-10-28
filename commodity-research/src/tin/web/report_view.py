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
            'research_cards': _content_rows(payload.get('research_cards'), names),
            'research_events': payload.get('research_events') or [],
            'coverage': payload.get('coverage') or {},
            'next_review': present_watch(watch_items(payload.get('judgment'),
                payload.get('strategies') or [], payload.get('research_cards') or []), names, units),
            'counts': {'observations': len(observations),
                       **{key: len(rows) for key, rows in sections.items()}}}
    result.update(_workflow_view(result, payload, names))
    return result


def _workflow_view(view, payload, names):
    from tin.web.report_drafts import FORMAT, SECTION_LABELS
    from tin.web.report_watch import display_text
    period = payload.get('period') or {'kind': 'daily', 'end': payload.get('as_of')}
    manual = payload.get('manual_report')
    saved = manual.get('draft') if isinstance(manual, dict) else None
    if not isinstance(saved, dict) or saved.get('format') != FORMAT:
        saved = None
    summary = '\n\n'.join(_plain(row.get('summary')) for row in view['briefs'] if row.get('summary'))
    if not summary and view['judgment_replay']:
        summary = view['judgment_replay']['tone_note']
    research = []
    if view['judgment']:
        judgment = view['judgment']
        if judgment.get('contradiction'):
            research.append(judgment['contradiction'])
        research.extend(side['label'] + '：' + side['claim'] for side in judgment['cases'] if side['claim'])
    research.extend(row['title'] + '：触发 ' + row['trigger'] + '；应对 '
                    + (row.get('response_action') or '尚未填写') for row in view['research_cards'])
    research.extend(row['title'] + '：' + row['thesis'] for row in view['strategies'] if row.get('thesis'))
    texts = {'summary': summary, 'research': '\n\n'.join(research), 'dashboard': '',
             'watch': '\n'.join(row['text'] for row in view['next_review'] if row.get('text'))}
    panels = {}
    for key, label in SECTION_LABELS.items():
        selected = (saved.get('sections') or {}).get(key) if saved else None
        # A saved empty string is an intentional edit. Only an entirely absent
        # personal draft receives automatic material from saved research.
        panels[key] = {'label': label,
            'visible': selected.get('visible') is True if selected is not None else True,
            'text': _plain(selected.get('text')) if selected is not None else texts[key],
            'references': [row for row in (saved or {}).get('reference_snapshots') or [] if row.get('section') == key]}
        if key == 'watch':
            panels[key]['text'] = display_text(panels[key]['text'], names)
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


def _legacy_markdown(payload, *, title='研究报告', names=None):
    view = present_snapshot(payload, names)
    period = view.get('period') or {'kind': 'daily', 'start': payload.get('as_of'), 'end': payload.get('as_of')}
    weekly = period['kind'] == 'weekly'
    lines = ['# ' + _md(title), '', '> 报告期：' + str(period['start']) + ' — ' + str(period['end']),
             '> 依据当前已确认来源回放；研究逻辑和策略为当前保存版本，不代表当时已形成的判断。', '',
             '## 研究结论', '']
    if view['briefs']:
        for brief in view['briefs']:
            lines += ['### ' + str(brief.get('trade_date')) + ' · ' + _md(brief.get('status')), '',
                      _md(brief.get('summary')) or '未填写已保存结论', '',
                      '- 基调：' + (_md(brief.get('tone')) or '未填写'),
                      '- 操作倾向：' + (_md(brief.get('stance')) or '未填写'), '']
            for claim in brief['claims']:
                lines.append('- ' + _md(claim['text']))
                for evidence in claim['evidence']:
                    lines.append('  - ' + _md(evidence.get('name') or '已保存证据') + '：' + _md(evidence.get('value'))
                                 + ' · ' + _md(evidence.get('as_of')))
    elif view['judgment_replay']:
        replay = view['judgment_replay']
        lines += ['### ' + replay['label'] + ' · ' + _md(replay['status']), '',
                  '- 基调：' + ( _md(replay['tone']) or '未填写'), '',
                  _md(replay['tone_note']) or '当前保存记录未填写基调说明。', '',
                  '> 回放当前已保存研究判断，供本次数据核对；没有据此生成新的日报结论。', '']
    else:
        lines += ['尚无已保存研究结论；本报告只呈现事实和已保存研究逻辑。', '']
    lines += ['## 主要变化', '', '| 指标 | 首个有效值 | 末个有效值 | 变化 | 单位 | 数据日期 |',
              '| --- | --- | --- | --- | --- | --- |']
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
    if notes:
        lines += ['', *notes]
    lines += ['', '## 研究判断', '']
    judgment = view['judgment']
    if judgment:
        lines += ['- 研究状态：' + _md(judgment.get('status')),
                  '- 基调：' + _md(judgment.get('tone')), _md(judgment.get('contradiction')), '']
        for side in judgment['cases']:
            lines += ['### ' + side['label'], _md(side['claim'])]
            lines += ['- ' + _md(item['text']) + (' · ' + _md(item.get('name')) if item.get('name') else '')
                      for item in side['evidence']]
        for key, heading in [('thresholds', '区间与阈值'), ('falsifiers', '证伪条件')]:
            lines += ['', '### ' + heading]
            if view['rule_preview']:
                lines += ['> ' + _md(view['rule_preview']['label']) + '：' + _md(view['rule_preview']['notice'])]
            lines += ['- ' + _md(item['name']) + ' · ' + _md(item['series_name']) + '：' + _md(item['condition']) + ' · ' + _md(item['state'])
                      + ('（当前规则试算）' if item['is_preview'] else '')
                      + (' · 已记录：' + _md(item['recorded_state']) if item['recorded_state'] else '')
                      + (' · 依据日期：' + '、'.join(_md(value) for value in item['evidence_dates']) if item['evidence_dates'] else '')
                      for item in judgment[key]]
    else:
        lines.append('未登记研究判断。')
    lines += ['', '## 策略跟踪', '', '策略条件触发不等于实际成交记录。']
    if view['research_cards']:
        lines += ['', '### 当前保存的人工研究策略', '',
                  '以下为当前人工研究方案，不代表历史当时已知观点或实际成交；正文数字未逐项核验。']
    for card in view['research_cards']:
        lines += ['', '#### ' + _md(card['title']) + ' · ' + _md(card['direction_label']),
            '- 触发条件：' + _md(card['trigger']),
            '- 应对：' + (_md(card.get('response_action')) or '尚未填写 · 观察方案待补应对'),
            '- 暂停/撤回：' + _md(card['invalidation']), '- 复核日期：' + _md(card['review_date']),
            '- 补充范围：' + _md(card['thesis']), '- 执行前核对：' + _md(card['watch_for'])]
    for item in view['strategies']:
        lines += ['', '### ' + _md(item['title']) + ' · ' + _md(item['status_label']),
                  '- 类型：' + _md(item['mode_label']), '- 依据：' + _md(item.get('thesis')),
                  '- 失效/复核：' + _md(item.get('invalidation'))]
    if not view['strategies'] and not view['research_cards']:
        lines.append('尚无已保存策略；请先由研究员确认研究逻辑和具体合约。')
    for item in view['strategy_events']:
        lines.append('- ' + _md(item['at']) + ' · ' + _md(item['title']) + ' · ' + _md(item['label']) + '：' + _md(item['reason']))
    lines += ['', '## ' + ('下周待验证' if weekly else '下次研究待验证'), '']
    lines += ['- ' + _md(item['text']) for item in view['next_review']]
    if not view['next_review']:
        lines.append('- 暂无已保存验证条件，需研究员补充。')
    lines += ['', '## 数据缺口与来源', '']
    for item in view['gaps']:
        lines.append('- ' + _md(item.get('name') or item.get('series_id')) + '：' + _md(item.get('reason')))
    for item in view['observations']:
        lines.append('- ' + _md(item['name']) + ' · `' + _md(item['series_id']).replace('`', '') + '` · '
                     + _md(item.get('as_of')) + ' · ' + _md(item.get('source')))
    return '\n'.join(lines).rstrip() + '\n'


def readable_markdown(payload, *, title='研究报告', names=None):
    """The same four panels as HTML; manual omissions are never auto-filled."""
    view = present_snapshot(payload, names)
    if not view['is_manual_report']:
        original = _legacy_markdown(payload, title=title, names=names)
        pieces = original.split('\n## ')
        intro, sections = pieces[0], {}
        for piece in pieces[1:]:
            heading, _, content = piece.partition('\n')
            sections[heading] = content.strip()
        watch = sections.get('下周待验证') or sections.get('下次研究待验证') or ''
        source = sections.get('数据缺口与来源', '')
        return (intro.rstrip() + '\n\n## 摘要\n\n### 研究结论\n\n' + sections.get('研究结论', '')
            + '\n\n## 研判策略\n\n### 研究判断\n\n' + sections.get('研究判断', '')
            + '\n\n### 策略跟踪\n\n' + sections.get('策略跟踪', '')
            + '\n\n## 数据展板\n\n### 主要变化\n\n' + sections.get('主要变化', '')
            + '\n\n## 待关注\n\n' + watch
            + '\n\n<details><summary>数据缺口与来源</summary>\n\n## 数据缺口与来源\n\n'
            + source + '\n\n</details>\n')
    manual = view['manual_report']
    if manual.get('unavailable_references'):
        from fastapi import HTTPException
        raise HTTPException(409, '报告引用已不可读，请调整引用后再导出')
    saved = manual['draft']
    period = view.get('period') or {'start': saved['date'], 'end': saved['date']}
    lines = ['# ' + _md(saved['title']), '', '> 报告期：' + str(period.get('start')) + ' — ' + str(period.get('end')),
        '> 个人手工报告 v' + str(manual['revision']) + '；文字为人工内容，原始指标事实保持只读。', '']
    for key, panel in view['panels'].items():
        if not panel['visible']:
            continue
        lines += ['## ' + panel['label'], '']
        lines += [_md(line) for line in panel['text'].splitlines()]
        for ref in panel['references']:
            lines += ['', '### ' + _md(ref['title']) + ' · v' + str(ref['revision']),
                '> 引用已保存记录的明确版本；原文数字不据此变成核验事实。', '',
                *[_md(line) for line in (ref.get('body_text') or ref.get('summary') or '').splitlines()]]
        if key == 'dashboard':
            lines += ['', '| 指标 | 首个有效值 | 末个有效值 | 变化 | 单位 | 数据日期 |',
                '| --- | --- | --- | --- | --- | --- |']
            rows = view['changes'] or [{'name': row['name'], 'first_text': '—', 'last_text': _number(row.get('value')),
                'change_text': '—', 'unit': row.get('unit'), 'last': {'as_of': row.get('as_of')}} for row in view['observations']]
            for row in rows:
                first, last = row.get('first') or {}, row.get('last') or {}
                lines.append('| ' + ' | '.join(_md(value) for value in [row['name'], row['first_text'], row['last_text'],
                    row['change_text'], row.get('unit'), _plain(first.get('as_of')) + ' → ' + _plain(last.get('as_of'))]) + ' |')
            names = {row['series_id']: row['name'] for row in view['observations']}
            for figure in saved['figures']:
                lines += ['', '- 选图：' + _md(names.get(figure['series_id']) or '已引用指标')
                    + (' · ' + _md(figure.get('caption')) if figure.get('caption') else '')]
        lines.append('')
    lines += ['<details><summary>报告版本与来源</summary>', '',
        '- 记录：`' + _md(manual['id']) + '` · v' + str(manual['revision']),
        '- 来源版本：`' + _md(saved['source_revision']) + '`']
    for ref in saved.get('references') or []:
        lines.append('- 引用：' + _md(ref['kind']) + ' `' + _md(ref['id']) + '` · v' + str(ref['revision']))
    for row in view['observations']:
        lines.append('- ' + _md(row['name']) + ' · `' + _md(row['series_id']) + '` · ' + _md(row.get('as_of')))
    lines += ['', '</details>', '']
    return '\n'.join(lines)
