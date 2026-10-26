"""Synthetic configured-model and explicit KB contract-binding regression."""
import asyncio
from datetime import date, datetime, timedelta, timezone
from types import SimpleNamespace

import httpx
import pytest
from sqlalchemy import select

from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope
from tin.jobs.seed import seed_researcher
from tin.judgments.service import save_version
from tin.llm.platform_provider import PlatformStrategyProvider
from tin.models import Indicator, Judgment, Observation, SourceMapping, Strategy
from tin.schemas.judgment import JudgmentIn
from tin.schemas.strategy import StrategyPlan
from tin.strategy.generate import build_context, generate_candidates, validate_evidence
from tin.web.market_contracts import CONTRACTS_KEY
from tin.web.restricted_views import mapping_key
from tests.test_knowledge_facts import canonical, FIRST, page

WB = '10000000-0000-4000-8000-000000000001'
QUOTE, FUND, OTHER = ['kb-' + digit * 48 for digit in ('1', '2', '3')]
DAY = date(2026, 9, 18)
STAMP = datetime(2026, 9, 18, 7, tzinfo=timezone.utc)


@pytest.fixture
def scoped_strategy(session):
    session.info.update(tenant_id=1, weknora_user_id='researcher', weknora_display_name='Researcher', workbench_id=WB)
    researcher = seed_researcher(session)
    for identifier, unit, value in [(QUOTE, '元/吨', 100), (FUND, '吨', 17), (OTHER, '吨', 23)]:
        session.add(Indicator(series_id=identifier, name='Synthetic source', variety='SN', category='test',
            caliber={}, unit=unit, source='fixture', frequency='日', fetch_mode='manual'))
        session.flush()
        session.add(Observation(series_id=identifier, value=value, as_of=STAMP, fetched_at=STAMP,
            caliber_snapshot={}, source='fixture', revision=0, status='正常'))
    session.add(SourceMapping(id=mapping_key(session, CONTRACTS_KEY), config={'contracts': [{
        'contract': 'SN2611', 'delivery_month': '2026-11', 'close': QUOTE, 'settle': QUOTE,
        'price_unit': '元/吨'}]}, confirmed_by='researcher', confirmed_at=STAMP))
    session.commit()
    row = save_version(session, JudgmentIn(variety='SN', author='researcher', written_at=DAY,
        contradiction={'bull': {'claim': 'Fixture supply', 'evidence': [{'desc': 'Measured', 'series_id': FUND}]},
            'bear': {'claim': 'Fixture demand', 'evidence': [{'desc': 'Measured', 'series_id': OTHER}]}},
        marginal_focus=[{'desc': 'Fixture marginal', 'series_id': FUND}], whitelist=[QUOTE, FUND, OTHER],
        pricing_power={'short': ['Measured evidence'], 'long': ['Measured evidence']}, tone='观望'),
        actor='researcher', activate=True)
    session.commit()
    return session, researcher.id, row


def candidate(context):
    observations = context['observations']
    return StrategyPlan(title='Synthetic configured strategy', kind='outright',
        legs=[{'contract': 'SN2611', 'side': 'buy', 'quote_series_id': QUOTE}],
        thesis='Synthetic inference', alternative_explanation='Competing synthetic inference',
        evidence=[{'series_id': item['series_id'], 'observation_id': item['observation_id'],
            'as_of': item['as_of'], 'source': item['source'], 'value': item['value'], 'statement': 'Fixture input'} for item in observations],
        market_expectation='Explicit inference from fixture data', invalidation='Fixture boundary', catalyst='Fixture review',
        horizon_days=2, as_of=context['market_as_of'], review_at=STAMP + timedelta(days=2),
        entry={'low': 100, 'high': 110, 'reason': 'Synthetic assumption'},
        target={'low': 120, 'high': 130, 'reason': 'Synthetic assumption'},
        stop={'low': 80, 'high': 90, 'reason': 'Synthetic assumption'}, entry_condition='Manual review', risk_budget=100)


def test_workbench_strategy_uses_saved_contract_binding_and_real_kb_ids(scoped_strategy):
    session, researcher, _ = scoped_strategy
    context, gaps = build_context(session, 'SN', DAY, researcher_id=researcher)
    assert gaps == []
    assert context['available_contracts'][0]['quote_series_id'] == QUOTE
    assert context['available_contracts'][0]['quote_kind'] == 'close'
    assert {item['series_id'] for item in context['observations']} == {QUOTE, FUND, OTHER}
    assert validate_evidence(candidate(context), context) == []


def test_configured_platform_model_is_invoked_and_only_a_draft_is_saved(scoped_strategy, monkeypatch):
    session, researcher, _ = scoped_strategy
    context, _ = build_context(session, 'SN', DAY, researcher_id=researcher)
    plan = candidate(context)
    calls = []
    scope = WorkbenchScope(Identity('researcher', '', '', 1, 'owner'), WB, 'source-kb', 1,
        {'workspace_mode': 'isolated', 'can_edit': True}, 'Bearer synthetic')
    provider = PlatformStrategyProvider(scope, 'configured-model')
    def post(url, **kwargs):
        calls.append((url, kwargs))
        return httpx.Response(200, json={'success': True, 'data': {'model_id': 'configured-model',
            'content': __import__('json').dumps({'candidates': [plan.model_dump(mode='json')], 'gaps': []})}})
    async def resolve(*args): return scope
    monkeypatch.setattr('tin.llm.platform_provider.httpx.post', post)
    monkeypatch.setattr('tin.integration.workbench_scope.resolve_workbench', resolve)
    monkeypatch.setattr('tin.llm.platform_provider.settings.weknora_api_url', 'http://native.test')
    result = generate_candidates(session, 'SN', DAY, researcher_id=researcher, provider=provider)
    session.commit()
    assert result.status == 'generated' and len(result.strategies) == 1
    assert len(calls) == 1 and calls[0][0].endswith(f'/research/{WB}/strategy/generate')
    assert calls[0][1]['json']['model_id'] == 'configured-model'
    assert result.strategies[0].status == 'draft' and result.strategies[0].published_at is None
    assert result.strategies[0].plan['legs'][0]['quote_series_id'] == QUOTE


