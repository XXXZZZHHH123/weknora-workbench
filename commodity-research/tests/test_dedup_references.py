"""Reference notices never write; an explicit acceptance creates a revision."""
from contextlib import contextmanager
from dataclasses import replace
from datetime import datetime, timezone
import json
from types import SimpleNamespace
from uuid import uuid4

from fastapi import HTTPException
import pytest
from sqlalchemy import select

from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, artifact_session, current_workbench
from tin.models import SourceMapping
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision
from tin.schemas.chart import ChartDefinition
from tin.web import charts, dedup_references as refs, favorites, restricted_views
from tin.web.research_references import remember_record_scope
from tin.web.workbench_artifacts import record_revision
from tests.chart_fixtures import OTHER, SID, THIRD, definition
from tests.test_chart_api import chart_client
from tests.test_workbench_sources_v2 import WB, source


@pytest.fixture
def context(chart_client, monkeypatch):
    _, state = chart_client
    identity = Identity('first', 'fixture@test', 'Fixture', 17, 'owner')
    scope = WorkbenchScope(identity, WB, '', 0, {'workspace_mode': 'isolated', 'can_edit': True},
        'Bearer fixture', scope_version=2, sources_revision=7, sources=(source(1),))
    actor, marker = current_identity.set(identity), current_workbench.set(scope)
    mapping = {SID: OTHER}
    monkeypatch.setattr(refs, '_replacements', lambda scope, identifiers:
                        {key: value for key, value in mapping.items() if key in identifiers})
    try:
        yield SimpleNamespace(scope=scope, state=state, mapping=mapping)
    finally:
        current_workbench.reset(marker); current_identity.reset(actor)


@contextmanager
def different_scope(context, **changes):
    identity = replace(context.scope.identity, **changes.pop('identity', {}))
    scope = replace(context.scope, identity=identity, **changes)
    actor, marker = current_identity.set(identity), current_workbench.set(scope)
    try:
        yield scope
    finally:
        current_workbench.reset(marker); current_identity.reset(actor)


def rewrite(kind, identifier='', revision=1, **changes):
    return refs.rewrite_references(refs.RewriteInput.model_validate({
        'kind': kind, 'id': identifier, 'revision': revision, 'sources_revision': 7, 'confirmed': True,
        'replacements': {SID: OTHER}, **changes}))


def test_favorites_notice_requires_confirmation_and_keeps_original_revision(context):
    saved = favorites.put_favorites(favorites.FavoritesInput(revision=0, items=[
        favorites.FavoriteItem(kind='indicator', id=SID), favorites.FavoriteItem(kind='indicator', id=OTHER)]))
    notice = refs.reference_notices('favorites')
    assert notice['items'][0]['personal_id'] == SID and notice['items'][0]['default_id'] == OTHER
    assert notice['can_rewrite'] and notice['revision'] == saved['revision'] == 1
    assert [item['id'] for item in favorites.get_favorites()['items']] == [SID, OTHER]
    with pytest.raises(HTTPException) as error:
        rewrite('favorites', confirmed=False)
    assert error.value.status_code == 422
    assert rewrite('favorites')['revision'] == 2
    assert [item['id'] for item in favorites.get_favorites()['items']] == [OTHER]
    with artifact_session(context.scope) as session:
        row = session.scalar(favorites.owned_query(context.scope))
        original = session.get(WorkbenchArtifactRevision, (row.id, 1))
        assert json.loads(original.body)['items'][0]['id'] == SID
        assert session.get(WorkbenchArtifactRevision, (row.id, 2)) is not None
    with pytest.raises(HTTPException) as error:
        rewrite('favorites')
    assert error.value.status_code == 409


