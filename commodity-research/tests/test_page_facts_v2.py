"""Bounded v2 projections never attach archived facts or load an entire source."""
from dataclasses import replace
from datetime import date
import asyncio

from fastapi import HTTPException
import pytest
from sqlalchemy import select,func,text
from sqlalchemy.exc import DatabaseError

from tin.integration import kb_facts,page_facts,workbench_scope
from tin.integration.identity import current_identity
from tin.models import Indicator
from tests.test_workbench_sources_v2 import IDENTITY,WB,source


def test_empty_v2_session_masks_legacy_tables_without_any_full_facts_request(tmp_path,monkeypatch):
    monkeypatch.setattr(workbench_scope.settings,'data_dir',tmp_path)
    scope=workbench_scope.WorkbenchScope(IDENTITY,WB,'',0,{'workspace_mode':'isolated','scope_version':2,'can_edit':True},
        'Bearer synthetic',scope_version=2,sources_revision=2)
    def forbidden(*args,**kwargs):raise AssertionError('empty page attempted full or network facts')
    monkeypatch.setattr(kb_facts,'fact_pages',forbidden)
    monkeypatch.setattr(page_facts,'_request',forbidden)
    marker=current_identity.set(IDENTITY)
    try:
        with page_facts.page_read_scope('home'),workbench_scope.workbench_session(scope) as session:
            assert session.scalar(select(func.count()).select_from(Indicator))==0
            assert session.info['knowledge_facts_bounded']
            with pytest.raises(DatabaseError,match='prohibited|not authorized'):session.execute(text('SELECT count(*) FROM main.observations'))
    finally:current_identity.reset(marker)


def test_page_scope_nested_defaults_preserve_explicit_ids_date_and_window():
    identifier='kb-'+'a'*48
    with page_facts.page_read_scope('indicators',series_ids=[identifier],day=date(2026,1,2),points_per_series=120):
        with page_facts.page_read_scope('indicators'):
            request=page_facts._page.get()
            assert request['series_ids']==[identifier] and request['day']=='2026-01-02' and request['points']==120
    assert page_facts._page.get() is None


def test_saved_rules_only_extend_their_own_series_and_include_change_baseline(monkeypatch):
    from types import SimpleNamespace
    from tin.judgments import service
    descriptive,short,long=['kb-'+letter*48 for letter in ('a','b','c')]
    payload=SimpleNamespace(falsifiers=[
        SimpleNamespace(series_id=short,expression='value >= 10',consecutive_periods=3),
        SimpleNamespace(series_id=long,expression='change > 0',consecutive_periods=120)])
    monkeypatch.setattr(service,'_refs',lambda value:[('whitelist',sid,sid) for sid in (descriptive,short,long)])
    assert page_facts._judgment_windows(payload)=={descriptive:2,short:3,long:121}


def test_page_calendar_merges_real_dates_across_distinct_saved_plans():
    calendar=page_facts._merge_calendars([
        {'as_of':'2026-01-03','min_date':'2026-01-01','max_date':'2026-01-05','previous_date':'2026-01-01',
            'next_date':'2026-01-05','latest_previous_date':'2026-01-03'},
        {'as_of':'2026-01-03','min_date':'2026-01-02','max_date':'2026-01-04','previous_date':'2026-01-02',
            'next_date':'2026-01-04','latest_previous_date':'2026-01-03'}])
    assert calendar=={'as_of':'2026-01-03','min_date':'2026-01-01','max_date':'2026-01-05',
        'previous_date':'2026-01-02','next_date':'2026-01-04','latest_previous_date':'2026-01-04'}


def test_retained_pre_knowledge_binding_is_missing_instead_of_reading_legacy_facts(monkeypatch):
    scope=workbench_scope.WorkbenchScope(IDENTITY,WB,'',0,{'scope_version':2},'Bearer synthetic',
        scope_version=2,sources_revision=7)
    def request(scope,body):
        assert body['series_ids']==[] and body['source_bindings']==[]
        return {'bounded':True,'version':'a'*64,'rows':[],'indicators':[],'unavailable_series_ids':[]}
    monkeypatch.setattr(page_facts,'_request',request)
    result=page_facts.bounded_facts(scope,['archived-before-native-binding'],'judgments')
    assert result['unavailable_series_ids']==['archived-before-native-binding'] and result['rows']==[]


