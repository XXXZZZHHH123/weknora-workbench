"""Knowledge-base imports have coverage gaps, not invented fetcher failures."""
from datetime import date, datetime, timezone

import pytest
from sqlalchemy import func, select

from tin.export import board
from tin.models import Indicator, Observation


@pytest.mark.parametrize('has_prior', [False, True])
def test_readonly_coverage_gap_does_not_invent_an_automatic_fetch_failure(session, monkeypatch, has_prior):
    identifier = 'isolated-readonly-field'
    session.add(Indicator(series_id=identifier, name='Current source quantity', variety='TEST',
        category='Imported', caliber={'time_type': '原表日期'}, unit='件', source='知识库结构化',
        frequency='日', fetch_mode='readonly', status='可用'))
    session.flush()
    prior = datetime(2026, 2, 2, tzinfo=timezone.utc)
    if has_prior:
        session.add(Observation(series_id=identifier, value=0, as_of=prior, fetched_at=prior,
            caliber_snapshot={'time_type': '原表日期'}, source='知识库结构化', status='正常'))
        session.flush()
    before = session.scalar(select(func.count()).select_from(Observation))
    calls, original = [], board._auto_gap_reason
    def auto_reason(database, series_id, day):
        calls.append(series_id)
        return original(database, series_id, day)
    monkeypatch.setattr(board, '_auto_gap_reason', auto_reason)
    gap = next(row for row in board.gaps(session, 'TEST', date(2026, 2, 3)) if row['series_id'] == identifier)
    assert identifier not in calls
    assert gap['reason'].startswith('截至所选日期，知识库暂无满足更新频率的已确认观测')
    assert '自动' not in gap['reason'] and '取数' not in gap['reason']
    assert gap['last_as_of'] == ('2026-02-02' if has_prior else None)
    assert gap['expected_by'] == '2026-02-03'
    assert session.scalar(select(func.count()).select_from(Observation)) == before
