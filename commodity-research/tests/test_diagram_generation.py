import json
import asyncio
from types import SimpleNamespace

import httpx
import pytest

from tests.test_knowledge_facts import canonical,FIRST,KB,SERIES,page
from tin.web import diagram_generation as generation


def endpoint(suffix=''):
    return page(FIRST,'/api/sn/diagram/generation'+suffix)


def model_graph(series_id=SERIES):
    return {'title':'Process','groups':[{'id':'g','label':'Materials','parent_id':None}],
            'nodes':[{'id':'feed','label':'Feed','group_id':'g','series_id':series_id},
                     {'id':'output','label':'Output','group_id':'g','series_id':None}],
            'edges':[{'from':'feed','to':'output'}]}


@pytest.fixture
def generator(canonical,monkeypatch):
    client,headers,state,root=canonical
    model_calls=[]
    response=model_graph()
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


@pytest.mark.parametrize('change',[
    lambda d:d['nodes'][0].update(series_id='foreign-id'),
    lambda d:d['groups'][0].update(agg='sum'),
    lambda d:d['nodes'][0].update(series_id='UNKNOWN_FORMULA'),
    lambda d:d.update(nodes=[]), lambda d:d.update(title='')])
def test_generated_invalid_or_foreign_bindings_never_save(generator,change):
    client,headers,_,response,_=generator
    change(response)
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Structure'})
    assert result.status_code==422,result.text


def test_unknown_json_fields_and_non_list_nodes_are_rejected(generator):
    client,headers,_,response,_=generator
    response['extra']='unexpected'
    assert client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Structure'}).status_code==422
    del response['extra'];response['nodes']={'value':'bad'}
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


def test_v2_diagram_catalog_and_apply_guard_use_metadata_not_page_observations(canonical, monkeypatch):
    from fastapi import HTTPException
    from tin.integration import kb_facts
    from tin.integration.identity import Identity
    from tin.integration.workbench_scope import WorkbenchScope, current_workbench
    from tin.web.diagram_draft import compile_graph
    _, _, _, _ = canonical
    scope = WorkbenchScope(Identity('researcher', '', '', 17, 'owner'), FIRST, KB, 17,
        {'workspace_mode': 'isolated', 'can_edit': True}, 'Bearer synthetic', scope_version=2, sources_revision=1)
    snapshot = {'catalog': {SERIES: {'series_id': SERIES, 'name': 'Measured quantity', 'unit': '吨'}},
        'version': 'f' * 64, 'datasets': []}
    monkeypatch.setattr(kb_facts, 'catalog_snapshot', lambda verified: snapshot, raising=False)
    def no_projection(*args, **kwargs): raise AssertionError('A model catalog must not load observations')
    monkeypatch.setattr(generation, 'SessionLocal', no_projection)
    token = current_workbench.set(scope)
    try:
        version, catalog = generation.catalog_snapshot()
        assert version == snapshot['version'] and catalog[0]['id'] == SERIES
        markdown = compile_graph(model_graph())
        layout, bindings = generation.validate_generated_configuration(markdown, version)
        assert bindings[0]['series_id'] == SERIES and len(layout['nodes']) >= 2
        with pytest.raises(HTTPException) as error:
            generation.validate_generated_configuration(markdown, '0' * 64)
        assert error.value.status_code == 409
        foreign = compile_graph(model_graph('kb-' + 'b' * 48))
        with pytest.raises(HTTPException) as error:
            generation.validate_generated_configuration(foreign, version)
        assert error.value.status_code == 422
    finally:
        current_workbench.reset(token)


def test_v2_diagram_generation_rejects_a_changed_source_revision_before_reusing_catalog(canonical, monkeypatch):
    from dataclasses import replace
    from fastapi import HTTPException
    from tin.integration.identity import Identity
    from tin.integration.workbench_scope import WorkbenchScope
    scope = WorkbenchScope(Identity('researcher', '', '', 17, 'owner'), FIRST, KB, 17,
        {'workspace_mode': 'isolated', 'can_edit': True}, 'Bearer synthetic', scope_version=2, sources_revision=1)
    async def changed(*args): return replace(scope, sources_revision=2)
    monkeypatch.setattr(generation, 'resolve_workbench', changed)
    with pytest.raises(HTTPException) as error:
        asyncio.run(generation.verify_generation_scope(scope, 'f' * 64))
    assert error.value.status_code == 409


