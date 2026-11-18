"""Dimension prose uses authorized facts, audited models and an explicit save."""
import asyncio
from copy import deepcopy
from decimal import Decimal
import json
from pathlib import Path
from types import SimpleNamespace

from fastapi import FastAPI, HTTPException
import httpx
import pytest

from tin.integration.workbench_scope import current_workbench
from tin.web import report_dimension_drafts as drafts

URL = '/api/sn/reports/2026-01-09/weekly/draft-dimensions'


def scope(**changes):
    values = {'workbench_id': 'bench', 'restricted': False, 'can_edit': True,
        'authorization': 'Bearer synthetic', 'metadata': {}, 'view_modules': ['reports'],
        'identity': SimpleNamespace(can_write=True)}
    values.update(changes)
    return SimpleNamespace(**values)


def row(identifier='custom'):
    return {'id': identifier, 'label': '研究员自定义名称', 'sentence': '原有研究员文字9999',
        'facts': [{'text': '真实指标 最新12.35吨（较01-02 +0.35，近4年同期第1低）',
            'name': '真实指标', 'unit': '吨', 'latest': 12.3456, 'date': '2026-01-09',
            'change': .3456, 'change_pct': .0288, 'baseline': {'date': '2026-01-02', 'value': 12},
            'position': {'n': 3, 'rank': 4, 'total': 4, 'rank_low': 1},
            'display': {'latest': '12.35', 'change': '+0.35'}}],
        'evidence': [{'side': 'support', 'text': '研判依据原文，仅作解释参考'}]}


def view():
    item = row()
    return {'dimension_overview': [item], 'evidence_groups': [{'id': 'custom', 'label': item['label'],
        'figures': [{'key': 'series:real', 'title': '真实指标图', 'conclusion': '原图结论'}]}]}


def candidate(text='真实指标升至12.35吨，变化有待观察。', **changes):
    return {'text': text, 'impact': 'neutral', 'metric_names': ['真实指标'], **changes}


def review(**changes):
    return {'grounded': True, 'metric_names': ['真实指标'], 'dates': [], 'reason': '', **changes}


@pytest.fixture(autouse=True)
def synchronous_read(monkeypatch):
    async def thread(function, *args, **kwargs):
        return function(*args, **kwargs)
    monkeypatch.setattr(drafts, 'run_in_threadpool', thread)


def request(body, *, active=None, url=URL):
    app = FastAPI()
    app.include_router(drafts.router)
    async def run():
        marker = current_workbench.set(active)
        try:
            async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url='http://test') as client:
                return await client.post(url, json=body)
        finally:
            current_workbench.reset(marker)
    return asyncio.run(run())


@pytest.mark.parametrize('text,expected', [
    ('真实指标12.3456吨', True), ('真实指标12.35吨', True),
    ('真实指标12.3吨', False), ('真实指标9999吨', False),
    ('较01-02增加0.35吨', True), ('近4年同期第1低', True),
    ('数值1,234.5', False), ('无数字的解释', True),
])
def test_numeric_verification_accepts_only_actual_or_displayed_rounding(text, expected):
    assert drafts.numbers_verified(text, drafts.source_facts(row())) is expected


def test_number_pool_does_not_include_internal_precision_or_template_metadata():
    item = row()
    item['facts'][0].update(display_decimals=15, series_id='internal-7777')
    item['facts'][0]['position']['template'] = {'offset': 9999}
    facts = drafts.source_facts(item)
    assert 'template' not in facts[0]['position']
    assert not drafts.numbers_verified('数值9999', facts)
    assert not drafts.numbers_verified('数值7777', facts)
    assert not drafts.numbers_verified('数值15', facts)


def test_dates_must_be_actual_input_dates_not_a_recombined_numeric_date():
    facts = drafts.source_facts(row())
    assert drafts.dates_verified('截至2026-01-09、较01-02', facts)
    assert not drafts.dates_verified('截至2026-01-02之后的01-03', facts)
    assert not drafts.dates_verified('截至2026-01-01', facts)


