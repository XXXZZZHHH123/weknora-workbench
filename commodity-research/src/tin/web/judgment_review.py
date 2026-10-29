"""Explicit, source-checked item decisions over a private model preview."""
from copy import deepcopy
from datetime import date, datetime, timezone
import hashlib
import json
from typing import Literal

from fastapi import APIRouter, HTTPException, Query, Request
from pydantic import BaseModel, ConfigDict, Field
from sqlalchemy import select
from starlette.concurrency import run_in_threadpool

from tin.integration.page_facts import catalog_page
from tin.integration.workbench_scope import artifact_session, require_shared_write, resolve_workbench
from tin.judgments.analysis import AnalysisError, validate_analysis
from tin.judgments.markdown import dump_markdown, version_diff
from tin.judgments.service import latest, latest_workbench_payload
from tin.models import AuditLog, SourceMapping
from tin.ownership import verified_actor
from tin.web import judgment_generation as generation

router = APIRouter(prefix='/api/sn/judgment/review')

SECTIONS = [
    ('contradiction', '主矛盾'), ('marginal_focus', '边际关注'), ('cost_line', '成本线'),
    ('pricing_power', '定价权'), ('time_dimension', '时间维度'), ('survey_notes', '调研备注'),
    ('whitelist', '指标范围'), ('paradigm', '研判范式'), ('tone', '研究基调'),
    ('thresholds', '阈值条件'), ('falsifiers', '证伪条件'), ('unresolved', '待补充内容')]


