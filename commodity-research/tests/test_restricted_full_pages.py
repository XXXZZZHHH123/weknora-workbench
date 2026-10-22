from datetime import date, datetime, timezone

from sqlalchemy import select

from tests.test_knowledge_facts import canonical, FIRST, SERIES, page
from tests.test_restricted_views import delegated, save_core
from tests.test_canonical_research_results import save_results


def test_authorized_judgment_displays_full_owned_logic_without_editor_or_other_users(canonical):
    client, headers, state, _ = canonical
    save_results(FIRST)
    save_results(FIRST, 'another-user')
    delegated(state, ('judgments',))
    visitor = {**headers, 'Authorization': 'Bearer source-viewer'}
    response = client.get(page(FIRST, '/sn/judgment'), headers=visitor)
    assert response.status_code == 200, response.text
    assert 'researcher original thesis' in response.text
    assert 'Human record' in response.text
    assert '主逻辑与证伪条件' in response.text and '版本记录' in response.text
    assert 'another-user' not in response.text
    assert 'data-open-logic' not in response.text and 'judgment-manager.js' not in response.text
    assert 'source_snapshots' not in response.text and not state.get('bounded_requests')


def test_authorized_strategy_and_report_show_saved_owned_body_without_hidden_payload(canonical):
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import Strategy
    client, headers, state, _ = canonical
    save_results(FIRST)
    save_results(FIRST, 'another-user')
    with isolated_session_factory(17, FIRST)() as session:
        row = session.scalar(select(Strategy).where(Strategy.author == 'researcher'))
        row.plan = {**row.plan, 'source_context': {'private': 'HIDDEN_RAW_SOURCE_SNAPSHOT'},
                    'entry_condition': 'OWN_ENTRY_CONDITION', 'invalidation': 'OWN_INVALIDATION'}
        session.commit()
    delegated(state, ('strategies', 'reports'))
    strategies = client.get(page(FIRST, '/sn/strategies'), headers=headers)
    assert strategies.status_code == 200, strategies.text
    assert 'Synthetic manual draft' in strategies.text and 'OWN_ENTRY_CONDITION' in strategies.text
    assert 'OWN_INVALIDATION' in strategies.text and 'HIDDEN_RAW_SOURCE_SNAPSHOT' not in strategies.text
    assert 'another-user' not in strategies.text and 'data-action=' not in strategies.text
    report = client.get(page(FIRST, '/sn/reports'), headers=headers)
    assert report.status_code == 200, report.text
    assert 'researcher original brief' in report.text and 'another-user' not in report.text
    assert 'source_snapshots' not in report.text and '下载 JSON' not in report.text
    assert not state.get('bounded_requests')


def test_authorized_market_reuses_full_core_card_visuals_but_never_loads_catalog(canonical):
    client, headers, state, _ = canonical
    save_core()
    state['bounded_previous'] = 15
    delegated(state, ('market',))
    response = client.get(page(FIRST, '/sn'), headers=headers)
    assert response.status_code == 200, response.text
    assert 'market-core-grid' in response.text and 'text-3xl' in response.text
    assert 'Measured quantity' in response.text and '较上期' in response.text
    assert 'market-core-open' not in response.text and 'market-core-catalog' not in response.text
    assert state['bounded_requests'][-1]['series_ids'] == [SERIES] and state['pages'] == 0


def test_restricted_market_empty_state_does_not_link_ungranted_sources_or_pages(canonical):
    client, headers, state, _ = canonical
    delegated(state, ('market',))
    response = client.get(page(FIRST, '/sn'), headers=headers)
    assert response.status_code == 200, response.text
    assert '/files' not in response.text
    assert 'research-open-sources' not in response.text
    assert '/sn/indicators' not in response.text
    assert not state.get('bounded_requests')


def test_market_permission_does_not_disclose_judgment_without_judgment_permission(canonical):
    client, headers, state, _ = canonical
    save_core()
    save_results(FIRST)
    delegated(state, ('market',))
    response = client.get(page(FIRST, '/sn'), headers=headers)
    assert response.status_code == 200, response.text
    assert 'researcher original thesis' not in response.text
    assert '查看完整研判' not in response.text


def test_legacy_restricted_personal_pages_explain_missing_workbench_ownership(canonical):
    client, headers, state, _ = canonical
    state['force_legacy'] = True
    delegated(state, ('judgments', 'strategies', 'reports'))
    for suffix in ('/sn/judgment', '/sn/strategies', '/sn/reports'):
        response = client.get(page(FIRST, suffix), headers=headers)
        assert response.status_code == 200, response.text
        assert '历史记录尚未归属到当前研究台' in response.text
        assert not state.get('bounded_requests')


def test_judgment_range_reads_only_its_saved_threshold_binding_and_same_range_arithmetic(canonical):
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import Judgment
    client, headers, state, _ = canonical
    save_results(FIRST)
    with isolated_session_factory(17, FIRST)() as session:
        row = session.scalar(select(Judgment))
        row.thresholds = [{'id': 'range', 'name': 'OWN_BOUND_RANGE', 'series_id': SERIES,
            'operator': 'between', 'value': [10, 20], 'type': '观察位', 'horizon': '短期'}]
        session.commit()
    delegated(state, ('judgments',))
    response = client.get(page(FIRST, '/sn/judgment'), headers=headers)
    assert response.status_code == 200, response.text
    assert 'OWN_BOUND_RANGE' in response.text and '已进入区间' in response.text
    assert state['bounded_requests'][-1]['view_module'] == 'judgments'
    assert state['bounded_requests'][-1]['series_ids'] == [SERIES]
    assert state['pages'] == 1  # original authorized save only; restricted does not project a catalog.


def test_report_renders_only_saved_claim_fields_and_never_serializes_hidden_capture(canonical):
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import DailyBrief
    client, headers, state, _ = canonical
    save_results(FIRST)
    with isolated_session_factory(17, FIRST)() as session:
        row = session.scalar(select(DailyBrief))
        row.claims = [{'claim': 'OWN_CLAIM_BODY', 'series_id': SERIES, 'value': 17.5,
            'as_of': '2026-01-02', 'source_snapshots': ['HIDDEN_CAPTURE']}]
        session.commit()
    delegated(state, ('reports',))
    response = client.get(page(FIRST, '/sn/reports'), headers=headers)
    assert response.status_code == 200, response.text
    assert 'OWN_CLAIM_BODY' in response.text and '17.5' in response.text
    assert 'HIDDEN_CAPTURE' not in response.text and 'source_snapshots' not in response.text
    assert not state.get('bounded_requests')


def test_missing_owner_never_falls_back_to_visitor_records(canonical):
    client, headers, state, _ = canonical
    save_results(FIRST)
    state['owner_missing'] = True
    delegated(state, ('judgments', 'strategies', 'reports'))
    for suffix in ('/sn/judgment', '/sn/strategies', '/sn/reports'):
        response = client.get(page(FIRST, suffix), headers=headers)
        assert response.status_code == 200, response.text
        assert '主人归属信息尚未同步' in response.text
        assert 'original thesis' not in response.text and 'original brief' not in response.text
    assert not state.get('bounded_requests')