def test_draft_uses_only_each_dimension_and_audited_purpose_without_saving(monkeypatch):
    original = view()
    original['dimension_overview'].append(row('other'))
    original['dimension_overview'][1]['facts'][0]['name'] = '另一个维度指标'
    original['dimension_overview'][1]['facts'][0]['text'] = '另一个维度指标 最新12.35吨'
    original['evidence_groups'] = []
    original['logic_strategy'] = {'tone': '当前基调', 'contradiction': '当前研判逻辑',
        'strategies': [{'title': '测试策略', 'text': '观察研判条件'}]}
    original['strategy_overview'] = {'text': '当前策略概况', 'triggered': []}
    observed = []
    monkeypatch.setattr(drafts, 'read_report', lambda *_: deepcopy(original))
    async def complete(_scope, model_id, prompt, *, purpose):
        observed.append((model_id, prompt, purpose))
        if purpose == 'report_summary_draft':
            assert '本周结论' in prompt and '当前基调' in prompt and '当前研判逻辑' in prompt
            assert '当前策略概况' in prompt and '测试策略' in prompt
            assert '变化仍需观察。' in prompt and '真实指标升至12.35吨' in prompt
            return {'text': '真实指标及另一个维度指标均为12.35吨。方向仍待观察。',
                    'metric_names': ['真实指标', '另一个维度指标']}
        if purpose == 'report_summary_review':
            assert '"name":"真实指标"' in prompt and '"name":"另一个维度指标"' in prompt
            return review(metric_names=['真实指标', '另一个维度指标'])
        if purpose == 'report_dimension_review':
            return review(metric_names=[] if '另一个维度指标' in prompt else ['真实指标'])
        if '另一个维度指标' in prompt:
            return candidate('本期变化仍需观察。', metric_names=[])
        assert '9999' not in prompt
        assert '真实指标' in prompt and '研判依据原文' in prompt and '研究员自己的解释' in prompt
        return candidate()
    monkeypatch.setattr(drafts.strategy_text, 'completion', complete)
    response = request({'model_id': 'visible-model', 'dimension_notes': {
        'custom': {'text': '研究员自己的解释', 'impact': 'bullish'},
        'other': {'text': '', 'impact': 'undecided'}}}, active=scope())
    assert response.status_code == 200, response.text
    assert [item['id'] for item in response.json()['dimensions']] == ['custom', 'other']
    assert all(item['status'] == 'ok' for item in response.json()['dimensions'])
    assert response.json()['summary'] == {'text': '真实指标及另一个维度指标均为12.35吨。方向仍待观察。', 'status': 'ok'}
    assert [purpose for _, _, purpose in observed] == ['report_dimension_draft', 'report_dimension_review'] * 2 + [
        'report_summary_draft', 'report_summary_review']
    assert original['dimension_overview'][0]['sentence'] == '原有研究员文字9999'
    assert '真实指标' not in observed[2][1] and '研究员自己的解释' not in observed[2][1]


@pytest.mark.parametrize('text,status', [('真实指标升至9999吨。', 'numbers_unverified'),
    ('截至2026-01-01真实指标12.35吨。', 'sources_unverified')])
def test_invalid_numerals_or_dates_mark_entire_sentence_and_do_not_prefill(monkeypatch, text, status):
    monkeypatch.setattr(drafts, 'read_report', lambda *_: {'dimension_overview': [row()]})
    calls = []
    async def complete(*args, **kwargs):
        calls.append(kwargs['purpose'])
        return candidate(text)
    monkeypatch.setattr(drafts.strategy_text, 'completion', complete)
    response = request({'model_id': 'model'}, active=scope())
    result = response.json()['dimensions'][0]
    assert result['status'] == status and result['text'] == '' and result['suggestion'] == text
    assert result['impact'] == 'undecided' and calls == ['report_dimension_draft', 'report_summary_draft']


def test_unlisted_metric_cannot_be_authorized_by_researcher_text(monkeypatch):
    monkeypatch.setattr(drafts, 'read_report', lambda *_: {'dimension_overview': [row()]})
    async def complete(*args, **kwargs):
        return candidate('新增指标12.35吨。', metric_names=['新增指标'])
    monkeypatch.setattr(drafts.strategy_text, 'completion', complete)
    response = request({'model_id': 'model', 'dimension_notes': {
        'custom': {'text': '新增指标12.35吨。', 'impact': 'bullish'}}}, active=scope())
    assert response.json()['dimensions'][0]['status'] == 'sources_unverified'


