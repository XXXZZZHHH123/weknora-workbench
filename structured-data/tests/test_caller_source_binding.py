"""Executing a model's explicit source plan without a second model round-trip.

Fixture headers and values are synthetic; they are not acceptance facts.
"""
import pytest

from structured_data.model_settings import thinking_fields, thinking_format
from structured_data.source_context import verify_selection
from test_source_context import duplicate_source  # noqa: F401  (pytest fixture)

GROUP = ['C', 'E', 'G', 'I']


def _never_called(payload):
    raise AssertionError('independent source model must not run when a valid plan was supplied')


def test_valid_caller_plan_executes_without_independent_model(duplicate_source):
    store, dataset = duplicate_source
    plan = [{'considered_ids': GROUP, 'selected_ids': ['G'], 'reason': '模型依据完整问题与实际公式来源选择该列'}]
    assert verify_selection(store, dataset, ['G'], '任意完整问题', _never_called, source_binding=plan) is None


@pytest.mark.parametrize('plan,needle', [
    ([{'considered_ids': ['C', 'G'], 'selected_ids': ['G'], 'reason': '遗漏同名来源'}], '同名来源组'),
    ([{'considered_ids': GROUP, 'selected_ids': ['X'], 'reason': '虚构ID'}], 'selected_ids'),
    ([{'considered_ids': GROUP, 'selected_ids': ['G'], 'reason': ''}], '理由'),
    ([{'considered_ids': GROUP, 'selected_ids': ['C'], 'reason': '选择了与请求不同的列'}], '未包含'),
    ([{'considered_ids': GROUP, 'selected_ids': [], 'reason': '尚未选择'}], 'selected_ids'),
    ([], '全部实际同名来源组'),
    (['not-a-binding'], ''),
])
def test_invalid_caller_plan_fails_closed_without_query_or_model(duplicate_source, plan, needle):
    store, dataset = duplicate_source
    result = verify_selection(store, dataset, ['G'], '任意完整问题', _never_called, source_binding=plan)
    assert result['status'] == 'plan_mismatch' and result['code'] == 'source_binding_invalid'
    assert needle in result['error']
    assert 'rows' not in result and 'aggregate' not in result
    # The real sibling group is returned so the plan can be corrected from evidence.
    assert {f['series_id'] for f in result['fields'][0]} == set(GROUP)


def test_caller_plan_still_requires_question(duplicate_source):
    store, dataset = duplicate_source
    plan = [{'considered_ids': GROUP, 'selected_ids': ['G'], 'reason': '依据'}]
    assert verify_selection(store, dataset, ['G'], None, _never_called, source_binding=plan)['status'] == 'error'


def test_unavailable_independent_review_returns_real_group_for_a_model_plan(duplicate_source):
    store, dataset = duplicate_source

    def timeout(payload):
        raise ValueError('来源模型核验超出本次时间预算；未执行取数')
    failed = verify_selection(store, dataset, ['I'], '另一个完整问题', timeout)
    assert failed['code'] == 'source_binding_unavailable' and 'rows' not in failed
    assert {f['series_id'] for f in failed['fields'][0]} == set(GROUP)
    assert 'source_binding' in failed['next_step']


def test_non_duplicate_field_ignores_plan_and_needs_no_model(duplicate_source):
    store, dataset = duplicate_source
    assert verify_selection(store, dataset, ['B'], '问题', _never_called, source_binding=None) is None


def test_signed_query_executes_validated_caller_plan(duplicate_source, monkeypatch):
    from fastapi.testclient import TestClient
    from structured_data.server import create_app
    from structured_data.field_resolver import FieldResolver
    from test_scoped_service import signed
    store, dataset = duplicate_source
    monkeypatch.setattr(FieldResolver, '__init__', lambda self, data: self.__dict__.update(store=data, model_fn=_never_called))
    key = b'caller-source-binding-guard-1234567890'
    path = store.root / 'read-key'
    path.write_bytes(key)
    monkeypatch.setenv('STRUCTURED_SERVICE_TOKEN_FILE', str(path))
    monkeypatch.setenv('STRUCTURED_TENANT_ID', '10000')
    plan = [{'considered_ids': GROUP, 'selected_ids': ['G'], 'reason': '模型依据完整问题选择'}]
    with TestClient(create_app(store.root)) as client:
        raw, headers = signed(key, body={'operation': 'query_series', 'arguments': {
            'dataset_id': dataset, 'series_id': 'G', 'source_question': '完整问题', 'source_binding': plan}})
        body = client.post('/api/tools/read', content=raw, headers=headers).json()
        assert body.get('rows') and body['rows'][0]['source_cell'] == 'G3'
        bad = [{'considered_ids': ['G'], 'selected_ids': ['G'], 'reason': '遗漏'}]
        raw, headers = signed(key, body={'operation': 'analyze_table', 'arguments': {
            'dataset_id': dataset, 'series_ids': ['G'], 'sql': 'SELECT sum(value) FROM data',
            'source_question': '完整问题', 'source_binding': bad}})
        body = client.post('/api/tools/read', content=raw, headers=headers).json()
        assert body['status'] == 'plan_mismatch' and 'rows' not in body


