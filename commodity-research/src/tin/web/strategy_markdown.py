"""All strategy cards of a workbench as one editable Markdown document.

Export, edit, preview and apply. Applying re-reads the document and saves through the same card path as the
form (field validation, citation resolution and revision compare-and-swap). Cards that are not in the document
are left untouched; the document never deletes a card.
"""
from datetime import date
import json
import re

from fastapi import APIRouter, HTTPException, Request
from pydantic import ValidationError

from tin.integration.page_facts import page_read_scope
from tin.integration.workbench_scope import artifact_session, require_shared_write

router = APIRouter()
FORMAT = 'research-strategy-markdown.v1'
MAX_BYTES = 1024 * 1024
MAX_CARDS = 100
SECTIONS = (('trigger', '适用 / 触发'), ('response_action', '应对措施'), ('invalidation', '暂停 / 撤回'),
            ('thesis', '适用范围与目标'), ('watch_for', '执行前核对'), ('original_text', '原始研究文字'))
LABELS = dict(SECTIONS)
SECTION_KEYS = {label: key for key, label in SECTIONS}
CITATIONS = '引用'
DIRECTIONS = {'bull': '偏多观察', 'bear': '偏空观察', 'range': '区间观察', 'observe': '等待验证'}
# The 方向 line is this document's own syntax: labels, codes and their short forms.
DIRECTION_VALUES = {**{label: key for key, label in DIRECTIONS.items()}, **{key: key for key in DIRECTIONS},
    '偏多': 'bull', '看多': 'bull', '多': 'bull', '偏空': 'bear', '看空': 'bear', '空': 'bear',
    '区间': 'range', '震荡': 'range', '观察': 'observe', '等待': 'observe'}
KINDS = {'indicator': '指标', 'judgment_condition': '研判条件', 'knowledge_file': '知识库原件'}
IDENTITY = {'indicator': ('series_id', 'source_version', 'source_binding_id'),
    'judgment_condition': ('id', 'revision', 'rule_kind', 'rule_id'),
    'knowledge_file': ('source_binding_id', 'knowledge_id', 'file_version')}
HELP = ('<!-- ' + FORMAT + '：每个“## 方案名称”是一份方案。修改文字后在「md管理」中检查并导入；'
        '保留注释里的编号会更新原方案，删去编号则作为新方案导入。文档里没有的方案保持不变，不会被删除。 -->')

_CARD = re.compile(r'^<!--\s*card:\s*(\S+)\s+revision:\s*(\d+)\s*-->$')
_META = re.compile(r'^\s*[-*]\s*([^：:]+?)\s*[：:]\s*(.*?)\s*$')
_JUDGMENT = re.compile(r'<!--\s*judgment:\s*(\S+?)@(\d+)\s*-->')
_CITE = re.compile(r'<!--\s*cite:\s*(\{.*\})\s*-->\s*$')
_NOTE = re.compile(r'^<!--\s*待核对[：:]\s*(.*?)\s*-->$')
_DATE = re.compile(r'^(\d{4})\s*[-/.年]\s*(\d{1,2})\s*[-/.月]\s*(\d{1,2})\s*日?$')


def _one_line(value):
    return ' '.join(str(value or '').split())


def _escape(text):
    """A text line starting with '#' gets one more leading backslash, so it is never read as a heading."""
    return '\n'.join('\\' + line if re.match(r'^\\*#', line) else line for line in text.split('\n'))


def _unescape(line):
    return line[1:] if re.match(r'^\\+#', line) else line


def _identity(citation):
    return {key: citation[key] for key in ('kind', *IDENTITY.get(citation.get('kind'), ())) if key in citation}


def dump(items, judgment_labels):
    """Readable Markdown for saved cards; hidden comments keep each card's and reference's real identity."""
    lines = ['# 策略方案', '', HELP]
    for item in items:
        card = item['card']
        lines += ['', '## ' + _one_line(card['title'])]
        if item.get('id'):
            lines.append(f'<!-- card: {item["id"]} revision: {item["revision"]} -->')
        lines += ['- 方向：' + DIRECTIONS[card['direction']], '- 复核日期：' + (card.get('review_date') or '')]
        reference = card.get('judgment_reference')
        if reference:
            label = _one_line(judgment_labels.get((reference['id'], reference['revision'])) or '已引用研判')
            lines.append(f'- 依据研判：{label} <!-- judgment: {reference["id"]}@{reference["revision"]} -->')
        else:
            lines.append('- 依据研判：不引用')
        if not (card.get('response_action') or '').strip():
            lines.append('- 观察方案：是（暂未制定应对措施）')
        # Notes about an organized draft; shown as reminders when checked, gone once the line is deleted.
        for note in item.get('notes') or []:
            lines.append('<!-- 待核对：' + _one_line(note).replace('-->', '—>') + ' -->')
        for key, label in SECTIONS:
            text = card.get(key) or ''
            if text.strip():
                lines += ['', '### ' + label, _escape(text.strip('\n'))]
        citations = card.get('citations') or []
        if citations:
            lines += ['', '### ' + CITATIONS]
            for citation in citations:
                title = _one_line(((citation.get('snapshot') or {}).get('title')) or KINDS[citation['kind']])
                lines.append('- ' + LABELS.get(citation['field'], citation['field']) + ' · ' + KINDS[citation['kind']]
                    + '：' + title.replace('<!--', '‹!--') + ' <!-- cite: '
                    + json.dumps(_identity(citation), ensure_ascii=False, sort_keys=True, separators=(',', ':')) + ' -->')
    return '\n'.join(lines) + '\n'


