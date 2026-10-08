import json
import pytest
from structured_data.field_resolver import FieldResolver
from structured_data.point_calculator import calculate
from test_catalog_aliases import catalogue


def proposal(ids, intent='lookup', expression='', unit='', decision='resolved'):
    return dict(intent=intent,decision=decision,question='选择需要的字段',fields=[dict(role=f'v{i+1}',label='字段用途',candidates=[dict(id=sid,reason='根据含义判断') for sid in group]) for i,group in enumerate(ids)],calculation=dict(expression=expression,unit=unit,explanation='单位换算'))


def test_resolve_uses_model_catalog_and_caches(catalogue):
    store,ds=catalogue;calls=[]
    def model(payload):
        calls.append(payload)
        assert payload['question']=='根据当天汇率'
        assert any(s['name']=='美元兑人民币' for sheet in payload['catalog'] for s in sheet['columns'])
        return proposal([['fx-context']])
    resolver=FieldResolver(store,model)
    plan=resolver.resolve(ds,'根据当天汇率',context_sheet='商品报价',date='2023-09-07')
    assert plan['status']=='resolved'
    data=resolver.execute(plan['resolution_id'])
    assert data['sources']['v1']['rows'][0]['source_cell']=='B2'
    assert resolver.resolve(ds,'根据当天汇率',context_sheet='商品报价',date='2023-09-07')['cached']
    assert len(calls)==1


def test_ambiguity_waits_and_selection_is_validated(catalogue):
    store,ds=catalogue;resolver=FieldResolver(store,lambda p:proposal([['fx-context','eur']],decision='clarify'))
    plan=resolver.resolve(ds,'汇率',date='2023-09-07')
    assert plan['status']=='needs_clarification'
    with pytest.raises(ValueError,match='尚未确认'):resolver.execute(plan['resolution_id'])
    with pytest.raises(ValueError):resolver.confirm(plan['resolution_id'],{'v1':'price'})
    with pytest.raises(ValueError):resolver.confirm(plan['resolution_id'],{'v9':'eur'})
    chosen=resolver.confirm(plan['resolution_id'],{'v1':'eur'})
    assert chosen['fields'][0]['candidates'][0]['metric']=='欧元兑人民币'
    assert resolver.execute(chosen['resolution_id'])['sources']['v1']['series']['series_id']=='eur'


def test_calculation_replanned_after_choice(catalogue):
    store,ds=catalogue;calls=[]
    def model(payload):
        calls.append(payload)
        if 'confirmed_selections' in payload:
            assert payload['confirmed_selections']=={'v1':'price','v2':'fx-context'}
            return proposal([['price'],['fx-context']],'calculation','v1*0.01*v2','元/磅')
        return proposal([['price','usd-price'],['fx-context']],'calculation','v1*v2','元/吨',decision='clarify')
    resolver=FieldResolver(store,model);plan=resolver.resolve(ds,'折人民币多少钱',date='2023-09-07')
    chosen=resolver.confirm(plan['resolution_id'],{'v1':'price'})
    assert len(calls)==2
    result=resolver.execute(chosen['resolution_id'])
    assert result['value_decimal']=='7.3065145' and result['unit']=='元/磅'


def test_unknown_scope_requires_text_not_singleton_click(catalogue):
    store,ds=catalogue;resolver=FieldResolver(store,lambda p:proposal([[]],decision='clarify'))
    plan=resolver.resolve(ds,'价格')
    assert plan['requires_note']
    with pytest.raises(ValueError,match='补充'):resolver.confirm(plan['resolution_id'],{'v1':'price'})
    changed=resolver.confirm(plan['resolution_id'],note='按美分每磅')
    assert '按美分每磅' in changed['original_question']


def test_incomplete_calculation_accepts_choice_and_replans_without_premature_execution(catalogue):
    store,ds=catalogue;calls=[]
    def model(payload):
        calls.append(payload)
        if len(calls)==1:return proposal([['price','usd-price']],'calculation',decision='clarify')
        assert '用户已确认字段' in payload['question'] and 'usd-price' in payload['question']
        return proposal([['usd-price']],'calculation',decision='clarify')
    resolver=FieldResolver(store,model);plan=resolver.resolve(ds,'价格换算',date='2023-09-07')
    assert plan['requires_note']
    with pytest.raises(ValueError,match='请选择'):resolver.confirm(plan['resolution_id'],{})
    changed=resolver.confirm(plan['resolution_id'],{'v1':'usd-price'})
    assert changed['parent_resolution_id']==plan['resolution_id']
    assert changed['status']=='needs_clarification' and 'answer' not in changed
    with pytest.raises(ValueError,match='尚未确认'):resolver.execute(changed['resolution_id'])


