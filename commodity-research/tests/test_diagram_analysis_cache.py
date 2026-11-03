"""Synthetic immutable diagram structures; never open the application database."""
import base64
from copy import deepcopy
from datetime import datetime, timezone
import importlib
from types import SimpleNamespace
import time

from fastapi import HTTPException
import pytest
from sqlalchemy.orm import sessionmaker

from tin.db import Base, make_engine
from tin.models import SourceMapping
from tin.web import diagram_analysis_cache as cache
from tin.web.diagram_draft import compile_graph


@pytest.fixture
def store(tmp_path, monkeypatch):
    engine = make_engine('sqlite:///' + str(tmp_path / 'diagram-snapshot.sqlite'))
    Base.metadata.create_all(engine)
    factory = sessionmaker(engine)
    monkeypatch.setattr(cache, 'artifact_session', lambda scope: factory())
    scope = SimpleNamespace(identity=SimpleNamespace(tenant_id=9, user_id='owner', can_write=True),
        workbench_id='workbench', knowledge_base_id='kb', knowledge_base_tenant_id=9,
        scope_version=2, sources_revision=1, sources=({'id': 'source', 'delegated': False},),
        source_delegations=(), additional_knowledge_bases=(), view_modules=('diagram',),
        restricted=False, metadata={'can_edit': True, 'can_read_source': True})
    yield scope, factory
    engine.dispose()


def key(name):
    return cache.digest(name)


def snapshot(label='Variable'):
    graph = {'title': 'Synthetic structure', 'groups': [
        {'id': 'main', 'label': 'Target', 'parent_id': None, 'role': 'target'}],
        'nodes': [{'id': 'variable', 'label': label, 'group_id': 'main', 'series_id': 'synthetic-series'}],
        'edges': []}
    return {'graph': graph, 'markdown': compile_graph(graph),
        'structure': {'target': 'Synthetic target', 'items': [{'id': 'synthetic-item'}]},
        'metadata': {'candidate_ids': ['synthetic-series'], 'candidate_subset': False,
            'candidate_count': 1, 'model_id': 'model', 'prompt_version': 'protocol', 'repair_attempts': 0}}


def fingerprint(scope, **changes):
    args = {'instructions': 'Complete instructions\n', 'image': b'raw image bytes',
        'source_identity': {'id': 'source', 'sha256': 'original-source-version'},
        'catalog_version': 'version', 'catalog': [
            {'id': 'series-b', 'unit': 'kg'}, {'id': 'series-a', 'unit': 'kg'}],
        'model': {'id': 'model', 'provider': 'vendor', 'parameters': {'secret': 'synthetic-secret'}},
        'protocol': 'diagram-protocol'}
    args.update(changes)
    return cache.identity(scope, **args)


def publish(scope, name='one', label='Variable'):
    ticket = cache.claim(scope, key(name))
    cache.publish(scope, ticket, snapshot(label))
    return ticket


def expire(factory, name):
    with factory() as session:
        row = session.get(SourceMapping, key(name))
        row.config = {**row.config, 'lease_until': time.time() - 1}
        session.commit()


def test_exact_input_and_id_sorted_catalog_are_stable(store):
    scope, _ = store
    original = fingerprint(scope)
    assert fingerprint(scope, catalog=[{'id': 'series-a', 'unit': 'kg'}, {'id': 'series-b', 'unit': 'kg'}]) == original
    assert fingerprint(scope, image='data:image/png;base64,' + base64.b64encode(b'raw image bytes').decode()) == original
    assert len(original) == 64


@pytest.mark.parametrize('changes', [
    {'instructions': 'Complete instructions'}, {'image': b'changed image'},
    {'source_identity': {'id': 'source', 'sha256': 'changed'}}, {'catalog_version': 'changed'},
    {'catalog': [{'id': 'series-b', 'unit': 'lb'}, {'id': 'series-a', 'unit': 'kg'}]},
    {'model': {'id': 'same-model', 'provider': 'changed'}}, {'protocol': 'changed'},
])
def test_changed_inputs_invalidate_reuse(store, changes):
    scope, _ = store
    assert fingerprint(scope, **changes) != fingerprint(scope)


@pytest.mark.parametrize('attribute,value', [('workbench_id', 'other'), ('sources_revision', 2),
    ('knowledge_base_id', 'other'), ('view_modules', ('diagram', 'judgments'))])