def _review_date(value):
    match = _DATE.match(value.strip())
    if not match:
        raise ValueError
    return date(*map(int, match.groups())).isoformat()


def _citation(number, line):
    """One citation line, or a warning when it lacks the identity a new reference must be chosen with."""
    text = line.strip()
    if not text:
        return None, None
    found = _CITE.search(text)
    if not text.startswith(('-', '*')) or not found:
        return None, f'第 {number} 行的引用缺少导出编号，未导入；新引用请在表单中用“引用”选择。'
    head = text[1:found.start()].strip()
    label = head.split(' · ', 1)[0].strip()
    if label not in SECTION_KEYS:
        return None, f'第 {number} 行的引用没有写明所属文字项（如“适用 / 触发 · …”），未导入。'
    try:
        identity = json.loads(found.group(1))
    except ValueError:
        identity = None
    if not isinstance(identity, dict) or identity.get('kind') not in IDENTITY:
        return None, f'第 {number} 行的引用编号无效，未导入。'
    return {**identity, 'field': SECTION_KEYS[label]}, None


def parse(text):
    """Split the document into cards; each keeps what it states and the line-numbered problems found."""
    lines = text.replace('\r\n', '\n').replace('\r', '\n').split('\n')
    blocks, current = [], None
    for number, line in enumerate(lines, 1):
        if line.startswith('## '):
            current = {'line': number, 'title': line[3:].strip(), 'body': []}
            blocks.append(current)
        elif current is not None:
            current['body'].append((number, line))
    return [_parse_card(block) for block in blocks]


def _parse_card(block):
    card = {'line': block['line'], 'title': block['title'], 'id': None, 'revision': None, 'meta': {},
            'sections': {}, 'citations': [], 'warnings': [], 'errors': []}
    body, index = block['body'], 0
    while index < len(body) and not body[index][1].strip():
        index += 1
    if index < len(body) and (found := _CARD.match(body[index][1].strip())):
        card['id'], card['revision'] = found.group(1), int(found.group(2))
        index += 1
    section, buffer, opened = None, [], set()

    def flush():
        if section is None:
            return
        if section == CITATIONS:
            for number, line in buffer:
                citation, warning = _citation(number, line)
                if citation:
                    card['citations'].append(citation)
                if warning:
                    card['warnings'].append(warning)
            return
        rows = [_unescape(line) for _, line in buffer]
        while rows and not rows[0].strip():
            rows.pop(0)
        while rows and not rows[-1].strip():
            rows.pop()
        card['sections'][SECTION_KEYS[section]] = '\n'.join(rows)

    for number, line in body[index:]:
        heading = line[4:].strip() if line.startswith('### ') else None
        if heading in SECTION_KEYS or heading == CITATIONS:
            flush()
            if heading in opened:
                card['errors'].append(f'第 {number} 行重复出现“{heading}”，请合并为一节。')
            opened.add(heading)
            section, buffer = heading, []
            continue
        if section is not None:
            buffer.append((number, line))
            continue
        if not line.strip():
            continue
        if (note := _NOTE.match(line.strip())):
            card['warnings'].append('待核对：' + note.group(1))
            continue
        found = _META.match(line)
        if not found:
            card['warnings'].append(f'第 {number} 行未识别，已忽略：{_one_line(line)[:40]}')
            continue
        key, value = found.group(1).strip(), found.group(2)
        if key == '方向':
            direction = DIRECTION_VALUES.get(value.strip())
            if direction is None:
                card['errors'].append(f'第 {number} 行的方向无法识别：{value}（可写偏多观察、偏空观察、区间观察或等待验证）。')
            else:
                card['meta']['direction'] = direction
        elif key == '复核日期':
            try:
                card['meta']['review_date'] = _review_date(value)
            except ValueError:
                card['errors'].append(f'第 {number} 行的复核日期无效：{value}（格式如 2026-11-30）。')
        elif key == '依据研判':
            reference = _JUDGMENT.search(value)
            if reference:
                card['meta']['judgment_reference'] = {'kind': 'judgment', 'id': reference.group(1),
                                                      'revision': int(reference.group(2))}
            elif value.strip() in {'', '不引用', '无'}:
                card['meta']['judgment_reference'] = None
            else:
                card['meta']['judgment_reference'] = None
                card['warnings'].append(f'第 {number} 行的依据研判缺少导出编号，导入后不引用研判，可在表单中选择。')
        elif key == '观察方案':
            card['meta']['observation'] = value.strip().startswith('是')
        else:
            card['warnings'].append(f'第 {number} 行的“{key}”不是方案字段，已忽略。')
    flush()
    if not card['title']:
        card['errors'].append(f'第 {block["line"]} 行的方案名称为空。')
    return card