@pytest.mark.parametrize('changed', [False, True])
def test_v2_original_selection_uses_native_scope_and_rejects_changes_without_full_inventory(monkeypatch, changed):
    import base64
    from fastapi import HTTPException
    from tin.integration.identity import Identity
    from tin.integration.workbench_scope import WorkbenchScope, current_workbench
    binding = {'id': 'binding', 'source_type': 'knowledge_base', 'source_id': KB,
        'knowledge_base_id': KB, 'tenant_id': 17, 'permission': 'viewer', 'delegated': False,
        'view_modules': ['diagram'], 'knowledge': []}
    scope = WorkbenchScope(Identity('researcher', '', '', 17, 'owner'), FIRST, KB, 17,
        {'workspace_mode': 'isolated', 'can_edit': True, 'can_read_source': True}, 'Bearer synthetic',
        scope_version=2, sources_revision=1, sources=(binding,))
    calls = []
    async def metadata(verified, path, **kwargs):
        assert path == '/api/v1/knowledge/original-image'
        calls.append(path)
        return {'data': {'id': 'original-image', 'knowledge_base_id': KB,
            'file_hash': ('2' if changed and len(calls) > 1 else '1') * 64, 'updated_at': 'synthetic-version'}}
    async def content(identifier, *, scope):
        assert identifier == 'original-image' and scope.source_binding_id == 'binding'
        return {'supported': True, 'image_data_url': 'data:image/png;base64,' + base64.b64encode(b'synthetic-image').decode(), 'sha256': '1' * 64}
    async def completion(*args): return model_graph()
    async def verify(*args): return None
    monkeypatch.setattr(generation, 'go_request', metadata)
    monkeypatch.setattr(generation, 'source_content', content)
    monkeypatch.setattr(generation, 'completion', completion)
    monkeypatch.setattr(generation, 'verify_generation_scope', verify)
    monkeypatch.setattr(generation, 'catalog_snapshot', lambda: ('f' * 64, [{'id': SERIES, 'name': 'Measured quantity', 'unit': '吨'}]))
    token = current_workbench.set(scope)
    try:
        body = generation.GenerateRequest(model_id='synthetic-model', source_id='original-image')
        if changed:
            with pytest.raises(HTTPException) as caught:
                asyncio.run(generation.generate(body))
            assert caught.value.status_code == 409
        else:
            result = asyncio.run(generation.generate(body))
            assert result['bindings'][0]['series_id'] == SERIES
        assert len(calls) == 2
        async def foreign(*args, **kwargs): return {'data': {'id': 'original-image', 'knowledge_base_id': 'unbound-kb'}}
        monkeypatch.setattr(generation, 'go_request', foreign)
        with pytest.raises(HTTPException) as caught:
            asyncio.run(generation.original_identity(scope, 'original-image'))
        assert caught.value.status_code == 403
    finally:
        current_workbench.reset(token)


@pytest.mark.parametrize('image',['https://example.test/image.png','data:image/png;base64,%%%','data:text/html;base64,eA=='])
def test_images_are_bounded_data_not_external_fetches(generator,image):
    client,headers,_,_,calls=generator
    assert client.post(endpoint(),headers=headers,json={'model_id':'configured-model','image':image}).status_code==400
    assert calls==[]


def catalog_fields(count, padding=''):
    return [{'id':'kb-'+format(i,'048x'),'name':'Gauge '+str(i),'unit':'kg','frequency':'日',
             'sheet':'Measurements','dimensions':{'coverage':padding},'source_file':'readings.xlsx',
             'knowledge_base_id':KB,'source_kind':'structured'} for i in range(count)]


