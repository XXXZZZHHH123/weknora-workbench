"""Synthetic, exact moving averages; missing periods are never filled."""
from datetime import date, datetime, timezone
from decimal import Decimal

import pytest
from pydantic import ValidationError
from sqlalchemy.orm import sessionmaker
from sqlalchemy.pool import StaticPool

from tin.compute.moving_average import moving_average_target, sma_boundary, sma_source_window, sma_values
from tin.db import Base, make_engine
from tin.models import Indicator, Observation
from tin.schemas.judgment import Threshold


SID = 'NEW.CHECK'
DAY = date(2031, 1, 10)


def threshold(value, operator='<'):
    return Threshold(id='moving_check', name='合成均线观察', series_id=SID,
                     value=value, operator=operator, type='观察位', horizon='短期')


def values(*items, unit='kg'):
    return [{'value': item, 'status': '正常', 'unit': unit} for item in items]


@pytest.fixture
def facts():
    engine = make_engine('sqlite://', poolclass=StaticPool,
                         connect_args={'check_same_thread': False})
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as session:
        session.add(Indicator(series_id=SID, name='合成数量', variety='TEST', category='合成',
            unit='kg', caliber={}, source='synthetic', frequency='day', fetch_mode='manual'))
        session.commit()
        yield session
    engine.dispose()


def point(session, day, value, *, revision=0, status='正常', unit='kg', sid=SID):
    session.add(Observation(series_id=sid, value=value,
        as_of=datetime(2031, 1, day, 6, tzinfo=timezone.utc),
        fetched_at=datetime(2031, 1, 15, tzinfo=timezone.utc),
        caliber_snapshot={'unit': unit}, source='synthetic', revision=revision, status=status))
    session.commit()


@pytest.mark.parametrize('window', [2, 20, 120])
def test_typed_average_target_round_trips_without_a_fake_constant(window):
    rule = threshold({'kind': 'sma', 'window': window})
    assert rule.model_dump(mode='json')['value'] == {'kind': 'sma', 'window': window}
    assert moving_average_target(rule.model_dump(mode='json')['value']) == {'kind': 'sma', 'window': window}


@pytest.mark.parametrize('target', [
    {'kind': 'sma', 'window': 1}, {'kind': 'sma', 'window': 121},
    {'kind': 'sma', 'window': True}, {'kind': 'sma', 'window': '20'},
    {'kind': 'sma', 'window': 20.0}, {'kind': 'sma'},
    {'kind': 'ema', 'window': 20}, {'kind': 'sma', 'window': 20, 'offset': 1},
])
def test_invalid_target_is_rejected_by_runtime_and_saved_schema(target):
    with pytest.raises(ValueError):
        moving_average_target(target)
    with pytest.raises(ValidationError):
        threshold(target)


def test_between_does_not_accept_a_dynamic_object_or_replace_it_with_zero():
    with pytest.raises(ValidationError):
        threshold({'kind': 'sma', 'window': 20}, operator='between')
    assert threshold(0).model_dump()['value'] == 0
    assert threshold([-2, 2], operator='between').model_dump()['value'] == [-2, 2]
    assert moving_average_target(0) is None
    assert moving_average_target([-2, 2]) is None


def test_average_has_no_output_until_the_whole_window_exists():
    assert sma_values(values(1, 3, 5, 7), 3) == [None, None, 3, 5]
    assert sma_values(values(1, 3), 3) == [None, None]
    assert sma_values([], 3) == []


def test_average_keeps_zero_and_negative_values_and_decimal_precision():
    assert sma_values(values(-2, 0, 2, 4), 2) == [None, -1, 1, 3]
    assert sma_values(values(0.1, 0.2, 0.3), 3) == [None, None, 0.2]


@pytest.mark.parametrize('bad', [None, float('nan'), float('inf'), float('-inf')])
def test_nonfinite_or_missing_value_breaks_each_affected_window(bad):
    result = sma_values(values(1, bad, 3, 5), 2)
    assert result == [None, None, None, 4]


