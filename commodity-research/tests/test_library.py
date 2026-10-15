import hashlib
import json
import time
from datetime import datetime, timedelta, timezone

from fastapi import FastAPI
from fastapi.testclient import TestClient
from fastapi.responses import JSONResponse
import httpx
import pytest
from sqlalchemy import func, select
from sqlalchemy.orm import sessionmaker
from sqlalchemy.pool import StaticPool

from tin.config import settings, SHANGHAI
from tin.db import Base, make_engine
from tin.integration import auth
from tin.integration.identity import Identity, current_identity
from tin.jobs.seed import seed_indicators
from tin.ingest.record import record
from tin.models import ImportPreview, LibraryOperation, Observation, SourceArtifact
from tin.provenance import router as sources
from tin.provenance.service import artifact_path, preserve_blob
from tin.web import library
from tin.schemas.caliber import Caliber
from tin.schemas.observation import ObservationIn


@pytest.fixture
def center(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, "data_dir", tmp_path)
    monkeypatch.setattr(settings, "weknora_api_url", "http://weknora.internal")
    monkeypatch.setattr(settings, "public_prefix", "")
    factories = {}

    def factory():
        identity = current_identity.get()
        if identity.tenant_id not in factories:
            engine = make_engine("sqlite://", poolclass=StaticPool, connect_args={"check_same_thread": False})
            Base.metadata.create_all(engine)
            factories[identity.tenant_id] = sessionmaker(engine, expire_on_commit=False)
            with factories[identity.tenant_id]() as session:
                seed_indicators(session)
        session = factories[identity.tenant_id]()
        session.info.update(identity.session_info())
        return session

    async def verify(token, tenant):
        who, role = token.split(":")
        return Identity(who, who + "@example.com", who, int(tenant or 100), role)

    monkeypatch.setattr(auth, "verify_bearer", verify)
    monkeypatch.setattr(library, "SessionLocal", factory)
    monkeypatch.setattr(sources, "SessionLocal", factory)
    app = FastAPI()
    app.include_router(library.router)
    app.include_router(sources.router)
    # Retired library-router component harness: exercise provider recovery and
    # immutable bytes independently of the production KB/workbench gateway.
    # Real authentication, Origin and actor-pin tests live in the auth/scope suites.
    @app.middleware('http')
    async def component_identity(request, call_next):
        supplied = request.headers.get('authorization', '')
        saved = auth._sessions.get(request.cookies.get(auth.COOKIE, ''))
        token = supplied.removeprefix('Bearer ') if supplied.startswith('Bearer ') else saved.token if saved else ''
        if not token:
            return JSONResponse({'detail': 'Component fixture requires identity'}, status_code=401)
        identity = await verify(token, request.headers.get('x-tenant-id'))
        if request.url.path == '/auth/session':
            sid = 'legacy-component-' + identity.user_id
            auth._sessions[sid] = auth.ResearchSession(token, identity, time.time() + 600, time.time())
            response = JSONResponse({'success': True})
            response.set_cookie(auth.COOKIE, sid)
            return response
        marker = current_identity.set(identity)
        try:
            return await call_next(request)
        finally:
            current_identity.reset(marker)
    with TestClient(app) as client:
        yield client, factory
    for session_factory in factories.values():
        session_factory.kw["bind"].dispose()


def headers(who="a", role="owner", tenant=100):
    return {"Authorization": f"Bearer {who}:{role}", "X-Tenant-ID": str(tenant),
            "X-Research-User": who, "Origin": "http://testserver"}


def upload(client, data=b"original evidence", name="report.txt", **kwargs):
    response = client.post("/api/library/upload", files={"file": (name, data)}, headers=headers(**kwargs))
    assert response.status_code == 200, response.text
    return response.json()["asset"]


def process(client, asset, body, **kwargs):
    response = client.post(f'/api/library/{asset["id"]}/process', json=body, headers=headers(**kwargs))
    assert response.status_code == 200, response.text
    return response.json()["operation"]


def operation(client, identifier, **kwargs):
    response = client.get("/api/library/operations/" + identifier, headers=headers(**kwargs))
    assert response.status_code == 200, response.text
    return response.json()["operation"]