def prompt_fields(prompt):
    encoded=prompt.rpartition('\n真实字段目录：')[2].partition('\n资料：')[0]
    return json.loads(encoded)


def catalog_bytes(fields):
    return len(json.dumps(fields,ensure_ascii=False,separators=(',',':')).encode('utf-8'))


def test_complete_catalog_over_256_fields_reaches_model_without_keyword_filter(generator,monkeypatch):
    client,headers,_,_,calls=generator
    fields=catalog_fields(300)
    monkeypatch.setattr(generation,'catalog_snapshot',lambda:('d'*64,fields))
    async def complete(*args,**kwargs):
        calls.append(args)
        return model_graph(fields[-1]['id'])
    monkeypatch.setattr(generation,'completion',complete)
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Study the bottleneck'})
    assert result.status_code==200,result.text
    assert len(calls)==1 and result.json()['candidate_count']==300
    assert prompt_fields(calls[0][2])==fields
    assert result.json()['bindings'][0]['series_id']==fields[-1]['id']
    assert result.json()['candidates_truncated'] is False


def test_catalog_selection_covers_all_byte_bounded_batches_with_full_input(generator,monkeypatch):
    client,headers,_,_,calls=generator
    fields=catalog_fields(5,'统计范围'*40)
    budget=catalog_bytes(fields[:2])
    monkeypatch.setattr(generation,'CATALOG_BYTES',budget,raising=False)
    monkeypatch.setattr(generation,'catalog_snapshot',lambda:('d'*64,fields))
    responses=[{'series_ids':[]},{'series_ids':[fields[3]['id']]},
               {'series_ids':[fields[4]['id']]},model_graph(fields[4]['id'])]
    async def complete(scope,model,prompt,image=None):
        calls.append({'prompt':prompt,'image':image})
        return responses.pop(0)
    monkeypatch.setattr(generation,'completion',complete)
    image='data:image/png;base64,eA=='
    instruction='Retain the whole research question, including reporting scope.'
    result=client.post(endpoint(),headers=headers,json={
        'model_id':'configured-model','instructions':instruction,'image':image})
    assert result.status_code==200,result.text
    assert len(calls)==4
    batches=[prompt_fields(call['prompt']) for call in calls[:-1]]
    assert [field for batch in batches for field in batch]==fields
    assert [len(batch) for batch in batches]==[2,2,1]
    assert all(catalog_bytes(batch)<=budget for batch in batches)
    assert all(instruction in call['prompt'] and call['image']==image for call in calls)
    assert prompt_fields(calls[-1]['prompt'])==[fields[3],fields[4]]
    assert result.json()['candidate_count']==2 and result.json()['candidates_truncated'] is False
    assert result.json()['bindings'][0]['series_id']==fields[4]['id']


@pytest.mark.parametrize('selection',[
    {'series_ids':['invented-id']},{'series_ids':None},{'series_ids':'not-a-list'},
    {'series_ids':[1]},{'series_ids':[{}]},{'series_ids':[],'extra':'instruction'},
    {'queries':['Gauge']},
])
def test_catalog_selection_rejects_invalid_plans_without_generating_or_saving(generator,monkeypatch,selection):
    client,headers,_,_,calls=generator
    fields=catalog_fields(3)
    monkeypatch.setattr(generation,'CATALOG_BYTES',catalog_bytes(fields[:1]),raising=False)
    monkeypatch.setattr(generation,'catalog_snapshot',lambda:('d'*64,fields))
    async def complete(*args,**kwargs):
        calls.append(args)
        return selection
    monkeypatch.setattr(generation,'completion',complete)
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Research outline'})
    assert result.status_code==422,result.text
    assert len(calls)==1
    assert client.get(page(suffix='/api/sn/diagram/markdown'),headers=headers).status_code==404