class ReviewRequest(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    draft_id: str = Field(min_length=16, max_length=128)
    revision: int = Field(ge=1)


class UpdateRequest(ReviewRequest):
    item_id: str = Field(min_length=1, max_length=80)
    action: Literal['confirm', 'keep_text', 'exclude']
    series_id: str | None = Field(default=None, max_length=256)
    source_quote: str | None = Field(default=None, max_length=4000)
    remember: bool = False


class ReuseRequest(ReviewRequest):
    enabled: bool


def digest(value):
    return hashlib.sha256(json.dumps(value, ensure_ascii=False, sort_keys=True,
        separators=(',', ':')).encode()).hexdigest()


def binding(field):
    if field is None:
        return None
    return {'series_id': field['id'], 'name': field.get('name') or '',
        **{key: field.get(key) for key in ('unit', 'source_file', 'sheet')}}


def field_catalog(metadata):
    return [{'id': field['series_id'], 'name': field.get('metric') or field.get('name') or '',
        **{key: field.get(key) for key in ('unit', 'source_file', 'sheet', 'frequency', 'dimensions',
            'source_kind', 'source_version', 'knowledge_base_id', 'dataset_id', 'source_knowledge_id',
            'source_series_id', 'source_binding_id')}} for field in metadata['catalog'].values()]


def describe(value):
    if isinstance(value, str):
        return value
    if isinstance(value, dict):
        return '\n'.join(str(value[key]) for key in ('claim', 'desc', 'name', 'text', 'note', 'reason')
            if isinstance(value.get(key), str) and value[key].strip())
    return ''


def requires_binding(item):
    return item['kind'] in ('thresholds', 'falsifiers', 'whitelist') or item['path'][0] == 'marginal_focus'


def attention_fields(issues):
    fields = []
    for issue in issues:
        code = issue.get('code', '')
        if code in {'missing_binding', 'unknown_series_id'} or code.startswith('unit_'):
            field = 'binding'
        elif code.startswith('quote_') or code in {'business_condition_not_in_source', 'business_condition_missing'}:
            field = 'source_quote'
        else:
            field = 'condition'
        if field not in fields:
            fields.append(field)
    return fields


def attention_message(fields):
    if 'binding' in fields and 'content' not in fields:
        return '请选择口径一致的指标。'
    if 'source_quote' in fields:
        return '请选取支持此条件的原文。'
    if 'condition' in fields:
        return '条件缺少可核验数字或格式，请补充原文或保留文字。'
    return '此项可保留文字，也可补充关联指标。'


def reject_fields(fields):
    raise HTTPException(422, {'message': attention_message(fields), 'attention_fields': fields})


def initialize_review(preview, assessment, catalog):
    candidate = deepcopy(assessment.candidate_document or assessment.document)
    baseline = deepcopy(assessment.document)
    # Quarantine records are structured here. Audit prose is never reparsed.
    baseline['unresolved'] = deepcopy(baseline['unresolved'][:len(candidate['unresolved'])])
    review = {'revision': 1, 'reuse_enabled': False, 'items': [], 'audit': [],
              'baseline': baseline, 'candidate': candidate,
              'candidate_history': deepcopy(assessment.candidate_history or [candidate]),
              'catalog': deepcopy(catalog)}
    preview['review'] = review
    known = {field['id']: field for field in catalog}

    def add(section, path, value, title, *, kind='description', sid=None, quote='', warnings=None):
        source_quote = quote if isinstance(quote, str) and quote.strip() and quote in preview['text'] else ''
        item = {'id': 'item-' + digest(path)[:24], 'section': section, 'path': path,
            'value': deepcopy(value), 'title': title, 'kind': kind, 'description': describe(value),
            'status': 'pending', 'series_id': sid, 'ai_series_id': sid, 'source_quote': source_quote,
            'warnings': list(dict.fromkeys(warnings or [])), 'remembered': False,
            'binding_explicit': False, 'requires_explicit_binding': False}
        review['items'].append(item)

    fields = baseline['judgment']
    contradiction = fields.get('contradiction', {})
    for side, label in [('bull', '多方'), ('bear', '空方')]:
        part = contradiction.get(side, {})
        if part.get('claim'):
            add('contradiction', ['contradiction', side, 'claim'], part['claim'], label + '主张')
        for index, evidence in enumerate(part.get('evidence', [])):
            add('contradiction', ['contradiction', side, 'evidence', index], evidence,
                label + '依据', kind='binding', sid=evidence.get('series_id'))
    for name, label in [('text', '主矛盾说明'), ('window', '适用时间')]:
        if contradiction.get(name):
            value = contradiction[name]
            add('contradiction', ['contradiction', name], value, label)
            if name == 'window':
                review['items'][-1]['description'] = '；'.join(str(v) for v in value.values() if v)
    for index, value in enumerate(fields.get('marginal_focus', [])):
        add('marginal_focus', ['marginal_focus', index], value, '边际关注',
            kind='binding', sid=value.get('series_id'))
    cost = fields.get('cost_line') or {}
    for name, label in [('type', '成本口径'), ('note', '成本说明')]:
        if cost.get(name):
            add('cost_line', ['cost_line', name], cost[name], label)
            if name == 'type':
                review['items'][-1]['description'] = {'cash': '现金成本', 'full': '完全成本'}[cost[name]]
    for index, value in enumerate(cost.get('items', [])):
        add('cost_line', ['cost_line', 'items', index], value, '成本依据', kind='binding', sid=value.get('series_id'))
    for section in ('pricing_power', 'time_dimension'):
        labels = {'category': '市场分类', 'short': '短期', 'long': '中长期', 'key': '关键说明', 'sources': '来源说明'}
        for name, value in (fields.get(section) or {}).items():
            if not value:
                continue
            if isinstance(value, list):
                for index, text in enumerate(value):
                    add(section, [section, name, index], text, labels.get(name, '研究说明'))
            else:
                add(section, [section, name], value, labels.get(name, '研究说明'))
    for name, label in [('survey_notes', '调研备注'), ('paradigm', '研判范式'), ('tone', '研究基调'), ('tone_note', '基调依据')]:
        if fields.get(name):
            add('tone' if name == 'tone_note' else name, [name], fields[name], label)
    for index, sid in enumerate(fields.get('whitelist', [])):
        add('whitelist', ['whitelist', index], known[sid].get('name') or '指标引用', '研究指标', kind='whitelist', sid=sid)
    if fields.get('priority_rule'):
        add('thresholds', ['priority_rule'], fields['priority_rule'], '条件优先顺序')
        names = {rule['id']: rule.get('name') or '阈值条件' for rule in fields.get('thresholds', [])}
        review['items'][-1]['description'] = ' → '.join(names.get(identifier, '阈值条件') for identifier in fields['priority_rule']['order'])
    for kind, title in [('thresholds', '阈值条件'), ('falsifiers', '证伪条件')]:
        for index, value in enumerate(candidate['judgment'].get(kind, [])):
            warnings = [issue['message'] for issue in assessment.issues if issue.get('rule_id') == value['id']]
            add(kind, [kind, index], value, title, kind=kind, sid=value.get('series_id'),
                quote=candidate['rule_quotes'].get(value['id']), warnings=warnings)
            review['items'][-1]['requires_explicit_binding'] = any(issue.get('rule_id') == value['id']
                and issue.get('code') in {'unit_mismatch', 'unit_coverage'} for issue in assessment.issues)
    for index, value in enumerate(baseline['unresolved']):
        add('unresolved', ['unresolved', index], value, value.get('section') or '待补充内容',
            kind='unresolved_binding', sid=value.get('series_id'),
            quote=value.get('business_condition') or (value.get('text') if value.get('text') in preview['text'] else ''))
        review['items'][-1]['description'] = 'AI建议说明：' + review['items'][-1]['description']
    valid_rules = {kind: {rule['id'] for rule in baseline['judgment'].get(kind, [])}
        for kind in ('thresholds', 'falsifiers')}
    for item in review['items']:
        path = ('/unresolved/' + '/'.join(map(str, item['path'][1:])) if item['section'] == 'unresolved'
            else '/judgment/' + '/'.join(map(str, item['path'])))
        related = [issue for issue in assessment.issues if issue.get('path') == path
            or issue.get('path', '').startswith(path + '/')
            or item['kind'] in valid_rules and issue.get('rule_id') == item['value']['id']
            or item['series_id'] and issue.get('rule_id') is None
                and issue.get('path') == '/binding_units/' + item['series_id']]
        item['warnings'] = list(dict.fromkeys(item['warnings'] + [issue['message'] for issue in related]))
        informational_units = {'unit_metadata_missing', 'descriptive_unit_metadata'}
        reasons = [issue['message'] for issue in related if issue.get('severity') in {'error', 'warning'}
            and not (item['kind'] in {'binding', 'whitelist', 'unresolved_binding'}
                and issue.get('code') in informational_units and issue.get('rule_id') is None)]
        if item['kind'] == 'unresolved_binding':
            reasons.insert(0, '模型将此项列为待补充内容，可补充指标或保留文字。')
        if requires_binding(item) and not item['series_id']:
            reasons.append('此结构条目需要关联真实指标，当前尚未关联。')
        if item['kind'] == 'binding' and not requires_binding(item) and not item['series_id']:
            item['warnings'].append('未关联指标，可按需要选择；当前保留为描述文字。')
        if item['kind'] in valid_rules and item['value']['id'] not in valid_rules[item['kind']]:
            reasons.append('此量化候选未通过原文、数字、单位与指标校验，当前不执行。')
        item['initial_attention_reasons'] = list(dict.fromkeys(reasons))
        item['initial_attention_fields'] = attention_fields([issue for issue in related
            if issue.get('severity') == 'error'])
        if requires_binding(item) and not item['series_id']:
            item['initial_attention_fields'].append('binding')
        if item['kind'] == 'unresolved_binding':
            item['initial_attention_fields'] = ['content'] + ([] if item['series_id'] else ['binding'])
        if reasons and not item['initial_attention_fields']:
            item['initial_attention_fields'] = ['condition']
        item['status'] = item['initial_status'] = 'pending' if reasons else 'ready'
        item['origin'] = 'model'
    preview['document'] = build_document(preview, catalog)
    if len(preview['document']['unresolved']) > 32:
        raise HTTPException(422, '待确认内容超过本次32项容量，请缩小资料；未静默丢失模型候选')


def at_path(value, path):
    for name in path:
        value = value[name]
    return value


def set_path(value, path, replacement):
    parent = at_path(value, path[:-1])
    parent[path[-1]] = replacement


def prune(value):
    if isinstance(value, list):
        return [prune(child) for child in value if child is not None]
    if isinstance(value, dict):
        return {key: prune(child) for key, child in value.items()}
    return value


def note_for(item, reason):
    text = item.get('source_quote') or item['description'] or '研究条件待补充，请对照完整原文。'
    return {'section': item['title'], 'text': text, 'reason': reason,
            'business_condition': None, 'rule_kind': None}


def build_document(preview, catalog):
    review = preview['review']
    document = deepcopy(review['baseline'])
    fields = document['judgment']
    document['rule_quotes'] = {}
    notes = []
    fields.update(thresholds=[], falsifiers=[])
    valid = {kind: {rule['id'] for rule in review['baseline']['judgment'].get(kind, [])}
             for kind in ('thresholds', 'falsifiers')}
    for item in review['items']:
        status, kind = item['status'], item['kind']
        if kind in ('thresholds', 'falsifiers'):
            rule = deepcopy(item['value'])
            if status == 'excluded':
                continue
            if status == 'kept_text' or status in {'pending', 'ready'} and rule['id'] not in valid[kind]:
                notes.append(note_for(item, '已保留为文字，未作为量化规则执行。' if status == 'kept_text'
                    else '待核对原文、数字、单位和指标；当前不作为量化规则执行。'))
                continue
            if status == 'confirmed':
                rule['series_id'] = item['series_id']
            fields[kind].append(rule)
            document['rule_quotes'][rule['id']] = item['source_quote']
            continue
        root = document['unresolved'] if item['section'] == 'unresolved' else fields
        path = item['path'][1:] if item['section'] == 'unresolved' else item['path']
        if status == 'excluded':
            set_path(root, path, None if isinstance(path[-1], int) or kind in ('binding', 'whitelist') else
                     '' if isinstance(item['value'], str) and path[-1] in ('claim', 'text') else None)
        elif kind in ('binding', 'unresolved_binding'):
            value = deepcopy(item['value'])
            value['series_id'] = (None if status == 'kept_text' else item['ai_series_id']
                if status in {'pending', 'ready'} and item['remembered'] else item['series_id'])
            set_path(root, path, value)
        elif kind == 'whitelist':
            sid = item['ai_series_id'] if status in {'pending', 'ready'} and item['remembered'] else item['series_id']
            set_path(root, path, None if status == 'kept_text' else sid)
            if status == 'kept_text':
                notes.append(note_for(item, '仅保留指标说明，不列为绑定指标。'))
        elif status == 'kept_text' and item['section'] != 'unresolved':
            # Free prose already has descriptive semantics. Enum fields need a
            # textual record rather than a silently retained classification.
            if item['path'][-1] in ('type', 'category', 'paradigm', 'tone'):
                set_path(root, path, None)
                notes.append(note_for(item, '用户保留为文字说明。'))
    document = prune(document)
    document['unresolved'].extend(notes)
    identifiers = {rule['id'] for rule in document['judgment']['thresholds']}
    priority = document['judgment'].get('priority_rule')
    if priority:
        order = [identifier for identifier in priority['order'] if identifier in identifiers]
        document['judgment']['priority_rule'] = {**priority, 'order': order} if order else None
    known = {field['id']: field for field in catalog}
    wanted = set(document['judgment'].get('whitelist', []))

    def refs(value):
        if isinstance(value, dict):
            for key, child in value.items():
                if key == 'series_id' and child is not None:
                    wanted.add(child)
                refs(child)
        elif isinstance(value, list):
            for child in value:
                refs(child)
    refs(document['judgment'])
    refs(document['unresolved'])
    document['binding_units'] = {sid: known[sid].get('unit') for sid in sorted(wanted) if sid in known}
    return document


def checked(preview, document, catalog, scope):
    return validate_analysis(document, preview['text'], catalog, variety=preview['variety'],
        actor=scope.identity.user_id, written_at=date.fromisoformat(preview['written_at']))


def confirmation_issues(preview, item, catalog, scope):
    trial = {**preview, 'review': {**preview['review'], 'items': [
        {**row, 'status': 'confirmed'} if row['id'] == item['id'] else row
        for row in preview['review']['items']]}}
    if requires_binding(item) and not item['series_id']:
        return [{'code': 'missing_binding', 'message': '请先选择当前授权目录中的指标。'}]
    if item['requires_explicit_binding'] and not item['binding_explicit']:
        return [{'code': 'unit_mismatch', 'message': '模型推荐的单位声明与目录不一致，请重新选择真实指标并核对原单位。'}]
    try:
        checked(trial, build_document(trial, catalog), catalog, scope)
    except AnalysisError as error:
        return error.issues
    return []


def confirmation_error(preview, item, catalog, scope):
    return list(dict.fromkeys(issue['message'] for issue in confirmation_issues(preview, item, catalog, scope)))


def review_response(scope, token, preview, catalog):
    known = {field['id']: field for field in catalog}
    sections = []
    for section_id, title in SECTIONS:
        items = []
        for item in preview['review']['items']:
            if item['section'] != section_id:
                continue
            issues = confirmation_issues(preview, item, catalog, scope)
            errors = list(dict.fromkeys(issue['message'] for issue in issues))
            fields = []
            if item['status'] == 'pending':
                fields = list(item['initial_attention_fields']) + attention_fields(issues)
                if item['remembered'] and item['series_id'] != item['ai_series_id']:
                    fields.append('binding')
                fields = list(dict.fromkeys(fields))
            rule = None
            if item['kind'] in ('thresholds', 'falsifiers'):
                value = item['value']
                rule = {key: value[key] for key in ('operator', 'type', 'horizon', 'expression', 'side', 'consecutive_periods') if key in value}
                if 'value' in value:
                    rule['value_literal'] = json.dumps(value['value'], ensure_ascii=False, separators=(',', ':'))
            items.append({'id': item['id'], 'kind': item['kind'], 'title': item['title'],
                'description': item['description'], 'original_text': item['source_quote'],
                'source_quote': item['source_quote'], 'status': item['status'],
                'origin': item['origin'], 'requires_attention': item['status'] == 'pending',
                'attention_fields': fields,
                'attention_reasons': [attention_message(fields)] if fields else [],
                'requires_quote': item['kind'] in ('thresholds', 'falsifiers'),
                'warnings': errors if errors else item['warnings'] if item['status'] == 'pending' else [],
                'binding': binding(known.get(item['series_id'])),
                'ai_binding': binding(known.get(item['ai_series_id'])),
                'requires_binding': requires_binding(item),
                'can_bind': item['kind'] in ('binding', 'unresolved_binding', 'whitelist', 'thresholds', 'falsifiers'),
                'can_confirm': not errors, 'can_confirm_with_binding': item['kind'] in ('binding', 'unresolved_binding', 'whitelist', 'thresholds', 'falsifiers'),
                'rule': rule, 'remembered': item['remembered']})
        if items:
            sections.append({'id': section_id, 'title': title, 'items': items})
    items = preview['review']['items']
    return {'draft_id': token, 'revision': preview['review']['revision'], 'sections': sections,
        'pending_count': sum(item['status'] == 'pending' for item in items),
        'needs_attention_count': sum(item['status'] == 'pending' for item in items),
        'ready_count': sum(item['status'] == 'ready' for item in items),
        'confirmed_count': sum(item['status'] == 'confirmed' for item in items), 'total': len(items),
        'reuse_enabled': preview['review']['reuse_enabled']}


def preview_response(scope, token, preview, state):
    catalog = state['catalog']
    result = checked(preview, preview['document'], catalog, scope)
    known = {field['id']: field for field in catalog}
    return {'draft_id': token, 'base_version': preview['base_version'], 'source_text': preview['text'],
        'markdown': dump_markdown(result.payload), 'diff': version_diff(state['base_payload'], result.payload),
        'bindings': [{key: value for key, value in binding(known[sid]).items() if key in ('series_id', 'name', 'unit')}
            for sid in result.binding_units], 'unresolved': result.unresolved,
        'catalog_total': len(catalog), 'candidate_count': preview.get('candidate_count', len(catalog)),
        'candidate_subset': preview.get('candidate_subset', False), 'validation_issues': preview['validation_issues'],
        'repair_attempts': preview['repair_attempts'], 'review_available': True,
        'review': review_response(scope, token, preview, catalog),
        'notice': '可逐项核对并保存包含待确认项的新草稿；未通过校验的量化候选不执行，保存后不会自动生效。'}


def review_snapshot(scope, preview, page_query=None):
    metadata = catalog_page(scope, **(page_query or {'limit': 1}), version=preview['fact_version'])
    with artifact_session(scope) as session:
        row = latest(session, preview['variety'])
        base_version = row.version if row else 0
        if metadata['version'] != preview['fact_version'] or base_version != preview['base_version']:
            raise HTTPException(409, '研究逻辑版本或知识库来源已改变，请重新分析')
        # The private preview holds the complete authorized immutable catalog
        # used for generation. A current version check makes its metadata safe
        # to reuse without downloading that catalog on every keystroke.
        return {'catalog': preview['review']['catalog'], 'page': metadata,
            'base_payload': latest_workbench_payload(session, preview['variety'], scope)}


async def current_preview(scope, token, page_query=None):
    preview = generation.previews.get(scope, token)
    verified = await resolve_workbench(scope.identity, scope.workbench_id, scope.authorization)
    require_shared_write(verified)
    if (verified.restricted or verified.metadata.get('can_read_source') is False
            or generation.scope_key(verified) != generation.scope_key(scope)
            or verified.additional_knowledge_bases != scope.additional_knowledge_bases):
        raise HTTPException(409, '研究工作台来源或权限已改变，请重新分析')
    state = await run_in_threadpool(review_snapshot, verified, preview, page_query)
    return preview, state


def preference_key(scope, preview, item):
    return digest(['judgment-review-binding.v1', scope.identity.tenant_id, scope.identity.user_id,
        scope.workbench_id, preview['variety'], digest(preview['text']), item['path'],
        digest(item['value'])])


def preference_signature(scope, field):
    return digest({'grant': generation.scope_key(scope), 'field': {key: field.get(key) for key in
        ('id', 'name', 'unit', 'sheet', 'source_file', 'frequency', 'dimensions', 'source_kind',
         'source_version', 'knowledge_base_id')}})


def record_decisions(scope, token, before, preview, catalog, decisions):
    known = {field['id']: field for field in catalog}
    now = datetime.now(timezone.utc)
    with artifact_session(scope) as session:
        for item, remember in decisions:
            if remember:
                field = known[item['series_id']]
                key = preference_key(scope, preview, item)
                config = {'kind': 'judgment-review-binding.v1', 'tenant_id': scope.identity.tenant_id,
                    'user_id': scope.identity.user_id, 'workbench_id': scope.workbench_id,
                    'variety': preview['variety'], 'series_id': item['series_id'],
                    'signature': preference_signature(scope, field)}
                row = session.get(SourceMapping, key)
                if row is None:
                    session.add(SourceMapping(id=key, config=config, version=1,
                        confirmed_by=scope.identity.user_id, confirmed_at=now))
                else:
                    row.config = config
                    row.version += 1
                    row.confirmed_by = scope.identity.user_id
                    row.confirmed_at = now
            detail = {'workbench_id': scope.workbench_id, 'draft_sha256': digest(token),
                'revision': preview['review']['revision'], 'item_id': item['id'],
                'action': item['status'], 'series_id': item['series_id'], 'remember': remember,
                'source_quote': item['source_quote'], 'source_quote_sha256': digest(item['source_quote'])}
            preview['review']['audit'].append(detail)
            session.add(AuditLog(at=now, actor=verified_actor(session, ''), action='研判逐项确认',
                target_type='judgment_preview', target_id=digest(token), detail=detail))
        generation.previews.replace(scope, token, preview, before['review']['revision'])
        try:
            session.commit()
        except Exception:
            session.rollback()
            generation.previews.replace(scope, token, before, preview['review']['revision'])
            raise


def check_revision(preview, revision):
    if preview['review']['revision'] != revision:
        raise HTTPException(409, '逐项确认已更新，请刷新后继续')


@router.get('')
async def get_review(draft_id: str = Query(min_length=16, max_length=128)):
    scope = generation.scope_for_analysis(write=True)
    preview, state = await current_preview(scope, draft_id)
    return review_response(scope, draft_id, preview, state['catalog'])


@router.get('/indicators')
async def indicators(draft_id: str = Query(min_length=16, max_length=128),
                     search: str = Query(default='', max_length=200),
                     offset: int = Query(default=0, ge=0, le=100000),
                     limit: int = Query(default=20, ge=1, le=50)):
    scope = generation.scope_for_analysis(write=True)
    _, state = await current_preview(scope, draft_id, {'offset': offset, 'limit': limit, 'search': search})
    page = state['page']
    fields = field_catalog({'catalog': {field['series_id']: field for field in page['series']}})
    return {'items': [binding(field) for field in fields], 'total': page['total'],
            'next_offset': page.get('next_offset')}


@router.post('/update')
async def update_review(request: Request):
    scope = generation.scope_for_analysis(write=True)
    body = await generation.read_body(request, UpdateRequest)
    async with generation.previews.mutation(scope, body.draft_id):
        before, state = await current_preview(scope, body.draft_id)
        check_revision(before, body.revision)
        preview = deepcopy(before)
        item = next((item for item in preview['review']['items'] if item['id'] == body.item_id), None)
        if item is None:
            raise HTTPException(404, '待确认条目不存在，请刷新预览')
        can_bind = item['kind'] in ('binding', 'unresolved_binding', 'whitelist', 'thresholds', 'falsifiers')
        known = {field['id']: field for field in state['catalog']}
        if 'series_id' in body.model_fields_set:
            if not can_bind or body.series_id is not None and body.series_id not in known:
                reject_fields(['binding'])
            item['series_id'] = body.series_id
            item['remembered'] = False
            item['binding_explicit'] = body.series_id is not None
        if body.source_quote is not None:
            if not body.source_quote.strip() or body.source_quote not in preview['text']:
                reject_fields(['source_quote'])
            item['source_quote'] = body.source_quote
        if body.remember and (body.action != 'confirm' or not can_bind or not item['series_id']):
            raise HTTPException(422, '只有明确确认绑定指标时才可勾选记住')
        item['status'] = {'confirm': 'confirmed', 'keep_text': 'kept_text', 'exclude': 'excluded'}[body.action]
        item['origin'] = 'user'
        if body.action == 'confirm':
            issues = confirmation_issues(preview, item, state['catalog'], scope)
            if issues:
                reject_fields(attention_fields(issues))
        try:
            preview['document'] = build_document(preview, state['catalog'])
            checked(preview, preview['document'], state['catalog'], scope)
        except AnalysisError as error:
            reject_fields(attention_fields(error.issues))
        preview['review']['revision'] += 1
        await current_preview(scope, body.draft_id)
        await run_in_threadpool(record_decisions, scope, body.draft_id, before, preview, state['catalog'], [(item, body.remember)])
        return preview_response(scope, body.draft_id, preview, state)


@router.post('/confirm-ready')
async def confirm_ready(request: Request):
    scope = generation.scope_for_analysis(write=True)
    body = await generation.read_body(request, ReviewRequest)
    async with generation.previews.mutation(scope, body.draft_id):
        before, state = await current_preview(scope, body.draft_id)
        check_revision(before, body.revision)
        preview, decisions = deepcopy(before), []
        for item in preview['review']['items']:
            if item['status'] in {'pending', 'ready'} and not confirmation_error(preview, item, state['catalog'], scope):
                item['status'] = 'confirmed'
                item['origin'] = 'user'
                decisions.append((item, False))
        preview['document'] = build_document(preview, state['catalog'])
        checked(preview, preview['document'], state['catalog'], scope)
        preview['review']['revision'] += 1
        await current_preview(scope, body.draft_id)
        await run_in_threadpool(record_decisions, scope, body.draft_id, before, preview, state['catalog'], decisions)
        return preview_response(scope, body.draft_id, preview, state)


def apply_preferences(scope, preview, catalog):
    known = {field['id']: field for field in catalog}
    with artifact_session(scope) as session:
        items = [item for item in preview['review']['items'] if item['status'] in {'pending', 'ready'}
            and item['kind'] in ('binding', 'unresolved_binding', 'whitelist', 'thresholds', 'falsifiers')]
        keys = {preference_key(scope, preview, item): item for item in items}
        rows = session.scalars(select(SourceMapping).where(SourceMapping.id.in_(keys))) if keys else []
        for row in rows:
            item, config = keys[row.id], row.config or {}
            field = known.get(config.get('series_id'))
            if (row.confirmed_by == scope.identity.user_id and config.get('kind') == 'judgment-review-binding.v1'
                    and config.get('tenant_id') == scope.identity.tenant_id
                    and config.get('user_id') == scope.identity.user_id
                    and config.get('workbench_id') == scope.workbench_id
                    and config.get('variety') == preview['variety'] and field is not None
                    and config.get('signature') == preference_signature(scope, field)):
                item['series_id'] = field['id']
                item['remembered'] = True
                item['origin'] = 'remembered'
                item['status'] = 'pending' if field['id'] != item['ai_series_id'] else item['initial_status']


@router.post('/reuse')
async def reuse(request: Request):
    scope = generation.scope_for_analysis(write=True)
    body = await generation.read_body(request, ReuseRequest)
    async with generation.previews.mutation(scope, body.draft_id):
        before, state = await current_preview(scope, body.draft_id)
        check_revision(before, body.revision)
        preview = deepcopy(before)
        for item in preview['review']['items']:
            if item['status'] in {'pending', 'ready'} and item['remembered']:
                item['series_id'] = item['ai_series_id']
                item['remembered'] = False
                item['origin'] = 'model'
                item['status'] = item['initial_status']
        if body.enabled:
            await run_in_threadpool(apply_preferences, scope, preview, state['catalog'])
        preview['review']['reuse_enabled'] = body.enabled
        preview['review']['revision'] += 1
        # A remembered choice only prefills review. It cannot alter the draft
        # document until this user confirms that particular item again.
        await current_preview(scope, body.draft_id)
        event = {'id': 'reuse', 'status': 'reuse_enabled' if body.enabled else 'reuse_disabled',
                 'series_id': None, 'source_quote': ''}
        await run_in_threadpool(record_decisions, scope, body.draft_id, before, preview, state['catalog'], [(event, False)])
        return preview_response(scope, body.draft_id, preview, state)
