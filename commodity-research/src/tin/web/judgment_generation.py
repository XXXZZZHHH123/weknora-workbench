"""Authorized model analysis -> bounded private preview -> explicit draft save."""
import copy
import asyncio
from datetime import date, datetime, timezone
import hashlib
import json
import secrets
import threading
import time
from urllib.parse import quote

import httpx
from fastapi import APIRouter, HTTPException, Request
from pydantic import BaseModel, ConfigDict, Field, ValidationError
from sqlalchemy import func, select
from sqlalchemy.exc import IntegrityError, OperationalError
from starlette.concurrency import run_in_threadpool

from tin.config import SHANGHAI, settings
from tin.db import SessionLocal
from tin.integration.workbench_scope import current_workbench, go_request, resolve_workbench
from tin.judgments.analysis import AnalysisError, SOURCE_BYTES, validate_analysis
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
    return (scope.identity.tenant_id, scope.identity.user_id, scope.workbench_id,
            scope.knowledge_base_id, scope.knowledge_base_tenant_id)


def scope_for_analysis(write=False):
    scope = current_workbench.get()
    if scope is None or not scope.authorization:
        raise HTTPException(409, '请从已绑定知识库的研究工作台管理研究逻辑')
    if write and not scope.identity.can_write:
        raise HTTPException(403, '当前角色不能分析或保存研究逻辑')
    return scope


async def read_body(request, schema):
    body = bytearray()
    async for piece in request.stream():
        body.extend(piece)
        if len(body) > 128 * 1024: raise HTTPException(413, '研究逻辑请求过大')
    try:
        return schema.model_validate(strict_json(body.decode('utf-8')))
    except (ValidationError, UnicodeError, ValueError, RecursionError):
        raise HTTPException(422, '研究逻辑请求格式无效') from None


def snapshot():
    with SessionLocal() as session:
        row = latest(session, settings.variety)
        catalog = [{'id': spec['series_id'], 'name': spec.get('metric') or spec.get('name'),
                    **{key: spec.get(key) for key in ('unit', 'frequency', 'sheet', 'dimensions',
                        'source_file', 'source_kind', 'knowledge_base_id', 'source_version')}}
                   for spec in session.info['knowledge_facts_catalog'].values()]
        return {'fact_version': session.info['knowledge_facts_version'], 'catalog': catalog,
                'base_version': row.version if row else 0,
                'base_payload': to_payload(row) if row else None}


def make_prompt(protocol, catalog, text):
    value = protocol + '\n真实字段目录：' + catalog_json(catalog) + '\n完整原文：\n' + text
    if len(value.encode('utf-8')) > PROMPT_BYTES:
        raise HTTPException(413, '完整资料与指标目录超过本次容量，请缩小范围后重试')
    return value


async def completion(scope, model_id, prompt):
    try:
        async with httpx.AsyncClient(timeout=125, follow_redirects=False, trust_env=False) as client:
            response = await client.post(settings.weknora_api_url.rstrip('/') + '/api/v1/workspaces/research/'
                + quote(scope.workbench_id, safe='') + '/judgment/generate',
                headers={'Authorization': scope.authorization, 'X-Tenant-ID': str(scope.identity.tenant_id)},
                json={'model_id': model_id, 'knowledge_base_id': scope.knowledge_base_id, 'prompt': prompt})
        if response.status_code != 200:
            status = response.status_code if response.status_code in {400, 401, 403, 404, 409, 413, 422, 429, 503} else 503
            message = {401: '登录已失效，请重新登录', 403: '没有当前知识库或模型的使用权限',
                       404: '模型或知识库已不存在', 409: '工作台来源已改变，请重新分析',
                       413: '研究逻辑资料过大', 429: '模型请求受限，请稍后重试'}.get(status,
                       '模型未完成研究逻辑分析，原文和现有版本保持不变')
            try: message = native_failure_message(response.json().get('error', {}), status) or message
            except (ValueError, AttributeError): pass
            raise HTTPException(status, message)
        envelope = response.json()
        data = envelope.get('data')
        if envelope.get('success') is not True or not isinstance(data, dict) or data.get('model_id') != model_id:
            raise ValueError()
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


async def fresh(scope, previous):
    verified = await resolve_workbench(scope.identity, scope.workbench_id, scope.authorization)
    if (scope_key(verified) != scope_key(scope) or
            verified.additional_knowledge_bases != scope.additional_knowledge_bases):
        raise HTTPException(409, '研究工作台的知识库来源已改变，请重新分析')
    state = await run_in_threadpool(snapshot)
    if state['fact_version'] != previous['fact_version'] or state['base_version'] != previous['base_version']:
        raise HTTPException(409, '研究逻辑版本或知识库来源已改变，请刷新后重新分析')
    return state


