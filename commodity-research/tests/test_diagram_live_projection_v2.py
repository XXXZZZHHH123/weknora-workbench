"""Live diagram previews retain old nodes without reading archived fact tables."""
import calendar
from datetime import date, datetime
import json

import httpx
import pytest
from fastapi.testclient import TestClient

from tin.config import SHANGHAI, settings
from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, artifact_session
from tin.models import DiagramTemplate, Indicator, Observation
from tin.web.app import app

WB='10000000-0000-4000-8000-000000000081'
KB='20000000-0000-4000-8000-000000000081'
BINDING='30000000-0000-4000-8000-000000000081'
KNOWN='kb-'+'a'*48
NEW='kb-'+'b'*48
UNKNOWN='kb-'+'c'*48
OLD='ARCHIVED.quantity'
PATH='/research/workbenches/'+WB+'/api/sn/diagram/values'


def node(identifier,series):
    return {'id':identifier,'label':identifier,'binding':{'kind':'series','series_id':series}}


@pytest.fixture
def live_projection(tmp_path,monkeypatch):
    monkeypatch.setattr(settings,'weknora_api_url','http://native-diagram.test')
    monkeypatch.setattr(settings,'structured_data_url','http://structured-diagram.test')
    monkeypatch.setattr(settings,'data_dir',tmp_path)
    key=tmp_path/'service-key';key.write_bytes(b'synthetic-diagram-test-key-'*2)
    monkeypatch.setattr(settings,'service_token_file',key)
    identity=Identity('diagram-reader','reader@example.test','Reader',17,'owner')
    metadata={'id':WB,'name':'Fixture','scope_tenant_id':17,'owner_id':identity.user_id,
        'knowledge_base_id':KB,'scope_version':2,'sources_revision':7,
        'workspace_mode':'isolated','can_edit':True,'can_read_source':True}
    source={'id':BINDING,'source_type':'knowledge_base','source_id':KB,'knowledge_base_id':KB,
        'tenant_id':17,'kind':'primary','permission':'editor','delegated':False,
        'view_modules':['home','market','indicators','judgments','strategies','diagram','reports'],
        'knowledge':[{'id':'source-one','file_type':'xlsx','file_hash':'d'*32,'parse_status':'stored'}]}
    scope=WorkbenchScope(identity,WB,KB,17,metadata,'Bearer synthetic-diagram-reader',
        scope_version=2,sources_revision=7,sources=(source,))
    marker=current_identity.set(identity)
    try:
        with artifact_session(scope) as session:
            stamp=datetime(2026,9,30,15,tzinfo=SHANGHAI)
            session.add(DiagramTemplate(variety=settings.variety,name='Saved fixture',is_default=True,
                layout={'nodes':[node('saved',KNOWN),node('retired',OLD)],'edges':[]},
                created_at=stamp,updated_at=stamp))
            session.add(Indicator(series_id=OLD,name='Archived fixture',variety=settings.variety,category='fixture',
                caliber={'time_type':'原表日期'},unit='吨',source='fixture',frequency='月',fetch_mode='manual'))
            session.flush()
            session.add(Observation(series_id=OLD,value=999999,as_of=stamp,fetched_at=stamp,
                caliber_snapshot={'time_type':'原表日期'},source='fixture',status='正常'))
            session.commit()
    finally:current_identity.reset(marker)
    specs={identifier:{'series_id':identifier,'metric':'Measured '+suffix,'source_knowledge_id':'source-one',
        'dataset_id':'e'*24,'source_series_id':'field-'+suffix,'unit':'吨','frequency':'月','sheet':'Records',
        'source_file':'measurements.xlsx','source_binding_id':BINDING,'source_type':'knowledge_base','source_id':KB,
        'knowledge_base_id':KB,'knowledge_base_tenant_id':17,'source_version':'f'*64}
        for identifier,suffix in ((KNOWN,'a'),(NEW,'b'))}
    calls=[]
    def upstream(request):
        if request.url.host=='native-diagram.test':
            if request.url.path=='/api/v1/auth/me':
                return httpx.Response(200,json={'success':True,'data':{'user':{'id':identity.user_id,
                    'email':identity.email,'username':identity.display_name},'tenant':{'id':17},
                    'memberships':[{'tenant_id':17,'role':'owner'}]}})
            if request.url.path==f'/api/v1/workspaces/research/{WB}':
                return httpx.Response(200,json={'success':True,'data':metadata})
            if request.url.path==f'/api/v1/workspaces/research/{WB}/source-scope':
                return httpx.Response(200,json={'success':True,'data':{'scope_version':2,'sources_revision':7,
                    'id':WB,'user_id':identity.user_id,'scope_tenant_id':17,'knowledge_base_id':KB,
                    'inventory_included':True,'sources':[source],'view_modules':source['view_modules']}})
        if request.url.host=='structured-diagram.test' and request.url.path=='/api/research/facts':
            body=json.loads(request.content);calls.append(body)
            assert body['source_bindings']==[BINDING]
            identifiers=body['series_ids']
            assert OLD not in identifiers,'legacy binding entered the fact plan'
            selected=[specs[identifier] for identifier in identifiers if identifier in specs]
            envelope={'scope_version':2,'sources_revision':7,'workbench_id':WB,'version':'f'*64,
                'unavailable_series_ids':sorted(set(identifiers)-set(specs))}
            if body.get('catalog_only'):
                return httpx.Response(200,json={**envelope,'catalog_only':True,'series':selected,'datasets':[],
                    'total':len(selected),'offset':0,'next_offset':None,'truncated':False})
            assert body['view_module']=='diagram'
            rows=[]
            for spec in selected:
                values=[]
                for index in range(13):
                    month_index=2025*12+8+index
                    year,zero_month=divmod(month_index,12);month=zero_month+1
                    stamp=date(year,month,calendar.monthrange(year,month)[1]).isoformat()
                    if stamp<=body['as_of']:
                        values.append({'series_id':spec['series_id'],'date':stamp,'value':index+1,'value_status':'valid'})
                rows.extend(values[-body['points_per_series']:])
            return httpx.Response(200,json={**envelope,'bounded':True,'indicators':selected,'rows':rows,
                'source_versions':{BINDING:'f'*64}})
        raise AssertionError('unexpected request: '+request.method+' '+request.url.path)
    transport=httpx.MockTransport(upstream)
    original_async,original_client=httpx.AsyncClient,httpx.Client
    monkeypatch.setattr(httpx,'AsyncClient',lambda **kwargs:original_async(transport=transport,**kwargs))
    def sync_request(method,url,**kwargs):
        kwargs.pop('trust_env',None)
        with original_client(transport=transport) as client:return client.request(method,url,**kwargs)
    monkeypatch.setattr(httpx,'request',sync_request)
    headers={'Authorization':'Bearer synthetic-diagram-reader','X-Tenant-ID':'17','Origin':'http://testserver'}
    return TestClient(app),headers,calls


