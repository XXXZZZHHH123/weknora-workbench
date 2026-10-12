"""Research documents reuse WeKnora ingestion and the caller's existing access."""
import hashlib
import json
import os
from pathlib import Path
import re
import uuid
from datetime import datetime, timezone

import httpx
from fastapi import APIRouter, Body, File, HTTPException, Request, UploadFile, Query
from fastapi.responses import FileResponse
from fastapi.templating import Jinja2Templates

from tin.config import settings
from tin.db import SessionLocal
from tin.integration.identity import current_identity, research_prefix

router = APIRouter()
templates = Jinja2Templates(directory=Path(__file__).parent/'templates')
MAX_FILE = 100*1024*1024


def state_path():
    identity = current_identity.get()
    if identity is None:
        raise HTTPException(401, '请从 WeKnora 打开商品研究')
    directory = Path(settings.data_dir)/'workspaces'/str(identity.tenant_id)
    directory.mkdir(parents=True, exist_ok=True)
    return directory/'materials.json'


def state():
    path = state_path()
    return json.loads(path.read_text()) if path.is_file() else {'kb_id':None}


def headers(request):
    from tin.integration.auth import COOKIE, _sessions
    session = _sessions.get(request.cookies.get(COOKIE,''))
    identity = current_identity.get()
    if not session or not identity or session.identity.user_id != identity.user_id or session.identity.tenant_id != identity.tenant_id:
        raise HTTPException(401, '请重新从 WeKnora 打开商品研究')
    return {'Authorization':'Bearer '+session.token, 'X-Tenant-ID':str(identity.tenant_id)}


async def call(request, method, path, *, envelope=False, **kwargs):
    if not settings.weknora_api_url:
        raise HTTPException(503, '研报资料入口需要连接 WeKnora')
    try:
        async with httpx.AsyncClient(timeout=90, follow_redirects=False, trust_env=False) as client:
            response = await client.request(method, settings.weknora_api_url.rstrip('/')+'/api/v1'+path,
                                            headers=headers(request), **kwargs)
    except httpx.HTTPError:
        raise HTTPException(503, '资料服务暂时无法连接，请稍后重试') from None
    if response.status_code >= 300:
        # Never echo response bodies or credentials from an upstream service.
        raise HTTPException(response.status_code if 400<=response.status_code<500 else 503,
                            '资料操作失败，请检查知识库权限、模型配置或重复文件')
    doc = response.json()
    return doc if envelope else doc.get('data',doc)


async def libraries(request):
    result = await call(request,'GET','/knowledge-bases')
    rows = result if isinstance(result,list) else result.get('knowledge_bases',result.get('items',[]))
    identity = current_identity.get()
    return [row for row in rows if str(row.get('tenant_id'))==str(identity.tenant_id) and row.get('type','document')=='document']


async def bound_library(request):
    selected = state().get('kb_id')
    row = next((row for row in await libraries(request) if row['id']==selected),None)
    if row is None:
        raise HTTPException(409, '请先为锡研究选择当前空间的文档知识库')
    return row


@router.get('/materials')
def page(request: Request):
    identity=current_identity.get()
    return templates.TemplateResponse(request,'materials.html',{'prefix':research_prefix(),
        'identity':{'tenant':identity.tenant_id,'user':identity.user_id} if identity else None})


@router.get('/api/materials')
async def overview(request: Request, page: int = Query(1,ge=1), page_size: int = Query(25,ge=1,le=100)):
    available = await libraries(request)
    selected = state().get('kb_id')
    selected = selected if any(row['id']==selected for row in available) else None
    documents=[]; total=0
    if selected:
        result = await call(request,'GET',f'/knowledge-bases/{selected}/knowledge',envelope=True,
                            params={'page':page,'page_size':page_size})
        documents=result.get('data',[]);total=result.get('total',len(documents))
    return {'kb_id':selected,'libraries':[{'id':row['id'],'name':row['name']} for row in available],
            'documents':documents,'total':total,'page':page,'page_size':page_size,
            'notice':'正文按知识库处理；表格指标请从文件接入入口导入。'}


