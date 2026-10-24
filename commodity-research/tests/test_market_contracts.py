"""Contract curves use explicit KB bindings and comparable same-day values."""
from copy import deepcopy
from dataclasses import replace
from datetime import date, datetime
import math

import httpx
from fastapi import HTTPException
import pytest
from sqlalchemy import delete, event

from tin.config import SHANGHAI
from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope, current_workbench
from tin.models import Indicator, Observation, SourceMapping
from tin.web import market_contracts as market

WB = '10000000-0000-4000-8000-000000000081'
KB = '20000000-0000-4000-8000-000000000081'
OTHER_KB = '20000000-0000-4000-8000-000000000082'
DAY = date(2026, 1, 13)
IDS = ['kb-' + format(number, '048x') for number in range(1, 10)]


def config():
    return [{'contract': 'SYN-A', 'delivery_month': '2026-03', 'settle': IDS[0], 'close': IDS[1],
             'volume': IDS[2], 'oi': IDS[3], 'price_unit': '元/吨', 'volume_unit': '手', 'oi_unit': '手', 'main': True},
            {'contract': 'SYN-B', 'delivery_month': '2026-04', 'settle': IDS[4], 'close': IDS[5],
             'volume': IDS[6], 'oi': IDS[7], 'price_unit': '元/吨', 'volume_unit': '手', 'oi_unit': '手', 'main': False}]


@pytest.fixture
def prepared(session):
    scope = WorkbenchScope(Identity('owner', 'owner@test', 'Owner', 17, 'owner'), WB, KB, 17,
                           {'can_read_source': True, 'can_edit': True})
    catalog = {}
    units = ['元/吨', '元/吨', '手', '手'] * 2 + ['美元']
    session.info.update(workbench_id=WB, knowledge_facts=True, knowledge_base_id=KB,
                        knowledge_base_tenant_id=17, weknora_user_id='owner', knowledge_facts_catalog=catalog)
    for index, identifier in enumerate(IDS):
        unit = units[index]
        catalog[identifier] = {'knowledge_base_id': KB, 'knowledge_base_tenant_id': 17, 'unit': unit}
        session.add(Indicator(series_id=identifier, name='Explicit field ' + str(index), variety='',
            category='知识库字段', caliber={'time_type': '原表日期'}, unit=unit,
            source='知识库结构化', frequency='日', fetch_mode='readonly', status='可用'))
    session.flush()
    values = [100, 101, 600, 900, 110, 111, 700, 901, 999999]
    for index, identifier in enumerate(IDS):
        for day in (12, 13):
            stamp = datetime(2026, 1, day, tzinfo=SHANGHAI)
            session.add(Observation(series_id=identifier, value=values[index] - (13 - day),
                as_of=stamp, fetched_at=stamp, caliber_snapshot={}, source='知识库结构化', status='正常'))
    stamp = datetime(2026, 1, 14, tzinfo=SHANGHAI)
    session.add(Observation(series_id=IDS[4], value=120, as_of=stamp, fetched_at=stamp,
        caliber_snapshot={}, source='知识库结构化', status='正常'))
    session.commit()
    marker = current_workbench.set(scope)
    try:
        yield session, scope
    finally:
        current_workbench.reset(marker)


def test_saved_specific_contracts_sort_by_real_delivery_month_and_only_exact_current_kb_ids(prepared):
    session, _ = prepared
    result = market.save_contract_config(session, list(reversed(config())))
    assert [row['contract'] for row in result['contracts']] == ['SYN-A', 'SYN-B']
    reads = []
    def measured(connection, cursor, statement, parameters, context, executemany):
        if statement.lstrip().lower().startswith('select') and 'observations' in statement.lower():
            reads.append(statement)
    event.listen(session.bind, 'before_cursor_execute', measured)
    try:
        curve = market.configured_contract_curve(session, DAY)
    finally:
        event.remove(session.bind, 'before_cursor_execute', measured)
    assert curve['source'] == 'knowledge-base' and curve['date'] == DAY.isoformat()
    assert curve['unit'] == '元/吨' and curve['volume_unit'] == '手'
    assert [row['settle'] for row in curve['rows']] == [100, 110]
    assert curve['shape'] == 'Contango' and curve['min'] == 100 and curve['max'] == 110 and curve['mid'] == 105
    assert [row['month'] for row in curve['rows']] == ['2026-03', '2026-04']
    assert curve['rows'][0]['main'] and not curve['rows'][1]['main']
    assert all(row['active'] for row in curve['rows'])
    assert len(reads) == 8 and all('limit' in sql.lower() and 'observations.series_id = ?' in sql for sql in reads)
    assert IDS[8] not in str(curve) and '999999' not in str(curve)


