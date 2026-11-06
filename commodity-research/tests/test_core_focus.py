"""Shared mixed core-focus configuration, with isolated real SQL stores."""
from contextlib import contextmanager
from dataclasses import replace
from datetime import date, datetime, timezone
from types import SimpleNamespace
from urllib.parse import parse_qs, urlsplit

from fastapi import HTTPException, Request
from fastapi.encoders import jsonable_encoder
from lxml.html import fromstring
from pydantic import ValidationError
import pytest

from tin.config import SHANGHAI
from tin.export.board import Card
from tin.ingest.record import record
from tin.integration.identity import current_identity
from tin.integration.page_facts import _page
from tin.integration.workbench_scope import WorkbenchScope, current_workbench
from tin.models import Indicator, SourceMapping
from tin.schemas.caliber import Caliber
from tin.schemas.chart import ChartDefinition
from tin.schemas.observation import ObservationIn
from tin.web import app as web
from tin.web import charts, market, restricted_views as restricted
from tests.chart_fixtures import FIELDS, SID, OTHER, THIRD, definition
from tests.test_workbench_sources_v2 import IDENTITY, WB, source

DAY = date(2026, 9, 18)
SECOND_WB = '10000000-0000-4000-8000-000000000092'


def indicator(identifier=SID):
    return {'kind': 'indicator', 'id': identifier}


def chart(identifier):
    return {'kind': 'chart', 'id': identifier}


class DirectFocusClient:
    """Exercise router handlers without a TestClient socket or live transport."""
    def __init__(self, state):
        self.state = state

    def request(self, method, url, *, json=None):
        parsed = urlsplit(url)
        params = parse_qs(parsed.query, keep_blank_values=True)
        request = Request({'type': 'http', 'method': method, 'scheme': 'http', 'path': parsed.path,
                           'query_string': parsed.query.encode(), 'headers': [(b'host', b'fixture.invalid')],
                           'server': ('fixture.invalid', 80)})
        route = next(route for route in self.state.router.routes
                     if route.path == parsed.path and method in route.methods)
        try:
            if method == 'POST':
                value = route.endpoint(market.CoreConfig.model_validate(json))
            else:
                value = route.endpoint(request, series_id=params.get('series_id'), item=params.get('item'),
                                       date=params.get('date', [None])[0])
            status = 200
        except HTTPException as error:
            status, value = error.status_code, {'detail': error.detail}
        except ValidationError as error:
            status, value = 422, {'detail': str(error)}
        value = jsonable_encoder(value)
        return SimpleNamespace(status_code=status, json=lambda: value, text=str(value))

    def post(self, body):
        return self.request('POST', '/api/sn/market/core/config', json=body)

    def get(self, query):
        return self.request('GET', '/api/sn/market/core?' + query)


@pytest.fixture
def focus(session):
    scope = WorkbenchScope(IDENTITY, WB, '', 0,
                          {'workspace_mode': 'isolated', 'can_edit': True, 'owner_id': IDENTITY.user_id},
                          'Bearer fixture', scope_version=2, sources_revision=7,
                          sources=(source(1),), view_modules=('market', 'indicators'))
    session.info.update(workbench_id=WB, weknora_user_id=IDENTITY.user_id,
                        knowledge_facts_catalog={identifier: {**field, 'name': field['metric']}
                                                 for identifier, field in FIELDS.items()})
    for identifier, field in FIELDS.items():
        session.add(Indicator(series_id=identifier, name=field['metric'], variety='SN', category='隔离测试',
                              caliber={'time_type': '发布时点'}, unit=field['unit'], source='Fixture',
                              frequency='日', fetch_mode='manual', status='可用'))
    session.flush()
    for index, identifier in enumerate(FIELDS):
        record(session, ObservationIn(series_id=identifier, value=10 + index,
               as_of=datetime(2026, 9, 18, 15, tzinfo=SHANGHAI), caliber=Caliber(time_type='发布时点'),
               source='Fixture', entered_by='fixture', note='Isolated core-focus test'))
    session.commit()
    session.info['knowledge_facts'] = True
    calls = []

    @contextmanager
    def page_session():
        calls.append(dict(_page.get() or {}))
        yield session

    state = SimpleNamespace(session=session, scope=scope, calls=calls)
    state.router = market.build_market_router(page_session, web.templates, lambda _session, _date: DAY)
    state.client = DirectFocusClient(state)
    identity_token = current_identity.set(scope.identity)
    scope_token = current_workbench.set(scope)
    try:
        yield state
    finally:
        current_workbench.reset(scope_token)
        current_identity.reset(identity_token)


def add_chart(focus, *, workbench=WB, deleted=False):
    token = current_workbench.set(replace(focus.scope, workbench_id=workbench))
    previous = focus.session.info['workbench_id']
    focus.session.info['workbench_id'] = workbench
    try:
        row = charts.create_row(focus.session, current_workbench.get(),
                                ChartDefinition.model_validate(definition(title='任意图表 <安全>')))
        if deleted:
            row.status = 'deleted'
        focus.session.commit()
        return row.id
    finally:
        focus.session.info['workbench_id'] = previous
        current_workbench.reset(token)


