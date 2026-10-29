"""Private analysis jobs over real temporary result stores; all analysis is synthetic."""
import asyncio
from contextlib import contextmanager
from copy import deepcopy
from dataclasses import asdict
import importlib
import json
from types import SimpleNamespace

from fastapi import HTTPException, Request
import pytest
from sqlalchemy import select

from tin.config import settings
from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, artifact_session, current_workbench
from tin.judgments.analysis import assess_draft
from tin.judgments.service import latest, latest_workbench_payload
from tin.models import SourceMapping
from tin.web import judgment_generation as generation
from tin.web import judgment_review as review


WB = '10000000-0000-4000-8000-000000000431'
OTHER_WB = '10000000-0000-4000-8000-000000000432'
SERIES = 'kb-' + 'a' * 48
VERSION = 'c' * 64
SOURCE = 'Synthetic supply is uncertain. Synthetic stock above 12 kg requires review.'
PRIVATE_TITLE = 'Synthetic private source title'


@contextmanager
def active(scope):
    identity_marker = current_identity.set(scope.identity)
    workbench_marker = current_workbench.set(scope)
    try:
        yield
    finally:
        current_workbench.reset(workbench_marker)
        current_identity.reset(identity_marker)


@pytest.fixture
def job_case(request, tmp_path, monkeypatch):
    jobs = importlib.import_module('tin.web.judgment_jobs')
    mode = getattr(request, 'param', 'legacy')
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://synthetic.invalid')
    monkeypatch.setattr(jobs, 'registry', jobs.Registry(path=tmp_path / 'private-job-registry.sqlite'))
    monkeypatch.setattr(jobs, 'runtime', jobs.Runtime())
    monkeypatch.setattr(generation, 'previews', generation.PreviewStore())
    state = {'can_edit': True, 'can_read_source': True, 'revision': 1,
        'fact_version': VERSION, 'fail_analysis': False, 'calls': [], 'observed': []}

    def scope(user='audit-a', *, tenant=43, workbench=WB):
        identity = Identity(user, user + '@synthetic.invalid', user, tenant, 'contributor')
        sources = ({'id': 'synthetic-binding', 'source_type': 'knowledge_base',
            'source_id': 'synthetic-kb', 'knowledge_base_id': 'synthetic-kb', 'tenant_id': tenant,
            'permission': 'editor', 'delegated': False, 'view_modules': ['judgments']},)
        return WorkbenchScope(identity, workbench, 'synthetic-kb', tenant,
            {'workspace_mode': mode, 'can_edit': state['can_edit'],
                'can_read_source': state['can_read_source']},
            'Bearer audit-secret-' + user, scope_version=2,
            sources_revision=state['revision'], sources=sources)

    catalog = [{'id': SERIES, 'name': 'Synthetic stock', 'unit': 'kg',
        'knowledge_base_id': 'synthetic-kb', 'knowledge_base_tenant_id': 43,
        'source_file': 'synthetic.csv', 'sheet': 'Synthetic', 'source_version': VERSION}]

    def snapshot(variety=None):
        current = current_workbench.get()
        assert current is not None
        with artifact_session(current) as session:
            row = latest(session, variety or 'XY')
            return {'variety': variety or 'XY', 'fact_version': state['fact_version'],
                'base_version': row.version if row else 0, 'catalog': deepcopy(catalog),
                'base_payload': latest_workbench_payload(session, variety or 'XY', current)}

    async def resolve(identity, workbench_id, authorization):
        assert authorization == 'Bearer audit-secret-' + identity.user_id
        return scope(identity.user_id, tenant=identity.tenant_id, workbench=workbench_id)

    async def analyze(current, body, progress=None):
        state['calls'].append((current.identity.user_id, current.workbench_id, body.model_id))
        state['observed'].append((current_identity.get(), current_workbench.get()))
        if state['fail_analysis']:
            raise RuntimeError(state['fail_analysis'] if isinstance(state['fail_analysis'], str)
                else 'Synthetic analysis failure')
        document = {'judgment': {'contradiction': {'bull': {'claim': 'Synthetic supply is uncertain'}},
            'marginal_focus': [{'desc': 'Synthetic stock matters', 'series_id': SERIES}],
            'thresholds': [{'id': 'boundary', 'name': 'Synthetic condition', 'series_id': SERIES,
                'operator': '>', 'value': 12, 'type': '观察位', 'horizon': '短期'}]},
            'binding_units': {SERIES: 'kg'},
            'rule_quotes': {'boundary': 'Synthetic stock above 12 kg'}, 'unresolved': []}
        assessment = assess_draft(document, body.text, catalog, variety='XY', actor=current.identity.user_id)
        preview = {'variety': 'XY', 'text': body.text, 'written_at': '2030-01-02',
            'fact_version': state['fact_version'], 'base_version': body.base_version,
            'source_name': body.source_name, 'panel_title': body.panel_title,
            'model_id': body.model_id, 'validation_issues': [], 'repair_attempts': 0}
        review.initialize_review(preview, assessment, catalog)
        return preview, snapshot('XY')

    def page(*args, **kwargs):
        return {'version': state['fact_version'], 'series': [{'series_id': SERIES,
            'metric': 'Synthetic stock', 'unit': 'kg'}], 'total': 1, 'offset': 0,
            'next_offset': None, 'datasets': []}

    monkeypatch.setattr(generation, 'snapshot', snapshot)
    monkeypatch.setattr(generation, '_analyze', analyze)
    monkeypatch.setattr(generation, 'resolve_workbench', resolve)
    monkeypatch.setattr(review, 'resolve_workbench', resolve)
    monkeypatch.setattr(review, 'catalog_page', page)
    if hasattr(jobs, 'resolve_workbench'):
        monkeypatch.setattr(jobs, 'resolve_workbench', resolve)
    if hasattr(jobs, 'verify_bearer'):
        async def verify(token, tenant=None):
            assert token.startswith('audit-secret-')
            return scope(token.removeprefix('audit-secret-'), tenant=int(tenant or 43)).identity
        monkeypatch.setattr(jobs, 'verify_bearer', verify)
    return SimpleNamespace(jobs=jobs, scope=scope, first=scope(), second=scope('audit-b'),
        state=state, catalog=catalog, path=tmp_path)


