"""Dimension prose uses authorized facts, audited models and an explicit save."""
import asyncio
from copy import deepcopy
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
    observed = []
    monkeypatch.setattr(drafts, 'read_report', lambda *_: deepcopy(original))
    async def complete(_scope, model_id, prompt, *, purpose):
        observed.append((model_id, prompt, purpose))
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
    assert [purpose for _, _, purpose in observed] == ['report_dimension_draft', 'report_dimension_review'] * 2
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
    assert result['impact'] == 'undecided' and calls == ['report_dimension_draft']


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
        if kwargs['purpose'] == 'report_dimension_draft':
            return candidate('真实指标12.35吨，新增指标也在走高。')
        return review(grounded=False, metric_names=['真实指标', '新增指标'])
    monkeypatch.setattr(drafts.strategy_text, 'completion', complete)
    response = request({'model_id': 'model'}, active=scope())
    assert response.json()['dimensions'][0]['status'] == 'sources_unverified'
    assert response.json()['dimensions'][0]['text'] == ''


def test_all_grouped_figure_conclusions_are_unsaved_previews(monkeypatch):
    monkeypatch.setattr(drafts, 'read_report', lambda *_: view())
    async def complete(*args, **kwargs):
        return review() if kwargs['purpose'] == 'report_dimension_review' else candidate()
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
        return review() if kwargs['purpose'] == 'report_dimension_review' else candidate()
    monkeypatch.setattr(drafts.strategy_text, 'completion', complete)
    response = request({'model_id': 'model'}, active=scope())
    assert response.status_code == 409


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
        if kwargs['purpose'] == 'report_dimension_review':
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
        return review() if kwargs['purpose'] == 'report_dimension_review' else candidate()
    monkeypatch.setattr(drafts.strategy_text, 'completion', complete)
    response = request({'model_id': 'model'}, active=scope())
    assert response.status_code == 200
    assert [item['id'] for item in response.json()['dimensions']] == ['custom']
    assert calls == ['report_dimension_draft', 'report_dimension_review']