@pytest.mark.parametrize('empty',[False,True])
def test_catalog_selection_accepts_explicit_id_array_including_empty(generator,monkeypatch,empty):
    client,headers,_,_,calls=generator
    fields=catalog_fields(2)
    monkeypatch.setattr(generation,'CATALOG_BYTES',catalog_bytes(fields[:1]),raising=False)
    monkeypatch.setattr(generation,'catalog_snapshot',lambda:('d'*64,fields))
    selected=[] if empty else [fields[0]['id']]
    responses=[selected,[],model_graph(None if empty else fields[0]['id'])]
    async def complete(*args,**kwargs):
        calls.append(args)
        return responses.pop(0)
    monkeypatch.setattr(generation,'completion',complete)
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Research outline'})
    assert result.status_code==200,result.text
    assert len(calls)==3
    assert result.json()['candidate_count']==len(selected)
    assert [binding['series_id'] for binding in result.json()['bindings']]==selected
    assert prompt_fields(calls[-1][2])==([] if empty else fields[:1])
    assert result.json()['candidates_truncated'] is False


@pytest.mark.parametrize('object_wrapper',[False,True])
def test_repeated_real_ids_form_one_candidate_set_without_losing_distinct_choices(generator,monkeypatch,object_wrapper):
    client,headers,_,_,calls=generator
    fields=catalog_fields(3)
    monkeypatch.setattr(generation,'CATALOG_BYTES',catalog_bytes(fields[:2]),raising=False)
    monkeypatch.setattr(generation,'catalog_snapshot',lambda:('d'*64,fields))
    identifiers=[fields[1]['id'],fields[0]['id'],fields[1]['id']]
    selection={'series_ids':identifiers} if object_wrapper else identifiers
    responses=[selection,[],model_graph(fields[1]['id'])]
    async def complete(*args,**kwargs):
        calls.append(args)
        return responses.pop(0)
    monkeypatch.setattr(generation,'completion',complete)
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Research outline'})
    assert result.status_code==200,result.text
    assert len(calls)==3 and result.json()['candidate_count']==2
    assert prompt_fields(calls[-1][2])==fields[:2]
    assert result.json()['bindings'][0]['series_id']==fields[1]['id']


def test_raw_selection_limit_applies_before_deduplicating_real_ids(generator,monkeypatch):
    client,headers,_,_,calls=generator
    fields=catalog_fields(3)
    monkeypatch.setattr(generation,'CATALOG_BYTES',catalog_bytes(fields[:2]),raising=False)
    monkeypatch.setattr(generation,'catalog_snapshot',lambda:('d'*64,fields))
    async def complete(*args,**kwargs):
        calls.append(args)
        return [fields[0]['id']]*25
    monkeypatch.setattr(generation,'completion',complete)
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Research outline'})
    assert result.status_code==422,result.text
    assert len(calls)==1


@pytest.mark.parametrize('kind',['unknown','other-batch','non-string','nested','field-name','over-limit'])
def test_catalog_selection_array_keeps_all_id_and_capacity_guards(generator,monkeypatch,kind):
    client,headers,_,_,calls=generator
    fields=catalog_fields(26)
    monkeypatch.setattr(generation,'CATALOG_BYTES',catalog_bytes(fields[:25]),raising=False)
    monkeypatch.setattr(generation,'catalog_snapshot',lambda:('d'*64,fields))
    invalid={
        'unknown':['invented-id'],'other-batch':[fields[-1]['id']],
        'non-string':[1],
        'nested':[[fields[0]['id']]],'field-name':[fields[0]['name']],
        'over-limit':[field['id'] for field in fields[:25]],
    }[kind]
    async def complete(*args,**kwargs):
        calls.append(args)
        return invalid
    monkeypatch.setattr(generation,'completion',complete)
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Research outline'})
    assert result.status_code==422,result.text
    assert result.json()['detail']=='模型没有给出有效指标筛选结果'
    assert len(calls)==1
    assert client.get(page(suffix='/api/sn/diagram/markdown'),headers=headers).status_code==404