def run(case, scenario):
    async def execute():
        try:
            return await scenario()
        finally:
            await case.jobs.runtime.shutdown()
    return asyncio.run(execute())


async def completed(case, scope=None, key='synthetic-isolation-request'):
    scope = scope or case.first
    body = generation.GenerateRequest(model_id='synthetic-model', text=SOURCE,
        source_name=PRIVATE_TITLE, base_version=0, variety='XY')
    with active(scope):
        queued = await case.jobs.start_job(scope, body, key)
        for _ in range(500):
            status = await case.jobs.job_status(scope, queued['id'])
            if status['state'] not in {'queued', 'running'}:
                assert status['state'] == 'succeeded', status
                return queued['id'], await case.jobs.job_result(scope, queued['id'])
            await asyncio.sleep(0.01)
    pytest.fail('Synthetic analysis job never finished')


@pytest.mark.parametrize('job_case', ['legacy', 'isolated'], indirect=True)
def test_job_ids_and_canonical_tokens_cannot_cross_user_tenant_or_workbench(job_case):
    case = job_case
    async def scenario():
        identifier, result = await completed(case)
        token = result['draft_id']
        for foreign in (case.second, case.scope(tenant=44), case.scope(workbench=OTHER_WB)):
            with active(foreign):
                for operation in (case.jobs.job_status, case.jobs.job_result):
                    with pytest.raises(HTTPException) as denied:
                        await operation(foreign, identifier)
                    assert denied.value.status_code in {403, 404, 409}
                with pytest.raises(HTTPException):
                    generation.previews.get(foreign, token)
                if foreign.identity != case.first.identity:
                    assert case.jobs.list_jobs(foreign.identity)['items'] == []
        with active(case.first):
            assert generation.previews.get(case.first, token)['text'] == SOURCE
    run(case, scenario)


@pytest.mark.parametrize('field,value', [
    ('kind', 'unrelated-config'), ('tenant_id', 44), ('user_id', 'audit-b'),
    ('workbench_id', OTHER_WB), ('id', '0' * 32), ('confirmed_by', 'audit-b'),
])
def test_job_source_mapping_requires_matching_key_owner_and_config(job_case, field, value):
    case = job_case
    async def scenario():
        identifier, result = await completed(case)
        with active(case.first), artifact_session(case.first) as session:
            row = session.get(SourceMapping, case.jobs.job_key(case.first, identifier))
            assert row is not None
            if field == 'confirmed_by':
                row.confirmed_by = value
            else:
                row.config = {**row.config, field: value}
            session.commit()
        generation.previews.items.clear()
        with active(case.first):
            with pytest.raises(HTTPException) as denied:
                await case.jobs.job_result(case.first, identifier)
            assert denied.value.status_code in {403, 404, 409}
            with pytest.raises(HTTPException):
                generation.previews.get(case.first, result['draft_id'])
    run(case, scenario)


def test_global_job_metadata_and_durable_storage_never_serialize_bearer_scope(job_case):
    case = job_case
    async def scenario():
        identifier, result = await completed(case)
        public = case.jobs.list_jobs(case.first.identity)
        assert len(public['items']) == 1 and public['items'][0]['id'] == identifier
        public_json = json.dumps(public, ensure_ascii=False, default=str)
        for private in (SOURCE, PRIVATE_TITLE, result['draft_id'], 'synthetic-model'):
            assert private not in public_json
        assert public['items'][0]['title'] == '研究逻辑分析'
        assert 'authorization' in asdict(case.first)
        secrets = (case.first.authorization, case.first.authorization.removeprefix('Bearer '))
        rendered = json.dumps([public, result], ensure_ascii=False, default=str)
        with artifact_session(case.first) as session:
            configs = session.scalars(select(SourceMapping.config)).all()
            rendered += json.dumps(configs, ensure_ascii=False, default=str)
        for secret in secrets:
            assert secret not in rendered
            for path in case.path.rglob('*'):
                if path.is_file():
                    assert secret.encode() not in path.read_bytes(), str(path)
    run(case, scenario)


