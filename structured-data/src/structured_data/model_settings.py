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
        path=Path(os.environ.get('STRUCTURED_MODEL_CONFIG',os.environ.get('COTTON_MODEL_CONFIG','/run/secrets/model-config.json')))
    cfg=json.loads(path.read_text()).get('chat',{}) if path.is_file() else {}
    cfg=dict(cfg)
    cfg['api_key']=cfg.get('api_key') or os.environ.get(cfg.get('api_key_env',''),'')
    return cfg


def public_model(root):
    cfg=read_model(root)
    return {k:cfg.get(k,'') for k in ('model','base_url')} | {'has_api_key':bool(cfg.get('api_key')),'protocol':'OpenAI-compatible'}


def save_model(root, body):
    cfg=read_model(root)
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
