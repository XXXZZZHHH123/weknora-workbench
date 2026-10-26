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


def test_new_explicit_product_reuses_model_analysis_without_inheriting_other_product_versions(generator):
    client, headers, _, _, calls = generator
    first = generate(client, headers).json()
    assert apply(client, headers, first).status_code == 200
    response = client.post(endpoint('/generate'), headers=headers,
        json={'model_id': 'synthetic-chat', 'text': TEXT, 'base_version': 0, 'variety': 'XY'})
    assert response.status_code == 200, response.text
    assert response.json()['base_version'] == 0
    assert apply(client, headers, response.json()).status_code == 200
    assert [(row.variety, row.version) for row in rows()] == [('SN', 1), ('XY', 1)]
    managed = client.get(endpoint('/manage'), headers=headers).json()
    assert managed['variety'] == 'XY' and managed['current']['variety'] == 'XY'
    assert client.get(endpoint('/manage?variety=SN'), headers=headers).json()['current']['variety'] == 'SN'
    assert len(calls) == 2

@pytest.mark.parametrize('change',[
    lambda d:d['judgment']['thresholds'][0].update(series_id='foreign'),
    lambda d:d['judgment']['contradiction']['bull'].update(evidence=None)])
def test_invalid_model_draft_never_creates_version(generator,change):
    client,headers,_,response,_=generator;change(response)
    result=generate(client,headers);assert result.status_code==422,result.text
    assert rows()==[]


@pytest.mark.parametrize('change', [
    lambda d: d['binding_units'].update({SERIES: 'kg'}),
    lambda d: d['judgment']['thresholds'][0].update(value=99999),
])
def test_invalid_quantitative_item_is_not_executable_after_one_bounded_repair(generator, change):
    client, headers, _, response, calls = generator; change(response)
    result = generate(client, headers)
    assert result.status_code == 200, result.text
    assert len(calls) == 2 and result.json()['repair_attempts'] == 1
    assert '上次结构化校验反馈' in calls[1]['prompt']
    preview = result.json()
    assert preview['validation_issues'] and preview['unresolved'] and rows() == []
    assert apply(client, headers, preview).status_code == 200
    assert rows()[0].status == '草稿' and rows()[0].thresholds == [] and rows()[0].source_note == TEXT


def test_single_repair_can_fix_exact_quote_and_never_loops(generator, monkeypatch):
    client, headers, _, response, calls = generator
    broken = copy.deepcopy(response); broken['rule_quotes']['watch'] = 'paraphrased source'
    async def complete(scope, model, prompt):
        calls.append({'prompt': prompt})
        return copy.deepcopy(broken if len(calls) == 1 else response)
    monkeypatch.setattr(generation, 'completion', complete)
    result = generate(client, headers)
    assert result.status_code == 200 and len(calls) == 2
    assert result.json()['validation_issues'] == [] and result.json()['repair_attempts'] == 1


def test_forbidden_cost_fact_feedback_names_every_field_and_never_saves_unchanged_facts(generator):
    client, headers, _, response, calls = generator
    response['judgment']['cost_line'] = {'type': 'cash', 'value': 7, 'source': 'untrusted',
        'items': [{'desc': 'Cost', 'series_id': SERIES, 'value': 7}]}
    result = generate(client, headers)
    assert result.status_code == 422 and len(calls) == 2 and rows() == []
    paths = {'/judgment/cost_line/value', '/judgment/cost_line/source',
             '/judgment/cost_line/items/0/value'}
    assert {issue['path'] for issue in result.json()['detail']['issues']} == paths
    assert all(path in calls[1]['prompt'] for path in paths)
    assert 'JSON null' in calls[1]['prompt']


def test_repair_cannot_drop_a_previously_verified_rule(generator, monkeypatch):
    client, headers, _, response, calls = generator
    broken = copy.deepcopy(response)
    broken['judgment']['thresholds'].append({**response['judgment']['thresholds'][0], 'id': 'bad', 'value': 99999})
    broken['rule_quotes']['bad'] = response['rule_quotes']['watch']
    async def complete(scope, model, prompt):
        calls.append({'prompt': prompt})
        if len(calls) == 1: return broken
        return {'judgment': {'contradiction': {}}, 'binding_units': {}, 'rule_quotes': {}, 'unresolved': []}
    monkeypatch.setattr(generation, 'completion', complete)
    result = generate(client, headers)
    assert result.status_code == 200 and len(calls) == 2
    assert any(item['code'] == 'repair_changed_verified_rule' for item in result.json()['validation_issues'])
    assert apply(client, headers, result.json()).status_code == 200
    assert [item['id'] for item in rows()[0].thresholds] == ['watch']


