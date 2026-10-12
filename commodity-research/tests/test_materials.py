import asyncio
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest
from fastapi import HTTPException
from tin.integration.identity import Identity, current_identity
from tin.web import materials


@pytest.fixture
def workspace(tmp_path,monkeypatch):
    monkeypatch.setattr(materials.settings,'data_dir',tmp_path)
    marker=current_identity.set(Identity('a','a@example.com','A',10000,'owner'))
    yield
    current_identity.reset(marker)


def test_library_binding_rejects_cross_space_and_keeps_state_separate(workspace,monkeypatch):
    rows=[{'id':'own-kb','name':'锡研报','tenant_id':10000,'type':'document'},
          {'id':'other-kb','name':'private','tenant_id':10001,'type':'document'}]
    monkeypatch.setattr(materials,'call',AsyncMock(return_value=rows))
    assert [k['id'] for k in asyncio.run(materials.libraries(None))]==['own-kb']
    with pytest.raises(HTTPException) as error:
        asyncio.run(materials.bind(None,{'kb_id':'other-kb'}))
    assert error.value.status_code==403
    assert asyncio.run(materials.bind(None,{'kb_id':'own-kb'}))=={'kb_id':'own-kb'}
    assert materials.state_path().stat().st_mode & 0o777==0o600
    marker=current_identity.set(Identity('b','b@example.com','B',10001,'owner'))
    try: assert materials.state()=={'kb_id':None}
    finally: current_identity.reset(marker)


def test_document_upload_preserves_exact_bytes_and_external_knowledge_id(workspace,session,monkeypatch):
    from starlette.datastructures import UploadFile
    from io import BytesIO
    import hashlib
    from tin.models import SourceArtifact
    from tin.provenance.service import artifact_path
    monkeypatch.setattr(materials,'bound_library',AsyncMock(return_value={'id':'own-kb'}))
    call=AsyncMock(return_value={'id':'knowledge-1','parse_status':'pending'})
    monkeypatch.setattr(materials,'call',call)
    class Session:
        def __enter__(self):return session
        def __exit__(self,*a):pass
    session.info.update(tenant_id=10000,weknora_user_id='a')
    monkeypatch.setattr(materials,'SessionLocal',Session)
    raw='锡供应研究\n原始证据'.encode()
    response=asyncio.run(materials.upload(None,UploadFile(filename='tin.txt',file=BytesIO(raw))))
    assert response['sha256']==hashlib.sha256(raw).hexdigest()
    artifact=session.get(SourceArtifact,response['artifact_id'])
    assert artifact_path(session,artifact).read_bytes()==raw
    assert artifact.details['knowledge_id']=='knowledge-1'
    assert call.await_args.kwargs['files']['file'][1]==raw
    repeat=asyncio.run(materials.upload(None,UploadFile(filename='tin.txt',file=BytesIO(raw))))
    assert repeat['already_imported'] is True
    assert call.await_count==1


def test_documents_page_preserves_total_and_passes_pagination(workspace,monkeypatch):
    async def fake(request,method,path,**kwargs):
        if path=='/knowledge-bases':return [{'id':'kb','name':'锡资料','tenant_id':10000}]
        assert kwargs['params']=={'page':3,'page_size':25}
        assert kwargs['envelope'] is True
        return {'data':[{'id':'doc-51','title':'older document'}],'total':101,'page':3,'page_size':25}
    monkeypatch.setattr(materials,'call',fake)
    asyncio.run(materials.bind(None,{'kb_id':'kb'}))
    result=asyncio.run(materials.overview(None,page=3,page_size=25))
    assert result['total']==101 and result['documents'][0]['id']=='doc-51' and result['page']==3


def test_proxy_uses_actual_browser_identity_and_fixed_weknora_origin(workspace,monkeypatch):
    from tin.integration.auth import COOKIE,_sessions
    identity=current_identity.get()
    _sessions['materials-test']=SimpleNamespace(identity=identity,token='test-private-token')
    request=SimpleNamespace(cookies={COOKIE:'materials-test'})
    try:
        assert materials.headers(request)=={'Authorization':'Bearer test-private-token','X-Tenant-ID':'10000'}
        marker=current_identity.set(Identity('other','x@example.com','X',10000,'owner'))
        try:
            with pytest.raises(HTTPException): materials.headers(request)
        finally:current_identity.reset(marker)
    finally:_sessions.pop('materials-test',None)