def test_initial_date_is_latest_true_common_date_not_latest_unrelated_indicator(prepared):
    session, _ = prepared
    market.save_contract_config(session, config())
    assert market.configured_contract_curve(session)['date'] == '2026-01-13'
    assert market.configured_contract_curve(session, date(2026, 1, 14))['rows'][0]['contract'] == 'SYN-B'
    assert len(market.configured_contract_curve(session, date(2026, 1, 14))['rows']) == 1
    assert market.configured_contract_curve(session, date(2026, 1, 15))['rows'] == []


def test_future_period_end_does_not_select_a_future_workbench_default(prepared, monkeypatch):
    from tin.compute import engine
    session, _ = prepared

    class Clock(datetime):
        @classmethod
        def now(cls, tz=None):
            return cls(2026, 1, 13, 12, tzinfo=SHANGHAI).astimezone(tz or SHANGHAI)

    monkeypatch.setattr(engine, 'datetime', Clock)
    future = datetime(2026, 12, 31, tzinfo=SHANGHAI)
    session.add(Observation(series_id=IDS[-1], value=123, as_of=future, fetched_at=future,
        caliber_snapshot={'time_type': '原表日期'}, source='知识库结构化', status='正常'))
    session.commit()
    assert engine.latest_trade_date(session) == DAY
    # The original future period remains stored; only the default cutoff changes.
    assert session.query(Observation).filter(Observation.as_of == future).count() == 1


def test_no_common_date_uses_latest_real_day_with_missing_contract_left_missing(prepared):
    session, _ = prepared
    market.save_contract_config(session, config())
    session.execute(delete(Observation).where(Observation.series_id == IDS[4],
        Observation.as_of < datetime(2026, 1, 14, tzinfo=SHANGHAI)))
    session.commit()
    curve = market.configured_contract_curve(session)
    assert curve['date'] == '2026-01-14' and len(curve['rows']) == 1
    assert curve['rows'][0]['contract'] == 'SYN-B' and curve['rows'][0]['close'] is None
    assert curve['notices'] and curve['shape'] is None


def test_missing_same_day_volume_close_and_oi_are_not_forward_filled_or_active(prepared):
    session, _ = prepared
    market.save_contract_config(session, config())
    session.execute(delete(Observation).where(Observation.series_id.in_(IDS[1:4]),
        Observation.as_of == datetime(2026, 1, 13, tzinfo=SHANGHAI)))
    session.commit()
    curve = market.configured_contract_curve(session, DAY)
    assert curve['rows'][0]['close'] is None and curve['rows'][0]['volume'] is None and curve['rows'][0]['oi'] is None
    assert not curve['rows'][0]['active'] and curve['shape'] is None


def test_quantity_unit_is_explicit_never_tonnes_as_lots_and_unit_changes_fail_closed(prepared):
    session, _ = prepared
    contracts = config()
    contracts[0]['volume_unit'] = '吨'
    session.info['knowledge_facts_catalog'][IDS[2]]['unit'] = '吨'
    market.save_contract_config(session, contracts)
    curve = market.configured_contract_curve(session, DAY)
    assert curve['rows'][0]['volume'] == 600 and not curve['rows'][0]['active']
    assert curve['volume_unit'] is None and curve['notices']
    session.info['knowledge_facts_catalog'][IDS[2]]['unit'] = '手'
    with pytest.raises(HTTPException) as denied:
        market.configured_contract_curve(session, DAY)
    assert denied.value.status_code == 409