def test_repair_transport_failure_keeps_the_already_verified_partial_draft(generator, monkeypatch):
    from fastapi import HTTPException
    client, headers, _, response, calls = generator
    broken = copy.deepcopy(response)
    broken['judgment']['thresholds'].append({**response['judgment']['thresholds'][0], 'id': 'pending', 'value': 99999})
    broken['rule_quotes']['pending'] = response['rule_quotes']['watch']
    async def complete(scope, model, prompt):
        calls.append({'prompt': prompt})
        if len(calls) == 1: return broken
        raise HTTPException(503, 'Synthetic unavailable repair')
    monkeypatch.setattr(generation, 'completion', complete)
    result = generate(client, headers)
    assert result.status_code == 200 and len(calls) == 2
    assert any(item['code'] == 'repair_unavailable' for item in result.json()['validation_issues'])
    assert apply(client, headers, result.json()).status_code == 200
    assert [item['id'] for item in rows()[0].thresholds] == ['watch']
    assert any('pending' in item['text'] for item in rows()[0].unstructured)


def test_source_access_failure_during_repair_never_releases_a_partial_preview(generator, monkeypatch):
    from fastapi import HTTPException
    client, headers, _, response, calls = generator
    broken = copy.deepcopy(response); broken['judgment']['thresholds'][0]['value'] = 99999
    async def complete(scope, model, prompt):
        calls.append({'prompt': prompt})
        if len(calls) == 1: return broken
        raise HTTPException(403, 'Synthetic access revoked')
    monkeypatch.setattr(generation, 'completion', complete)
    result = generate(client, headers)
    assert result.status_code == 403 and len(calls) == 2 and rows() == []
    assert generation.previews.items == {}

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


def test_source_index_compression_preserves_every_field_and_full_source_without_selection(monkeypatch):
    import json
    catalog = [{'id': 'real-' + str(index), 'name': '量' * 65 + str(index),
        'unit': 'kg', 'frequency': '日', 'sheet': 'Measurements', 'dimensions': {'scope': 'all'},
        'source_file': 'current-source-' + 'a' * 140 + '.xlsx', 'source_kind': 'primary',
        'knowledge_base_id': 'library-' + '1' * 36, 'source_version': 'f' * 64}
        for index in range(300)]
    assert len(generation.catalog_json(catalog).encode()) > generation.PROMPT_BYTES
    async def no_selection(*args):
        raise AssertionError('A losslessly indexed full catalog must not call a selector')
    monkeypatch.setattr(generation, 'completion', no_selection)
    selected, partial = asyncio.run(generation.choose_catalog(None, 'm', catalog, TEXT))
    assert selected == catalog and partial is False
    prompt = generation.make_prompt(generation.analysis_protocol(False), selected, TEXT)
    assert len(prompt.encode()) <= generation.PROMPT_BYTES and TEXT in prompt
    encoded = prompt.split('真实字段目录：', 1)[1].split('\n完整原文：', 1)[0]
    grouped = json.loads(encoded)
    restored = [{**{key: value for key, value in field.items() if key != 'source_ref'},
                 **grouped['sources'][field['source_ref']]} for field in grouped['indicators']]
    assert restored == catalog and len(grouped['sources']) == 1
    assert 'source_ref不是指标ID' in prompt


def test_source_index_preserves_nulls_and_distinct_source_versions_without_key_collision():
    import json
    source = {'source_file': 'a' * 1024, 'source_kind': None, 'knowledge_base_id': 'library'}
    catalog = [{'id': 'real-' + str(index), 'unit': None if index == 0 else 'kg',
                **source, 'source_version': 'v1' if index < 2 else 'v2'} for index in range(4)]
    encoded, grouped = generation.prompt_catalog(catalog)
    assert grouped
    doc = json.loads(encoded)
    assert len(doc['sources']) == 2 and doc['indicators'][0]['unit'] is None
    assert all(source['source_kind'] is None for source in doc['sources'].values())
    catalog[0]['source_ref'] = 'actual field metadata'
    encoded, grouped = generation.prompt_catalog(catalog)
    assert not grouped and json.loads(encoded) == catalog


