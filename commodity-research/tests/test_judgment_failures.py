"""Only finite application reasons and schema labels reach background notices."""
import asyncio
from contextlib import contextmanager
import json
from types import SimpleNamespace

from fastapi import HTTPException
import pytest

from tin.web import judgment_failures as failures
from tin.web import judgment_jobs as jobs
from tin.web.judgment_job_registry import Registry, metadata


def validation(code, path='/', **extra):
    return HTTPException(422, {'code': 'judgment_validation', 'message': 'private source marker',
        'issues': [{'code': code, 'path': path, 'message': 'kb-private-series bearer-secret', **extra}]})


@pytest.mark.parametrize('error,expected', [
    (TimeoutError('private backend address'), 'timeout'),
    (HTTPException(503, '连接模型服务超时，请检查网络或代理设置'), 'timeout'),
    (HTTPException(422, '模型输出达到长度上限，未生成完整研究逻辑；请精炼输出要求或更换模型后重试'), 'output_limit'),
    (HTTPException(503, '模型输出达到长度上限，未生成完整研究逻辑；请精炼输出要求或更换模型后重试'), 'output_limit'),
    (HTTPException(422, '模型未给出有效的真实指标选择'), 'selection_invalid'),
    (HTTPException(422, '模型选出的候选指标过多，超出本次分析容量'), 'selection_too_broad'),
    (HTTPException(422, '模型筛选指标时输出超出长度上限'), 'selection_output_limit'),
    (validation('source_coverage'), 'source_coverage'),
    (validation('field_schema', '/judgment/pricing_power/category'), 'field_schema'),
    (validation('unknown_series_id', '/binding_units/kb-private-series'), 'selection_invalid'),
    (validation('number_not_in_source', '/judgment/thresholds/0/value'), 'number_validation'),
    (validation('unit_mismatch', '/binding_units/kb-private-series'), 'unit_validation'),
    (validation('rule_periods', '/judgment/falsifiers/0/consecutive_periods'), 'period_validation'),
    (validation('quote_not_in_source', '/rule_quotes/private-rule'), 'quote_validation'),
    (validation('priority_reference', '/judgment/priority_rule/order'), 'relation_validation'),
    (HTTPException(422, {'code': 'number_precision', 'message': 'private text'}), 'number_validation'),
    (HTTPException(422, '模型没有返回完整有效的配置，请调整资料后重试'), 'invalid_json'),
    (HTTPException(413, 'private source marker'), 'capacity'),
    (HTTPException(401, 'bearer-secret'), 'auth_expired'),
    (HTTPException(403, 'private source marker'), 'permission_denied'),
    (HTTPException(404, 'private source marker'), 'resource_missing'),
    (HTTPException(409, 'private source marker'), 'source_changed'),
    (HTTPException(429, 'private provider address'), 'rate_limited'),
    (HTTPException(503, 'private provider address'), 'service_unavailable'),
    (RuntimeError('private provider address'), 'analysis_failed'),
])
def test_only_known_categories_explain_the_failure(error, expected):
    public = failures.failure_for(error, 'validating')
    assert public['diagnostic']['code'] == expected
    assert public['diagnostic']['stage'] == 'validating'
    raw = json.dumps(public, ensure_ascii=False)
    assert all(private not in raw for private in ('private', 'bearer-secret', 'kb-private-series', '/judgment/'))


@pytest.mark.parametrize('path,category,label', [
    ('/judgment/contradiction/bull/evidence/0/series_id/kb-private-series', 'bull_evidence', '多方依据'),
    ('/judgment/contradiction/bear/claim', 'bear_claim', '空方观点'),
    ('/judgment/pricing_power/kb-private-series', 'pricing_power', '定价权'),
    ('/judgment/time_dimension/short', 'time_dimension', '时间维度'),
    ('/binding_units/kb-private-series', 'binding_units', '指标单位'),
    ('/rule_quotes/private-rule', 'rule_quotes', '原文依据'),
    ('/source_coverage/secret-source-id', 'source_coverage', '原文覆盖'),
    ('/unresolved/0/name', 'unresolved', '补充说明'),
    ('/judgment/unstructured/0/rule_kind', 'unresolved', '补充说明'),
])
def test_fields_are_business_labels_and_dynamic_path_suffixes_never_appear(path, category, label):
    public = failures.failure_for(validation('field_schema', path))
    assert public['diagnostic']['fields'] == [category]
    assert public['message'].startswith(label + '：')
    assert path not in public['message'] and 'series_id' not in public['message']
    assert 'kb-private' not in public['message'] and 'secret-source' not in public['message']


