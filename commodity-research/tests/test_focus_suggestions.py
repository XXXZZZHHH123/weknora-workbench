"""Core-focus relevance belongs to the model; program code checks real identities."""
import asyncio
from contextlib import contextmanager
import copy
import json
from types import SimpleNamespace

from fastapi import FastAPI, HTTPException
import httpx
import pytest
from sqlalchemy import select

from tin.models import LlmCall
from tin.web import focus_suggestions as suggestions, strategy_text
from tin.web.diagram_outline import outline_prompt
from tin.web.research_framework import RESEARCH_FRAMEWORK, RESEARCH_GROUPS


@pytest.fixture(autouse=True)
def direct_metadata_reads(monkeypatch):
    """Keep isolated metadata/audit work in this test's in-memory session."""
    async def thread(function, *args, **kwargs):
        return function(*args, **kwargs)
    monkeypatch.setattr(suggestions, 'run_in_threadpool', thread)
    monkeypatch.setattr(strategy_text, 'run_in_threadpool', thread)


def catalog(size=4, *, sheet='Synthetic worksheet', start=0):
    return {f'field-{i}': {'series_id': f'field-{i}', 'metric': f'Synthetic measure {i}',
                          'unit': 'unit', 'frequency': 'weekly', 'sheet': sheet,
                          'source_knowledge_id': 'file', 'dataset_id': 'dataset'}
            for i in range(start, start + size)}


def choice(identifier='field-0', group='供给', reason='Model selected this scope'):
    return {'series_id': identifier, 'group': group, 'reason': reason}


def scope(**changes):
    value = {'workbench_id': 'bench', 'knowledge_base_id': 'kb', 'sources_revision': 3,
             'scope_version': 2, 'restricted': False, 'can_edit': True,
             'authorization': 'Bearer synthetic-token', 'metadata': {},
             'identity': SimpleNamespace(tenant_id=1, can_write=True)}
    value.update(changes)
    return SimpleNamespace(**value)


def test_framework_is_shared_by_both_model_tasks():
    assert RESEARCH_FRAMEWORK in outline_prompt()
    assert RESEARCH_FRAMEWORK in suggestions.worksheet_prompt(suggestions.worksheets(catalog()))
    assert RESEARCH_FRAMEWORK in suggestions.indicator_prompt([], [], 0)
    assert RESEARCH_GROUPS == ('核心标的', '供给', '需求', '库存', '平衡', '宏观')


def test_worksheet_summary_preserves_source_identity_and_only_samples_names():
    data = catalog(6)
    data['other'] = {**data['field-0'], 'source_knowledge_id': 'another-file'}
    sheets = suggestions.worksheets(data)
    assert [len(sheet['fields']) for sheet in sheets] == [6, 1]
    prompt = suggestions.worksheet_prompt(sheets)
    rows = json.loads(prompt.split('\n当前工作表清单：')[1])
    assert rows[0]['field_count'] == 6
    assert rows[0]['field_names'] == [data[f'field-{i}']['metric'] for i in range(3)]
    assert 'field-0' not in prompt and 'Synthetic measure 5' not in prompt
    assert rows[0]['sheet'] == rows[1]['sheet'] and rows[0]['worksheet_id'] != rows[1]['worksheet_id']


def test_validation_deduplicates_in_model_order_and_caps_twelve_without_adding_fields():
    data = catalog(18)
    document = {'items': [choice('field-4'), choice('field-4', '需求'),
                          *[choice(f'field-{i}') for i in range(18) if i != 4]]}
    items, total = suggestions.validate_suggestions(document, data)
    assert total == 18 and len(items) == 12
    assert [item['series_id'] for item in items] == ['field-4', 'field-0', 'field-1', 'field-2', 'field-3',
                                                   'field-5', 'field-6', 'field-7', 'field-8', 'field-9', 'field-10', 'field-11']
    assert items[0]['group'] == '供给'


def test_field_and_worksheet_names_never_select_filter_reorder_or_supplement_indicators():
    ordinary = catalog(6)
    changed = copy.deepcopy(ordinary)
    # Deliberately give selected fields unrelated names and unselected fields all
    # framework labels. Identical model IDs must remain the entire decision.
    for i, field in enumerate(changed.values()):
        field.update(metric='Unrelated label' if i in {1, 4} else '国内价格 海外价格 现货升贴水 期限结构 内外比价 库存 供给',
                     sheet='Entirely different worksheet' if i in {1, 4} else '核心标的 宏观 平衡 需求')
    document = {'items': [choice('field-4', '宏观'), choice('field-1', '核心标的')]}
    a, _ = suggestions.validate_suggestions(document, ordinary)
    b, _ = suggestions.validate_suggestions(document, changed)
    assert [(row['series_id'], row['group'], row['reason']) for row in a] == [
        (row['series_id'], row['group'], row['reason']) for row in b]
    assert [row['series_id'] for row in b] == ['field-4', 'field-1']
    assert suggestions.validate_suggestions({'items': []}, changed) == ([], 0)


