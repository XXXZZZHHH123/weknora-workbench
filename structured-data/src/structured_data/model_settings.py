"""Provider-neutral settings shared by mapping suggestions and semantic retrieval."""
import json
import os
from pathlib import Path
import tempfile
from urllib.parse import urlsplit


def read_model(root=None):
    root=Path(root or os.environ.get('STRUCTURED_DATA_DIR',os.environ.get('COTTON_DATA_DIR','data')))
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
    return {k:cfg.get(k,'') for k in ('model','base_url')} | {'has_api_key':bool(cfg.get('api_key')),'protocol':'OpenAI-compatible'}


def save_model(root, body):
    cfg=read_model(root)
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
    fd,tmp=tempfile.mkstemp(prefix='.model-',dir=root)
    try:
        with os.fdopen(fd,'w') as f:json.dump({'chat':{k:cfg[k] for k in ('model','base_url','api_key','enable_thinking') if k in cfg}},f)
        os.replace(tmp,Path(root)/'model-settings.json')
    finally:
        if Path(tmp).exists():Path(tmp).unlink()
    return public_model(root)
