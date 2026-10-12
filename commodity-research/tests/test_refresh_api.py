"""Workspace API exposes actual controls and enforces the verified member role."""
import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient
from tin.integration.identity import Identity,current_identity
from tin.web import refresh
from tin.refresh.service import refresh_state


@pytest.fixture
def client(session,monkeypatch,tmp_path):
    from tin.config import settings
    monkeypatch.setattr(settings,"data_dir",tmp_path)
    session.info.update(tenant_id=55)
    monkeypatch.setattr(refresh,"SessionLocal",lambda:session)
    dispatched=[]
    monkeypatch.setattr(refresh.scheduler,"submit",lambda tenant,run:dispatched.append((tenant,run)))
    app=FastAPI();app.include_router(refresh.router)
    return TestClient(app),dispatched


def test_controls_run_enabled_sources_once_with_verified_actor(client,session):
    api,dispatched=client
    marker=current_identity.set(Identity("verified","u@test","U",55,"contributor"))
    try:
        state=api.get('/api/sn/refresh').json()
        assert state['enabled'] and state['times']==['08:00','16:30']
        saved=api.put('/api/sn/refresh/settings',json={'enabled':False,'times':['10:15'],'sources':['shfe_quotes']})
        assert saved.status_code==200 and saved.json()['next_run_at'] is None
        assert api.post('/api/sn/refresh/run',json={'tenant_id':999,'actor':'spoof'}).status_code==422
        result=api.post('/api/sn/refresh/run')
        assert result.status_code==202
        second=api.post('/api/sn/refresh/run')
        assert second.json()['run_id']==result.json()['run_id'] and not second.json()['created']
        state=refresh_state(session)
        assert state['active']['actor']=='verified'
        assert all(tenant==55 for tenant,run in dispatched)
        assert state['active']['status']=='pending'
    finally: current_identity.reset(marker)


def test_viewer_can_read_state_but_cannot_change_plan_or_start(client):
    api,_=client
    marker=current_identity.set(Identity('viewer','v@test','V',55,'viewer'))
    try:
        assert api.get('/api/sn/refresh').status_code==200
        assert api.post('/api/sn/refresh/run').status_code==403
        assert api.put('/api/sn/refresh/settings',json={'enabled':False,'times':['08:00'],'sources':['shfe_quotes']}).status_code==403
    finally: current_identity.reset(marker)


def test_unknown_sources_and_invalid_times_never_dispatch(client):
    api,dispatched=client
    for payload in ({'enabled':True,'times':['99:20'],'sources':['shfe_quotes']},
                    {'enabled':True,'times':['08:00'],'sources':['http://foreign.test']}):
        assert api.put('/api/sn/refresh/settings',json=payload).status_code==400
    assert dispatched==[]



def test_one_source_retry_does_not_change_plan_or_replace_active_run(client,session):
    api,dispatched=client
    marker=current_identity.set(Identity('owner','o@test','Owner',55,'owner'))
    try:
        original=api.get('/api/sn/refresh').json()
        retry=api.post('/api/sn/refresh/run',json={'sources':['cfets_fx']})
        assert retry.status_code==202
        assert refresh_state(session)['active']['results']==[]
        from tin.models.refresh import RefreshRun
        active=session.get(RefreshRun,retry.json()['run_id'])
        assert active.sources==['cfets_fx']
        next_source=api.post('/api/sn/refresh/run',json={'sources':['shfe_quotes']})
        assert next_source.json()['run_id']==retry.json()['run_id'] and not next_source.json()['created']
        assert active.sources==['cfets_fx']
        state=api.get('/api/sn/refresh').json()
        assert state['times']==original['times'] and state['enabled']==original['enabled']
        assert [row['id'] for row in state['sources'] if row['enabled']]==[row['id'] for row in original['sources'] if row['enabled']]
        assert state['next_run_at']==original['next_run_at']
    finally: current_identity.reset(marker)


def test_single_source_viewer_and_unknown_sources_are_rejected(client):
    api,dispatched=client
    assert api.post('/api/sn/refresh/run',json={'sources':['foreign_url']}).status_code==400
    assert api.post('/api/sn/refresh/run',json={'sources':['shfe_quotes','shfe_quotes']}).status_code==400
    assert api.post('/api/sn/refresh/run',json={'sources':[]}).status_code==422
    marker=current_identity.set(Identity('viewer','v@test','V',55,'viewer'))
    try:
        assert api.post('/api/sn/refresh/run',json={'sources':['shfe_quotes']}).status_code==403
    finally: current_identity.reset(marker)
    assert dispatched==[]
