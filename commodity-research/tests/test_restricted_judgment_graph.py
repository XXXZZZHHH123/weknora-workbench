"""Owner-shared research uses the real graph helper without widening source scope."""
from dataclasses import replace
from datetime import datetime
from types import SimpleNamespace
from fastapi import HTTPException

import pytest
from sqlalchemy import select
from tin.config import SHANGHAI
from tin.integration import page_facts, workbench_scope
from tin.integration.identity import Identity
from tin.models import Judgment, Researcher
from tests.test_canonical_research_results import save_results
from tests.test_knowledge_facts import canonical, FIRST, LEGACY, SERIES, KB, page
from tests.test_restricted_views import delegated

PRIVATE = 'kb-' + 'b'*48

def update_record(identifier, user, **changes):
    factory=(workbench_scope.result_session_factory(__import__('tin.db',fromlist=['tenant_database_path']).tenant_database_path(17))
        if identifier==LEGACY else workbench_scope.isolated_session_factory(17,identifier))
    with factory() as session:
        who=select(Researcher.id).where(Researcher.weknora_user_id==user)
        row=session.scalar(select(Judgment).where(Judgment.researcher_id.in_(who)))
        for key,value in changes.items():setattr(row,key,value)
        session.commit();return row.id

@pytest.mark.parametrize('identifier',[FIRST,LEGACY])
def test_shared_graph_has_bound_history_and_falsifier_but_never_other_users_data(canonical,identifier):
    client,headers,state,_=canonical
    save_results(identifier);save_results(identifier,'another-user')
    update_record(identifier,'researcher',contradiction={'bull':{'claim':'Shared research condition',
        'evidence':[{'desc':'Shared stock observation','series_id':SERIES}]},'bear':{'claim':'Alternative'}},
        falsifiers=[{'id':'owner_check','desc':'Owner invalidation','series_id':SERIES,
            'expression':'value >= 10','unit_confirmed':'件','consecutive_periods':1,'side':'bull'}])
    update_record(identifier,'another-user',whitelist=[PRIVATE])
    delegated(state,('judgments',));before=state['pages']
    response=client.get(page(identifier,'/sn/judgment'),headers={**headers,'Authorization':'Bearer source-viewer'})
    assert response.status_code==200,response.text
    assert 'data-judgment-chart' in response.text and 'Shared stock observation' in response.text
    assert 'Owner invalidation' in response.text and '触发' in response.text
    assert 'another-user' not in response.text and PRIVATE not in response.text
    assert 'data-open-logic' not in response.text and 'judgment-manager.js' not in response.text
    assert state['pages']==before
    assert state['bounded_requests'] and all(request['series_ids']==[SERIES] for request in state['bounded_requests'])
    assert all(request['include_invalid'] is True and request['view_module']=='judgments'
        and request['as_of']==datetime.now(SHANGHAI).date().isoformat() for request in state['bounded_requests'])


def test_restricted_v2_plan_uses_only_shared_owner_and_preserves_all_known_missing_periods(canonical,monkeypatch):
    client,headers,state,_=canonical
    save_results(FIRST);save_results(FIRST,'another-user')
    owner=update_record(FIRST,'researcher',falsifiers=[{'id':'owner_streak','desc':'Owner long condition',
        'series_id':SERIES,'expression':'change > 0','unit_confirmed':'件','consecutive_periods':50,'side':'bull'}])
    update_record(FIRST,'another-user',whitelist=[PRIVATE])
    identity=Identity('source-viewer','viewer@fixture.invalid','Visitor',17,'viewer')
    from tests.test_workbench_sources_v2 import source
    delegated_source = source(1, True)
    delegated_source['view_modules'] = ['judgments']
    scope=workbench_scope.WorkbenchScope(identity,FIRST,KB,17,
        {'workspace_mode':'isolated','can_read_source':False,'owner_id':'researcher'},'Bearer source-viewer',
        view_modules=('judgments',),scope_version=2,sources_revision=1,sources=(delegated_source,))
    from tin.web import judgment_panels
    from tin.judgments import service
    monkeypatch.setattr(judgment_panels,'selected_judgment',lambda *args,**kwargs:pytest.fail('Cannot select visitor personal records'))
    monkeypatch.setattr(service,'current',lambda *args,**kwargs:pytest.fail('Cannot use viewer current judgment'))
    plans,_=page_facts._saved_plan(scope,{'module':'judgments','series_ids':None,'judgment_id':owner})
    assert plans==[{'ids':[SERIES],'points':51,'include_invalid':True}]
    with pytest.raises(HTTPException) as error:
        page_facts._saved_plan(scope,{'module':'judgments','series_ids':None,'judgment_id':owner+1})
    assert getattr(error.value,'status_code',None)==404