def response(data=None, status=200, content=None):
    return httpx.Response(status, json=data if content is None else None, content=content)


def test_unassigned_upload_private_and_legacy_sources_cannot_bypass(center):
    client, _ = center
    asset = upload(client)
    assert not asset["shared_research"]
    assert client.get(f'/api/library/{asset["id"]}', headers=headers()).status_code == 200
    assert client.get(f'/api/library/{asset["id"]}', headers=headers(who="b")).status_code == 404
    assert client.get(f'/api/library/{asset["id"]}', headers=headers(tenant=101)).status_code == 404
    assert client.get('/api/library', headers=headers(who="b")).json()["total"] == 0
    client.post('/auth/session', headers=headers())
    assert client.get('/api/sources').json()["sources"] == []
    for suffix in ("", "/download"):
        assert client.get('/api/sources/' + asset["id"] + suffix).status_code == 404
    assert client.get(f'/api/library/{asset["id"]}/download', headers=headers()).content == b"original evidence"
    # Receiving the same bytes is independently recorded without copying bytes.
    same = upload(client, name="different-name.txt", who="b")
    assert same["id"] == asset["id"] and same["filename"] == "different-name.txt"
    assert client.get('/api/library', headers=headers(who="b")).json()["total"] == 1


def test_viewer_document_uses_real_shared_kb_grants_and_preserves_options(center, monkeypatch):
    client, _ = center
    raw = b"shared-kb original"
    asset = upload(client, raw, who="reader", role="viewer")
    calls = []
    state = {"parse_status": "pending"}

    async def remote(request, provider, method, path, **kwargs):
        calls.append((provider, method, path, kwargs))
        if path == '/knowledge-bases/shared':
            return response({"data": {"id": "shared", "tenant_id": 999}})
        if method == 'POST' and path.endswith('/knowledge/file'):
            assert kwargs['files']['file'][1] == raw
            assert kwargs['data']['fileName'] == 'reports/tin/report.txt'
            assert kwargs['data']['tag_ids'] == 'tag-a,tag-b'
            assert json.loads(kwargs['data']['process_config']) == {"chunk_size": 500}
            assert kwargs['data']['enable_multimodel'] == 'true'
        return response({"data": {"id": "knowledge-1", **state}})

    monkeypatch.setattr(library, '_remote', remote)
    op = process(client, asset, {"purpose": "knowledge", "kb_id": "shared", "folder": "reports/tin",
        "tag_ids": ["tag-a", "tag-b"], "process_config": {"chunk_size": 500}, "enable_multimodel": True}, who="reader", role="viewer")
    status = operation(client, op['id'], who="reader", role="viewer")
    assert status['status'] == 'pending' and status['parse_status'] == 'pending'
    state['parse_status'] = 'completed'
    assert operation(client, op['id'], who="reader", role="viewer")['status'] == 'completed'
    rejected = client.post(f'/api/library/{asset["id"]}/process', json={'purpose':'research'}, headers=headers(who="reader", role="viewer"))
    assert rejected.status_code == 403
    assert all('reader:viewer' not in json.dumps(item) for item in status.values())
    client.post('/auth/session', headers=headers(who="reader", role="viewer"))
    assert client.get('/api/sources').json()['sources'] == []


def test_kb_rejection_is_recoverable_and_original_is_retained(center, monkeypatch):
    client, _ = center
    asset = upload(client)
    permitted = False

    async def remote(request, provider, method, path, **kwargs):
        if path == '/knowledge-bases/kb':
            return response({'data': {'id': 'kb', 'tenant_id':100}})
        if method == 'POST':
            return response({'data': {'id':'k', 'parse_status':'pending'}}, status=200 if permitted else 403)
        return response({'data': {'id':'k', 'parse_status':'completed'}})

    monkeypatch.setattr(library, '_remote', remote)
    op = process(client, asset, {'purpose':'knowledge','kb_id':'kb'})
    assert operation(client, op['id'])['status'] == 'failed'
    assert client.get(f'/api/library/{asset["id"]}/download', headers=headers()).content == b'original evidence'
    permitted = True
    retried = client.post('/api/library/operations/' + op['id'] + '/retry', headers=headers())
    assert retried.status_code == 200
    assert operation(client, op['id'])['status'] == 'completed'
    assert operation(client, op['id'])['attempts'] == 2