def test_scope_changes_invalidate_fingerprint(store, attribute, value):
    scope, _ = store
    other = deepcopy(scope)
    setattr(other, attribute, value)
    assert fingerprint(other) != fingerprint(scope)


def test_users_and_tenants_have_different_identity(store):
    scope, _ = store
    other = deepcopy(scope)
    other.identity.user_id = 'another'
    assert fingerprint(other) != fingerprint(scope)
    other = deepcopy(scope)
    other.identity.tenant_id += 1
    assert fingerprint(other) != fingerprint(scope)


@pytest.mark.parametrize('catalog', [[{'id': 'same'}, {'id': 'same'}], [{'unit': 'kg'}], [None]])
def test_invalid_catalog_is_not_cached(store, catalog):
    with pytest.raises(HTTPException) as failure:
        fingerprint(store[0], catalog=catalog)
    assert failure.value.status_code == 422


def test_success_is_immutable_and_no_images_credentials_or_instructions_are_persisted(store):
    scope, factory = store
    cache_key = fingerprint(scope)
    ticket = cache.claim(scope, cache_key)
    draft = snapshot()
    cache.publish(scope, ticket, draft)
    cache.mark_delivered(scope, cache_key)
    draft['graph']['nodes'][0]['label'] = 'Manual review'
    loaded = cache.claim(scope, cache_key)['snapshot']
    assert loaded['graph']['nodes'][0]['label'] == 'Variable'
    loaded['structure']['target'] = 'Another target'
    assert cache.claim(scope, cache_key)['snapshot']['structure']['target'] == 'Synthetic target'
    with factory() as session:
        raw = str(session.get(SourceMapping, cache_key).config)
    assert 'synthetic-secret' not in raw
    assert 'raw image bytes' not in raw
    assert 'Complete instructions' not in raw
    with pytest.raises(HTTPException):
        cache.publish(scope, ticket, snapshot('Changed'))
    cache.abandon(scope, ticket)
    assert cache.claim(scope, cache_key)['snapshot']['graph']['nodes'][0]['label'] == 'Variable'


def test_failure_removes_only_own_pending_and_retry_gets_new_fence(store):
    scope, factory = store
    ticket = cache.claim(scope, key('failure'))
    with pytest.raises(HTTPException) as error:
        cache.claim(scope, key('failure'))
    assert error.value.status_code == 409
    cache.abandon(scope, ticket)
    assert cache.claim(scope, key('failure'))['fence'] != ticket['fence']


def test_explicit_refresh_success_replaces_previous_graph(store):
    scope, _ = store
    publish(scope)
    cache.prune_after_success(scope, key('one'))
    replacement = cache.claim(scope, key('one'), refresh=True)
    with pytest.raises(HTTPException):
        cache.claim(scope, key('one'))
    cache.publish(scope, replacement, snapshot('Replacement'))
    assert cache.claim(scope, key('one'))['snapshot']['graph']['nodes'][0]['label'] == 'Replacement'


@pytest.mark.parametrize('lease_expired', [False, True])
def test_refresh_failure_or_expiry_restores_previous_success(store, lease_expired):
    scope, factory = store
    publish(scope)
    cache.prune_after_success(scope, key('one'))
    replacement = cache.claim(scope, key('one'), refresh=True)
    if lease_expired:
        expire(factory, 'one')
    else:
        cache.abandon(scope, replacement)
    assert cache.claim(scope, key('one'))['snapshot'] == snapshot()
    with factory() as session:
        row = session.get(SourceMapping, key('one'))
        assert row.config['state'] == 'ready'
        assert row.config['delivery_pending'] is False
        assert 'previous_ready' not in row.config


def test_expired_refresh_writer_cannot_overwrite_or_abandon_replacement(store):
    scope, factory = store
    publish(scope)
    old = cache.claim(scope, key('one'), refresh=True)
    expire(factory, 'one')
    new = cache.claim(scope, key('one'), refresh=True)
    with pytest.raises(HTTPException):
        cache.publish(scope, old, snapshot('Wrong old writer'))
    cache.abandon(scope, old)
    cache.publish(scope, new, snapshot('New writer'))
    assert cache.claim(scope, key('one'))['snapshot']['graph']['nodes'][0]['label'] == 'New writer'