def test_optional_fields_and_units_can_be_absent_but_prices_must_be_explicit(prepared):
    session, _ = prepared
    contracts = config()[:1]
    contracts[0].update(close=None, volume=None, oi=None, volume_unit=None, oi_unit=None)
    market.save_contract_config(session, contracts)
    curve = market.configured_contract_curve(session, DAY)
    assert len(curve['rows']) == 1 and not curve['rows'][0]['active']
    assert curve['volume_unit'] is None and curve['oi_unit'] is None and curve['rows'][0]['x'] == 200


@pytest.mark.parametrize('invalid', ['unknown', 'legacy', 'wrong_kb', 'unit', 'mixed_price', 'month',
                                     'duplicate_contract', 'duplicate_month', 'duplicate_settle', 'main', 'many', 'extra'])
def test_invalid_or_unverifiable_bindings_are_rejected_before_persistence(prepared, invalid):
    session, _ = prepared
    contracts = config()
    if invalid == 'unknown': contracts[0]['settle'] = 'kb-' + 'f' * 48
    elif invalid == 'legacy': contracts[0]['settle'] = 'SHFE.SYN.2603.settle'
    elif invalid == 'wrong_kb': session.info['knowledge_facts_catalog'][IDS[0]]['knowledge_base_id'] = OTHER_KB
    elif invalid == 'unit': contracts[0]['price_unit'] = '美元/吨'
    elif invalid == 'mixed_price': contracts[1]['price_unit'] = '美元/吨'
    elif invalid == 'month': contracts[0]['delivery_month'] = '26-03'
    elif invalid == 'duplicate_contract': contracts[1]['contract'] = contracts[0]['contract']
    elif invalid == 'duplicate_month': contracts[1]['delivery_month'] = contracts[0]['delivery_month']
    elif invalid == 'duplicate_settle': contracts[1]['settle'] = contracts[0]['settle']
    elif invalid == 'main': contracts[1]['main'] = True
    elif invalid == 'many': contracts *= 13
    else: contracts[0]['knowledge_base_id'] = OTHER_KB
    with pytest.raises(HTTPException):
        market.save_contract_config(session, contracts)
    assert session.get(SourceMapping, market.CONTRACTS_KEY + ':' + WB) is None


@pytest.mark.parametrize('mode', ['readonly', 'restricted', 'old_facts', 'other_workbench'])
def test_save_requires_source_readable_workbench_editor(prepared, mode):
    session, scope = prepared
    if mode == 'readonly': scope = replace(scope, metadata={**scope.metadata, 'can_edit': False})
    elif mode == 'restricted': scope = replace(scope, metadata={**scope.metadata, 'can_read_source': False})
    elif mode == 'old_facts': session.info['knowledge_facts'] = False
    else: scope = replace(scope, workbench_id='10000000-0000-4000-8000-000000000082')
    marker = current_workbench.set(scope)
    try:
        with pytest.raises(HTTPException) as denied:
            market.save_contract_config(session, config())
        assert denied.value.status_code == 403
    finally:
        current_workbench.reset(marker)


def charts_for(identifiers, *, common=True):
    values = [100, 101, 600, 900, 110, 111, 700, 901]
    charts = {}
    for identifier in identifiers:
        index = IDS.index(identifier)
        days = ['2026-01-13', '2026-01-12'] if index < 4 else ['2026-01-14', '2026-01-13' if common else '2026-01-11']
        charts[identifier] = {'unit': '元/吨' if index % 4 < 2 else '手',
            'points': [{'date': day, 'value': values[index]} for day in days]}
    return charts