@router.post('/api/materials/library')
async def bind(request: Request, body: dict = Body(...)):
    selected = body.get('kb_id')
    if not any(row['id']==selected for row in await libraries(request)):
        raise HTTPException(403, '只能绑定当前空间有权限的文档知识库')
    path = state_path()
    temporary=path.with_name(path.name+'.'+uuid.uuid4().hex+'.tmp')
    try:
        temporary.write_text(json.dumps({'kb_id':selected}))
        temporary.chmod(0o600)
        os.replace(temporary,path)
    finally:
        temporary.unlink(missing_ok=True)
    return {'kb_id':selected}


@router.post('/api/materials/upload')
async def upload(request: Request, file: UploadFile = File(...)):
    library = await bound_library(request)
    filename = Path((file.filename or '').replace('\\','/')).name
    if Path(filename).suffix.lower() not in {'.pdf','.docx','.doc','.txt','.md','.html','.png','.jpg','.jpeg','.webp'}:
        raise HTTPException(400, '支持 PDF、Word、文本、网页文件和图片；表格请使用文件接入入口')
    chunks,size=[],0
    while chunk:=await file.read(1024*1024):
        size+=len(chunk)
        if size>MAX_FILE:
            raise HTTPException(413, '文件超过100MB')
        chunks.append(chunk)
    raw=b''.join(chunks)
    if not raw:
        raise HTTPException(400, '文件为空')
    sha=hashlib.sha256(raw).hexdigest()
    from tin.provenance.service import preserve_blob, start_batch
    with SessionLocal() as session:
        artifact=preserve_blob(session,raw,filename,kind='document',metadata={'variety':'SN'})
        batch=start_batch(session,artifact,kind='document',mapping={'kb_id':library['id']},
                          key={'sha256':sha,'kb_id':library['id'],'kind':'document'})
        session.commit()
        if batch.status=='committed':
            return {**batch.result,'already_imported':True}
        batch_id=batch.id
    try:
        result=await call(request,'POST',f'/knowledge-bases/{library["id"]}/knowledge/file',
            files={'file':(filename,raw,file.content_type or 'application/octet-stream')},
            data={'metadata':json.dumps({'commodity':'SN','source_sha256':sha},ensure_ascii=False)})
    except HTTPException as exc:
        if exc.status_code!=409:
            raise
        # Recover a successful remote upload whose local acknowledgement failed.
        existing=await call(request,'GET',f'/knowledge-bases/{library["id"]}/knowledge',
                            params={'page':1,'page_size':100,'keyword':filename})
        matches=[row for row in existing if isinstance(row.get('metadata'),dict)
                 and row['metadata'].get('source_sha256')==sha]
        if len(matches)!=1:
            raise HTTPException(409, '原件已保留；知识库有重复文件，请在知识库核对该文件后重试') from None
        result=matches[0]
    with SessionLocal() as session:
        from tin.models import SourceArtifact, SourceBatch
        artifact=session.get(SourceArtifact,sha)
        binding={'knowledge_id':result.get('id'),'kb_id':library['id'],'parse_status':result.get('parse_status')}
        bindings=list(artifact.details.get('knowledge_bindings',[]))
        if not any(row.get('knowledge_id')==binding['knowledge_id'] and row.get('kb_id')==binding['kb_id'] for row in bindings):
            bindings.append(binding)
        artifact.details={**artifact.details,**binding,'knowledge_bindings':bindings}
        response={'knowledge':result,'artifact_id':sha,'sha256':sha,
                  'notice':'原件已保存，正文按知识库配置解析；图片和扫描件识别取决于视觉模型配置。'}
        batch=session.get(SourceBatch,batch_id);batch.status='committed';batch.result=response
        batch.committed_at=datetime.now(timezone.utc)
        session.commit()
    return response


@router.post('/api/materials/url')
async def url(request: Request, body: dict = Body(...)):
    library=await bound_library(request)
    # WeKnora validates the URL and applies its normal fetch/SSRF policy.
    result=await call(request,'POST',f'/knowledge-bases/{library["id"]}/knowledge/url',
                      json={'url':body.get('url'),'title':body.get('title','')})
    return {'knowledge':result,'notice':'网页由知识库采集、保存和解析，可在普通对话中查询。'}
