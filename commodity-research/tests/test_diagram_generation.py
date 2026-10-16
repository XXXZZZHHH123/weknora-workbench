import json
import asyncio
from types import SimpleNamespace

import httpx
import pytest

from tests.test_knowledge_facts import canonical,FIRST,KB,SERIES,page
from tin.web import diagram_generation as generation


def endpoint(suffix=''):
    return page(FIRST,'/api/sn/diagram/generation'+suffix)


@pytest.fixture
def generator(canonical,monkeypatch):
    client,headers,state,root=canonical
    model_calls=[]
    response={'markdown':'# Process\n## Materials\n- Feed <!-- bind: '+SERIES+' -->\n  - Output'}
    async def complete(scope,model_id,prompt,image=None):
        assert scope.knowledge_base_id==KB
        model_calls.append({'model':model_id,'prompt':prompt,'image':image})
        return dict(response)
    monkeypatch.setattr(generation,'completion',complete)
    return client,headers,state,response,model_calls


def test_generated_markdown_binds_only_actual_kb_ids_and_does_not_save_before_confirmation(generator):
    client,headers,state,response,calls=generator
    assert client.post('/research/auth/session',headers=headers).status_code==200
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'A process outline'})
    assert result.status_code==200,result.text
    body=result.json()
    assert body['bindings']==[{'node':'Feed','series_id':SERIES,'name':'Measured quantity','unit':'吨'}]
    assert body['nodes']==2 and body['model_id']=='configured-model'
    assert SERIES in calls[0]['prompt'] and 'A process outline' in calls[0]['prompt']
    assert client.get(page(suffix='/api/sn/diagram/markdown'),headers=headers).status_code==404
    applied=client.post(page(suffix='/api/sn/diagram/markdown'),headers=headers,json={
        'markdown':body['markdown'],'fact_version':body['version']})
    assert applied.status_code==200,applied.text
    assert applied.json()['unbound']==1
    assert client.get(page(suffix='/api/sn/diagram/markdown'),headers=headers).status_code==200


@pytest.mark.parametrize('markdown',[
    '# Invalid\n## Group\n- Label <!-- bind: foreign-id -->',
    '# Invalid\n## Group <!-- agg: sum -->\n- Label',
    '# Invalid\n## Group\n- Label <!-- bind: UNKNOWN_FORMULA -->',
    '', 'unstructured text'])
def test_generated_invalid_or_foreign_bindings_never_save(generator,markdown):
    client,headers,_,response,_=generator
    response['markdown']=markdown
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Structure'})
    assert result.status_code==422,result.text


def test_unknown_json_fields_and_non_string_markdown_are_rejected(generator):
    client,headers,_,response,_=generator
    response['extra']='unexpected'
    assert client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Structure'}).status_code==422
    del response['extra'];response['markdown']={'value':'bad'}
    assert client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Structure'}).status_code==422


def test_source_version_changes_during_generation_or_before_apply_require_refresh(generator,monkeypatch):
    client,headers,state,response,calls=generator
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Structure'}).json()
    state['value']=42
    apply=client.post(page(suffix='/api/sn/diagram/markdown'),headers=headers,json={
        'markdown':result['markdown'],'fact_version':result['version']})
    assert apply.status_code==409,apply.text
    async def changing(*args,**kwargs):
        state['value']+=1
        return response
    monkeypatch.setattr(generation,'completion',changing)
    assert client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Structure'}).status_code==409


def test_viewer_cannot_generate_and_empty_input_cannot_call_models(generator):
    client,headers,_,_,calls=generator
    assert client.post(endpoint(),headers={**headers,'Authorization':'Bearer source-viewer'},json={
        'model_id':'configured-model','instructions':'Structure'}).status_code==403
    assert client.post(endpoint(),headers=headers,json={'model_id':'configured-model'}).status_code==400
    assert calls==[]


@pytest.mark.parametrize('image',['https://example.test/image.png','data:image/png;base64,%%%','data:text/html;base64,eA=='])
def test_images_are_bounded_data_not_external_fetches(generator,image):
    client,headers,_,_,calls=generator
    assert client.post(endpoint(),headers=headers,json={'model_id':'configured-model','image':image}).status_code==400
    assert calls==[]


