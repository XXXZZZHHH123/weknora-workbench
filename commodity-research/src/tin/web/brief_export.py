"""Export existing personal reports through native knowledge ingestion.

Native content hashes are the durable idempotency record. No research fact is
copied back into a second database, and no model participates in exporting.
"""
from contextlib import asynccontextmanager
from datetime import date, datetime, time, timedelta
import asyncio
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import time as clock
from urllib.parse import quote, urlencode, urlsplit

import httpx
from fastapi import APIRouter, HTTPException
from pydantic import BaseModel, ConfigDict, StrictStr
from sqlalchemy import select
from starlette.concurrency import run_in_threadpool

from tin.config import SHANGHAI, settings
from tin.export.board import snapshot
from tin.integration.identity import current_identity
from tin.integration.workbench_scope import current_workbench, go_request
from tin.models import DailyBrief, Observation
from tin.ownership import scope_query

router = APIRouter(prefix='/api/sn')


class ExportInput(BaseModel):
    model_config = ConfigDict(extra='forbid')
    folder_path: StrictStr = ''


def _scope():
    scope = current_workbench.get()
    identity = current_identity.get()
    if not settings.weknora_api_url or scope is None or identity is None:
        raise HTTPException(409, '请从绑定知识库的研究工作台导出日报')
    if (identity.tenant_id != scope.identity.tenant_id or identity.user_id != scope.identity.user_id
            or not scope.authorization.startswith('Bearer ')):
        raise HTTPException(403, '工作台登录范围不匹配')
    if not identity.can_write:
        raise HTTPException(403, '当前空间角色只有读取权限')
    return scope


def _folder_path(value):
    if (not isinstance(value, str) or len(value) > 1024 or '\\' in value
            or any(ord(c) < 32 or 127 <= ord(c) <= 159 for c in value)
            or (value and any(p in {'', '.', '..'} for p in value.split('/')))):
        raise HTTPException(400, '请选择知识库中已有的有效文件夹')
    return value


async def _folders(scope):
    kb = (await go_request(scope, '/api/v1/knowledge-bases/' + quote(scope.knowledge_base_id, safe=''))).get('data')
    if (not isinstance(kb, dict) or kb.get('id') != scope.knowledge_base_id
            or kb.get('tenant_id') != scope.knowledge_base_tenant_id):
        raise HTTPException(403, '绑定知识库范围已改变')
    if kb.get('my_permission') not in {'editor', 'admin'}:
        raise HTTPException(403, '当前账号没有知识库写入权限')
    document = await go_request(scope, '/api/v1/knowledge-bases/' + quote(scope.knowledge_base_id, safe='') + '/knowledge/folders')
    tree = document.get('data')
    if not isinstance(tree, dict) or not isinstance(tree.get('folders'), list):
        raise HTTPException(503, '知识库文件夹列表暂不可用')
    choices = [{'path': '', 'name': '知识库根目录', 'depth': 0}]
    seen = {''}

    def visit(nodes, parent='', depth=1):
        if depth > 24:
            raise HTTPException(503, '知识库目录层级过深，请先整理目录')
        for node in nodes:
            if not isinstance(node, dict) or not isinstance(node.get('path'), str):
                raise HTTPException(503, '知识库文件夹列表格式无效')
            path = _folder_path(node['path'])
            if (not path or path in seen or path.rpartition('/')[0] != parent
                    or node.get('name') != path.rpartition('/')[2]):
                raise HTTPException(503, '知识库文件夹目录关系无效')
            children = node.get('children')
            if children is None:
                children = []
            if not isinstance(children, list) or len(choices) >= 4096:
                raise HTTPException(503, '知识库文件夹列表超出可选择范围')
            seen.add(path)
            choices.append({'path': path, 'name': node['name'], 'depth': depth})
            if children:
                visit(children, path, depth + 1)
    visit(tree['folders'])
    return choices


def _session():
    # Preserve the original application's request-local scope and test overrides.
    from tin.web.app import SessionLocal
    return SessionLocal()


def _brief_payload(brief_id):
    with _session() as session:
        row = session.scalar(scope_query(session, select(DailyBrief).where(DailyBrief.id == brief_id), DailyBrief))
        if row is None:
            raise HTTPException(404, '日报不存在或不属于当前账号和工作台')
        return {key: (value.isoformat() if isinstance(value := getattr(row, key), datetime) else value)
                for key in ('id', 'trade_date', 'variety', 'status', 'tone', 'tone_note', 'stance', 'summary',
                            'range_low', 'range_high', 'range_series_id', 'claims', 'unverified',
                            'playbook_item_ids', 'signal_ids', 'model', 'generated_at', 'reviewed_by', 'reviewed_at')}


