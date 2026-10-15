"""A report upload is a real, scoped native document, never an invented success."""
from contextlib import contextmanager
from datetime import datetime, timezone
import hashlib
import json
from email import policy
from email.parser import BytesParser

import httpx
import pytest
from fastapi.testclient import TestClient
from sqlalchemy.orm import sessionmaker
from sqlalchemy.pool import StaticPool

from tin.config import settings
from tin.db import Base, make_engine
from tin.integration.workbench_scope import current_workbench
from tin.models import DailyBrief, Researcher
from tin.web.app import app

WB = 'a0000000-0000-4000-8000-000000000001'
OTHER_WB = 'a0000000-0000-4000-8000-000000000002'
KB = 'b0000000-0000-4000-8000-000000000001'
OTHER_KB = 'b0000000-0000-4000-8000-000000000002'


def endpoint(suffix, workbench=WB):
    return f'/research/workbenches/{workbench}/api/sn/{suffix}'


@pytest.fixture
def export_client(monkeypatch, tmp_path):
    from tin.web import app as module
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://native-export.test')
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    factories = {}
    for identifier in (WB, OTHER_WB):
        engine = make_engine('sqlite://', poolclass=StaticPool, connect_args={'check_same_thread': False})
        Base.metadata.create_all(engine)
        factories[identifier] = sessionmaker(engine, expire_on_commit=False)
        with factories[identifier]() as session:
            session.add_all([Researcher(id=1, username='writer', weknora_user_id='writer', display_name='Researcher', varieties=[], created_at=datetime.now(timezone.utc)),
                             Researcher(id=2, username='someone', weknora_user_id='someone', display_name='Other', varieties=[], created_at=datetime.now(timezone.utc))])
            session.flush()
            if identifier == WB:
                session.add_all([DailyBrief(id=1, researcher_id=1, variety=settings.variety, trade_date='2026-02-03',
                    status='草稿', tone='区间震荡', stance='观望', summary='隔离的日报正文',
                    claims=[{'text':'明确事实', 'evidence':[{'series_id':'kb-'+'a'*48,'value':0,'as_of':'2026-02-03'}]}],
                    unverified=[{'text':'待核对说明','problems':['来源尚待确认']}], generated_at=datetime(2026,2,3,tzinfo=timezone.utc)),
                    DailyBrief(id=2, researcher_id=2, variety=settings.variety, trade_date='2026-02-03',
                    status='已采纳', summary='其他研究员私有日报', generated_at=datetime(2026,2,3,tzinfo=timezone.utc))])
            session.commit()

    @contextmanager
    def scoped_session():
        scope = current_workbench.get()
        assert scope is not None
        with factories[scope.workbench_id]() as session:
            session.info.update(scope.identity.session_info(), workbench_id=scope.workbench_id,
                                knowledge_base_id=scope.knowledge_base_id,
                                knowledge_base_tenant_id=scope.knowledge_base_tenant_id)
            yield session
    monkeypatch.setattr(module, 'SessionLocal', scoped_session)
    state = {'calls': [], 'uploads': [], 'documents': {}, 'revoked': False,
             'permission': 'editor', 'upload_status': 200, 'upload_wrong_kb': False,
             'bad_folder_tree': False, 'detail_wrong_hash': False, 'detail_wrong_folder': False,
             'upload_malformed': False, 'parse_status': 'pending',
             'inventory_truncated': False, 'inventory_total_changes': False, 'inventory_duplicate_page': False,
             'folders': [{'path':'日报','name':'日报','children':[{'path':'日报/归档','name':'归档','children':[]}]}],
             'factories': factories}

    def native(request):
        state['calls'].append((request.method, request.url.path))
        assert request.url.host == 'native-export.test', 'No model or external service may be contacted'
        assert request.headers['x-tenant-id'] == '17'
        token = request.headers.get('authorization','').removeprefix('Bearer ')
        if token not in {'writer','viewer'}:
            return httpx.Response(401, json={'success':False})
        if request.url.path == '/api/v1/auth/me':
            return httpx.Response(200, json={'success':True,'data':{'user':{'id':token,'email':token+'@test'},
                'tenant':{'id':17},'memberships':[{'tenant_id':17,'role':'viewer' if token=='viewer' else 'contributor'}]}})
        if request.url.path.startswith('/api/v1/workspaces/research/'):
            identifier = request.url.path.rsplit('/',1)[-1]
            if identifier not in factories:
                return httpx.Response(404, json={'success':False})
            return httpx.Response(200,json={'success':True,'data':{'id':identifier,'name':'合成研究台',
                'knowledge_base_id':KB,'scope_tenant_id':17,'workspace_mode':'isolated'}})
        if request.url.path == f'/api/v1/knowledge-bases/{KB}':
            return httpx.Response(403 if state['revoked'] else 200,json={'success':True,
                'data':{'id':KB,'tenant_id':23,'my_permission':state['permission']}})
        if request.url.path == f'/api/v1/knowledge-bases/{KB}/knowledge/folders':
            return httpx.Response(200,json={'success':True,'data':{'folders':
                'invalid' if state['bad_folder_tree'] else state['folders'], 'root_document_count':0,'total_document_count':0}})
        if request.url.path == f'/api/v1/knowledge-bases/{KB}/knowledge':
            assert request.method == 'GET' and request.url.params['file_type'] == 'md'
            assert 'folder_path' in request.url.params and request.url.params['keyword'].endswith('.md')
            rows = [row for row in state['documents'].values()
                    if request.url.params['keyword'] in row['file_name'] and row['folder_path'] == request.url.params['folder_path']]
            page, size = int(request.url.params['page']), int(request.url.params['page_size'])
            total = len(rows)
            selected = rows[(page-1)*size:page*size]
            if state['inventory_truncated']: total += 200
            if state['inventory_total_changes'] and page > 1: total += 1
            if state['inventory_duplicate_page'] and page > 1: selected = rows[:len(selected)]
            return httpx.Response(200,json={'success':True,'data':selected,'total':total,'page':page,'page_size':size})
        if request.url.path == f'/api/v1/knowledge-bases/{KB}/knowledge/file':
            assert request.method == 'POST'
            if state['upload_malformed']:
                return httpx.Response(200,json=[])
            message = BytesParser(policy=policy.default).parsebytes(('Content-Type: '+request.headers['content-type']+'\r\n\r\n').encode()+request.content)
            fields = {part.get_param('name', header='content-disposition'):part.get_payload(decode=True) for part in message.iter_parts()}
            state['uploads'].append(fields)
            assert fields['file'].startswith(b'---\n')
            assert 'process_config' not in fields, 'Normal Markdown parsing must remain enabled'
            digest = hashlib.md5(fields['file']).hexdigest()
            existing = next((row for row in state['documents'].values() if row['file_hash']==digest and row['parse_status'] not in {'failed','deleting'}),None)
            if existing:
                return httpx.Response(409,json={'success':False,'code':'duplicate_file','data':existing})
            if state['upload_status'] != 200:
                return httpx.Response(state['upload_status'],json={'success':False,'detail':'native upload failed'})
            filename = fields['fileName'].decode()
            folder, _, name = filename.rpartition('/')
            row = {'id':f'export-real-{len(state["documents"])+1}', 'knowledge_base_id':OTHER_KB if state['upload_wrong_kb'] else KB,
                'tenant_id':23,'file_hash':digest,'file_name':name,'folder_path':folder,'parse_status':state['parse_status'],'file_type':'md'}
            state['documents'][row['id']] = row
            return httpx.Response(200,json={'success':True,'data':row})
        if request.url.path.startswith('/api/v1/knowledge/'):
            identifier = request.url.path.rsplit('/',1)[-1]
            row = state['documents'].get(identifier)
            if row is None:
                return httpx.Response(404,json={'success':False})
            row = dict(row)
            if state['detail_wrong_hash']: row['file_hash']='0'*32
            if state['detail_wrong_folder']: row['folder_path']='unexpected'
            return httpx.Response(200,json={'success':True,'data':row})
        return httpx.Response(404,json={'success':False,'detail':'fixture route absent'})
    original = httpx.AsyncClient
    monkeypatch.setattr(httpx, 'AsyncClient', lambda **kwargs: original(transport=httpx.MockTransport(native), **kwargs))
    headers = {'Authorization':'Bearer writer','X-Tenant-ID':'17','Origin':'http://testserver'}
    with TestClient(app) as client:
        yield client, headers, state
    for factory in factories.values(): factory.kw['bind'].dispose()


