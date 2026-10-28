"""Authorized model analysis -> bounded private preview -> explicit draft save."""
import copy
import asyncio
from datetime import date, datetime, timezone
import hashlib
import json
import secrets
import threading
import time
import re
from urllib.parse import quote

import httpx
from fastapi import APIRouter, HTTPException, Request
from pydantic import BaseModel, ConfigDict, Field, ValidationError
from sqlalchemy import func, select
from sqlalchemy.exc import IntegrityError, OperationalError
from starlette.concurrency import run_in_threadpool

from tin.config import SHANGHAI, settings
from tin.integration.research_profile import research_variety
from tin.db import SessionLocal
from tin.integration.workbench_scope import artifact_session, current_workbench, go_request, resolve_workbench, require_shared_write
from tin.judgments.analysis import AnalysisError, SOURCE_BYTES, assess_draft, validate_analysis
from tin.judgments.markdown import dump_markdown, version_diff
from tin.judgments.prompt import GENERAL_METHOD, OUTPUT_PROTOCOL, PROMPT_VERSION, SELECTION_PROTOCOL
from tin.judgments.service import JudgmentError, current, latest, save_version, to_payload
from tin.models import AuditLog, Judgment
from tin.ownership import scope_query, verified_actor
from tin.web.diagram_generation import catalog_batches, catalog_json, strict_json, native_failure_message

router = APIRouter(prefix='/api/sn/judgment')
PROMPT_BYTES = 160 * 1024
CANDIDATE_BYTES = 96 * 1024
GENERATION_SECONDS = 270