@pytest.mark.parametrize(('document', 'message'), [
    ({'items': [choice('missing')]}, '不存在'),
    ({'items': [choice(group='其他')]}, '研究分组'),
    ({'items': 'invalid'}, 'JSON'),
    (['invalid'], 'JSON'),
    ({'items': [{'series_id': 'field-0', 'group': '供给'}]}, '理由'),
])
def test_invalid_model_suggestions_report_reason_and_never_fall_back(document, message):
    with pytest.raises(HTTPException) as caught:
        suggestions.validate_suggestions(document, catalog())
    assert caught.value.status_code == 422 and message in caught.value.detail


@pytest.mark.parametrize('document', [None, {'worksheets': 'bad'},
    {'worksheets': [{'worksheet_id': 'unknown', 'group': '供给'}]},
    {'worksheets': [{'worksheet_id': ['w1'], 'group': '供给'}]},
    {'worksheets': [{'worksheet_id': 'w1', 'group': '其他'}]}])
def test_invalid_first_step_never_uses_a_fixed_worksheet_or_indicator(document):
    with pytest.raises(HTTPException) as caught:
        suggestions.selected_fields(document, suggestions.worksheets(catalog()))
    assert caught.value.status_code == 422


def test_two_calls_include_the_framework_metadata_and_model_ordered_400_field_truncation(monkeypatch):
    data = {**catalog(250, sheet='First worksheet'), **catalog(200, sheet='Second worksheet', start=250)}
    monkeypatch.setattr(suggestions, 'catalog_snapshot', lambda active: {'catalog': data, 'version': 'version'})
    calls = []
    replies = [
        {'worksheets': [{'worksheet_id': 'w2', 'group': '需求'}, {'worksheet_id': 'w1', 'group': '供给'},
                        {'worksheet_id': 'w2', 'group': '库存'}]},
        {'items': [choice('field-300', '需求'), choice('field-2', '供给')]},
    ]

    async def complete(active, model_id, prompt, *, purpose=None):
        calls.append((model_id, prompt, purpose))
        return replies.pop(0)

    monkeypatch.setattr(strategy_text, 'completion', complete)
    result = asyncio.run(suggestions.generate_suggestions(scope(), 'platform-model'))
    assert len(calls) == 2 and [call[2] for call in calls] == ['core_focus_sheets', 'core_focus_fields']
    assert all(call[0] == 'platform-model' and RESEARCH_FRAMEWORK in call[1] for call in calls)
    assert 'field-0' not in calls[0][1] and '"field_count":250' in calls[0][1]
    fields = json.loads(calls[1][1].split('\n当前真实字段：')[1])
    assert len(fields) == 400
    assert [row['series_id'] for row in fields] == [*[f'field-{i}' for i in range(250, 450)],
                                                    *[f'field-{i}' for i in range(200)]]
    assert all({'name', 'unit', 'frequency', 'sheet', 'series_id'} <= row.keys() for row in fields)
    assert result['field_count'] == 450 and result['selected_field_count'] == 400 and result['truncated'] is True
    assert '450' in result['notice'] and '400' in result['notice'] and '第一步工作表顺序' in result['notice']
    assert [item['series_id'] for item in result['items']] == ['field-300', 'field-2']


def test_empty_model_worksheet_selection_does_not_fall_back_or_make_second_call(monkeypatch):
    monkeypatch.setattr(suggestions, 'catalog_snapshot', lambda active: {'catalog': catalog()})
    calls = []

    async def complete(*args, **kwargs):
        calls.append(args)
        return {'worksheets': []}

    monkeypatch.setattr(strategy_text, 'completion', complete)
    result = asyncio.run(suggestions.generate_suggestions(scope(), 'model'))
    assert len(calls) == 1 and result['items'] == [] and '未使用固定列表' in result['notice']


def test_endpoint_requires_platform_model_and_writable_unrestricted_source_scope(monkeypatch):
    app = FastAPI()
    app.include_router(suggestions.router)
    active = [scope()]
    monkeypatch.setattr(suggestions, 'current_workbench', SimpleNamespace(get=lambda: active[0]))

    async def generate(active, model_id):
        return {'items': [choice()], 'model_id': model_id}

    monkeypatch.setattr(suggestions, 'generate_suggestions', generate)
    monkeypatch.setattr(suggestions, 'require_shared_write', lambda active: None)
    path = '/api/sn/market/core/suggestions'

    async def check():
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url='http://test') as client:
            assert (await client.post(path, json={})).status_code == 422
            assert (await client.post(path, json={'model_id': 'platform-model', 'api_key': 'forbidden'})).status_code == 422
            response = await client.post(path, json={'model_id': 'platform-model'})
            assert response.status_code == 200 and response.json()['model_id'] == 'platform-model'
            for denied in (scope(restricted=True), scope(metadata={'can_read_source': False}),
                           scope(identity=SimpleNamespace(can_write=False)), scope(authorization='')):
                active[0] = denied
                assert (await client.post(path, json={'model_id': 'platform-model'})).status_code == 403
    asyncio.run(check())