def test_daily_brief_exports_to_real_selected_folder_and_reuses_same_document(export_client):
    client, headers, state = export_client
    folders = client.get(endpoint('brief/export/folders'),headers=headers)
    assert folders.status_code == 200, folders.text
    assert folders.json()['knowledge_base_id'] == KB
    result = client.post(endpoint('brief/1/export'), headers=headers, json={'folder_path':'日报/归档'})
    assert result.status_code == 200, result.text
    payload = result.json()
    assert payload['knowledge_id'] == 'export-real-1'
    assert payload['folder_path'] == '日报/归档'
    assert payload['parse_status'] == 'pending'
    assert payload['reused'] is False
    content = state['uploads'][0]['file'].decode()
    assert '隔离的日报正文' in content and '草稿' in content and '未经研究员确认' in content
    assert '待核对说明' in content and '来源尚待确认' in content and '"value": 0' in content
    repeated = client.post(endpoint('brief/1/export'), headers=headers, json={'folder_path':'日报/归档'})
    assert repeated.status_code == 200, repeated.text
    assert repeated.json()['knowledge_id'] == payload['knowledge_id'] and repeated.json()['reused'] is True
    assert len(state['documents']) == 1


@pytest.mark.parametrize('body', [{}, {'folder_path':''}])
def test_root_folder_is_default_and_real_knowledge_link_opens_its_id(export_client, body):
    client, headers, state = export_client
    result = client.post(endpoint('brief/1/export'),headers=headers,json=body)
    assert result.status_code == 200, result.text
    row = result.json()
    assert row['folder_path'] == '' and row['knowledge_id'] in state['documents']
    assert 'knowledge_id=' + row['knowledge_id'] in row['url']
    assert '/platform/knowledge-bases/' + KB in row['url']
    assert '/' not in state['uploads'][0]['fileName'].decode()


