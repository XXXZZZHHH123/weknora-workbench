"""Server-verified sessions for the embedded research service.

The bearer never appears in a URL or client-readable cookie. Every browser request
uses a short cached /auth/me verification; tenant and role come from that response.
"""
import base64
import json
import secrets
import time
from dataclasses import dataclass, replace

import httpx
from fastapi import HTTPException, Request
from fastapi.responses import HTMLResponse, JSONResponse
from starlette.middleware.base import BaseHTTPMiddleware

from tin.config import settings
from tin.integration.identity import Identity, current_identity, research_prefix, research_static_prefix

COOKIE = "research_session"


@dataclass
class ResearchSession:
    token: str
    identity: Identity
    expires: float
    verified: float


_sessions: dict[str, ResearchSession] = {}


def _token_expiry(token):
    try:
        segment = token.split(".")[1]
        payload = json.loads(base64.urlsafe_b64decode(segment + "=" * (-len(segment) % 4)))
        return float(payload["exp"])
    except (ValueError, KeyError, IndexError, TypeError):
        # Signature and validity are checked by WeKnora, never by this parser.
        return time.time() + settings.session_seconds


async def verify_bearer(token: str, requested_tenant: str | int | None = None) -> Identity:
    if not token or len(token) > 16384 or not settings.weknora_api_url:
        raise HTTPException(401, "请先登录 WeKnora")
    headers = {"Authorization": f"Bearer {token}"}
    if requested_tenant is not None:
        try:
            tenant_id = int(requested_tenant)
            if tenant_id <= 0:
                raise ValueError
        except (TypeError, ValueError):
            raise HTTPException(403, "无效空间") from None
        headers["X-Tenant-ID"] = str(tenant_id)
    try:
        async with httpx.AsyncClient(timeout=10, follow_redirects=False, trust_env=False) as client:
            response = await client.get(settings.weknora_api_url.rstrip("/") + "/api/v1/auth/me", headers=headers)
        if response.status_code in (401, 403):
            raise HTTPException(response.status_code, "登录已失效或没有空间权限")
        response.raise_for_status()
        result = response.json()
        data = result["data"]
        if result.get("success") is not True or data.get("tenant_required"):
            raise HTTPException(403, "请先选择有权限的空间")
        user, tenant = data["user"], data["tenant"]
        if user.get("is_active") is False:
            raise HTTPException(401, "账号已停用")
        active_id = int(tenant["id"])
        if active_id <= 0 or (requested_tenant is not None and active_id != int(requested_tenant)):
            raise HTTPException(403, "空间权限不匹配")
        membership = next((m for m in data.get("memberships", []) if int(m["tenant_id"]) == active_id), None)
        role = membership.get("role") if membership else None
        if role not in {"owner", "admin", "contributor", "viewer"} or not user.get("id") or not user.get("email"):
            raise HTTPException(403, "没有已确认的空间成员权限")
        return Identity(str(user["id"]), str(user["email"]), str(user.get("username") or user["email"]), active_id, role)
    except HTTPException:
        raise
    except (httpx.HTTPError, ValueError, KeyError, TypeError):
        raise HTTPException(503, "暂时无法核验 WeKnora 登录，请稍后重试") from None


def _same_origin(request):
    origin = request.headers.get("origin")
    # Origin is mandatory for cookie-authenticated writes and token exchanges.
    # Proxy must preserve the public Host; http/https accounts for TLS termination.
    from urllib.parse import urlsplit
    try:
        parsed = urlsplit(origin or "")
        return (parsed.scheme == request.headers.get("x-forwarded-proto", request.url.scheme)
                and parsed.netloc == request.headers.get("host"))
    except ValueError:
        return False