def test_invalid_status_does_not_skip_back_to_older_good_values():
    points = values(10, 20, 30, 40)
    points[1]['status'] = '无效'
    assert sma_values(points, 2) == [None, None, None, 35]


def test_changed_explicit_unit_breaks_mixed_unit_windows():
    points = values(1, 2, 300, 400)
    points[2]['unit'] = points[3]['unit'] = 'g'
    assert sma_values(points, 2) == [None, 1.5, None, 350]


def test_boundary_uses_newest_revision_once_and_respects_cutoff(facts):
    point(facts, 1, 10)
    point(facts, 2, 20)
    point(facts, 2, 26, revision=1)
    point(facts, 3, 30)
    point(facts, 11, 9000)
    boundary = sma_boundary(facts, SID, DAY, 3)
    assert boundary['count'] == 3
    assert boundary['total'] == Decimal(66)
    assert boundary['value'] == 22
    assert not boundary['error']
    assert [row.value for row in boundary['points']] == [10, 26, 30]


def test_boundary_preserves_decimal_total_for_exact_trigger_comparison(facts):
    point(facts, 1, 0.1)
    point(facts, 2, 0.2)
    point(facts, 3, 0.3)
    boundary = sma_boundary(facts, SID, DAY, 3)
    assert boundary['total'] == Decimal('0.6')
    assert boundary['value'] == 0.2


def test_boundary_reports_real_count_without_extending_an_incomplete_window(facts):
    point(facts, 1, 10)
    point(facts, 2, 20)
    boundary = sma_boundary(facts, SID, DAY, 3)
    assert boundary['count'] == 2
    assert boundary['value'] is None and boundary['total'] is None
    assert boundary['error']


def test_boundary_does_not_replace_the_latest_invalid_record_with_an_older_value(facts):
    for day in (1, 2, 3):
        point(facts, day, 10 * day)
    point(facts, 4, 900, status='无效')
    boundary = sma_boundary(facts, SID, DAY, 3)
    assert boundary['count'] == 3
    assert boundary['value'] is None and boundary['total'] is None
    assert [row.value for row in boundary['points']] == [20, 30, 900]


def test_boundary_rejects_observation_unit_that_disagrees_with_current_catalog(facts):
    point(facts, 1, 1000, unit='g')
    point(facts, 2, 2000, unit='g')
    boundary = sma_boundary(facts, SID, DAY, 2)
    assert boundary['value'] is None and boundary['error']


def test_unknown_series_is_missing_without_touching_other_series(facts):
    point(facts, 1, 10)
    point(facts, 2, 20)
    boundary = sma_boundary(facts, 'NEW.OTHER', DAY, 2)
    assert boundary['value'] is None and boundary['count'] == 0 and boundary['error']


@pytest.mark.parametrize('quote', ['MA20', 'SMA(20)', 'ma 20', '20日均线', '20期移动平均线',
                                   '20次简单移动平均线', '低于20日均线时复核', '20日线'])
def test_source_window_only_checks_the_explicit_model_declared_operand(quote):
    assert sma_source_window(quote, 20)


@pytest.mark.parametrize('quote', ['MA20.5', 'EMA20', '20吨', '20', 'SMA(20.5)', 'MA200',
                                   '-20日均线', '120日均线', '20.5日均线', 'MA20e1', '120日线', '20日线200'])
def test_arbitrary_quantity_wrong_window_and_different_average_cannot_validate_sma(quote):
    assert not sma_source_window(quote, 20)


def test_rolling_overlay_accepts_observation_objects_without_turning_gaps_into_zero():
    from types import SimpleNamespace
    points = [SimpleNamespace(value=value, status='正常', caliber_snapshot={'unit': 'kg'})
              for value in [1, 3, 5]]
    assert sma_values(points, 2) == [None, 2, 4]
    points[1].status = '无效'
    assert sma_values(points, 2) == [None, None, None]