def test_pristine_expired_lease_can_retry_and_old_writer_is_fenced(store):
    scope, factory = store
    old = cache.claim(scope, key('new'))
    expire(factory, 'new')
    new = cache.claim(scope, key('new'))
    with pytest.raises(HTTPException):
        cache.publish(scope, old, snapshot())
    cache.abandon(scope, old)
    cache.publish(scope, new, snapshot('New'))
    assert cache.claim(scope, key('new'))['snapshot']['graph']['nodes'][0]['label'] == 'New'


@pytest.mark.parametrize('mutation', [
    lambda value: value.pop('structure'),
    lambda value: value.update(structure={}),
    lambda value: value.update(markdown='# differs\n'),
    lambda value: value['metadata'].update(candidate_count=2),
    lambda value: value['metadata'].update(candidate_ids=['same', 'same'], candidate_count=2),
    lambda value: value['metadata'].update(candidate_subset='false'),
    lambda value: value['metadata'].update(repair_attempts=True),
])
def test_incomplete_or_inconsistent_graph_is_never_published(store, mutation):
    scope, _ = store
    ticket = cache.claim(scope, key('invalid'))
    value = snapshot()
    mutation(value)
    with pytest.raises(HTTPException) as error:
        cache.publish(scope, ticket, value)
    assert error.value.status_code == 422
    cache.abandon(scope, ticket)
    assert 'fence' in cache.claim(scope, key('invalid'))


def test_corrupted_ready_and_previous_success_are_never_reused(store):
    scope, factory = store
    publish(scope)
    with factory() as session:
        row = session.get(SourceMapping, key('one'))
        corrupted = deepcopy(row.config)
        corrupted['snapshot']['structure']['target'] = 'Tampered'
        row.config = corrupted
        session.commit()
    with pytest.raises(HTTPException) as error:
        cache.claim(scope, key('one'))
    assert error.value.status_code == 409
    with pytest.raises(HTTPException):
        cache.claim(scope, key('one'), refresh=True)


@pytest.mark.parametrize('change', ['user', 'tenant', 'workbench', 'grant'])
def test_exact_key_cannot_cross_identity_or_source_grant(store, change):
    scope, _ = store
    publish(scope)
    other = deepcopy(scope)
    if change == 'user': other.identity.user_id = 'other'
    if change == 'tenant': other.identity.tenant_id += 1
    if change == 'workbench': other.workbench_id = 'other'
    if change == 'grant': other.sources_revision += 1
    for action in [lambda: cache.claim(other, key('one')),
            lambda: cache.prune_after_success(other, key('one'))]:
        with pytest.raises(HTTPException) as error:
            action()
        assert error.value.status_code == 409


@pytest.mark.parametrize('change', ['viewer', 'no-edit', 'restricted', 'delegated', 'hidden-source'])
def test_permissions_apply_to_every_cache_operation(store, change):
    scope, _ = store
    ticket = publish(scope)
    other = deepcopy(scope)
    if change == 'viewer': other.identity.can_write = False
    if change == 'no-edit': other.metadata['can_edit'] = False
    if change == 'restricted': other.restricted = True
    if change == 'delegated': other.sources[0]['delegated'] = True
    if change == 'hidden-source': other.metadata['can_read_source'] = False
    for action in [lambda: fingerprint(other), lambda: cache.claim(other, key('one')),
            lambda: cache.publish(other, ticket, snapshot()), lambda: cache.abandon(other, ticket),
            lambda: cache.prune_after_success(other, key('one'))]:
        with pytest.raises(HTTPException) as error:
            action()
        assert error.value.status_code == 403


def test_latest_delivery_prunes_only_older_delivered_owner_success(store):
    scope, factory = store
    publish(scope, 'older')
    cache.mark_delivered(scope, key('older'))
    other = deepcopy(scope)
    other.identity.user_id = 'other'
    publish(other, 'other')
    cache.mark_delivered(other, key('other'))
    pending = cache.claim(scope, key('pending'))
    publish(scope, 'undelivered')
    publish(scope, 'latest')
    cache.mark_delivered(scope, key('latest'))
    with factory() as session:
        assert session.get(SourceMapping, key('older')) is None
        assert session.get(SourceMapping, key('other')) is not None
        assert session.get(SourceMapping, pending['key']).config['state'] == 'pending'
        assert session.get(SourceMapping, key('undelivered')).config['delivery_pending'] is True
    assert cache.claim(scope, key('latest'))['snapshot'] == snapshot()


