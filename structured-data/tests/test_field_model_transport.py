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
        return httpx.Response(200,json={'choices':[{'message':{'content':response},'finish_reason':'stop'}]})
    monkeypatch.setattr(httpx.Client,'post',post)
    return calls


def test_multiple_plans_are_not_guessed_and_retry_is_bounded(tmp_path,monkeypatch):
    valid={'fields':[{'candidate_ids':['c1']}],'intent':'lookup','decision':'resolved'}
    calls=setup_model(tmp_path,monkeypatch,['说明\n```json\n{}\n```\n```json\n{}\n```',json.dumps(valid)])
    assert ask_model({'question':'测试'},tmp_path)==valid
    assert len(calls)==2 and 'response_format' not in calls[0]


def test_scalar_id_is_normalized_without_changing_its_value(tmp_path,monkeypatch):
    setup_model(tmp_path,monkeypatch,[json.dumps({'fields':[{'candidate_ids':'c230'}]})])
    assert ask_model({},tmp_path)['fields'][0]['candidate_ids']==['c230']


def test_format_failure_reports_real_cause_and_stops(tmp_path,monkeypatch):
    calls=setup_model(tmp_path,monkeypatch,['not JSON','still not JSON'])
    with pytest.raises(ValueError,match='输出格式错误'):ask_model({},tmp_path)
    assert len(calls)==2


def test_empty_string_candidate_is_missing_not_an_invented_identifier(tmp_path,monkeypatch):
    setup_model(tmp_path,monkeypatch,[json.dumps({'fields':[{'candidate_ids':['']}],'local_data_missing':True})])
    assert ask_model({},tmp_path)['fields'][0]['candidate_ids']==[]


def test_candidate_written_as_json_key_requires_format_repair(tmp_path,monkeypatch):
    bad={'fields':[{'role':'v1','candidate_ids':['c196'],'c200':'另一种价格'}]}
    fixed={'fields':[{'role':'v1','candidate_ids':['c196','c200'],'reason':'价格口径不同'}]}
    calls=setup_model(tmp_path,monkeypatch,[json.dumps(bad),json.dumps(fixed)])
    result=ask_model({},tmp_path)
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