def test_duplicate_kb_ack_recovers_existing_identity_by_original_hash(center, monkeypatch):
    client, _ = center
    asset = upload(client)

    async def remote(request, provider, method, path, **kwargs):
        if path == '/knowledge-bases/kb':
            return response({'data': {'id':'kb','tenant_id':100}})
        if path.endswith('/knowledge/file'):
            return response({'code':'duplicate_file','data':{'id':'old-k','parse_status':'completed'}},status=409)
        if path.endswith('/download'):
            return response(content=b'original evidence')
        return response({'data': {'id':'old-k','parse_status':'completed'}})

    monkeypatch.setattr(library, '_remote', remote)
    op = process(client, asset, {'purpose':'knowledge','kb_id':'kb'})
    assert operation(client, op['id'])['remote_ids']['knowledge_id'] == 'old-k'
    assert operation(client, op['id'])['status'] == 'completed'


def test_kb_read_without_raw_download_cannot_publish_or_reprocess(center, monkeypatch):
    client, _ = center
    asset = upload(client)

    async def remote(request, provider, method, path, **kwargs):
        if path == '/knowledge-bases/kb':
            return response({'data':{'id':'kb','tenant_id':100}})
        if path.endswith('/download'):
            return response(status=403)
        return response({'data': {'id':'k','parse_status':'completed'}})

    monkeypatch.setattr(library, '_remote', remote)
    process(client, asset, {'purpose':'knowledge','kb_id':'kb'})
    assert client.get(f'/api/library/{asset["id"]}', headers=headers(who='b')).status_code == 200
    assert client.get(f'/api/library/{asset["id"]}/download', headers=headers(who='b')).status_code == 403
    for body in [{'purpose':'research'}, {'purpose':'knowledge','kb_id':'another-kb'}]:
        assert client.post(f'/api/library/{asset["id"]}/process', json=body, headers=headers(who='b')).status_code == 403
    assert client.get('/api/library/operations/not-mine', headers=headers(who='b')).status_code == 404


def test_research_mapping_confirmation_reuses_file_and_operation(center):
    client, _ = center
    raw = b'date,unknown-price\n2026-09-18,100\n'
    asset = upload(client, raw, 'provider.csv')
    assert not asset['capabilities']['structured'] and asset['capabilities']['research']
    op = process(client, asset, {'purpose':'research'})
    preview = operation(client, op['id'])
    assert preview['status'] == 'needs_mapping' and preview['result']['mode'] == 'table'
    confirmed = process(client, asset, {'purpose':'research','operation_id':op['id'],'confirm':True,
        'column_overrides':{'c1':{'series_id':'SMM.SN.spot.1','source_unit':'元/吨','factor':1}}})
    assert confirmed['id'] == op['id']
    saved = operation(client, op['id'])
    assert saved['status'] == 'completed' and saved['result']['import_result']['committed'] == 1
    # A repeat confirmation returns the durable result instead of rerunning.
    assert process(client, asset, {'purpose':'research','operation_id':op['id'],'confirm':True,
        'column_overrides':{'c1':{'series_id':'SMM.SN.spot.1','source_unit':'元/吨','factor':1}}})['id'] == op['id']
    client.post('/auth/session', headers=headers())
    assert any(row['id'] == asset['id'] for row in client.get('/api/sources').json()['sources'])
    assert client.get('/api/sources/' + asset['id'] + '/download').content == raw