def stored_core(focus):
    return focus.session.get(SourceMapping, restricted.mapping_key(focus.session, restricted.CORE_KEY))


def test_old_series_ids_read_in_order_and_next_write_uses_items(focus):
    row = SourceMapping(id=restricted.mapping_key(focus.session, restricted.CORE_KEY),
                        config={'series_ids': [OTHER, SID]}, confirmed_by='fixture',
                        confirmed_at=datetime.now(timezone.utc))
    focus.session.add(row)
    focus.session.commit()
    assert restricted.core_items(focus.session) == [indicator(OTHER), indicator()]
    assert restricted.core_config(focus.session) == [OTHER, SID]
    assert [card.series_id for card in market.saved_core_cards(focus.session, DAY, [])] == [OTHER, SID]
    response = focus.client.post({'series_ids': [SID, OTHER]})
    assert response.status_code == 200, response.text
    assert stored_core(focus).config == {'items': [indicator(), indicator(OTHER)]}
    assert response.json() == {'items': [indicator(), indicator(OTHER)], 'series_ids': [SID, OTHER]}


def test_mixed_items_preserve_order_and_chart_cards_fetch_values_lazily(focus):
    identifier = add_chart(focus)
    items = [indicator(OTHER), chart(identifier), indicator()]
    response = focus.client.post({'items': items})
    assert response.status_code == 200, response.text
    assert stored_core(focus).config == {'items': items}
    assert restricted.core_config(focus.session) == [OTHER, SID]
    assert focus.calls[-1]['series_ids'] == [OTHER, SID]
    cards = market.saved_core_cards(focus.session, DAY, [])
    assert [value['id'] if isinstance(value, dict) else value.series_id for value in cards] == [OTHER, identifier, SID]
    assert cards[1] == {'kind': 'chart', 'id': identifier, 'title': '任意图表 <安全>', 'status': 'pending'}
    assert cards[0].value == 11 and cards[2].value == 10
    html = web.templates.get_template('_market_cards.html').render(cards=cards)
    document = fromstring('<main>' + html + '</main>')
    nodes = document.xpath('//*[@data-market-card-series or @data-market-card-chart]')
    assert [node.get('data-market-card-series') or node.get('data-market-card-chart') for node in nodes] == [OTHER, identifier, SID]
    assert document.xpath('//*[@data-market-card-chart]//*[@data-focus-chart-plot]')
    assert '任意图表 <安全>' in document.text_content()
    assert not document.xpath('//安全'), 'Chart titles must be escaped by the actual template'


def test_core_get_returns_exact_mixed_selection_html_and_only_reads_indicator_projection(focus):
    identifier = add_chart(focus)
    response = focus.client.get(f'item=chart:{identifier}&item=indicator:{SID}&date=2026-09-18')
    assert response.status_code == 200, response.text
    payload = response.json()
    assert payload['items'] == [chart(identifier), indicator()]
    assert payload['series_ids'] == [SID] and payload['ready_count'] == 1
    assert focus.calls[-1]['module'] == 'market' and focus.calls[-1]['series_ids'] == [SID]
    document = fromstring('<main>' + payload['html'] + '</main>')
    assert document.xpath('//*[@data-market-card-chart]/@data-market-card-chart') == [identifier]
    assert document.xpath('//*[@data-market-card-series]/@data-market-card-series') == [SID]
    legacy = focus.client.get(f'series_id={OTHER}&series_id={SID}')
    assert legacy.status_code == 200 and legacy.json()['series_ids'] == [OTHER, SID]


@pytest.mark.parametrize('cutoff', ['', '&date=2026-09-18'])
def test_chart_only_get_does_not_require_indicator_observations_or_resolve_their_date(focus, cutoff):
    identifier = add_chart(focus)

    @contextmanager
    def page_session():
        focus.calls.append(dict(_page.get() or {}))
        yield focus.session

    def no_indicator_date(*args):
        pytest.fail('Chart-only requests must leave dates to the lazy chart-data request')

    focus.router = market.build_market_router(page_session, web.templates, no_indicator_date)
    response = focus.client.get('item=chart:' + identifier + cutoff)
    assert response.status_code == 200, response.text
    assert response.json()['items'] == [chart(identifier)]
    assert response.json()['series_ids'] == [] and response.json()['ready_count'] == 0
    assert focus.calls[-1]['series_ids'] == []
    document = fromstring(response.json()['html'])
    assert document.get('data-market-card-chart') == identifier


def test_config_accepts_exactly_16_and_never_silently_truncates(focus):
    identifiers = ['kb-' + f'{index:048x}' for index in range(17)]
    focus.session.info['knowledge_facts_catalog'].update({identifier: {} for identifier in identifiers})
    items = [indicator(identifier) for identifier in identifiers]
    assert focus.client.post({'items': items[:16]}).status_code == 200
    assert len(restricted.core_items(focus.session)) == 16
    assert focus.client.post({'items': items}).status_code == 422
    assert restricted.core_items(focus.session) == items[:16]


