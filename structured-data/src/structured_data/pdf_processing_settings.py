"""Workspace-admin PDF policy; immutable snapshots travel with admitted jobs."""
from contextlib import contextmanager
from contextvars import ContextVar
from dataclasses import asdict, dataclass
import os

from fastapi import HTTPException
import httpx


@dataclass(frozen=True)
class PDFProcessingSettings:
    overlap_pages: int = 1
    grouping_request_bytes: int = 24000
    queue_timeout_seconds: int = 86400
    execution_timeout_seconds: int = 7200

    def __post_init__(self):
        for name, low, high in (('overlap_pages', 1, 5), ('grouping_request_bytes', 12000, 96000),
                               ('queue_timeout_seconds', 3600, 86400), ('execution_timeout_seconds', 600, 7200)):
            value = getattr(self, name)
            if type(value) is not int or not low <= value <= high:
                raise ValueError('Invalid PDF processing policy: ' + name)

    def snapshot(self):
        return asdict(self)


current_settings = ContextVar('pdf_processing_settings', default=None)


@contextmanager
def use_settings(snapshot):
    settings = PDFProcessingSettings(**snapshot) if isinstance(snapshot, dict) else snapshot
    if not isinstance(settings, PDFProcessingSettings):
        raise ValueError('PDF processing snapshot is required')
    marker = current_settings.set(settings)
    try:
        yield settings
    finally:
        current_settings.reset(marker)


async def load_settings(authorization, identity):
    from .native_import import upstream_headers
    headers = upstream_headers(authorization, identity)
    if identity.knowledge_base_id:
        headers['X-Knowledge-Base-ID'] = identity.knowledge_base_id
    base = os.environ.get('WEKNORA_API_URL', 'http://127.0.0.1:18081').rstrip('/')
    try:
        async with httpx.AsyncClient(trust_env=False, follow_redirects=False, timeout=10) as client:
            response = await client.get(base + '/api/v1/tenants/kv/pdf-processing-settings', headers=headers)
        # Compatibility with the previous API during rolling upgrades only.
        if response.status_code == 404:
            return PDFProcessingSettings()
        if response.status_code != 200:
            raise HTTPException(503, '暂时无法读取PDF处理配置，请稍后重试')
        return PDFProcessingSettings(**response.json()['data'])
    except (httpx.HTTPError, ValueError, KeyError, TypeError):
        raise HTTPException(503, 'PDF处理配置无效或暂不可用，请联系管理员') from None
