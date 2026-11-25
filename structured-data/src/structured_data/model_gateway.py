"""Use verified platform model IDs without copying provider credentials to Python."""
from contextvars import ContextVar
import json
import os
import re
from urllib.parse import quote

import httpx

from .workspaces import current_identity


current_authorization = ContextVar('structured_model_authorization', default='')
# A single request can resolve fields, bind sources and retry model output.
# Share only the public catalogue within that request's verified scope.
request_model_catalog = ContextVar('structured_request_model_catalog', default=None)
MAX_RESPONSE_BYTES = 256 * 1024


class ModelGatewayError(ValueError):
    def __init__(self, code, message):
        super().__init__(message)
        self.public_code = code


def _context():
    identity = current_identity.get()
    authorization = current_authorization.get()
    if (identity is None or not identity.knowledge_base_id
            or not authorization.startswith('Bearer ') or len(authorization) > 16391):
        raise ValueError('平台模型需要当前知识库的有效登录，请从知识库重新打开。')
    return identity, {'Authorization': authorization, 'X-Tenant-ID': str(identity.tenant_id)}


def _base():
    # Deployment-owned API address; no caller-controlled provider URL is used.
    return os.environ.get('WEKNORA_API_URL', 'http://127.0.0.1:18081').rstrip('/')


def _identifier(value):
    if not isinstance(value, str) or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}', value):
        raise ValueError('请选择平台已配置的通用模型。')
    return value


def _model(document, tenant):
    if (not isinstance(document, dict) or document.get('type') != 'KnowledgeQA'
            or document.get('status') != 'active'
            or (document.get('tenant_id') != tenant and document.get('is_builtin') is not True)):
        return None
    try:
        identifier = _identifier(document.get('id'))
    except ValueError:
        return None
    return {'id': identifier, 'name': str(document.get('display_name') or document.get('name') or identifier)[:255]}


def _check_status(response):
    if response.status_code in {401, 403}:
        raise ModelGatewayError('MODEL_AUTH', '当前登录或知识库模型使用权限已失效，请刷新后重试。')
    if response.status_code == 404:
        raise ModelGatewayError('MODEL_NOT_FOUND', '所选平台模型或知识库已不可用，请重新选择模型。')
    if response.status_code == 429:
        raise ModelGatewayError('MODEL_RATE_LIMIT', '平台模型暂时达到调用限制，请稍后重试。')
    if response.status_code >= 300:
        raise ModelGatewayError('MODEL_HTTP_ERROR', f'平台模型服务返回 HTTP {response.status_code}，请稍后重试。')
    if len(response.content) > MAX_RESPONSE_BYTES:
        raise ModelGatewayError('MODEL_RESPONSE_LIMIT', '平台模型返回内容超过大小限制。')
    try:
        result = response.json()
    except ValueError:
        raise ModelGatewayError('MODEL_OUTPUT_INVALID', '平台模型服务未返回有效结果。') from None
    if not isinstance(result, dict) or result.get('success') is not True:
        raise ModelGatewayError('MODEL_OUTPUT_INVALID', '平台模型服务未返回有效结果。')
    return result.get('data')


def available_models():
    identity, headers = _context()
    catalog = request_model_catalog.get()
    if catalog is not None and identity in catalog:
        return catalog[identity]
    try:
        with httpx.Client(timeout=5, trust_env=False, follow_redirects=False) as client:
            rows = _check_status(client.get(_base() + '/api/v1/models', headers=headers))
        if not isinstance(rows, list):
            raise ValueError('平台模型列表无效，请刷新后重试。')
        models = []
        for row in rows:
            if (model := _model(row, identity.tenant_id)) is not None:
                purposes = row.get('default_for')
                models.append(model | {
                    'workspace_default': isinstance(purposes, list) and 'KnowledgeQA' in purposes,
                    'tenant_default': row.get('tenant_id') == identity.tenant_id and row.get('is_default') is True,
                })
        if catalog is not None:
            catalog[identity] = models
        return models
    except httpx.HTTPError:
        raise ValueError('暂时无法读取平台模型，请检查平台连接。') from None


async def validate_model(identifier):
    identifier = _identifier(identifier)
    identity, headers = _context()
    try:
        async with httpx.AsyncClient(timeout=10, trust_env=False, follow_redirects=False) as client:
            row = _check_status(await client.get(_base() + '/api/v1/models/' + quote(identifier, safe=''), headers=headers))
        model = _model(row, identity.tenant_id)
        if model is None or model['id'] != identifier:
            raise ValueError('所选模型不是当前空间可用的通用模型，请重新选择。')
        return model
    except httpx.HTTPError:
        raise ValueError('暂时无法核验平台模型，配置未保存。') from None


