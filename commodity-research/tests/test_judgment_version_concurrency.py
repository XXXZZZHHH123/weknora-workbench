"""Append-only version guards on a fresh isolated SQLite file."""
from concurrent.futures import ThreadPoolExecutor
from datetime import date
from threading import Barrier
import pytest
from sqlalchemy import event, select
from sqlalchemy.exc import IntegrityError, OperationalError
from sqlalchemy.orm import sessionmaker
from tin.db import Base, make_engine
from tin.judgments.service import JudgmentError, latest, save_version
from tin.models import Judgment
from tin.schemas.judgment import JudgmentIn


def payload(**values):
    return JudgmentIn.model_validate({'variety':'SYNTHETIC','author':'r','written_at':date(2030,1,2),
                                      'contradiction':{},**values})


def factory(tmp_path):
    engine=make_engine('sqlite:///'+str(tmp_path/'versions.db'),connect_args={'check_same_thread':False})
    Base.metadata.create_all(engine)
    return engine,sessionmaker(engine,expire_on_commit=False)


def test_stale_basis_is_rejected_without_replacing_prior_version(tmp_path):
    engine,session_factory=factory(tmp_path)
    with session_factory() as session:
        row=save_version(session,payload(),actor='r',expected_base_version=0);session.commit()
        assert latest(session,'SYNTHETIC').id==row.id
        with pytest.raises(JudgmentError,match='已有新版本'):
            save_version(session,payload(),actor='r',expected_base_version=0)
        assert len(session.scalars(select(Judgment)).all())==1
    engine.dispose()


def test_cost_references_cannot_bypass_registered_id_validation(tmp_path):
    engine,session_factory=factory(tmp_path)
    with session_factory() as session:
        with pytest.raises(JudgmentError,match='未登记'):
            save_version(session,payload(cost_line={'items':[{'desc':'Basis','series_id':'foreign'}]}),actor='r')
        assert session.scalars(select(Judgment)).all()==[]
    engine.dispose()


def test_two_concurrent_saves_of_same_basis_have_one_winner_and_no_overwrite(tmp_path):
    engine,session_factory=factory(tmp_path);barrier=Barrier(2)
    def at_max(connection,cursor,statement,parameters,context,executemany):
        if 'max(judgments.version)' in statement:barrier.wait(timeout=5)
    event.listen(engine,'after_cursor_execute',at_max)
    def save():
        with session_factory() as session:
            try:
                row=save_version(session,payload(),actor='r',expected_base_version=0);session.commit();return row.version
            except (JudgmentError,IntegrityError,OperationalError):session.rollback();return None
    with ThreadPoolExecutor(max_workers=2) as pool:results=list(pool.map(lambda _:save(),range(2)))
    event.remove(engine,'after_cursor_execute',at_max)
    assert results.count(1)==1 and results.count(None)==1
    with session_factory() as session:
        rows=session.scalars(select(Judgment)).all();assert len(rows)==1 and rows[0].version==1
    engine.dispose()