def test_old_binding_and_unsaved_real_binding_use_live_bounded_values_with_monthly_yoy(live_projection):
    client,headers,calls=live_projection
    layout={'nodes':[node('saved',KNOWN),node('retired',OLD),node('unsaved-live',NEW)],'edges':[]}
    response=client.post(PATH,headers=headers,json={'layout':layout,'date':'2026-09-30'})
    assert response.status_code==200,response.text
    values={item['node_id']:item for item in response.json()['nodes']}
    assert values['retired']['value'] is None and values['retired']['state']=='retired'
    assert values['unsaved-live']['value']==13
    assert values['unsaved-live']['yoy']==1200
    bounded=[body for body in calls if not body.get('catalog_only')]
    assert len(bounded)==1 and bounded[0]['points_per_series']==24
    assert set(bounded[0]['series_ids'])=={KNOWN,NEW}


def test_malformed_knowledge_binding_still_rejects_before_any_fact_read(live_projection):
    client,headers,calls=live_projection
    response=client.post(PATH,headers=headers,json={'layout':{'nodes':[node('malformed','kb-wrong')],'edges':[]}})
    assert response.status_code==422,response.text
    assert calls==[]


def test_unknown_canonical_binding_is_rejected_without_partial_live_values(live_projection):
    client,headers,calls=live_projection
    layout={'nodes':[node('saved',KNOWN),node('new-unknown',UNKNOWN)],'edges':[]}
    response=client.post(PATH,headers=headers,json={'layout':layout,'date':'2026-09-30'})
    assert response.status_code==403,response.text
    assert all(body.get('catalog_only') is True for body in calls)
    assert 'nodes' not in response.json()