def test_independent_model_review_rejects_undeclared_metric_and_ungrounded_explanation(monkeypatch):
    monkeypatch.setattr(drafts, 'read_report', lambda *_: {'dimension_overview': [row()]})
    async def complete(*args, **kwargs):
        if kwargs['purpose'].endswith('_draft'):
            return candidate('真实指标12.35吨，新增指标也在走高。')
        return review(grounded=False, metric_names=['真实指标', '新增指标'])
    monkeypatch.setattr(drafts.strategy_text, 'completion', complete)
    response = request({'model_id': 'model'}, active=scope())
    assert response.json()['dimensions'][0]['status'] == 'sources_unverified'
    assert response.json()['dimensions'][0]['text'] == ''


@pytest.mark.parametrize('text,names,review_changes,expected,reviewed', [
    ('真实指标为9999吨。后续看更新。', ['真实指标'], {}, 'numbers_unverified', False),
    ('截至2026-01-01真实指标为12.35吨。后续看更新。', ['真实指标'], {}, 'sources_unverified', False),
    ('其他指标为12.35吨。后续看更新。', ['其他指标'], {}, 'sources_unverified', False),
    ('真实指标为12.35吨。其他指标预期转强。', ['真实指标'],
     {'grounded': False, 'metric_names': ['真实指标', '其他指标']}, 'sources_unverified', True),
])
def test_summary_is_grounded_only_in_dimension_facts_and_never_prefills_rejected_text(
        monkeypatch, text, names, review_changes, expected, reviewed):
    item = {**row(), 'sentence': '已保存维度结论', 'impact': 'bearish'}
    projection = {'dimension_overview': [item], 'logic_strategy': {'tone': '研判文字提到9999'}}
    monkeypatch.setattr(drafts, 'read_report', lambda *_: deepcopy(projection))
    purposes = []

    async def complete(_scope, _model, prompt, *, purpose):
        purposes.append(purpose)
        if purpose == 'report_dimension_draft':
            return candidate('真实指标为8888吨。')
        if purpose == 'report_summary_draft':
            assert '本日结论' in prompt and '研判文字提到9999' in prompt
            assert '已保存维度结论' in prompt and '"impact":"bearish"' in prompt
            assert '8888' not in prompt
            return {'text': text, 'metric_names': names}
        return review(**review_changes)

    monkeypatch.setattr(drafts.strategy_text, 'completion', complete)
    response = request({'model_id': 'model'}, active=scope(), url=URL.replace('/weekly/', '/daily/'))
    assert response.status_code == 200, response.text
    summary = response.json()['summary']
    assert summary['text'] == '' and summary['suggestion'] == text and summary['status'] == expected
    assert '未预填' in summary['reason'] and ('report_summary_review' in purposes) == reviewed


def test_all_grouped_figure_conclusions_are_unsaved_previews(monkeypatch):
    monkeypatch.setattr(drafts, 'read_report', lambda *_: view())
    async def complete(*args, **kwargs):
        return review() if kwargs['purpose'].endswith('_review') else candidate()
    monkeypatch.setattr(drafts.strategy_text, 'completion', complete)
    response = request({'model_id': 'model'}, active=scope())
    assert response.status_code == 200
    assert response.json()['figures'][0]['key'] == 'series:real'
    assert response.json()['figures'][0]['status'] == 'ok'


@pytest.mark.parametrize('failure', [HTTPException(503, '模型不可用'), HTTPException(403, '无模型权限')])
def test_model_failures_report_actual_error_without_fixed_prose(monkeypatch, failure):
    monkeypatch.setattr(drafts, 'read_report', lambda *_: {'dimension_overview': [row()]})
    async def complete(*args, **kwargs):
        raise failure
    monkeypatch.setattr(drafts.strategy_text, 'completion', complete)
    response = request({'model_id': 'model'}, active=scope())
    assert response.status_code == failure.status_code and response.json()['detail'] == failure.detail
    assert 'dimensions' not in response.json()


def test_changed_projection_during_generation_is_rejected(monkeypatch):
    calls = [view(), {'dimension_overview': []}]
    monkeypatch.setattr(drafts, 'read_report', lambda *_: calls.pop(0))
    async def complete(*args, **kwargs):
        return review() if kwargs['purpose'].endswith('_review') else candidate()
    monkeypatch.setattr(drafts.strategy_text, 'completion', complete)
    response = request({'model_id': 'model'}, active=scope())
    assert response.status_code == 409


