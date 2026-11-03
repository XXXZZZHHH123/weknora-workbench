"""Independent SQLite processes cannot both model-generate one fingerprint."""
import multiprocessing
from types import SimpleNamespace
import time

from fastapi import HTTPException
import pytest
from sqlalchemy.orm import sessionmaker

from tin.db import Base, make_engine
from tin.models import SourceMapping
from tin.web import diagram_analysis_cache as cache
from tests.test_diagram_analysis_cache import snapshot


def scope():
    return SimpleNamespace(identity=SimpleNamespace(tenant_id=77, user_id='synthetic-owner', can_write=True),
        workbench_id='synthetic-workbench', knowledge_base_id='synthetic-kb', knowledge_base_tenant_id=77,
        metadata={'can_edit': True}, sources=(), restricted=False, source_delegations=())


def claim_worker(path, barrier, output, refresh):
    engine = make_engine('sqlite:///' + path)
    factory = sessionmaker(engine)
    cache.artifact_session = lambda _: factory()
    barrier.wait(timeout=20)
    try:
        ticket = cache.claim(scope(), cache.digest('same-source-image-and-model'), refresh=refresh)
        output.put((200, ticket.get('fence')))
    except HTTPException as error:
        output.put((error.status_code, None))
    finally:
        engine.dispose()


@pytest.mark.parametrize('mode', ['pristine', 'expired', 'refresh'])
def test_exactly_one_process_claims_a_fingerprint(tmp_path, monkeypatch, mode):
    path = str(tmp_path / 'synthetic-diagram-race.sqlite')
    engine = make_engine('sqlite:///' + path)
    Base.metadata.create_all(engine)
    factory = sessionmaker(engine)
    monkeypatch.setattr(cache, 'artifact_session', lambda _: factory())
    key = cache.digest('same-source-image-and-model')
    if mode in {'expired', 'refresh'}:
        initial = cache.claim(scope(), key)
        if mode == 'refresh':
            cache.publish(scope(), initial, snapshot())
            cache.mark_delivered(scope(), key)
        else:
            with factory() as session:
                row = session.get(SourceMapping, key)
                row.config = {**row.config, 'lease_until': time.time() - 1}
                session.commit()
    context = multiprocessing.get_context('spawn')
    barrier, output = context.Barrier(2), context.Queue()
    workers = [context.Process(target=claim_worker, args=(path, barrier, output, mode == 'refresh')) for _ in range(2)]
    try:
        for worker in workers:
            worker.start()
        results = [output.get(timeout=40) for _ in workers]
        assert sorted(item[0] for item in results) == [200, 409]
        winning_fence = next(item[1] for item in results if item[0] == 200)
        with factory() as session:
            stored = session.get(SourceMapping, key).config
            assert stored['state'] == 'pending'
            assert stored['fence'] == winning_fence
            if mode == 'refresh':
                assert stored['previous_ready']['snapshot'] == snapshot()
    finally:
        for worker in workers:
            if worker.pid is None: continue
            worker.join(timeout=5)
            if worker.is_alive():
                worker.terminate()
                worker.join(timeout=5)
        engine.dispose()
