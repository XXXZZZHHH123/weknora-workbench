"""Explicit, versioned references in authored strategy prose; never executable rules."""
from copy import deepcopy
import hashlib
import json
from typing import Annotated, Any, Literal, Union
from urllib.parse import quote, urlsplit

import httpx
from fastapi import APIRouter, HTTPException, Query, Request
from pydantic import BaseModel, ConfigDict, Field, TypeAdapter, ValidationError
from starlette.concurrency import run_in_threadpool

from tin.config import settings
from tin.integration.kb_facts import catalog_page
from tin.integration.workbench_scope import (artifact_session, current_workbench, go_request,
    source_scope_for_binding)
from tin.web.research_display import condition_text, human_text, reference_label

router = APIRouter()
MAX_CITATIONS = 32
MAX_CONTENT_BYTES = 96 * 1024
MAX_CONTENT_CHARACTERS = 24000
MAX_PARSED_CHUNKS = 200
MAX_REQUEST_BYTES = 128 * 1024
TEXT_KINDS = frozenset({'txt', 'md', 'markdown', 'json', 'csv', 'tsv', 'xml'})
CitationField = Literal['thesis', 'watch_for', 'trigger', 'response_action', 'invalidation', 'original_text']


class CitationBase(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    field: CitationField = 'thesis'
    # Client snapshots are accepted for round trips but always discarded.
    snapshot: dict[str, Any] | None = None


class IndicatorCitation(CitationBase):
    kind: Literal['indicator']
    series_id: str = Field(min_length=1, max_length=128)
    source_version: str = Field(min_length=1, max_length=256)
    source_binding_id: str | None = Field(default=None, min_length=1, max_length=128)


class JudgmentConditionCitation(CitationBase):
    kind: Literal['judgment_condition']
    id: str = Field(min_length=1, max_length=128)
    revision: int = Field(ge=1, strict=True)
    rule_kind: Literal['threshold', 'falsifier']
    rule_id: str = Field(min_length=1, max_length=64)


class KnowledgeFileCitation(CitationBase):
    kind: Literal['knowledge_file']
    source_binding_id: str = Field(min_length=1, max_length=128)
    knowledge_id: str = Field(min_length=1, max_length=128)
    file_version: str = Field(pattern=r'^[a-f0-9]{64}$')


Citation = Annotated[Union[IndicatorCitation, JudgmentConditionCitation, KnowledgeFileCitation], Field(discriminator='kind')]
_citation = TypeAdapter(Citation)
_citations = TypeAdapter(Annotated[list[Citation], Field(max_length=MAX_CITATIONS)])


class CitationPreviewInput(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    citation: Citation


def require_scope(scope=None):
    scope = scope or current_workbench.get()
    if scope is None:
        raise HTTPException(409, '请先进入当前研究工作台')
    if (scope.restricted or scope.metadata.get('can_read_source') is False
            or 'strategies' not in scope.view_modules):
        raise HTTPException(403, '当前授权不开放研究策略引用或原件读取')
    return scope


def parse_citation(value):
    try:
        model = _citation.validate_python(value)
    except (ValidationError, TypeError, ValueError):
        raise HTTPException(422, '请选择有效的指标、研判条件或知识库文件引用') from None
    return model.model_dump(mode='json', exclude_none=True, exclude={'snapshot'})


def _text(value, default='', limit=240):
    return value[:limit] if isinstance(value, str) and value.strip() else default


def _version(field, page):
    version = field.get('source_version') or page.get('version')
    if not isinstance(version, str) or not version or len(version) > 256:
        raise HTTPException(503, '当前指标来源版本不完整，请刷新后重新选择')
    return version


def _binding(scope, field):
    identifier = field.get('source_binding_id')
    if getattr(scope, 'scope_version', 1) != 2:
        return identifier if isinstance(identifier, str) else None
    matches = [row for row in scope.sources if not row['delegated'] and (
        row['id'] == identifier and row['knowledge_base_id'] == field.get('knowledge_base_id')
        if identifier else row['knowledge_base_id'] == field.get('knowledge_base_id'))]
    if len(matches) != 1:
        raise HTTPException(403, '该指标不属于当前直接授权的数据来源')
    return matches[0]['id']


def _indicator_citation(scope, field, page, slot='thesis'):
    result = {'kind': 'indicator', 'field': slot, 'series_id': field['series_id'],
        'source_version': _version(field, page)}
    binding = _binding(scope, field)
    if binding:
        result['source_binding_id'] = binding
    return result


def _indicator_preview(field):
    name = human_text(field.get('metric') or field.get('name')) or '已选指标'
    return {'type': 'indicator', 'series_id': field['series_id'], 'name': name,
        'unit': _text(field.get('unit'), '未注明'), 'frequency': _text(field.get('frequency'), '未注明'),
        'source': _text(field.get('source_file'), '已授权结构化来源'),
        'detail_url': '/sn/indicators/' + quote(field['series_id'], safe='')}


def _result(citation, title, subtitle, preview):
    normalized = {**citation, 'snapshot': {'title': title, 'subtitle': subtitle, 'preview': preview}}
    return {'citation': normalized, 'title': title, 'subtitle': subtitle, 'preview': preview}


def _resolve_indicator(scope, citation):
    page = catalog_page(scope, limit=1, series_ids=[citation['series_id']])
    fields = page.get('series')
    if not isinstance(fields, list) or len(fields) != 1 or fields[0].get('series_id') != citation['series_id']:
        raise HTTPException(404, '引用指标已删除、失效或不在当前授权目录中，请重新选择')
    field = fields[0]
    binding = _binding(scope, field)
    if citation.get('source_binding_id') is not None and citation['source_binding_id'] != binding:
        raise HTTPException(403, '引用指标与所选数据来源不匹配，请重新选择')
    if citation['source_version'] != _version(field, page):
        raise HTTPException(409, '引用指标的来源版本已更新，请重新选择并核对口径')
    normalized = _indicator_citation(scope, field, page, citation['field'])
    preview = _indicator_preview(field)
    return _result(normalized, preview['name'], preview['unit'] + ' · ' + preview['frequency'], preview)


def _condition_data(session, scope, citation):
    from tin.web.research_references import resolve_judgment_condition
    return resolve_judgment_condition(session, scope, citation)


def _condition_text(rule, kind):
    if kind == 'falsifier':
        text = condition_text(rule.get('expression'))
        unit = rule.get('unit_confirmed')
        if unit:
            text += ' ' + human_text(unit)
        periods = rule.get('consecutive_periods', 1)
        if isinstance(periods, int) and not isinstance(periods, bool) and periods > 1:
            text += '，连续 ' + str(periods) + ' 期'
        return text
    operator = rule.get('operator')
    value = rule.get('value')
    comparisons = {'>': '高于', '>=': '不低于', '<': '低于', '<=': '不高于',
        'crosses_below': '下穿', 'crosses_above': '上穿'}
    if isinstance(value, dict) and value.get('kind') == 'sma':
        return '指标数值' + comparisons.get(operator, '比较') + str(value.get('window')) + '期简单均线'
    if operator == 'between' and isinstance(value, list) and len(value) == 2:
        return '指标数值位于 ' + str(value[0]) + ' 至 ' + str(value[1]) + ' 区间'
    return '指标数值' + comparisons.get(operator, '比较') + ' ' + str(value)


def _resolve_condition(scope, citation):
    with artifact_session(scope) as session:
        actual = _condition_data(session, scope, citation)
    rule = actual['condition']
    name = human_text(rule.get('name') or rule.get('desc')) or ('阈值条件' if citation['rule_kind'] == 'threshold' else '证伪条件')
    judgment_title = reference_label(actual)
    text = _condition_text(rule, citation['rule_kind'])
    preview = {'type': 'condition', 'text': text, 'judgment_title': judgment_title,
        'rule_name': name, 'rule_kind': citation['rule_kind'],
        'origin_url': '/sn/judgment?judgment_id=' + quote(citation['id'], safe=''),
        'series_id': rule.get('series_id')}
    return _result(citation, name, judgment_title, preview)


def _source_scope(scope, binding_id):
    if getattr(scope, 'scope_version', 1) == 2:
        return source_scope_for_binding(scope, binding_id, direct_only=True)
    if binding_id != scope.knowledge_base_id:
        raise HTTPException(403, '引用文件不属于当前直接授权的数据来源')
    return scope


def _sources(scope):
    if getattr(scope, 'scope_version', 1) == 2:
        return [(source['id'], _source_scope(scope, source['id']))
            for source in sorted(scope.sources, key=lambda row: row['id']) if not source['delegated']]
    return [(scope.knowledge_base_id, scope)]


def _file_version(source):
    # A metadata identity, not a claim that original bytes have been frozen.
    data = {key: source.get(key) for key in ('id', 'knowledge_base_id', 'file_hash', 'updated_at',
        'file_size', 'file_name', 'file_type', 'title', 'type')}
    return hashlib.sha256(json.dumps(data, ensure_ascii=False, sort_keys=True,
        separators=(',', ':'), allow_nan=False).encode()).hexdigest()


file_version = _file_version


async def _native_file(scope, binding_id, knowledge_id):
    bound = _source_scope(scope, binding_id)
    document = await go_request(bound, '/api/v1/knowledge/' + quote(knowledge_id, safe=''))
    source = document.get('data') if isinstance(document, dict) else None
    if (not isinstance(source, dict) or source.get('id') != knowledge_id
            or source.get('knowledge_base_id') != bound.knowledge_base_id
            or source.get('parse_status') in {'deleted', 'deleting'}):
        raise HTTPException(404, '引用文件已删除、移出来源或无读取权限，请重新选择')
    return bound, source


def _file_preview(source, expected):
    name = _text(source.get('file_name') or source.get('title'), '未命名知识库文件')
    kind = _text(source.get('file_type')).lower().lstrip('.')
    current = _file_version(source)
    size = source.get('file_size')
    raw_supported = kind in TEXT_KINDS and (type(size) is not int or 0 <= size <= MAX_CONTENT_BYTES)
    parsed_supported = kind not in TEXT_KINDS and source.get('parse_status') in {'ready', 'completed'}
    supported = raw_supported or parsed_supported
    return {'type': 'file', 'knowledge_id': source['id'], 'file_name': name, 'file_type': kind,
        'url': '/platform/knowledge-bases/' + quote(source['knowledge_base_id'], safe='')
            + '?knowledge_id=' + quote(source['id'], safe=''),
        'content_supported': supported,
        'content_kind': 'original_text' if kind in TEXT_KINDS else 'parsed_fragments',
        'current_version': current,
        'version_status': 'current' if expected == current else 'updated',
        'notice': ('引用保存时的文件版本信息；正文未下载或导入策略。' if expected == current else
            '知识库原件已更新；保存前请重新选择当前版本。')}


async def _resolve_file(scope, citation, *, strict_version=True):
    _, source = await _native_file(scope, citation['source_binding_id'], citation['knowledge_id'])
    preview = _file_preview(source, citation['file_version'])
    if strict_version and preview['version_status'] != 'current':
        raise HTTPException(409, '引用文件的版本已更新，请重新选择；未使用其他版本替代')
    return _result(citation, preview['file_name'], '知识库文件 · ' + (preview['file_type'] or '原件'), preview)


async def resolve_citations(scope, values):
    require_scope(scope)
    try:
        models = _citations.validate_python(values)
    except (ValidationError, ValueError, TypeError):
        raise HTTPException(422, '策略引用格式无效或超过32项，请重新选择') from None
    normalized = []
    for model in models:
        citation = model.model_dump(mode='json', exclude_none=True, exclude={'snapshot'})
        if citation['kind'] == 'indicator':
            result = await run_in_threadpool(_resolve_indicator, scope, citation)
        elif citation['kind'] == 'judgment_condition':
            result = await run_in_threadpool(_resolve_condition, scope, citation)
        else:
            result = await _resolve_file(scope, citation)
        normalized.append(result['citation'])
    return normalized


async def normalize_card_citations(scope, carddict):
    from tin.web.research_strategy import ResearchStrategyCard
    try:
        card = ResearchStrategyCard.model_validate(carddict)
    except (ValidationError, ValueError, TypeError):
        raise HTTPException(422, '策略内容或引用格式无效，请核对后保存') from None
    result = card.model_dump(mode='json', exclude_none=True)
    if card.citations:
        result['citations'] = await resolve_citations(scope, result['citations'])
    return result


def _condition_options(scope, search, offset, limit, judgment_id=None, revision=None):
    from tin.web.research_references import list_references, resolve_reference
    with artifact_session(scope) as session:
        if judgment_id is not None:
            if revision is None:
                raise HTTPException(422, '请选择研判的明确版本')
            parents = [resolve_reference(session, scope, 'judgment', judgment_id, revision)]
        else:
            directory = list_references(session, scope, 'judgment', limit=100, offset=0)
            parents = directory['items']
            if directory['total'] > 100:
                raise HTTPException(413, '研判记录较多，请先明确选择要引用的研判版本')
        rows = []
        for parent in parents:
            actual = parent if 'payload' in parent else resolve_reference(session, scope, 'judgment', parent['id'], parent['revision'])
            for key, kind in (('thresholds', 'threshold'), ('falsifiers', 'falsifier')):
                for rule in actual['payload'].get(key, []):
                    label = human_text(rule.get('name') or rule.get('desc')) or ('阈值条件' if kind == 'threshold' else '证伪条件')
                    subtitle = reference_label(actual) + ' · ' + _condition_text(rule, kind)
                    if search and search.casefold() not in (label + ' ' + subtitle).casefold():
                        continue
                    citation = {'kind': 'judgment_condition', 'field': 'thesis', 'id': actual['id'],
                        'revision': actual['revision'], 'rule_kind': kind, 'rule_id': rule['id']}
                    rows.append({'citation': citation, 'title': label, 'subtitle': subtitle})
    total = len(rows)
    return {'items': rows[offset:offset + limit], 'total': total, 'offset': offset, 'limit': limit,
        'has_more': offset + limit < total, 'next_offset': offset + limit if offset + limit < total else None}


async def _file_options(scope, search, offset, limit):
    size, catalogs, total = 100, [], 0
    for binding_id, bound in _sources(scope):
        async def read(page, bound=bound):
            document = await go_request(bound, '/api/v1/knowledge-bases/' + quote(bound.knowledge_base_id, safe='')
                + '/knowledge', params={'page': page, 'page_size': size, 'keyword': search})
            data, count = document.get('data'), document.get('total')
            if (not isinstance(data, list) or type(count) is not int or count < 0 or count > 10000
                    or len(data) != min(size, max(0, count - (page - 1) * size))
                    or any(not isinstance(row, dict) or not isinstance(row.get('id'), str)
                        or not row['id'] or row.get('knowledge_base_id') != bound.knowledge_base_id for row in data)):
                raise HTTPException(503, '知识库文件目录不完整，请刷新后重试')
            return data, count
        first, count = await read(1)
        catalogs.append((binding_id, bound, first, count, read, total))
        total += count
        if total > 10000:
            raise HTTPException(413, '本次文件目录超过10000项，请缩小搜索范围')
    rows = []
    for binding_id, bound, first, count, read, start in catalogs:
        left, right = max(0, offset - start), min(count, offset + limit - start)
        if right <= left:
            continue
        for page in range(left // size + 1, (right - 1) // size + 2):
            data, current = (first, count) if page == 1 else await read(page)
            if current != count:
                raise HTTPException(409, '文件目录在读取期间改变，请刷新后重试')
            begin, end = max(0, left - (page - 1) * size), min(len(data), right - (page - 1) * size)
            for source in data[begin:end]:
                if source.get('parse_status') in {'deleted', 'deleting'}:
                    continue
                citation = {'kind': 'knowledge_file', 'field': 'thesis', 'source_binding_id': binding_id,
                    'knowledge_id': source['id'], 'file_version': _file_version(source)}
                name = _text(source.get('file_name') or source.get('title'), '未命名知识库文件')
                rows.append({'citation': citation, 'title': name,
                    'subtitle': '知识库文件 · ' + _text(source.get('file_type'), '原件')})
    return {'items': rows, 'total': total, 'offset': offset, 'limit': limit,
        'has_more': offset + limit < total, 'next_offset': offset + limit if offset + limit < total else None}


@router.get('/citation-options')
async def citation_options(kind: Literal['indicator', 'judgment_condition', 'knowledge_file'],
        search: str = Query('', max_length=200), offset: int = Query(0, ge=0, le=10000),
        limit: int = Query(30, ge=1, le=100), judgment_id: str | None = Query(None, max_length=128),
        revision: int | None = Query(None, ge=1)):
    scope = require_scope()
    if kind == 'indicator':
        page = await run_in_threadpool(catalog_page, scope, offset=offset, limit=limit, search=search)
        fields = page.get('series')
        if not isinstance(fields, list) or type(page.get('total')) is not int:
            raise HTTPException(503, '当前指标目录暂不可读，请稍后重试')
        rows = []
        for field in fields:
            preview = _indicator_preview(field)
            rows.append({'citation': _indicator_citation(scope, field, page), 'title': preview['name'],
                'subtitle': preview['unit'] + ' · ' + preview['frequency'] + ' · ' + preview['source']})
        next_offset = page.get('next_offset')
        return {'items': rows, 'total': page['total'], 'offset': offset, 'limit': limit,
            'has_more': next_offset is not None, 'next_offset': next_offset}
    if kind == 'judgment_condition':
        return await run_in_threadpool(_condition_options, scope, search, offset, limit, judgment_id, revision)
    return await _file_options(scope, search, offset, limit)


def _unique_json(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('Duplicate JSON property')
        result[key] = value
    return result


def _reject_constant(_):
    raise ValueError('Non-finite JSON is not allowed')


@router.post('/citation-preview')
async def citation_preview(request: Request):
    scope = require_scope()
    raw = bytearray()
    async for chunk in request.stream():
        raw.extend(chunk)
        if len(raw) > MAX_REQUEST_BYTES:
            raise HTTPException(413, '引用预览请求过大，请重新选择')
    try:
        value = json.loads(raw.decode('utf-8'), object_pairs_hook=_unique_json, parse_constant=_reject_constant)
        body = CitationPreviewInput.model_validate(value)
    except (ValidationError, ValueError, TypeError, RecursionError, UnicodeDecodeError):
        raise HTTPException(422, '引用预览参数无效，请重新选择') from None
    citation = parse_citation(body.citation)
    if citation['kind'] == 'knowledge_file':
        return await _resolve_file(scope, citation, strict_version=False)
    normalized = (await resolve_citations(scope, [citation]))[0]
    snapshot = normalized['snapshot']
    return {'citation': normalized, **snapshot}


async def _parsed_chunk_rows(bound, knowledge_id):
    rows, identifiers, total, previous_index = [], set(), None, -1
    page, size = 1, 20
    while total is None or len(rows) < total:
        document = await go_request(bound, '/api/v1/chunks/' + quote(knowledge_id, safe=''),
            params={'page': page, 'page_size': size})
        data, count = document.get('data'), document.get('total')
        if (document.get('success') is not True or not isinstance(data, list)
                or type(count) is not int or count < 0 or document.get('page') != page
                or document.get('page_size') != size or total is not None and total != count
                or len(data) != min(size, max(0, count - (page - 1) * size))):
            raise HTTPException(409, '已有解析片段的分页信息不完整，请刷新后重试')
        if count > MAX_PARSED_CHUNKS:
            raise HTTPException(413, '已有解析片段过多，请保留文件引用或选择更小的资料')
        total = count
        for row in data:
            if (not isinstance(row, dict) or not isinstance(row.get('id'), str) or not row['id'] or len(row['id']) > 128
                    or row['id'] in identifiers or row.get('knowledge_id') != knowledge_id
                    or row.get('knowledge_base_id') != bound.knowledge_base_id
                    or type(row.get('tenant_id')) is not int or row['tenant_id'] != bound.knowledge_base_tenant_id
                    or row.get('chunk_type') != 'text' or type(row.get('status')) is not int or row['status'] not in {0, 2}
                    or not isinstance(row.get('updated_at'), str) or not row['updated_at'] or len(row['updated_at']) > 64
                    or type(row.get('chunk_index')) is not int or row['chunk_index'] <= previous_index
                    or type(row.get('content_revision')) is not int or row['content_revision'] < 0
                    or not isinstance(row.get('content'), str)):
                raise HTTPException(409, '已有解析片段的归属、顺序或修订版本不一致，未载入正文')
            identifiers.add(row['id'])
            previous_index = row['chunk_index']
            rows.append(row)
            text = '\n\n'.join(item['content'] for item in rows)
            try:
                if len(text.encode('utf-8')) > MAX_CONTENT_BYTES or len(text) > MAX_CONTENT_CHARACTERS:
                    raise HTTPException(413, '已有解析正文超过96 KiB或24000字，请保留文件引用或选择更小的资料')
            except UnicodeEncodeError:
                raise HTTPException(422, '已有解析正文包含无效文字，未载入替代内容') from None
        page += 1
    return rows


def _chunk_provenance(rows):
    return [{'id': row['id'], 'revision': row['content_revision'], 'index': row['chunk_index'],
        'updated_at': row.get('updated_at'), 'sha256': hashlib.sha256(row['content'].encode()).hexdigest()}
        for row in rows]


async def _parsed_content(scope, bound, source, source_binding_id, result):
    notice = '知识库已有解析片段，可能有重叠、表格或人工修订，不等同原件全文。'
    if source.get('parse_status') not in {'ready', 'completed'}:
        return {**result, 'supported': False, 'text': '', 'content_kind': 'parsed_fragments',
            'notice': '该文件尚无已完成的解析正文；可保留文件引用并在知识库预览。'}
    rows = await _parsed_chunk_rows(bound, source['id'])
    if not rows:
        return {**result, 'supported': False, 'text': '', 'content_kind': 'parsed_fragments',
            'notice': '该文件暂无可读的已有解析正文；可保留文件引用并在知识库预览。'}
    provenance = _chunk_provenance(rows)
    # Chunk edits need not change the original file hash. Recheck the bounded
    # chunk revision/content identities as well as the native file metadata.
    repeated = await _parsed_chunk_rows(bound, source['id'])
    if _chunk_provenance(repeated) != provenance:
        raise HTTPException(409, '解析正文在读取期间修订，请刷新后重新选择')
    _, after = await _native_file(scope, source_binding_id, source['id'])
    if _file_version(after) != result['file_version'] or after.get('parse_status') not in {'ready', 'completed'}:
        raise HTTPException(409, '文件在读取期间更新，请刷新后重新选择')
    text = '\n\n'.join(row['content'] for row in rows)
    return {**result, 'supported': True, 'text': text, 'content_kind': 'parsed_fragments',
        'sha256': hashlib.sha256(text.encode()).hexdigest(), 'notice': notice,
        'provenance': {'kind': 'knowledge_chunks', 'chunk_count': len(rows), 'chunks': provenance}}


@router.get('/citation-file-content')
async def citation_file_content(source_binding_id: str = Query(min_length=1, max_length=128),
        knowledge_id: str = Query(min_length=1, max_length=128),
        file_version: str = Query(pattern=r'^[a-f0-9]{64}$')):
    scope = require_scope()
    bound, source = await _native_file(scope, source_binding_id, knowledge_id)
    expected = _file_version(source)
    if file_version != expected:
        raise HTTPException(409, '引用文件的版本已更新，请重新选择；未读取替代正文')
    kind = _text(source.get('file_type')).lower().lstrip('.')
    result = {'knowledge_id': knowledge_id, 'file_version': expected,
        'filename': _text(source.get('file_name') or source.get('title'), '未命名知识库文件')}
    if kind not in TEXT_KINDS:
        return await _parsed_content(scope, bound, source, source_binding_id, result)
    size = source.get('file_size')
    if type(size) is int and (size < 0 or size > MAX_CONTENT_BYTES):
        raise HTTPException(413, '文件正文超过96 KiB，请保留为文件引用或在知识库查看')
    base = (settings.weknora_api_url or '').rstrip('/')
    parsed = urlsplit(base)
    if (not bound.authorization.startswith('Bearer ') or parsed.scheme not in {'http', 'https'}
            or not parsed.netloc or parsed.username or parsed.path or parsed.query or parsed.fragment):
        raise HTTPException(503, '知识库原件读取地址或授权无效')
    raw = bytearray()
    try:
        async with httpx.AsyncClient(timeout=30, follow_redirects=False, trust_env=False) as client:
            async with client.stream('GET', base + '/api/v1/knowledge/' + quote(knowledge_id, safe='') + '/download',
                    headers={'Authorization': bound.authorization, 'X-Tenant-ID': str(bound.identity.tenant_id)}) as response:
                if response.status_code != 200:
                    raise HTTPException(response.status_code if response.status_code in {401, 403, 404} else 503,
                        '文件正文暂不可读，原有策略内容已保留')
                length = response.headers.get('content-length')
                if length and length.isdecimal() and int(length) > MAX_CONTENT_BYTES:
                    raise HTTPException(413, '文件正文超过96 KiB，请保留为文件引用')
                async for chunk in response.aiter_bytes():
                    if len(raw) + len(chunk) > MAX_CONTENT_BYTES:
                        raise HTTPException(413, '文件正文超过96 KiB，请保留为文件引用')
                    raw.extend(chunk)
    except httpx.HTTPError:
        raise HTTPException(503, '文件正文读取未完成，原有策略内容已保留') from None
    registered = source.get('file_hash')
    if registered:
        if (not isinstance(registered, str) or len(registered) not in {32, 64}
                or any(char not in '0123456789abcdefABCDEF' for char in registered)
                or (hashlib.md5(raw).hexdigest() if len(registered) == 32 else hashlib.sha256(raw).hexdigest()) != registered.lower()):
            raise HTTPException(409, '文件正文与登记版本不一致，未载入替代内容')
    if type(size) is int and len(raw) != size:
        raise HTTPException(409, '文件正文大小与登记版本不一致，未载入替代内容')
    _, after = await _native_file(scope, source_binding_id, knowledge_id)
    if _file_version(after) != expected:
        raise HTTPException(409, '文件在读取期间更新，请重新选择后载入')
    try:
        text = raw.decode('utf-8-sig')
    except UnicodeDecodeError:
        raise HTTPException(422, '文件正文不是有效UTF-8，请转为UTF-8后载入；文件引用仍可保留') from None
    if len(text) > MAX_CONTENT_CHARACTERS:
        raise HTTPException(413, '文件正文超过24000字，请保留文件引用或选择更小的资料')
    return {**result, 'supported': True, 'text': text, 'sha256': hashlib.sha256(raw).hexdigest(),
        'content_kind': 'original_text', 'notice': ''}