def test_v2_generation_snapshot_and_save_use_metadata_and_result_storage_without_loading_observations(canonical, monkeypatch):
    from tin.integration.identity import Identity
    from tin.integration.workbench_scope import WorkbenchScope, current_workbench
    from tin.integration import kb_facts
    _, _, _, _ = canonical
    identity = Identity('researcher', 'fixture@test', 'Researcher', 17, 'owner')
    scope = WorkbenchScope(identity, FIRST, KB, 17, {'workspace_mode': 'isolated', 'can_edit': True},
        'Bearer synthetic', scope_version=2, sources_revision=1)
    catalog = {SERIES: {'series_id': SERIES, 'name': 'Synthetic quantity', 'unit': '吨',
        'knowledge_base_id': KB, 'knowledge_base_tenant_id': 17}}
    metadata_calls = []
    def metadata(verified):
        assert verified is scope
        metadata_calls.append(verified.workbench_id)
        return {'catalog': catalog, 'version': 'f' * 64, 'datasets': []}
    def no_facts(*args, **kwargs):
        raise AssertionError('Judgment metadata/save must not open a fact projection')
    monkeypatch.setattr(kb_facts, 'catalog_snapshot', metadata, raising=False)
    monkeypatch.setattr(generation, 'SessionLocal', no_facts)
    token = current_workbench.set(scope)
    try:
        before = generation.snapshot()
        assert before['base_version'] == 0 and before['fact_version'] == 'f' * 64
        assert before['catalog'][0]['id'] == SERIES and 'observations' not in before
        saved = generation.persist(scope, {'fact_version': before['fact_version'], 'document': document(),
            'text': TEXT, 'source_name': 'synthetic.txt', 'base_version': 0,
            'written_at': '2026-09-18', 'model_id': 'synthetic-model'})
        assert saved['status'] == '草稿' and saved['version'] == 1
        assert generation.snapshot()['base_version'] == 1
        assert len(metadata_calls) == 3
        with isolated_session_factory(17, FIRST)() as session:
            from tin.models import Observation, Indicator
            assert session.scalar(select(Observation)) is None
            assert session.scalar(select(Indicator)) is None
    finally:
        current_workbench.reset(token)


@pytest.mark.parametrize('finish_reason', ['length', 'max_tokens'])
def test_truncated_native_output_is_not_repaired_or_saved(monkeypatch, finish_reason):
    from fastapi import HTTPException
    monkeypatch.setattr(generation.settings, 'weknora_api_url', 'http://native.test')
    original = httpx.AsyncClient
    def upstream(request):
        return httpx.Response(200, json={'success': True, 'data': {
            'model_id': 'm', 'content': '{"judgment":', 'finish_reason': finish_reason}})
    monkeypatch.setattr(generation.httpx, 'AsyncClient', lambda *args, **kwargs: original(*args, **{
        **kwargs, 'transport': httpx.MockTransport(upstream)}))
    scope = SimpleNamespace(workbench_id=FIRST, knowledge_base_id=KB, authorization='Bearer synthetic',
        identity=SimpleNamespace(tenant_id=17))
    with pytest.raises(HTTPException) as caught:
        asyncio.run(generation.completion(scope, 'm', 'full source'))
    assert caught.value.status_code == 422 and '长度上限' in caught.value.detail


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


def test_actual_model_request_uses_the_exact_pricing_power_schema(generator):
    import json
    from tin.schemas.judgment import PricingPower

    client, headers, _, _, calls = generator
    result = generate(client, headers)
    assert result.status_code == 200, result.text
    assert len(calls) == 1 and rows() == []
    prompt = calls[0]['prompt']
    marker = '定价权对象的 JSON Schema：\n'
    assert marker in prompt
    schema, _ = json.JSONDecoder().raw_decode(prompt.split(marker, 1)[1])
    assert schema == PricingPower.model_json_schema()
    category = schema['properties']['category']
    assert any(item.get('type') == 'null' for item in category['anyOf'])
    assert next(item['enum'] for item in category['anyOf'] if 'enum' in item) == [
        '有LME外盘', '纯宏观', '无外盘']
    assert 'category仅有LME' not in prompt
    assert SERIES not in json.dumps(schema)


@pytest.mark.parametrize('category', ['unsupported-category', True, {'category': 'unsupported-category'}])
def test_pricing_power_schema_keeps_invalid_model_categories_rejected(generator, category):
    client, headers, _, response, calls = generator
    response['judgment']['pricing_power'] = {'category': category}
    result = generate(client, headers)
    assert result.status_code == 422, result.text
    detail = result.json()['detail']
    assert detail['code'] == 'judgment_validation'
    assert any(issue['code'] == 'field_schema' and issue['path'] == '/judgment/pricing_power/category' for issue in detail['issues'])
    assert len(calls) == 2 and rows() == [] and not generation.previews.items