def _comparable(card):
    value = {key: card.get(key) for key in ('title', 'direction', 'review_date', 'judgment_reference',
                                            *(key for key, _ in SECTIONS))}
    for key, _ in SECTIONS:
        value[key] = (value[key] or '').strip('\n')
    value['citations'] = sorted(json.dumps({**_identity(item), 'field': item['field']}, sort_keys=True)
                                for item in card.get('citations') or [])
    return value


def _changes(before, after):
    labels = {'title': '方案名称', 'direction': '方向', 'review_date': '复核日期', 'judgment_reference': '依据研判',
              'citations': '引用', **LABELS}
    old, new = _comparable(before), _comparable(after)
    return [label for key, label in labels.items() if old.get(key) != new.get(key)]


_REQUIRED = {'title': '方案名称', 'trigger': '“适用 / 触发”', 'invalidation': '“暂停 / 撤回”', 'review_date': '复核日期'}


def _validation_messages(error):
    messages = []
    for item in error.errors(include_url=False):
        field = item['loc'][0] if item['loc'] else ''
        if field in _REQUIRED:
            messages.append('缺少' + _REQUIRED[field] + '。')
        elif field in LABELS:
            messages.append(f'“{LABELS[field]}”超过长度上限。')
        elif field == 'citations':
            messages.append('引用格式无效或超过 32 项。')
        else:
            messages.append('方案内容格式无效。')
    return list(dict.fromkeys(messages))


async def preview(scope, text):
    """Per-card plan for the document; the bodies to save stay server side and are rebuilt on apply."""
    from tin.web import research_strategy as strategy
    from tin.web.strategy_citations import resolve_citations
    if not isinstance(text, str) or not text.strip():
        raise HTTPException(422, '请先粘贴或导出策略 Markdown')
    parsed = parse(text)
    if not parsed:
        raise HTTPException(422, '没有找到以“## 方案名称”开头的方案')
    if len(parsed) > MAX_CARDS:
        raise HTTPException(413, f'一次最多导入 {MAX_CARDS} 份方案，请分批导入')
    seen, existing = set(), {}
    with artifact_session(scope) as session:
        for card in parsed:
            if card['id'] is None:
                continue
            if card['id'] in seen:
                card['errors'].append(f'第 {card["line"]} 行的方案编号在文档中重复出现，请删去其中一份的编号。')
                continue
            seen.add(card['id'])
            try:
                existing[card['id']] = strategy._read_card(session, scope, card['id'])
            except HTTPException:
                card['errors'].append('找不到这份方案或无权访问；如需另存，请删去编号注释后作为新方案导入。')
        visible = strategy._visible_card_ids(session, scope)
    results = []
    for card in parsed:
        current = existing.get(card['id'])
        base = (current or {}).get('card') or {}
        meta = card['meta']
        body = {'format': strategy.FORMAT, 'title': card['title'],
                'direction': meta.get('direction', base.get('direction', 'observe')),
                'review_date': meta.get('review_date', base.get('review_date') if current else None),
                'judgment_reference': meta['judgment_reference'] if 'judgment_reference' in meta
                    else base.get('judgment_reference'),
                'citations': card['citations']}
        for key, _ in SECTIONS:
            body[key] = card['sections'].get(key, '')
        if base.get('reference'):
            body['reference'] = base['reference']
        observation = not body['response_action'].strip()
        if observation and not meta.get('observation'):
            card['warnings'].append('未填写应对措施，将保存为观察方案。')
        body = {key: value for key, value in body.items() if value is not None}
        errors = list(card['errors'])
        for key in ('title', 'trigger', 'invalidation'):
            if key not in body or not str(body.get(key) or '').strip():
                errors.append('缺少' + _REQUIRED[key] + '。')
        model = None
        try:
            model = strategy.ResearchStrategyCard.model_validate(body)
        except ValidationError as error:
            errors.extend(message for message in _validation_messages(error) if message not in errors)
        action = 'create'
        if current is not None:
            action = 'update'
            if not current.get('can_edit') or current.get('card') is None:
                errors.append('这份方案当前不能修改（历史记录或无编辑权限）；如需另存，请删去编号注释后导入。')
            elif current['revision'] != card['revision']:
                errors.append(f'这份方案已在别处更新（当前第 {current["revision"]} 版），请重新导出后再修改。')
            elif model is not None and not _changes(base, body):
                action = 'unchanged'
        elif card['id'] is not None:
            action = 'error'
        if model is not None and not errors and action != 'unchanged':
            try:
                if body.get('citations'):
                    await resolve_citations(scope, body['citations'])
                if body.get('judgment_reference'):
                    with artifact_session(scope) as session:
                        strategy._validate_reference(session, scope,
                            strategy.JudgmentReference.model_validate(body['judgment_reference']))
            except HTTPException as error:
                errors.append(str(error.detail) if isinstance(error.detail, str) else '引用或依据研判已不可用，请重新选择。')
        results.append({'line': card['line'], 'title': card['title'] or '（未命名）', 'card_id': card['id'],
            'action': 'error' if errors else action,
            'changes': _changes(base, body) if action == 'update' and not errors else [],
            'warnings': card['warnings'], 'errors': errors,
            '_input': None if errors or action == 'unchanged' else {'card': body,
                'source_ids': (current or {}).get('source_ids') or [], 'acknowledge_observation': observation,
                **({'revision': current['revision']} if current else {})}})
    mentioned = {card['id'] for card in parsed if card['id']}
    counts = {name: sum(1 for row in results if row['action'] == name)
              for name in ('create', 'update', 'unchanged', 'error')}
    return {'cards': results, 'counts': counts,
            'untouched': sum(1 for identifier, state in visible if state == 'current' and identifier not in mentioned),
            'can_apply': not counts['error'] and bool(counts['create'] + counts['update'])}