def test_research_retained_original_regenerates_expired_preview(center):
    client, factory = center
    asset = upload(client, b'date,[SMM.SN.spot.1]\n2026-09-18,100\n', 'provider.csv')
    op = process(client, asset, {'purpose':'research'})
    first = operation(client, op['id'])
    token = current_identity.set(Identity('a','a@example.com','A',100,'owner'))
    try:
        with factory() as session:
            row=session.get(ImportPreview,first['result']['preview_id'])
            row.expires_at=datetime.now(timezone.utc)-timedelta(hours=1)
            session.commit()
    finally:
        current_identity.reset(token)
    process(client, asset, {'purpose':'research','operation_id':op['id'],'confirm':True})
    expired=operation(client,op['id'])
    assert expired['status']=='failed' and '过期' in expired['error']
    assert expired['result']['preview_id']==first['result']['preview_id']
    assert client.post('/api/library/operations/'+op['id']+'/retry',headers=headers()).status_code==200
    renewed=operation(client,op['id'])
    assert renewed['status']=='needs_mapping' and renewed['result']['preview_id']!=first['result']['preview_id']
    process(client,asset,{'purpose':'research','operation_id':op['id'],'confirm':True})
    assert operation(client,op['id'])['status']=='completed'


def test_structured_lost_job_recovers_durable_sha_record_and_reconciles_review(center, monkeypatch):
    client, _ = center
    asset = upload(client, b'xlsx original fake', 'provider.xlsx')
    doc = {'import_id':'import-one','sha256':asset['id'],'publish_status':'completed','dataset_id':'dataset-one',
           'summary':{'needs_review':1},'updated_at':'2026-10-12T00:00:00Z'}

    async def remote(request, provider, method, path, **kwargs):
        assert provider == 'structured'
        if path == '/api/import':
            assert kwargs['files']['file'][1] == b'xlsx original fake'
            return response({'id':'volatile-job','status':'processing'})
        if path.startswith('/api/jobs/'):
            return response(status=404)
        if path == '/api/imports':
            return response({'imports':[{'import_id':'import-one'}]})
        return response(doc)

    monkeypatch.setattr(library, '_remote', remote)
    op=process(client,asset,{'purpose':'structured'})
    recovered=operation(client,op['id'])
    assert recovered['status']=='needs_mapping' and recovered['remote_ids']['dataset_id']=='dataset-one'
    # A confirmation from the existing review UI is reconciled in the center.
    doc['summary']={'confirmed':1};doc['dataset_id']='dataset-two'
    completed=operation(client,op['id'])
    assert completed['status']=='completed' and completed['remote_ids']['dataset_id']=='dataset-two'


def test_structured_lost_job_without_receipt_never_claims_success(center, monkeypatch):
    client,_=center
    asset=upload(client,b'xlsx original fake','provider.xlsx')

    async def remote(request,provider,method,path,**kwargs):
        if path=='/api/import':return response({'id':'gone-job'})
        if path=='/api/imports':return response({'imports':[]})
        return response(status=404)

    monkeypatch.setattr(library,'_remote',remote)
    op=process(client,asset,{'purpose':'structured'})
    assert operation(client,op['id'])['status']=='interrupted'
    assert client.get(f'/api/library/{asset["id"]}/download',headers=headers()).status_code==200


def test_library_manual_filter_total_pagination_and_legacy_discovery(center,monkeypatch):
    client,factory=center
    first=upload(client,b'first','first.txt');second=upload(client,b'second','second.txt')
    token=current_identity.set(Identity('a','a@example.com','A',100,'owner'))
    try:
        with factory() as session:
            preserve_blob(session,b'official response','official.csv',kind='automatic')
            session.commit()
    finally:current_identity.reset(token)
    result=client.get('/api/library?offset=0&limit=1',headers=headers()).json()
    assert result['total']==2 and len(result['assets'])==1 and result['next_offset']==1
    assert client.get('/api/library?source=automatic',headers=headers()).json()['total']==1
    assert client.get('/api/library?source=all',headers=headers()).json()['total']==3

    async def remote(request,provider,method,path,**kwargs):
        if path=='/knowledge-bases':return response({'data':[{'id':'older-kb','name':'older documents'}]})
        if provider=='knowledge':
            assert kwargs['params']=={'page':2,'page_size':25}
            return response({'data':[{'id':'older-document'}],'total':101})
        return response({'datasets':[{'dataset_id':'older-dataset','source_file':'historical.xlsx'}]})

    monkeypatch.setattr(library,'_remote',remote)
    legacy=client.get('/api/library/legacy?kb_id=older-kb&page=2',headers=headers()).json()
    assert legacy['total']==101 and legacy['documents'][0]['id']=='older-document'
    assert legacy['datasets'][0]['dataset_id']=='older-dataset'
    assert client.get('/api/library/indicators',headers=headers()).json()['indicators']