def test_changed_content_or_destination_creates_explicit_new_markdown_version(export_client):
    client, headers, state = export_client
    first = client.post(endpoint('brief/1/export'),headers=headers,json={}).json()
    second = client.post(endpoint('brief/1/export'),headers=headers,json={'folder_path':'日报'}).json()
    with state['factories'][WB]() as session:
        session.get(DailyBrief,1).summary = '人工修改后的日报正文'
        session.commit()
    third = client.post(endpoint('brief/1/export'),headers=headers,json={}).json()
    assert len({first['knowledge_id'],second['knowledge_id'],third['knowledge_id']}) == 3
    assert first['file_name'] != third['file_name']
    assert '人工修改后的日报正文' in state['uploads'][-1]['file'].decode()
    assert '隔离的日报正文' in state['uploads'][0]['file'].decode()
    assert len(state['documents']) == 3


@pytest.mark.parametrize('folder', ['..','../日报','日报/../其他','/日报','日报/','日报//归档','日报\\归档','日报\x00','不存在'])
def test_invalid_or_missing_folder_never_uploads(export_client, folder):
    client, headers, state = export_client
    response = client.post(endpoint('brief/1/export'),headers=headers,json={'folder_path':folder})
    assert response.status_code == 400, response.text
    assert not state['uploads']


@pytest.mark.parametrize('body', [{'folder_path':1},{'knowledge_base_id':OTHER_KB},{'actor':'someone'},{'filename':'../escape.md'}])
def test_extra_scope_or_path_controls_cannot_be_injected(export_client, body):
    client, headers, state = export_client
    assert client.post(endpoint('brief/1/export'),headers=headers,json=body).status_code == 422
    assert not state['uploads']


@pytest.mark.parametrize('brief_id,workbench', [(2,WB),(999,WB),(1,OTHER_WB)])
def test_private_missing_or_other_workbench_report_is_not_exported(export_client, brief_id, workbench):
    client, headers, state = export_client
    result = client.post(endpoint(f'brief/{brief_id}/export',workbench),headers=headers,json={})
    assert result.status_code == 404, result.text
    assert not state['uploads']


@pytest.mark.parametrize('mode', ['viewer','shared_viewer','revoked','csrf','unauthenticated'])
def test_current_authorization_is_required_for_every_export(export_client, mode):
    client, headers, state = export_client
    headers = dict(headers)
    if mode == 'viewer': headers['Authorization'] = 'Bearer viewer'
    if mode == 'shared_viewer': state['permission'] = 'viewer'
    if mode == 'revoked': state['revoked'] = True
    if mode == 'csrf': headers['Origin'] = 'http://untrusted.test'
    if mode == 'unauthenticated': headers = {'Origin':'http://testserver'}
    result = client.post(endpoint('brief/1/export'),headers=headers,json={})
    assert result.status_code in {401,403}, result.text
    assert not state['uploads']