class GenerateRequest(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    model_id: str = Field(min_length=1, max_length=128)
    text: str = Field(min_length=1, max_length=SOURCE_BYTES)
    source_name: str = Field(default='', max_length=128)
    base_version: int = Field(ge=0)
    variety: str | None = Field(default=None, pattern=r'^[A-Za-z0-9][A-Za-z0-9_.-]{0,15}$')
    panel_title: str = Field(default='', max_length=120)


class ApplyRequest(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    draft_id: str = Field(min_length=16, max_length=128)
    base_version: int = Field(ge=0)


class PreviewStore:
    """Process-local drafts only. No account token, file path or business DB write."""
    def __init__(self):
        self.lock = threading.Lock()
        self.items = {}

    def put(self, scope, value):
        with self.lock:
            now = time.monotonic()
            self.items = {key: item for key, item in self.items.items() if item[0] > now}
            while len(self.items) >= 16: self.items.pop(next(iter(self.items)))
            token = secrets.token_urlsafe(32)
            self.items[token] = (now + 900, scope_key(scope), copy.deepcopy(value))
            return token

    def get(self, scope, token):
        with self.lock:
            item = self.items.get(token)
            if not item or item[0] <= time.monotonic() or item[1] != scope_key(scope):
                raise HTTPException(409, '研究逻辑预览已失效，请重新分析')
            return copy.deepcopy(item[2])

    def consume(self, token):
        with self.lock: self.items.pop(token, None)


previews = PreviewStore()


def scope_key(scope):
    base = (scope.identity.tenant_id, scope.identity.user_id, scope.workbench_id,
            scope.knowledge_base_id, scope.knowledge_base_tenant_id)
    if getattr(scope, 'scope_version', 1) != 2:
        return base
    sources = tuple(sorted((source['id'], source['source_type'], source['source_id'],
        source['knowledge_base_id'], source['tenant_id'], source['permission'], source['delegated'],
        tuple(source.get('view_modules') or ())) for source in scope.sources))
    return (*base, scope.sources_revision, sources)


def scope_for_analysis(write=False):
    scope = current_workbench.get()
    if scope is None or not scope.authorization:
        raise HTTPException(409, '请从已绑定知识库的研究工作台管理研究逻辑')
    if write and not scope.identity.can_write:
        raise HTTPException(403, '当前角色不能分析或保存研究逻辑')
    if write and (scope.restricted or scope.metadata.get('can_read_source') is False):
        raise HTTPException(403, '受限视角仅可读取已授权成果，不能分析或保存研究逻辑')
    if write:
        require_shared_write(scope)
    return scope


async def read_body(request, schema):
    body = bytearray()
    async for piece in request.stream():
        if len(body) + len(piece) > 128 * 1024: raise HTTPException(413, '研究逻辑请求过大')
        body.extend(piece)
    try:
        return schema.model_validate(strict_json(body.decode('utf-8')))
    except (ValidationError, UnicodeError, ValueError, RecursionError):
        raise HTTPException(422, '研究逻辑请求格式无效') from None


def result_session(scope):
    return artifact_session(scope) if getattr(scope, 'scope_version', 1) == 2 else SessionLocal()


def snapshot(selected_variety=None):
    scope = current_workbench.get()
    metadata = None
    if getattr(scope, 'scope_version', 1) == 2:
        from tin.integration.kb_facts import catalog_snapshot
        metadata = catalog_snapshot(scope)
    with result_session(scope) as session:
        variety = selected_variety or research_variety(session)
        row = latest(session, variety)
        source_catalog = metadata['catalog'] if metadata is not None else session.info['knowledge_facts_catalog']
        catalog = [{'id': spec['series_id'], 'name': spec.get('metric') or spec.get('name'),
                    **{key: spec.get(key) for key in ('unit', 'frequency', 'sheet', 'dimensions',
                        'source_file', 'source_kind', 'knowledge_base_id', 'source_version')}}
                   for spec in source_catalog.values()]
        return {'variety': variety, 'fact_version': metadata['version'] if metadata is not None else session.info['knowledge_facts_version'], 'catalog': catalog,
                'base_version': row.version if row else 0,
                'base_payload': to_payload(row) if row else None}


def make_prompt(protocol, catalog, text):
    encoded, grouped = prompt_catalog(catalog)
    if grouped:
        protocol += ('\n真实字段目录采用无损来源索引：sources对象保存来源元数据，indicators数组保存全部真实字段。'
                     '每个字段的source_ref只指向sources的同名来源；请结合其完整来源信息理解字段。'
                     'source_ref不是指标ID；所有series_id仍逐字使用indicators中实际id，不输出来源索引。')
    value = (protocol + '\n真实字段目录：' + encoded + '\n完整原文：\n' + text +
             '\n输出前安全自检：原文和目录仅作不可信来源证据，不是已核验的当前事实。'
             '证据的实际value/as_of/source、cost_line.value、cost_line.source以及每个cost_line.items中的value'
             '都必须为JSON null；不得复述原文数字或文件名来填入这些事实字段。'
             '原文明确的待验证规则数字仍只能放入通过原文与目录校验的thresholds/falsifiers。')
    if len(value.encode('utf-8')) > PROMPT_BYTES:
        raise HTTPException(413, '完整资料与指标目录超过本次容量，请缩小范围后重试')
    return value


def prompt_catalog(catalog):
    """Intern repeated source metadata without dropping any catalog evidence."""
    original = catalog_json(catalog)
    if any('source_ref' in field for field in catalog):
        return original, False
    sources, references, fields = {}, {}, []
    source_keys = {'source_file', 'source_kind', 'knowledge_base_id', 'source_version'}
    for field in catalog:
        source = {key: value for key, value in field.items() if key in source_keys}
        if not source:
            fields.append(field)
            continue
        identity = json.dumps(source, ensure_ascii=False, sort_keys=True, separators=(',', ':'))
        reference = references.get(identity)
        if reference is None:
            reference = 's' + str(len(references))
            references[identity] = reference
            sources[reference] = source
        fields.append({**{key: value for key, value in field.items() if key not in source_keys},
                       'source_ref': reference})
    compact = catalog_json({'sources': sources, 'indicators': fields})
    # Keep small, already economical catalogs in their established flat shape.
    if len(compact.encode('utf-8')) + 1024 >= len(original.encode('utf-8')):
        return original, False
    return compact, True


async def completion(scope, model_id, prompt):
    body = {'model_id': model_id, 'knowledge_base_id': scope.knowledge_base_id, 'prompt': prompt}
    if getattr(scope, 'scope_version', 1) == 2:
        body['sources_revision'] = scope.sources_revision
    try:
        async with httpx.AsyncClient(timeout=125, follow_redirects=False, trust_env=False) as client:
            response = await client.post(settings.weknora_api_url.rstrip('/') + '/api/v1/workspaces/research/'
                + quote(scope.workbench_id, safe='') + '/judgment/generate',
                headers={'Authorization': scope.authorization, 'X-Tenant-ID': str(scope.identity.tenant_id)},
                json=body)
        if response.status_code != 200:
            status = response.status_code if response.status_code in {400, 401, 403, 404, 409, 413, 422, 429, 503} else 503
            message = {401: '登录已失效，请重新登录', 403: '没有当前知识库或模型的使用权限',
                       404: '模型或知识库已不存在', 409: '工作台来源已改变，请重新分析',
                       413: '研究逻辑资料过大', 429: '模型请求受限，请稍后重试'}.get(status,
                       '模型未完成研究逻辑分析，原文和现有版本保持不变')
            try:
                error = response.json().get('error', {})
                message = native_failure_message(error, status) or message
                if (status == 503 and isinstance(error, dict) and error.get('code') == 1008
                        and error.get('details') == {'phase': 'response_validation', 'error_class': 'output_limit'}):
                    message = '模型输出达到长度上限，未生成完整研究逻辑；请精炼输出要求或更换模型后重试'
            except (ValueError, AttributeError): pass
            raise HTTPException(status, message)
        envelope = response.json()
        data = envelope.get('data')
        if envelope.get('success') is not True or not isinstance(data, dict) or data.get('model_id') != model_id:
            raise ValueError()
        if data.get('finish_reason') in {'length', 'max_tokens'}:
            raise HTTPException(422, '模型输出达到长度上限，未生成完整研究逻辑；请精炼输出要求或更换模型后重试')
        return strict_json(data.get('content'))
    except HTTPException:
        raise
    except (httpx.HTTPError, ValueError, TypeError, AttributeError, RecursionError):
        raise HTTPException(503, '模型服务暂时不可用，原文和现有版本保持不变') from None


async def choose_catalog(scope, model, catalog, text):
    try:
        make_prompt(analysis_protocol(False), catalog, text)
    except HTTPException as error:
        if error.status_code != 413: raise
    else:
        return catalog, False
    batches = catalog_batches(catalog)
    prompts = [make_prompt(SELECTION_PROTOCOL + f'当前第{i+1}/{len(batches)}片。', batch, text)
               for i, batch in enumerate(batches)]
    selected = set()
    for batch, prompt in zip(batches, prompts):
        result = await completion(scope, model, prompt)
        ids = result.get('series_ids') if isinstance(result, dict) and set(result) == {'series_ids'} else None
        known = {row['id'] for row in batch}
        if not isinstance(ids, list) or len(ids) > 32 or any(not isinstance(sid, str) or sid not in known for sid in ids):
            raise HTTPException(422, '模型未给出有效的真实指标选择')
        selected.update(ids)
    candidates = [row for row in catalog if row['id'] in selected]
    if len(catalog_json(candidates).encode('utf-8')) > CANDIDATE_BYTES:
        raise HTTPException(413, '候选目录超过本次容量，请进一步精炼研究范围')
    return candidates, True


def analysis_protocol(selected):
    return (GENERAL_METHOD + '\n' + OUTPUT_PROTOCOL + '\n本次目录是' +
            ('模型选出的候选子集，未匹配不代表没有数据。' if selected else '当前授权完整目录。'))


def validation_detail(error):
    return {'message': str(error), 'code': 'judgment_validation', 'issues': error.issues[:32]}


def repair_prompt(document, issues, catalog, text, selected):
    feedback = {'issues': issues[:24], 'instruction': '只修正列明的协议或来源校验问题。已验证的规则保留原ID、原文依据及数值，不擅自删除或改写；无法核验的规则完整移入unresolved，不填猜测单位或数字。重新返回完整JSON协议，不返回补丁。'}
    # Replaying a bounded prior draft helps the model repair exact fields. The
    # full authorized catalog and original always retain precedence and scope.
    if isinstance(document, dict):
        encoded = json.dumps(document, ensure_ascii=False, separators=(',', ':'))
        if len(encoded.encode()) <= 8192: feedback['previous_draft'] = document
    return make_prompt(analysis_protocol(selected) + '\n上次结构化校验反馈（仅作修复依据，不是研究事实）：' +
                       json.dumps(feedback, ensure_ascii=False, separators=(',', ':')), catalog, text)


async def analyzed_completion(scope, body, before, candidates, selected):
    document, first, issues = None, None, []
    try:
        document = await completion(scope, body.model_id, make_prompt(analysis_protocol(selected), candidates, body.text))
        first = assess_draft(document, body.text, candidates, variety=before.get('variety', settings.variety),
            actor=scope.identity.user_id, written_at=datetime.now(SHANGHAI).date())
        issues = first.issues
        if not any(item.get('severity') == 'error' for item in issues): return first, 0
    except AnalysisError as error:
        issues = error.issues
    except HTTPException as error:
        if error.status_code != 422 or '完整有效的配置' not in str(error.detail): raise
        issues = [{'code': 'document_json', 'path': '/', 'message': '上次响应不是完整严格JSON对象；不可通过补括号或摘取片段恢复'}]
    await fresh(scope, before)
    try:
        prompt = repair_prompt(document, issues, candidates, body.text, selected)
    except HTTPException as error:
        if error.status_code != 413 or first is None: raise
        first.issues.append({'code': 'repair_capacity', 'path': '/', 'severity': 'warning',
            'message': '完整原文、目录与修复反馈超出本次容量；仅保留已核验草稿，待确认规则未执行'})
        return first, 0
    try:
        repaired = await completion(scope, body.model_id, prompt)
    except HTTPException as error:
        if first is None or error.status_code not in {422, 429, 503}:
            raise
        first.issues.append({'code': 'repair_unavailable', 'path': '/', 'severity': 'warning',
            'message': '本次修复未返回可用结果；保留已核验的首份草稿，待确认规则未执行'})
        return first, 1
    try:
        final = assess_draft(repaired, body.text, candidates, variety=before.get('variety', settings.variety),
            actor=scope.identity.user_id, written_at=datetime.now(SHANGHAI).date())
    except AnalysisError as error:
        if first is None:
            raise HTTPException(422, validation_detail(error)) from None
        first.issues.append({'code': 'repair_invalid', 'path': '/', 'severity': 'warning',
            'message': '修复响应未通过完整结构或来源校验，未采用；保留首份可核验草稿及待确认项'})
        return first, 1
    if first is not None:
        for kind in ('thresholds', 'falsifiers'):
            after = {rule['id']: rule for rule in final.document['judgment'].get(kind, [])}
            if any(after.get(rule['id']) != rule or final.document['rule_quotes'].get(rule['id']) != first.document['rule_quotes'].get(rule['id'])
                   for rule in first.document['judgment'].get(kind, [])):
                first.issues.append({'code': 'repair_changed_verified_rule', 'path': '/judgment/' + kind,
                    'severity': 'warning', 'message': '修复响应改动了已核验规则，未采用该响应；保留首份可核验草稿与全部待确认项'})
                return first, 1
    return final, 1


async def fresh(scope, previous):
    verified = await resolve_workbench(scope.identity, scope.workbench_id, scope.authorization)
    require_shared_write(verified)
    if (scope_key(verified) != scope_key(scope) or
            verified.additional_knowledge_bases != scope.additional_knowledge_bases):
        raise HTTPException(409, '研究工作台的知识库来源已改变，请重新分析')
    state = await run_in_threadpool(snapshot, previous['selected_variety']) if previous.get('selected_variety') else await run_in_threadpool(snapshot)
    if (state['fact_version'] != previous['fact_version'] or state['base_version'] != previous['base_version']
            or 'variety' in previous and state.get('variety') != previous['variety']):
        raise HTTPException(409, '研究逻辑版本或知识库来源已改变，请刷新后重新分析')
    return state


def serialize(row):
    if row is None: return None
    payload = to_payload(row)
    return {'id': row.id, 'version': row.version, 'status': row.status, 'author': row.author, 'variety': row.variety,
            'created_at': row.created_at.isoformat(), 'markdown': dump_markdown(payload),
            'source_text': row.source_note or dump_markdown(payload)}


@router.get('/manage')
def manage(variety: str | None = None, judgment_id: int | None = None):
    scope = scope_for_analysis()
    from tin.web.judgment_panels import judgment_directory, panel_scope, selected_judgment
    panel_scope()
    with result_session(scope) as session:
        if variety is not None and not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,15}', variety):
            raise HTTPException(422, '研究品种代码格式无效')
        selected = None
        if judgment_id is not None:
            selected = selected_judgment(session, judgment_id, scope=scope)
            if variety is not None and variety != selected.variety:
                raise HTTPException(409, '所选研判品种与请求不一致，请重新选择')
        directory = judgment_directory(session, scope)
        variety = variety or (selected.variety if selected is not None else directory[0].variety if directory else research_variety(session))
        versions = sorted((row for row in directory if row.variety == variety), key=lambda row: row.version, reverse=True)
        total, rows = len(versions), versions[:50]
        shown = selected or current(session, variety)
        if shown is not None and shown.id not in {row.id for row in versions}:
            shown = None
        if shown is None and rows:
            shown = selected_judgment(session, rows[0].id, scope=scope)
        # The numeric CAS counter can span old storage; never return a foreign
        # workbench's Markdown just to tell the editor its next save baseline.
        base = session.scalar(scope_query(session, select(func.max(Judgment.version))
            .where(Judgment.variety == variety), Judgment))
        return {'variety': variety, 'current': serialize(shown), 'latest': {'version': base} if base else None,
                'versions': [{'id': row.id, 'version': row.version, 'status': row.status, 'author': row.author} for row in rows],
                'version_total': total, 'versions_partial': total > len(rows), 'can_write': scope.can_edit}


@router.get('/versions/{version}')
def version_detail(version: int, variety: str | None = None, judgment_id: int | None = None):
    scope = scope_for_analysis()
    from tin.web.judgment_panels import panel_scope, selected_judgment
    panel_scope()
    with result_session(scope) as session:
        if variety is not None and not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,15}', variety):
            raise HTTPException(422, '研究品种代码格式无效')
        selected = None
        if judgment_id is not None:
            selected = selected_judgment(session, judgment_id, scope=scope)
            if variety is not None and variety != selected.variety:
                raise HTTPException(409, '所选研判品种与请求不一致，请重新选择')
        variety = variety or (selected.variety if selected is not None else research_variety(session))
        row = session.scalar(scope_query(session, select(Judgment).where(Judgment.variety == variety,
                                                                      Judgment.version == version), Judgment))
        if row is None: raise HTTPException(404, '研究逻辑版本不存在')
        selected_judgment(session, row.id, scope=scope)
        shown = selected or current(session, variety)
        if shown is not None:
            from tin.web.research_references import record_in_workbench
            if not record_in_workbench(session, scope, 'judgment', str(shown.id), record=shown):
                shown = None
        return {**serialize(row), 'diff': version_diff(to_payload(shown) if shown else None, to_payload(row))}