def test_changed_judgment_during_summary_generation_is_rejected(monkeypatch):
    first = {**view(), 'logic_strategy': {'tone': '原有研判'}}
    later = {**first, 'logic_strategy': {'tone': '更新的研判'}}
    projections = [first, later]
    monkeypatch.setattr(drafts, 'read_report', lambda *_: projections.pop(0))

    async def complete(*args, **kwargs):
        return review() if kwargs['purpose'].endswith('_review') else candidate()

    monkeypatch.setattr(drafts.strategy_text, 'completion', complete)
    assert request({'model_id': 'model'}, active=scope()).status_code == 409


@pytest.mark.parametrize('active,status', [(None, 409), (scope(restricted=True), 403),
    (scope(can_edit=False), 403), (scope(view_modules=[]), 403),
    (scope(authorization=''), 403), (scope(metadata={'can_read_source': False}), 403)])
def test_permission_denied_before_reading_or_model(monkeypatch, active, status):
    monkeypatch.setattr(drafts, 'read_report', lambda *_: pytest.fail('must reject before reading'))
    response = request({'model_id': 'model'}, active=active)
    assert response.status_code == status


@pytest.mark.parametrize('body', [{'model_id': ''}, {'model_id': 'model', 'facts': [{'latest': 9999}]},
    {'model_id': 'model', 'dimension_notes': {'custom': {'text': 'x' * 201}}},
    {'model_id': 'model', 'figure_conclusions': {'series:real': 'x' * 201}},
    {'model_id': 'model', 'dimension_notes': {'custom': {'impact': 'invented'}}}])
def test_invalid_or_client_authored_facts_fail_before_reading(monkeypatch, body):
    monkeypatch.setattr(drafts, 'read_report', lambda *_: pytest.fail('must validate before reading'))
    assert request(body, active=scope()).status_code == 422


def test_metric_name_and_unit_digits_do_not_authorize_new_numerical_claims():
    facts = [{'name': '合约99测试', 'unit': '吨/888单位', 'latest': 12.3456,
              'text': '合约99测试 12.35 吨/888单位'}]
    assert drafts.numbers_verified('合约99测试为12.35吨/888单位。', facts)
    assert not drafts.numbers_verified('本期为99。', facts)
    assert not drafts.numbers_verified('本期为888。', facts)


def test_unsaved_note_in_an_empty_dimension_is_drafted_in_workbench_order(monkeypatch):
    projection = {'dimension_overview': [row()], 'editable_dimensions': [
        {'id': 'empty', 'label': '研究员新增空维度'}, {'id': 'custom', 'label': '自定义维度'},
        {'id': 'unused', 'label': '无内容维度'}]}
    monkeypatch.setattr(drafts, 'read_report', lambda *_: deepcopy(projection))
    calls = []
    async def complete(*args, **kwargs):
        calls.append(args[2])
        if kwargs['purpose'].endswith('_review'):
            return review(metric_names=[])
        assert '没有数字的新研究员文字' in args[2] or '真实指标' in args[2]
        return candidate('变化尚待观察。', metric_names=[])
    monkeypatch.setattr(drafts.strategy_text, 'completion', complete)
    response = request({'model_id': 'model', 'dimension_notes': {
        'empty': {'text': '没有数字的新研究员文字', 'impact': 'neutral'},
        'unused': {'text': '', 'impact': 'undecided'}}}, active=scope())
    assert response.status_code == 200
    assert [value['id'] for value in response.json()['dimensions']] == ['empty', 'custom']
    assert '没有数字的新研究员文字' in calls[0] and '真实指标' not in calls[0]


def test_empty_dimension_configuration_changes_invalidate_drafting(monkeypatch):
    first = {'dimension_overview': [row()], 'editable_dimensions': [{'id': 'empty', 'label': '之前名称'}]}
    after = deepcopy(first)
    after['editable_dimensions'][0]['label'] = '修改后名称'
    projections = [first, after]
    monkeypatch.setattr(drafts, 'read_report', lambda *_: projections.pop(0))
    response = request({'model_id': 'model'}, active=scope())
    assert response.status_code == 409


def test_written_numerals_cannot_bypass_program_number_check():
    facts = drafts.source_facts(row())
    assert not drafts.numbers_verified('近六年同期第八低。', facts)
    assert not drafts.numbers_verified('合成指标最新一百吨。', facts)
    assert not drafts.numbers_verified('合成指标最新五吨。', facts)
    assert drafts.numbers_verified('一方面变化仍待观察。', facts)


