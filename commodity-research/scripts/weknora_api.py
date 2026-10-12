"""Small local administration client; credentials never enter output or URLs."""
import json
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit
from urllib.request import Request, build_opener, ProxyHandler, HTTPRedirectHandler


class Failure(ValueError):
    pass


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # Login and bearer requests must never be forwarded to another origin.
        return None


class API:
    def __init__(self, url, tenant=None):
        parsed = urlsplit(url)
        if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
            raise Failure("API 地址无效，不能包含凭据")
        self.url, self.token, self.tenant = url.rstrip("/"), "", tenant
        self.opener = build_opener(ProxyHandler({}), NoRedirect())

    def call(self, method, path, body=None):
        headers = {"Accept": "application/json"}
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        if self.tenant:
            headers["X-Tenant-ID"] = str(self.tenant)
        payload = None if body is None else json.dumps(body).encode()
        if payload is not None:
            headers["Content-Type"] = "application/json"
        try:
            with self.opener.open(Request(self.url + path, data=payload, headers=headers, method=method), timeout=30) as response:
                result = json.load(response)
        except HTTPError as exc:
            raise Failure(f"{method} {path}: HTTP {exc.code}") from None
        except (URLError, ValueError, TimeoutError):
            raise Failure("WeKnora 连接失败或响应格式无效") from None
        if isinstance(result, dict) and result.get("success") is False:
            raise Failure("WeKnora 拒绝本次操作")
        return result

    def login_email(self, accounts, email):
        rows = json.loads(Path(accounts).read_text())
        if isinstance(rows, dict):
            rows = [rows]
        matches = [row for row in rows if row.get("email") == email]
        if len(matches) != 1:
            raise Failure("账号文件中未找到唯一指定邮箱")
        response = self.call("POST", "/api/v1/auth/login", matches[0])
        self.token = response.get("token", "")
        if not self.token:
            raise Failure("登录未返回有效凭据")
        data = self.call("GET", "/api/v1/auth/me")["data"]
        if data["user"].get("email") != email or not data.get("tenant"):
            raise Failure("目标账号或当前空间核验失败")
        self.tenant = int(data["tenant"]["id"])
        return data


def unpack(result):
    return result.get("data", result)


def rows(result):
    data = unpack(result)
    if isinstance(data, dict):
        data = data.get("items", data.get("data", []))
    if not isinstance(data, list):
        raise Failure("列表响应格式无效")
    return data
