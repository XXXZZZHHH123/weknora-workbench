"""Source-bound continuation of native knowledge uploads, never a browser upload API."""
from __future__ import annotations
from contextvars import ContextVar
import asyncio
import base64
import contextvars
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import threading
import time
import uuid

import httpx
from fastapi import HTTPException, Request
from starlette.datastructures import UploadFile
from .workspaces import Identity, current_identity, valid_knowledge_base_id

UPLOAD_PATH = '/internal/ingestion/upload'
CHECK_PATH = '/internal/ingestion/check'
RESULT_PATH = '/internal/ingestion/result'
FIELDS = {'attempt', 'tenant_id', 'knowledge_base_id', 'knowledge_id', 'user_id', 'file_name', 'file_type', 'file_hash', 'source_sha256', 'bundle_sha256', 'file_size'}
current_ingestion_claim = ContextVar('structured_ingestion_claim', default=None)
current_ingestion_model = ContextVar('structured_ingestion_model', default='')
MAX_BYTES = 100 * 1024 * 1024


def key_bytes():
    key = Path(os.environ['STRUCTURED_SERVICE_TOKEN_FILE']).read_bytes().strip()
    if len(key) < 32:
        raise ValueError('ingestion key unavailable')
    return key


def validate_claim(claim):
    if not isinstance(claim, dict) or set(claim) != FIELDS:
        raise ValueError('invalid ingestion identity')
    if any(not valid_knowledge_base_id(claim[k]) for k in ('attempt', 'knowledge_base_id', 'knowledge_id')):
        raise ValueError('invalid ingestion source')
    if type(claim['tenant_id']) is not int or claim['tenant_id'] <= 0 or type(claim['file_size']) is not int or not 0 < claim['file_size'] <= MAX_BYTES:
        raise ValueError('invalid ingestion size or tenant')
    if not isinstance(claim['user_id'], str) or len(claim['user_id']) > 128:
        raise ValueError('invalid ingestion actor')
    for name, size in [('file_hash', 32), ('source_sha256', 64), ('bundle_sha256', 64)]:
        if not isinstance(claim[name], str) or not re.fullmatch('[a-f0-9]{' + str(size) + '}', claim[name]):
            raise ValueError('invalid ingestion fingerprint')
    name = claim['file_name']
    if (claim['file_type'] not in {'pdf', 'xlsx'} or not isinstance(name, str) or not name or len(name) > 1024
            or name != Path(name.replace('\\', '/')).name or any(ord(c) < 32 for c in name)
            or not name.lower().endswith('.' + claim['file_type'])):
        raise ValueError('invalid ingestion filename')
    return claim


def signed_headers(claim, method, path, *, now=None, body=None):
    encoded = base64.urlsafe_b64encode(json.dumps(claim, separators=(',', ':')).encode()).rstrip(b'=').decode()
    stamp = str(int(time.time() if now is None else now))
    parts = [stamp, method, path, encoded]
    if body is not None:
        parts.append(hashlib.sha256(body).hexdigest())
    signature = hmac.new(key_bytes(), '\n'.join(parts).encode(), hashlib.sha256).hexdigest()
    headers = {'X-Ingestion-Claim': encoded, 'X-Ingestion-Timestamp': stamp, 'X-Ingestion-Signature': signature}
    if body is not None:
        headers['X-Ingestion-Body-SHA256'] = hashlib.sha256(body).hexdigest()
    return headers


def verify_ingestion(request):
    try:
        stamp = request.headers['x-ingestion-timestamp']
        encoded = request.headers['x-ingestion-claim']
        if len(stamp) > 20 or not stamp.isdecimal() or abs(time.time() - int(stamp)) > 60 or len(encoded) > 4096:
            raise ValueError('expired ingestion grant')
        expected = hmac.new(key_bytes(), '\n'.join([stamp, request.method, request.url.path, encoded]).encode(), hashlib.sha256).hexdigest()
        if not hmac.compare_digest(expected, request.headers.get('x-ingestion-signature', '')):
            raise ValueError('invalid ingestion signature')
        return validate_claim(json.loads(base64.urlsafe_b64decode(encoded + '=' * (-len(encoded) % 4))))
    except (KeyError, ValueError, TypeError, OSError):
        raise HTTPException(401, '上传处理任务未通过来源核验') from None


def task_identity(claim):
    return Identity(claim['user_id'] or 'ingestion:' + claim['attempt'], '', '知识库上传任务', claim['tenant_id'],
                    'contributor', claim['knowledge_base_id'], claim['tenant_id'], True)