def test_missing_judgment_or_contract_source_does_not_call_any_model(scoped_strategy):
    session, researcher, row = scoped_strategy
    row.status = '草稿'; session.commit()
    class Never:
        name, model = 'fixture', 'fixture'
        def complete(self, request): raise AssertionError('Model must not be called without valid research')
    result = generate_candidates(session, 'SN', DAY, researcher_id=researcher, provider=Never())
    assert result.status == 'no_candidate' and any('研究判断' in gap for gap in result.gaps)
    assert session.scalar(select(Strategy)) is None


def test_fake_quote_binding_or_changed_values_never_pass_the_evidence_gate(scoped_strategy):
    session, researcher, _ = scoped_strategy
    context, _ = build_context(session, 'SN', DAY, researcher_id=researcher)
    original = candidate(context)
    forged = original.model_copy(update={'legs': [original.legs[0].model_copy(update={'quote_series_id': FUND})]})
    assert any('具体合约' in item for item in validate_evidence(forged, context))
    changed = original.model_copy(update={'evidence': [original.evidence[0].model_copy(update={'value': 9999}), *original.evidence[1:]]})
    assert any('数值' in item for item in validate_evidence(changed, context))


def test_http_strategy_selector_uses_configured_generic_models_and_passes_the_selected_id(canonical, monkeypatch):
    from tin.web import strategy as adapter
    from tin.llm import platform_provider
    client, headers, _, _ = canonical
    async def models(scope, path):
        assert path == '/api/v1/models'
        return {'data': [{'id': 'configured-chat', 'name': 'Configured generic', 'type': 'KnowledgeQA', 'status': 'active'},
            {'id': 'embedding', 'type': 'Embedding', 'status': 'active'},
            {'id': 'disabled-chat', 'type': 'KnowledgeQA', 'status': 'inactive'}]}
    monkeypatch.setattr(adapter, 'go_request', models)
    base = page(FIRST, '/api/sn/strategies')
    result = client.get(base + '/models', headers=headers)
    assert result.status_code == 200 and result.json()['models'] == [{'id': 'configured-chat', 'name': 'Configured generic'}]
    selected = []
    original = platform_provider.PlatformStrategyProvider
    class Captured(original):
        def __init__(self, scope, model_id):
            selected.append(model_id)
            super().__init__(scope, model_id)
    monkeypatch.setattr(platform_provider, 'PlatformStrategyProvider', Captured)
    missing = client.post(base + '/generate', headers=headers, json={})
    assert missing.status_code == 400 and selected == []
    generated = client.post(base + '/generate', headers=headers, json={'model_id': 'configured-chat'})
    assert generated.status_code == 200 and selected == ['configured-chat']
    assert generated.json()['status'] == 'no_candidate' and any('研究判断' in gap for gap in generated.json()['gaps'])


def test_scoped_strategy_inference_has_replaced_the_old_tenant_worker_block():
    from tin.integration.workbench_paths import source_preparation_required
    assert not source_preparation_required('/api/sn/strategies/generate')
    assert source_preparation_required('/api/sn/import/commit')
    assert source_preparation_required('/api/sn/refresh/run')


@pytest.mark.parametrize('with_center', [False, True])
def test_model_strategy_endpoint_still_rejects_restricted_source_context(canonical, monkeypatch, with_center):
    client, headers, state, _ = canonical
    state['source_delegated'] = True
    if with_center:
        from tests.test_knowledge_facts import OTHER_KB
        state['default_macro'] = {'tenant_id': 17, 'knowledge_base_id': OTHER_KB, 'kind': 'macro', 'name': 'Center'}
    from tin.llm import platform_provider
    def never(*args, **kwargs): raise AssertionError('Restricted source must not instantiate a model provider')
    monkeypatch.setattr(platform_provider, 'PlatformStrategyProvider', never)
    response = client.post(page(FIRST, '/api/sn/strategies/generate'), headers=headers, json={'model_id': 'configured-chat'})
    assert response.status_code == 403


def test_model_strategy_endpoint_requires_workbench_edit_permission(canonical, monkeypatch):
    client, headers, state, _ = canonical
    state['wb_edit'] = False
    from tin.llm import platform_provider
    def never(*args, **kwargs): raise AssertionError('A workbench viewer cannot generate model drafts')
    monkeypatch.setattr(platform_provider, 'PlatformStrategyProvider', never)
    response = client.post(page(FIRST, '/api/sn/strategies/generate'), headers=headers, json={'model_id': 'configured-chat'})
    assert response.status_code == 403
