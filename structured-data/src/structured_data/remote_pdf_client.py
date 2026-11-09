"""Trusted remote-PDF queue bridge shared with native knowledge ingestion."""
from __future__ import annotations

import asyncio
import hashlib
import json
import re
from urllib.parse import quote

import httpx

from .pdf_parser_bridge import PDFParserError
from .workspaces import current_identity


class RemotePDFError(PDFParserError):
    def __init__(self, code):
        self.code = code if re.fullmatch(r'[a-zA-Z0-9_]{1,64}', str(code)) else 'failed'
        super().__init__('Remote PDF parser: ' + self.code)


async def parse_remote_pdf(path, source, cfg, progress_callback=None, *, retry_token=None):
    if retry_token is not None and (not isinstance(retry_token, str) or not re.fullmatch(r'[a-f0-9]{32}', retry_token)):
        raise RemotePDFError('invalid_retry_token')
    identity = current_identity.get()
    if identity is None:
        raise RemotePDFError('missing_scope')
    tenant = identity.knowledge_base_tenant_id if identity.knowledge_base_id else identity.tenant_id
    if not tenant or not cfg.api_key:
        raise RemotePDFError('missing_scope_or_configuration')
    headers = {'Authorization': 'Bearer ' + cfg.api_key,
               'X-Parse-Scope': 'tenant:' + str(tenant),
               'X-Parse-Owner': identity.user_id or 'tenant:' + str(tenant),
               'X-Parse-Queue-Timeout': str(cfg.queue_timeout_seconds),
               'X-Parse-Execution-Timeout': str(cfg.timeout_seconds)}
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        if stream.read(5) != b'%PDF-':
            raise RemotePDFError('invalid_pdf')
        stream.seek(0)
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    sha = digest.hexdigest()
    if source.get('sha256', sha) != sha:
        raise RemotePDFError('source_mismatch')
    job = None
    finished = False
    async with httpx.AsyncClient(base_url=cfg.url, headers=headers, trust_env=False,
                                 follow_redirects=False, timeout=60) as client:
        async def request(method, route, **kwargs):
            async with client.stream(method, route, **kwargs) as response:
                if not 200 <= response.status_code < 300:
                    raise RemotePDFError('http_' + str(response.status_code))
                raw = bytearray()
                async for chunk in response.aiter_bytes():
                    raw.extend(chunk)
                    if len(raw) > cfg.max_response_bytes:
                        raise RemotePDFError('oversize_result')
                try:
                    value = json.loads(raw)
                except (ValueError, UnicodeError):
                    raise RemotePDFError('invalid_response') from None
                if not isinstance(value, dict):
                    raise RemotePDFError('invalid_response')
                return value
        try:
            loop = asyncio.get_running_loop()
            admission_started = None
            async def admit(action):
                nonlocal admission_started
                waiting_capacity = False
                while True:
                    elapsed = loop.time() - admission_started if admission_started is not None else 0
                    remaining = cfg.queue_timeout_seconds - elapsed
                    if remaining <= 0:
                        raise RemotePDFError('client_queue_timeout')
                    try:
                        async with asyncio.timeout(remaining):
                            if waiting_capacity:
                                await request('GET', '/v1/capacity')
                            return await action()
                    except RemotePDFError as exc:
                        if exc.code != 'http_429':
                            raise
                        if admission_started is None:
                            admission_started = loop.time()
                        waiting_capacity = True
                        if progress_callback:
                            progress_callback({'queue_status': 'queued', 'progress': {'stage': 'waiting_for_admission'}})
                        remaining = cfg.queue_timeout_seconds - (loop.time() - admission_started)
                        try:
                            async with asyncio.timeout(max(0, remaining)):
                                await asyncio.sleep(min(cfg.poll_interval_seconds, 5))
                        except TimeoutError:
                            raise RemotePDFError('client_queue_timeout') from None
                    except TimeoutError:
                        raise RemotePDFError('client_queue_timeout') from None

            async def submit():
                with path.open('rb') as stream:
                    return await request('POST', '/v1/jobs', files={'file': ('document.pdf', stream, 'application/pdf')})
            job = await admit(submit)
            job_id = job.get('id')
            lease = job.get('lease_id', '')
            if not isinstance(job_id, str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,128}', job_id):
                raise RemotePDFError('invalid_job_id')
            if not isinstance(lease, str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,128}', lease):
                raise RemotePDFError('invalid_lease_id')
            route = '/v1/jobs/' + quote(job_id, safe='')
            if retry_token is not None and job.get('status') in {'failed', 'cancelled', 'canceled'}:
                # Explicit UI action only; normal imports and polling never restart terminal work.
                job = await admit(lambda: request('POST', route + '/retry', headers={'X-Parse-Retry-Key': retry_token}))
                if job.get('id') != job_id:
                    raise RemotePDFError('invalid_retry_job_id')
                lease = job.get('lease_id')
                if not isinstance(lease, str) or not re.fullmatch(r'[A-Za-z0-9_-]{1,128}', lease):
                    raise RemotePDFError('invalid_lease_id')
            loop = asyncio.get_running_loop()
            last = loop.time()
            queue_used = loop.time() - admission_started if admission_started is not None else 0.0
            execution_used = 0.0
            previous_status = job.get('status')
            timeout_code = 'client_timeout'
            while True:
                now = loop.time()
                if previous_status == 'queued':
                    queue_used += now - last
                elif previous_status == 'running':
                    execution_used += now - last
                last = now
                if progress_callback:
                    progress_callback({'remote_job_id': job_id, 'queue_status': job.get('status'),
                                       'progress': job.get('progress')})
                status = job.get('status')
                if status == 'completed':
                    break
                if status in {'failed', 'cancelled', 'canceled'}:
                    finished = True
                    error = job.get('error') or {}
                    raise RemotePDFError(error.get('code', status) if isinstance(error, dict) else status)
                if status not in {'queued', 'running'}:
                    raise RemotePDFError('invalid_status')
                remaining = (cfg.queue_timeout_seconds - queue_used if status == 'queued'
                             else cfg.timeout_seconds - execution_used)
                timeout_code = 'client_queue_timeout' if status == 'queued' else 'client_timeout'
                if remaining <= 0:
                    raise TimeoutError()
                previous_status = status
                async with asyncio.timeout(remaining):
                    await asyncio.sleep(min(cfg.poll_interval_seconds, 5))
                    job = await request('GET', route)
            artifact = await request('GET', route + '/result')
            artifact_source = artifact.get('source')
            if (artifact.get('schema_version') != 'remote_pdf_v1' or not isinstance(artifact_source, dict)
                    or artifact_source.get('sha256') != sha):
                raise RemotePDFError('source_mismatch')
            pages = artifact.get('pages', [])
            count = artifact_source.get('page_count')
            if (type(count) is not int or not isinstance(pages, list) or count != len(pages) or not pages
                    or any(not isinstance(p, dict) or type(p.get('page')) is not int for p in pages)
                    or [p['page'] for p in pages] != list(range(1, count + 1))):
                raise RemotePDFError('page_coverage_invalid')
            bundle = artifact.get('bundle')
            if (not isinstance(bundle, dict) or not isinstance(bundle.get('source'), dict)
                    or bundle['source'].get('sha256') != sha):
                raise RemotePDFError('source_mismatch')
            if (type(bundle['source'].get('page_count')) is not int or bundle['source']['page_count'] != count
                    or not isinstance(bundle.get('fragments'), list)
                    or any(not isinstance(f, dict) or type(f.get('page')) is not int or not 1 <= f['page'] <= count
                           for f in bundle['fragments'])):
                raise RemotePDFError('page_coverage_invalid')
            from .pdf_tables import _bundle
            bundle['source'].update({k: v for k, v in source.items() if k in {
                'knowledge_id', 'knowledge_base_id', 'file_name'}})
            bundle['source'].setdefault('file_name', path.name)
            _bundle(bundle)
            bundle['remote_job_id'] = job_id
            # Per-request latency must not enter the immutable extraction
            # bundle: cache hits for identical source/profile/output share a draft.
            finished = True
            return bundle
        except TimeoutError:
            raise RemotePDFError(locals().get('timeout_code', 'client_timeout')) from None
        finally:
            if job and not finished and 'route' in locals():
                # Detach this consumer; the service cancels only the last lease.
                try:
                    async with asyncio.timeout(5):
                        await client.delete(route, headers={'X-Parse-Lease': lease})
                except Exception:
                    pass
