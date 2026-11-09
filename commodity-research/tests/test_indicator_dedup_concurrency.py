"""The database fences cross-pair confirmations, beyond per-row CAS checks."""
from copy import deepcopy

from fastapi import HTTPException
import pytest
from sqlalchemy import select

from tin.integration import indicator_dedup as service
from tin.models.indicator_dedup import IndicatorCorrespondence
from tests.test_indicator_dedup import decide, field, only, setup


def test_database_prevents_two_confirmed_targets_and_undo_releases_constraint(setup):
    scope, _ = setup
    first = decide(scope, only(scope))
    with service.correspondence_session(scope) as session:
        original = session.get(IndicatorCorrespondence, first['id'])
        values = {column.name: deepcopy(getattr(original, column.name))
                  for column in IndicatorCorrespondence.__table__.columns}
    values.update(id='e' * 64, default_id='kb-' + 'c' * 48, state='pending', revision=1,
                  confirmed_by=None, confirmed_at=None, history=[])
    values['default']['series_id'] = values['default_id']
    with service.correspondence_session(scope) as session:
        second = IndicatorCorrespondence(**values)
        session.add(second)
        session.commit()
    # Simulate a stale concurrent decision after its application-level conflict
    # SELECT. The partial unique index still rejects its actual write.
    with pytest.raises(HTTPException) as error:
        with service.correspondence_session(scope) as session:
            second = session.get(IndicatorCorrespondence, values['id'])
            second.state = 'confirmed_default'
            session.commit()
    assert error.value.status_code == 409
    with service.correspondence_session(scope) as session:
        confirmed = session.scalars(select(IndicatorCorrespondence).where(
            IndicatorCorrespondence.state == 'confirmed_default')).all()
        assert [row.id for row in confirmed] == [first['id']]
        assert session.get(IndicatorCorrespondence, values['id']).state == 'pending'
    service.undo(scope, first['id'], first['revision'])
    with service.correspondence_session(scope) as session:
        second = session.get(IndicatorCorrespondence, values['id'])
        second.state = 'confirmed_default'
        session.commit()
    assert service.confirmed_replacements(scope) == {values['personal_id']: values['default_id']}


def test_candidate_capacity_stops_while_building_pairs(setup, monkeypatch):
    scope, _ = setup
    own, central = scope.sources
    fields = [field('kb-' + f'{index:048x}', own, description='S1234567') for index in range(101)]
    fields += [field('kb-' + f'{index:048x}', central, description='S1234567') for index in range(1000, 1100)]
    original, generated = service.pair_id, 0
    def bounded_pair(*args):
        nonlocal generated
        generated += 1
        assert generated <= 10001, '拒绝容量必须发生在生成期间，不能先构造完整笛卡尔积'
        return original(*args)
    monkeypatch.setattr(service, 'pair_id', bounded_pair)
    with pytest.raises(HTTPException) as error:
        service.candidate_pairs(scope, fields)
    assert error.value.status_code == 413 and generated == 10001