@pytest.mark.parametrize('value', ['12', True, None, {'minimum': 12}, 'missing'])
def test_invalid_numeric_threshold_stays_unmodified_in_pending_items_and_never_executes(generator, value):
    import json
    from tin.judgments.analysis import AnalysisError, validate_analysis
    client, headers, _, response, calls = generator
    rule = response['judgment']['thresholds'][0]
    if value == 'missing':
        rule.pop('value')
    else:
        rule['value'] = value
    original = copy.deepcopy(rule)
    # The strict final guard must still reject the exact untrusted document.
    with pytest.raises(AnalysisError, match='有限数字'):
        validate_analysis(response, TEXT, [{'id': SERIES, 'unit': '吨'}], variety='SN', actor='researcher')
    result = generate(client, headers)
    assert result.status_code == 200, result.text
    preview = result.json()
    assert len(calls) == 2 and rows() == [] and preview['repair_attempts'] == 1
    assert any(item['code'] == 'rule_number_type' and item['path'] == '/judgment/thresholds/0/value' for item in preview['validation_issues'])
    pending = next(item for item in preview['unresolved'] if item['section'] == 'thresholds[0]待确认')
    assert json.loads(pending['text'].split('未通过校验的模型候选（不执行）：', 1)[1]) == original
    assert 'Stocks above 12 吨' in pending['text'] and '有限数字' in pending['reason']
    assert apply(client, headers, preview).status_code == 200
    saved = rows()[0]
    assert saved.status == '草稿' and saved.thresholds == [] and saved.source_note == TEXT


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


def test_authorized_macro_threshold_and_original_monthly_falsifier_are_valid_preview(generator, monkeypatch):
    from tin.judgments.prompt import SOURCE_BINDING_PROTOCOL
    client, headers, _, response, calls = generator
    macro = 'kb-' + 'c' * 48
    original_snapshot = generation.snapshot
    def mixed_snapshot():
        return {**original_snapshot(), 'catalog': [
            {'id': SERIES, 'name': 'Monthly measured stocks', 'unit': '吨', 'frequency': '月',
             'source_kind': 'primary', 'knowledge_base_id': KB},
            {'id': macro, 'name': 'External risk gauge', 'unit': 'points', 'frequency': '日',
             'source_kind': 'macro', 'knowledge_base_id': 'authorized-reference-library'}]}
    monkeypatch.setattr(generation, 'snapshot', mixed_snapshot)
    risk_quote = 'External risk gauge above 7 points requires review.'
    monthly_quote = 'Monthly stocks above 12 吨 for 2 consecutive monthly periods invalidate the claim.'
    text = TEXT + ' ' + risk_quote + ' ' + monthly_quote
    response['judgment']['thresholds'].append({'id': 'external-risk', 'name': 'External risk boundary',
        'series_id': macro, 'operator': '>', 'value': 7, 'type': '风控线', 'horizon': '短期'})
    response['judgment']['falsifiers'] = [{'id': 'monthly-evidence', 'desc': 'Original monthly condition',
        'series_id': SERIES, 'expression': 'value > 12', 'unit_confirmed': '吨',
        'consecutive_periods': 2, 'side': 'bull', 'note': 'Use the original monthly cadence.'}]
    response['binding_units'][macro] = 'points'
    response['rule_quotes'].update({'external-risk': risk_quote, 'monthly-evidence': monthly_quote})
    result = client.post(endpoint('/generate'), headers=headers,
        json={'model_id': 'synthetic-chat', 'text': text, 'base_version': 0})
    assert result.status_code == 200, result.text
    assert len(calls) == 1 and rows() == []
    prompt = calls[0]['prompt']
    assert SOURCE_BINDING_PROTOCOL in prompt and macro in prompt
    assert 'primary和macro只是来源分类' in prompt and '观测频率与研究期限分开处理' in prompt
    changed = {item['field']: item['after'] for item in result.json()['diff']['fields']}
    assert len(changed['thresholds']) == 2
    assert changed['thresholds'][1]['series_id'] == macro and changed['thresholds'][1]['value'] == 7
    assert changed['falsifiers'][0]['consecutive_periods'] == 2
    assert 'monthly' in changed['falsifiers'][0]['note']


