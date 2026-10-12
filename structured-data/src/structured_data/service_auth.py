"""Bounded, path-bound calls from verified WeKnora and research services."""
import base64
import hashlib
import hmac
import json
import os
from pathlib import Path
import time

from fastapi import HTTPException
from .workspaces import Identity


async def verify_service(request):
    try:
        timestamp = request.headers["x-research-timestamp"]
        if len(timestamp)>20 or not timestamp.isdecimal() or abs(int(time.time()) - int(timestamp)) > 60:
            raise ValueError("expired")
        encoded = request.headers["x-research-identity"]
        if len(encoded) > 4096:
            raise ValueError("oversized identity")
        if int(request.headers.get("content-length", "0")) > 16384:
            raise HTTPException(413, "查询参数超过大小上限")
        chunks, size = [], 0
        async for chunk in request.stream():
            size += len(chunk)
            if size > 16384:
                raise HTTPException(413, "查询参数超过大小上限")
            chunks.append(chunk)
        body = b"".join(chunks)
        request._body = body
        key = Path(os.environ["STRUCTURED_SERVICE_TOKEN_FILE"]).read_bytes().strip()
        if len(key) < 32:
            raise ValueError("missing key")
        message = "\n".join((timestamp, request.method, request.url.path, encoded,
                              hashlib.sha256(body).hexdigest())).encode()
        signature = hmac.new(key, message, hashlib.sha256).hexdigest()
        if not hmac.compare_digest(signature, request.headers.get("x-research-signature", "")):
            raise ValueError("invalid signature")
        data = json.loads(base64.urlsafe_b64decode(encoded + "=" * (-len(encoded) % 4)))
        if set(data) != {"user_id", "email", "display_name", "tenant_id", "role"}:
            raise ValueError("invalid identity")
        if (type(data["tenant_id"]) is not int or data["tenant_id"] <= 0
                or not isinstance(data["user_id"], str) or not data["user_id"].strip()
                or len(data["user_id"]) > 128
                or data["role"] not in {"owner", "admin", "contributor", "viewer"}
                or any(not isinstance(data[k], str) for k in ("email", "display_name"))):
            raise ValueError("invalid identity")
        return Identity(**data)
    except (KeyError, ValueError, TypeError, OSError):
        raise HTTPException(401, "结构化查询未通过空间身份核验") from None