def test_worker_identity_context_is_owned_and_failure_diagnostics_are_private(job_case):
    case = job_case
    async def scenario():
        sentinel = case.scope('audit-sentinel', workbench=OTHER_WB)
        with active(sentinel):
            await completed(case)
            assert current_identity.get() == sentinel.identity
            assert current_workbench.get() == sentinel
            diagnostic = 'Synthetic private diagnostic ' + case.second.authorization
            case.state['fail_analysis'] = diagnostic
            body = generation.GenerateRequest(model_id='synthetic-model', text=SOURCE,
                source_name=PRIVATE_TITLE, base_version=0, variety='XY')
            with active(case.second):
                queued = await case.jobs.start_job(case.second, body, 'synthetic-failing-request')
                for _ in range(500):
                    status = await case.jobs.job_status(case.second, queued['id'])
                    if status['state'] not in {'queued', 'running'}:
                        break
                    await asyncio.sleep(0.01)
                assert status['state'] == 'failed'
                assert diagnostic not in json.dumps(status, ensure_ascii=False, default=str)
                assert case.second.authorization not in json.dumps(status, ensure_ascii=False, default=str)
            assert current_identity.get() == sentinel.identity
            assert current_workbench.get() == sentinel
        expected = [case.first, case.second]
        assert len(case.state['observed']) == len(expected)
        for (identity, captured), owner in zip(case.state['observed'], expected, strict=True):
            assert identity == owner.identity and captured == owner
    run(case, scenario)


@pytest.mark.parametrize('changed,value', [
    ('can_edit', False), ('can_read_source', False), ('revision', 2), ('fact_version', 'd' * 64),
])
def test_completed_result_rechecks_current_permissions_and_sources(job_case, changed, value):
    case = job_case
    async def scenario():
        identifier, result = await completed(case)
        calls = len(case.state['calls'])
        case.state[changed] = value
        generation.previews.items.clear()
        fresh = case.scope()
        with active(fresh), pytest.raises(HTTPException) as rejected:
            await case.jobs.job_result(fresh, identifier)
        assert rejected.value.status_code in {401, 403, 409}
        assert len(case.state['calls']) == calls
        assert SOURCE not in str(rejected.value.detail)
    run(case, scenario)


def test_empty_bindings_and_text_only_rule_survive_canonical_preview_reload(job_case):
    case = job_case
    async def scenario():
        identifier, result = await completed(case)
        token = result['draft_id']
        with active(case.first):
            before = generation.previews.get(case.first, token)
            changed = deepcopy(before)
            decisions = []
            for item in changed['review']['items']:
                if item['kind'] in {'binding', 'thresholds'}:
                    item.update(status='kept_text', series_id=None, binding_explicit=False, origin='user')
                    decisions.append((item, False))
            changed['review']['revision'] += 1
            changed['document'] = review.build_document(changed, case.catalog)
            review.record_decisions(case.first, token, before, changed, case.catalog, decisions)
            generation.previews.items.clear()
            reopened = await case.jobs.job_result(case.first, identifier)
            restored = generation.previews.get(case.first, reopened['draft_id'])
            assert reopened['draft_id'] == token
            assert restored['document']['judgment']['marginal_focus'][0]['series_id'] is None
            assert restored['document']['judgment']['thresholds'] == []
            for item in restored['review']['items']:
                if item['kind'] in {'binding', 'thresholds'}:
                    assert item['status'] == 'kept_text' and item['series_id'] is None
                    assert item['ai_series_id'] == SERIES
            item = next(item for item in restored['review']['items'] if item['kind'] == 'binding')
            body = json.dumps({'draft_id': token, 'revision': restored['review']['revision'],
                'item_id': item['id'], 'action': 'keep_text', 'series_id': None, 'remember': True}).encode()
            async def receive():
                return {'type': 'http.request', 'body': body, 'more_body': False}
            request = Request({'type': 'http', 'method': 'POST', 'path': '/api/sn/judgment/review/update',
                'headers': [(b'content-type', b'application/json')]}, receive)
            with pytest.raises(HTTPException) as rejected:
                await review.update_review(request)
            assert rejected.value.status_code == 422
        with artifact_session(case.first) as session:
            preferences = [row for row in session.scalars(select(SourceMapping)).all()
                if row.config.get('kind') == 'judgment-review-binding.v1']
            assert preferences == []
        assert len(case.state['calls']) == 1
    run(case, scenario)
