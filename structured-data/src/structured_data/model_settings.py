"""Provider-neutral settings shared by mapping suggestions and semantic retrieval."""
import json
import os
from pathlib import Path
import tempfile
from urllib.parse import urlsplit

from .workspaces import current_identity


NO_FIELD_MODEL = '没有可用的字段模型。请在 WeKnora「模型管理」添加并启用对话模型，再在知识库设置选择摘要模型，或在结构化页面的「字段模型」中选择。'
AMBIGUOUS_FIELD_MODEL = '当前空间有多个可用对话模型，且未确定唯一默认模型。请在知识库设置选择摘要模型，或在结构化页面的「字段模型」中选择。'
FOREIGN_KB_MODEL = '字段模型只支持当前空间的知识库。请切换到该知识库所属空间后重试。'


def _native_selection(root):
    path = Path(root) / 'model-settings.json'
    if not path.is_file():
        return {}
    selected = json.loads(path.read_text()).get('chat', {})
    if selected.get('native_model_id'):
        return {key: selected[key] for key in ('native_model_id', 'enable_thinking') if key in selected}
    return {}


def _automatic_model(models, summary_model_id):
    summary = next((model for model in models if model['id'] == summary_model_id), None)
    if summary:
        return summary, ''
    defaults = [model for model in models if model['workspace_default']]
    if not defaults:
        defaults = [model for model in models if model['tenant_default']]
    candidates = defaults or models
    if len(candidates) == 1:
        return candidates[0], ''
    return None, AMBIGUOUS_FIELD_MODEL if models else NO_FIELD_MODEL


def platform_model_settings(root):
    from .model_gateway import available_models

    identity = current_identity.get()
    if identity.knowledge_base_tenant_id != identity.tenant_id:
        explicit_id, models, effective, error = '', [], None, FOREIGN_KB_MODEL
    else:
        explicit_id = _native_selection(root).get('native_model_id', '')
        models = available_models()
        if explicit_id:
            effective = next((model for model in models if model['id'] == explicit_id), None)
            error = '' if effective else '已选择的字段模型不可用，请在结构化页面的「字段模型」中重新选择已启用的对话模型。'
        else:
            effective, error = _automatic_model(models, identity.summary_model_id)

    def public(model):
        return {key: model[key] for key in ('id', 'name')}

    return {
        'native_model_id': explicit_id,
        'protocol': 'platform',
        'can_edit': identity.can_write and identity.knowledge_base_tenant_id == identity.tenant_id,
        'models': [public(model) for model in models],
        'effective_model': public(effective) if effective else None,
        'model_selection': ('explicit' if explicit_id else 'automatic') if effective else 'unavailable',
        'model_error': error,
    }


def read_model(root=None):
    from .ingestion import current_ingestion_claim, current_ingestion_model
    if current_ingestion_claim.get():
        # The admitted upload uses the KB's server-configured model. No user
        # credential or legacy provider key is inherited by the background task.
        selected = _native_selection(root) if root else {}
        if selected:
            return selected
        model = current_ingestion_model.get()
        return {'native_model_id': model} if model else {}
    root=Path(root or os.environ.get('STRUCTURED_DATA_DIR',os.environ.get('COTTON_DATA_DIR','data')))
    identity = current_identity.get()
    if identity is not None and identity.knowledge_base_id:
        # Verified KB requests use platform model IDs. Legacy provider
        # configuration belongs to the unscoped path below.
        from .model_gateway import available_models

        if identity.knowledge_base_tenant_id != identity.tenant_id:
            return {'model_error': FOREIGN_KB_MODEL}
        selected = _native_selection(root)
        if selected:
            return selected
        try:
            effective, error = _automatic_model(available_models(), identity.summary_model_id)
        except ValueError as exc:
            return {'model_error': str(exc)}
        return {'native_model_id': effective['id']} if effective else {'model_error': error}

    path=root/'model-settings.json'
    if not path.is_file():
        # A KB may reuse its owner's configured model, never another tenant's pilot secret.
        if root.parent.name == 'knowledge-bases':
            return read_model(root.parent.parent)
        # New spaces must configure their own provider. A global legacy pilot
        # secret must never become an editable credential in another space.
        if root.parent.name=='workspaces' and root.name.isdecimal():
            return {}
        path=Path(os.environ.get('STRUCTURED_MODEL_CONFIG',os.environ.get('COTTON_MODEL_CONFIG','/run/secrets/model-config.json')))
    cfg=json.loads(path.read_text()).get('chat',{}) if path.is_file() else {}
    cfg=dict(cfg)
    cfg['api_key']=cfg.get('api_key') or os.environ.get(cfg.get('api_key_env',''),'')
    return cfg