@pytest.mark.parametrize('current', [{SID: THIRD}, {}, {SID: OTHER, THIRD: OTHER}])
def test_reviewed_correspondences_must_still_match_before_rewriting(context, current):
    favorites.put_favorites(favorites.FavoritesInput(revision=0, items=[
        favorites.FavoriteItem(kind='indicator', id=SID), favorites.FavoriteItem(kind='indicator', id=THIRD)]))
    reviewed = refs.reference_notices('favorites')
    expected = {item['personal_id']: item['default_id'] for item in reviewed['items']}
    assert expected == {SID: OTHER}
    context.mapping.clear(); context.mapping.update(current)
    with pytest.raises(HTTPException) as error:
        rewrite('favorites', replacements=expected)
    assert error.value.status_code == 409
    unchanged = favorites.get_favorites()
    assert unchanged['revision'] == reviewed['revision'] == 1
    assert [item['id'] for item in unchanged['items']] == [SID, THIRD]
    with artifact_session(context.scope) as session:
        row = session.scalar(favorites.owned_query(context.scope))
        assert session.get(WorkbenchArtifactRevision, (row.id, 2)) is None


def test_chart_direct_and_derived_operands_are_changed_only_after_acceptance(context):
    value = definition(curves=[{'series_id': SID, 'label': '原曲线'},
        {'derived': {'op': 'sub', 'a': SID, 'b': THIRD}, 'label': '差值'}])
    # Both source series units are fixed fixture metadata, not label inference.
    context.state.fields[THIRD]['unit'] = context.state.fields[SID]['unit']
    chart = charts.create_chart(ChartDefinition.model_validate(value))
    notice = refs.reference_notices('chart', chart['id'])
    assert len(notice['items']) == 1
    with artifact_session(context.scope) as session:
        assert charts.decode_definition(charts.load_artifact(session, chart['id'])).curves[0].series_id == SID
    assert rewrite('chart', chart['id'])['revision'] == 2
    with artifact_session(context.scope) as session:
        row = charts.load_artifact(session, chart['id'])
        changed = charts.decode_definition(row)
        assert changed.curves[0].series_id == OTHER and changed.curves[1].derived.a == OTHER
        assert changed.curves[1].derived.b == THIRD
        assert SID in session.get(WorkbenchArtifactRevision, (row.id, 1)).body


def test_shared_core_changes_with_cas_and_keeps_history(context):
    with artifact_session(context.scope) as session:
        key = restricted_views.mapping_key(session, restricted_views.CORE_KEY)
        session.add(SourceMapping(id=key, config={'items': [{'kind': 'indicator', 'id': SID}]}, version=3,
                                  confirmed_by='first', confirmed_at=datetime.now(timezone.utc)))
        session.commit()
    assert refs.reference_notices('core')['revision'] == 3
    assert rewrite('core', revision=3)['revision'] == 4
    with artifact_session(context.scope) as session:
        assert restricted_views.core_items(session) == [{'kind': 'indicator', 'id': OTHER}]
        history = session.scalar(select(WorkbenchArtifact).where(WorkbenchArtifact.module == 'core_focus'))
        assert SID in session.get(WorkbenchArtifactRevision, (history.id, 3)).body
        assert OTHER in session.get(WorkbenchArtifactRevision, (history.id, 4)).body
    with pytest.raises(HTTPException) as error:
        rewrite('core', revision=3)
    assert error.value.status_code == 409
    # A normal edit between dedup acceptances increments the mapping version;
    # the next acceptance must archive that actual config before replacing it.
    from tin.web.chart_data import catalog_fields
    fields = catalog_fields(context.scope, [SID, OTHER])
    with artifact_session(context.scope) as session:
        session.info['knowledge_facts_catalog'] = fields
        restricted_views.save_core_config(session, [{'kind': 'indicator', 'id': SID}])
    assert refs.reference_notices('core')['revision'] == 5
    assert rewrite('core', revision=5)['revision'] == 6
    with artifact_session(context.scope) as session:
        history = session.scalar(select(WorkbenchArtifact).where(WorkbenchArtifact.module == 'core_focus'))
        assert json.loads(session.get(WorkbenchArtifactRevision, (history.id, 5)).body)['items'] == [
            {'kind': 'indicator', 'id': SID}]
        assert json.loads(session.get(WorkbenchArtifactRevision, (history.id, 6)).body)['items'] == [
            {'kind': 'indicator', 'id': OTHER}]


