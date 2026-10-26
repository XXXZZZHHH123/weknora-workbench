"""Configured platform models; credentials remain in the native model service."""
import asyncio
import json
from urllib.parse import quote

import httpx
from fastapi import HTTPException

from tin.config import settings
from tin.llm.base import LlmResponse, ProviderUnavailable, STATUS_ERROR, STATUS_OK


class PlatformStrategyProvider:
    name = 'platform'

    def __init__(self, scope, model_id):
        if (scope is None or not scope.authorization.startswith('Bearer ') or scope.restricted or scope.metadata.get('can_read_source') is False
                or not scope.identity.can_write or not isinstance(model_id, str) or not model_id.strip()):
            raise ProviderUnavailable('请选择本工作台可用的已配置通用模型')
        self.scope, self.model = scope, model_id
        self.source_version = None
        if getattr(scope, 'scope_version', 1) == 2:
            from tin.integration.kb_facts import catalog_snapshot
            self.source_version = catalog_snapshot(scope)['version']

    def verify_scope(self):
        from tin.integration.workbench_scope import resolve_workbench
        from tin.web.judgment_generation import scope_key
        fresh = asyncio.run(resolve_workbench(self.scope.identity, self.scope.workbench_id, self.scope.authorization))
        if scope_key(fresh) != scope_key(self.scope):
            raise ProviderUnavailable('研究来源授权在生成期间改变，未保存策略候选')
        if getattr(self.scope, 'scope_version', 1) == 2:
            before = {item['id']: item['knowledge'] for item in self.scope.sources}
            after = {item['id']: item['knowledge'] for item in fresh.sources}
            if before != after:
                raise ProviderUnavailable('研究来源原件在生成期间改变，未保存策略候选')
            from tin.integration.kb_facts import catalog_snapshot
            if catalog_snapshot(fresh)['version'] != self.source_version:
                raise ProviderUnavailable('研究来源确认版本在生成期间改变，未保存策略候选')

    def complete(self, request):
        # Purpose is a fixed route, never supplied as a caller-controlled URL.
        if request.purpose != 'generate_strategy':
            raise ProviderUnavailable('平台策略模型不能用于其他用途')
        prompt = json.dumps({'instructions': [item.text for item in request.system],
            'messages': [item.dump() for item in request.messages],
            'output_schema': request.output_schema}, ensure_ascii=False, separators=(',', ':'))
        if len(prompt.encode()) > 160 * 1024:
            raise ProviderUnavailable('策略完整依据超过本次容量，请缩小研究范围')
        body = {'model_id': self.model, 'prompt': prompt, 'knowledge_base_id': self.scope.knowledge_base_id}
        revision = getattr(self.scope, 'sources_revision', 0)
        if type(revision) is int and revision > 0: body['sources_revision'] = revision
        try:
            response = httpx.post(settings.weknora_api_url.rstrip('/') + '/api/v1/workspaces/research/'
                + quote(self.scope.workbench_id, safe='') + '/strategy/generate', json=body,
                headers={'Authorization': self.scope.authorization, 'X-Tenant-ID': str(self.scope.identity.tenant_id)},
                timeout=125, follow_redirects=False, trust_env=False)
            if response.status_code != 200:
                return LlmResponse(status=STATUS_ERROR, provider=self.name, model=self.model,
                    error=f'平台策略模型请求未完成（HTTP {response.status_code}）')
            envelope = response.json(); data = envelope.get('data')
            if (envelope.get('success') is not True or not isinstance(data, dict)
                    or data.get('model_id') != self.model):
                raise ValueError('invalid model envelope')
            reason = data.get('finish_reason')
            if reason in {'length', 'max_tokens'}:
                return LlmResponse(status=STATUS_ERROR, provider=self.name, model=self.model,
                    stop_reason=reason, error='模型输出达到长度上限；未接受不完整策略')
            from tin.web.diagram_generation import strict_json
            document = strict_json(data.get('content'))
            if not isinstance(document, dict): raise ValueError('expected object')
            return LlmResponse(status=STATUS_OK, provider=self.name, model=self.model,
                text=data['content'], data=document, stop_reason=reason if isinstance(reason, str) else None)
        except (httpx.HTTPError, ValueError, TypeError, AttributeError, HTTPException):
            return LlmResponse(status=STATUS_ERROR, provider=self.name, model=self.model,
                error='平台模型未返回完整有效的策略JSON对象；未保存候选')
