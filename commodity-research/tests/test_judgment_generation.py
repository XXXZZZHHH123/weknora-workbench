"""Isolated SQLite and mock model transport; synthetic source only."""
import asyncio
import copy
from types import SimpleNamespace
import httpx
import pytest
from sqlalchemy import select
from tests.test_knowledge_facts import canonical, FIRST, SECOND, KB, SERIES, page
from tin.web import judgment_generation as generation
from tin.integration.workbench_scope import isolated_session_factory
from tin.models import Judgment

TEXT='A supply contraction and soft orders require opposing evidence. Stocks above 12 吨 require review.'
def document():
    return {'judgment':{'contradiction':{'bull':{'claim':'Supply contraction','evidence':[
        {'desc':'Measured stock','series_id':SERIES}]},'bear':{'claim':'Soft orders','evidence':[]}},
        'marginal_focus':[{'desc':'Stock change','series_id':SERIES}], 'whitelist':[SERIES],
        'thresholds':[{'id':'watch','name':'Stock watch','series_id':SERIES,'operator':'>',
                      'value':12,'type':'观察位','horizon':'短期'}]},
        'binding_units':{SERIES:'吨'},'rule_quotes':{'watch':'Stocks above 12 吨'},
        'unresolved':[{'section':'demand','text':'Orders','reason':'No comparable indicator'}]}
def endpoint(suffix):return page(FIRST,'/api/sn/judgment'+suffix)
def rows():
    with isolated_session_factory(17,FIRST)() as session:
        return session.scalars(select(Judgment).order_by(Judgment.version)).all()
@pytest.fixture
def generator(canonical,monkeypatch):
    client,headers,state,tmp=canonical;calls=[];response=document()
    async def complete(scope,model,prompt):
        assert scope.knowledge_base_id==KB
        calls.append({'model':model,'prompt':prompt});return copy.deepcopy(response)
    monkeypatch.setattr(generation,'completion',complete);generation.previews.items.clear()
    return client,headers,state,response,calls

def generate(client,headers,base=0):
    return client.post(endpoint('/generate'),headers=headers,json={'model_id':'synthetic-chat','text':TEXT,'base_version':base})
def apply(client,headers,preview):
    return client.post(endpoint('/apply'),headers=headers,json={'draft_id':preview['draft_id'],'base_version':preview['base_version']})

def test_generate_is_preview_only_then_explicit_save_appends_draft(generator):
    client,headers,_,_,calls=generator
    generated=generate(client,headers);assert generated.status_code==200,generated.text
    preview=generated.json();assert TEXT in calls[0]['prompt'] and SERIES in calls[0]['prompt']
    assert len(calls) == 1
    assert rows()==[] and preview['bindings'][0]['unit']=='吨'
    saved=apply(client,headers,preview);assert saved.status_code==200,saved.text
    assert saved.json()['status']=='草稿' and saved.json()['version']==1
    assert rows()[0].source_note==TEXT and rows()[0].author=='researcher'
    assert not rows()[0].falsifiers and rows()[0].priority_rule is None
    assert apply(client,headers,preview).status_code==409 and len(rows())==1
    managed=client.get(endpoint('/manage'),headers=headers).json()
    assert managed['current']['version']==1 and SERIES in managed['current']['markdown']
    assert client.get(endpoint('/versions/1'),headers=headers).json()['diff']['fields']==[]

@pytest.mark.parametrize('change',[
    lambda d:d['binding_units'].update({SERIES:'kg'}),
    lambda d:d['judgment']['thresholds'][0].update(value=99999),
    lambda d:d['judgment']['thresholds'][0].update(series_id='foreign'),
    lambda d:d['judgment']['contradiction']['bull'].update(evidence=None)])
def test_invalid_model_draft_never_creates_version(generator,change):
    client,headers,_,response,_=generator;change(response)
    result=generate(client,headers);assert result.status_code==422,result.text
    assert rows()==[]

def test_viewer_and_foreign_workbench_preview_are_rejected(generator):
    client,headers,_,_,calls=generator
    assert generate(client,{**headers,'Authorization':'Bearer source-viewer'}).status_code==403 and calls==[]
    preview=generate(client,headers).json()
    result=client.post(page(SECOND,'/api/sn/judgment/apply'),headers=headers,
        json={'draft_id':preview['draft_id'],'base_version':0})
    assert result.status_code==409 and rows()==[]
    assert client.get(page(SECOND,'/api/sn/judgment/versions/1'),headers=headers).status_code==404

def test_stale_preview_and_changed_source_cannot_save(generator):
    client,headers,state,_,_=generator
    a=generate(client,headers).json();b=generate(client,headers).json()
    assert apply(client,headers,a).status_code==200
    assert apply(client,headers,b).status_code==409 and len(rows())==1
    c=generate(client,headers,1).json();state['value']+=1
    assert apply(client,headers,c).status_code==409 and len(rows())==1

