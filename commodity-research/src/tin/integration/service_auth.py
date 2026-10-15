"""Authenticate read-only calls signed by the WeKnora server's verified caller."""
import base64
import hashlib
import hmac
import json
import time
from pathlib import Path

from fastapi import HTTPException

from tin.config import settings
from tin.integration.identity import Identity


async def verify_service_request(request):
    try:
        timestamp = request.headers["x-research-timestamp"]
        if len(timestamp)>20 or not timestamp.isdecimal() or abs(int(time.time()) - int(timestamp)) > 60:
            raise ValueError("expired")
        encoded = request.headers["x-research-identity"]
        if len(encoded) > 4096:
            raise ValueError("identity too large")
        length = request.headers.get("content-length")
        if length is not None and int(length) > 16384:
            raise HTTPException(413, "研究读取参数超过大小上限")
        # This endpoint shares a proxy with large Excel uploads. Bound both
        # declared and streamed bodies before allocating or decoding them.
        chunks, size = [], 0
        async for chunk in request.stream():
            size += len(chunk)
            if size > 16384:
                raise HTTPException(413, "研究读取参数超过大小上限")
            chunks.append(chunk)
        body = b"".join(chunks)
        # Starlette's cached request replays this bounded body to the route.
        request._body = body
        key = Path(settings.service_token_file).read_bytes().strip()
        if len(key) < 32:
            raise ValueError("missing service key")
        message = "\n".join((timestamp, request.method, "/api/tools/read", encoded,
                              hashlib.sha256(body).hexdigest())).encode()
        expected = hmac.new(key, message, hashlib.sha256).hexdigest()
        if not hmac.compare_digest(expected, request.headers.get("x-research-signature", "")):
            raise ValueError("invalid signature")
        data = json.loads(base64.urlsafe_b64decode(encoded + "=" * (-len(encoded) % 4)))
        required = {"user_id", "email", "display_name", "tenant_id", "role"}
        scoped = {"workbench_id", "knowledge_base_id", "knowledge_base_tenant_id"}
        if not isinstance(data, dict) or set(data) not in (required, required | scoped):
            raise ValueError("invalid identity")
        if scoped <= set(data):
            from uuid import UUID
            workbench = UUID(data["workbench_id"])
            knowledge_base = UUID(data["knowledge_base_id"])
            if (str(workbench) != data["workbench_id"] or workbench.int == 0
                    or str(knowledge_base) != data["knowledge_base_id"] or knowledge_base.int == 0
                    or type(data["knowledge_base_tenant_id"]) is not int or data["knowledge_base_tenant_id"] <= 0):
                raise ValueError("invalid workbench scope")
        if (not isinstance(data["tenant_id"], int) or isinstance(data["tenant_id"], bool)
                or data["tenant_id"] <= 0 or not isinstance(data["user_id"], str)
                or not data["user_id"].strip() or len(data["user_id"]) > 128
                or data["role"] not in {"owner", "admin", "contributor", "viewer"}
                or any(not isinstance(data[k], str) for k in ("email", "display_name"))):
            raise ValueError("invalid identity")
        return Identity(**data)
    except (ValueError, KeyError, OSError, TypeError, AttributeError):
        raise HTTPException(401, "研究读取请求未通过服务身份校验") from None
