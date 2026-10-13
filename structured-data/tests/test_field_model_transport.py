import json
import httpx
import pytest
from structured_data.field_resolver import ask_model


def setup_model(tmp_path, monkeypatch, replies):
    (tmp_path/'model-settings.json').write_text(json.dumps({'chat':{'api_key':'test-only','model':'test','base_url':'https://model.invalid/v1'}}))
    calls=[]
    def post(self,url,**kwargs):
        calls.append(kwargs['json'])
        response=replies.pop(0)
        try:
            value=json.loads(response)
            if isinstance(value,dict) and 'fields' in value:value.setdefault('time_scope',{'kind':'unbounded','evidence':[],'explanation':'test'})
            response=json.dumps(value)
        except json.JSONDecodeError:pass
        return httpx.Response(200,json={'choices':[{'message':{'content':response},'finish_reason':'stop'}]})
    monkeypatch.setattr(httpx.Client,'post',post)
    return calls


def test_multiple_plans_are_not_guessed_and_retry_is_bounded(tmp_path,monkeypatch):
    valid={'fields':[{'candidate_ids':['c1']}],'intent':'lookup','decision':'resolved'}
    calls=setup_model(tmp_path,monkeypatch,['说明\n```json\n{}\n```\n```json\n{}\n```',json.dumps(valid)])
    assert ask_model({'question':'测试','_catalog_selection':True},tmp_path)['fields']==valid['fields']
    assert len(calls)==2 and 'response_format' not in calls[0]


def test_scalar_id_is_normalized_without_changing_its_value(tmp_path,monkeypatch):
    setup_model(tmp_path,monkeypatch,[json.dumps({'fields':[{'candidate_ids':'c230'}]})])
    assert ask_model({'_catalog_selection':True},tmp_path)['fields'][0]['candidate_ids']==['c230']


def test_format_failure_reports_real_cause_and_stops(tmp_path,monkeypatch):
    calls=setup_model(tmp_path,monkeypatch,['not JSON','still not JSON'])
    with pytest.raises(ValueError,match='输出格式错误'):ask_model({'_catalog_selection':True},tmp_path)
    assert len(calls)==2


def test_empty_string_candidate_is_missing_not_an_invented_identifier(tmp_path,monkeypatch):
    setup_model(tmp_path,monkeypatch,[json.dumps({'fields':[{'candidate_ids':['']}],'local_data_missing':True})])
    assert ask_model({'_catalog_selection':True},tmp_path)['fields'][0]['candidate_ids']==[]


def test_candidate_written_as_json_key_requires_format_repair(tmp_path,monkeypatch):
    bad={'fields':[{'role':'v1','candidate_ids':['c196'],'c200':'另一种价格'}]}
    fixed={'fields':[{'role':'v1','candidate_ids':['c196','c200'],'reason':'价格口径不同'}]}
    calls=setup_model(tmp_path,monkeypatch,[json.dumps(bad),json.dumps(fixed)])
    result=ask_model({'_catalog_selection':True},tmp_path)
    assert result['fields'][0]['candidate_ids']==['c196','c200']
    assert len(calls)==2 and '候选ID全部放入' in calls[1]['messages'][-1]['content']


def test_catalogue_batches_cover_all_columns_without_out_of_batch_ids():
    from structured_data.field_resolver import narrow_catalogue
    catalogue=[{'sheet':'表','columns':[{'id':f'id{i}','name':'字段'+str(i),'details':'长表头'*200} for i in range(45)]}]
    seen=[]
    def select(payload,root):
        cols=[c for g in payload['catalog'] for c in g['columns']];seen.extend(c['id'] for c in cols)
        assert len(json.dumps(payload,ensure_ascii=False))<15000
        return {'fields':[{'candidate_ids':[c['id'] for c in cols if c['id']=='id44']}]}
    result=narrow_catalogue({'catalog':catalogue,'question':'最后的字段'},None,select)
    assert sorted(seen)==sorted(f'id{i}' for i in range(45))
    assert [c['id'] for g in result['catalog'] for c in g['columns']]==['id44']
    with pytest.raises(ValueError,match='批次外'):
        narrow_catalogue({'catalog':catalogue,'question':'字段'},None,lambda p,r:{'fields':[{'candidate_ids':['invented']}]})