# Mirrors the Go provider registry (internal/models/providers ThinkingFormat).
# This is wire-protocol selection by configured endpoint, never by question.
_THINKING_TYPE_HOSTS = ('volces.com', 'deepseek.com', 'moonshot.ai', 'moonshot.cn', 'bigmodel.cn',
                        'minimax.cn', 'minimax.io', 'xiaomimimo.com', 'lkeap.cloud.tencent.com')
_THINKING_FORMATS = ('thinking-type', 'enable-thinking', 'none')


def thinking_format(cfg):
    explicit = cfg.get('thinking_format')
    if explicit in _THINKING_FORMATS:
        return explicit
    host = (urlsplit(str(cfg.get('base_url') or '')).hostname or '').lower()
    if any(host == suffix or host.endswith('.' + suffix) for suffix in _THINKING_TYPE_HOSTS):
        return 'thinking-type'
    return 'enable-thinking'


def thinking_fields(cfg, enabled):
    """Request fields that actually toggle reasoning for this endpoint.

    Some vendors silently ignore an unknown enable_thinking flag and keep
    reasoning, which made bounded verification calls slow without any error.
    enabled=None keeps the provider default.
    """
    if not isinstance(enabled, bool):
        return {}
    form = thinking_format(cfg)
    if form == 'thinking-type':
        return {'thinking': {'type': 'enabled' if enabled else 'disabled'}}
    if form == 'enable-thinking':
        return {'enable_thinking': enabled}
    return {}


def public_model(root):
    cfg=read_model(root)
    if cfg.get('native_model_id'):
        return {'native_model_id':cfg['native_model_id'],'protocol':'platform'}
    return {k:cfg.get(k,'') for k in ('model','base_url')} | {'has_api_key':bool(cfg.get('api_key')),'protocol':'OpenAI-compatible'}


def save_model(root, body):
    if not isinstance(body,dict):raise ValueError('模型配置格式无效')
    if 'native_model_id' in body:
        from .model_gateway import _identifier
        if set(body)-{'native_model_id','enable_thinking'}:raise ValueError('平台模型只需选择已配置的模型，不接收地址或密钥')
        selected={'native_model_id':_identifier(body['native_model_id'])}
        if 'enable_thinking' in body:
            if not isinstance(body['enable_thinking'],bool):raise ValueError('enable_thinking 须为布尔值')
            selected['enable_thinking']=body['enable_thinking']
        _write_model(root,selected)
        return public_model(root)
    cfg=read_model(root)
    if cfg.get('native_model_id'):
        raise ValueError('当前已选择平台模型，请继续从平台模型列表选择；不会自动回退旧服务')
    old_endpoint=cfg.get('base_url','').rstrip('/')
    new_endpoint=str(body.get('base_url','')).strip().rstrip('/')
    if old_endpoint and new_endpoint!=old_endpoint and not body.get('api_key'):
        raise ValueError('更换 API 地址时必须填写该地址的新密钥，不能转发原密钥')
    for key in ('model','base_url'):
        value=body.get(key)
        if not isinstance(value,str) or not value.strip() or len(value)>1000:raise ValueError('请填写有效的模型名称和 API 地址')
        cfg[key]=value.strip()
    url=urlsplit(cfg['base_url'])
    if url.scheme not in ('https','http') or not url.hostname or url.username or url.password or url.query or url.fragment:
        raise ValueError('API 地址须为完整 HTTP(S) 地址，不包含密钥、查询参数或片段')
    if 'enable_thinking' in body:
        if not isinstance(body['enable_thinking'],bool):raise ValueError('enable_thinking 须为布尔值')
        cfg['enable_thinking']=body['enable_thinking']
    if body.get('api_key'):
        if not isinstance(body['api_key'],str) or len(body['api_key'])>4096:raise ValueError('密钥格式无效')
        cfg['api_key']=body['api_key'].strip()
    if not cfg.get('api_key'):raise ValueError('请填写 API Key')
    # Only this service's override changes. Original shared provider configuration stays intact.
    _write_model(root,{k:cfg[k] for k in ('model','base_url','api_key','enable_thinking') if k in cfg})
    return public_model(root)


def _write_model(root,cfg):
    fd,tmp=tempfile.mkstemp(prefix='.model-',dir=root)
    try:
        with os.fdopen(fd,'w') as f:json.dump({'chat':cfg},f)
        os.replace(tmp,Path(root)/'model-settings.json')
    finally:
        if Path(tmp).exists():Path(tmp).unlink()
