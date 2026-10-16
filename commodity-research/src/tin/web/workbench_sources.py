"""Read an authorized original without requiring a semantic model."""
import hashlib
import base64
from urllib.parse import quote

import httpx
from fastapi import APIRouter, Depends, HTTPException

from tin.config import settings
from tin.integration.workbench_scope import WorkbenchScope, go_request, verified_workbench

router = APIRouter(prefix="/api/workbenches/{workbench_id}/sources")
MAX_TEXT_BYTES = 1024 * 1024
IMAGE_MIMES = {"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "webp": "image/webp", "gif": "image/gif", "bmp": "image/bmp"}


@router.get("/{knowledge_id}/content")
async def source_content(knowledge_id: str, scope: WorkbenchScope = Depends(verified_workbench)):
    doc = await go_request(scope, "/api/v1/knowledge/" + quote(knowledge_id, safe=""))
    source = doc.get("data")
    if not isinstance(source, dict) or source.get("id") != knowledge_id or source.get("knowledge_base_id") != scope.knowledge_base_id:
        raise HTTPException(403, "来源不属于当前工作台绑定知识库")
    kind = str(source.get("file_type", "")).lower().lstrip(".")
    filename = source.get("file_name") or source.get("title") or ""
    if kind not in {"txt", "md", "markdown", "json", "xml", "csv", "tsv"} and kind not in IMAGE_MIMES:
        return {"knowledge_id": knowledge_id, "filename": filename, "text": "", "supported": False,
                "notice": "此格式请查看原件或结构化字段；尚未生成正文预览"}
    declared_size = source.get("file_size")
    if type(declared_size) is int and declared_size > MAX_TEXT_BYTES:
        raise HTTPException(413, "正文原件超过 1 MiB，请从知识库查看原件")
    try:
        async with httpx.AsyncClient(timeout=30, follow_redirects=False, trust_env=False) as client:
            async with client.stream("GET", settings.weknora_api_url.rstrip("/") + "/api/v1/knowledge/"
                + quote(knowledge_id, safe="") + "/download", headers={"Authorization": scope.authorization,
                    "X-Tenant-ID": str(scope.identity.tenant_id)}) as response:
                if response.status_code != 200:
                    raise HTTPException(response.status_code if response.status_code in {401, 403, 404} else 503,
                                        "原件暂时不可读，未使用其他文件代替")
                chunks, size = [], 0
                async for chunk in response.aiter_bytes():
                    size += len(chunk)
                    if size > MAX_TEXT_BYTES:
                        raise HTTPException(413, "正文原件超过预览大小上限")
                    chunks.append(chunk)
        raw = b"".join(chunks)
    except httpx.HTTPError:
        raise HTTPException(503, "原件暂时不可读，请稍后重试") from None
    digest = source.get("file_hash", "")
    if digest and (not isinstance(digest, str) or len(digest) not in {32, 64}
                   or (hashlib.md5(raw).hexdigest() if len(digest) == 32 else hashlib.sha256(raw).hexdigest()) != digest.lower()):
        raise HTTPException(409, "原件与登记哈希不一致，未显示替代内容")
    if kind in IMAGE_MIMES:
        return {"knowledge_id": knowledge_id, "filename": filename, "text": "", "supported": True,
                "image_data_url": "data:" + IMAGE_MIMES[kind] + ";base64," + base64.b64encode(raw).decode("ascii"),
                "sha256": hashlib.sha256(raw).hexdigest(), "truncated": False,
                "notice": "原始图片预览，未进行识别或生成研究结论"}
    try:
        text = raw.decode("utf-8-sig")
        encoding = "UTF-8"
    except UnicodeDecodeError:
        try:
            text = raw.decode("gb18030")
            encoding = "GB18030"
        except UnicodeDecodeError:
            raise HTTPException(422, "无法确认原件文字编码，请从知识库查看原件") from None
    return {"knowledge_id": knowledge_id, "filename": filename, "text": text, "encoding": encoding,
            "sha256": hashlib.sha256(raw).hexdigest(), "supported": True, "truncated": False,
            "notice": "原件文字，未作为指令执行；观点和计划需人工核对后建立研究记录"}
