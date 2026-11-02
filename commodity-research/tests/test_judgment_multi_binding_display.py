"""One research statement may show several independently scoped observations."""
from copy import deepcopy
from tests.test_judgment_research_view import facts, point, saved, rule, A, B, OTHER, DAY
from tests.test_judgment_research_template import render
from tests.test_knowledge_facts import canonical, FIRST, OTHER_KB, SERIES, page
from tests.test_judgment_charts import saved as save_chart_judgment, query
from tests.test_restricted_views import delegated
from tin.integration import page_facts
from tin.integration.workbench_scope import isolated_session_factory
from tin.models import Judgment
from tin.schemas.judgment import JudgmentIn
from tin.web import judgment_research as research


def test_one_evidence_keeps_multiple_metrics_and_reads_only_its_saved_ids(facts, monkeypatch):
    point(facts, A, 1, 11)
    point(facts, B, 1, 37)
    point(facts, OTHER, 1, 999)
    wanted = []
    actual = research.recent_points

    def observed(session, identifier, day, count):
        wanted.append((identifier, count))
        return actual(session, identifier, day, count)

    monkeypatch.setattr(research, 'recent_points', observed)
    row = saved(bull=[{'desc': 'Two independent observations support this claim',
        'series_id': A, 'related_series_ids': [B]}])
    view = research.judgment_research_view(facts, row, DAY)
    evidence = view['sides']['bull']['evidence']
    assert len(evidence) == 1 and evidence[0]['desc'] == 'Two independent observations support this claim'
    assert evidence[0]['metric']['series_id'] == A
    assert [metric['series_id'] for metric in evidence[0]['metrics']] == [A, B]
    assert [metric['latest'] for metric in evidence[0]['metrics']] == [11, 37]
    assert set(wanted) == {(A, 24), (B, 24)}
    assert set(view['metrics']) == {A, B}


def test_an_empty_primary_does_not_hide_an_explicit_related_observation(facts):
    point(facts, B, 1, 0)
    view = research.judgment_research_view(facts, saved(bull=[{
        'desc': 'An independently bound observation', 'series_id': None,
        'related_series_ids': [B]}]), DAY)
    item = view['sides']['bull']['evidence'][0]
    assert item['metric'] is None
    assert len(item['metrics']) == 1 and item['metrics'][0]['latest'] == 0
    assert view['counts']['bound'] == view['counts']['available'] == 1


def test_shared_comparison_includes_a_related_evidence_id_without_inventing_rule_semantics(facts):
    view = research.judgment_research_view(facts, saved(
        bull=[{'desc': 'Combined observation', 'series_id': A, 'related_series_ids': [B]}],
        bear=[{'desc': 'Opposing interpretation', 'series_id': B}]), DAY)
    assert len(view['shared']) == 1
    shared = view['shared'][0]
    assert shared['metric']['series_id'] == B
    assert shared['bull_texts'] == ['Combined observation']
    assert shared['bear_texts'] == ['Opposing interpretation']
    assert not {'operator', 'conjunction', 'strength'} & set(shared)


def test_related_rule_observations_do_not_acquire_its_threshold_or_trigger(facts):
    point(facts, A, 1, 5)
    point(facts, B, 1, 100)
    original = rule('primary-only', A, 'bull', 'value > 10')
    original['related_series_ids'] = [B]
    view = research.judgment_research_view(facts, saved(falsifiers=[original]), DAY)
    item = view['sides']['bull']['falsifiers'][0]
    assert item['state'] == '未触发'
    assert item['metric']['series_id'] == A
    assert [metric['series_id'] for metric in item['metrics']] == [A, B]
    assert view['metrics'][A]['boundaries'][0]['value'] == 10
    assert view['metrics'][B]['boundaries'] == []


def test_saved_manual_frequency_conflict_is_missing_data_instead_of_a_trigger(facts):
    point(facts, A, 1, 5)
    point(facts, A, 8, 7)
    original = rule('manual-period', A, 'bull', 'change > 0')
    original['caliber_confirmation'] = {'series_id': A, 'unit': 'kg', 'frequency': '周',
        'basis': 'Synthetic verified publication cadence', 'confirmed_by': 'synthetic',
        'confirmed_at': '2030-01-01T00:00:00Z', 'catalog_version': 'synthetic-version'}
    view = research.judgment_research_view(facts, saved(falsifiers=[original]), DAY)
    item = view['sides']['bull']['falsifiers'][0]
    assert item['state'] == '数据缺失' and item['current_value'] is None
    assert '统计频率' in item['gap_note']
    assert facts.get(research.Indicator, A).frequency == 'day'


def test_saved_manual_frequency_only_fills_this_rules_missing_source_frequency(facts):
    facts.get(research.Indicator, A).frequency = ''
    facts.commit()
    point(facts, A, 1, 5)
    point(facts, A, 8, 7)
    original = rule('manual-period', A, 'bull', 'change > 0')
    original['caliber_confirmation'] = {'series_id': A, 'unit': 'kg', 'frequency': '周',
        'basis': 'Synthetic verified publication cadence', 'confirmed_by': 'synthetic',
        'confirmed_at': '2030-01-01T00:00:00Z', 'catalog_version': 'synthetic-version'}
    view = research.judgment_research_view(facts, saved(falsifiers=[original]), DAY)
    assert view['sides']['bull']['falsifiers'][0]['state'] == '触发'
    assert facts.get(research.Indicator, A).frequency == ''