@pytest.mark.parametrize('status', [403,413,500])
def test_native_upload_failure_is_not_reported_as_success(export_client, status):
    client, headers, state = export_client
    state['upload_status'] = status
    response = client.post(endpoint('brief/1/export'),headers=headers,json={})
    assert response.status_code == (status if status in {403,413} else 503), response.text
    assert not state['documents']


@pytest.mark.parametrize('flag', ['upload_wrong_kb','detail_wrong_hash','detail_wrong_folder'])
def test_receipt_and_actual_document_must_both_match_scope_content_and_folder(export_client, flag):
    client, headers, state = export_client
    state[flag] = True
    result = client.post(endpoint('brief/1/export'),headers=headers,json={})
    assert result.status_code == 409, result.text
    assert 'knowledge_id' not in result.json()


def test_deleted_target_folder_requires_reselection_before_saving(export_client):
    client, headers, state = export_client
    assert client.get(endpoint('brief/export/folders'),headers=headers).status_code == 200
    state['folders'] = []
    result = client.post(endpoint('brief/1/export'),headers=headers,json={'folder_path':'日报/归档'})
    assert result.status_code == 400, result.text
    assert not state['uploads']


def test_invalid_native_folder_tree_is_visible_error_not_synthetic_root_success(export_client):
    client, headers, state = export_client
    state['bad_folder_tree'] = True
    result = client.get(endpoint('brief/export/folders'),headers=headers)
    assert result.status_code == 503, result.text
    assert not state['uploads']


def test_unscoped_export_cannot_use_legacy_tenant_database(export_client):
    client, headers, state = export_client
    response = client.post('/research/api/sn/brief/1/export',headers=headers,json={})
    assert response.status_code in {401,409}, response.text
    assert not state['uploads']


def test_snapshot_export_matches_actual_current_source_rows_and_ignores_render_clock(export_client):
    from tin.config import SHANGHAI
    from tin.models import Indicator, Observation
    client, headers, state = export_client
    field = 'kb-'+'a'*48
    with state['factories'][WB]() as session:
        session.add(Indicator(series_id=field,name='真实合成字段',variety=settings.variety,
            category='原表',caliber={},unit='件',source='knowledge',frequency='日',fetch_mode='structured'))
        session.flush()
        session.add(Observation(series_id=field,value=0,as_of=datetime(2026,2,3,15,tzinfo=SHANGHAI),
            fetched_at=datetime(2026,2,3,16,tzinfo=SHANGHAI),caliber_snapshot={},source='knowledge',revision=0))
        session.commit()
    first = client.post(endpoint('reports/2026-02-03/export'),headers=headers,json={'folder_path':'日报'})
    assert first.status_code == 200, first.text
    content = state['uploads'][0]['file'].decode()
    assert field in content and '"value": 0.0' in content and '当日数据与研判' in content
    assert '"generated_at"' not in content
    second = client.post(endpoint('reports/2026-02-03/export'),headers=headers,json={'folder_path':'日报'})
    assert second.status_code == 200, second.text
    assert second.json()['knowledge_id'] == first.json()['knowledge_id'] and second.json()['reused'] is True
    response = client.post(endpoint('reports/2026-02-04/export'),headers=headers,json={})
    assert response.status_code == 404
    page = client.get('/research/workbenches/'+WB+'/sn/reports',headers=headers)
    assert page.status_code == 200, page.text
    assert '导出到知识库' in page.text and 'data-export-kind="snapshot"' in page.text
    assert '日报导出文件夹' in page.text
    home = client.get('/research/workbenches/'+WB+'/',headers=headers)
    assert home.status_code == 200, home.text
    assert 'data-export-kind="brief"' in home.text and 'data-export-id="1"' in home.text
    assert 'brief-export.js' in home.text


def test_native_malformed_receipt_is_not_success(export_client):
    client, headers, state = export_client
    state['upload_malformed'] = True
    result = client.post(endpoint('brief/1/export'),headers=headers,json={})
    assert result.status_code == 503, result.text
    assert not state['documents']


