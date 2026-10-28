"""Six explicit series at most, real dates and bounded report-source windows."""
from datetime import date, datetime
from fastapi import HTTPException
import pytest
from sqlalchemy import event

from tin.config import SHANGHAI
from tin.models import Indicator, Observation


def test_report_figure_bounds_are_not_days_or_arbitrary_history():
    from tin.web.report_figures import validate_selection, points_for_period
    ids = ['kb-' + format(i, '048x') for i in range(7)]
    assert validate_selection(ids[:6], ids) == ids[:6]
    assert points_for_period('daily') == 90
    assert points_for_period('weekly') == 120
    with pytest.raises(HTTPException) as cap:
        validate_selection(ids, ids)
    assert cap.value.status_code == 422
    with pytest.raises(HTTPException) as outside:
        validate_selection(ids[:1], ids[1:])
    assert outside.value.status_code == 403
    with pytest.raises(HTTPException):
        validate_selection([ids[0], ids[0]], ids)


def test_figure_reads_highest_revision_only_real_dates_and_preserves_raw_values(session):
    from tin.web.report_figures import read_figures
    sid = 'fixture.figure'
    session.add(Indicator(series_id=sid, name='Actual dated series', variety='SN', category='fixture',
        unit='吨', source='Actual source', caliber={}, frequency='日', fetch_mode='readonly'))
    session.flush()
    for stamp, value, revision, status in [('2026-01-01', 1, 0, '正常'),
        ('2026-01-04', 3, 0, '正常'), ('2026-01-04', 4, 1, '正常'),
        ('2026-01-05', 100, 0, '正常'), ('2026-01-05', 101, 1, '异常'),
        ('2026-01-10', 999, 0, '正常')]:
        session.add(Observation(series_id=sid, value=value, revision=revision, status=status,
            as_of=datetime.fromisoformat(stamp).replace(tzinfo=SHANGHAI),
            fetched_at=datetime(2026, 1, 11, tzinfo=SHANGHAI), caliber_snapshot={},
            source='Actual source', source_url=None))
    session.commit()
    writes = []
    def watch(_conn, _cur, sql, *_):
        if sql.lstrip().upper().startswith(('INSERT', 'UPDATE', 'DELETE')):
            writes.append(sql)
    engine = session.get_bind()
    event.listen(engine, 'before_cursor_execute', watch)
    try:
        result = read_figures(session, [sid], date(2026, 1, 9), 'daily')
    finally:
        event.remove(engine, 'before_cursor_execute', watch)
    assert not writes
    series = result['series'][0]
    assert series['unit'] == '吨'
    assert [x['date'] for x in series['points']] == ['2026-01-01', '2026-01-04']
    assert [x['value'] for x in series['points']] == [1, 4]
    assert result['points_per_series'] == 90


def test_graph_coordinates_use_actual_time_and_do_not_overflow(session):
    from tin.web.report_figures import chart_view
    row = chart_view('fixture', 'Name', '吨', [
        {'date': '2026-01-01', 'value': 0}, {'date': '2026-01-02', 'value': 2},
        {'date': '2026-01-11', 'value': 5}])
    points = row['points']
    assert points[1]['x'] - points[0]['x'] == pytest.approx((points[2]['x'] - points[0]['x']) / 10, abs=.02)
    extreme = chart_view('fixture', 'Name', '吨', [
        {'date': '2026-01-01', 'value': -1e308}, {'date': '2026-01-02', 'value': 1e308}])
    assert all(0 <= x['y'] <= 200 for x in extreme['points'])
