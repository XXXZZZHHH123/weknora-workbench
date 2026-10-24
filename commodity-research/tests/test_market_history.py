"""Historical market charts must remain explicit, finite, KB-only page reads."""
from dataclasses import replace
from datetime import date, datetime, timezone
import math
from pathlib import Path

from fastapi import HTTPException
import httpx
from jinja2 import Environment, FileSystemLoader, select_autoescape
import pytest
from sqlalchemy import event

from tin.config import SHANGHAI
from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope, current_workbench
from tin.models import Indicator, Observation, SourceMapping
from tin.web import market_history as history
from tests.test_knowledge_facts import canonical, FIRST, SERIES, page

WB = '10000000-0000-4000-8000-000000000071'
OTHER_WB = '10000000-0000-4000-8000-000000000072'
KB = '20000000-0000-4000-8000-000000000071'
OTHER_KB = '20000000-0000-4000-8000-000000000072'
A, B, HIDDEN = ['kb-' + letter * 48 for letter in 'abc']
DAY = date(2026, 1, 13)


@pytest.fixture
def prepared(session):
    identity = Identity('test-owner', 'owner@test', 'Test', 17, 'owner')
    scope = WorkbenchScope(identity, WB, KB, 17, {'can_read_source': True, 'can_edit': True})
    session.info.update(workbench_id=WB, knowledge_facts=True, knowledge_base_id=KB,
                        knowledge_base_tenant_id=17, weknora_user_id='test-owner',
                        knowledge_facts_catalog={identifier: {'knowledge_base_id': KB,
                            'knowledge_base_tenant_id': 17} for identifier in (A, B, HIDDEN)})
    for identifier, name, unit in [(A, 'A measured price', '元/吨'), (B, 'B quantity', '件'),
                                    (HIDDEN, 'UNSELECTED_SENTINEL', '美元')]:
        session.add(Indicator(series_id=identifier, name=name, variety='', category='知识库字段',
            caliber={'time_type': '原表日期'}, unit=unit, source='知识库结构化',
            frequency='日', fetch_mode='readonly', status='可用'))
    session.flush()
    for identifier, day, value, revision in [(A, 2, 8, 0), (A, 3, 10, 0), (A, 3, 12, 1),
                                             (A, 13, 0, 0), (A, 14, 900, 0),
                                             (B, 3, -1, 0), (HIDDEN, 3, 1234567, 0)]:
        stamp = datetime(2026, 1, day, tzinfo=SHANGHAI)
        session.add(Observation(series_id=identifier, value=value, as_of=stamp, fetched_at=stamp,
            caliber_snapshot={'time_type': '原表日期'}, source='知识库结构化', status='正常', revision=revision))
    session.commit()
    marker = current_workbench.set(scope)
    try:
        yield session, scope
    finally:
        current_workbench.reset(marker)


def test_core_card_consent_never_implicitly_publishes_history_and_empty_disables(prepared):
    session, scope = prepared
    assert history.history_config(session)['series_ids'] == []
    assert history.market_history(session, DAY)['series'] == []
    from tin.web.restricted_views import save_core_config
    save_core_config(session, [B, A])
    assert history.history_config(session) == {'series_ids': [], 'points_per_series': 180}
    assert history.market_history(session, DAY)['series'] == []
    history.save_history_config(session, [B, A])
    assert [row['series_id'] for row in history.market_history(session, DAY)['series']] == [B, A]
    history.save_history_config(session, [])
    assert history.market_history(session, DAY)['series'] == []


def test_explicit_bounded_history_original_dates_units_revisions_zero_and_real_spacing(prepared):
    session, scope = prepared
    history.save_history_config(session, [A, B], 3)
    reads = []
    def measured(connection, cursor, statement, parameters, context, executemany):
        if 'observations' in statement.lower() and statement.lstrip().lower().startswith('select'):
            reads.append((statement, parameters))
    event.listen(session.bind, 'before_cursor_execute', measured)
    try:
        result = history.market_history(session, DAY)
    finally:
        event.remove(session.bind, 'before_cursor_execute', measured)
    assert result['through'] == DAY.isoformat() and result['points_per_series'] == 3
    first, second = result['series']
    assert [point['value'] for point in first['points']] == [8, 12, 0]
    assert [point['date'] for point in first['points']] == ['2026-01-02', '2026-01-03', '2026-01-13']
    assert first['latest'] == 0 and first['unit'] == '元/吨'
    assert first['points'][1]['x'] - first['points'][0]['x'] < 50
    assert first['points'][2]['x'] - first['points'][1]['x'] > 380
    assert second['unit'] == '件' and second['count'] == 1 and second['points'][0]['value'] == -1
    assert HIDDEN not in str(result) and 'UNSELECTED_SENTINEL' not in str(result)
    assert len(reads) == 2 and all('limit' in sql.lower() for sql, _ in reads)
    assert all('observations.series_id = ?' in sql for sql, _ in reads)