def public(plan):
    return {**plan, 'cards': [{key: value for key, value in row.items() if not key.startswith('_')}
                              for row in plan['cards']]}


async def _read_markdown(request):
    raw = bytearray()
    async for chunk in request.stream():
        raw.extend(chunk)
        if len(raw) > MAX_BYTES + 4096:
            raise HTTPException(413, '策略 Markdown 超过 1 MiB，请分批导入')
    try:
        value = json.loads(raw.decode('utf-8'))
    except (UnicodeDecodeError, ValueError):
        raise HTTPException(422, '请求格式无效') from None
    if not isinstance(value, dict) or set(value) != {'markdown'} or not isinstance(value['markdown'], str):
        raise HTTPException(422, '请求格式无效')
    if len(value['markdown'].encode('utf-8')) > MAX_BYTES:
        raise HTTPException(413, '策略 Markdown 超过 1 MiB，请分批导入')
    return value['markdown']


def _writable_scope():
    from tin.web.research_strategy import require_card_scope
    scope = require_card_scope()
    require_shared_write(scope)
    return scope


@router.get('/markdown')
def export_markdown():
    from tin.web import research_strategy as strategy
    scope = strategy.require_card_scope()
    with artifact_session(scope) as session:
        data = strategy.build_context(session, scope, limit=100)
        items = [item for item in data['items'] if item['card'] is not None and item['scope_status'] == 'current']
        labels = {}
        for item in items:
            reference = item['card'].get('judgment_reference')
            if reference and (reference['id'], reference['revision']) not in labels:
                labels[(reference['id'], reference['revision'])] = strategy._judgment_label(session, scope, reference)
    return {'markdown': dump(items, labels), 'count': len(items), 'total': data['total'],
            'skipped': len(data['items']) - len(items), 'truncated': data['truncated'],
            'can_import': scope.can_edit}


@router.post('/markdown/preview')
async def preview_markdown(request: Request):
    scope = _writable_scope()
    return public(await preview(scope, await _read_markdown(request)))


@router.post('/markdown/apply')
async def apply_markdown(request: Request):
    from tin.web import research_strategy as strategy
    from tin.web.workbench_artifacts import ArtifactModule, create_artifact, update_artifact
    scope = _writable_scope()
    plan = await preview(scope, await _read_markdown(request))
    if not plan['can_apply']:
        raise HTTPException(422, {'message': '文档中仍有需要修改的方案，未导入任何内容' if plan['counts']['error']
            else '文档与当前方案一致，没有需要导入的修改', 'preview': public(plan)})
    saved = []
    for row in plan['cards']:
        if row['_input'] is None:
            continue
        body = strategy.CardInput.model_validate(row['_input'])
        with page_read_scope('strategies', series_ids=[]):
            if row['action'] == 'update':
                record = await update_artifact(ArtifactModule.strategies, row['card_id'], strategy.artifact_input(body), scope)
            else:
                record = await create_artifact(ArtifactModule.strategies, strategy.artifact_input(body), scope)
        saved.append({**strategy.serialize_card(record), 'action': row['action']})
    return {'saved': saved, 'counts': plan['counts'], 'untouched': plan['untouched']}