def cache_identity(config):
    fields = {key: config.get(key) for key in ('native_model_id', 'model', 'base_url', 'enable_thinking')}
    if config.get('native_model_id'):
        identity = current_identity.get()
        fields['scope'] = ({key: getattr(identity, key) for key in (
            'user_id', 'tenant_id', 'knowledge_base_id', 'knowledge_base_tenant_id', 'workbench_id')}
            if identity is not None else None)
    return json.dumps(fields, sort_keys=True)


def _review_status(response, timeout):
    from .review_model import ReviewModelError

    # These reasons originate in our platform handler. Provider error text is
    # never forwarded by the handler or used for a retry decision here.
    if response.status_code in {502, 504}:
        try:
            envelope = response.json()
            error = envelope.get('error') if isinstance(envelope, dict) else None
            reason = error.get('reason') if isinstance(error, dict) else None
        except ValueError:
            reason = None
        if reason in ('MODEL_REASONING_ONLY', 'MODEL_TRUNCATED', 'MODEL_EMPTY',
                      'MODEL_TIMEOUT', 'MODEL_UPSTREAM_REJECTED', 'MODEL_HTTP_ERROR', 'MODEL_CONNECTION'):
            raise ReviewModelError(reason, timeout=timeout,
                retry_thinking_off=(reason == 'MODEL_REASONING_ONLY' and error.get('retry_thinking_off') is True))
    if response.status_code >= 300 and response.status_code not in {401, 403, 404, 429}:
        raise ReviewModelError('MODEL_TIMEOUT' if response.status_code == 504 else 'MODEL_HTTP_ERROR', timeout=timeout)
    return _check_status(response)


def complete(config, messages, *, max_tokens=8192, temperature=0, enable_thinking=None, timeout=90, purpose='repair',
             response_format=None, review_timeout=None, cancelled=None):
    """An OpenAI-shaped adapter; authorization stays only in the active context."""
    identifier = _identifier(config.get('native_model_id'))
    from .ingestion import current_ingestion_claim, signed_headers
    claim = current_ingestion_claim.get()
    identity, headers = (current_identity.get(), {}) if claim else _context()
    endpoint = {'repair': 'chat', 'fields': 'plan'}.get(purpose)
    if endpoint is None:
        raise ValueError('不支持的结构化模型任务。')
    body = {'model_id': identifier, 'messages': messages, 'max_tokens': max_tokens, 'temperature': temperature}
    if isinstance(enable_thinking, bool):
        body['enable_thinking'] = enable_thinking
    if response_format:
        body['response_format'] = response_format
    if review_timeout is not None:
        body['timeout_ms'] = max(1, int(review_timeout * 1000))
    if claim:
        raw = json.dumps(body, separators=(',', ':')).encode()
        path = '/internal/ingestion/model'
        headers = signed_headers(claim, 'POST', path, body=raw)
        headers['Content-Type'] = 'application/json'
        post_body = {'content': raw}
    else:
        path = '/api/v1/knowledge-bases/' + quote(identity.knowledge_base_id, safe='') + '/structured-model/' + endpoint
        post_body = {'json': body}
    url = _base() + path
    try:
        if review_timeout is not None:
            from .review_model import post_review
            response = post_review(url, headers=headers, timeout=timeout, model_timeout=review_timeout,
                                   cancelled=cancelled, max_bytes=MAX_RESPONSE_BYTES, trust_env=False, **post_body)
            data = _review_status(response, review_timeout)
        else:
            with httpx.Client(timeout=timeout, trust_env=False, follow_redirects=False) as client:
                data = _check_status(client.post(url, headers=headers, **post_body))
        if (not isinstance(data, dict) or data.get('model_id') != identifier
                or not isinstance(data.get('content'), str) or not isinstance(data.get('finish_reason'), str)):
            raise ModelGatewayError('MODEL_OUTPUT_INVALID', '平台模型未返回所选模型的有效结果，未应用建议。')
        return {'choices': [{'message': {'content': data['content']}, 'finish_reason': data['finish_reason']}]}
    except httpx.HTTPError as exc:
        code = 'MODEL_TIMEOUT' if isinstance(exc, getattr(httpx, 'TimeoutException', ())) else 'MODEL_CONNECTION'
        raise ModelGatewayError(code, '平台模型连接失败或超时，未切换到其他模型。') from None