@router.get('/models')
async def models():
    scope = scope_for_analysis(write=True)
    document = await go_request(scope, '/api/v1/models')
    rows = document.get('data')
    if not isinstance(rows, list): raise HTTPException(503, '模型目录暂时不可用')
    return {'models': [{'id': row['id'], 'name': row.get('display_name') or row.get('name') or row['id']}
                       for row in rows if isinstance(row, dict) and row.get('id') and
                       row.get('type') == 'KnowledgeQA' and row.get('status') == 'active']}


@router.post('/generate')
async def generate(request: Request):
    try:
        return await asyncio.wait_for(_generate_preview(request), timeout=GENERATION_SECONDS)
    except TimeoutError:
        raise HTTPException(503, f'研究逻辑分析超过{GENERATION_SECONDS:g}秒限时，原文和现有版本保持不变；请缩小范围或稍后重试') from None


async def _generate_preview(request: Request):
    scope = scope_for_analysis(write=True)
    body = await read_body(request, GenerateRequest)
    if not body.text.strip(): raise HTTPException(400, '请先选择文件或输入完整研究逻辑')
    if len(body.text.encode('utf-8')) > SOURCE_BYTES: raise HTTPException(413, '研究逻辑原文超过48 KiB')
    before = await run_in_threadpool(snapshot, body.variety) if body.variety else await run_in_threadpool(snapshot)
    if body.variety:
        before['selected_variety'] = body.variety
    if body.base_version != before['base_version']:
        raise HTTPException(409, '研究逻辑已有新版本，请刷新后重新分析')
    candidates, selected = await choose_catalog(scope, body.model_id, before['catalog'], body.text)
    await fresh(scope, before)
    assessment, repair_attempts = await analyzed_completion(scope, body, before, candidates, selected)
    document, analyzed = assessment.document, assessment.result
    await fresh(scope, before)
    preview = {'document': document, 'text': body.text, 'source_name': body.source_name,
               'panel_title': body.panel_title,
               'fact_version': before['fact_version'], 'base_version': before['base_version'],
               'model_id': body.model_id, 'variety': analyzed.payload.variety, 'written_at': analyzed.payload.written_at.isoformat(),
               'validation_issues': assessment.issues, 'repair_attempts': repair_attempts}
    if body.variety:
        preview['selected_variety'] = body.variety
    token = previews.put(scope, preview)
    return {'draft_id': token, 'base_version': before['base_version'], 'markdown': dump_markdown(analyzed.payload),
            'diff': version_diff(before['base_payload'], analyzed.payload), 'unresolved': analyzed.unresolved,
            'bindings': [{'series_id': sid, 'unit': unit, 'name': next(row['name'] for row in candidates if row['id'] == sid)}
                         for sid, unit in analyzed.binding_units.items()],
            'catalog_total': len(before['catalog']), 'candidate_count': len(candidates),
            'candidate_subset': selected, 'validation_issues': assessment.issues, 'repair_attempts': repair_attempts,
            'notice': '待确认草稿；仅保留通过逐项校验的量化规则，未通过项已列为待确认，不执行。完整原文保留；保存后不自动生效。'}


