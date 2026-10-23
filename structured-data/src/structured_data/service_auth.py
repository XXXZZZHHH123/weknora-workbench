"""Bounded, path-bound calls from verified WeKnora and research services."""
import base64
import hashlib
import hmac
import json
import os
from pathlib import Path
import time
from urllib.parse import quote
from uuid import UUID

import httpx

from fastapi import HTTPException
from .workspaces import Identity, valid_knowledge_base_id


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
        required = {"user_id", "email", "display_name", "tenant_id", "role"}
        scope_fields = {"knowledge_base_id", "knowledge_base_tenant_id"}
        optional = scope_fields | {'workbench_id', 'authorization_sha256', 'principal_type'}
        if not isinstance(data, dict) or not required <= set(data) or set(data) - required - optional:
            raise ValueError("invalid identity")
        if (type(data["tenant_id"]) is not int or data["tenant_id"] <= 0
                or not isinstance(data["user_id"], str) or not data["user_id"].strip()
                or len(data["user_id"]) > 128
                or data["role"] not in {"owner", "admin", "contributor", "viewer"}
                or any(not isinstance(data[k], str) for k in ("email", "display_name"))):
            raise ValueError("invalid identity")
        if scope_fields <= set(data) and (not valid_knowledge_base_id(data['knowledge_base_id'])
                or type(data['knowledge_base_tenant_id']) is not int or data['knowledge_base_tenant_id'] <= 0):
            raise ValueError("invalid knowledge-base scope")
        requested = request.headers.get('x-knowledge-base-id')
        if requested is not None and requested != data.get('knowledge_base_id'):
            raise ValueError("unsigned knowledge-base scope")
        scoped = bool(scope_fields & set(data))
        if scoped and not scope_fields <= set(data):
            raise ValueError('incomplete knowledge-base scope')
        if data.get('workbench_id') and not scoped:
            raise ValueError('missing workbench source scope')
        principal = data.pop('principal_type', None)
        authorization_hash = data.pop('authorization_sha256', None)
        if principal is not None and (principal != 'api_key' or not scoped or data.get('workbench_id')):
            raise ValueError('invalid machine grant')
        authorization = request.headers.get('authorization', '')
        if scoped and principal != 'api_key':
            if (not authorization.startswith('Bearer ') or len(authorization) > 16391
                    or not isinstance(authorization_hash, str)
                    or not hmac.compare_digest(authorization_hash, hashlib.sha256(authorization.encode()).hexdigest())):
                raise ValueError('missing or unsigned current authorization')
        elif authorization_hash is not None:
            if (principal != 'api_key' or not isinstance(authorization_hash, str)
                    or not hmac.compare_digest(authorization_hash, hashlib.sha256(authorization.encode()).hexdigest())):
                raise ValueError('unexpected authorization binding')
        identity = Identity(**data)
        if identity.workbench_id:
            await verify_research_projection(request, identity)
        elif scoped and principal != 'api_key':
            await verify_direct_knowledge(identity, authorization)
        if scoped and principal != 'api_key':
            request.state.verified_authorization = authorization
        return identity
    except (KeyError, ValueError, TypeError, OSError):
        raise HTTPException(401, "结构化查询未通过空间身份核验") from None


async def verify_direct_knowledge(identity, authorization):
    """Native signed human reads retain current per-resource ACL enforcement."""
    try:
        base = os.environ.get('WEKNORA_API_URL', 'http://127.0.0.1:18081').rstrip('/')
        async with httpx.AsyncClient(trust_env=False, timeout=10, follow_redirects=False) as client:
            response = await client.get(base + '/api/v1/knowledge-bases/'
                + quote(identity.knowledge_base_id, safe=''),
                headers={'Authorization': authorization, 'X-Tenant-ID': str(identity.tenant_id)})
        if response.status_code in {401, 403, 404}:
            raise HTTPException(response.status_code, '知识库读取授权已失效')
        response.raise_for_status()
        document = response.json()
        kb = document.get('data')
        if (document.get('success') is not True or not isinstance(kb, dict)
                or kb.get('id') != identity.knowledge_base_id
                or type(kb.get('tenant_id')) is not int
                or kb['tenant_id'] != identity.knowledge_base_tenant_id
                or kb.get('my_permission') not in {'viewer', 'editor', 'admin'}):
            raise HTTPException(403, '知识库读取权限信息无效')
    except HTTPException:
        raise
    except (ValueError, TypeError, AttributeError):
        raise HTTPException(403, '知识库读取权限核验失败') from None
    except httpx.HTTPError:
        raise HTTPException(503, '暂时无法核验知识库读取权限') from None