@pytest.mark.parametrize('common', [True, False])
def test_restricted_curve_uses_config_ids_one_selected_day_and_only_real_values(prepared, monkeypatch, common):
    session, scope = prepared
    market.save_contract_config(session, config())
    scope = replace(scope, metadata={**scope.metadata, 'can_read_source': False}, view_modules=('market',))
    session.info['knowledge_facts'] = False
    calls = []
    def fetch(checked, identifiers, settlements):
        calls.append((checked, identifiers, settlements))
        charts = charts_for(identifiers, common=common)
        stamp = '2026-01-13' if common else '2026-01-14'
        metadata = {identifier: {'unit': chart['unit'], 'knowledge_base_id': KB,
            'knowledge_base_tenant_id': 17} for identifier, chart in charts.items()}
        values = {identifier: {point['date']: point['value'] for point in chart['points'] if point['date'] == stamp}
                  for identifier, chart in charts.items()}
        return date.fromisoformat(stamp), metadata, values
    monkeypatch.setattr(market, '_same_day_contract_values', fetch)
    curve = market.readonly_contract_curve(session, scope)
    assert calls[0][1] == IDS[:8] and calls[0][2] == [IDS[0], IDS[4]]
    assert calls[0][0].knowledge_base_id == KB and calls[0][0].additional_knowledge_bases == ()
    assert curve['date'] == ('2026-01-13' if common else '2026-01-14')
    assert len(curve['rows']) == (2 if common else 1)
    if not common: assert curve['rows'][0]['contract'] == 'SYN-B'
    assert IDS[8] not in str(curve) and curve['source'] == 'knowledge-base'


def test_restricted_rejects_foreign_origin_page_revocation_and_changed_units(prepared, monkeypatch):
    session, scope = prepared
    market.save_contract_config(session, config())
    scope = replace(scope, metadata={**scope.metadata, 'can_read_source': False}, view_modules=('market',))
    from tin.web.restricted_views import ORIGINS_KEY
    origins = session.get(SourceMapping, ORIGINS_KEY + ':' + WB)
    original = deepcopy(origins.config)
    changed = deepcopy(original)
    changed['origins'][IDS[0]]['knowledge_base_id'] = OTHER_KB
    origins.config = changed
    session.commit()
    monkeypatch.setattr(market, '_same_day_contract_values', lambda *args: pytest.fail('foreign source must not be probed'))
    with pytest.raises(HTTPException) as denied: market.readonly_contract_curve(session, scope)
    assert denied.value.status_code == 403
    with pytest.raises(HTTPException) as denied: market.readonly_contract_curve(session, replace(scope, view_modules=('diagram',)))
    assert denied.value.status_code == 403
    origins.config = original
    session.commit()
    def wrong_unit(checked, identifiers, settlements):
        charts = charts_for(identifiers)
        metadata = {identifier: {'unit': chart['unit'], 'knowledge_base_id': KB,
            'knowledge_base_tenant_id': 17} for identifier, chart in charts.items()}
        metadata[IDS[0]]['unit'] = '美元/吨'
        return DAY, metadata, {}
    monkeypatch.setattr(market, '_same_day_contract_values', wrong_unit)
    with pytest.raises(HTTPException) as denied: market.readonly_contract_curve(session, scope)
    assert denied.value.status_code == 409


def test_latest_invalid_revision_does_not_revive_previous_value(prepared):
    session, _ = prepared
    market.save_contract_config(session, config())
    stamp = datetime(2026, 1, 13, tzinfo=SHANGHAI)
    session.add(Observation(series_id=IDS[0], value=999, as_of=stamp, fetched_at=stamp,
        caliber_snapshot={}, source='知识库结构化', status='无效', revision=1))
    session.commit()
    curve = market.configured_contract_curve(session, DAY)
    assert len(curve['rows']) == 1 and curve['rows'][0]['contract'] == 'SYN-B'


def test_zero_negative_prices_and_large_finite_geometry_do_not_invent_values():
    contracts = config()
    for values in ([0, -10], [-1e308, 1e308]):
        curve = market._curve(contracts, DAY, {IDS[0]: {DAY.isoformat(): values[0]}, IDS[4]: {DAY.isoformat(): values[1]}})
        assert [row['settle'] for row in curve['rows']] == values
        assert all(math.isfinite(row[axis]) for row in curve['rows'] for axis in ('x', 'y'))
        assert math.isfinite(curve['mid'])


@pytest.mark.parametrize('prices,expected', [([100, 110], 'Contango'), ([110, 100], 'Backwardation'),
                                           ([100, 100], '持平'), ([0, 0], '持平')])
def test_active_contract_endpoints_equal_are_flat_not_backwardation(prices, expected):
    values = {IDS[0]: {DAY.isoformat(): prices[0]}, IDS[4]: {DAY.isoformat(): prices[1]},
              IDS[2]: {DAY.isoformat(): 600}, IDS[6]: {DAY.isoformat(): 700}}
    curve = market._curve(config(), DAY, values)
    assert curve['shape'] == expected