def test_ppt_binding_acceptance_creates_revision_and_preserves_other_bindings(context):
    now, identifier = datetime.now(timezone.utc), str(uuid4())
    body = {'format': 'ppt-template.v1', 'filename': 'fixture.pptx', 'sha256': 'a' * 64,
            'source': {'kind': 'upload'}, 'exports': [],
            'presentation': {'slides': [{'slots': [{'slot_id': 'slot-a', 'replaceable': True},
                                                  {'slot_id': 'slot-b', 'replaceable': True}]}]},
            'bindings': {'slot-a': {'kind': 'series', 'series_id': SID, 'rows': 20}, 'slot-b': {'kind': 'keep'}}}
    with artifact_session(context.scope) as session:
        row = WorkbenchArtifact(id=identifier, module='ppt_templates', actor='first', title='Fixture',
            body=json.dumps(body), status='draft', source_ids=[], source_snapshots=[], revision=1,
            created_at=now, updated_at=now)
        session.add(row); session.flush(); remember_record_scope(session, 'artifact', row.id)
        record_revision(session, row, 'first'); session.commit()
    assert refs.reference_notices('ppt', identifier)['items'][0]['personal_id'] == SID
    assert rewrite('ppt', identifier)['revision'] == 2
    with artifact_session(context.scope) as session:
        row = session.get(WorkbenchArtifact, identifier)
        assert json.loads(row.body)['bindings'] == {'slot-a': {'kind': 'series', 'series_id': OTHER, 'rows': 20},
                                                    'slot-b': {'kind': 'keep'}}
        assert SID in session.get(WorkbenchArtifactRevision, (identifier, 1)).body


def test_permissions_actor_isolation_and_stale_source_decision(context):
    favorites.put_favorites(favorites.FavoritesInput(revision=0, items=[favorites.FavoriteItem(kind='indicator', id=SID)]))
    with different_scope(context, identity={'user_id': 'second'}):
        assert refs.reference_notices('favorites')['items'] == []
    with different_scope(context, identity={'role': 'viewer'}):
        assert refs.reference_notices('favorites')['can_rewrite'] is False
        with pytest.raises(HTTPException) as error:
            rewrite('favorites')
        assert error.value.status_code == 403
    with different_scope(context, metadata={'workspace_mode': 'isolated', 'can_edit': False}):
        assert refs.reference_notices('favorites')['can_rewrite'] is True
        assert refs.reference_notices('core')['can_rewrite'] is False
    with different_scope(context, sources=(source(1, True),)):
        with pytest.raises(HTTPException) as error:
            refs.reference_notices('favorites')
        assert error.value.status_code == 403
    with pytest.raises(HTTPException) as error:
        rewrite('favorites', sources_revision=6)
    assert error.value.status_code == 409
    context.mapping.clear()
    with pytest.raises(HTTPException) as error:
        rewrite('favorites')
    assert error.value.status_code == 409
    for kind in ('judgment', 'strategy'):
        with pytest.raises(HTTPException) as error:
            rewrite(kind, '1')
        assert error.value.status_code == 422


@pytest.mark.parametrize('kind,payload', [
    ('judgment', {'marginal_focus': [{'related_series_ids': [SID]}]}),
    ('judgment', {'whitelist': [SID]}),
    ('strategy', {'citations': [{'kind': 'indicator', 'series_id': SID}]}),
    ('strategy', {'evidence': [{'series_ids': [SID]}]}),
])
def test_research_notices_cover_declared_binding_lists_without_rewriting(context, monkeypatch, kind, payload):
    from tin.web import research_references
    monkeypatch.setattr(research_references, 'resolve_reference', lambda *args:
                        {'payload': payload, 'series_ids': [], 'revision': 4})
    notice = refs.reference_notices(kind, '17')
    assert notice['items'][0]['personal_id'] == SID
    assert notice['can_rewrite'] is False and '仅提示' in notice['reason']
    assert notice['items'][0]['url'].endswith('/sn/indicators/' + OTHER)
