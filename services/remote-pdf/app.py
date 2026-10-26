"""Authenticated, bounded remote PDF worker shared by native and structured ingestion."""
import asyncio
from contextlib import asynccontextmanager
import fcntl
import hashlib
import hmac
import json
import logging
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time

from fastapi import Depends, FastAPI, Header, HTTPException, Request
from fastapi.responses import JSONResponse, FileResponse
from starlette.datastructures import UploadFile as StarletteUploadFile
from starlette.formparsers import MultiPartException
from starlette.exceptions import HTTPException as StarletteHTTPException
from store import Store, QueueFull, RetryConflict, RetryOwnerMismatch

log = logging.getLogger('remote_pdf')

DEFAULTS = dict(document_concurrency=2, page_concurrency=2, global_outstanding=16,
                owner_outstanding=4, scope_outstanding=8, max_upload_bytes=100*1024*1024, max_uploads=4,
                queue_timeout_seconds=1800, execution_timeout_seconds=1800,
                retention_seconds=86400, disk_reserve_bytes=512*1024*1024,
                max_storage_bytes=5*1024**3, max_job_output_bytes=256*1024**2)


def load_settings():
    path = os.environ.get('REMOTE_PDF_SETTINGS_FILE', '/run/secrets/remote-pdf.json')
    config = json.loads(Path(path).read_text())
    config['data_dir'] = os.environ.get('REMOTE_PDF_DATA_DIR', config.get('data_dir', '/data'))
    return config