def test_bounded_catalogue_keeps_complete_siblings_after_model_screening():
    from structured_data.field_resolver import narrow_catalogue
    catalogue=[{'sheet':'实际来源','columns':[{'id':sid,'name':'同名'} for sid in ['a','b','c','d']]}]
    duplicate_sources=[[{'series_id':'a'},{'series_id':'b'}],[{'series_id':'c'},{'series_id':'d'}]]
    result=narrow_catalogue({'catalog':catalogue,'question':'不要第一来源，沿用上下文',
        'duplicate_sources':duplicate_sources},None,lambda payload,root:{'fields':[{'candidate_ids':['b']}]})
    assert [c['id'] for group in result['catalog'] for c in group['columns']]==['a','b']
    assert result['duplicate_sources']==[duplicate_sources[0]]
    assert result['question']=='不要第一来源，沿用上下文'


def test_screening_projects_every_metadata_id_then_restores_selected_deep_evidence():
    from structured_data.field_resolver import narrow_catalogue
    deep={'source_column':'B','headers':[{'cell':'B1','value':'原始依赖'}],
          'formula_source':{'references':[{'sheet':'输入表','reference':'D9',
            'fields':[{'series_id':'dependency','metric':'来源输入','column':'D',
                       'source_context':{'notes':'原件证据'*800}}]}]}}
    columns=[{'id':f'field{i}','name':f'真实字段{i}','dimensions':{'原表列':'B'},
              'source_context':deep,'date_mode':'date'} for i in range(500)]
    columns.append({'id':'sibling','name':'真实字段499','source_context':deep})
    seen=[]
    def model(payload,root):
        assert payload['_catalog_selection']
        projected=[column for group in payload['catalog'] for column in group['columns']]
        seen.extend(column['id'] for column in projected)
        assert all('source_context' not in column for column in projected)
        assert all(column['source_column']=='B' and column['header_cells']==['B1'] for column in projected)
        assert projected[0]['dependencies'][0]['fields'][0]['id']=='dependency'
        return {'fields':[{'candidate_ids':['field499'] if any(c['id']=='field499' for c in projected) else []}]}
    payload={'question':'根据完整问题确定实际相关来源，不用首个字段',
             'catalog':[{'sheet':'原始数据','columns':columns}],
             'duplicate_sources':[[{'series_id':'field499'},{'series_id':'sibling'}]]}
    result=narrow_catalogue(payload,None,model)
    assert set(seen)=={column['id'] for column in columns}
    selected=[c for group in result['catalog'] for c in group['columns']]
    assert {c['id'] for c in selected}=={'field499','sibling'}
    assert all(column['source_context']==deep for column in selected)
    assert result['catalog_scan']['scope']=='all supplied fields screened'


def test_over_budget_source_scope_is_model_planned_from_complete_real_summary():
    from structured_data.field_resolver import narrow_catalogue
    columns=[{'id':f'id{i}','name':'完整真实字段'+str(i)+'来源说明'*30,
              'unit':'件','date_mode':'date'} for i in range(1500)]
    payload={'question':'不要另一种来源，比较上下文要求的字段',
             'catalog':[{'sheet':'完整来源甲','columns':columns},
                        {'sheet':'真实来源乙','columns':[{'id':'chosen','name':'实际值','unit':'件'}]}]}
    calls=[]
    def model(request,root):
        calls.append(request)
        assert request['question']==payload['question']
        if request.get('_metadata_scope'):
            assert {g['sheet'] for g in request['catalog_summary']}=={'完整来源甲','真实来源乙'}
            assert request['catalog_summary'][0]['field_count']==1500
            assert request['catalog_summary'][0]['metrics']==sorted(c['name'] for c in columns)
            return {'sheets':['真实来源乙'],'reason':'模型根据完整任务和真实摘要明确实际来源'}
        assert request['_catalog_selection']
        assert [c['id'] for group in request['catalog'] for c in group['columns']]==['chosen']
        return {'fields':[{'candidate_ids':['chosen']}]}
    result=narrow_catalogue(payload,None,model)
    assert len(calls)==2 and result['catalog_scan']['source_scope_plan']['sheets']==['真实来源乙']
    assert result['catalog_scan']['source_count']==2 and result['catalog_scan']['screened_fields']==1
    with pytest.raises(ValueError,match='有效实际工作表范围'):
        narrow_catalogue(payload,None,lambda p,r:{'sheets':['not-a-source']})


def test_thinking_option_is_explicit_and_preserved_not_guessed_from_model_name(tmp_path,monkeypatch):
    from structured_data.model_settings import save_model,read_model
    calls=setup_model(tmp_path,monkeypatch,[json.dumps({'fields':[]})])
    assert save_model(tmp_path,{'model':'configured-provider','base_url':'https://model.invalid/v1','enable_thinking':False})['model']=='configured-provider'
    assert read_model(tmp_path)['enable_thinking'] is False
    ask_model({'_catalog_selection':True},tmp_path)
    assert calls[0]['enable_thinking'] is False
    save_model(tmp_path,{'model':'another-model','base_url':'https://model.invalid/v1'})
    assert read_model(tmp_path)['enable_thinking'] is False
    with pytest.raises(ValueError):save_model(tmp_path,{'model':'test','base_url':'https://model.invalid/v1','enable_thinking':'false'})