def test_reject_model_hallucinated_ids(catalogue):
    store,ds=catalogue;resolver=FieldResolver(store,lambda p:proposal([['invented']]))
    with pytest.raises(ValueError,match='实际候选目录'):resolver.resolve(ds,'任意问题')


@pytest.mark.parametrize('expr',['__import__("os")','v1**2','v1.real','v2+1','v1/0','v1+10000000000000'])
def test_calculator_rejects_unsafe_or_invalid_expressions(expr):
    with pytest.raises(ValueError):calculate(expr,{'v1':12})


def test_generic_calculator_decimal_and_all_inputs_used():
    assert calculate('(v1-v2)/v2*100',{'v1':120,'v2':100})['rounded_value']=='20.00'
    with pytest.raises(ValueError):calculate('v1',{'v1':1,'v2':2})


def test_currency_conversion_cannot_invent_a_cents_factor():
    from structured_data.point_calculator import validate_constants
    with pytest.raises(ValueError,match='0.01'):
        validate_constants('v1/v2*0.01',[{'unit':'元/吨'},{'unit':None}],'当日价格换算成美元','美元/吨')
    validate_constants('v1/v2',[{'unit':'元/吨'},{'unit':None}],'当日价格换算成美元','美元/吨')
    validate_constants('v1*0.01*v2',[{'unit':'美分/磅'},{'unit':None}],'换算人民币','元/磅')


def test_orchestrator_executes_clear_plan_and_infers_literal_date(catalogue):
    store,ds=catalogue
    resolver=FieldResolver(store,lambda p:proposal([['price']]))
    answer=resolver.answer('2023/9/7价格是多少')
    assert answer['date']=='2023-09-07'
    assert answer['answer']['sources']['v1']['rows'][0]['value']==99.7
    missing=resolver.answer('价格是多少')
    assert missing['status']=='needs_clarification' and missing['requires_note']
    assert 'answer' not in missing


def test_short_catalog_ids_map_to_authoritative_source(catalogue):
    store,ds=catalogue
    def model(payload):
        column=next(c for s in payload['catalog'] for c in s['columns'] if c['name']=='欧元兑人民币')
        return dict(intent='lookup',decision='resolved',fields=[dict(role='v1',label='汇率',candidate_ids=[column['id']],reason='根据币种')])
    plan=FieldResolver(store,model).resolve(ds,'欧元的汇价',date='2023-09-07')
    assert plan['fields'][0]['candidates'][0]['series_id']=='eur'


@pytest.mark.parametrize('groups',[[1],[dict(role='v1',candidates='bad')],[dict(role='v1',candidates=[False])]])
def test_malformed_model_response_fails_closed(catalogue,groups):
    store,ds=catalogue
    resolver=FieldResolver(store,lambda p:dict(intent='lookup',decision='resolved',fields=groups))
    with pytest.raises(ValueError):resolver.resolve(ds,'格式错误测试')


def test_model_date_slashes_are_normalized(catalogue):
    store,ds=catalogue
    result=FieldResolver(store,lambda p:proposal([['price']])).answer('查价格',date='2023/9/7')
    assert result['answer']['date']=='2023-09-07'


def test_large_catalogue_expands_missing_factor_without_full_dump(catalogue):
    store,ds=catalogue;path=store._path(ds)/'series.json'
    data=json.loads(path.read_text())
    data.extend(dict(series_id=f'extra-{i}',sheet='其他表',metric=f'无关指标{i}',unit='吨') for i in range(101))
    path.write_text(json.dumps(data));calls=[]
    def model(payload):
        calls.append(payload)
        if len(calls)==1:return proposal([['price'],[]],'calculation',decision='clarify')
        assert sum(len(s['columns']) for s in payload['catalog'])<24
        assert any(s['sheet']=='另一张表' for s in payload['catalog'])
        return proposal([['price'],['fx-context']],'calculation','v1*0.01*v2','元/磅')
    plan=FieldResolver(store,model,lambda p:{'search_terms':['美元兑人民币']}).answer('2023年9月7日美国 EMOT M 1-1/8换算',dataset_id=ds)
    assert len(calls)==2 and plan['answer']['value_decimal']=='7.3065145'
    assert not plan['retrieval']['full_catalogue_fallback']


