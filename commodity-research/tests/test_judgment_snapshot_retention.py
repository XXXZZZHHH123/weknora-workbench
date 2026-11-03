"""Only the latest delivered temporary interpretation is retained per owner and workbench."""
from copy import deepcopy
from datetime import datetime, timezone
import time

from tests.test_judgment_analysis_snapshot import store
from tin.models import SourceMapping
from tin.web import judgment_analysis_cache as cache
from tin.web import judgment_generation as generation


def ready(scope, key, value):
    ticket = cache.claim(scope, key)
    cache.publish(scope, ticket, {'document': value})
    return ticket


def test_success_replaces_older_snapshots_without_affecting_another_owner(store):
    scope, factory = store
    ready(scope, 'first', {'name': 'first'})
    cache.prune_snapshots_after_success(scope, 'first')
    other = deepcopy(scope)
    other.identity.user_id = 'another-owner'
    ready(other, 'other', {'name': 'other'})
    cache.prune_snapshots_after_success(other, 'other')
    ready(scope, 'second', {'name': 'second'})
    cache.prune_snapshots_after_success(scope, 'second')
    with factory() as session:
        assert session.get(SourceMapping, 'first') is None
        assert session.get(SourceMapping, 'other') is not None
    assert cache.claim(scope, 'second')['snapshot']['document'] == {'name': 'second'}


def test_pending_or_undelivered_work_does_not_destroy_the_previous_usable_result(store):
    scope, factory = store
    ready(scope, 'first', {'name': 'first'})
    cache.prune_snapshots_after_success(scope, 'first')
    failed = cache.claim(scope, 'failed')
    cache.abandon(scope, failed)
    ready(scope, 'not-delivered', {'name': 'not-delivered'})
    cache.prune_snapshots_after_success(scope, 'first')
    assert cache.claim(scope, 'first')['snapshot']['document'] == {'name': 'first'}
    assert cache.claim(scope, 'not-delivered')['snapshot']['document'] == {'name': 'not-delivered'}
    pending = cache.claim(scope, 'pending')
    cache.prune_snapshots_after_success(scope, 'not-delivered')
    with factory() as session:
        assert session.get(SourceMapping, 'first') is None
        assert session.get(SourceMapping, pending['key']).config['state'] == 'pending'


def test_partial_delivered_preview_replaces_older_temporary_snapshots_only(store):
    scope, factory = store
    ready(scope, 'first', {'name': 'first'})
    cache.prune_snapshots_after_success(scope, 'first')
    with factory() as session:
        session.add(SourceMapping(id='saved-business', config={'kind': 'saved-research'},
            version=1, confirmed_by=scope.identity.user_id, confirmed_at=datetime.now(timezone.utc)))
        session.commit()
    cache.prune_snapshots_after_success(scope, None)
    with factory() as session:
        assert session.get(SourceMapping, 'first') is None
        assert session.get(SourceMapping, 'saved-business') is not None


def test_abandoned_expired_publications_are_removed_on_the_next_analysis(store):
    scope, factory = store
    ready(scope, 'abandoned', {'name': 'abandoned'})
    with factory() as session:
        row = session.get(SourceMapping, 'abandoned')
        row.config = {**row.config, 'lease_until': time.time() - 1}
        session.commit()
    cache.claim(scope, 'new')
    with factory() as session:
        assert session.get(SourceMapping, 'abandoned') is None


def test_cached_result_also_commits_expired_orphan_cleanup(store):
    scope, factory = store
    ready(scope, 'delivered', {'name': 'delivered'})
    cache.prune_snapshots_after_success(scope, 'delivered')
    ready(scope, 'orphan', {'name': 'orphan'})
    with factory() as session:
        row = session.get(SourceMapping, 'orphan')
        row.config = {**row.config, 'lease_until': time.time() - 1}
        session.commit()
    assert cache.claim(scope, 'delivered')['snapshot']['document'] == {'name': 'delivered'}
    with factory() as session:
        assert session.get(SourceMapping, 'orphan') is None


def test_in_memory_previews_keep_only_the_latest_for_the_same_owner_and_workbench(store):
    scope, _ = store
    previews = generation.PreviewStore()
    old = previews.put(scope, {'name': 'old'})
    other = deepcopy(scope)
    other.workbench_id = 'other-workbench'
    retained = previews.put(other, {'name': 'other'})
    new = previews.put(scope, {'name': 'new'})
    assert old not in previews.items
    assert previews.get(scope, new)['name'] == 'new'
    assert previews.get(other, retained)['name'] == 'other'
