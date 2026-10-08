"""Public presentation settings, isolated from model credentials."""
import json
from pathlib import Path
from urllib.parse import urlsplit

DEFAULTS={'title':'结构化数据工作台','workspace_label':'Excel 数据管理',
          'agent_name':'表格数据分析（结构化）','weknora_url':'http://127.0.0.1:18080/platform/agents'}


def read_ui_config(root):
    path=Path(root)/'workbench-config.json'
    result=dict(DEFAULTS)
    if path.is_file():
        settings=json.loads(path.read_text())
        for key in result:
            value=settings.get(key)
            if isinstance(value,str) and value.strip() and len(value)<500:result[key]=value.strip()
    url=urlsplit(result['weknora_url'])
    if url.scheme not in ('http','https') or not url.hostname or url.username or url.password:
        result['weknora_url']=DEFAULTS['weknora_url']
    return result