def test_source_binding_review_is_bounded_and_preserves_full_source_context(tmp_path,monkeypatch):
    import time
    from structured_data.model_settings import save_model
    review={'decision':'resolved','bindings':[{'considered_ids':['a','b'],'selected_ids':['b'],'reason':'根据完整问题和实际依赖选择'}]}
    calls=setup_model(tmp_path,monkeypatch,['not JSON',json.dumps(review)])
    save_model(tmp_path,{'model':'test','base_url':'https://model.invalid/v1','enable_thinking':True})
    payload={'_source_binding':True,'_source_binding_deadline':time.monotonic()+20,
             'question':'不要上一组，要刚才确认的来源',
             'source_groups':[[{'series_id':'a','source_context':{'headers':[{'cell':'C1','value':'来源甲'}]}},
                               {'series_id':'b','source_context':{'headers':[{'cell':'G1','value':'来源乙'}]}}]],
             'requested_series_ids':['b']}
    assert ask_model(payload,tmp_path)==review
    assert len(calls)==2 and all(call['enable_thinking'] is False and call['max_tokens']==2000 for call in calls)
    actual=json.loads(calls[0]['messages'][1]['content'])
    assert actual['question']==payload['question'] and actual['source_groups']==payload['source_groups']
    assert '_source_binding_deadline' not in actual
    assert 'bindings' in calls[1]['messages'][-1]['content']
    expired=dict(payload,_source_binding_deadline=time.monotonic()-1)
    with pytest.raises(ValueError,match='时间预算'):ask_model(expired,tmp_path)
    assert len(calls)==2


def test_assistant_source_dates_and_model_subtask_are_not_user_time_evidence(tmp_path,monkeypatch):
    invalid={'intent':'lookup','decision':'resolved','fields':[{'role':'v1','label':'指标','candidate_ids':[]}],
             'query':{'operation':'rows'},'time_scope':{'kind':'dates','dates':['2031-01-02'],
               'evidence':['2031年1月2日'],'explanation':'模型错误地使用了来源日期'},'presentation':{}}
    calls=setup_model(tmp_path,monkeypatch,[json.dumps(invalid),json.dumps(invalid)])
    payload={'_intent':True,'question':'请解释这个指标', 'model_subtask':'读取2031年1月2日的数据',
             'source_conversation':[{'role':'user','content':'请看文件'},
                                    {'role':'assistant','content':'原件中有2031年1月2日的记录'}]}
    with pytest.raises(ValueError,match='依据必须逐字来自用户'):
        ask_model(payload,tmp_path)
    assert len(calls)==2
    submitted=json.loads(calls[0]['messages'][1]['content'])
    assert submitted['model_subtask']==payload['model_subtask'] and submitted['source_conversation']==payload['source_conversation']


def test_unified_plan_schema_repairs_without_a_separate_time_interpreter(tmp_path,monkeypatch):
    fixed={'intent':'lookup','decision':'resolved','fields':[{'role':'v1','candidate_ids':[]}],
           'time_scope':{'kind':'range','evidence':['今年'],'explanation':'当前自然年','start_date':'2026-01-01','end_date':'2026-12-31'}}
    calls=setup_model(tmp_path,monkeypatch,['{}',json.dumps(fixed)])
    result=ask_model({'question':'今年合计多少','_intent':True},tmp_path)
    assert result['time_scope']['end_date']=='2026-12-31'
    assert len(calls)==2
    assert all('_time_scope' not in json.loads(c['messages'][1]['content']) for c in calls)


def test_directory_screening_does_not_force_previous_topic_into_results():
    from structured_data.field_resolver import narrow_catalogue
    payload={'question':'换个对象有哪些字段','task_requirements':{'metadata':True},
             'previous_task':{'original_question':'先前对象的价格'},
             'prior_series':[{'id':'old'}],
             'catalog':[{'sheet':'来源','columns':[{'id':'old','name':'前一对象'},{'id':'new','name':'新对象'}]}]}
    def select(request,root):
        assert request['previous_task']==payload['previous_task']
        return {'metadata':{'series_ids':['new']}}
    out=narrow_catalogue(payload,None,select)
    assert [c['id'] for g in out['catalog'] for c in g['columns']]==['new']
