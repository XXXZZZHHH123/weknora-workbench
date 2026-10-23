"""Fetch one authorized native original, never a caller-supplied URL or path."""
import hashlib
import os
from pathlib import Path
import re
from urllib.parse import quote

from fastapi import HTTPException
import httpx

from .workspaces import valid_knowledge_base_id


def upstream_headers(authorization, identity):
    if not authorization.startswith("Bearer "):
        raise HTTPException(401, "请通过当前 WeKnora 登录状态打开原件。")
    return {"Authorization": authorization, "X-Tenant-ID": str(identity.tenant_id)}


def native_limit(max_upload):
    try:
        native_mb = int(os.environ.get("MAX_FILE_SIZE_MB", "50"))
        if native_mb <= 0:
            raise ValueError
    except ValueError:
        native_mb = 50
    return min(max_upload, native_mb * 1024 * 1024)


def check_response(response):
    if response.status_code == 401:
        raise HTTPException(401, "WeKnora 登录已过期，请重新登录。")
    if response.status_code == 403:
        raise HTTPException(403, "没有读取该知识库原件的权限。")
    if response.status_code == 404:
        raise HTTPException(404, "原件不存在或已删除，请刷新知识库目录。")
    if response.status_code != 200:
        raise HTTPException(502, "无法读取知识库原件，请稍后重试。")


async def load_source(source_id, authorization, identity, max_upload, *, allowed_types=('xlsx',)):
    if not identity.knowledge_base_id:
        raise HTTPException(400, "请从原件所属知识库打开结构化解析。")
    base = os.environ.get("WEKNORA_API_URL", "http://127.0.0.1:18081").rstrip("/")
    try:
        async with httpx.AsyncClient(trust_env=False, timeout=15, follow_redirects=False) as client:
            response = await client.get(base + "/api/v1/knowledge/" + quote(source_id, safe=""),
                                        headers=upstream_headers(authorization, identity))
        check_response(response)
        payload = response.json()
        if not isinstance(payload, dict):
            raise ValueError('invalid knowledge response')
        source = payload.get("data", payload)
    except (httpx.HTTPError, ValueError):
        raise HTTPException(502, "无法核验知识库原件，请稍后重试。") from None
    if (not isinstance(source, dict) or payload.get("success") is False
            or source.get("id") != source_id
            or source.get("knowledge_base_id") != identity.knowledge_base_id
            or source.get("tenant_id") != identity.knowledge_base_tenant_id):
        raise HTTPException(403, "原件不属于当前知识库。")
    if source.get("parse_status") == "deleting" or source.get("deleted_at"):
        raise HTTPException(409, "原件正在删除，请刷新知识库目录。")
    filename = source.get("file_name")
    file_type = str(source.get("file_type", "")).lower()
    if (source.get("type") != "file" or file_type not in allowed_types
            or not isinstance(filename, str) or not filename.lower().endswith('.' + file_type)):
        raise HTTPException(400, "原件类型与当前导入入口不匹配。")
    if filename != Path(filename.replace("\\", "/")).name or any(ord(char) < 32 for char in filename):
        raise HTTPException(400, "原件文件名无效。")
    size, fingerprint = source.get("file_size"), source.get("file_hash")
    if type(size) is not int or size <= 0:
        raise HTTPException(400, "原件大小无效。")
    if size > native_limit(max_upload):
        raise HTTPException(413, "原件超过允许的文件大小。")
    if not isinstance(fingerprint, str) or not re.fullmatch(r"[0-9a-fA-F]{32}", fingerprint):
        raise HTTPException(400, "原件指纹无效，请重新上传文件。")
    return {"source_knowledge_id": source_id, "file_name": filename, "file_size": size,
            "file_hash": fingerprint.lower(), "knowledge_base_id": identity.knowledge_base_id,
            "tenant_id": identity.knowledge_base_tenant_id}


def original_fingerprint(path, max_upload, expected_sha256=None):
    """Hash saved bytes in bounded chunks before publishing a source identity."""
    if not path.is_file() or path.stat().st_size <= 0:
        raise HTTPException(409, "结构化原件不存在，请重新选择文件。")
    if path.stat().st_size > native_limit(max_upload):
        raise HTTPException(413, "原件超过知识库允许的文件大小。")
    md5, sha256, size = hashlib.md5(), hashlib.sha256(), 0
    with path.open('rb') as original:
        for chunk in iter(lambda: original.read(1024 * 1024), b''):
            size += len(chunk)
            if size > native_limit(max_upload):
                raise HTTPException(413, "原件超过知识库允许的文件大小。")
            md5.update(chunk)
            sha256.update(chunk)
    if expected_sha256 is not None and sha256.hexdigest() != expected_sha256:
        raise HTTPException(409, "已保存的结构化原件发生改变，未登记到知识库。")
    return size, md5.hexdigest()