def test_native_saved_but_parse_failed_retains_real_recovery_link_without_fake_success(export_client):
    client, headers, state = export_client
    state['parse_status'] = 'failed'
    result = client.post(endpoint('brief/1/export'),headers=headers,json={})
    assert result.status_code == 502, result.text
    detail = result.json()['detail']
    assert detail['knowledge_id'] == 'export-real-1'
    assert detail['knowledge_id'] in state['documents'] and '解析失败' in detail['message']
    repeated = client.post(endpoint('brief/1/export'),headers=headers,json={})
    assert repeated.status_code == 502 and repeated.json()['detail']['knowledge_id'] == detail['knowledge_id']
    assert len(state['uploads']) == len(state['documents']) == 1, 'Failed native parse retries must use the original document rather than pile up uploads'


def test_invalid_persisted_report_date_cannot_become_upload_path(export_client):
    client, headers, state = export_client
    with state['factories'][WB]() as session:
        session.get(DailyBrief,1).trade_date = '../escape'
        session.commit()
    result = client.post(endpoint('brief/1/export'),headers=headers,json={})
    assert result.status_code == 409, result.text
    assert not state['uploads']


@pytest.mark.parametrize('attempt', range(8))
def test_concurrent_repeated_export_creates_one_real_document(export_client, attempt):
    from concurrent.futures import ThreadPoolExecutor
    client, headers, state = export_client
    def submit():
        return client.post(endpoint('brief/1/export'),headers=headers,json={'folder_path':'日报/归档'})
    with ThreadPoolExecutor(max_workers=2) as pool:
        results = list(pool.map(lambda _: submit(), range(2)))
    assert all(response.status_code == 200 for response in results), [response.text for response in results]
    assert len({response.json()['knowledge_id'] for response in results}) == 1, [fields['file'].decode() for fields in state['uploads']]
    assert len(state['documents']) == 1
    assert sorted(response.json()['reused'] for response in results) == [False, True]


def _many_filename_candidates(client, headers, state):
    response = client.post(endpoint('brief/1/export'),headers=headers,json={})
    assert response.status_code == 200, response.text
    actual = state['documents'].pop(response.json()['knowledge_id'])
    state['documents'].update({f'candidate-{index}':dict(actual,id=f'candidate-{index}',file_hash='0'*32)
                               for index in range(130)})
    state['documents'][actual['id']] = actual
    return actual


def test_complete_native_pagination_and_hash_find_actual_document_beyond_first_page(export_client):
    client, headers, state = export_client
    actual = _many_filename_candidates(client,headers,state)
    repeated = client.post(endpoint('brief/1/export'),headers=headers,json={})
    assert repeated.status_code == 200, repeated.text
    assert repeated.json()['knowledge_id'] == actual['id'] and repeated.json()['reused'] is True
    assert len(state['uploads']) == 1, 'Filename lookalikes do not cause an extra upload or become content truth'
    assert len(state['documents']) == 131


@pytest.mark.parametrize('mode,expected', [('inventory_truncated',503),('inventory_total_changes',409),('inventory_duplicate_page',409)])
def test_incomplete_or_changing_native_inventory_cannot_create_duplicates(export_client, mode, expected):
    client, headers, state = export_client
    _many_filename_candidates(client,headers,state)
    state[mode] = True
    repeated = client.post(endpoint('brief/1/export'),headers=headers,json={})
    assert repeated.status_code == expected, repeated.text
    assert len(state['uploads']) == 1


def test_existing_receipt_is_revalidated_against_actual_native_document(export_client):
    client, headers, state = export_client
    response = client.post(endpoint('brief/1/export'),headers=headers,json={})
    assert response.status_code == 200
    state['detail_wrong_hash'] = True
    repeated = client.post(endpoint('brief/1/export'),headers=headers,json={})
    assert repeated.status_code == 409, repeated.text
    assert len(state['uploads']) == 1


def test_filename_candidate_in_another_knowledge_base_never_grants_reuse(export_client):
    client, headers, state = export_client
    response = client.post(endpoint('brief/1/export'),headers=headers,json={})
    state['documents'][response.json()['knowledge_id']]['knowledge_base_id'] = OTHER_KB
    repeated = client.post(endpoint('brief/1/export'),headers=headers,json={})
    assert repeated.status_code == 409, repeated.text
    assert len(state['uploads']) == 1
