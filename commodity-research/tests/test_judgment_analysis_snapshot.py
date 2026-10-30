"""Synthetic immutable analyses, separate from mutable confirmation drafts."""
from copy import deepcopy
from types import SimpleNamespace
import time

from fastapi import HTTPException
import pytest
from sqlalchemy.orm import sessionmaker

from tin.db import Base, make_engine
from tin.models import SourceMapping
from tin.web import judgment_generation as generation
from tin.web import judgment_analysis_cache as cache


@pytest.fixture
def store(tmp_path, monkeypatch):
    engine = make_engine('sqlite:///' + str(tmp_path / 'analyses.db'))
    Base.metadata.create_all(engine)
    factory = sessionmaker(engine)
    monkeypatch.setattr(generation, 'result_session', lambda scope: factory())
    scope = SimpleNamespace(identity=SimpleNamespace(tenant_id=9, user_id='owner'),
        workbench_id='workbench', knowledge_base_id='kb', knowledge_base_tenant_id=9,
        additional_knowledge_bases=(), source_delegations=())
    yield scope, factory
    engine.dispose()


def test_snapshot_is_immutable_and_new_reader_cannot_edit_it(store):
    scope, factory = store
    ticket = cache.claim(scope, 'key')
    snapshot = {'document': {'unresolved': []}, 'candidate': {'untouched': True}}
    cache.publish(scope, ticket, snapshot)
    snapshot['document']['unresolved'].append('manual decision')
    loaded = cache.claim(scope, 'key')['snapshot']
    assert loaded['document']['unresolved'] == []
    loaded['candidate']['untouched'] = False
    assert cache.claim(scope, 'key')['snapshot']['candidate']['untouched'] is True
    with pytest.raises(HTTPException) as error:
        cache.publish(scope, ticket, {'changed': True})
    assert error.value.status_code == 409
    cache.abandon(scope, ticket)
    assert cache.claim(scope, 'key')['snapshot']['document']['unresolved'] == []


def test_pending_claim_blocks_duplicate_work_and_failure_can_retry(store):
    scope, _ = store
    ticket = cache.claim(scope, 'key')
    with pytest.raises(HTTPException) as error:
        cache.claim(scope, 'key')
    assert error.value.status_code == 409
    cache.abandon(scope, ticket)
    assert cache.claim(scope, 'key')['fence'] != ticket['fence']


def test_expired_writer_cannot_publish_or_abandon_replacement(store):
    scope, factory = store
    old = cache.claim(scope, 'key')
    with factory() as session:
        row = session.get(SourceMapping, 'key')
        row.config = {**row.config, 'lease_until': time.time() - 1}
        session.commit()
    new = cache.claim(scope, 'key')
    with pytest.raises(HTTPException) as error:
        cache.publish(scope, old, {'wrong': True})
    assert error.value.status_code == 409
    cache.abandon(scope, old)
    cache.publish(scope, new, {'right': True})
    assert cache.claim(scope, 'key')['snapshot'] == {'right': True}


def test_owned_snapshot_and_integrity_are_rechecked(store):
    scope, factory = store
    ticket = cache.claim(scope, 'key')
    cache.publish(scope, ticket, {'document': {}})
    other = deepcopy(scope)
    other.identity.user_id = 'other'
    with pytest.raises(HTTPException):
        cache.claim(other, 'key')
    with factory() as session:
        row = session.get(SourceMapping, 'key')
        row.config = {**row.config, 'snapshot': {'corrupted': True}}
        session.commit()
    with pytest.raises(HTTPException) as error:
        cache.claim(scope, 'key')
    assert error.value.status_code == 409


def test_analysis_identity_ignores_review_titles_but_preserves_sources(store):
    scope, _ = store
    body = SimpleNamespace(text='Full original text', base_version=0, source_name='a.md', panel_title='A')
    before = {'variety': 'UNIT', 'fact_version': 'v1', 'base_version': 0,
        'catalog': [{'id': 'one', 'name': 'Quantity', 'unit': 'kg'}]}
    key = cache.identity(scope, body, before, 'model-revision', 'protocol')
    body.base_version, body.source_name, body.panel_title = 12, 'b.md', 'B'
    before['base_version'] = 12
    assert cache.identity(scope, body, before, 'model-revision', 'protocol') == key
    assert cache.identity(scope, body, before, 'changed-model', 'protocol') != key
    assert cache.identity(scope, body, before, 'model-revision', 'changed-protocol') != key
    before['catalog'][0]['unit'] = 'lb'
    assert cache.identity(scope, body, before, 'model-revision', 'protocol') != key
    body.text += '\n'
    assert cache.identity(scope, body, before, 'model-revision', 'protocol') != key