def test_library_bearer_checks_origin_actor_pin_and_raw_integrity(center):
    client,factory=center
    # Production Origin/actor checks are covered with the real scoped middleware
    # in test_knowledge_facts, while this harness checks retained source bytes.
    asset=upload(client)
    token=current_identity.set(Identity('a','a@example.com','A',100,'owner'))
    try:
        with factory() as session:
            artifact_path(session,session.get(SourceArtifact,asset['id'])).write_bytes(b'tampered')
    finally:current_identity.reset(token)
    assert client.get(f'/api/library/{asset["id"]}/download',headers=headers()).status_code==409
    op=process(client,asset,{'purpose':'research'})
    assert operation(client,op['id'])['status']=='failed'


def test_fixed_provider_request_forwards_verified_identity_without_redirects(center,monkeypatch):
    client,_=center
    calls=[]

    def transport(request):
        calls.append(request)
        assert request.headers['Authorization']=='Bearer a:owner'
        assert request.headers['X-Tenant-ID']=='100'
        if request.method == 'GET':
            return response({'data':{'id':'kb','tenant_id':100}})
        assert request.headers['Origin']=='http://testserver'
        return response(status=302)

    original=httpx.AsyncClient

    def http_client(**kwargs):
        assert kwargs['follow_redirects'] is False and kwargs['trust_env'] is False
        return original(**kwargs,transport=httpx.MockTransport(transport))

    monkeypatch.setattr(library.httpx,'AsyncClient',http_client)
    asset=upload(client)
    op=process(client,asset,{'purpose':'knowledge','kb_id':'kb'})
    assert operation(client,op['id'])['status']=='failed'
    assert len(calls)==2 and all(call.url.host=='weknora.internal' for call in calls)


def test_knowledge_failed_or_cancelled_parse_retries_existing_remote_identity(center,monkeypatch):
    client,_=center
    asset=upload(client)
    state={'parse_status':'cancelled'}
    creates=[];reparses=[]

    async def remote(request,provider,method,path,**kwargs):
        if path=='/knowledge-bases/kb':return response({'data':{'id':'kb','tenant_id':100}})
        if method=='POST' and path.endswith('/knowledge/file'):creates.append(path)
        if path.endswith('/reparse'):
            reparses.append(path);state['parse_status']='processing'
        return response({'data':{'id':'k',**state}})

    monkeypatch.setattr(library,'_remote',remote)
    op=process(client,asset,{'purpose':'knowledge','kb_id':'kb'})
    cancelled=operation(client,op['id'])
    assert cancelled['status']=='failed' and cancelled['parse_status']=='cancelled'
    assert client.post('/api/library/operations/'+op['id']+'/retry',headers=headers()).status_code==200
    assert operation(client,op['id'])['status']=='pending'
    assert len(creates)==1 and len(reparses)==1
    state['parse_status']='completed'
    assert operation(client,op['id'])['status']=='completed'


def test_denied_structured_process_keeps_original_private(center,monkeypatch):
    client,_=center
    asset=upload(client,b'xlsx bytes','sheet.xlsx')

    async def remote(*args,**kwargs):return response(status=403)

    monkeypatch.setattr(library,'_remote',remote)
    op=process(client,asset,{'purpose':'structured'})
    assert operation(client,op['id'])['status']=='failed'
    assert client.get('/api/library/'+asset['id'],headers=headers(who='b')).status_code==404
    assert client.get('/api/library/'+asset['id']+'/download',headers=headers()).content==b'xlsx bytes'