def test_large_catalog_recall_is_model_planned_then_model_decides_bindings(generator,monkeypatch):
    client,headers,_,_,calls=generator
    fields=[{'id':'kb-'+format(i,'048x'),'name':'Field '+str(i),'unit':'kg','frequency':'日'} for i in range(300)]
    monkeypatch.setattr(generation,'catalog_snapshot',lambda:('d'*64,fields))
    responses=[{'queries':['Field 299']},{'markdown':'# Process\n## Group\n- Node <!-- bind: '+fields[299]['id']+' -->'}]
    async def complete(*args,**kwargs):
        calls.append(args)
        return responses.pop(0)
    monkeypatch.setattr(generation,'completion',complete)
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'A complete process'})
    assert result.status_code==200,result.text
    assert len(calls)==2 and result.json()['candidate_count']==1
    assert result.json()['bindings'][0]['series_id']==fields[299]['id']


@pytest.mark.parametrize('content',['{} trailing','{"markdown":"a","markdown":"b"}','{"value":NaN}','```json\n{}\n```'])
def test_model_response_is_strict_whole_json(content):
    from fastapi import HTTPException
    with pytest.raises(HTTPException):
        generation.strict_json(content)


def test_generator_uses_original_diagram_page_and_compact_composer(generator):
    client,headers,_,_,_=generator
    client.post('/research/auth/session',headers=headers)
    result=client.get(page(suffix='/sn/diagram'),headers=headers)
    assert result.status_code==200,result.text
    assert 'dg-canvas' in result.text and 'dg-md-text' in result.text
    assert 'md管理' in result.text and 'rows="2"' in result.text
    assert 'id="dg-generate-btn"' not in result.text
    assert 'id="dg-generator" hidden' not in result.text


def test_native_model_capability_error_is_visible_instead_of_generic_quota_failure(monkeypatch):
    from fastapi import HTTPException
    original = httpx.AsyncClient
    monkeypatch.setattr(generation.settings, 'weknora_api_url', 'http://native.fixture')
    message = '所选模型不支持图片，请选择视觉对话模型或提供文字资料'
    transport = httpx.MockTransport(lambda request: httpx.Response(400, json={'error': {'code': 1000, 'message': message}}))
    monkeypatch.setattr(generation.httpx, 'AsyncClient', lambda **kwargs: original(transport=transport, **kwargs))
    scope = SimpleNamespace(workbench_id='workbench', knowledge_base_id='library', authorization='Bearer fixture', identity=SimpleNamespace(tenant_id=1))
    with pytest.raises(HTTPException) as caught:
        asyncio.run(generation.completion(scope, 'model', 'draft'))
    assert caught.value.status_code == 400
    assert caught.value.detail == message


def test_model_listing_respects_explicit_input_and_resolved_capabilities_before_legacy_flag(generator, monkeypatch):
    client,headers,_,_,_=generator
    entries = [
        {'id':'explicit-text','parameters':{'supports_vision':True,'spec':{'input':['text']}},'capabilities':{'input':['text']}},
        {'id':'explicit-image','parameters':{'supports_vision':False,'spec':{'input':['text','image']}},'capabilities':{'input':['text','image']}},
        {'id':'redacted-text','parameters':{'supports_vision':True},'capabilities':{'input':['text']}},
        {'id':'resolved-image','parameters':{'supports_vision':False},'capabilities':{'input':['text','image']}},
        {'id':'local-legacy','parameters':{'supports_vision':True}},
    ]
    async def models(*args,**kwargs):
        return {'success':True,'data':[{**row,'type':'KnowledgeQA','status':'active'} for row in entries]}
    monkeypatch.setattr(generation,'go_request',models)
    response=client.get(endpoint('/models'),headers=headers)
    assert response.status_code==200,response.text
    assert {row['id']:row['supports_vision'] for row in response.json()['models']} == {
        'explicit-text':False,'explicit-image':True,'redacted-text':False,'resolved-image':True,'local-legacy':True}


@pytest.mark.parametrize('message', ['Token sk-private', '<script>secret</script>', 'x' * 2000, {'message': 'nested'}])
def test_unrecognized_native_error_payload_does_not_leak_configuration(monkeypatch, message):
    from fastapi import HTTPException
    original = httpx.AsyncClient
    monkeypatch.setattr(generation.settings, 'weknora_api_url', 'http://native.fixture')
    transport = httpx.MockTransport(lambda request: httpx.Response(400, json={'error': {'message': message}}))
    monkeypatch.setattr(generation.httpx, 'AsyncClient', lambda **kwargs: original(transport=transport, **kwargs))
    scope = SimpleNamespace(workbench_id='workbench', knowledge_base_id='library', authorization='Bearer fixture', identity=SimpleNamespace(tenant_id=1))
    with pytest.raises(HTTPException) as caught:
        asyncio.run(generation.completion(scope, 'model', 'draft'))
    assert caught.value.detail == '生成参数无效，请核对模型、资料格式或重新打开当前工作台'