@pytest.mark.parametrize('text', ['合成指标最新+12吨', '合成指标变化+2吨',
    '合成指标最新2026吨', '合成指标最新9吨', '合成指标最新1吨'])
def test_sign_changes_and_date_fragments_do_not_pass_number_check(text):
    facts = [{'name': '合成指标', 'latest': -12, 'change': -2, 'date': '2026-01-09',
              'baseline': {'date': '2026-01-02', 'value': -10},
              'text': '合成指标 -12 吨（较01-02 -2）', 'unit': '吨'}]
    assert not drafts.numbers_verified(text, facts)
    assert drafts.numbers_verified('合成指标最新-12吨，较01-02变化-2吨。', facts)
    assert drafts.dates_verified('截至2026-01-09、较01-02', facts)


def test_retained_uneditable_dimension_buckets_do_not_call_model(monkeypatch):
    projection = {'dimension_overview': [row(), row('__unlabeled__'), row('__deleted__')],
                  'editable_dimensions': [{'id': 'custom', 'label': '当前可编辑维度'}]}
    monkeypatch.setattr(drafts, 'read_report', lambda *_: deepcopy(projection))
    calls = []
    async def complete(*args, **kwargs):
        calls.append(kwargs['purpose'])
        if kwargs['purpose'] == 'report_summary_draft':
            assert '__unlabeled__' not in args[2] and '__deleted__' not in args[2]
        return review() if kwargs['purpose'].endswith('_review') else candidate()
    monkeypatch.setattr(drafts.strategy_text, 'completion', complete)
    response = request({'model_id': 'model'}, active=scope())
    assert response.status_code == 200
    assert [item['id'] for item in response.json()['dimensions']] == ['custom']
    assert calls == ['report_dimension_draft', 'report_dimension_review', 'report_summary_draft', 'report_summary_review']


ACCEPTANCE = json.loads((Path(__file__).parent / 'fixtures' /
                         'report_dimension_draft_acceptance.json').read_text())


@pytest.mark.parametrize('example', ACCEPTANCE, ids=lambda example: example['case'])
def test_real_acceptance_sentences_remain_isolated_and_use_only_source_facts(example):
    facts = drafts.source_facts({'facts': example['facts']})
    result = drafts._candidate(candidate(example['text'], metric_names=example['metric_names']), facts)
    assert result['status'] == 'ok', result
    if example.get('rejected_text'):
        result = drafts._candidate(candidate(example['rejected_text'], metric_names=example['metric_names']), facts)
        assert result['status'] == 'numbers_unverified' and result['text'] == ''


@pytest.mark.parametrize('word', ['下降', '减少', '回落', '下滑', '低于'])
def test_negative_change_direction_words_accept_absolute_magnitude(word):
    facts = [{'name': '合成指标', 'latest': 36.12, 'change': -2.91,
              'display': {'change': '-2.91'}}]
    assert drafts.semantic_change('2.91', word) == Decimal('-2.91')
    assert drafts.numbers_verified('合成指标' + word + '2.91。', facts)


@pytest.mark.parametrize('word', ['上升', '增加', '回升', '提高', '高于'])
def test_positive_change_direction_words_accept_absolute_magnitude(word):
    facts = [{'name': '合成指标', 'latest': 36.12, 'change': 2.91,
              'display': {'change': '+2.91'}}]
    assert drafts.semantic_change('2.91', word) == Decimal('2.91')
    assert drafts.numbers_verified('合成指标' + word + '2.91。', facts)


@pytest.mark.parametrize('text', ['甲指标增加2.91', '甲指标上升-2.91', '甲指标下降+2.91',
    '乙指标下降2.91', '甲指标增加35.7'])
def test_reversed_change_cannot_borrow_a_latest_value_or_another_metric(text):
    facts = [{'name': '甲指标', 'latest': 2.91, 'change': -2.91},
             {'name': '乙指标', 'latest': 36.12, 'change': 2.91},
             {'name': '丙指标', 'latest': 135.7, 'change': 35.7}]
    assert not drafts.numbers_verified(text, facts)


def test_names_and_alias_digits_are_removed_but_do_not_authorize_values():
    facts = [{'name': '合约99测试', 'raw_name': 'DATA / 合约99测试',
              'readable_name': '合约99测试', 'aliases': ['测试合约99'], 'latest': 12.35}]
    assert drafts.numbers_verified('ＤＡＴＡ／ 合约 99 测试最新12.35。', facts)
    assert drafts.numbers_verified('测试合约99最新12.35。', facts)
    assert not drafts.numbers_verified('本期最新99。', facts)
    assert not drafts.metric_names_verified(['合约98测试'], facts)