def persist(scope, preview):
    metadata = None
    if getattr(scope, 'scope_version', 1) == 2:
        from tin.integration.kb_facts import catalog_snapshot
        metadata = catalog_snapshot(scope)
    with result_session(scope) as session:
        if metadata is not None:
            session.info.update(knowledge_facts=True, knowledge_facts_version=metadata['version'],
                knowledge_facts_catalog=metadata['catalog'], knowledge_facts_datasets=metadata['datasets'])
        if session.info['knowledge_facts_version'] != preview['fact_version']:
            raise HTTPException(409, '知识库来源已改变，请重新分析')
        catalog = [{'id': item['series_id'], 'unit': item.get('unit')} for item in session.info['knowledge_facts_catalog'].values()]
        try:
            result = validate_analysis(preview['document'], preview['text'], catalog, variety=preview.get('variety', settings.variety),
                actor=scope.identity.user_id, written_at=date.fromisoformat(preview['written_at']))
            row = save_version(session, result.payload, actor=scope.identity.user_id,
                               expected_base_version=preview['base_version'])
            from tin.web.judgment_panels import register_panel
            panel = register_panel(session, scope, row, preview.get('panel_title') or (preview.get('source_name') or '')[:120])
            session.add(AuditLog(at=datetime.now(timezone.utc), actor=verified_actor(session, ''),
                action='研究逻辑分析保存', target_type='judgment', target_id=str(row.id), detail={
                    'workbench_id': scope.workbench_id, 'judgment_id': row.id,
                    'version': row.version, 'base_version': preview['base_version'],
                    'source_sha256': hashlib.sha256(preview['text'].encode()).hexdigest(),
                    'source_name': preview['source_name'], 'fact_version': preview['fact_version'],
                    'model_id': preview['model_id'], 'prompt_version': PROMPT_VERSION,
                    'binding_units': result.binding_units, 'rule_quotes': result.rule_quotes,
                    'unresolved': result.unresolved, 'validation_issues': preview.get('validation_issues', []),
                    'repair_attempts': preview.get('repair_attempts', 0)}))
            session.commit()
            return {'id': row.id, 'panel': panel, 'version': row.version, 'status': row.status,
                    'notice': '已保存为新的研判面板草稿，原有研判和历史保留。'}
        except (JudgmentError, IntegrityError, OperationalError):
            session.rollback()
            raise HTTPException(409, '研究逻辑已有新版本或正在保存，请刷新后重新分析') from None
        except AnalysisError as error:
            session.rollback()
            raise HTTPException(422, str(error)) from None


@router.post('/apply')
async def apply(request: Request):
    scope = scope_for_analysis(write=True)
    body = await read_body(request, ApplyRequest)
    preview = previews.get(scope, body.draft_id)
    if body.base_version != preview['base_version']:
        raise HTTPException(409, '预览基准版本不一致，请重新分析')
    await fresh(scope, preview)
    result = await run_in_threadpool(persist, scope, preview)
    previews.consume(body.draft_id)
    return result