def test_many_candidates_are_bounded_and_still_require_choice(catalogue):
    store,ds=catalogue
    plan=FieldResolver(store,lambda p:proposal([['price','fx-context','fx-other','eur','reverse','usd-price']])).resolve(ds,'未确定指标')
    assert plan['status']=='needs_clarification'
    assert len(plan['fields'][0]['candidates'])==5 and plan['candidates_truncated']
    assert '部分候选' in plan['question']


def test_invalid_tool_scope_is_recoverable_without_guessing_or_model_call(catalogue):
    store,ds=catalogue
    def no_model(p):raise AssertionError('Invalid scope must not call the model')
    resolver=FieldResolver(store,no_model)
    result=resolver.tool_answer('价格',sheet='不存在的猜测表')
    assert result['kind']=='structured_tool_notice' and result['status']=='invalid_scope'
    assert result['max_retries']==1 and result['retryable']
    assert '商品报价' in result['available_sheets']
    assert 'answer' not in result


def test_model_failure_has_terminal_tool_notice(catalogue):
    store,ds=catalogue
    def fail(p):raise ValueError('模型服务暂不可用')
    result=FieldResolver(store,fail).tool_answer('价格')
    assert result['retryable'] is False and result['max_retries']==0
    assert result['message']=='模型服务暂不可用' and 'answer' not in result


def test_partial_current_sheet_match_falls_back_to_all_sheets(catalogue):
    store,ds=catalogue;calls=[]
    def model(payload):
        calls.append(payload)
        if len(calls)==1:
            assert len(payload['catalog'])==1
            return proposal([['price'],[]],'calculation',decision='clarify')
        assert any(g['sheet']=='另一张表' for g in payload['catalog'])
        assert 'available_on_date' not in payload
        return proposal([['price'],['fx-other']],'calculation','v1*0.01*v2','元/磅')
    p=FieldResolver(store,model).answer('按当日汇率换算',dataset_id=ds,context_sheet='商品报价',prior_series_ids=['price'],date='2023-09-07')
    assert len(calls)==2 and p['answer']['sources']['v2']['source']['sheet']=='另一张表'


def test_web_fallback_only_after_full_catalogue_has_missing_input(catalogue):
    store,ds=catalogue
    raw=proposal([['price'],[]],'calculation',decision='clarify')
    raw.update(local_data_missing=True,missing_inputs=['2023-09-07 所需货币对中间价'])
    r=FieldResolver(store,lambda p:raw)
    p=r.resolve(ds,'换算',date='2023-09-07')
    assert p['status']=='external_lookup_required' and p['web_fallback']['eligible']
    with pytest.raises(ValueError,match='尚未确认'):r.execute(p['resolution_id'])
    restricted=r.resolve(ds,'换算',date='2023-09-07',sheet='商品报价')
    assert 'web_fallback' not in restricted


def test_missing_formula_is_audited_before_asking_user_again(catalogue):
    store,ds=catalogue;path=store._path(ds)/'series.json'
    data=json.loads(path.read_text())
    data.extend(dict(series_id=f'audit-{i}',sheet='其他表',metric=f'其他{i}',unit='吨') for i in range(101))
    path.write_text(json.dumps(data));calls=[]
    def model(payload):
        calls.append(payload)
        if len(calls)<2:
            return proposal([['price'],['fx-context']],'calculation',decision='clarify')
        assert 'proposed_plan' in payload
        assert len([c for s in payload['catalog'] for c in s['columns']])<=24
        return proposal([['price'],['fx-context']],'calculation','v1*0.01*v2','元/磅')
    plan=FieldResolver(store,model,lambda p:{'search_terms':[]}).answer('2023年9月7日，美国 EMOT M 1-1/8使用美元兑人民币换算',dataset_id=ds)
    assert len(calls)==2
    assert plan['status']=='resolved' and plan['answer']['rounded_value']=='7.31'


