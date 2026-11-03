"""Real SQLite processes must admit only one analysis for the same exact input."""
import multiprocessing
import time
from types import SimpleNamespace

from fastapi import HTTPException
import pytest
from sqlalchemy.orm import sessionmaker

from tin.db import Base, make_engine
from tin.models import SourceMapping
from tin.web import judgment_generation as generation
from tin.web import judgment_analysis_cache as cache


def _scope():
    return SimpleNamespace(identity=SimpleNamespace(tenant_id=77, user_id='synthetic-owner'),
        workbench_id='synthetic-workbench')


def _claim_worker(path, barrier, queue):
    engine = make_engine('sqlite:///' + path)
    factory = sessionmaker(engine)
    generation.result_session = lambda scope: factory()
    barrier.wait(timeout=20)
    try:
        ticket = cache.claim(_scope(), 'shared-synthetic-analysis')
        queue.put((200, ticket.get('fence')))
    except HTTPException as error:
        queue.put((error.status_code, None))
    finally:
        engine.dispose()


@pytest.mark.parametrize('expired', [False, True])
def test_two_processes_cannot_both_start_one_pristine_analysis(tmp_path, monkeypatch, expired):
    path = str(tmp_path / 'synthetic-race.sqlite')
    engine = make_engine('sqlite:///' + path)
    Base.metadata.create_all(engine)
    factory = sessionmaker(engine)
    monkeypatch.setattr(generation, 'result_session', lambda scope: factory())
    if expired:
        cache.claim(_scope(), 'shared-synthetic-analysis')
        with factory() as session:
            row = session.get(SourceMapping, 'shared-synthetic-analysis')
            row.config = {**row.config, 'lease_until': time.time() - 1}
            session.commit()
    context = multiprocessing.get_context('spawn')
    barrier, queue = context.Barrier(2), context.Queue()
    workers = [context.Process(target=_claim_worker, args=(path, barrier, queue)) for _ in range(2)]
    for worker in workers:
        worker.start()
    try:
        outcomes = [queue.get(timeout=40) for _ in workers]
        assert sorted(result[0] for result in outcomes) == [200, 409]
        winner = next(result[1] for result in outcomes if result[0] == 200)
        with factory() as session:
            row = session.get(SourceMapping, 'shared-synthetic-analysis')
            assert row.config['fence'] == winner
            assert row.config['state'] == 'pending'
    finally:
        for worker in workers:
            worker.join(timeout=5)
            if worker.is_alive():
                worker.terminate()
                worker.join(timeout=5)
        engine.dispose()
