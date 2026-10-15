"""Fetch one authorized native original, never a caller-supplied URL or path."""
import hashlib
import os
from pathlib import Path
import re
from urllib.parse import quote

from fastapi import HTTPException
import httpx


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


async def load_source(source_id, authorization, identity, max_upload):
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
    if (source.get("type") != "file" or str(source.get("file_type", "")).lower() != "xlsx"
            or not isinstance(filename, str) or not filename.lower().endswith(".xlsx")):
        raise HTTPException(400, "结构化解析目前支持 .xlsx；旧版 Excel 请先另存为 .xlsx。")
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


async def download_source(source, path, authorization, identity, max_upload):
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
    current = await load_source(source["source_knowledge_id"], authorization, identity, max_upload)
    if current != source:
        raise HTTPException(409, "原件所属知识库或文件已改变，请刷新后重试。")