def test_independent_provider_outage_preserves_legacy_and_original_visibility(center,monkeypatch):
    client,_=center
    asset=upload(client)

    async def remote(request,provider,method,path,**kwargs):
        if provider=='structured':return response(status=503)
        if path=='/knowledge-bases':return response({'data':[{'id':'kb','name':'existing KB'}]})
        if path=='/knowledge-bases/kb':return response({'data':{'id':'kb','tenant_id':100}})
        return response({'data':{'id':'k','parse_status':'pending'}})

    monkeypatch.setattr(library,'_remote',remote)
    op=process(client,asset,{'purpose':'knowledge','kb_id':'kb'})
    legacy=client.get('/api/library/legacy',headers=headers()).json()
    assert legacy['libraries'][0]['id']=='kb' and legacy['errors']['structured']
    async def unavailable(*args,**kwargs):return response(status=503)
    monkeypatch.setattr(library,'_remote',unavailable)
    listing=client.get('/api/library',headers=headers()).json()
    assert listing['total']==1 and listing['assets'][0]['operations'][0]['status_checked'] is False
    result=client.get('/api/library/'+asset['id'],headers=headers()).json()
    assert result['operations'][0]['status_checked'] is False
    assert client.get('/api/library/'+asset['id']+'/download',headers=headers()).content==b'original evidence'


def test_internal_structured_request_binds_origin_to_configured_host(center,monkeypatch):
    client,_=center
    monkeypatch.setattr(settings,'structured_data_url','http://structured-data:18082')
    calls=[]

    def transport(request):
        calls.append(request)
        assert request.headers['Host']=='structured-data:18082'
        assert request.headers['Authorization']=='Bearer a:owner'
        assert request.headers['X-Tenant-ID']=='100'
        if request.method=='POST':
            assert request.url.path=='/api/import'
            assert request.headers['Origin']=='http://structured-data:18082'
            assert b'exact xlsx bytes' in request.content
            return response({'id':'remote-job','status':'processing'})
        return response({'id':'remote-job','status':'processing'})

    original=httpx.AsyncClient

    def http_client(**kwargs):
        assert kwargs['follow_redirects'] is False and kwargs['trust_env'] is False
        return original(**kwargs,transport=httpx.MockTransport(transport))

    monkeypatch.setattr(library.httpx,'AsyncClient',http_client)
    asset=upload(client,b'exact xlsx bytes','prices.xlsx')
    op=process(client,asset,{'purpose':'structured'})
    result=operation(client,op['id'])
    assert result['status']=='running' and result['remote_ids']['job_id']=='remote-job'
    assert len(calls)==2
    # The separate production scope test exercises the browser Origin boundary;
    # this retired-router component harness only verifies the internal request.
    assert len(calls)==2


def test_library_search_and_kind_filter_before_pagination_without_private_enumeration(center):
    client,_=center
    # Matches older than the first page must remain searchable and pageable.
    for number in range(30):
        upload(client,f'older table {number}'.encode(),f'Older-TIN-{number}.CSV')
    for number in range(26):
        upload(client,f'new document {number}'.encode(),f'new-document-{number}.txt')
    page=client.get('/api/library?search=older-tin&kind=table&limit=25',headers=headers()).json()
    assert page['total']==30 and len(page['assets'])==25 and page['next_offset']==25
    tail=client.get('/api/library?search=OLDER-TIN&kind=table&offset=25&limit=25',headers=headers()).json()
    assert tail['total']==30 and len(tail['assets'])==5 and tail['next_offset'] is None
    assert set(row['id'] for row in page['assets']).isdisjoint(row['id'] for row in tail['assets'])
    documents=client.get('/api/library?kind=document&limit=25',headers=headers()).json()
    assert documents['total']==26 and len(documents['assets'])==25 and documents['next_offset']==25
    assert client.get('/api/library?search=older-tin&kind=document',headers=headers()).json()['total']==0
    for suffix in ('?search=Older-TIN&kind=table','?kind=document','?search=new-document&offset=25'):
        hidden=client.get('/api/library'+suffix,headers=headers(who='b')).json()
        assert hidden['total']==0 and hidden['assets']==[] and hidden['next_offset'] is None
    # Same content can have distinct caller aliases; only the visible caller's
    # filename participates in a search, never another uploader's private name.
    same=upload(client,b'same exact bytes','private-secret-name.pdf')
    other=upload(client,b'same exact bytes','visible-to-b.txt',who='b')
    assert same['id']==other['id']
    assert client.get('/api/library?search=private-secret',headers=headers(who='b')).json()['total']==0
    assert client.get('/api/library?search=VISIBLE-TO-B&kind=document',headers=headers(who='b')).json()['total']==1
    assert client.get('/api/library',params={'search':'x'*201},headers=headers()).status_code==422