def bootstrap_html(source=None, indicator=None, workbench_id=None, view=None):
    prefix = json.dumps(research_prefix())
    import re
    target = research_static_prefix() + '/'
    if workbench_id is not None:
        from tin.integration.workbench_scope import canonical_workbench_id
        canonical_workbench_id(workbench_id)
        views = {'home': '/', 'diagram': '/sn/diagram', 'indicators': '/sn/indicators', 'judgment': '/sn/judgment', 'strategies': '/sn/strategies', 'reports': '/sn/reports'}
        if view is not None and view not in views:
            raise HTTPException(400, '研究工作台视图无效')
        target = research_static_prefix() + '/workbenches/' + workbench_id + views.get(view, '/')
    if workbench_id is None and isinstance(source,str) and re.fullmatch('[a-f0-9]{64}',source):
        target = research_prefix() + '/files?source=' + source
    elif workbench_id is None and isinstance(indicator, str) and indicator and len(indicator) <= 128 and not any(ord(c) < 32 for c in indicator):
        from urllib.parse import quote
        target = research_prefix() + '/sn/indicators/' + quote(indicator, safe='')
    return HTMLResponse('''<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>商品研究</title>
<p id="status">正在连接研究工作台…</p><script>
const prefix = PREFIX;
let connecting = false;
window.addEventListener('message', async event => {
  if (event.origin !== location.origin || event.source !== parent || event.data?.type !== 'research-auth' || connecting) return;
  const expectedWorkbench = WORKBENCH;
  if (expectedWorkbench !== null && event.data.workbenchId !== expectedWorkbench) return;
  const {token} = event.data;
  const tenant = event.data.tenantId ?? event.data.tenant_id;
  if (typeof token !== 'string' || !tenant) return;
  connecting = true;
  try {
    const response = await fetch(prefix + '/auth/session', {method:'POST', credentials:'same-origin',
      headers:{'Authorization':'Bearer ' + token,'X-Tenant-ID':String(tenant)}});
    if (!response.ok) throw new Error('登录或空间权限核验失败，请重新登录后重试。');
    location.replace(TARGET);
  } catch (error) { document.getElementById('status').textContent = error.message; connecting = false; }
});
parent.postMessage({type:'research-ready'}, location.origin);
</script></html>'''.replace('PREFIX', prefix).replace('TARGET', json.dumps(target)).replace('WORKBENCH', json.dumps(workbench_id)), headers={"Cache-Control": "no-store", "Referrer-Policy": "no-referrer"})