def test_catalog_selection_rejects_real_id_from_other_batch(generator,monkeypatch):
    client,headers,_,_,calls=generator
    fields=catalog_fields(3)
    monkeypatch.setattr(generation,'CATALOG_BYTES',catalog_bytes(fields[:1]),raising=False)
    monkeypatch.setattr(generation,'catalog_snapshot',lambda:('d'*64,fields))
    async def complete(*args,**kwargs):
        calls.append(args)
        return {'series_ids':[fields[-1]['id']]}
    monkeypatch.setattr(generation,'completion',complete)
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Research outline'})
    assert result.status_code==422,result.text
    assert len(calls)==1


@pytest.mark.parametrize('kind',['single-field','batch-count'])
def test_catalog_capacity_is_preflighted_before_any_model_call(generator,monkeypatch,kind):
    client,headers,_,_,calls=generator
    fields=catalog_fields(5)
    budget=catalog_bytes(fields[:1])-(1 if kind=='single-field' else 0)
    monkeypatch.setattr(generation,'CATALOG_BYTES',budget,raising=False)
    monkeypatch.setattr(generation,'CATALOG_MAX_BATCHES',4,raising=False)
    monkeypatch.setattr(generation,'catalog_snapshot',lambda:('d'*64,fields))
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Research outline'})
    assert result.status_code==413,result.text
    assert calls==[]


def test_selected_catalog_over_budget_is_rejected_instead_of_truncated(generator,monkeypatch):
    client,headers,_,_,calls=generator
    fields=catalog_fields(3)
    monkeypatch.setattr(generation,'CATALOG_BYTES',catalog_bytes(fields[:1]),raising=False)
    monkeypatch.setattr(generation,'catalog_snapshot',lambda:('d'*64,fields))
    async def complete(scope,model,prompt,image=None):
        calls.append(prompt)
        return {'series_ids':[field['id'] for field in prompt_fields(prompt)]}
    monkeypatch.setattr(generation,'completion',complete)
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Research outline'})
    assert result.status_code==413,result.text
    assert len(calls)==3


def test_no_compatible_selection_is_explicitly_unbound_without_keyword_fallback(generator,monkeypatch):
    client,headers,_,_,calls=generator
    fields=catalog_fields(2)
    monkeypatch.setattr(generation,'CATALOG_BYTES',catalog_bytes(fields[:1]),raising=False)
    monkeypatch.setattr(generation,'catalog_snapshot',lambda:('d'*64,fields))
    responses=[{'series_ids':[]},{'series_ids':[]},model_graph(None)]
    async def complete(*args,**kwargs):
        calls.append(args)
        return responses.pop(0)
    monkeypatch.setattr(generation,'completion',complete)
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Research outline'})
    assert result.status_code==200,result.text
    assert len(calls)==3 and result.json()['candidate_count']==0 and result.json()['bindings']==[]
    assert '待补全' in result.json()['notice']


def test_complete_catalog_byte_boundary_does_not_add_unnecessary_selection_call(generator,monkeypatch):
    client,headers,_,_,calls=generator
    fields=catalog_fields(2,'多字节')
    monkeypatch.setattr(generation,'CATALOG_BYTES',catalog_bytes(fields),raising=False)
    monkeypatch.setattr(generation,'catalog_snapshot',lambda:('d'*64,fields))
    async def complete(*args,**kwargs):
        calls.append(args)
        return model_graph(fields[1]['id'])
    monkeypatch.setattr(generation,'completion',complete)
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Research outline'})
    assert result.status_code==200,result.text
    assert len(calls)==1 and prompt_fields(calls[0][2])==fields


def test_catalog_selection_output_limit_is_rejected_without_taking_first_ids(generator,monkeypatch):
    client,headers,_,_,calls=generator
    fields=catalog_fields(66)
    monkeypatch.setattr(generation,'CATALOG_BYTES',catalog_bytes(fields[:65]),raising=False)
    monkeypatch.setattr(generation,'catalog_snapshot',lambda:('d'*64,fields))
    async def complete(*args,**kwargs):
        calls.append(args)
        return {'series_ids':[field['id'] for field in fields[:65]]}
    monkeypatch.setattr(generation,'completion',complete)
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Research outline'})
    assert result.status_code==422,result.text
    assert result.json()['detail']=='模型没有给出有效指标筛选结果'
    assert len(calls)==1