def test_known_research_table_mapping_requires_confirmation_for_historical_revision(center):
    client,factory=center
    original=upload(client,b'date,[SMM.SN.spot.1]\n2026-09-18,100\n','provider.csv')
    first=process(client,original,{'purpose':'research'})
    process(client,original,{'purpose':'research','operation_id':first['id'],'confirm':True})
    changed=upload(client,b'date,[SMM.SN.spot.1]\n2026-09-18,110\n','provider.csv')
    second=process(client,changed,{'purpose':'research'})
    preview=operation(client,second['id'])
    assert preview['status']=='needs_mapping'
    assert preview['result']['known_mapping'] and preview['result']['revision_count']==1
    token=current_identity.set(Identity('a','a@example.com','A',100,'owner'))
    try:
        with factory() as session:
            rows=session.scalars(select(Observation).where(Observation.series_id=='SMM.SN.spot.1').order_by(Observation.revision)).all()
            assert [row.value for row in rows]==[100]
    finally:current_identity.reset(token)
    process(client,changed,{'purpose':'research','operation_id':second['id'],'confirm':True,
        'selected_row_keys':[row['row_key'] for row in preview['result']['rows']]})
    assert operation(client,second['id'])['status']=='completed'
    token=current_identity.set(Identity('a','a@example.com','A',100,'owner'))
    try:
        with factory() as session:
            rows=session.scalars(select(Observation).where(Observation.series_id=='SMM.SN.spot.1').order_by(Observation.revision)).all()
            assert [row.value for row in rows]==[100,110] and [row.revision for row in rows]==[0,1]
    finally:current_identity.reset(token)


@pytest.mark.parametrize('previous_value',[None,99])
def test_confirm_keeps_original_preview_and_blocks_concurrent_database_change(center,previous_value):
    client,factory=center
    def insert(value):
        token=current_identity.set(Identity('b','b@example.com','B',100,'contributor'))
        try:
            with factory() as session:
                record(session,ObservationIn(series_id='SMM.SN.spot.1',value=value,
                    as_of=datetime(2026,9,18,11,30,tzinfo=SHANGHAI),
                    caliber=Caliber(time_type='发布时点',price_type='均价',spot_source='SMM'),
                    source='人工',entered_by='b',note='independent concurrent entry'))
                session.commit()
        finally:current_identity.reset(token)
    if previous_value is not None:insert(previous_value)
    asset=upload(client,b'date,[SMM.SN.spot.1]\n2026-09-18,100\n','provider.csv')
    op=process(client,asset,{'purpose':'research'})
    first=operation(client,op['id'])
    assert first['status']=='needs_mapping'
    assert first['result']['rows'][0]['category']==('ready' if previous_value is None else 'revision')
    insert(111)
    process(client,asset,{'purpose':'research','operation_id':op['id'],'confirm':True,
        'selected_row_keys':[row['row_key'] for row in first['result']['rows']]})
    blocked=operation(client,op['id'])
    assert blocked['status']=='failed' and blocked['result']['import_result']['committed']==0
    assert blocked['result']['preview_id']==first['result']['preview_id']
    assert blocked['result']['import_result']['stale_rows'][0]['old_value']==111
    token=current_identity.set(Identity('a','a@example.com','A',100,'owner'))
    try:
        with factory() as session:
            values=[row.value for row in session.scalars(select(Observation).order_by(Observation.revision))]
            assert values==([111] if previous_value is None else [99,111])
    finally:current_identity.reset(token)
    assert client.post('/api/library/operations/'+op['id']+'/retry',headers=headers()).status_code==200
    renewed=operation(client,op['id'])
    assert renewed['status']=='needs_mapping' and renewed['request']['confirm'] is False
    assert renewed['result']['preview_id']!=first['result']['preview_id']
    assert renewed['result']['rows'][0]['old_value']==111
    process(client,asset,{'purpose':'research','operation_id':op['id'],'confirm':True,
        'selected_row_keys':[row['row_key'] for row in renewed['result']['rows']]})
    assert operation(client,op['id'])['status']=='completed'
