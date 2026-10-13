"""The binding model cannot change the task understood in the small context."""
from copy import deepcopy
import json
import httpx
from structured_data.field_resolver import ask_model


def task():
    return dict(intent='calculation',decision='resolved',
                fields=[dict(role='v1',label='最高值',candidate_ids=[],operation='max'),dict(role='v2',label='平均值',candidate_ids=[],operation='mean')],
                query={'operation':'statistics'},calculation={'expression':'v1-v2','unit':'原表未注明单位'},
                time_scope=dict(kind='unbounded',evidence=[],explanation='未限定时间'),presentation={})


def configure(tmp_path,monkeypatch,responses):
    (tmp_path/'model-settings.json').write_text(json.dumps({'chat':{'api_key':'test-only','model':'test','base_url':'https://model.invalid/v1'}}))
    calls=[]
    def post(self,url,**kw):
        calls.append(deepcopy(kw['json']))
        return httpx.Response(200,json={'choices':[{'finish_reason':'stop','message':{'content':json.dumps(responses.pop(0))}}]})
    monkeypatch.setattr(httpx.Client,'post',post)
    return calls


def test_binding_cannot_overwrite_operations_time_or_formula(tmp_path,monkeypatch):
    semantic=task();bad_binding=dict(intent='lookup',decision='resolved',
        fields=[dict(role='v1',candidate_ids=['p'],operation='min'),dict(role='v2',candidate_ids=['p'],operation='min')],
        query={'operation':'min'},time_scope={'kind':'range','start_date':'2022-01-01','end_date':'2022-01-31'},
        calculation={'expression':'v1+v2','unit':'原表未注明单位'},presentation={'analysis':'trend','chart':True})
    calls=configure(tmp_path,monkeypatch,[{"mode":"continue","resolved_question":"最高比平均高多少"},semantic,bad_binding])
    out=ask_model({'question':'最高比平均高多少','previous_task':{'original_question':'选择指标'},'catalog':[{'sheet':'sheet','columns':[{'id':'p','name':'price'}]}]},tmp_path)
    assert len(calls)==3
    first=json.loads(calls[1]['messages'][1]['content']);second=json.loads(calls[2]['messages'][1]['content'])
    assert first['_intent'] and 'catalog' not in first and first['previous_task']
    assert second['semantic_task']['query']['operation']=='statistics'
    assert out['query']['operation']=='statistics' and out['time_scope']['kind']=='unbounded'
    assert [f['operation'] for f in out['fields']]==['max','mean']
    assert out['calculation']['expression']=='v1-v2' and out['presentation']=={}


def test_bad_intent_is_repaired_before_binding(tmp_path,monkeypatch):
    bad=task();bad['query']['operation']='max'
    good=task();bound=task()
    for f in bound['fields']:f['candidate_ids']=['p']
    calls=configure(tmp_path,monkeypatch,[bad,good,bound])
    out=ask_model({'question':'两个统计之差','catalog':[]},tmp_path)
    assert len(calls)==3
    assert json.loads(calls[1]['messages'][1]['content'])['_intent']
    assert 'statistics' in calls[1]['messages'][-1]['content']
    assert out['query']['operation']=='statistics'


def test_binding_cannot_add_or_drop_a_required_role(tmp_path,monkeypatch):
    semantic=task();bad=deepcopy(semantic);bad['fields']=bad['fields'][:1];bad['fields'][0]['candidate_ids']=['p']
    good=deepcopy(semantic)
    for f in good['fields']:f['candidate_ids']=['p']
    calls=configure(tmp_path,monkeypatch,[bad,good])
    out=ask_model({'question':'两个统计之差','semantic_task':semantic,'catalog':[]},tmp_path)
    assert len(calls)==2 and len(out['fields'])==2
    assert 'preserve exactly' in calls[1]['messages'][-1]['content']


def test_directory_source_scope_is_model_selected_from_complete_summary(tmp_path,monkeypatch):
    scope={'sheets':['主来源'],'reason':'对象对应的原始指标表'}
    result={'intent':'metadata','decision':'resolved','fields':[],'metadata':{'series_ids':['p']}}
    calls=configure(tmp_path,monkeypatch,[scope,result])
    out=ask_model({'question':'这个对象有哪些价格口径','semantic_task':{'intent':'metadata','fields':[]},
                   'catalog_summary':[{'sheet':'主来源','metrics':['收盘价']},{'sheet':'另一个对象','metrics':['收盘价']}],
                   'catalog':[{'sheet':'主来源','columns':[{'id':'p','name':'收盘价'}]},
                              {'sheet':'另一个对象','columns':[{'id':'other','name':'收盘价'}]}]},tmp_path)
    assert out['metadata']['series_ids']==['p']
    first=json.loads(calls[0]['messages'][1]['content']);second=json.loads(calls[1]['messages'][1]['content'])
    assert first['_metadata_scope'] and len(first['catalog_summary'])==2
    assert [g['sheet'] for g in second['catalog']]==['主来源']


def test_conversational_references_resolved_before_calendar_and_field_binding(tmp_path,monkeypatch):
    old=dict(kind='range',start_date='2023-09-01',end_date='2023-09-30',dates=[],period=None,evidence=['九月'],explanation='已讨论期间')
    semantic=task();semantic['time_scope']=dict(old,evidence=['这个月的统计','九月'])
    bound=deepcopy(semantic)
    for f in bound['fields']:f['candidate_ids']=['p']
    calls=configure(tmp_path,monkeypatch,[{'mode':'continue','resolved_question':'讨论中的2023年9月统计'},semantic,bound])
    out=ask_model({'question':'这个月的统计','catalog':[],'reference_date':'2026-10-10',
                   'previous_task':{'user_messages':['九月'],'task_requirements':{'time_scope':old}}},tmp_path)
    context=json.loads(calls[0]['messages'][1]['content']);intent=json.loads(calls[1]['messages'][1]['content'])
    assert context['_context_resolution'] and 'reference_date' not in context
    assert intent['question']=='这个月的统计' and intent['contextual_question']=='讨论中的2023年9月统计'
    assert out['time_scope']['start_date']=='2023-09-01' and len(calls)==3
    assert out['time_scope']['evidence']==['这个月的统计','九月']


def test_ui_confirmation_metadata_is_not_human_time_evidence(tmp_path,monkeypatch):
    semantic=task();semantic['time_scope']=dict(kind='range',start_date='2022-01-01',end_date='2022-01-31',evidence=['整个一月','2022年的'],explanation='所问的一月')
    bound=deepcopy(semantic)
    for f in bound['fields']:f['candidate_ids']=['p']
    configure(tmp_path,monkeypatch,[semantic,bound])
    out=ask_model({'question':'整个一月\n用户补充：2022年的','catalog':[], 'confirmed_sources':[{'series_id':'p','sheet':'预测2028'}]},tmp_path)
    assert out['time_scope']['evidence']==['整个一月','2022年的']
    assert out['time_scope']['start_date']=='2022-01-01'