@pytest.mark.parametrize('selected_count',[24,25])
def test_compact_catalog_selection_budget_accepts_24_and_rejects_25(generator,monkeypatch,selected_count):
    client,headers,_,_,calls=generator
    fields=catalog_fields(26)
    monkeypatch.setattr(generation,'CATALOG_BYTES',catalog_bytes(fields[:25]),raising=False)
    monkeypatch.setattr(generation,'catalog_snapshot',lambda:('d'*64,fields))
    responses=[{'series_ids':[field['id'] for field in fields[:selected_count]]},
               {'series_ids':[]},model_graph(fields[0]['id'])]
    async def complete(*args,**kwargs):
        calls.append(args)
        return responses.pop(0)
    monkeypatch.setattr(generation,'completion',complete)
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Research overview'})
    if selected_count==24:
        assert result.status_code==200,result.text
        assert len(calls)==3 and result.json()['candidate_count']==24
        assert prompt_fields(calls[-1][2])==fields[:24]
        assert result.json()['bindings'][0]['series_id']==fields[0]['id']
    else:
        assert result.status_code==422,result.text
        assert result.json()['detail']=='模型没有给出有效指标筛选结果'
        assert len(calls)==1


def test_explicit_detail_request_keeps_all_26_legal_nodes_without_truncation(generator):
    client,headers,_,response,_=generator
    response['nodes']=[{'id':'n'+str(i),'label':'Variable '+str(i),'group_id':'g',
                        'series_id':SERIES if i==0 else None} for i in range(26)]
    response['edges']=[{'from':'n'+str(i),'to':'n'+str(i+1)} for i in range(25)]
    result=client.post(endpoint(),headers=headers,json={
        'model_id':'configured-model','instructions':'Expand the complete 26 research variables with all supported details.'})
    assert result.status_code==200,result.text
    assert result.json()['nodes']==26
    from tin.export import diagram_md
    layout=diagram_md.parse(result.json()['markdown'])
    assert {node['id'] for node in layout['nodes'] if node.get('kind')!='group'}=={'n'+str(i) for i in range(26)}
    assert len(layout['edges'])==25


def test_prompt_byte_limit_is_checked_before_model_call(generator,monkeypatch):
    client,headers,_,_,calls=generator
    monkeypatch.setattr(generation,'MODEL_PROMPT_BYTES',200,raising=False)
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Research outline'})
    assert result.status_code==413,result.text
    assert calls==[]


def test_source_change_during_catalog_selection_stops_before_final_generation(generator,monkeypatch):
    client,headers,_,_,calls=generator
    fields=catalog_fields(2)
    version=['d'*64]
    monkeypatch.setattr(generation,'CATALOG_BYTES',catalog_bytes(fields[:1]),raising=False)
    monkeypatch.setattr(generation,'catalog_snapshot',lambda:(version[0],fields))
    responses=[{'series_ids':[fields[0]['id']]},{'series_ids':[]},model_graph(fields[0]['id'])]
    async def complete(*args,**kwargs):
        calls.append(args)
        if len(calls)==2:
            version[0]='e'*64
        return responses.pop(0)
    monkeypatch.setattr(generation,'completion',complete)
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Research outline'})
    assert result.status_code==409,result.text
    assert len(calls)==2
    assert client.get(page(suffix='/api/sn/diagram/markdown'),headers=headers).status_code==404


def test_generation_keeps_duplicate_names_and_explicit_flow_through_apply_and_export(generator):
    client,headers,_,response,_=generator
    response['groups'][0]['label']='Product'
    for node in response['nodes']:
        node['label']='Product'
    response['edges'].append({'from':'output','to':'feed'})
    result=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'Repeated labels with an explicit cycle'})
    assert result.status_code==200,result.text
    body=result.json()
    applied=client.post(page(suffix='/api/sn/diagram/markdown'),headers=headers,json={
        'markdown':body['markdown'],'fact_version':body['version']})
    assert applied.status_code==200,applied.text
    exported=client.get(page(suffix='/api/sn/diagram/markdown'),headers=headers)
    assert exported.status_code==200,exported.text
    from tin.export import diagram_md
    restored=diagram_md.parse(exported.json()['markdown'])
    assert {(edge['from'],edge['to']) for edge in restored['edges']}=={('feed','output'),('output','feed')}
    assert {node['id']:node.get('parent') for node in restored['nodes']}=={'g':None,'feed':'g','output':'g'}