def test_catalog_selector_keeps_authorized_macro_and_original_cadence_in_every_batch(monkeypatch):
    from tin.judgments.prompt import SOURCE_BINDING_PROTOCOL
    import json
    catalog = [{'id': 'real-' + str(index), 'unit': 'kg', 'frequency': '月',
                'source_kind': 'macro' if index == 0 else 'primary', 'name': '量' * 1400 + str(index)}
               for index in range(53)]
    observed = []
    async def choose(scope, model, prompt):
        assert SOURCE_BINDING_PROTOCOL in prompt and TEXT in prompt
        document = json.loads(prompt.split('真实字段目录：', 1)[1].split('\n完整原文：', 1)[0])
        if isinstance(document, dict):
            fields = [{**{k: v for k, v in item.items() if k != 'source_ref'},
                       **document['sources'][item['source_ref']]} for item in document['indicators']]
        else:
            fields = document
        observed.extend(fields)
        return {'series_ids': ['real-0'] if any(field['id'] == 'real-0' for field in fields) else []}
    monkeypatch.setattr(generation, 'completion', choose)
    selected, partial = asyncio.run(generation.choose_catalog(None, 'm', catalog, TEXT))
    assert partial and selected == [catalog[0]] and observed == catalog


@pytest.mark.parametrize('invalid', ['unit', 'foreign', 'periods'])
def test_macro_or_low_frequency_never_bypasses_source_unit_and_quote_guards(invalid):
    from tin.judgments.analysis import AnalysisError, validate_analysis
    macro = 'kb-' + 'c' * 48
    quote = 'The external series above 7 points for 2 monthly periods invalidates the claim.'
    body = {'judgment': {'contradiction': {}, 'falsifiers': [{'id': 'external-condition',
        'desc': 'Original condition', 'series_id': macro, 'expression': 'value > 7',
        'unit_confirmed': 'points', 'consecutive_periods': 2, 'side': 'bull'}]},
        'binding_units': {macro: 'points'}, 'rule_quotes': {'external-condition': quote}, 'unresolved': []}
    catalog = [{'id': macro, 'unit': 'points', 'source_kind': 'macro', 'frequency': '月'}]
    if invalid == 'unit':
        body['binding_units'][macro] = 'kg'
    elif invalid == 'foreign':
        catalog = []
    else:
        body['judgment']['falsifiers'][0]['consecutive_periods'] = 3
    with pytest.raises(AnalysisError):
        validate_analysis(body, quote, catalog, variety='TEST', actor='synthetic')


def test_selection_empty_example_keeps_one_object_shape_on_every_complete_batch(monkeypatch):
    """The model-facing empty example passes the same strict selector as real output."""
    import json
    catalog = [{'id': 'real-' + str(index), 'unit': 'kg', 'name': '量' * 1400 + str(index)}
               for index in range(53)]
    observed = []

    async def choose(scope, model, prompt):
        marker = '空结果 JSON 示例：\n'
        assert marker in prompt, 'The selection protocol must provide an unambiguous empty object example'
        example, _ = json.JSONDecoder().raw_decode(prompt.split(marker, 1)[1])
        assert isinstance(example, dict) and set(example) == {'series_ids'}
        assert example['series_ids'] == []
        fields = json.loads(prompt.split('真实字段目录：', 1)[1].split('\n完整原文：', 1)[0])
        observed.extend(field['id'] for field in fields)
        assert TEXT in prompt
        return example

    monkeypatch.setattr(generation, 'completion', choose)
    selected, partial = asyncio.run(generation.choose_catalog(None, 'm', catalog, TEXT))
    assert partial and selected == []
    assert observed == [row['id'] for row in catalog]


@pytest.mark.parametrize('response', [
    [], {'series_ids': [], 'extra': 'not part of protocol'}, {'series_ids': None},
    {'series_ids': ['unknown-id']}, {'series_ids': [True]}, {'series_ids': ['real-0'] * 33},
])
def test_selection_protocol_keeps_strict_shape_source_and_raw_size_validation(monkeypatch, response):
    from fastapi import HTTPException
    catalog = [{'id': 'real-' + str(index), 'unit': 'kg', 'name': '量' * 1400 + str(index)}
               for index in range(53)]

    async def invalid(*_):
        return copy.deepcopy(response)

    monkeypatch.setattr(generation, 'completion', invalid)
    with pytest.raises(HTTPException) as caught:
        asyncio.run(generation.choose_catalog(None, 'm', catalog, TEXT))
    assert caught.value.status_code == 422
    assert caught.value.detail == '模型未给出有效的真实指标选择'