@pytest.mark.parametrize('detail', [
    'https://private.invalid bearer-secret',
    '模型未给出有效的真实指标选择 bearer-secret',
    {'message': 'bearer-secret', 'issues': [{'code': 'field_schema', 'path': '/judgment/variety'}]},
    {'code': ['judgment_validation'], 'message': 'bearer-secret', 'issues': []},
    {'code': {'code': 'judgment_validation'}, 'issues': []},
    {'code': 'judgment_validation', 'issues': {'code': 'field_schema', 'message': 'bearer-secret'}},
    {'code': 'judgment_validation', 'issues': [{'code': {'code': 'field_schema'}, 'message': 'bearer-secret'}]},
    {'code': 'judgment_validation', 'issues': [{'code': 'unknown-private-code', 'path': '/judgment/variety', 'message': 'bearer-secret'}]},
    {'code': 'judgment_validation', 'issues': [None, ['field_schema'], {'code': 'field_schema', 'path': ['private']}]},
])
def test_unknown_or_forged_nested_details_never_pass_raw_content(detail):
    public = failures.failure_for(HTTPException(422, detail), 'https://private.invalid')
    assert public['diagnostic']['stage'] == 'unknown'
    raw = json.dumps(public, ensure_ascii=False)
    assert not any(private in raw for private in ('bearer-secret', 'private.invalid', 'unknown-private-code'))
    assert 'message' not in public['diagnostic']


def test_issue_counts_and_fields_are_bounded_and_warning_cannot_become_a_failure_reason():
    error = HTTPException(422, {'code': 'judgment_validation', 'issues': [
        {'code': 'unknown_series_id', 'path': '/binding_units/secret', 'severity': 'warning'},
        *[{'code': 'field_schema', 'path': '/judgment/' + field, 'message': 'secret'}
          for field in failures.FIELD_LABELS],
    ]})
    public = failures.failure_for(error, 'analyzing')
    assert public['diagnostic']['code'] == 'field_schema'
    assert len(public['diagnostic']['fields']) == 4
    assert len(public['message']) < 300


@pytest.mark.parametrize('value', [
    None, {}, {'code': 'field_schema', 'stage': 'validating', 'fields': ['private']},
    {'code': 'field_schema', 'stage': 'private', 'fields': []},
    {'code': 'field_schema', 'stage': 'validating', 'fields': [], 'message': 'bearer-secret'},
    {'code': 'field_schema', 'stage': 'validating', 'fields': ['pricing_power'] * 2},
    {'code': [], 'stage': 'validating', 'fields': []},
])
def test_invalid_persisted_diagnostics_fail_closed(value):
    assert failures.safe_diagnostic(value) is None
    assert failures.render_failure(value) == failures.MESSAGES['analysis_failed']


def scope():
    return SimpleNamespace(identity=SimpleNamespace(tenant_id=29, user_id='synthetic-owner'),
        workbench_id='synthetic-workbench', metadata={})


@pytest.fixture
def runtime_case(tmp_path, monkeypatch):
    registry = Registry(tmp_path / 'isolated-job-index.sqlite')
    monkeypatch.setattr(jobs, 'registry', registry)
    runtime = jobs.Runtime()
    current = scope()
    async def fresh(value): return value
    monkeypatch.setattr(jobs, 'fresh_scope', fresh)
    @contextmanager
    def empty_results(_):
        yield SimpleNamespace(get=lambda *args, **kwargs: None)
    monkeypatch.setattr(jobs, 'artifact_session', empty_results)
    return current, registry, runtime