def ocr_runtime_profile(config, runner=subprocess.run):
    """Fingerprint installed OCR code/data once at startup, without calling any model API."""
    if not config.get('fallback_ocr', False):
        return None
    options = dict(check=True, capture_output=True, text=True, timeout=10,
                   env={**os.environ, 'LC_ALL': 'C'})
    version = runner(['tesseract', '--version'], **options).stdout.strip()
    listing = runner(['tesseract', '--list-langs'], **options).stdout
    first_line = listing.splitlines()[0] if listing else ''
    data_path = first_line.partition('"')[2].partition('"')[0]
    if not version or not data_path or not Path(data_path).is_absolute():
        raise ValueError('Unable to fingerprint configured OCR runtime')
    languages = {}
    for language in sorted(set(config.get('ocr_languages', 'chi_sim+eng').split('+'))):
        if not language or any(c not in 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_' for c in language):
            raise ValueError('Invalid configured OCR language')
        digest = hashlib.sha256()
        with (Path(data_path)/(language+'.traineddata')).open('rb') as stream:
            for block in iter(lambda: stream.read(1024*1024), b''):
                digest.update(block)
        languages[language] = digest.hexdigest()
    return {'version': version, 'traineddata_sha256': languages}


def safe_error(exc):
    # Exception text may contain provider URLs, request payloads, API keys or filenames.
    code = getattr(exc, 'code', '')
    if code == 'PARSER_STORAGE_EXHAUSTED':
        return {'code': code, 'message': '解析存储配额不足，请管理员清理或扩容后重试。'}
    allowed = {'INVALID_PDF', 'PDF_TOO_LARGE', 'PDF_ENCRYPTED', 'PDF_PAGE_LIMIT',
               'MODEL_AUTH_FAILED', 'MODEL_RATE_LIMIT', 'MODEL_UNAVAILABLE',
               'MODEL_TIMEOUT', 'MODEL_CONTENT_FILTER', 'INVALID_MODEL_OUTPUT', 'PDF_RENDER_FAILED',
               'REMOTE_PDF_EXTRACTION_FAILED', 'REMOTE_PDF_MODEL_REQUEST_FAILED', 'REMOTE_PDF_OUTPUT_INVALID'}
    if isinstance(exc, TimeoutError):
        return {'code': 'EXECUTION_TIMEOUT', 'message': '远程解析超过执行时限；已保留完成的页面，可手动重试。'}
    if isinstance(exc, OSError) and exc.errno == 28:
        return {'code': 'PARSER_STORAGE_EXHAUSTED', 'message': '解析存储空间不足，请管理员清理或扩容后重试。'}
    messages = {
        'MODEL_CONTENT_FILTER': '模型服务拒绝处理此页，未返回识别结果；请更换获准模型或启用本地 OCR，并保留页面审核。',
        'MODEL_AUTH_FAILED': '模型服务拒绝认证或授权，请管理员检查现有模型配置与账户状态。',
        'MODEL_RATE_LIMIT': '模型服务限流，重试预算已用完；请稍后手动重试。',
        'MODEL_TIMEOUT': '模型服务请求超时，已保留完成页面，请稍后手动重试。',
        'MODEL_UNAVAILABLE': '模型服务暂不可用，已保留完成页面，请稍后手动重试。',
        'INVALID_PDF': '原文件不是有效 PDF，请检查文件后重新上传。',
        'PDF_ENCRYPTED': 'PDF 已加密，请先解密后上传。',
        'PDF_PAGE_LIMIT': 'PDF 页数超过解析服务限制，请拆分文件后重试。',
        'REMOTE_PDF_OUTPUT_INVALID': '模型返回的页面结构无效，受控修复未成功，请核对原件后手动重试。',
    }
    return {'code': code if code in allowed else 'REMOTE_PARSE_FAILED',
            'message': messages.get(code, '远程文档解析失败，请检查模型服务状态或原文件；已保留完成页面。')}



def public(job):
    return {k: job[k] for k in ('id', 'status', 'created', 'started', 'finished', 'execution_used')} | {
        'engine': 'remote_vlm', 'progress': json.loads(job['progress']),
        'error': json.loads(job['error']) if job['error'] else None,
    }


class StorageQuotaError(Exception):
    code = 'PARSER_STORAGE_EXHAUSTED'


class BodyLimit:
    """Bound multipart bodies before Starlette spools them to disk, including chunked requests."""
    def __init__(self, app, limit, uploads):
        self.app, self.limit, self.uploads = app, limit, uploads
        self.active = 0

    async def __call__(self, scope, receive, send):
        if scope['type'] != 'http' or scope['method'] != 'POST' or scope['path'] != '/v1/jobs':
            return await self.app(scope, receive, send)
        if self.active >= self.uploads:
            return await JSONResponse({'detail': '上传并发已满，请稍后重试。'}, 429, headers={'Retry-After': '10'})(scope, receive, send)
        self.active += 1
        size = 0
        async def bounded_receive():
            nonlocal size
            message = await receive()
            if message['type'] == 'http.request':
                size += len(message.get('body', b''))
                if size > self.limit:
                    raise MultiPartException('PDF_BODY_LIMIT')
            return message
        try:
            await self.app(scope, bounded_receive, send)
        finally:
            self.active -= 1


class Worker:
    def __init__(self, settings, parser):
        self.settings = DEFAULTS | settings
        s = self.settings
        if not s.get('token') or len(s['token']) < 24:
            raise ValueError('A private token of at least 24 characters is required')
        for key in DEFAULTS:
            if not isinstance(s[key], (int, float)) or s[key] <= 0:
                raise ValueError('Invalid positive queue setting: ' + key)
        self.root = Path(s['data_dir'])
        self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.root.chmod(0o700)
        (self.root/'tmp').mkdir(mode=0o700, exist_ok=True)
        self.lock = (self.root / '.worker.lock').open('a')
        try:
            fcntl.flock(self.lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError('Only one remote PDF scheduler may own a data directory')
        self.store = Store(self.root/'jobs.sqlite3', int(s['global_outstanding']), int(s['owner_outstanding']), s['retention_seconds'], int(s['scope_outstanding']))
        self.parser = parser
        config = s.get('extractor', {})
        # The real extractor includes effective defaults, prompt and credential digest; no plaintext key is stored.
        profile = dict(s.get('_parser_profile') or {k: v for k, v in config.items() if k not in ('api_key',)})
        profile['service_schema'] = 'remote_pdf_v1.1'
        profile['ocr_runtime'] = ocr_runtime_profile(config)
        extractor_source = Path(__file__).parent/'remote_pdf'/'extractor.py'
        if extractor_source.is_file():
            profile['extractor_sha256'] = hashlib.sha256(extractor_source.read_bytes()).hexdigest()
        watermark_source = Path(__file__).parent/'remote_pdf'/'watermarks.py'
        if watermark_source.is_file():
            profile['watermark_sha256'] = hashlib.sha256(watermark_source.read_bytes()).hexdigest()
        self.profile = hashlib.sha256(json.dumps(profile, sort_keys=True).encode()).hexdigest()
        self.page_semaphore = asyncio.Semaphore(int(s['page_concurrency']))
        self.running = {}
        self.stop = False
        self.schedule_task = None

    def directory(self, job_id):
        return self.root / 'jobs' / job_id

    def capacity(self, owner, scope):
        self.store.check_capacity(owner, scope)
        if shutil.disk_usage(self.root).free < self.settings['disk_reserve_bytes'] + self.settings['max_upload_bytes']:
            raise HTTPException(507, '解析存储空间不足。')
        used = sum(p.stat().st_size for p in self.root.rglob('*') if p.is_file())
        active = sum(self.store.counts().get(k, 0) for k in ('queued', 'running'))
        reserved = (active+1)*self.settings['max_job_output_bytes'] + self.settings['max_upload_bytes']*self.settings['max_uploads']
        if used + reserved > self.settings['max_storage_bytes']:
            raise HTTPException(507, '解析存储配额已满，请清理过期任务后重试。')

    def cleanup(self):
        for job_id in self.store.expired():
            shutil.rmtree(self.directory(job_id), ignore_errors=True)
            self.store.delete(job_id)

    async def start(self):
        self.store.recover()
        # No requests exist yet and the exclusive lock is held: these are interrupted uploads only.
        for orphan in self.root.glob('upload-*'):
            if orphan.is_file():
                orphan.unlink()
        self.cleanup()
        self.schedule_task = asyncio.create_task(self.schedule())

    async def close(self):
        self.stop = True
        if self.schedule_task:
            self.schedule_task.cancel()
            await asyncio.gather(self.schedule_task, return_exceptions=True)
        pending = list(self.running.values())
        for task in pending:
            task.cancel()
        await asyncio.gather(*pending, return_exceptions=True)
        self.store.close()
        fcntl.flock(self.lock, fcntl.LOCK_UN)
        self.lock.close()

    async def schedule(self):
        cleanup_at = 0
        while True:
            self.store.expire_queue(self.settings['queue_timeout_seconds'])
            if time.monotonic() > cleanup_at:
                self.cleanup()
                cleanup_at = time.monotonic() + 60
            while len(self.running) < self.settings['document_concurrency']:
                job = self.store.take()
                if not job:
                    break
                task = asyncio.create_task(self.execute(job))
                self.running[job['id']] = task
                task.add_done_callback(lambda _, job_id=job['id']: self.running.pop(job_id, None))
            await asyncio.sleep(.1)

    async def execute(self, job):
        job_id = job['id']
        start = time.monotonic()
        elapsed = lambda: job['execution_used'] + time.monotonic() - start
        progress_state = json.loads(job['progress'])
        storage_exhausted = False
        executing_task = asyncio.current_task()
        async def progress(value):
            nonlocal progress_state
            # Extractor owns this trusted schema, but don't expose arbitrary provider strings.
            allowed = ('stage', 'page', 'completed_pages', 'total_pages', 'cached_pages', 'tables', 'review_required')
            incoming = {k: value[k] for k in allowed if k in value}
            if 'complete_pages' in value and 'completed_pages' not in incoming:
                incoming['completed_pages'] = value['complete_pages']
            for key in ('completed_pages', 'cached_pages'):
                if key in incoming:
                    incoming[key] = max(progress_state.get(key, 0), incoming[key])
            progress_state.update(incoming)
            self.store.progress(job_id, progress_state, elapsed())
        async def heartbeat():
            nonlocal storage_exhausted
            while True:
                output_bytes = sum(p.stat().st_size for p in self.directory(job_id).rglob('*') if p.is_file() and p.name != 'input.pdf')
                if output_bytes > self.settings['max_job_output_bytes'] or shutil.disk_usage(self.root).free < self.settings['disk_reserve_bytes']:
                    storage_exhausted = True
                    executing_task.cancel()
                    return
                self.store.progress(job_id, progress_state, elapsed())
                await asyncio.sleep(1)
        heartbeat_task = asyncio.create_task(heartbeat())
        try:
            budget = self.settings['execution_timeout_seconds'] - job['execution_used']
            if budget <= 0:
                raise TimeoutError()
            if job['profile'] != self.profile:
                # Never resume an old job under a silently changed model/prompt/config.
                self.store.finish(job_id, 'failed', {'code': 'PARSER_PROFILE_CHANGED', 'message': '解析配置已更新，请重新提交文件。'}, elapsed())
                return
            output_dir = self.directory(job_id)
            result = await asyncio.wait_for(self.parser(output_dir/'input.pdf', output_dir,
                    config=self.settings.get('extractor', {}), page_semaphore=self.page_semaphore,
                    progress=progress, cancelled=lambda: self.store.get(job_id)['status'] == 'cancelled'), budget)
            # Completion is atomic; a completed record always has a complete result file.
            target = output_dir/'result.json'
            temp = output_dir/'result.json.tmp'
            with temp.open('w') as f:
                json.dump(result, f, ensure_ascii=False)
                f.flush()
                os.fsync(f.fileno())
            temp.replace(target)
            self.store.finish(job_id, 'completed', elapsed=elapsed())
        except asyncio.CancelledError:
            if self.stop:
                self.store.progress(job_id, progress_state, elapsed())
                self.store.requeue(job_id)
            elif storage_exhausted:
                self.store.finish(job_id, 'failed', safe_error(StorageQuotaError()), elapsed=elapsed())
            else:
                self.store.finish(job_id, 'cancelled', elapsed=elapsed())
            raise
        except Exception as exc:
            log.error('PDF job %s failed with %s', job_id, type(exc).__name__)
            self.store.finish(job_id, 'failed', safe_error(exc), elapsed())
        finally:
            heartbeat_task.cancel()
            await asyncio.gather(heartbeat_task, return_exceptions=True)


def create_app(settings=None, parser=None):
    settings = DEFAULTS | (load_settings() if settings is None else settings)
    if parser is None:
        from remote_pdf.extractor import ExtractorConfig, parse_pdf
        settings['_parser_profile'] = ExtractorConfig(**settings.get('extractor', {})).profile()
        parser = parse_pdf
    worker = Worker(settings, parser)
    @asynccontextmanager
    async def lifespan(app):
        await worker.start()
        yield
        await worker.close()
    app = FastAPI(lifespan=lifespan, docs_url=None, redoc_url=None, openapi_url=None)
    app.state.worker = worker
    app.add_middleware(BodyLimit, limit=settings['max_upload_bytes']+1024*1024, uploads=settings['max_uploads'])

    async def identity(authorization: str = Header(default=''), x_parse_scope: str = Header(default=''),
                       x_parse_owner: str = Header(default='')):
        expected = 'Bearer '+settings['token']
        if not hmac.compare_digest(authorization.encode(), expected.encode()):
            raise HTTPException(401, 'Unauthorized')
        if not x_parse_scope or len(x_parse_scope) > 512 or len(x_parse_owner) > 512:
            raise HTTPException(400, 'Trusted X-Parse-Scope is required')
        scope = hashlib.sha256(x_parse_scope.encode()).hexdigest()
        # Owners are namespaced by scope; arbitrary strings never become paths or public output.
        owner = hashlib.sha256((x_parse_scope+'\0'+(x_parse_owner or x_parse_scope)).encode()).hexdigest()
        return scope, owner

    @app.get('/health')
    async def health():
        return {'status': 'ok', 'engine': 'remote_vlm', 'schema': 'remote_pdf_v1'}

    @app.post('/v1/jobs', status_code=202)
    async def submit(request: Request, identity=Depends(identity)):
        scope, owner = identity
        worker.cleanup()
        # Existing cached jobs can still be reused when queue is full, so final queue admission follows hashing.
        if shutil.disk_usage(worker.root).free < settings['disk_reserve_bytes'] + settings['max_upload_bytes']:
            raise HTTPException(507, '解析存储空间不足。')
        tmp_path = None
        try:
            async with request.form(max_files=1, max_fields=2) as form:
                upload = form.get('file')
                if not isinstance(upload, StarletteUploadFile):
                    raise HTTPException(422, 'A PDF file is required')
                with tempfile.NamedTemporaryFile(dir=worker.root, prefix='upload-', delete=False) as tmp:
                    tmp_path = Path(tmp.name)
                    digest, size = hashlib.sha256(), 0
                    while chunk := await upload.read(1024*1024):
                        size += len(chunk)
                        if size > settings['max_upload_bytes']:
                            raise HTTPException(413, 'PDF 超过上传大小限制。')
                        digest.update(chunk)
                        tmp.write(chunk)
                with tmp_path.open('rb') as f:
                    if b'%PDF-' not in f.read(1024):
                        raise HTTPException(422, '上传文件不是有效 PDF。')
                existing = worker.store.find(scope, digest.hexdigest(), worker.profile)
                if existing:
                    return public(existing) | {'reused': True, 'lease_id': worker.store.lease(existing['id'])}
                worker.capacity(owner, scope)
                job, reused = worker.store.insert(scope, owner, digest.hexdigest(), worker.profile, size)
                directory = worker.directory(job['id'])
                directory.mkdir(parents=True, mode=0o700)
                tmp_path.replace(directory/'input.pdf')
                return public(job) | {'reused': reused, 'lease_id': worker.store.lease(job['id'])}
        except QueueFull:
            raise HTTPException(429, '解析队列已满，请稍后重试。', headers={'Retry-After': '10'})
        except StarletteHTTPException as exc:
            if exc.status_code == 400 and exc.detail == 'PDF_BODY_LIMIT':
                raise HTTPException(413, 'PDF 超过上传大小限制。') from exc
            raise
        finally:
            if tmp_path:
                tmp_path.unlink(missing_ok=True)

    def lookup(job_id, identity):
        job = worker.store.get(job_id, identity[0])
        if not job:
            raise HTTPException(404, 'Job not found')
        return job

    @app.get('/v1/jobs/{job_id}')
    async def status(job_id: str, identity=Depends(identity)):
        return public(lookup(job_id, identity))

    @app.get('/v1/jobs/{job_id}/result')
    async def result(job_id: str, identity=Depends(identity)):
        job = lookup(job_id, identity)
        if job['status'] != 'completed':
            raise HTTPException(409, 'Job has no completed result')
        path = worker.directory(job_id)/'result.json'
        if not path.is_file():
            raise HTTPException(503, 'Result unavailable')
        return FileResponse(path, media_type='application/json')

    page_image_lock = asyncio.Lock()

    @app.get('/v1/jobs/{job_id}/pages/{page}/image')
    async def page_image(job_id: str, page: int, identity=Depends(identity)):
        job = lookup(job_id, identity)
        if job['status'] != 'completed':
            raise HTTPException(409, 'PDF result is not complete')
        directory = worker.directory(job_id)
        result_file = directory / 'result.json'
        if not result_file.is_file():
            raise HTTPException(503, 'PDF source result unavailable')
        artifact = json.loads(result_file.read_text())
        if not 1 <= page <= artifact.get('source', {}).get('page_count', 0):
            raise HTTPException(404, 'PDF page not found')
        with (directory/'input.pdf').open('rb') as original:
            source_sha = hashlib.file_digest(original, 'sha256').hexdigest()
        if source_sha != artifact.get('source', {}).get('sha256'):
            raise HTTPException(409, 'PDF original no longer matches its result')
        target = directory / 'page-images' / f'page-{page:04d}.png'
        async with page_image_lock:
            if not target.is_file():
                from remote_pdf.extractor import _render
                config = settings.get('extractor', {})
                if shutil.disk_usage(worker.root).free < settings['disk_reserve_bytes'] + 32*1024*1024:
                    raise HTTPException(507, 'PDF page image storage unavailable')
                raw, _ = await asyncio.to_thread(_render, directory/'input.pdf', page,
                    config.get('dpi', 180), config.get('max_image_pixels', 16_000_000))
                if len(raw) > 16*1024*1024:
                    raise HTTPException(413, 'PDF page image exceeds size limit')
                used = sum(p.stat().st_size for p in worker.root.rglob('*') if p.is_file())
                job_used = sum(p.stat().st_size for p in directory.rglob('*') if p.is_file() and p.name != 'input.pdf')
                active = sum(worker.store.counts().get(k, 0) for k in ('queued', 'running'))
                reserved = active * settings['max_job_output_bytes']
                if (job_used + len(raw) > settings['max_job_output_bytes']
                        or used + reserved + len(raw) > settings['max_storage_bytes']):
                    raise HTTPException(507, 'PDF page image storage quota exceeded')
                target.parent.mkdir(exist_ok=True)
                pending = target.with_suffix('.tmp')
                try:
                    pending.write_bytes(raw)
                    pending.replace(target)
                finally:
                    pending.unlink(missing_ok=True)
        return FileResponse(target, media_type='image/png', headers={'Cache-Control': 'private, no-store', 'X-Source-SHA256': source_sha, 'X-PDF-Page': str(page)})

    @app.delete('/v1/jobs/{job_id}')
    async def cancel(job_id: str, x_parse_lease: str = Header(default=''), identity=Depends(identity)):
        job = lookup(job_id, identity)
        remaining = worker.store.detach(job_id, x_parse_lease)
        if remaining is None:
            raise HTTPException(404, 'Lease not found')
        if remaining == 0 and job['status'] in ('queued', 'running'):
            worker.store.finish(job_id, 'cancelled')
            task = worker.running.get(job_id)
            if task:
                task.cancel()
                await asyncio.gather(task, return_exceptions=True)
        return public(worker.store.get(job_id))

    @app.post('/v1/jobs/{job_id}/retry', status_code=202)
    async def retry(job_id: str, x_parse_retry_key: str = Header(default=''), identity=Depends(identity)):
        job = lookup(job_id, identity)
        if not x_parse_retry_key or len(x_parse_retry_key) > 128 or any(ord(c) < 33 or ord(c) > 126 for c in x_parse_retry_key):
            raise HTTPException(400, 'A unique trusted X-Parse-Retry-Key is required for this manual action')
        if job['profile'] != worker.profile:
            raise HTTPException(409, 'Parser profile changed; submit source again')
        key_hash = hashlib.sha256(x_parse_retry_key.encode()).hexdigest()
        try:
            retried, lease_id, reused = worker.store.retry(job_id, key_hash, identity[1])
            return public(retried) | {'lease_id': lease_id, 'reused': reused}
        except RetryOwnerMismatch:
            raise HTTPException(404, 'Retry request not found')
        except RetryConflict:
            raise HTTPException(409, 'Only failed or cancelled jobs may be explicitly retried')
        except QueueFull:
            raise HTTPException(429, '解析队列已满，请稍后重试。', headers={'Retry-After': '10'})
    return app