def test_selected_field_and_note_are_both_preserved(catalogue):
    store,ds=catalogue;calls=[]
    def model(payload):
        calls.append(payload)
        if len(calls)==1:return proposal([['price','usd-price'],[]],'calculation',decision='clarify')
        assert 'usd-price' in payload['question'] and '当日汇率' in payload['question']
        assert payload['prior_series'][0]['name']
        return proposal([['usd-price'],['fx-context']],'calculation','v1*v2','元/吨')
    resolver=FieldResolver(store,model);plan=resolver.resolve(ds,'价格换算',date='2023-09-07')
    result=resolver.confirm(plan['resolution_id'],{'v1':'usd-price'},note='当日汇率')
    assert result['confirmed_by_user']
    assert result['fields'][0]['candidates'][0]['series_id']=='usd-price'


def test_missing_role_does_not_block_confirmation_of_existing_choices(catalogue):
    store,ds=catalogue;calls=[]
    def model(payload):
        calls.append(payload)
        if len(calls)==1:return proposal([['price','usd-price'],[]],'calculation',decision='clarify')
        assert '用户已确认字段' in payload['question']
        return proposal([['usd-price'],['fx-context']],'calculation','v1*v2','元/吨')
    resolver=FieldResolver(store,model);plan=resolver.resolve(ds,'价格换算',date='2023-09-07')
    result=resolver.confirm(plan['resolution_id'],{'v1':'usd-price'})
    assert result['status']=='resolved'


def test_index_shortlist_expands_before_declaring_missing(catalogue):
    store,ds=catalogue;path=store._path(ds)/'series.json';data=json.loads(path.read_text())
    data.extend(dict(series_id=f'past-{i}',sheet='其他表',metric=f'历史指标{i}',unit='吨') for i in range(101))
    path.write_text(json.dumps(data));sizes=[]
    def model(payload):
        sizes.append(sum(len(g['columns']) for g in payload['catalog']))
        return dict(proposal([[]],decision='clarify'),local_data_missing=True,missing_inputs=['缺少指标'])
    result=FieldResolver(store,model,lambda p:{'search_terms':['不存在字段']}).resolve(ds,'2023-09-07查指标')
    assert sizes[0]<=24 and 107 in sizes[1:]
    assert result['status']=='external_lookup_required'


def test_price_ambiguity_is_not_bypassed_by_external_missing_input(catalogue):
    store,ds=catalogue
    raw=dict(proposal([['price','usd-price'],[]],decision='clarify'),local_data_missing=True,missing_inputs=['换算因子'])
    result=FieldResolver(store,lambda p:raw).resolve(ds,'价格')
    assert result['status']=='needs_clarification' and 'web_fallback' not in result


def test_currency_only_conversion_cannot_invent_weight_basis():
    from structured_data.point_calculator import validate_output_unit
    with pytest.raises(ValueError,match='计量基准'):
        validate_output_unit('美元/吨（原表未注明）',[{'unit':None},{'unit':None}],'人民币价格换美元')
    validate_output_unit('美元（沿用原计量基准）',[{'unit':None},{'unit':None}],'人民币价格换美元')
    validate_output_unit('美元/吨',[{'unit':'元/吨'},{'unit':None}],'人民币价格换美元')


def test_multiple_result_formula_is_repaired_without_asking_user(catalogue):
    store,ds=catalogue;calls=[]
    def model(payload):
        calls.append(payload)
        if len(calls)==1:return proposal([['price'],['fx-context']],'calculation','v1, v1*v2','元, 美元')
        assert 'repair_instruction' in payload
        return proposal([['price'],['fx-context']],'calculation','v1*0.01*v2','元/磅')
    result=FieldResolver(store,model).answer('2023-09-07原价和换算价',dataset_id=ds)
    assert len(calls)==2 and result['status']=='resolved' and result['answer']['rounded_value']=='7.31'


def test_dated_query_never_uses_undated_value(catalogue):
    store,ds=catalogue;path=store._path(ds)/'series.json';data=json.loads(path.read_text())
    next(s for s in data if s['series_id']=='price')['date_mode']='none';path.write_text(json.dumps(data))
    resolver=FieldResolver(store,lambda p:proposal([['price']]))
    with pytest.raises(ValueError,match='无日期字段'):
        resolver.resolve(ds,'2023-09-07价格')
    plan=resolver.resolve(ds,'无日期表的数值')
    with pytest.raises(ValueError,match='没有日期'):
        resolver.execute(plan['resolution_id'],date='2023-09-07')