async def register_source(path, authorization, identity, max_upload, *, expected_sha256=None):
    """Store an original through the native ACL, storage and duplicate checks."""
    from starlette.concurrency import run_in_threadpool
    if not identity.knowledge_base_id:
        raise HTTPException(400, "请在原件所属知识库中登记文件。")
    if not identity.can_write:
        raise HTTPException(403, "没有编辑当前知识库的权限。")
    if not path.name.lower().endswith('.xlsx'):
        raise HTTPException(400, "结构化原件登记目前支持 .xlsx。")
    size, digest = await run_in_threadpool(original_fingerprint, path, max_upload, expected_sha256)
    base = os.environ.get("WEKNORA_API_URL", "http://127.0.0.1:18081").rstrip("/")
    endpoint = base + '/api/v1/knowledge-bases/' + quote(identity.knowledge_base_id, safe='') + '/knowledge/file'
    try:
        async with httpx.AsyncClient(trust_env=False, timeout=httpx.Timeout(120, connect=10),
                                     follow_redirects=False) as client:
            with path.open('rb') as original:
                response = await client.post(endpoint, headers=upstream_headers(authorization, identity),
                    files={'file': (path.name, original,
                        'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet')},
                    data={'process_config': '{"source_only":true}'})
        if response.status_code == 413:
            raise HTTPException(413, '原件超过知识库允许的文件大小。')
        if response.status_code != 409:
            check_response(response)
        payload = response.json()
        # Native upload reports an existing original as a strict conflict
        # receipt. Its ID still needs the same independent ACL/hash re-read.
        duplicate = (response.status_code == 409 and isinstance(payload, dict)
                     and payload.get('success') is False and payload.get('code') == 'duplicate_file'
                     and isinstance(payload.get('data'), dict))
        if (not isinstance(payload, dict) or (response.status_code == 409 and not duplicate)
                or (not duplicate and payload.get('success') is False)):
            raise ValueError('invalid upload receipt')
        document = payload.get('data', payload)
        source_id = document.get('id') if isinstance(document, dict) else None
        if not valid_knowledge_base_id(source_id):
            raise ValueError('invalid source identity')
    except (httpx.HTTPError, ValueError):
        raise HTTPException(502, 'Excel 原件未能保存到知识库，未开始结构化导入，请重试。') from None
    source = await load_source(source_id, authorization, identity, max_upload)
    if source['file_size'] != size or source['file_hash'] != digest:
        raise HTTPException(409, '知识库原件与上传内容不一致，未关联结构化数据。')
    return source


async def download_source(source, path, authorization, identity, max_upload, *, allowed_types=('xlsx',)):
    base = os.environ.get("WEKNORA_API_URL", "http://127.0.0.1:18081").rstrip("/")
    endpoint = base + "/api/v1/knowledge/" + quote(source["source_knowledge_id"], safe="") + "/download"
    size, digest = 0, hashlib.md5()
    try:
        async with httpx.AsyncClient(trust_env=False, timeout=60, follow_redirects=False) as client:
            async with client.stream("GET", endpoint, headers=upstream_headers(authorization, identity)) as response:
                check_response(response)
                with path.open("wb") as original:
                    async for chunk in response.aiter_bytes(1024 * 1024):
                        size += len(chunk)
                        if size > native_limit(max_upload) or size > source["file_size"]:
                            raise HTTPException(413, "原件超过允许的大小或在读取期间改变。")
                        digest.update(chunk)
                        original.write(chunk)
    except httpx.HTTPError:
        raise HTTPException(502, "原件传输失败，请稍后重试。") from None
    if size != source["file_size"] or digest.hexdigest() != source["file_hash"]:
        raise HTTPException(409, "原件在读取期间改变或校验失败，请刷新后重试。")
    current = await load_source(source["source_knowledge_id"], authorization, identity, max_upload, allowed_types=allowed_types)
    if current != source:
        raise HTTPException(409, "原件所属知识库或文件已改变，请刷新后重试。")