def test_invalid_explicit_id_export_is_a_client_error_instead_of_server_failure(generator):
    client,headers,_,_,_=generator
    result=client.post(page(suffix='/api/sn/diagram/markdown/dump'),headers=headers,json={
        'layout':{'nodes':[{'id':'bad id','reference_id':'bad id','kind':'group','label':'Group','parent':None}],
                  'edges':[]}})
    assert result.status_code==400,result.text


def test_generated_graph_still_allows_existing_canvas_edits_before_export(generator):
    client,headers,_,response,_=generator
    response['groups'][0]['label']='Same'
    for node in response['nodes']:
        node['label']='Same'
    generated=client.post(endpoint(),headers=headers,json={'model_id':'configured-model','instructions':'A graph with repeated names'})
    assert generated.status_code==200,generated.text
    from tin.export import diagram_md
    layout=diagram_md.parse(generated.json()['markdown'])
    group=next(node for node in layout['nodes'] if node.get('kind')=='group')
    group['binding']={'kind':'agg','op':'sum','members':['output']}
    layout['nodes'].append({'id':'added','label':'Independent','parent':None,'x':0,'y':0,'w':140,'h':66,
                            'binding':{'kind':'none'},'statics':[]})
    exported=client.post(page(suffix='/api/sn/diagram/markdown/dump'),headers=headers,json={'layout':{
        'nodes':layout['nodes'],'edges':layout['edges']}})
    assert exported.status_code==200,exported.text
    restored=diagram_md.parse(exported.json()['markdown'])
    nodes={node['id']:node for node in restored['nodes']}
    assert nodes['g']['binding']['members']==['output']
    assert nodes['added']['parent'] is None


@pytest.mark.parametrize('content',['{} trailing','{"markdown":"a","markdown":"b"}','{"value":NaN}'])
def test_model_response_is_strict_whole_json(content):
    from fastapi import HTTPException
    with pytest.raises(HTTPException):
        generation.strict_json(content)


@pytest.mark.parametrize('value',[{'queries':['Material','Output']},{'markdown':'# Process\n## Group\n- Node\u2028Text'}])
@pytest.mark.parametrize('wrapper',['{}','```json\n{}\n```','```\n{}\n```',' \t\r\n```json\r\n{}\r\n```\r\n\t '])
def test_model_response_accepts_one_complete_json_fence(value,wrapper):
    content=wrapper.format(json.dumps(value,ensure_ascii=False))
    assert generation.strict_json(content)==value


@pytest.mark.parametrize('content',[
    'Before\n```json\n{}\n```','```json\n{}\n```\nAfter',
    '```json\n{}\n```\n```json\n{}\n```','```python\n{}\n```',
    '```json extra\n{}\n```','```json\n{}','```json\n{}\n``',
    '```json\n{"queries":[],"queries":["Other"]}\n```',
    '```\n{"value":NaN}\n```','```json\n{"value":Infinity}\n```',
    '```json\n{} trailing\n```','```json\n{}\n{}\n```',
    '\u00a0{}','\u2028{}','```jſon\n{}\n```',
    '\u00a0```json\n{}\n```','```json\n{}\n```\u00a0',
])
def test_model_response_fences_keep_strict_full_document_validation(content):
    from fastapi import HTTPException
    with pytest.raises(HTTPException):
        generation.strict_json(content)


def test_model_response_fences_do_not_bypass_original_size_limit():
    from fastapi import HTTPException
    with pytest.raises(HTTPException):
        generation.strict_json('```json\n'+json.dumps({'markdown':'x'*(128*1024)})+'\n```')


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