def test_legacy_missing_origin_and_retired_id_stay_missing_without_trying_other_sources(monkeypatch):
    from tin.web import judgment_charts
    monkeypatch.setattr(judgment_charts,'_legacy_points',lambda *args:pytest.fail('Cannot guess a retired binding origin'))
    scope=SimpleNamespace(knowledge_base_id=KB,knowledge_base_tenant_id=17,additional_knowledge_bases=())
    result=page_facts._legacy_shared_window(scope,{'ids':['legacy.retired',SERIES],'points':24},{},None)
    assert result['rows']==[] and result['indicators']==[]
    assert result['unavailable_series_ids']==['legacy.retired',SERIES]


def test_v2_unrestricted_non_ma_saved_graph_plan_keeps_missing_periods(monkeypatch):
    from tests.test_judgment_research_view import _plan_fixture,saved,A
    scope=_plan_fixture(monkeypatch,saved(whitelist=[A]))
    plans,_=page_facts._saved_plan(scope,{'module':'judgments','series_ids':None})
    assert plans==[{'ids':[A],'points':24,'include_invalid':True}]


def test_shared_nondefault_variety_uses_latest_owner_product_and_same_product_rules(canonical):
    from datetime import timedelta
    client,headers,state,_=canonical
    save_results(FIRST);save_results(FIRST,'another-user')
    update_record(FIRST,'another-user',variety='PRIVATE',whitelist=[PRIVATE])
    with workbench_scope.isolated_session_factory(17,FIRST)() as session:
        owner_ids=select(Researcher.id).where(Researcher.weknora_user_id=='researcher')
        original=session.scalar(select(Judgment).where(Judgment.researcher_id.in_(owner_ids)))
        original.status='生效'
        values={column.key:getattr(original,column.key) for column in Judgment.__table__.columns if column.key!='id'}
        values.update(variety='ALT',version=1,status='草稿',created_at=original.created_at+timedelta(seconds=1),
            contradiction={'bull':{'claim':'Alternate commodity thesis','evidence':[
                {'desc':'Alternate commodity data','series_id':SERIES}]},'bear':{'claim':'Alternate opposing thesis'}},
            thresholds=[{'id':'alt_boundary','name':'Alternate commodity boundary','series_id':SERIES,
                'operator':'<','value':20,'type':'观察位','horizon':'短期'}],
            falsifiers=[{'id':'alt_invalidation','desc':'Alternate commodity invalidation','series_id':SERIES,
                'expression':'value >= 10','unit_confirmed':'件','consecutive_periods':1,'side':'bull'}])
        session.add(Judgment(**values));session.commit()
    delegated(state,('judgments',));before=state['pages']
    response=client.get(page(FIRST,'/sn/judgment'),headers={**headers,'Authorization':'Bearer source-viewer'})
    assert response.status_code==200,response.text
    assert 'Alternate commodity thesis' in response.text and 'Alternate commodity data' in response.text
    assert 'Alternate commodity boundary' in response.text and 'Alternate commodity invalidation' in response.text
    assert 'data-judgment-chart' in response.text and 'data-metric-trend' in response.text
    assert 'researcher original thesis' not in response.text and 'another-user' not in response.text
    assert PRIVATE not in response.text and 'data-open-logic' not in response.text
    assert state['pages']==before and all(request['series_ids']==[SERIES] for request in state['bounded_requests'])