def test_history_does_not_revive_invalid_newer_revision(prepared):
    session, _ = prepared
    history.save_history_config(session, [A], 3)
    stamp = datetime(2026, 1, 3, tzinfo=SHANGHAI)
    session.add(Observation(series_id=A, value=99, as_of=stamp, fetched_at=stamp,
        caliber_snapshot={}, source='知识库结构化', status='无效', revision=2))
    session.commit()
    result = history.market_history(session, DAY)
    assert [point['value'] for point in result['series'][0]['points']] == [8, 0]


@pytest.mark.parametrize('ids,points', [([A, A], 3), (['SHFE.SYN.0001.settle'], 3),
                                        (['kb-' + 'f' * 48], 3), ([A], 0), ([A], 181), ([A], True)])
def test_unverified_or_invalid_configuration_is_rejected(prepared, ids, points):
    session, _ = prepared
    with pytest.raises(HTTPException):
        history.save_history_config(session, ids, points)
    assert session.get(SourceMapping, history.HISTORY_KEY + ':' + WB) is None


@pytest.mark.parametrize('mode', ['restricted', 'readonly', 'old_database', 'wrong_workbench'])
def test_configuration_requires_current_source_readable_editor(prepared, mode):
    session, scope = prepared
    if mode == 'restricted':
        scope = replace(scope, metadata={**scope.metadata, 'can_read_source': False})
    elif mode == 'readonly':
        scope = replace(scope, metadata={**scope.metadata, 'can_edit': False})
    elif mode == 'old_database':
        session.info['knowledge_facts'] = False
    else:
        scope = replace(scope, workbench_id=OTHER_WB)
    marker = current_workbench.set(scope)
    try:
        with pytest.raises(HTTPException) as denied:
            history.save_history_config(session, [A])
        assert denied.value.status_code == 403
    finally:
        current_workbench.reset(marker)


def test_stale_or_cross_workbench_configuration_never_reads_a_foreign_source(prepared):
    session, scope = prepared
    history.save_history_config(session, [A, B])
    del session.info['knowledge_facts_catalog'][A]
    reads = []
    def measured(connection, cursor, statement, parameters, context, executemany):
        if 'observations' in statement.lower():
            reads.append(statement)
    event.listen(session.bind, 'before_cursor_execute', measured)
    try:
        result = history.market_history(session, DAY)
    finally:
        event.remove(session.bind, 'before_cursor_execute', measured)
    assert result['unavailable'] == [A] and result['notice']
    assert result['series'] == [] and result['series_ids'] == [A, B]
    assert reads == []  # Do not show even B as an unexplained partial selection.
    assert history.history_config(session)['series_ids'] == [A, B]
    session.info['workbench_id'] = OTHER_WB
    with pytest.raises(HTTPException) as denied:
        history.market_history(session, DAY)
    assert denied.value.status_code == 403


def bounded_response(body):
    return {'bounded': True, 'knowledge_base_id': KB, 'knowledge_base_tenant_id': 17,
        'version': 'd' * 64, 'offset': 0, 'next_offset': None, 'truncated': False, 'total': 3,
        'indicators': [{'series_id': A, 'metric': 'Saved A', 'unit': '元/吨'}],
        'rows': [{'series_id': A, 'date': f'2026-01-{day:02d}', 'value': value, 'value_status': 'valid'}
                 for day, value in [(2, 8), (3, 12), (13, 0)]]}