def serialize(row):
    if row is None: return None
    payload = to_payload(row)
    return {'version': row.version, 'status': row.status, 'author': row.author,
            'created_at': row.created_at.isoformat(), 'markdown': dump_markdown(payload),
            'source_text': row.source_note or dump_markdown(payload)}


@router.get('/manage')
def manage():
    scope = scope_for_analysis()
    with SessionLocal() as session:
        rows = session.scalars(scope_query(session, select(Judgment).where(Judgment.variety == settings.variety)
            .order_by(Judgment.version.desc()).limit(50), Judgment)).all()
        total = session.scalar(scope_query(session, select(func.count(Judgment.id))
            .where(Judgment.variety == settings.variety), Judgment))
        return {'current': serialize(current(session, settings.variety)), 'latest': serialize(rows[0]) if rows else None,
                'versions': [{'version': row.version, 'status': row.status, 'author': row.author} for row in rows],
                'version_total': total, 'versions_partial': total > len(rows), 'can_write': scope.identity.can_write}


@router.get('/versions/{version}')
def version_detail(version: int):
    scope_for_analysis()
    with SessionLocal() as session:
        row = session.scalar(scope_query(session, select(Judgment).where(Judgment.variety == settings.variety,
                                                                      Judgment.version == version), Judgment))
        if row is None: raise HTTPException(404, '研究逻辑版本不存在')
        shown = current(session, settings.variety)
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
    before = await run_in_threadpool(snapshot)
    if body.base_version != before['base_version']:
        raise HTTPException(409, '研究逻辑已有新版本，请刷新后重新分析')
    candidates, selected = await choose_catalog(scope, body.model_id, before['catalog'], body.text)
    await fresh(scope, before)
    document = await completion(scope, body.model_id, make_prompt(analysis_protocol(selected), candidates, body.text))
    try:
        analyzed = validate_analysis(document, body.text, candidates, variety=settings.variety,
                                     actor=scope.identity.user_id, written_at=datetime.now(SHANGHAI).date())
    except AnalysisError as error:
        raise HTTPException(422, str(error)) from None
    await fresh(scope, before)
    preview = {'document': document, 'text': body.text, 'source_name': body.source_name,
               'fact_version': before['fact_version'], 'base_version': before['base_version'],
               'model_id': body.model_id, 'written_at': analyzed.payload.written_at.isoformat()}
    token = previews.put(scope, preview)
    return {'draft_id': token, 'base_version': before['base_version'], 'markdown': dump_markdown(analyzed.payload),
            'diff': version_diff(before['base_payload'], analyzed.payload), 'unresolved': analyzed.unresolved,
            'bindings': [{'series_id': sid, 'unit': unit, 'name': next(row['name'] for row in candidates if row['id'] == sid)}
                         for sid, unit in analyzed.binding_units.items()],
            'catalog_total': len(before['catalog']), 'candidate_count': len(candidates),
            'candidate_subset': selected, 'notice': '待确认草稿；已核对指标ID、单位及规则数字出处，因果含义与统计口径仍需人工确认。保存后不自动生效。'}


def persist(scope, preview):
    with SessionLocal() as session:
        if session.info['knowledge_facts_version'] != preview['fact_version']:
            raise HTTPException(409, '知识库来源已改变，请重新分析')
        catalog = [{'id': item['series_id'], 'unit': item.get('unit')} for item in session.info['knowledge_facts_catalog'].values()]
        try:
            result = validate_analysis(preview['document'], preview['text'], catalog, variety=settings.variety,
                actor=scope.identity.user_id, written_at=date.fromisoformat(preview['written_at']))
            row = save_version(session, result.payload, actor=scope.identity.user_id,
                               expected_base_version=preview['base_version'])
            session.add(AuditLog(at=datetime.now(timezone.utc), actor=verified_actor(session, ''),
                action='研究逻辑分析保存', target_type='judgment', target_id=str(row.id), detail={
                    'version': row.version, 'base_version': preview['base_version'],
                    'source_sha256': hashlib.sha256(preview['text'].encode()).hexdigest(),
                    'source_name': preview['source_name'], 'fact_version': preview['fact_version'],
                    'model_id': preview['model_id'], 'prompt_version': PROMPT_VERSION,
                    'binding_units': result.binding_units, 'rule_quotes': result.rule_quotes,
                    'unresolved': result.unresolved}))
            session.commit()
            return {'version': row.version, 'status': row.status, 'notice': '已保存为新草稿，上一版本保留。'}
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