def _snapshot_payload(day):
    return _period_payload(day, 'daily')


def _period_payload(day, period):
    from tin.integration.kb_facts import page_read_scope
    from tin.integration.research_profile import research_variety
    from tin.web.report_period import build_period, period_start
    try:
        value = date.fromisoformat(day)
        if value.isoformat() != day:
            raise ValueError
    except ValueError:
        raise HTTPException(400, '日期格式应为 YYYY-MM-DD') from None
    with page_read_scope('reports', day=value, points_per_series=8 if period == 'weekly' else 2), _session() as session:
        start = datetime.combine(period_start(value, period), time.min, tzinfo=SHANGHAI)
        end = datetime.combine(value + timedelta(days=1), time.min, tzinfo=SHANGHAI)
        exists = session.scalar(select(Observation.id).where(Observation.status == '正常',
            Observation.as_of >= start, Observation.as_of < end).limit(1))
        variety = research_variety(session)
        payload = build_period(session, variety, value, period, source=snapshot(session, variety, value), preview_rules=True)
        if exists is None and not any(payload.get(key) for key in ('judgment','briefs','strategies','research_cards')):
            raise HTTPException(404, '当前知识库在该日没有可导出的数据快照')
    # Integrated reports are current-source snapshots, not pre-existing archives.
    # A rendering timestamp changes per click and must not create false versions.
    payload.pop('generated_at', None)
    return payload


def _json(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2, allow_nan=False)


def _markdown(scope, kind, identifier, folder, payload):
    report_day = payload.get('trade_date') or payload.get('as_of')
    try:
        if date.fromisoformat(report_day).isoformat() != report_day:
            raise ValueError
    except (ValueError, TypeError):
        raise HTTPException(409, '日报中的日期无效，请先核对原记录') from None
    weekly = kind == 'weekly'
    period = payload.get('period') or {}
    prefix = str(period.get('start') or report_day) + ' — ' + report_day if weekly else report_day
    title = f'{prefix} {scope.metadata.get("name") or "研究工作台"} ' + ('周报' if weekly else '日报')
    provenance = {'research_workbench_id': scope.workbench_id, 'knowledge_base_id': scope.knowledge_base_id,
                  'research_owner_id': scope.identity.user_id, 'report_kind': kind,
                  'report_id': str(identifier), 'export_folder': folder}
    # JSON strings are valid YAML scalar values; names are content, never paths.
    front = '\n'.join(f'{key}: {json.dumps(value, ensure_ascii=False)}' for key, value in provenance.items())
    lines = ['---', front, '---', '']
    if kind == 'brief':
        from tin.web.report_view import present_snapshot, _md
        lines += ['# ' + _md(title), '']
        label = str(payload.get('status') or '未登记')
        if label == '草稿':
            label += '（未经研究员确认）'
        lines += ['> 状态：' + label, '', '## 研究结论', '',
                  _md(payload.get('summary') or '未填写结论'), '',
                  '- 基调：' + _md(payload.get('tone') or '未填写'),
                  '- 基调说明：' + _md(payload.get('tone_note') or '未填写'),
                  '- 操作倾向：' + _md(payload.get('stance') or '未填写'), '']
        rendered = present_snapshot({'briefs': [payload]})['briefs'][0]
        lines += ['## 证据链', '']
        for claim in rendered['claims']:
            lines += ['- ' + _md(claim['text'])]
            for evidence in claim['evidence']:
                lines += ['  - ' + _md(evidence.get('name') or '已保存证据') + ' · '
                          + _md(evidence.get('value')) + ' · ' + _md(evidence.get('as_of'))]
        if rendered['unverified']:
            lines += ['', '## 待核验内容', '']
            lines += ['- ' + _md(item['text']) + ' · ' + _md(item['reason']) for item in rendered['unverified']]
    else:
        from tin.web.report_view import readable_markdown
        lines += [readable_markdown(payload, title=title)]
    lines += ['', '<details><summary>原始记录与来源审计</summary>', '', '```json', _json(payload), '```', '', '</details>', '']
    blob = '\n'.join(lines).encode('utf-8')
    if len(blob) > 4 * 1024 * 1024:
        raise HTTPException(413, '日报内容过大，请缩小归档范围')
    digest = hashlib.sha256(blob).hexdigest()
    filename = f'研究{"周报" if weekly else "日报"}_{report_day}_{digest[:12]}.md'
    return filename, blob, digest