def test_different_necessary_windows_cannot_mix_changed_source_snapshots(monkeypatch):
    scope=workbench_scope.WorkbenchScope(IDENTITY,WB,'',0,{'scope_version':2},'Bearer synthetic',
        scope_version=2,sources_revision=7)
    plans=[{'ids':['kb-'+'a'*48],'points':2},{'ids':['kb-'+'b'*48],'points':3}]
    monkeypatch.setattr(page_facts,'_saved_plan',lambda scope,request:(plans,{}))
    responses=iter([{'version':letter*64,'source_versions':{'binding-a':letter*64},'rows':[],
        'indicators':[],'unavailable_series_ids':[]} for letter in ('a','b')])
    monkeypatch.setattr(page_facts,'bounded_facts',lambda *args,**kwargs:next(responses))
    with pytest.raises(HTTPException) as error:page_facts.projected_page_session(None,scope)
    assert error.value.status_code==409


def test_explicit_catalog_and_chat_never_include_delegated_sources(monkeypatch):
    direct,delegated=source(1),source(2,True)
    scope=workbench_scope.WorkbenchScope(IDENTITY,WB,'',0,{'scope_version':2},'Bearer synthetic',
        scope_version=2,sources_revision=7,sources=(direct,delegated))
    captured=[]
    def request(scope,body):
        captured.append(body)
        return {'catalog_only':True,'version':'a'*64,'series':[],'datasets':[],
            'offset':0,'total':0,'next_offset':None,'truncated':False}
    monkeypatch.setattr(page_facts,'_request',request)
    result=page_facts.catalog_page(scope)
    assert result['total']==0 and captured[0]['source_bindings']==[direct['id']]


@pytest.mark.parametrize('invalid',['kb-wrong',['*'],['kb-'+'a'*48]*2])
def test_invalid_explicit_page_ids_do_not_become_an_all_source_scan(invalid):
    with pytest.raises(HTTPException):
        with page_facts.page_read_scope('indicators',series_ids=invalid):pass


def test_readable_center_does_not_expand_private_source_page_consent(monkeypatch):
    private,center=source(1,True),source(2)
    private['view_modules']=['diagram']
    center.update(source_type='central_dataset',kind='macro')
    document={'success':True,'data':{'scope_version':2,'sources_revision':7,'id':WB,
        'user_id':IDENTITY.user_id,'scope_tenant_id':17,'knowledge_base_id':private['knowledge_base_id'],
        'sources':[private,center],'unavailable_sources':[],'view_modules':['diagram']}}
    async def native(scope,path,**kwargs):
        if path.endswith('/source-scope'):return document
        return {'success':True,'data':{'id':WB,'scope_tenant_id':17,'workspace_mode':'isolated',
            'scope_version':2,'can_read_source':False,'knowledge_base_id':private['knowledge_base_id']}}
    monkeypatch.setattr(workbench_scope,'go_request',native)
    scope=asyncio.run(workbench_scope.resolve_workbench(IDENTITY,WB,'Bearer synthetic'))
    assert scope.restricted and scope.view_modules==('diagram',)
    assert not workbench_scope.source_scope_for_binding(scope,center['id'],direct_only=True).restricted
    from tin.web import restricted_views
    from tin.web.app import templates
    def forbidden(*args,**kwargs):raise AssertionError('unapproved page loaded source/owner records')
    monkeypatch.setattr(restricted_views,'page_data',forbidden)
    monkeypatch.setattr(templates,'TemplateResponse',lambda request,name,context:context)
    from starlette.requests import Request
    request=Request({'type':'http','method':'GET','path':'/sn','headers':[],'query_string':b''})
    result=asyncio.run(restricted_views.restricted_response(request,scope,'/sn'))
    assert result['restricted_authorized'] is False