def test_latest_draft_is_basis_even_if_an_older_version_is_active(generator):
    client,headers,_,_,calls=generator
    assert apply(client,headers,generate(client,headers).json()).status_code==200
    with isolated_session_factory(17,FIRST)() as session:
        session.scalar(select(Judgment)).status='生效';session.commit()
    assert apply(client,headers,generate(client,headers,1).json()).status_code==200
    managed=client.get(endpoint('/manage'),headers=headers).json()
    assert managed['current']['version']==1 and managed['latest']['version']==2
    count=len(calls);assert generate(client,headers,1).status_code==409 and len(calls)==count

def test_source_change_during_generation_discards_preview(generator,monkeypatch):
    client,headers,state,response,_=generator
    async def complete(*args):state['value']+=1;return copy.deepcopy(response)
    monkeypatch.setattr(generation,'completion',complete)
    assert generate(client,headers).status_code==409 and rows()==[]

def test_old_form_keeps_private_input_escaped_without_saving(generator):
    client,headers,_,_,calls=generator;raw=TEXT+' <script>window.bad=1</script>'
    result=client.post(page(FIRST,'/sn/judgment/import'),headers=headers,
        data={'text':raw,'author':'forged'},follow_redirects=False)
    assert result.status_code==200,result.text
    assert '&lt;script&gt;' in result.text and '<script>window.bad=1</script>' not in result.text
    assert TEXT in result.text and rows()==[] and calls==[] and raw not in result.headers.get('location','')

def test_model_failure_or_oversize_keeps_no_version(generator,monkeypatch):
    client,headers,_,_,_=generator
    async def fail(*args):
        from fastapi import HTTPException
        raise HTTPException(503,'Synthetic unavailable')
    monkeypatch.setattr(generation,'completion',fail)
    assert generate(client,headers).status_code==503
    large=client.post(endpoint('/generate'),headers=headers,json={'model_id':'m','text':'字'*20000,'base_version':0})
    assert large.status_code==413 and rows()==[]

def test_native_transport_is_fixed_purpose_and_redacts_provider_body(monkeypatch):
    monkeypatch.setattr(generation.settings,'weknora_api_url','http://native.test')
    original=httpx.AsyncClient;requests=[]
    def upstream(request):requests.append(request);return httpx.Response(503,json={'error':{'message':'provider secret body'}})
    monkeypatch.setattr(generation.httpx,'AsyncClient',lambda *args,**kwargs:original(*args,**{
        **kwargs,'transport':httpx.MockTransport(upstream)}))
    scope=SimpleNamespace(workbench_id=FIRST,knowledge_base_id=KB,authorization='Bearer synthetic',identity=SimpleNamespace(tenant_id=17))
    from fastapi import HTTPException
    with pytest.raises(HTTPException) as caught:asyncio.run(generation.completion(scope,'m','full source'))
    assert 'secret' not in caught.value.detail and requests[0].url.path.endswith('/judgment/generate')
    assert b'purpose' not in requests[0].content and b'image' not in requests[0].content


def test_complete_catalog_batches_and_complete_source_are_model_selected(monkeypatch):
    catalog = [{'id': 'real-' + str(index), 'unit': 'kg', 'name': '量' * 1400 + str(index)}
               for index in range(53)]
    observed, chosen = [], []
    async def choose(scope, model, prompt):
        assert TEXT in prompt
        encoded = prompt.split('真实字段目录：', 1)[1].split('\n完整原文：', 1)[0]
        import json
        chunk = json.loads(encoded)
        observed.extend(row['id'] for row in chunk)
        chosen.append(chunk[0])
        assert len(prompt.encode('utf-8')) <= generation.PROMPT_BYTES
        return {'series_ids': [chunk[0]['id']]}
    monkeypatch.setattr(generation, 'completion', choose)
    selected, partial = asyncio.run(generation.choose_catalog(None, 'm', catalog, TEXT))
    assert partial and selected == chosen
    assert observed == [row['id'] for row in catalog]
    assert len(generation.make_prompt('synthetic protocol', selected, TEXT).encode()) <= generation.PROMPT_BYTES


def test_client_purpose_payload_or_invalid_unicode_never_reaches_model(generator):
    client, headers, _, _, calls = generator
    body = {'model_id': 'synthetic-chat', 'text': TEXT, 'base_version': 0, 'purpose': 'diagram'}
    assert client.post(endpoint('/generate'), headers=headers, json=body).status_code == 422
    assert client.post(endpoint('/apply'), headers=headers,
        json={'draft_id': 'x' * 32, 'base_version': 0, 'judgment': {}}).status_code == 422
    import json
    result = client.post(endpoint('/generate'), headers={**headers, 'Content-Type': 'application/json'},
        content=json.dumps({'model_id': 'synthetic-chat', 'text': '\ud800', 'base_version': 0}))
    assert result.status_code == 422
    assert calls == [] and rows() == []


