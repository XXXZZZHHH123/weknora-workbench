"""Independent cache acceptance: current permissions and source facts win."""
from datetime import datetime, timezone
import json
import shutil
import sqlite3

import pytest
from sqlalchemy import select, text
from sqlalchemy.exc import DatabaseError

from tin.config import settings
from tin.integration import kb_facts
from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import (
    WorkbenchScope, isolated_session_factory, workbench_session,
)
from tin.models import DiagramTemplate, Observation
from tests.test_knowledge_facts import (
    FIRST, SECOND, KB, OTHER_KB, SERIES, api, canonical,
)


def _values(client,headers,identifier=FIRST):
    return client.get(api(identifier,'indicators/'+SERIES+'/observations'),headers=headers)


def _cache_files(root):
    return sorted(root.rglob('*.facts.sqlite'))


def _close_readers(root):
    # Simulate process/connection eviction before editing this disposable cache,
    # without altering the current upstream fixture or its version.
    with kb_facts._projection_lock:
        matches=[key for key in kb_facts._projection_engines if str(root) in key]
        engines=[kb_facts._projection_engines.pop(key) for key in matches]
    for engine in engines:
        engine.dispose()


def _scope():
    identity=Identity('researcher','researcher@test','Researcher',17,'owner')
    return WorkbenchScope(identity,FIRST,KB,17,{'workspace_mode':'isolated'},'Bearer source-user')


def test_disk_hit_still_rechecks_permission_version_withdrawal_and_outage(canonical):
    client,headers,state,root=canonical
    initial=_values(client,headers)
    assert initial.status_code==200,initial.text
    assert initial.json()['observations'][0]['value']==17.5
    assert len(_cache_files(root))==1
    pages=state['pages'];before=len(state['calls'])
    _close_readers(root)
    cached=_values(client,headers)
    assert cached.status_code==200 and cached.json()==initial.json()
    assert state['pages']==pages
    calls=state['calls'][before:]
    assert ('GET','/api/v1/workspaces/research/'+FIRST) in calls
    assert ('GET','/api/v1/knowledge-bases/'+KB) in calls
    assert ('GET','/api/v1/knowledge-bases/'+KB+'/knowledge') in calls
    assert ('POST','/api/research/facts') in calls
    state['revoked']=True
    forbidden=_values(client,headers)
    assert forbidden.status_code==403 and '17.5' not in forbidden.text
    assert state['pages']==pages
    state['revoked']=False;state['down']=True
    unavailable=_values(client,headers)
    assert unavailable.status_code==503 and '17.5' not in unavailable.text
    state['down']=False;state['value']=0
    updated=_values(client,headers)
    assert updated.status_code==200,updated.text
    assert updated.json()['observations'][0]['value']==0
    assert state['pages']>pages
    state['ready']=False
    withdrawn=_values(client,headers)
    assert withdrawn.status_code==404 and '17.5' not in withdrawn.text
    assert client.get(api(),headers=headers).json()['total']==0
    state['ready']=True;state['sources']=[]
    assert client.get(api(),headers=headers).json()['total']==0


@pytest.mark.parametrize('damage',[
    'invalid_database','invalid_json','wrong_version','forged_source_metadata',
    'forged_observation','extra_table','extra_view','extra_trigger','invalid_signature',
])
def test_damaged_or_modified_cache_is_rebuilt_from_fresh_sources(canonical,damage):
    client,headers,state,root=canonical
    initial=_values(client,headers)
    assert initial.status_code==200,initial.text
    pages=state['pages'];path,=_cache_files(root)
    _close_readers(root)
    if damage=='invalid_signature':
        signature_path=path.with_name(path.name+'.sig')
        signature=json.loads(signature_path.read_text())
        signature['mac']='0'*64
        signature_path.write_text(json.dumps(signature))
    elif damage=='invalid_database':
        path.write_bytes(b'not a sqlite database')
    else:
        with sqlite3.connect(path) as db:
            if damage=='invalid_json':
                db.execute("UPDATE _kb_cache_meta SET payload='not json'")
            elif damage in {'wrong_version','forged_source_metadata'}:
                document=json.loads(db.execute('SELECT payload FROM _kb_cache_meta').fetchone()[0])
                if damage=='wrong_version':
                    document['info']['knowledge_facts_version']='0'*64
                else:
                    # A valid JSON/type envelope with an unchanged version must
                    # not authorize invented provenance inside the catalog.
                    document['info']['knowledge_facts_catalog'][SERIES]['source_knowledge_id']='forged-source'
                db.execute('UPDATE _kb_cache_meta SET payload=?',(json.dumps(document),))
            elif damage=='forged_observation':
                db.execute('UPDATE _kb_observations SET value=654321')
            elif damage=='extra_table':
                db.execute('CREATE TABLE unexpected_cache_object(value TEXT)')
            elif damage=='extra_view':
                db.execute('CREATE VIEW unexpected_cache_object AS SELECT 1 AS value')
            elif damage=='extra_trigger':
                db.execute('CREATE TRIGGER unexpected_cache_object AFTER INSERT ON _kb_observations BEGIN SELECT 1; END')
    rebuilt=_values(client,headers)
    assert rebuilt.status_code==200,rebuilt.text
    assert rebuilt.json()==initial.json()
    assert state['pages']>pages,'altered disk cache was accepted without rebuilding canonical facts'
    assert len(_cache_files(root))==1
    assert not list(root.rglob('.building-*'))