def test_restricted_history_only_published_ids_bounded_protocol_no_fact_session(prepared, monkeypatch):
    session, scope = prepared
    history.save_history_config(session, [A], 3)
    restricted = replace(scope, metadata={**scope.metadata, 'can_read_source': False}, view_modules=('market',))
    calls, sources = [], []
    def current_sources(checked):
        sources.append(checked)
        return [{'knowledge_id': 'saved-source', 'file_hash': 'f' * 32}]
    def fetch(method, path, body, **kwargs):
        calls.append((method, path, body))
        return httpx.Response(200, json=bounded_response(body), request=httpx.Request(method, 'http://local.test' + path))
    monkeypatch.setattr('tin.integration.kb_facts.current_sources', current_sources)
    monkeypatch.setattr('tin.provenance.bridge.structured_request', fetch)
    session.info['knowledge_facts'] = False  # restricted uses the artifact DB only
    result = history.readonly_market_history(session, restricted)
    assert result['series_ids'] == [A] and result['series'][0]['count'] == 3
    body = calls[0][2]
    assert body == {'sources': [{'knowledge_id': 'saved-source', 'file_hash': 'f' * 32}],
        'series_ids': [A], 'view_module': 'market', 'as_of': datetime.now(SHANGHAI).date().isoformat(),
        'points_per_series': 3}
    assert len(sources) == 1 and sources[0].knowledge_base_id == KB
    assert 'source_cell' not in str(result) and HIDDEN not in str(result)


@pytest.mark.parametrize('corrupt', ['other_id', 'other_kb', 'over_limit', 'duplicate', 'future', 'nonfinite', 'invalid_date'])
def test_restricted_overbroad_or_malformed_response_fails_closed(prepared, monkeypatch, corrupt):
    session, scope = prepared
    history.save_history_config(session, [A], 3)
    restricted = replace(scope, metadata={**scope.metadata, 'can_read_source': False}, view_modules=('market',))
    doc = bounded_response({})
    if corrupt == 'other_id':
        doc['rows'][0]['series_id'] = HIDDEN
    elif corrupt == 'other_kb':
        doc['knowledge_base_id'] = OTHER_KB
    elif corrupt == 'over_limit':
        doc['rows'].append({'series_id': A, 'date': '2026-01-04', 'value': 4, 'value_status': 'valid'})
        doc['total'] = 4
    elif corrupt == 'duplicate':
        doc['rows'][1]['date'] = doc['rows'][0]['date']
    elif corrupt == 'future':
        doc['rows'][0]['date'] = '9999-01-01'
    elif corrupt == 'nonfinite':
        doc['rows'][0]['value'] = math.inf
    else:
        doc['rows'][0]['date'] = '20260102'
    monkeypatch.setattr('tin.integration.kb_facts.current_sources', lambda scope: [])
    # Response.json is stubbed for non-finite JSON since real JSON encoding rejects it.
    class Response:
        status_code = 200
        def raise_for_status(self): pass
        def json(self): return doc
    monkeypatch.setattr('tin.provenance.bridge.structured_request', lambda *args, **kwargs: Response())
    with pytest.raises(HTTPException) as denied:
        history.readonly_market_history(session, restricted)
    assert denied.value.status_code == 503


def test_restricted_missing_origin_or_page_authorization_does_not_probe_sources(prepared, monkeypatch):
    session, scope = prepared
    history.save_history_config(session, [A], 3)
    restricted = replace(scope, metadata={**scope.metadata, 'can_read_source': False}, view_modules=('market',))
    from tin.web.restricted_views import ORIGINS_KEY
    row = session.get(SourceMapping, ORIGINS_KEY + ':' + WB)
    row.config = {'origins': {A: {'knowledge_base_id': OTHER_KB, 'tenant_id': 17}}}
    session.commit()
    monkeypatch.setattr('tin.integration.kb_facts.current_sources', lambda scope: pytest.fail('must not probe unbound sources'))
    with pytest.raises(HTTPException) as denied:
        history.readonly_market_history(session, restricted)
    assert denied.value.status_code == 403
    with pytest.raises(HTTPException) as denied:
        history.readonly_market_history(session, replace(restricted, view_modules=('diagram',)))
    assert denied.value.status_code == 403