def test_an_old_request_delivering_late_cannot_evict_newer_delivered_snapshot(store):
    scope, factory = store
    old = cache.claim(scope, key('old'))
    publish(scope, 'new')
    cache.mark_delivered(scope, key('new'))
    cache.publish(scope, old, snapshot('Late'))
    cache.mark_delivered(scope, key('old'))
    with factory() as session:
        assert session.get(SourceMapping, key('old')) is None
        assert session.get(SourceMapping, key('new')) is not None


def test_saved_business_records_are_outside_cache_cleanup(store):
    scope, factory = store
    with factory() as session:
        session.add(SourceMapping(id=key('business'), config={'kind': 'saved-business'},
            version=1, confirmed_by=scope.identity.user_id, confirmed_at=datetime.now(timezone.utc)))
        session.commit()
    publish(scope, 'old')
    cache.mark_delivered(scope, key('old'))
    publish(scope, 'new')
    cache.mark_delivered(scope, key('new'))
    with factory() as session:
        assert session.get(SourceMapping, key('business')).config == {'kind': 'saved-business'}


def test_result_survives_module_reload_and_database_reconnection(store, monkeypatch):
    scope, factory = store
    publish(scope)
    reloaded = importlib.reload(cache)
    monkeypatch.setattr(reloaded, 'artifact_session', lambda scope: factory())
    assert reloaded.claim(scope, key('one'))['snapshot'] == snapshot()


def test_failure_after_publish_restores_last_delivered_success(store):
    scope, _ = store
    publish(scope)
    cache.mark_delivered(scope, key('one'))
    refresh = cache.claim(scope, key('one'), refresh=True)
    cache.publish(scope, refresh, snapshot('Not delivered'))
    cache.abandon(scope, refresh)
    assert cache.claim(scope, key('one'))['snapshot'] == snapshot()


def test_expired_undelivered_refresh_restores_success_and_pristine_orphan_is_pruned(store):
    scope, factory = store
    publish(scope)
    cache.mark_delivered(scope, key('one'))
    refresh = cache.claim(scope, key('one'), refresh=True)
    cache.publish(scope, refresh, snapshot('Undelivered'))
    publish(scope, 'orphan')
    expire(factory, 'one')
    expire(factory, 'orphan')
    assert cache.claim(scope, key('one'))['snapshot'] == snapshot()
    with factory() as session:
        assert session.get(SourceMapping, key('orphan')) is None


def test_expired_old_grant_does_not_block_fresh_source_generation(store):
    scope, factory = store
    cache.claim(scope, key('old-grant'))
    expire(factory, 'old-grant')
    new_scope = deepcopy(scope)
    new_scope.sources_revision += 1
    assert 'fence' in cache.claim(new_scope, key('new-grant'))
    with factory() as session:
        assert session.get(SourceMapping, key('old-grant')) is None


def test_safe_model_digest_and_legacy_verified_scope_are_accepted(store):
    scope, _ = store
    assert fingerprint(scope, model='a' * 64)
    del scope.metadata['can_edit']
    del scope.identity.can_write
    assert fingerprint(scope, model='a' * 64)


def test_same_id_delete_recreate_cannot_be_overwritten_by_a_stale_row_version(store):
    scope, factory = store
    ticket = cache.claim(scope, key('aba'))
    with factory() as stale_session:
        stale = stale_session.get(SourceMapping, key('aba'))
        assert stale.version == 1
        cache.abandon(scope, ticket)
        replacement = cache.claim(scope, key('aba'))
        assert replacement['fence'] != ticket['fence']
        with pytest.raises(HTTPException) as error:
            cache._write(stale_session, stale, {**stale.config, 'state': 'ready'})
        assert error.value.status_code == 409
    with factory() as session:
        assert session.get(SourceMapping, key('aba')).config['fence'] == replacement['fence']


@pytest.mark.parametrize('value,status', [('x' * (cache.MAX_BYTES + 1), 413), (float('nan'), 422)])
def test_nonfinite_or_oversized_snapshots_never_replace_success(store, value, status):
    scope, _ = store
    publish(scope)
    cache.mark_delivered(scope, key('one'))
    ticket = cache.claim(scope, key('one'), refresh=True)
    draft = snapshot()
    draft['metadata']['extra'] = value
    with pytest.raises(HTTPException) as error:
        cache.publish(scope, ticket, draft)
    assert error.value.status_code == status
    cache.abandon(scope, ticket)
    assert cache.claim(scope, key('one'))['snapshot'] == snapshot()