def test_common_day_older_than_latest_two_points_is_the_same_in_both_views(prepared, monkeypatch):
    session, scope = prepared
    market.save_contract_config(session, config())
    for identifier, days in [(IDS[0], (15, 16)), (IDS[4], (17, 18))]:
        for day in days:
            stamp = datetime(2026, 1, day, tzinfo=SHANGHAI)
            session.add(Observation(series_id=identifier, value=200, as_of=stamp, fetched_at=stamp,
                caliber_snapshot={}, source='知识库结构化', status='正常'))
    session.commit()
    normal = market.configured_contract_curve(session)
    assert normal['date'] == DAY.isoformat() and len(normal['rows']) == 2
    snapshot = (DAY, session.info['knowledge_facts_catalog'],
        {IDS[0]: {DAY.isoformat(): 100}, IDS[4]: {DAY.isoformat(): 110}})
    monkeypatch.setattr(market, '_same_day_contract_values', lambda *args: snapshot)
    readonly = market.readonly_contract_curve(session,
        replace(scope, metadata={**scope.metadata, 'can_read_source': False}, view_modules=('market',)))
    assert readonly['date'] == normal['date']
    assert [row['settle'] for row in readonly['rows']] == [row['settle'] for row in normal['rows']]


@pytest.mark.parametrize('corrupt', [None, 'wrong_day', 'foreign_id', 'duplicate', 'future', 'unselected', 'nonfinite'])
def test_same_day_consumer_requests_one_bound_snapshot_and_rejects_extra_values(prepared, monkeypatch, corrupt):
    session, scope = prepared
    scope = replace(scope, metadata={**scope.metadata, 'can_read_source': False}, view_modules=('market',))
    doc = {'bounded': True, 'knowledge_base_id': KB, 'knowledge_base_tenant_id': 17,
        'version': 'a' * 64, 'offset': 0, 'next_offset': None, 'truncated': False,
        'selected_date': DAY.isoformat(), 'total': 2,
        'indicators': [{'series_id': identifier, 'unit': session.info['knowledge_facts_catalog'][identifier]['unit']}
                       for identifier in IDS[:8]],
        'rows': [{'series_id': identifier, 'date': DAY.isoformat(), 'value': value, 'value_status': 'valid'}
                 for identifier, value in [(IDS[0], 100), (IDS[4], 110)]]}
    if corrupt == 'wrong_day': doc['rows'][0]['date'] = '2026-01-12'
    elif corrupt == 'foreign_id': doc['rows'][0]['series_id'] = IDS[8]
    elif corrupt == 'duplicate': doc['rows'][1] = dict(doc['rows'][0])
    elif corrupt == 'future': doc['selected_date'] = '9999-01-01'
    elif corrupt == 'unselected': doc['selected_date'] = None
    elif corrupt == 'nonfinite': doc['rows'][0]['value'] = math.inf
    calls = []
    monkeypatch.setattr('tin.integration.kb_facts.current_sources', lambda checked: [])
    class Response:
        status_code = 200
        def raise_for_status(self): pass
        def json(self): return doc
    def fetch(method, path, body, **kwargs):
        calls.append((method, path, body))
        return Response()
    monkeypatch.setattr('tin.provenance.bridge.structured_request', fetch)
    if corrupt is None:
        chosen, metadata, values = market._same_day_contract_values(scope, IDS[:8], [IDS[0], IDS[4]])
        assert chosen == DAY and set(metadata) == set(IDS[:8]) and len(values) == 2
        assert calls[0][2]['points_per_series'] == 1
        assert calls[0][2]['same_day_series_ids'] == [IDS[0], IDS[4]]
        assert calls[0][2]['series_ids'] == IDS[:8] and calls[0][2]['view_module'] == 'market'
        assert set(calls[0][2]) == {'sources', 'series_ids', 'same_day_series_ids', 'view_module', 'as_of', 'points_per_series'}
    else:
        with pytest.raises(HTTPException) as denied:
            market._same_day_contract_values(scope, IDS[:8], [IDS[0], IDS[4]])
        assert denied.value.status_code == 503