def test_chart_geometry_extreme_finite_values_and_template_escaping(prepared):
    session, _ = prepared
    chart = history._chart(A, '<script>unsafe()</script>', '未知<script>',
        [{'date': '2026-01-02', 'value': -1e308}, {'date': '2026-01-03', 'value': 1e308}])
    assert all(math.isfinite(point[axis]) for point in chart['points'] for axis in ('x', 'y'))
    templates = Path(history.__file__).parent / 'templates'
    env = Environment(loader=FileSystemLoader(templates), autoescape=select_autoescape(['html']))
    env.filters['num'] = lambda value, unit: str(value)
    data = {'series': [chart], 'points_per_series': 180, 'through': DAY.isoformat()}
    for restricted in (True, False):
        html = env.get_template('_market_history.html').render(market_history=data,
            restricted_view=restricted, market_catalog={'can_edit_shared': True})
        assert '<script>unsafe' not in html and '&lt;script&gt;' in html
        assert ('id="market-history-open"' in html) is not restricted
        assert '历史行情' in html and '合约期限结构' not in html


def test_history_configuration_and_page_use_the_verified_kb_integration(canonical):
    client, headers, state, _ = canonical
    target = page(FIRST, '/api/sn/market/history/config')
    saved = client.post(target, headers=headers, json={'series_ids': [SERIES], 'points_per_series': 60})
    assert saved.status_code == 200, saved.text
    assert saved.json() == {'series_ids': [SERIES], 'points_per_series': 60}
    response = client.get(page(FIRST, '/sn'), headers=headers)
    assert response.status_code == 200, response.text
    assert 'id="market-history"' in response.text and 'Measured quantity' in response.text
    assert 'data-history-series="' + SERIES + '"' in response.text
    assert 'id="market-history-open"' in response.text
    from lxml.html import fromstring
    assert '合约期限结构' not in fromstring(response.text).xpath('//h2/text()')
    state['revoked'] = True
    assert client.get(page(FIRST, '/sn'), headers=headers).status_code == 403


def test_stale_history_keeps_market_cards_and_reselection_without_old_values(canonical):
    from lxml.html import fromstring
    from tin.integration.workbench_scope import isolated_session_factory

    client, headers, state, _ = canonical
    core = client.post(page(FIRST, '/api/sn/market/core/config'), headers=headers,
                       json={'series_ids': [SERIES]})
    assert core.status_code == 200, core.text
    missing = 'kb-' + 'f' * 48
    # Simulate a formerly valid saved field whose source was later withdrawn.
    with isolated_session_factory(17, FIRST)() as session:
        session.add(SourceMapping(id=history.HISTORY_KEY + ':' + FIRST,
            config={'series_ids': [SERIES, missing], 'points_per_series': 60},
            confirmed_by='researcher', confirmed_at=datetime.now(timezone.utc)))
        session.commit()
    response = client.get(page(FIRST, '/sn'), headers=headers)
    assert response.status_code == 200, response.text
    document = fromstring(response.text)
    assert document.xpath('//*[@id="market-history-open"]')
    assert '重新选择行情' in document.xpath('//*[@id="market-history"]')[0].text_content()
    assert document.xpath('//*[@id="market-core-ready"]')[0].text == '1'
    assert 'Measured quantity' in response.text
    assert not document.xpath('//*[@data-history-series]')
    with isolated_session_factory(17, FIRST)() as session:
        assert session.get(SourceMapping, history.HISTORY_KEY + ':' + FIRST).config['series_ids'] == [SERIES, missing]
    state['revoked'] = True
    assert client.get(page(FIRST, '/sn'), headers=headers).status_code == 403


def test_history_restricted_page_uses_only_saved_ids_and_cannot_edit_or_change_range(canonical):
    client, headers, state, _ = canonical
    target = page(FIRST, '/api/sn/market/history/config')
    saved = client.post(target, headers=headers, json={'series_ids': [SERIES], 'points_per_series': 60})
    assert saved.status_code == 200, saved.text
    state.update(source_delegated=True, view_modules=['market'])
    response = client.get(page(FIRST, '/sn'), headers=headers)
    assert response.status_code == 200, response.text
    assert 'id="market-history"' in response.text and 'id="market-history-open"' not in response.text
    assert state['bounded_requests'][-1]['series_ids'] == [SERIES]
    assert state['bounded_requests'][-1]['points_per_series'] == 60
    for suffix in ['/sn?date=2001-01-01', '/sn?series_id=hidden', '/sn?points_per_series=180']:
        assert client.get(page(FIRST, suffix), headers=headers).status_code in {403, 422}
    assert client.post(target, headers=headers,
        json={'series_ids': [SERIES], 'points_per_series': 180}).status_code == 403
    state['source_consent_revoked'] = True
    assert client.get(page(FIRST, '/sn'), headers=headers).status_code == 403