@asynccontextmanager
async def _export_lock(scope, identity):
    # Serialize identical export attempts across workers; only hashes form paths.
    root = Path(settings.data_dir) / 'brief-export-locks'
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    key = hashlib.sha256(f'{scope.identity.tenant_id}:{scope.workbench_id}:{identity}'.encode()).hexdigest()
    descriptor = os.open(root / (key + '.lock'), os.O_CREAT | os.O_RDWR | os.O_CLOEXEC, 0o600)
    deadline = clock.monotonic() + 35
    acquired = False
    try:
        while not acquired:
            try:
                fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
                acquired = True
            except BlockingIOError:
                if clock.monotonic() >= deadline:
                    raise HTTPException(409, '同一日报正在导出，请稍后重试') from None
                await asyncio.sleep(.05)
        yield
    finally:
        if acquired:
            fcntl.flock(descriptor, fcntl.LOCK_UN)
        os.close(descriptor)


def _validate_document(scope, row, folder, content_hash):
    if (not isinstance(row, dict) or not isinstance(row.get('id'), str)
            or not re.fullmatch(r'[A-Za-z0-9_-]{1,128}', row['id'])
            or row.get('knowledge_base_id') != scope.knowledge_base_id
            or row.get('tenant_id') != scope.knowledge_base_tenant_id
            or row.get('folder_path', '') != folder or row.get('file_hash') != content_hash
            or str(row.get('file_type', '')).lower() != 'md'
            or row.get('parse_status') in {'deleting', 'deleted'}):
        raise HTTPException(409, '导出文档的知识库、目录或内容校验不一致，请刷新后重试')
    return row


async def _find_existing(scope, filename, folder, content_hash):
    """Names recall candidates; complete inventory, scope and MD5 decide reuse."""
    expected_total = None
    seen, matches = set(), []
    page = 1
    while True:
        document = await go_request(scope, '/api/v1/knowledge-bases/' + quote(scope.knowledge_base_id, safe='') + '/knowledge',
            params={'keyword': filename, 'file_type': 'md', 'folder_path': folder,
                    'page': page, 'page_size': 100, 'sort_by': 'created_at', 'sort_order': 'asc'})
        rows, total = document.get('data'), document.get('total')
        if (not isinstance(rows, list) or type(total) is not int or not 0 <= total <= 2000
                or type(document.get('page')) is not int or type(document.get('page_size')) is not int
                or document.get('page') != page or document.get('page_size') != 100):
            raise HTTPException(503, '知识库导出记录列表不完整，未创建重复文档，请刷新后重试')
        if expected_total is not None and total != expected_total:
            raise HTTPException(409, '知识库导出记录正在改变，请刷新后重试')
        expected_total = total
        if len(rows) != min(100, max(0, total - (page - 1) * 100)):
            raise HTTPException(503, '知识库导出记录分页不完整，未创建重复文档')
        for row in rows:
            if (not isinstance(row, dict) or not isinstance(row.get('id'), str)
                    or not re.fullmatch(r'[A-Za-z0-9_-]{1,128}', row['id'])
                    or row['id'] in seen or row.get('knowledge_base_id') != scope.knowledge_base_id
                    or row.get('tenant_id') != scope.knowledge_base_tenant_id
                    or row.get('folder_path', '') != folder):
                raise HTTPException(409, '知识库导出记录范围不一致，未复用或创建文档')
            seen.add(row['id'])
            if (row.get('file_name') == filename and row.get('file_hash') == content_hash
                    and str(row.get('file_type', '')).lower() == 'md'
                    and row.get('parse_status') not in {'deleting', 'deleted'}):
                matches.append(row)
        if len(seen) == total:
            break
        page += 1
    # Older duplicate documents may already exist. Prefer a live non-failed
    # result, then a stable actual ID; never create another copy to repair one.
    return min(matches, key=lambda row: (row.get('parse_status') == 'failed', row['id'])) if matches else None