def setup_platform(monkeypatch, session, replies):
    calls = []

    class Client:
        def __init__(self, **kwargs):
            assert kwargs['trust_env'] is False and kwargs['follow_redirects'] is False

        async def __aenter__(self):
            return self

        async def __aexit__(self, *args):
            pass

        async def post(self, url, **kwargs):
            calls.append((url, kwargs))
            reply = replies.pop(0)
            if isinstance(reply, BaseException):
                raise reply
            status, content = reply
            body = {'success': True, 'data': {'model_id': 'platform-model', 'content': content}}
            return httpx.Response(status, json=body, request=httpx.Request('POST', url))

    @contextmanager
    def artifact_session(active):
        yield session

    monkeypatch.setattr(strategy_text.httpx, 'AsyncClient', Client)
    monkeypatch.setattr(strategy_text.settings, 'weknora_api_url', 'http://synthetic.invalid')
    monkeypatch.setattr(strategy_text.citations, 'artifact_session', artifact_session)
    return calls


def test_two_platform_calls_use_the_existing_route_and_persist_llm_audit(monkeypatch, session):
    monkeypatch.setattr(suggestions, 'catalog_snapshot', lambda active: {'catalog': catalog()})
    calls = setup_platform(monkeypatch, session, [
        (200, json.dumps({'worksheets': [{'worksheet_id': 'w1', 'group': '供给'}]})),
        (200, json.dumps({'items': [choice()]})),
    ])
    result = asyncio.run(suggestions.generate_suggestions(scope(), 'platform-model'))
    assert result['items'][0]['series_id'] == 'field-0'
    assert len(calls) == 2 and all(url.endswith('/api/v1/workspaces/research/bench/strategy/generate') for url, _ in calls)
    assert all(kwargs['json']['model_id'] == 'platform-model' and kwargs['json']['sources_revision'] == 3 for _, kwargs in calls)
    rows = list(session.scalars(select(LlmCall).order_by(LlmCall.id)))
    assert [row.purpose for row in rows] == ['core_focus_sheets', 'core_focus_fields']
    assert all(row.status == 'ok' and row.provider == 'workbench' for row in rows)
    assert [row.request['messages'][0]['content'] for row in rows] == [kwargs['json']['prompt'] for _, kwargs in calls]
    assert all('Authorization' not in json.dumps(row.request) for row in rows)


@pytest.mark.parametrize(('status', 'content', 'message'), [
    (200, '{invalid JSON', '无效 JSON'), (200, '[]', 'JSON 对象'), (503, '', '暂时不可用')])
def test_invalid_json_and_model_failure_are_audited_with_explicit_failure(monkeypatch, session, status, content, message):
    setup_platform(monkeypatch, session, [(status, content)])
    with pytest.raises(HTTPException) as caught:
        asyncio.run(strategy_text.completion(scope(), 'platform-model', 'full prompt', purpose='core_focus_sheets'))
    assert message in caught.value.detail
    row = session.scalar(select(LlmCall))
    assert row.status == 'error' and row.purpose == 'core_focus_sheets'
    assert row.request['messages'][0]['content'] == 'full prompt'
    if status == 200:
        assert row.response['text'] == content


def test_cancelled_or_timed_out_platform_call_is_audited_as_error(monkeypatch, session):
    setup_platform(monkeypatch, session, [asyncio.CancelledError()])
    with pytest.raises(asyncio.CancelledError):
        asyncio.run(strategy_text.completion(scope(), 'platform-model', 'full prompt', purpose='core_focus_sheets'))
    row = session.scalar(select(LlmCall))
    assert row.status == 'error' and row.error == '模型调用已取消或超时'


def test_audit_failure_never_returns_an_unaudited_suggestion(monkeypatch, session):
    setup_platform(monkeypatch, session, [(200, '{"worksheets":[]}')])

    def fail(*args, **kwargs):
        raise RuntimeError('synthetic audit failure')

    monkeypatch.setattr(strategy_text, '_record_completion', fail)
    with pytest.raises(HTTPException) as caught:
        asyncio.run(strategy_text.completion(scope(), 'platform-model', 'full prompt', purpose='core_focus_sheets'))
    assert caught.value.status_code == 503 and '审计未能保存' in caught.value.detail
