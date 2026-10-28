"""Synthetic decimal boundaries for deterministic rules; no model or live data."""

import json
from datetime import date, datetime, timezone
from decimal import Decimal, localcontext
from types import SimpleNamespace

import pytest
from sqlalchemy.orm import sessionmaker
from sqlalchemy.pool import StaticPool

from tin.brief.prompt import scope_applies
from tin.compute.signals import _falsifier_signal, _quantities
from tin.db import Base, make_engine
from tin.export.board import threshold_radar_values
from tin.models import Indicator, Judgment, Observation


SERIES = 'SYNTHETIC.PRECISION'
DAY = date(2026, 8, 4)


@pytest.fixture
def precision_session():
    engine = make_engine('sqlite://', poolclass=StaticPool)
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as session:
        session.add(Indicator(series_id=SERIES, name='Synthetic decimal', variety='TEST',
                              category='测试', caliber={}, unit='u', source='synthetic',
                              frequency='日', fetch_mode='manual', phase='P1', status='可用'))
        judgment = Judgment(variety='TEST', version=1, author='synthetic', written_at=DAY,
                            review_due=DAY, contradiction={}, marginal_focus=[], pricing_power={},
                            whitelist=[], thresholds=[], falsifiers=[], status='草稿', unstructured=[],
                            created_at=datetime(2026, 8, 1, tzinfo=timezone.utc))
        session.add(judgment)
        session.commit()
        yield session, judgment
    engine.dispose()


def add_points(session, *values):
    for day, value in enumerate(values, start=1):
        add_point(session, day, value)
    session.commit()


def add_point(session, day, value, revision=0):
    when = datetime(2026, 8, day, 10, tzinfo=timezone.utc)
    session.add(Observation(series_id=SERIES, value=value, as_of=when, fetched_at=when,
                            caliber_snapshot={}, source='synthetic', revision=revision))


def falsifier(expression, periods=1):
    return {'id': 'decimal-boundary', 'series_id': SERIES, 'expression': expression,
            'consecutive_periods': periods, 'unit_confirmed': 'u'}


@pytest.mark.parametrize('previous,current,target', [(0.2, 0.3, '0.1'), (0.3, 0.2, '-0.1'),
                                                    (0.1, 0.4, '0.3')])
@pytest.mark.parametrize('operator,expected', [('>', False), ('>=', True), ('<', False), ('<=', True)])
def test_change_boundaries_agree_in_signals_and_scope(precision_session, previous, current,
                                                     target, operator, expected):
    session, judgment = precision_session
    add_points(session, previous, current)
    expression = f'change {operator} {target}'
    signal = _falsifier_signal(session, judgment, falsifier(expression), DAY)
    assert (signal.state == '触发') is expected
    assert signal.current_value == float(target)
    assert type(signal.current_value) is float
    assert scope_applies(session, {'series_id': SERIES, 'when': expression}, DAY)[0] is expected
    session.commit()
    session.expire_all()
    assert type(signal.current_value) is float
    json.dumps({'current_value': signal.current_value, 'evidence': signal.evidence})


@pytest.mark.parametrize('target,expected', [('0.1000000000000000000000000001', True),
                                            ('0.0999999999999999999999999999', False)])
def test_legacy_expression_threshold_is_not_rounded_before_comparison(precision_session, target, expected):
    session, judgment = precision_session
    add_points(session, 0.1)
    expression = f'value < {target}'
    assert (_falsifier_signal(session, judgment, falsifier(expression), DAY).state == '触发') is expected
    assert scope_applies(session, {'series_id': SERIES, 'when': expression}, DAY)[0] is expected


def test_decimal_change_keeps_consecutive_periods_and_revision_window(precision_session):
    session, judgment = precision_session
    add_points(session, 0.1, 9.0, 0.3)
    add_point(session, 2, 0.2, revision=1)
    add_point(session, 5, 100.0)  # Beyond DAY; must not enter the current window.
    session.commit()
    signal = _falsifier_signal(session, judgment, falsifier('change >= 0.1', periods=2), DAY)
    assert signal.state == '触发'
    assert signal.current_value == 0.1
    assert [entry['value'] for entry in signal.evidence] == [0.1, 0.2, 0.3]
    assert len(signal.evidence) == 3


def test_existing_float_artifact_is_compared_as_stored(precision_session):
    session, judgment = precision_session
    stored = 0.1 + 0.2
    assert str(stored) == '0.30000000000000004'
    add_points(session, 0.2, stored)
    signal = _falsifier_signal(session, judgment, falsifier('change > 0.1'), DAY)
    assert signal.state == '触发'
    assert signal.current_value == 0.10000000000000004
    assert scope_applies(session, {'series_id': SERIES, 'when': 'change > 0.1'}, DAY)[0]


def test_subtraction_does_not_inherit_decimal_context_precision():
    points = [SimpleNamespace(value=1e-300), SimpleNamespace(value=1e300)]
    with localcontext() as context:
        context.prec = 6
        quantity = _quantities('change', points)[0][1]
    assert type(quantity) is Decimal
    expected = Decimal('9' * 600 + 'e-300')
    assert quantity == expected


def test_radar_uses_decimal_distance_and_json_number_progress():
    rule = {'id': 'threshold', 'name': 'Synthetic threshold', 'series_id': SERIES,
            'type': '观察位', 'horizon': '短期', 'operator': '<', 'value': 1.0}
    values = {SERIES: {'name': 'Synthetic decimal', 'unit': '元',
                       'date': DAY.isoformat(), 'value': 1.00005}}
    row = threshold_radar_values(SimpleNamespace(thresholds=[rule]), values)[0]
    assert row['state'] == '未触发'
    assert row['distance'] == '距触发 0.0000 元'  # Exact halfway rounds to the even fourth digit.
    assert type(row['value']) is float
    assert type(row['progress']) in (int, float)
    json.dumps(row)


@pytest.mark.parametrize('operator,value,expected', [('>', 0.3, '未触发'), ('>=', 0.3, '已触发'),
                                                    ('<', 0.3, '未触发'), ('<=', 0.3, '已触发'),
                                                    ('between', [0.3, 0.4], '在区间内')])
def test_radar_keeps_inclusive_and_exclusive_boundaries(operator, value, expected):
    rule = {'id': 'threshold', 'name': 'Synthetic threshold', 'series_id': SERIES,
            'type': '观察位', 'horizon': '短期', 'operator': operator, 'value': value}
    values = {SERIES: {'name': 'Synthetic decimal', 'unit': 'u', 'date': DAY.isoformat(), 'value': 0.3}}
    row = threshold_radar_values(SimpleNamespace(thresholds=[rule]), values)[0]
    assert row['state'] == expected
    assert type(row['progress']) in (int, float)
    json.dumps(row)


@pytest.mark.parametrize('literal', ['9' * 129, '1e1001', '1e-1001'])
def test_invalid_legacy_literals_raise_instead_of_becoming_numeric_decisions(precision_session, literal):
    session, judgment = precision_session
    add_points(session, 0.1)
    expression = f'value > {literal}'
    with pytest.raises(ValueError):
        _falsifier_signal(session, judgment, falsifier(expression), DAY)
    with pytest.raises(ValueError):
        scope_applies(session, {'series_id': SERIES, 'when': expression}, DAY)
