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


def test_exact_remap_rewrites_and_restores_workbench_bindings(context):
    from datetime import date
    from tin.judgments.service import save_version
    from tin.models import Judgment, JudgmentSeriesRef
    from tin.schemas.judgment import JudgmentIn
    from tin.web import monitors, report_drafts, workbench_remaps as remaps
    from tin.web.research_dimensions import DIMENSIONS_KEY, default_dimensions

    scope = context.scope
    chart = charts.create_chart(ChartDefinition.model_validate(definition(curves=[
        {'series_id': SID, 'label': SID},
        {'derived': {'op': 'sub', 'a': SID, 'b': OTHER}, 'label': '差值'},
    ])))
    favorites.put_favorites(favorites.FavoritesInput(revision=0, items=[
        favorites.FavoriteItem(kind='indicator', id=SID), favorites.FavoriteItem(kind='indicator', id=OTHER)]))
    now = datetime.now(timezone.utc)
    core = {'items': [{'kind': 'indicator', 'id': SID}]}
    dimensions = default_dimensions()
    dimensions[0]['sections'] = [{'id': 's-fixture', 'label': '绑定', 'items': [
        {'kind': 'indicator', 'id': SID}, {'kind': 'chart', 'id': chart['id'], 'revision': 1}]}]
    with artifact_session(scope) as session:
        session.info.update(knowledge_facts=True, knowledge_facts_catalog={SID: {}, OTHER: {}})
        judgment = save_version(session, JudgmentIn.model_validate({
            'variety': 'TEST', 'author': 'first', 'written_at': date(2026, 1, 2), 'contradiction': {},
            'whitelist': [SID, OTHER],
        }), actor='first')
        reference = {'kind': 'judgment', 'id': str(judgment.id), 'revision': judgment.version, 'section': 'research'}
        report = {'format': report_drafts.FORMAT, 'period': 'weekly', 'date': '2026-01-09', 'title': '周报',
                  'workbench_id': scope.workbench_id, 'source_revision': 'a' * 64,
                  'figures': [{'series_id': SID, 'caption': SID}], 'references': [reference],
                  'reference_snapshots': report_drafts.freeze_references(session, scope, [reference]),
                  'figure_conclusions': {'indicator:' + SID: '记录原值'}}
        report_row = monitors._new_artifact(scope, 'briefs', '周报', report)
        session.add(report_row)
        remember_record_scope(session, 'artifact', report_row.id)
        monitor = {'workbench_id': scope.workbench_id, 'logic': 'all', 'rules': [{'series_id': SID}],
                   'sentence': SID, 'enabled': True}
        monitor_row = monitors._new_artifact(scope, monitors.MODULE, '监测', monitor)
        alert = {'monitor_id': monitor_row.id, 'workbench_id': scope.workbench_id,
                 'condition_key': monitors._condition_key(monitor), 'monitor_revision': 1,
                 'triggered': True, 'rule_states': [{'series_id': SID}]}
        alert_row = monitors._new_artifact(scope, monitors.STATE_MODULE, '监测状态', alert,
                                           identifier=monitors._state_id(scope, monitor_row.id))
        session.add_all([monitor_row, alert_row])
        for name, config in ((restricted_views.CORE_KEY, core), (DIMENSIONS_KEY, {'dimensions': dimensions})):
            session.add(SourceMapping(id=restricted_views.mapping_key(session, name), config=config,
                                      version=1, confirmed_by=scope.identity.user_id, confirmed_at=now))
        session.commit()
        report_id, monitor_id, judgment_id = report_row.id, monitor_row.id, judgment.id

    with different_scope(context, identity={'user_id': 'second'}):
        favorites.put_favorites(favorites.FavoritesInput(revision=0, items=[
            favorites.FavoriteItem(kind='indicator', id=SID)]))
    item = {'id': 'exact-pair', 'revision': 2, 'personal': {'series_id': SID}, 'default': {
        'series_id': OTHER, 'metric': '短名称', 'knowledge_base_id': scope.sources[0]['knowledge_base_id'],
        'knowledge_base_tenant_id': 17, 'source_knowledge_id': 'fixture-document'}}
    assert remaps.rewrite_workbenches([(scope, [item])]) == {item['id']: [scope.workbench_id]}
    assert remaps.rewrite_workbenches([(scope, [item])]) == {item['id']: [scope.workbench_id]}, 'retry is idempotent'
    with artifact_session(scope) as session:
        row = charts.load_artifact(session, chart['id'])
        current = charts.decode_definition(row)
        assert row.revision == 2 and current.curves[0].series_id == OTHER
        assert current.curves[0].descriptor.metric == '短名称' and current.curves[0].label == SID
        assert current.curves[1].derived.a == OTHER
        assert SID in session.get(WorkbenchArtifactRevision, (row.id, 1)).body
        assert restricted_views.core_items(session) == [{'kind': 'indicator', 'id': OTHER}]
        valid = remaps._artifact_body(session.get(WorkbenchArtifact, report_id))
        assert valid['figures'] == [{'series_id': OTHER, 'caption': SID}]
        assert valid['figure_conclusions'] == {'indicator:' + OTHER: '记录原值'}
        assert valid['reference_snapshots'][0]['series_ids'] == [OTHER]
        assert not report_drafts.serialize_report(session, scope, session.get(WorkbenchArtifact, report_id),
                                                   strict=True)['unavailable_references']
        bound = session.get(SourceMapping, restricted_views.mapping_key(session, DIMENSIONS_KEY)).config
        assert bound['dimensions'][0]['sections'][0]['items'] == [
            {'kind': 'indicator', 'id': OTHER}, {'kind': 'chart', 'id': chart['id'], 'revision': 2}]
        judgment = session.get(Judgment, judgment_id)
        assert judgment.version == 1 and judgment.whitelist == [OTHER]
        assert set(session.scalars(select(JudgmentSeriesRef.series_id).where(
            JudgmentSeriesRef.judgment_id == judgment_id))) == {OTHER}
        body = monitors._body(session.get(WorkbenchArtifact, monitor_id))
        assert body['rules'][0]['series_id'] == OTHER and body['sentence'] == SID
        assert monitors._state(session, scope, monitor_id)['condition_key'] == monitors._condition_key(body)
    assert [entry['id'] for entry in favorites.get_favorites()['items']] == [OTHER]
    with different_scope(context, identity={'user_id': 'second'}):
        assert [entry['id'] for entry in favorites.get_favorites()['items']] == [SID]

    favorites.put_favorites(favorites.FavoritesInput(revision=2, items=[
        favorites.FavoriteItem(kind='indicator', id=OTHER), favorites.FavoriteItem(kind='indicator', id=THIRD)]))
    with pytest.raises(HTTPException, match='绑定后来又被编辑'):
        remaps.rewrite_workbenches([(scope, [item])], undo=True)
    assert [entry['id'] for entry in favorites.get_favorites()['items']] == [OTHER, THIRD]
    favorites.put_favorites(favorites.FavoritesInput(revision=3, items=[favorites.FavoriteItem(kind='indicator', id=OTHER)]))
    remaps.rewrite_workbenches([(scope, [item])], undo=True)
    with artifact_session(scope) as session:
        row = charts.load_artifact(session, chart['id'])
        assert row.revision == 3 and charts.decode_definition(row).curves[0].series_id == SID
        assert restricted_views.core_items(session) == core['items']
        assert session.get(Judgment, judgment_id).whitelist == [SID, OTHER]
        assert remaps._artifact_body(session.get(WorkbenchArtifact, report_id)) == report
        assert monitors._body(session.get(WorkbenchArtifact, monitor_id)) == monitor
        assert monitors._state(session, scope, monitor_id)['monitor_revision'] == 3
    assert [entry['id'] for entry in favorites.get_favorites()['items']] == [SID, OTHER]