@pytest.mark.parametrize('body,status', [
    ({'items': [indicator(), indicator()]}, 409),
    ({'items': [{'kind': 'unknown', 'id': SID}]}, 422),
    ({'items': [{'kind': 'indicator', 'id': '../invalid'}]}, 409),
    ({'items': [{'kind': 'chart', 'id': ''}]}, 422),
    ({'items': [indicator()], 'series_ids': [SID]}, 422),
    ({}, 422),
    ({'items': [{'kind': 'indicator', 'id': SID, 'title': 'untrusted'}]}, 422),
])
def test_duplicate_and_invalid_configuration_rejected_without_save(focus, body, status):
    assert focus.client.post(body).status_code == status
    assert stored_core(focus) is None


def test_indicator_must_belong_to_current_catalog_even_if_sql_row_exists(focus):
    focus.session.info['knowledge_facts_catalog'].pop(SID)
    assert focus.session.get(Indicator, SID) is not None
    response = focus.client.post({'items': [indicator()]})
    assert response.status_code == 404 and '当前知识库' in response.json()['detail']
    assert stored_core(focus) is None


@pytest.mark.parametrize('problem', ['missing', 'other_workbench', 'deleted'])
def test_chart_must_be_live_in_this_workbench_for_save_and_get(focus, problem):
    identifier = ('missing-chart' if problem == 'missing' else
                  add_chart(focus, workbench=SECOND_WB if problem == 'other_workbench' else WB,
                            deleted=problem == 'deleted'))
    assert focus.client.post({'items': [chart(identifier)]}).status_code == 404
    assert focus.client.get('item=chart:' + identifier).status_code == 404
    assert stored_core(focus) is None


@pytest.mark.parametrize('query,status', [
    ('item=indicator:' + THIRD + '&item=indicator:' + THIRD, 422),
    ('item=unknown:' + SID, 422), ('item=invalid', 422),
    ('series_id=' + SID + '&item=indicator:' + OTHER, 422),
    ('item=indicator:' + SID + '&date=invalid', 400),
    ('item=indicator:' + SID + '&unexpected=1', 422),
])
def test_core_get_rejects_ambiguous_or_invalid_requests_before_projection(focus, query, status):
    assert focus.client.get(query).status_code == status
    assert not focus.calls


def test_unconfigured_focus_is_empty_instead_of_automatic_default_cards(focus):
    fallback = [Card(key='fallback', label='Fallback', unit='件', status='ok', series_id=SID, value=42)]
    assert restricted.core_items(focus.session) == []
    assert market.saved_core_cards(focus.session, DAY, fallback) == []
    assert focus.client.post({'items': []}).status_code == 200
    assert stored_core(focus).config == {'items': []}
    assert market.saved_core_cards(focus.session, DAY, fallback) == []


def test_restricted_projection_only_requests_bound_indicators_and_explains_hidden_charts(focus, monkeypatch):
    identifier = add_chart(focus)
    assert focus.client.post({'items': [chart(identifier), indicator(), indicator(OTHER)]}).status_code == 200
    assert restricted.bound_series_ids(focus.session, 'market') == [SID, OTHER]
    assert restricted.bound_series_ids(focus.session, 'indicators') == [SID, OTHER]
    private_source = source(1, delegated=True)
    private_scope = replace(focus.scope, sources=(private_source,), view_modules=('market', 'indicators'))
    calls = []

    @contextmanager
    def artifacts(_scope):
        yield focus.session

    def values(_session, _scope, identifiers, module):
        calls.append((identifiers, module))
        # The actual necessary-values adapter returns only fields granted by the
        # freshly verified source, leaving another saved binding undisclosed.
        return {SID: {'name': '已授权字段', 'unit': '件', 'value': 8, 'date': '2026-09-18',
                      'change': None, 'change_pct': None}}

    monkeypatch.setattr(restricted, 'artifact_session', artifacts)
    monkeypatch.setattr(restricted, 'necessary_values', values)
    monkeypatch.setattr('tin.web.market_history.readonly_market_history', lambda *args: {})
    monkeypatch.setattr('tin.web.market_contracts.readonly_contract_curve', lambda *args: {})
    token = current_workbench.set(private_scope)
    try:
        payload = restricted.page_data(private_scope, 'market')
        assert calls == [([SID, OTHER], 'market')]
        assert payload['core_charts_hidden'] is True
        assert [card.series_id for card in payload['market_cards']] == [SID]
        html = web.templates.get_template('_market_core.html').render(
            cards=payload['market_cards'], restricted_view=True, restricted_data=payload)
    finally:
        current_workbench.reset(token)
    document = fromstring(html)
    assert '图表条目暂不对受限视角开放' in document.text_content()
    assert document.xpath('//*[@data-market-card-series]/@data-market-card-series') == [SID]
    assert not document.xpath('//*[@data-market-card-chart]')
    assert not document.xpath('//*[@id="market-core-open" or @id="market-core-suggestions-open"]')
    assert '核心关注' in document.get_element_by_id('market-core-title').text_content()