async def _actual_result(scope, row, filename, folder, content_hash, reused):
    row = _validate_document(scope, row, folder, content_hash)
    actual = await go_request(scope, '/api/v1/knowledge/' + quote(row['id'], safe=''))
    live = _validate_document(scope, actual.get('data'), folder, content_hash)
    if live['id'] != row['id']:
        raise HTTPException(409, '知识库返回的实际文档编号不一致')
    url = '/platform/knowledge-bases/' + quote(scope.knowledge_base_id, safe='') + '?' + urlencode({'folder': folder, 'knowledge_id': live['id']})
    if live.get('parse_status') == 'failed':
        raise HTTPException(502, {'message': '文件已保存，但知识库解析失败，请在原文档处理详情重新解析',
            'knowledge_id': live['id'], 'knowledge_base_id': scope.knowledge_base_id, 'folder_path': folder, 'url': url})
    return {'knowledge_id': live['id'], 'knowledge_base_id': scope.knowledge_base_id,
            'folder_path': folder, 'file_name': live.get('file_name', filename),
            'parse_status': live.get('parse_status'), 'reused': reused, 'url': url}


async def _upload(scope, filename, blob, folder):
    content_hash = hashlib.md5(blob).hexdigest()
    existing = await _find_existing(scope, filename, folder, content_hash)
    if existing is not None:
        return await _actual_result(scope, existing, filename, folder, content_hash, True)
    target = folder + '/' + filename if folder else filename
    base = settings.weknora_api_url.rstrip('/')
    origin = urlsplit(base)
    try:
        async with httpx.AsyncClient(timeout=30, follow_redirects=False, trust_env=False) as client:
            response = await client.post(base + '/api/v1/knowledge-bases/' + quote(scope.knowledge_base_id, safe='') + '/knowledge/file',
                headers={'Authorization': scope.authorization, 'X-Tenant-ID': str(scope.identity.tenant_id),
                         'Origin': origin.scheme + '://' + origin.netloc},
                files={'file': (filename, blob, 'text/markdown; charset=utf-8')},
                data={'fileName': target})
        document = response.json()
    except (httpx.HTTPError, ValueError):
        raise HTTPException(503, '知识库上传服务暂不可用；原日报仍然保留，可重试') from None
    if not isinstance(document, dict):
        raise HTTPException(503, '知识库上传服务返回格式异常；请在知识库核对后重试')
    reused = response.status_code == 409 and document.get('code') == 'duplicate_file'
    if response.status_code in {401, 403}:
        raise HTTPException(response.status_code, '主平台登录已失效或没有知识库写入权限')
    if response.status_code == 413:
        raise HTTPException(413, '知识库拒绝接收过大的日报')
    if not (reused or response.status_code == 200 and document.get('success') is True):
        raise HTTPException(503, '知识库未确认保存成功；原日报仍然保留，可重试')
    # Verify the actual native object; do not trust a fabricated/replayed receipt.
    return await _actual_result(scope, document.get('data'), filename, folder, content_hash, reused)


async def _export(kind, identifier, body):
    scope = _scope()
    folder = _folder_path(body.folder_path)
    choices = await _folders(scope)
    if folder not in {item['path'] for item in choices}:
        raise HTTPException(400, '目标文件夹已不存在，请重新选择')
    lock_identity = hashlib.sha256(_json([scope.identity.user_id, kind, str(identifier), folder]).encode()).hexdigest()
    async with _export_lock(scope, lock_identity):
        # Capture one complete record inside the per-report lock. Simultaneous
        # clicks must not interleave its database snapshot or native upload.
        if kind == 'weekly':
            payload = await run_in_threadpool(_period_payload, identifier, 'weekly')
        else:
            payload = await run_in_threadpool(_brief_payload if kind == 'brief' else _snapshot_payload, identifier)
        filename, blob, _ = await run_in_threadpool(_markdown, scope, kind, identifier, folder, payload)
        # Permissions and directory existence were freshly validated this request;
        # native upload independently enforces write ownership again.
        return await _upload(scope, filename, blob, folder)


@router.get('/brief/export/folders')
async def export_folders():
    scope = _scope()
    return {'knowledge_base_id': scope.knowledge_base_id, 'folders': await _folders(scope)}


@router.post('/brief/{brief_id}/export')
async def export_brief(brief_id: int, body: ExportInput):
    if brief_id < 1:
        raise HTTPException(400, '日报编号无效')
    return await _export('brief', brief_id, body)


@router.post('/reports/{day}/export')
async def export_snapshot(day: str, body: ExportInput):
    return await _export('snapshot', day, body)


@router.post('/reports/{day}/weekly/export')
async def export_weekly(day: str, body: ExportInput):
    return await _export('weekly', day, body)