def check_task_write(claim):
    """The native owner rechecks the admitted attempt, cancellation and current source."""
    async def check():
        base = os.environ.get('WEKNORA_API_URL', 'http://127.0.0.1:18081').rstrip('/')
        async with httpx.AsyncClient(trust_env=False, timeout=15, follow_redirects=False) as client:
            response = await client.post(base + CHECK_PATH, headers=signed_headers(claim, 'POST', CHECK_PATH))
        if response.status_code != 200 or response.json().get('valid') is not True:
            raise ValueError('原件、处理任务或知识库归属已改变，已停止自动入库')
    try:
        asyncio.run(check())
    except (httpx.HTTPError, KeyError, TypeError):
        raise ValueError('暂时无法核验原件处理权限，未自动入库') from None


def install_ingestion_routes(app, registry):
    running = set()
    lock = threading.Lock()

    def state_path(claim):
        return registry.directory() / 'ingestion' / (claim['attempt'] + '.json')

    def save(claim, state):
        from .pdf_api import _write
        _write(state_path(claim), {'claim': claim, **state})

    def run(claim, path, envelope):
        try:
            current_ingestion_model.set(envelope.get('native_default_model_id') or '')
            guard = lambda: check_task_write(claim)
            guard()
            directory = registry.directory()
            progress = lambda stage: save(claim, {'status': 'processing', 'stage': str(stage)[:500]})
            native = {'source_knowledge_id': claim['knowledge_id'], 'file_name': claim['file_name'],
                      'file_size': claim['file_size'], 'file_hash': claim['file_hash'],
                      'knowledge_base_id': claim['knowledge_base_id'], 'tenant_id': claim['tenant_id']}
            if claim['file_type'] == 'xlsx':
                from .review_worker import run_review_task
                import_lock = registry.objects()['import_lock']
                if not import_lock.acquire(blocking=False):
                    raise ValueError('本知识库已有工作表正在处理，请稍后重试')
                try:
                    result = run_review_task(directory, 'import', path=path, native_source=native,
                        use_reviewed_mappings=True, publish_guard=guard,
                        progress_callback=lambda value: progress(value.get('stage', '正在整理工作表')))
                    manager = registry.objects()['manager']
                    doc = manager.publication_view(manager.get(result['import_id']))
                    if any(sheet.get('status') not in {'auto_ready', 'template_ready', 'confirmed', 'trusted_existing', 'skip', 'chart_ready'} for sheet in doc['sheets']):
                        from .batch_ai_import import import_all_with_ai
                        assisted = import_all_with_ai(manager, result['import_id'], total_max_requests=64,
                            per_sheet_max_requests=8, max_seconds=1200,
                            task=lambda action, **kwargs: run_review_task(directory, action,
                                **({'publish_guard':guard} if action in {'confirm','mark_skip'} else {}), **kwargs),
                            check_write=guard, cancelled=lambda: False,
                            progress=lambda value: progress(value.get('stage', '正在核对工作表')))
                        result['automatic_review'] = assisted
                finally:
                    import_lock.release()
                doc = manager.publication_view(manager.get(result['import_id']))
                sheets = doc['sheets']
                result = {**result, 'sheets': [{'name': sheet['name'], 'status': sheet['status']} for sheet in sheets],
                          'attention_count': sum(sheet.get('status') not in {'auto_ready', 'template_ready', 'confirmed', 'trusted_existing', 'skip', 'chart_ready'} for sheet in sheets)}
            else:
                from .pdf_tables import PDFTableStore
                from .pdf_parser_bridge import normalize_parser_response
                from .pdf_automation import process_draft
                from .pdf_api import _write
                source = {'sha256': claim['source_sha256'], 'file_name': claim['file_name'],
                          'knowledge_id': claim['knowledge_id'], 'knowledge_base_id': claim['knowledge_base_id']}
                if envelope.get('engine') == 'remote_vlm':
                    bundle = envelope['bundle']
                    if bundle.get('source', {}).get('sha256') != claim['source_sha256']:
                        raise ValueError('PDF 识别结果与原件不一致')
                    bundle['source'].update(source)
                elif envelope.get('engine') in {'mineru_v1', 'mineru_legacy'}:
                    bundle = normalize_parser_response(envelope['engine'], envelope['response'], source, allow_empty_tables=True)
                else:
                    result = {'draft_id':None, 'attention_count':1,
                              'problem_code':'PDF_LAYOUT_UNAVAILABLE',
                              'message':'文字和图片已保留，但当前识别服务没有返回表格位置。请联系管理员启用表格识别后重新处理这份文件。'}
                    guard()
                    save(claim, {'status':'completed','stage':result['message'],'result':result})
                    return
                if not isinstance(bundle.get('fragments'), list):
                    raise ValueError('PDF 识别结果没有提供有效的表格列表')
                page_attention = len({c.get('page') for c in bundle.get('page_context', [])
                    if c.get('backend') == 'local_ocr' or c.get('issues')})
                if not bundle.get('fragments'):
                    page_attention = max(page_attention, int(bool(bundle.get('requires_review'))))
                    guard()
                    result = {'draft_id':None,'table_count':0,'accepted_count':0,'attention_count':page_attention,
                              'message':'没有识别到表格，文字和图片仍保存在本知识库文件中。'}
                    save(claim, {'status':'completed','stage':result['message'],'result':result})
                    return
                original = directory / 'pdf-tables' / 'originals' / (claim['source_sha256'] + '.pdf')
                original.parent.mkdir(parents=True, exist_ok=True)
                if original.exists():
                    if hashlib.sha256(original.read_bytes()).hexdigest() != claim['source_sha256']:
                        raise ValueError('已保存的PDF原件校验失败')
                else:
                    import shutil
                    pending = original.with_suffix('.' + uuid.uuid4().hex + '.tmp')
                    try:
                        shutil.copyfile(path, pending)
                        guard()
                        os.replace(pending, original)
                    finally:
                        pending.unlink(missing_ok=True)
                store = PDFTableStore(directory / 'pdf-tables')
                guard()
                draft = store.create_draft(bundle)
                auto = directory / 'pdf-tables' / 'automation' / (draft['draft_id'] + '.json')
                previous = json.loads(auto.read_text()) if auto.exists() else None
                result = process_draft(store, draft['draft_id'], directory, previous=previous,
                    save=lambda state: _write(auto, state), progress=progress, before_publish=guard)
                result = {**result, 'draft_id': draft['draft_id'],
                          'attention_count':result.get('attention_count',0)+page_attention,
                          'page_attention_count':page_attention}
            guard()
            save(claim, {'status': 'completed', 'stage': '表格处理完成', 'result': result})
        except Exception as exc:
            from .pdf_errors import processing_problem
            problem = processing_problem(exc, stage='source')
            save(claim, {'status': 'failed', 'stage': problem['title'], 'error': problem['message'], 'problem': problem})
        finally:
            path.unlink(missing_ok=True)
            with lock:
                running.discard(str(state_path(claim)))

    @app.post(UPLOAD_PATH)
    async def upload(request: Request):
        claim = request.state.ingestion_claim
        current_ingestion_claim.set(claim)
        from starlette.concurrency import run_in_threadpool
        await run_in_threadpool(check_task_write, claim)
        target = state_path(claim)
        if target.exists():
            saved = json.loads(target.read_text())
            if saved.get('claim') != claim:
                raise HTTPException(409, '处理任务与原件不一致')
            if saved.get('status') == 'completed' or str(target) in running:
                return {'attempt': claim['attempt'], 'status': saved['status'], 'reused': True}
        form = await request.form(max_files=1, max_fields=1, max_part_size=32 * 1024 * 1024)
        if set(form) != {'file', 'bundle'} or not isinstance(form['file'], UploadFile) or not isinstance(form['bundle'], str):
            raise HTTPException(400, '处理任务需要原件和来源信息')
        raw_bundle = form['bundle'].encode()
        if len(raw_bundle) > 32 * 1024 * 1024 or hashlib.sha256(raw_bundle).hexdigest() != claim['bundle_sha256']:
            raise HTTPException(409, '识别结果校验失败')
        envelope = json.loads(raw_bundle)
        if not isinstance(envelope, dict):
            raise HTTPException(400, '识别结果格式无效')
        directory = target.parent / 'uploads'
        directory.mkdir(parents=True, exist_ok=True)
        path = directory / (uuid.uuid4().hex + '.' + claim['file_type'])
        try:
            size, md5, sha = 0, hashlib.md5(), hashlib.sha256()
            with path.open('xb') as stream:
                while chunk := await form['file'].read(1024 * 1024):
                    size += len(chunk)
                    if size > claim['file_size'] or size > MAX_BYTES:
                        raise HTTPException(413, '原件超过允许的大小')
                    md5.update(chunk); sha.update(chunk); stream.write(chunk)
            if size != claim['file_size'] or md5.hexdigest() != claim['file_hash'] or sha.hexdigest() != claim['source_sha256']:
                raise HTTPException(409, '上传内容与知识库原件不一致')
            with lock:
                if str(target) in running:
                    path.unlink(missing_ok=True)
                    return {'attempt': claim['attempt'], 'status': 'processing', 'reused': True}
                running.add(str(target))
            save(claim, {'status': 'processing', 'stage': '正在整理知识库表格'})
            context = contextvars.copy_context()
            threading.Thread(target=context.run, args=(run, claim, path, envelope), daemon=True).start()
        except BaseException:
            path.unlink(missing_ok=True)
            raise
        finally:
            await form['file'].close()
        return {'attempt': claim['attempt'], 'status': 'processing'}

    @app.get(RESULT_PATH)
    def result(request: Request):
        claim = request.state.ingestion_claim
        check_task_write(claim)
        path = state_path(claim)
        if not path.exists():
            raise HTTPException(404, '处理任务不存在')
        state = json.loads(path.read_text())
        if state.get('status') == 'processing' and str(path) not in running:
            raise HTTPException(503, '后台处理已中断，知识库任务将自动恢复')
        if state.pop('claim', None) != claim:
            raise HTTPException(409, '处理任务与原件不一致')
        return state