@pytest.mark.parametrize('error,reason,label', [
    (validation('field_schema', '/judgment/pricing_power/category'), 'field_schema', '定价权'),
    (validation('unknown_series_id', '/binding_units/kb-private-series'), 'selection_invalid', '指标绑定'),
    (validation('source_coverage', '/source_coverage/secret-source-id'), 'source_coverage', '原文覆盖'),
    (HTTPException(422, '模型未给出有效的真实指标选择'), 'selection_invalid', ''),
    (HTTPException(422, '模型输出达到长度上限，未生成完整研究逻辑；请精炼输出要求或更换模型后重试'), 'output_limit', ''),
    (TimeoutError('bearer-secret'), 'timeout', ''),
])
def test_runtime_persists_same_safe_explanation_in_status_and_list(runtime_case, monkeypatch, error, reason, label):
    current, registry, runtime = runtime_case
    async def analyze(_, body, progress):
        await progress('validating')
        raise error
    monkeypatch.setattr(jobs.generation, '_analyze', analyze)
    meta, _ = registry.create(current, 'body-hash', 'unique-key', runtime.worker_id)
    async def run():
        await runtime.run(current, object(), meta)
        return await jobs.job_status(current, meta['id'])
    status = asyncio.run(run())
    listed = jobs.list_jobs(current.identity)['items'][0]
    assert status['state'] == listed['state'] == 'failed'
    assert status['message'] == listed['message']
    assert status['failure'] == {'code': reason, 'stage': 'validating',
        'fields': failures.failure_for(error)['diagnostic']['fields']}
    assert label in status['message']
    raw = json.dumps([status, listed], ensure_ascii=False)
    assert all(private not in raw for private in ('kb-private-series', 'secret-source-id', 'bearer-secret', '/binding_units'))
    stored = registry.get(current.identity, meta['id'])
    assert stored['failure'] == status['failure']


def test_diagnostic_finish_is_fenced_and_cannot_change_an_already_finished_job(runtime_case):
    current, registry, runtime = runtime_case
    meta, _ = registry.create(current, 'hash', 'key', runtime.worker_id)
    failure = failures.failure_for(validation('field_schema', '/judgment/cost_line'))
    registry.finish(meta['id'], runtime.worker_id, 'stale-fence', 'failed', 'raw secret', diagnostic=failure['diagnostic'])
    assert registry.get(current.identity, meta['id'])['state'] == 'queued'
    registry.finish(meta['id'], runtime.worker_id, meta['fence'], 'failed', 'raw secret', diagnostic=failure['diagnostic'])
    before = registry.get(current.identity, meta['id'])
    registry.finish(meta['id'], runtime.worker_id, meta['fence'], 'failed', 'another secret',
        diagnostic=failures.failure_for(TimeoutError())['diagnostic'])
    assert registry.get(current.identity, meta['id']) == before
    assert 'secret' not in metadata(before)['message']


def test_diagram_jobs_are_unchanged_by_optional_judgment_diagnostic(runtime_case):
    current, registry, runtime = runtime_case
    meta, _ = registry.create(current, 'hash', 'key', runtime.worker_id, 'diagram')
    registry.finish(meta['id'], runtime.worker_id, meta['fence'], 'failed', 'Safe diagram message')
    public = metadata(registry.get(current.identity, meta['id']))
    assert public['message'] == 'Safe diagram message'
    assert 'failure' not in public


def test_unknown_failure_does_not_echo_error_and_corrupt_index_diagnostic_is_not_exposed(runtime_case, monkeypatch):
    current, registry, runtime = runtime_case
    async def analyze(*args): raise RuntimeError('https://private.invalid bearer-secret')
    monkeypatch.setattr(jobs.generation, '_analyze', analyze)
    meta, _ = registry.create(current, 'hash', 'key', runtime.worker_id)
    asyncio.run(runtime.run(current, object(), meta))
    with registry.transaction() as connection:
        stored = registry.fetch(connection, meta['id'])
        stored.update(message='bearer-secret', failure={'code': 'field_schema', 'stage': 'private', 'fields': []})
        registry.write(connection, stored)
    public = jobs.list_jobs(current.identity)['items'][0]
    assert public['message'] == failures.MESSAGES['analysis_failed']
    assert 'failure' not in public
    assert 'bearer-secret' not in json.dumps(public)


def test_arbitrary_exception_stringification_is_never_invoked():
    class PrivateError(Exception):
        def __str__(self):
            raise AssertionError('Exception text must never be rendered')
    assert failures.failure_for(PrivateError())['message'] == failures.MESSAGES['analysis_failed']