def test_all_candidates_missing_on_date_report_absence_without_clarification(catalogue):
    store,ds=catalogue;resolver=FieldResolver(store,lambda p:proposal([['price','usd-price']],decision='clarify'))
    result=resolver.answer('2023-09-08价格',dataset_id=ds)
    assert result['status']=='no_data' and not result['requires_note']
    assert len(result['answer']['sources'])==2
    assert all(s['valid_count']==0 for s in result['answer']['sources'].values())
    with pytest.raises(ValueError,match='尚未确认'):resolver.execute(result['resolution_id'])
    present=resolver.answer('2023-09-07价格',dataset_id=ds)
    assert present['status']=='needs_clarification' and 'answer' not in present


def test_extremum_question_does_not_require_unknown_date(catalogue):
    store,ds=catalogue
    raw=proposal([['price']]);raw['query']={'operation':'max'}
    result=FieldResolver(store,lambda p:raw).answer('哪个日期价格最高，是多少？',dataset_id=ds)
    assert result['status']=='resolved' and not result['requires_note']
    source=result['answer']['sources']['v1']
    assert source['aggregate']==99.7
    assert source['rows'][0]['date']=='2023-09-07'
    assert source['rows'][0]['source_cell']=='B2'


def test_extremum_ignores_prior_single_day_but_honors_explicit_range(catalogue):
    store,ds=catalogue
    raw=dict(proposal([['price']]),query={'operation':'max'})
    resolver=FieldResolver(store,lambda p:raw)
    result=resolver.answer('价格最高是哪天',dataset_id=ds,date='2020-01-01')
    assert result['date'] is None and result['answer']['sources']['v1']['aggregate']==99.7
    raw=dict(proposal([['price']]),query={'operation':'min','start_date':'2023-09-08','end_date':'2023-09-09'})
    result=FieldResolver(store,lambda p:raw).answer('2023-09-08到2023-09-09价格最低是哪天',dataset_id=ds)
    assert result['answer']['sources']['v1']['status']=='no_data'
    assert result['answer']['sources']['v1']['rows']==[]


def test_extrema_ambiguity_still_requires_field_choice(catalogue):
    store,ds=catalogue
    raw=dict(proposal([['price','usd-price']],decision='clarify'),query={'operation':'max'})
    resolver=FieldResolver(store,lambda p:raw)
    plan=resolver.answer('哪个价格最高是哪天',dataset_id=ds)
    assert plan['status']=='needs_clarification' and 'answer' not in plan
    chosen=resolver.confirm(plan['resolution_id'],{'v1':'price'})
    assert resolver.attach_answer(chosen)['answer']['sources']['v1']['aggregate']==99.7


def test_invalid_statistical_operation_fails_closed(catalogue):
    store,ds=catalogue
    raw=dict(proposal([['price']]),query={'operation':'SELECT * FROM observations'})
    with pytest.raises(ValueError,match='查询操作'):
        FieldResolver(store,lambda p:raw).answer('统计',dataset_id=ds)


def test_missing_statistical_operation_is_repaired_not_asked_of_user(catalogue):
    store,ds=catalogue;calls=[]
    def model(p):
        calls.append(p)
        raw=proposal([['price']])
        if len(calls)>1:raw['query']={'operation':'max'}
        return raw
    result=FieldResolver(store,model).answer('价格最高的是哪一天？是多少？',dataset_id=ds)
    assert len(calls)==2 and result['status']=='resolved'
    assert 'repair_instruction' in calls[1]


def test_model_cannot_invent_extremum_date_filter(catalogue):
    store,ds=catalogue
    raw=dict(proposal([['price']]),query={'operation':'max','start_date':'2013-02-01'})
    with pytest.raises(ValueError,match='不可擅自限定范围'):
        FieldResolver(store,lambda p:raw).answer('价格最高是哪天',dataset_id=ds)


def test_named_daily_high_field_is_not_forced_into_global_extremum(catalogue):
    store,ds=catalogue
    result=FieldResolver(store,lambda p:proposal([['price']])).answer('2023/9/7最高价是多少',dataset_id=ds)
    assert result['query']['operation']=='rows'
    assert result['answer']['date']=='2023-09-07'
