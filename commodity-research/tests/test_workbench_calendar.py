"""Navigation dates must not scan or materialize every confirmed observation."""
from datetime import date, datetime, timedelta, timezone
from contextlib import contextmanager

import pytest
from sqlalchemy.orm import Session

from tin.compute import engine as compute_engine
from tin.compute.engine import latest_trade_date
from tin.config import SHANGHAI
from tin.db import make_engine
from tin.web.app import _neighbour_dates


@pytest.fixture
def calendar_clock(monkeypatch):
    class CalendarDateTime(datetime):
        current = datetime(2031, 9, 14, 12, tzinfo=SHANGHAI)

        @classmethod
        def now(cls, tz=None):
            return cls.current.astimezone(tz) if tz else cls.current.replace(tzinfo=None)

    monkeypatch.setattr(compute_engine, 'datetime', CalendarDateTime)
    return CalendarDateTime


@pytest.fixture
def fact_calendar(calendar_clock):
    engine = make_engine('sqlite://')
    with engine.connect() as connection:
        for source in ('primary_facts', 'macro_facts'):
            connection.exec_driver_sql(f"ATTACH DATABASE ':memory:' AS {source}")
            connection.exec_driver_sql(f'CREATE TABLE {source}.indicators (series_id TEXT PRIMARY KEY)')
            connection.exec_driver_sql(f'CREATE TABLE {source}.observations (series_id TEXT, as_of TEXT, status TEXT)')
            connection.exec_driver_sql(f'CREATE INDEX {source}.series_dates ON observations(series_id,as_of)')
            identifiers = [(source + str(i),) for i in range(8)]
            connection.exec_driver_sql(f'INSERT INTO {source}.indicators VALUES (?)', identifiers)
            start = datetime(2030, 12, 31, 17, tzinfo=timezone.utc)
            rows = [(identifier, (start + timedelta(days=day)).strftime('%Y-%m-%d %H:%M:%S.%f'), '正常')
                    for identifier, in identifiers for day in range(256)]
            # A non-normal row must not extend the navigation dates.
            rows.append((identifiers[0][0], '2032-01-01 00:00:00.000000', '失效'))
            connection.exec_driver_sql(f'INSERT INTO {source}.observations VALUES (?,?,?)', rows)
        for table in ('indicators', 'observations'):
            connection.exec_driver_sql(f'CREATE TEMP VIEW {table} AS SELECT * FROM primary_facts.{table} UNION ALL SELECT * FROM macro_facts.{table}')
        connection.commit()
        with Session(bind=connection) as session:
            session.info['workbench_id'] = 'synthetic-workbench'
            yield session
    engine.dispose()


@contextmanager
def bounded_sql(session):
    driver = session.connection().connection.driver_connection
    steps = []
    def progress():
        steps.append(1)
        return int(len(steps) > 80)
    driver.set_progress_handler(progress, 100)
    try:
        yield
    finally:
        driver.set_progress_handler(None, 0)


def test_workbench_latest_date_uses_series_indexes(fact_calendar):
    with bounded_sql(fact_calendar):
        assert latest_trade_date(fact_calendar) == date(2031, 9, 13)


def test_workbench_neighbour_dates_use_series_indexes(fact_calendar):
    with bounded_sql(fact_calendar):
        assert _neighbour_dates(fact_calendar, date(2031, 5, 10)) == ('2031-05-09', '2031-05-11')


def test_workbench_calendar_preserves_local_midnight_and_empty_boundaries(fact_calendar):
    assert _neighbour_dates(fact_calendar, date(2031, 1, 1)) == (None, '2031-01-02')
    assert _neighbour_dates(fact_calendar, date(2031, 9, 13)) == ('2031-09-12', None)
    assert _neighbour_dates(fact_calendar, date(2030, 12, 31)) == (None, '2031-01-01')


def test_workbench_calendar_uses_local_day_edges_and_filters_non_normal_rows(fact_calendar):
    c = fact_calendar.connection()
    c.exec_driver_sql('DELETE FROM primary_facts.observations')
    c.exec_driver_sql('DELETE FROM macro_facts.observations')
    c.exec_driver_sql('INSERT INTO primary_facts.observations VALUES (?,?,?)', [
        ('primary_facts0', '2030-12-31 15:59:59.000000', '正常'),
        ('primary_facts0', '2030-12-31 16:00:00.000000', '正常'),
        ('primary_facts0', '2031-01-01 15:59:59.000000', '正常'),
        ('primary_facts0', '2031-01-01 16:00:00.000000', '正常'),
        ('primary_facts0', '2032-01-01 00:00:00.000000', '失效'),
    ])
    assert _neighbour_dates(fact_calendar, date(2031, 1, 1)) == ('2030-12-31', '2031-01-02')
    assert latest_trade_date(fact_calendar) == date(2031, 1, 2)


def test_workbench_calendar_has_no_dates_when_confirmed_facts_are_empty(fact_calendar):
    c = fact_calendar.connection()
    c.exec_driver_sql('DELETE FROM primary_facts.observations')
    c.exec_driver_sql('DELETE FROM macro_facts.observations')
    assert latest_trade_date(fact_calendar) is None
    assert _neighbour_dates(fact_calendar, date(2031, 1, 1)) == (None, None)


def test_workbench_default_date_obeys_shanghai_midnight_without_removing_future_rows(fact_calendar, calendar_clock):
    c = fact_calendar.connection()
    c.exec_driver_sql('DELETE FROM primary_facts.observations')
    c.exec_driver_sql('DELETE FROM macro_facts.observations')
    c.exec_driver_sql('INSERT INTO primary_facts.observations VALUES (?,?,?)', [
        ('primary_facts0', '2031-01-01 15:59:59.000000', '正常'),
        ('primary_facts0', '2031-01-01 16:00:00.000000', '正常'),
    ])
    # UTC 15:59:59 and 16:00:00 straddle Shanghai's local midnight.
    calendar_clock.current = datetime(2031, 1, 1, 23, 59, 59, tzinfo=SHANGHAI)
    with bounded_sql(fact_calendar):
        assert latest_trade_date(fact_calendar) == date(2031, 1, 1)
    calendar_clock.current = datetime(2031, 1, 2, 0, 0, tzinfo=SHANGHAI)
    assert latest_trade_date(fact_calendar) == date(2031, 1, 2)
    assert c.exec_driver_sql('SELECT count(*) FROM primary_facts.observations').scalar() == 2


def test_workbench_default_date_is_empty_when_only_future_dates_exist(fact_calendar, calendar_clock):
    calendar_clock.current = datetime(2030, 12, 31, 23, 59, tzinfo=SHANGHAI)
    with bounded_sql(fact_calendar):
        assert latest_trade_date(fact_calendar) is None
    assert fact_calendar.connection().exec_driver_sql(
        "SELECT count(*) FROM primary_facts.observations WHERE status = '正常'"
    ).scalar() == 8 * 256