class ResearchAuthMiddleware(BaseHTTPMiddleware):
    async def dispatch(self, request, call_next):
        if not settings.weknora_api_url:
            return await call_next(request)
        prefix = research_static_prefix()
        # Supports direct service requests in tests as well as prefix-stripping proxies.
        path = request.scope["path"]
        if prefix and (path == prefix or path.startswith(prefix + "/")):
            path = path[len(prefix):] or "/"
        elif prefix:
            request.scope["path"] = prefix + path
            request.scope["raw_path"] = prefix.encode() + request.scope.get("raw_path", path.encode())
        # ASGI path includes root_path. Starlette's router subtracts it when
        # matching; mounted StaticFiles subtracts the cumulative /research/static
        # root again. A stripped scope.path with a prefixed root_path breaks that
        # second subtraction and looks up static/static/app.css.
        request.scope["root_path"] = prefix
        if path == "/healthz":
            return JSONResponse({"status": "ok", "mode": "weknora"})
        if path == "/auth/bootstrap" and request.method == "GET":
            try:
                return bootstrap_html(request.query_params.get('source'), request.query_params.get('indicator'), request.query_params.get('workbench_id'), request.query_params.get('view'))
            except HTTPException as exc:
                return JSONResponse({'detail': exc.detail}, status_code=exc.status_code)
        if path.startswith("/static/") and request.method == "GET":
            return await call_next(request)
        if path.startswith("/fonts/") and request.method in {"GET", "HEAD"}:
            return await call_next(request)
        try:
            from tin.integration.workbench_paths import parse_workbench_path, allowed_workbench_route, source_preparation_required
            try:
                scoped_path = parse_workbench_path(path)
            except ValueError:
                raise HTTPException(404, "研究工作台路径无效") from None
            route_path = scoped_path[1] if scoped_path else path
            authorization = request.headers.get("authorization", "")
            if path == "/api/tools/read":
                from tin.integration.service_auth import verify_service_request
                identity = await verify_service_request(request)
            elif (path.startswith("/api/library") or path.startswith("/api/workbenches/")
                  or scoped_path is not None
                  or (path == "/api/sn/indicators/catalog" and request.method == "GET")) and request.headers.get("authorization", "").startswith("Bearer "):
                if request.method not in {"GET", "HEAD", "OPTIONS"} and not _same_origin(request):
                    raise HTTPException(403, "请求来源不匹配")
                identity = await verify_bearer(request.headers["authorization"][7:], request.headers.get("x-tenant-id"))
                if path.startswith("/api/workbenches/") and request.method not in {"GET", "HEAD", "OPTIONS"} and not identity.can_write:
                    raise HTTPException(403, "当前空间角色只有读取权限")
                expected_user = request.headers.get("x-research-user")
                if expected_user and expected_user != identity.user_id:
                    raise HTTPException(403, "当前页面的账号已改变，请重新打开资料中心")
            elif path == "/auth/session" and request.method == "POST":
                if not _same_origin(request):
                    raise HTTPException(403, "请求来源不匹配")
                header = request.headers.get("authorization", "")
                if not header.startswith("Bearer "):
                    raise HTTPException(401, "缺少登录凭据")
                token = header[7:]
                identity = await verify_bearer(token, request.headers.get("x-tenant-id"))
                now = time.time()
                for stale in [key for key, value in _sessions.items() if value.expires <= now]:
                    _sessions.pop(stale, None)
                session_id = secrets.token_urlsafe(32)
                expires = min(now + settings.session_seconds, _token_expiry(token))
                if expires <= now:
                    raise HTTPException(401, "登录已过期")
                _sessions[session_id] = ResearchSession(token, identity, expires, now)
                response = JSONResponse({"success": True, "tenant_id": identity.tenant_id})
                response.set_cookie(COOKIE, session_id, httponly=True, samesite="strict", path=prefix + "/",
                                    secure=request.url.scheme == "https" or request.headers.get("x-forwarded-proto") == "https",
                                    max_age=int(expires - now))
                response.headers["Cache-Control"] = "no-store"
                return response
            else:
                session = _sessions.get(request.cookies.get(COOKIE, ""))
                if not session or session.expires <= time.time():
                    raise HTTPException(401, "请通过 WeKnora 商品研究入口重新登录")
                if time.time() - session.verified >= max(0, min(settings.auth_cache_seconds, 30)):
                    refreshed = await verify_bearer(session.token, session.identity.tenant_id)
                    if refreshed.user_id != session.identity.user_id:
                        raise HTTPException(401, "登录身份已改变")
                    session.identity, session.verified = refreshed, time.time()
                identity = session.identity
                authorization = "Bearer " + session.token
                # Browser headers cannot select a different database or spoof identity.
                requested = request.headers.get("x-tenant-id")
                expected_tenant = request.query_params.get("_research_tenant")
                expected_user = request.headers.get("x-research-user") or request.query_params.get("_research_user")
                if ((requested is not None and requested != str(identity.tenant_id))
                        or (expected_tenant is not None and expected_tenant != str(identity.tenant_id))
                        or (expected_user is not None and expected_user != identity.user_id)):
                    raise HTTPException(403, "当前页面的账号或空间已改变，请重新打开商品研究")
                if request.method not in {"GET", "HEAD", "OPTIONS"}:
                    read_posts = {"/api/sn/diagram/values", "/api/sn/diagram/markdown/dump"}
                    if not identity.can_write and route_path not in read_posts and not path.startswith("/api/library"):
                        raise HTTPException(403, "当前空间角色只有读取权限")
                    if not _same_origin(request):
                        raise HTTPException(403, "请求来源不匹配")
            scope_marker = None
            if path == '/api/tools/read':
                if not identity.workbench_id or not identity.authorization:
                    raise HTTPException(409, '请先选择绑定知识库的研究工作台，未读取旧工作台事实')
                fresh = await verify_bearer(identity.authorization[7:], identity.tenant_id)
                if fresh.user_id != identity.user_id:
                    raise HTTPException(403, '研究服务签名账号与当前登录不一致')
                from tin.integration.workbench_scope import resolve_workbench, current_workbench
                scope = await resolve_workbench(fresh, identity.workbench_id, identity.authorization)
                if scope.restricted:
                    raise HTTPException(403, '受限研究台不开放通用查询和来源浏览')
                if (scope.knowledge_base_id != identity.knowledge_base_id
                        or scope.knowledge_base_tenant_id != identity.knowledge_base_tenant_id):
                    raise HTTPException(403, '工作台知识库绑定已改变')
                identity = replace(fresh, workbench_id=identity.workbench_id,
                    knowledge_base_id=scope.knowledge_base_id,
                    knowledge_base_tenant_id=scope.knowledge_base_tenant_id,
                    authorization=identity.authorization)
                scope = replace(scope, identity=identity)
                scope_marker = current_workbench.set(scope)
            if scoped_path is not None:
                from tin.integration.workbench_scope import resolve_workbench, current_workbench
                scope = await resolve_workbench(identity, scoped_path[0], authorization)
                if not allowed_workbench_route(route_path, request.method):
                    raise HTTPException(404, "此入口不属于当前研究工作台")
                if source_preparation_required(route_path) or (route_path == '/sn/entry' and request.method == 'POST'):
                    raise HTTPException(409, "请在绑定知识库准备资料，并从本工作台的资料与来源入口核对后导入")
                if request.method not in {"GET", "HEAD", "OPTIONS"}:
                    if not identity.can_write and route_path not in {"/api/sn/diagram/values", "/api/sn/diagram/markdown/dump"}:
                        raise HTTPException(403, "当前空间角色只有读取权限")
                    if not _same_origin(request):
                        raise HTTPException(403, "请求来源不匹配")
                    if route_path.startswith('/api/sn/diagram') and route_path not in {
                            '/api/sn/diagram/values', '/api/sn/diagram/markdown/dump'}:
                        from tin.integration.workbench_scope import require_shared_write
                        require_shared_write(scope)
                scope_marker = current_workbench.set(scope)
                request.scope["path"] = prefix + route_path
                request.scope["raw_path"] = (prefix + route_path).encode()
            elif path != '/api/tools/read' and not path.startswith('/api/workbenches/'):
                raise HTTPException(409, '请先选择绑定知识库的研究工作台，未读取旧工作台事实')
            if path.startswith('/api/workbenches/') and request.method == 'POST':
                suffix = path.split('/', 4)[-1]
                if suffix.startswith('imports/') or suffix == 'indicators':
                    raise HTTPException(409, '事实只在知识库结构化页面维护，研究台不再单独导入观测')
            request.state.research_identity = identity
            marker = current_identity.set(identity)
            try:
                if scoped_path is not None and scope.restricted:
                    from tin.web.restricted_views import restricted_response
                    response = await restricted_response(request, scope, route_path)
                else:
                    response = await call_next(request)
                response.headers["Cache-Control"] = "no-store"
                return response
            finally:
                current_identity.reset(marker)
                if scope_marker is not None:
                    current_workbench.reset(scope_marker)
        except HTTPException as exc:
            if exc.status_code == 401 and "text/html" in request.headers.get("accept", ""):
                from html import escape
                return HTMLResponse("<!doctype html><meta charset=utf-8><p>" + escape(str(exc.detail)) +
                    "</p><script>parent.postMessage({type:'research-auth-expired'},location.origin)</script>",
                    status_code=401, headers={"Cache-Control": "no-store"})
            return JSONResponse({"detail": exc.detail}, status_code=exc.status_code, headers={"Cache-Control": "no-store"})
        except PermissionError:
            return JSONResponse({"detail": "无权读取或修改其他研究员记录"}, status_code=403)