def test_full_130k_catalog_that_fits_final_prompt_skips_selection(monkeypatch):
    catalog = [{'id': 'real-' + str(index), 'unit': 'kg', 'name': '量' * 850 + str(index)}
               for index in range(50)]
    assert 120 * 1024 < len(generation.catalog_json(catalog).encode('utf-8')) < 140 * 1024
    calls = []
    async def never_select(*args):
        calls.append(args)
        raise AssertionError('A full prompt within budget must not require selection')
    monkeypatch.setattr(generation, 'completion', never_select)
    selected, partial = asyncio.run(generation.choose_catalog(None, 'm', catalog, TEXT))
    assert selected == catalog and partial is False and calls == []


def test_total_generate_deadline_preserves_existing_versions_and_no_preview(generator,monkeypatch):
    client, headers, _, response, _ = generator
    monkeypatch.setattr(generation, 'GENERATION_SECONDS', 0.1, raising=False)
    async def slow(*args):
        await asyncio.sleep(0.5)
        return copy.deepcopy(response)
    monkeypatch.setattr(generation, 'completion', slow)
    result = generate(client, headers)
    assert result.status_code == 503, result.text
    assert '原文' in result.json()['detail'] and '现有版本' in result.json()['detail']
    assert rows() == [] and not generation.previews.items


def test_222k_catalog_uses_three_complete_selections_then_one_final_analysis(generator,monkeypatch):
    client, headers, _, response, _ = generator
    catalog = [{'id': SERIES if index == 0 else 'real-' + str(index), 'unit': '吨',
                'name': '量' * 1400 + str(index)} for index in range(53)]
    original = generation.snapshot
    def large_snapshot(): return {**original(), 'catalog': catalog}
    monkeypatch.setattr(generation, 'snapshot', large_snapshot)
    calls, seen = [], []
    async def complete(scope, model, prompt):
        calls.append(prompt)
        if prompt.startswith(generation.SELECTION_PROTOCOL):
            import json
            chunk = json.loads(prompt.split('真实字段目录：', 1)[1].split('\n完整原文：', 1)[0])
            seen.extend(item['id'] for item in chunk)
            return {'series_ids': [SERIES] if any(item['id'] == SERIES for item in chunk) else []}
        return copy.deepcopy(response)
    monkeypatch.setattr(generation, 'completion', complete)
    result = generate(client, headers)
    assert result.status_code == 200, result.text
    assert len(calls) == 4 and len(seen) == len(catalog)
    assert seen == [item['id'] for item in catalog]
    assert all(TEXT in prompt for prompt in calls)
    assert rows() == []


def test_actual_model_request_contains_a_machine_readable_numeric_rule_contract(generator):
    import json
    client, headers, _, _, calls = generator
    result = generate(client, headers)
    assert result.status_code == 200, result.text
    assert len(calls) == 1 and rows() == []
    prompt = calls[0]['prompt']
    marker = '阈值条目的 JSON Schema 类型示意：\n'
    assert marker in prompt
    schema, _ = json.JSONDecoder().raw_decode(prompt.split(marker, 1)[1])
    assert schema['type'] == 'object'
    assert {'operator', 'series_id', 'value'} <= set(schema['required'])
    assert schema['properties']['value']['oneOf'] == [
        {'type': 'number'}, {'type': 'array', 'items': {'type': 'number'}, 'minItems': 2, 'maxItems': 2}]
    assert schema['if']['properties']['operator']['const'] == 'between'
    assert schema['then']['properties']['value']['type'] == 'array'
    assert schema['else']['properties']['value']['type'] == 'number'
    # The type example is abstract: actual source, unit and IDs arrive only in
    # the request's authorized catalog and source, not as schema defaults.
    assert 'default' not in json.dumps(schema) and SERIES not in json.dumps(schema)
    assert 'JSON number' in prompt and 'unresolved' in prompt


@pytest.mark.parametrize('value', ['12', True, None, {'minimum': 12}, 'missing'])
def test_clearer_prompt_does_not_coerce_or_remove_invalid_model_thresholds(generator, value):
    client, headers, _, response, calls = generator
    rule = response['judgment']['thresholds'][0]
    if value == 'missing':
        rule.pop('value')
    else:
        rule['value'] = value
    result = generate(client, headers)
    assert result.status_code == 422, result.text
    assert len(calls) == 1 and rows() == [] and not generation.previews.items
    assert '有限数字' in result.json()['detail']


def test_explicit_numeric_interval_with_original_quote_remains_a_valid_preview(generator):
    client, headers, _, response, calls = generator
    quote = 'Use an interval between 12 and 13 吨 for observation.'
    response['judgment']['thresholds'][0].update(operator='between', value=[12, 13])
    response['rule_quotes']['watch'] = quote
    result = client.post(endpoint('/generate'), headers=headers,
        json={'model_id': 'synthetic-chat', 'text': TEXT + ' ' + quote, 'base_version': 0})
    assert result.status_code == 200, result.text
    assert len(calls) == 1 and rows() == []
    assert result.json()['bindings'][0]['unit'] == '吨'