def test_name_normalization_does_not_merge_distinct_numeric_tokens():
    facts = [{'name': '合成指标99', 'latest': 1235}]
    assert drafts.numbers_verified('合成 指标 99 最新1235', facts)
    assert not drafts.numbers_verified('合成指标99 最新12 35', facts)


def test_numeric_only_name_segments_cannot_hide_a_new_observation():
    facts = [{'name': '合成指标 / 99', 'latest': 12.35, 'unit': '1'}]
    assert drafts.numbers_verified('合成指标/99最新12.35', facts)
    assert not drafts.numbers_verified('本期为99', facts)
    assert not drafts.numbers_verified('本期为1', facts)


def test_normalized_names_are_verified_in_the_independent_second_review(monkeypatch):
    example = next(example for example in ACCEPTANCE if example['case'] == 'normalized_source_names')
    projection = {'dimension_overview': [{**row(), 'facts': example['facts']}]}
    monkeypatch.setattr(drafts, 'read_report', lambda *_: deepcopy(projection))
    purposes = []
    async def complete(*args, **kwargs):
        purposes.append(kwargs['purpose'])
        if kwargs['purpose'].endswith('_review'):
            return review(metric_names=['CZCE短纤结算价', 'ＣＺＣＥ ／ 持仓量'])
        return candidate(example['text'], metric_names=example['metric_names'])
    monkeypatch.setattr(drafts.strategy_text, 'completion', complete)
    response = request({'model_id': 'model'}, active=scope())
    assert response.json()['dimensions'][0]['status'] == 'ok'
    assert purposes == ['report_dimension_draft', 'report_dimension_review', 'report_summary_draft', 'report_summary_review']


def test_no_update_prompt_retains_latest_reference_without_authorizing_historical_change():
    example = next(example for example in ACCEPTANCE if example['case'] == 'no_update')
    facts = drafts.source_facts({'facts': example['facts']})
    assert facts[0]['update_status'] == 'no_update' and facts[0]['latest'] == 16865
    assert facts[0]['update_notice'] == '本期无新数据（最新 2023-09-21）'
    assert 'change' not in facts[0] and 'baseline' not in facts[0]
    assert facts[0]['display'] == {'latest': '16,865'}
    prompt = drafts.drafting_prompt({}, facts, '')
    assert 'no_update' in prompt and '本期无新数据' in prompt and '不写+0或持平' in prompt
    assert '第一句直接给出结论' in prompt and '一到两个关键数字作为依据' in prompt and '不逐项复述' in prompt
    assert '不按名称或关键词套句式' in prompt and '已有结论是否仍成立或无法判断' in prompt
    assert not drafts.numbers_verified('郑棉05合约下降0', facts)
    assert '不能据历史差值声称本期变化或持平' in drafts.review_prompt(facts, [], example['text'])


def test_no_update_does_not_leak_historical_changes_from_old_display_text():
    item = {'facts': [{'name': '合成指标', 'latest': 168.5, 'date': '2023-09-21',
        'text': '合成指标168.5（较09-21 +0）', 'change': 0, 'previous': 168.5,
        'display': {'latest': '168.5', 'change': '+0'}, 'update_status': 'no_update'}]}
    facts = drafts.source_facts(item)
    assert 'text' not in facts[0] and '+0' not in drafts._encode(facts)
    assert not drafts.numbers_verified('合成指标较09-21为+0。', facts)
    assert drafts.numbers_verified('合成指标本期无新数据（最新2023-09-21），最新168.5供参考。', facts)


def test_annotation_and_text_dimensions_never_enter_model_source_facts():
    item = row()
    item['facts'] += [{'name': '说明列', 'latest': 777, 'field_metadata': {'role': 'annotation'}},
                      {'name': '文本维度', 'latest': 888, 'field_metadata': {'role': 'text_dimension'}},
                      {'name': '旧归档说明列', 'latest': 999, 'role': 'annotation'}]
    facts = drafts.source_facts(item)
    assert len(facts) == 1
    assert not drafts.metric_names_verified(['说明列', '文本维度', '旧归档说明列'], facts)
    assert not drafts.numbers_verified('数值777', facts)