async def verify_research_projection(request, identity):
    """A signed service caller still needs current, source-owner-approved access."""
    try:
        identifier = UUID(identity.workbench_id)
        if str(identifier) != identity.workbench_id or identifier.int == 0:
            raise ValueError('invalid workbench')
        authorization = request.headers.get('authorization', '')
        if not authorization.startswith('Bearer '):
            raise HTTPException(401, '研究工作台读取缺少当前登录核验')
        base = os.environ.get('WEKNORA_API_URL', 'http://127.0.0.1:18081').rstrip('/')
        async with httpx.AsyncClient(trust_env=False, timeout=10, follow_redirects=False) as client:
            response = await client.get(base + '/api/v1/workspaces/research/'
                + quote(identity.workbench_id, safe='') + '/source-scope',
                headers={'Authorization': authorization, 'X-Tenant-ID': str(identity.tenant_id)})
        if response.status_code in {401, 403, 404}:
            raise HTTPException(response.status_code, '研究数据共享授权已失效')
        response.raise_for_status()
        document = response.json()
        grant = document.get('data')
        if (document.get('success') is not True or not isinstance(grant, dict)
                or grant.get('id') != identity.workbench_id
                or grant.get('user_id') != identity.user_id
                or type(grant.get('scope_tenant_id')) is not int
                or grant['scope_tenant_id'] != identity.tenant_id
                or not isinstance(grant.get('sources'), list)):
            raise ValueError('invalid research grant')
        source = next((item for item in grant['sources'] if isinstance(item, dict)
                       and item.get('knowledge_base_id') == identity.knowledge_base_id), None)
        if (source is None or type(source.get('tenant_id')) is not int
                or source['tenant_id'] != identity.knowledge_base_tenant_id
                or source.get('permission') not in {'viewer', 'editor', 'admin'}
                or type(source.get('delegated')) is not bool
                or not isinstance(source.get('knowledge'), list)):
            raise ValueError('invalid source scope')
        if source['delegated'] and request.url.path != '/api/research/facts':
            raise HTTPException(403, '研究数据共享仅允许已发布指标，不能读取原件或知识库全文')
        if source['delegated']:
            modules = grant.get('view_modules')
            allowed = {'home', 'market', 'indicators', 'judgments', 'strategies', 'diagram', 'reports'}
            if (not isinstance(modules, list) or not 1 <= len(modules) <= len(allowed)
                    or any(not isinstance(module, str) or module not in allowed for module in modules)
                    or len(set(modules)) != len(modules)):
                raise HTTPException(403, '研究台缺少当前有效的页面共享授权')
            body = await request.json()
            from .research_facts import _bounded_parameters
            try:
                if (not isinstance(body, dict) or not isinstance(body.get('view_module'), str)
                        or body['view_module'] not in modules or _bounded_parameters(body) is None):
                    raise ValueError('missing bounded page read')
            except (ValueError, TypeError):
                raise HTTPException(403, '研究台仅允许已授权页面的必要指标，不能读取全量目录或历史') from None
            request.state.research_view_module = body['view_module']
        request.state.research_source_manifest = source['knowledge']
    except HTTPException:
        raise
    except (ValueError, TypeError, KeyError, AttributeError):
        raise HTTPException(403, '研究工作台来源核验失败') from None
    except httpx.HTTPError:
        raise HTTPException(503, '暂时无法核验研究工作台来源授权') from None