@pytest.mark.parametrize('provider_status',[400,401,403,429,503])
def test_native_generation_preserves_actual_provider_status_without_raw_body(monkeypatch,provider_status):
    from fastapi import HTTPException
    original = httpx.AsyncClient
    monkeypatch.setattr(generation.settings,'weknora_api_url','http://native.fixture')
    payload={'error':{'code':1008,'message':'PRIVATE_BODY token=private-key',
        'details':{'phase':'completion','error_class':'provider_http_error','provider_http_status':provider_status}}}
    transport=httpx.MockTransport(lambda request:httpx.Response(503,json=payload))
    monkeypatch.setattr(generation.httpx,'AsyncClient',lambda **kwargs:original(transport=transport,**kwargs))
    scope=SimpleNamespace(workbench_id='workbench',knowledge_base_id='library',authorization='Bearer fixture',identity=SimpleNamespace(tenant_id=1))
    with pytest.raises(HTTPException) as caught:
        asyncio.run(generation.completion(scope,'model','draft'))
    assert caught.value.status_code==503
    assert f'模型服务返回 HTTP {provider_status}' in caught.value.detail
    assert 'PRIVATE_BODY' not in caught.value.detail and 'private-key' not in caught.value.detail


@pytest.mark.parametrize(('error_class','expected'),[
    ('request_timeout','模型服务超时'),('transport_error','无法连接模型服务'),
    ('outbound_blocked','出站安全校验'),('request_cancelled','已取消'),
    ('empty_response','空内容'),('invalid_response','有效文本配置'),('client_error','响应处理失败')])
def test_native_generation_safe_error_categories_reach_research_ui(monkeypatch,error_class,expected):
    from fastapi import HTTPException
    original=httpx.AsyncClient
    monkeypatch.setattr(generation.settings,'weknora_api_url','http://native.fixture')
    payload={'error':{'code':1008,'message':'PRIVATE_BODY',
        'details':{'phase':'response_validation' if error_class in {'empty_response','invalid_response'} else 'completion','error_class':error_class}}}
    transport=httpx.MockTransport(lambda request:httpx.Response(503,json=payload))
    monkeypatch.setattr(generation.httpx,'AsyncClient',lambda **kwargs:original(transport=transport,**kwargs))
    scope=SimpleNamespace(workbench_id='workbench',knowledge_base_id='library',authorization='Bearer fixture',identity=SimpleNamespace(tenant_id=1))
    with pytest.raises(HTTPException) as caught:
        asyncio.run(generation.completion(scope,'model','draft'))
    assert caught.value.status_code==503 and expected in caught.value.detail
    assert 'PRIVATE_BODY' not in caught.value.detail


@pytest.mark.parametrize('details',[
    None,[],{'phase':'completion','error_class':'PRIVATE_BODY'},
    {'phase':'PRIVATE_BODY','error_class':'provider_http_error','provider_http_status':400},
    {'phase':'completion','error_class':['provider_http_error'],'provider_http_status':400},
    {'phase':'completion','error_class':'provider_http_error','provider_http_status':'400'},
    {'phase':'completion','error_class':'provider_http_error','provider_http_status':True},
    {'phase':'completion','error_class':'provider_http_error','provider_http_status':999},
    {'phase':'completion','error_class':'provider_http_error','provider_http_status':400,'body':'PRIVATE_BODY'},
    {'phase':'completion','error_class':'request_timeout','provider_http_status':503},
    {'phase':'response_validation','error_class':'provider_http_error','provider_http_status':400},
    {'phase':'completion','error_class':'empty_response'},
])
def test_native_failure_details_reject_untrusted_values_without_leaking(monkeypatch,details):
    from fastapi import HTTPException
    original=httpx.AsyncClient
    monkeypatch.setattr(generation.settings,'weknora_api_url','http://native.fixture')
    payload={'error':{'code':1008,'message':'PRIVATE_BODY https://secret.invalid?key=private-key','details':details}}
    transport=httpx.MockTransport(lambda request:httpx.Response(503,json=payload))
    monkeypatch.setattr(generation.httpx,'AsyncClient',lambda **kwargs:original(transport=transport,**kwargs))
    scope=SimpleNamespace(workbench_id='workbench',knowledge_base_id='library',authorization='Bearer fixture',identity=SimpleNamespace(tenant_id=1))
    with pytest.raises(HTTPException) as caught:
        asyncio.run(generation.completion(scope,'model','draft'))
    assert caught.value.status_code==503
    assert caught.value.detail=='模型服务暂时不可用，现有产业图保持原样'
