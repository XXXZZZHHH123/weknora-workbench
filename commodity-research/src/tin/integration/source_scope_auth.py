"""Service-only proof for native source inventory; bearer ACL remains mandatory."""
import hashlib
import hmac
from pathlib import Path
import time
from uuid import UUID

from fastapi import HTTPException

from tin.config import settings


def proof_headers(path, authorization, tenant_id):
    prefix,suffix='/api/v1/workspaces/research/','/source-scope'
    if not path.startswith(prefix) or not path.endswith(suffix) or '?' in path or '#' in path:
        raise ValueError('Source-scope proof requires a fixed route')
    identifier=path[len(prefix):-len(suffix)]
    try:
        parsed=UUID(identifier)
        if str(parsed)!=identifier or parsed.int==0: raise ValueError
        key=Path(settings.service_token_file).read_bytes().strip()
        if len(key)<32: raise ValueError
    except (ValueError,TypeError,OSError):
        raise HTTPException(503,'研究来源服务核验配置不可用，未请求原件清单') from None
    if type(tenant_id) is not int or tenant_id<1 or not isinstance(authorization,str) or not authorization.startswith('Bearer '):
        raise HTTPException(401,'研究来源服务核验缺少当前登录')
    stamp=str(int(time.time()))
    message='\n'.join(('weknora.research-scope.v1','GET',path,stamp,hashlib.sha256(authorization.encode()).hexdigest(),str(tenant_id))).encode()
    return {'X-Research-Scope-Timestamp':stamp,'X-Research-Scope-Signature':hmac.new(key,message,hashlib.sha256).hexdigest()}


def require_inventory(document):
    data=document.get('data') if isinstance(document,dict) else None
    if not isinstance(data,dict) or data.get('inventory_included') is not True:
        raise HTTPException(503,'研究来源服务未确认原件清单核验，未将公开摘要当作空库')