def test_context_and_pending_statements_keep_their_grouped_observations(facts):
    row = saved()
    row.marginal_focus = [{'desc': 'Combined next observation', 'series_id': A, 'related_series_ids': [B]}]
    row.cost_line = {'type': None, 'items': [{'desc': 'Two inputs', 'series_id': None,
        'related_series_ids': [B]}]}
    row.unstructured = [{'section': 'falsifiers', 'text': 'Original future condition',
        'reason': 'Synthetic import note', 'business_condition': 'Original future condition',
        'rule_kind': 'falsifier', 'series_id': A, 'related_series_ids': [B]}]
    view = research.judgment_research_view(facts, row, DAY)
    assert [metric['series_id'] for metric in view['marginal_focus'][0]['metrics']] == [A, B]
    assert [metric['series_id'] for metric in view['cost_items'][0]['metrics']] == [B]
    assert [metric['series_id'] for metric in view['pending_conditions'][0]['metrics']] == [A, B]
    assert view['counts']['falsifiers'] == 0


def test_saved_fact_windows_and_chart_authority_include_only_declared_related_ids():
    from tin.web.judgment_panels import judgment_series_ids
    row = saved(bull=[{'desc': 'Two observations', 'series_id': A, 'related_series_ids': [B]}])
    identifiers = judgment_series_ids(row)
    assert identifiers == [A, B] and OTHER not in identifiers
    windows = page_facts._judgment_windows(JudgmentIn.model_validate({
        key: value for key, value in vars(row).items() if key != 'id'}))
    assert windows == {A: 2, B: 2}


def test_saved_template_shows_two_charts_inside_one_evidence_card():
    metrics = [{'name': name, 'series_id': identifier, 'unit': unit, 'latest': value,
        'previous': None, 'change': None, 'change_pct': None,
        'point_count': 1, 'segments': ['0,24 240,24']} for name, identifier, unit, value in
        [('First observation', A, 'kg', 0), ('Second observation', B, '%', 71)]]
    html = render(sides={'bull': {'claim': 'Combined research', 'evidence': [{
        'desc': 'One grouped research statement', 'metric': metrics[0], 'metrics': metrics}],
        'falsifiers': []}, 'bear': {'claim': 'Alternative', 'evidence': [], 'falsifiers': []}},
        shared=[])
    assert html.count('data-research-item="evidence"') == 1
    assert html.count('data-metric-trend') == 2
    assert 'First observation' in html and 'Second observation' in html
    assert html.count('One grouped research statement') == 1


def test_related_chart_uses_its_verified_source_and_rejects_unbound_or_revoked_sources(canonical):
    client, headers, state, _ = canonical
    identifier = save_chart_judgment()
    state['default_macro'] = {'knowledge_base_id': OTHER_KB, 'tenant_id': 17, 'name': 'Synthetic macro'}
    macro = 'kb-' + 'c' * 48
    # A real fixture catalog establishes the additional source's origin.
    response = client.post(page(FIRST, '/api/sn/market/core/config'), headers=headers,
        json={'series_ids': [SERIES, macro]})
    assert response.status_code == 200, response.text
    with isolated_session_factory(17, FIRST)() as session:
        row = session.get(Judgment, identifier)
        contradiction = deepcopy(row.contradiction)
        contradiction['bull']['evidence'] = [{'desc': 'Same statement, two sources',
            'series_id': SERIES, 'related_series_ids': [macro]}]
        row.contradiction = contradiction
        session.commit()
    pages = state['pages'], state.get('macro_pages', 0)
    response = client.get(page(FIRST, '/api/sn/judgment/chart'), headers=headers,
        params=query(identifier, series_id=macro))
    assert response.status_code == 200, response.text
    assert response.json()['metric']['latest'] == state.get('macro_value', 41.25)
    assert state['bounded_requests'][-1]['series_ids'] == [macro]
    assert (state['pages'], state.get('macro_pages', 0)) == pages
    before = len(state['calls'])
    response = client.get(page(FIRST, '/api/sn/judgment/chart'), headers=headers,
        params=query(identifier, series_id='kb-' + 'b' * 48))
    assert response.status_code == 403
    assert not any(path == '/api/research/facts' for _, path in state['calls'][before:])
    delegated(state, ('judgments',))
    visitor = {**headers, 'Authorization': 'Bearer source-viewer'}
    parameters = query(identifier, series_id=macro)
    parameters.pop('date')
    response = client.get(page(FIRST, '/api/sn/judgment/chart'), headers=visitor, params=parameters)
    assert response.status_code == 200, response.text
    state['macro_revoked'] = True
    assert client.get(page(FIRST, '/api/sn/judgment/chart'), headers=visitor,
        params=parameters).status_code == 403