def test_distinct_kb_does_not_reuse_even_a_warm_same_tenant_cache(canonical):
    client,headers,state,root=canonical
    initial=_values(client,headers)
    assert initial.status_code==200
    state['second_other_kb']=True
    other=client.get(api(SECOND),headers=headers)
    assert other.status_code==200,other.text
    assert other.json()['total']==0 and other.json()['indicators']==[]
    assert _values(client,headers,SECOND).status_code==404
    assert _values(client,headers).json()==initial.json()
    paths=_cache_files(root)
    assert len(paths)==2 and paths[0].parent!=paths[1].parent
    scopes=set()
    for path in paths:
        with sqlite3.connect(path) as db:
            metadata=json.loads(db.execute('SELECT payload FROM _kb_cache_meta').fetchone()[0])
        scopes.add(metadata['key'][2])
    assert scopes=={KB,OTHER_KB}


def test_copying_a_valid_signed_cache_into_another_kb_cannot_supply_facts(canonical):
    client,headers,state,root=canonical
    assert _values(client,headers).status_code==200
    state['second_other_kb']=True
    assert client.get(api(SECOND),headers=headers).json()['total']==0
    paths={}
    for path in _cache_files(root):
        with sqlite3.connect(path) as db:
            metadata=json.loads(db.execute('SELECT payload FROM _kb_cache_meta').fetchone()[0])
        paths[metadata['key'][2]]=path
    _close_readers(root)
    shutil.copyfile(paths[KB],paths[OTHER_KB])
    shutil.copyfile(paths[KB].with_name(paths[KB].name+'.sig'),
                    paths[OTHER_KB].with_name(paths[OTHER_KB].name+'.sig'))
    pages=state['pages']
    result=client.get(api(SECOND),headers=headers)
    assert result.status_code==200,result.text
    assert result.json()['total']==0 and result.json()['indicators']==[]
    assert state['pages']>pages


def test_service_signing_key_rotation_invalidates_a_previously_verified_cache(canonical):
    client,headers,state,root=canonical
    initial=_values(client,headers)
    assert initial.status_code==200
    pages=state['pages']
    settings.service_token_file.write_bytes(b'new-isolated-fixture-service-key'*3)
    response=_values(client,headers)
    assert response.status_code==200,response.text
    assert response.json()==initial.json()
    assert state['pages']>pages


@pytest.mark.parametrize('setting_name,new_endpoint',[
    ('weknora_api_url','http://native.test:81'),
    ('structured_data_url','http://structured-data:19083'),
])
def test_changing_upstream_endpoint_requires_an_independent_cache(canonical,monkeypatch,setting_name,new_endpoint):
    client,headers,state,root=canonical
    initial=_values(client,headers)
    assert initial.status_code==200
    pages=state['pages'];old_paths=set(_cache_files(root))
    monkeypatch.setattr(settings,setting_name,new_endpoint)
    response=_values(client,headers)
    assert response.status_code==200,response.text
    assert response.json()==initial.json()
    assert state['pages']>pages
    new_paths=set(_cache_files(root))-old_paths
    assert len(new_paths)==1
    assert next(iter(new_paths)).parent!=next(iter(old_paths)).parent


@pytest.mark.parametrize('statement',[
    'UPDATE kb_cache._kb_observations SET value=888',
    'DELETE FROM observations',
    'DELETE FROM temp._kb_evidence',
    'DROP TABLE kb_cache._kb_observations',
    'DROP VIEW temp.observations',
    'DROP INDEX kb_cache._kb_obs_lookup',
    'CREATE TEMP VIEW unverified AS SELECT * FROM kb_cache._kb_observations',
    'CREATE TABLE kb_cache.unverified(value REAL)',
    'PRAGMA writable_schema=ON',
    'PRAGMA kb_cache.query_only=OFF',
    "ATTACH DATABASE ':memory:' AS unverified",
    'DETACH DATABASE kb_cache',
    'SELECT * FROM main.observations',
])
def test_projected_session_denies_fact_mutation_schema_pragma_and_attach(canonical,statement):
    _,_,_,_=canonical
    scope=_scope();marker=current_identity.set(scope.identity)
    try:
        with workbench_session(scope) as session:
            assert session.scalar(select(Observation.value))==17.5
            with pytest.raises(DatabaseError):
                session.execute(text(statement))
            session.rollback()
            assert session.scalar(select(Observation.value))==17.5
    finally:
        current_identity.reset(marker)


def test_research_results_commit_while_business_fact_tables_stay_empty(canonical):
    client,headers,state,root=canonical
    scope=_scope();marker=current_identity.set(scope.identity)
    try:
        stamp=datetime(2026,2,3,tzinfo=timezone.utc)
        with workbench_session(scope) as session:
            session.add(DiagramTemplate(variety='SN',name='Authored acceptance layout',is_default=True,
                layout={'nodes':[{'id':'memo','label':'Authored annotation'}],'edges':[]},
                created_at=stamp,updated_at=stamp))
            session.commit()
            assert session.scalar(select(Observation.value))==17.5
        _close_readers(root)
        with workbench_session(scope) as session:
            assert session.scalar(select(DiagramTemplate.name))=='Authored acceptance layout'
            assert session.scalar(select(Observation.value))==17.5
        with isolated_session_factory(17,FIRST)() as raw:
            assert raw.scalar(select(DiagramTemplate.name))=='Authored acceptance layout'
            for table in ('indicators','observations','derived','trading_days','observation_sources'):
                assert raw.execute(text('SELECT count(*) FROM '+table)).scalar_one()==0
    finally:
        current_identity.reset(marker)
    assert _values(client,headers).status_code==200