def test_remap_resolves_other_workbenches_and_fences_undo(context, monkeypatch):
    from asyncio import run
    from tin.web import workbench_remaps as remaps
    original = context.scope
    second_id = '10000000-0000-4000-8000-000000000002'
    third_id = '10000000-0000-4000-8000-000000000003'
    directly_bound = replace(original, workbench_id=second_id, sources=(
        {**original.sources[0], 'knowledge': [{'id': 'fixture-document'}]},))
    unbound = replace(original, workbench_id=third_id, sources=(source(2),))

    async def platform(scope, path):
        assert scope == original and path == '/api/v1/workspaces/research'
        return {'data': [{'id': identifier, 'can_edit': True}
                         for identifier in (original.workbench_id, second_id, third_id)]}

    async def resolve(identity, identifier, authorization):
        assert (identity, authorization) == (original.identity, original.authorization)
        return {second_id: directly_bound, third_id: unbound}[identifier]

    monkeypatch.setattr(remaps, 'go_request', platform)
    monkeypatch.setattr(remaps, 'resolve_workbench', resolve)
    item = {'id': 'exact', 'revision': 2, 'default': {'series_id': OTHER, 'source_knowledge_id': 'fixture-document',
             'knowledge_base_id': original.sources[0]['knowledge_base_id'], 'knowledge_base_tenant_id': 17}}
    assert run(remaps.authorized_workbenches(original, [item])) == [(original, [item]), (directly_bound, [item])]
    item['workbench_ids'] = ['10000000-0000-4000-8000-000000000004']
    with pytest.raises(HTTPException, match='恢复相应工作台和来源权限'):
        run(remaps.authorized_workbenches(original, [item], undo=True))