def test_signed_multifield_lookup_uses_one_complete_binding_without_scalar_queries(duplicate_source, monkeypatch):
    from fastapi.testclient import TestClient
    from structured_data.server import create_app
    from structured_data.field_resolver import FieldResolver
    from structured_data import source_context
    from structured_data.store import DataStore
    from test_scoped_service import signed
    store, dataset = duplicate_source
    monkeypatch.setattr(FieldResolver, '__init__', lambda self, data: self.__dict__.update(
        store=data, model_fn=_never_called))
    validations = []
    original_verify = source_context.verify_selection

    def verify(*args, **kwargs):
        validations.append(list(args[2]))
        return original_verify(*args, **kwargs)

    def scalar_query(*args, **kwargs):
        raise AssertionError('multi-field input must not be expanded into scalar queries')

    monkeypatch.setattr(source_context, 'verify_selection', verify)
    monkeypatch.setattr(DataStore, 'query_series', scalar_query)
    key = b'multifield-source-binding-guard-1234567890'
    path = store.root / 'read-key'
    path.write_bytes(key)
    monkeypatch.setenv('STRUCTURED_SERVICE_TOKEN_FILE', str(path))
    monkeypatch.setenv('STRUCTURED_TENANT_ID', '10000')
    plan = [{'considered_ids': GROUP, 'selected_ids': GROUP,
             'reason': 'The request compares all actual alternatives on the same date.'}]
    with TestClient(create_app(store.root)) as client:
        raw, headers = signed(key, body={'operation': 'analyze_table', 'arguments': {
            'dataset_id': dataset, 'series_ids': GROUP,
            'sql': 'SELECT series_id,date,value,source_cell,date_cell FROM data ORDER BY series_id',
            'start_date': '2023-01-01', 'end_date': '2023-01-01',
            'source_question': 'Compare all four actual source alternatives on this date.',
            'source_binding': plan}})
        response = client.post('/api/tools/read', content=raw, headers=headers)
        assert response.status_code == 200
        body = response.json()
    assert validations == [GROUP]
    assert body['status'] == 'ok' and body['input_complete'] is True
    assert body['input_count'] == 4 and body['output_truncated'] is False
    assert body['rows'] == [[sid, '2023-01-01', value, sid + '3', 'A3']
                            for sid, value in zip(GROUP, [100, 130, 200, 230])]
    assert set(body['inputs']) == set(GROUP)
    assert body['source']['sha256'] == store.manifest(dataset)['sha256']
    assert body['result_contract']


@pytest.mark.parametrize('cfg,expected_off', [
    ({'base_url': 'https://ark.cn-beijing.volces.com/api/v3'}, {'thinking': {'type': 'disabled'}}),
    ({'base_url': 'https://api.deepseek.com'}, {'thinking': {'type': 'disabled'}}),
    ({'base_url': 'https://dashscope.aliyuncs.com/compatible-mode/v1'}, {'enable_thinking': False}),
    ({'base_url': 'https://unknown.example/v1'}, {'enable_thinking': False}),
    ({'base_url': 'https://ark.cn-beijing.volces.com/api/v3', 'thinking_format': 'none'}, {}),
    ({'base_url': 'https://unknown.example/v1', 'thinking_format': 'thinking-type'}, {'thinking': {'type': 'disabled'}}),
    # Suffix match only: a lookalike host does not inherit another vendor's protocol.
    ({'base_url': 'https://volces.com.evil.example/v1'}, {'enable_thinking': False}),
])
def test_thinking_toggle_uses_endpoint_protocol(cfg, expected_off):
    assert thinking_fields(cfg, False) == expected_off
    assert thinking_fields(cfg, None) == {}
    assert thinking_format(cfg) in ('thinking-type', 'enable-thinking', 'none')
