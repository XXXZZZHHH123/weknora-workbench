import json

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
    assert '生成助手' in result.text and 'rows="2"' in result.text
