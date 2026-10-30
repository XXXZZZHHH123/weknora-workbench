"""Explicit owner-only judgment deletion preserves records and version lineage."""
from dataclasses import replace
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import importlib
import json
from uuid import uuid4

from fastapi import FastAPI
from fastapi.testclient import TestClient
import pytest
from sqlalchemy import select
from sqlalchemy.orm import Session

from tin.config import settings
from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, artifact_session, current_workbench
from tin.judgments.service import current, latest, save_version, to_payload
from tin.models import AuditLog, Judgment, Strategy
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision
from tests.test_judgment_panels import seed
from tests.test_structured_judgment_import import payload
from tin.schemas.judgment import JudgmentIn

WB = '5add1691-47e2-4c0a-9815-eef36d567461'
OTHER = 'c05d36f1-9866-40f7-8327-fbfa80757fd0'
BASE = '/api/sn/judgment'


@pytest.fixture
def deletion(tmp_path, monkeypatch):
    lifecycle = importlib.import_module('tin.web.judgment_lifecycle')
    from tin.web.judgment_generation import router as generation_router
    from tin.web.judgment_panels import router as panel_router
    from tin.web.research_references import router as reference_router
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://fixture.invalid')
    state = {}
    app = FastAPI()
    for router in (panel_router, generation_router, lifecycle.router, reference_router):
        app.include_router(router)

    @app.middleware('http')
    async def scoped(request, call_next):
        tenant = int(request.headers.get('x-tenant', '17'))
        actor = Identity(request.headers.get('x-actor', 'first'), '', 'Fixture', tenant,
            request.headers.get('x-role', 'owner'))
        direct = {'id': 'fixture-source', 'source_type': 'knowledge_base', 'source_id': 'fixture-kb',
            'knowledge_base_id': 'fixture-kb', 'tenant_id': tenant, 'permission': 'editor',
            'kind': 'primary', 'delegated': False, 'view_modules': ['judgments']}
        scope = WorkbenchScope(actor, request.headers.get('x-workbench', WB), 'fixture-kb', tenant,
            {'workspace_mode': request.headers.get('x-mode', 'isolated'),
             'can_edit': request.headers.get('x-edit', 'true') == 'true',
             'can_read_source': request.headers.get('x-restricted') != 'true'},
            'Bearer fixture', scope_version=2, sources_revision=1, sources=(direct,))
        if request.headers.get('x-no-module'):
            scope = replace(scope, view_modules=('home',))
        state['scope'] = scope
        board, user = current_workbench.set(scope), current_identity.set(actor)
        try:
            return await call_next(request)
        finally:
            current_identity.reset(user)
            current_workbench.reset(board)

    with TestClient(app) as client:
        assert client.get(BASE + '/panels').status_code == 200
        yield client, state


def erase(client, identifier, expected_version, *, revision=0, headers=None, **extra):
    return client.request('DELETE', BASE + '/records/' + str(identifier), headers=headers,
        json={'version': expected_version, 'revision': revision, 'confirmed': True, **extra})


def test_delete_keeps_the_actual_judgment_snapshot_and_audit_but_hides_ordinary_views(deletion):
    from tin.web.judgment_panels import panel_identifier
    client, state = deletion
    identifier, version = seed(state)
    scope = state['scope']
    with artifact_session(scope) as session:
        before = to_payload(session.get(Judgment, identifier)).model_dump(mode='json')
    response = erase(client, identifier, version)
    assert response.status_code == 200, response.text
    assert response.json()['deleted'] is True and response.json()['id'] == identifier
    assert response.json()['version'] == version and response.json()['revision'] == 1
    with artifact_session(scope) as session:
        original = session.get(Judgment, identifier)
        assert original is not None and original.status == '已删除'
        assert to_payload(original).model_dump(mode='json') == before
        marker = session.get(WorkbenchArtifact, panel_identifier(scope, identifier))
        assert marker is not None and marker.actor == scope.identity.user_id
        assert marker.revision == 1 and marker.status == 'deleted'
        retained = session.get(WorkbenchArtifactRevision, (marker.id, 1))
        assert retained is not None and retained.status == marker.status
        audits = list(session.scalars(select(AuditLog).where(AuditLog.actor == scope.identity.user_id)))
        assert any(row.detail.get('judgment_id') == identifier and row.detail.get('version') == version
                   for row in audits), 'Deletion records the actual owner and immutable judgment version'
    assert client.get(BASE + '/panels').json()['items'] == []
    managed = client.get(BASE + '/manage?variety=XY').json()
    assert managed['current'] is None and managed['versions'] == []
    assert managed['latest']['version'] == version, 'The CAS counter does not forget a deleted version'
    assert client.get(BASE + '/versions/' + str(version) + '?variety=XY').status_code == 404
    assert client.get('/api/sn/research-references?kind=judgment').json()['items'] == []
    assert client.get('/api/sn/research-references/judgment/' + str(identifier),
        params={'revision': version}).status_code == 404
    assert erase(client, identifier, version).status_code in {404, 409}


def test_named_panel_revision_is_required_and_snapshots_survive_delete(deletion):
    client, state = deletion
    identifier, version = seed(state)
    created = client.post(BASE + '/panels', json={'judgment_id': identifier,
        'version': version, 'title': 'A retained title'}).json()
    renamed = client.patch(BASE + '/panels/' + created['id'],
        json={'title': 'A reviewed title', 'revision': 1})
    assert renamed.status_code == 200
    assert erase(client, identifier, version, revision=1).status_code == 409
    managed = client.get(BASE + '/manage?variety=XY').json()
    assert managed['current']['delete_revision'] == 2
    assert managed['versions'][0]['delete_revision'] == 2
    assert client.get(BASE + '/versions/' + str(version) + '?variety=XY').json()['delete_revision'] == 2
    deleted = erase(client, identifier, version, revision=2)
    assert deleted.status_code == 200, deleted.text
    with artifact_session(state['scope']) as session:
        history = list(session.scalars(select(WorkbenchArtifactRevision).where(
            WorkbenchArtifactRevision.artifact_id == created['id']).order_by(WorkbenchArtifactRevision.revision)))
        assert [row.revision for row in history] == [1, 2, 3]
        assert [row.title for row in history[:2]] == ['A retained title', 'A reviewed title']
        assert history[-1].status == 'deleted'
    assert client.patch(BASE + '/panels/' + created['id'],
        json={'title': 'Cannot reopen a deletion', 'revision': 3}).status_code == 404


@pytest.mark.parametrize('headers,code', [
    ({'x-actor': 'second'}, 404), ({'x-workbench': OTHER}, 404), ({'x-tenant': '18'}, 404),
    ({'x-edit': 'false'}, 403), ({'x-role': 'viewer'}, 403),
    ({'x-restricted': 'true'}, 403), ({'x-no-module': 'true'}, 403),
])
def test_delete_cannot_cross_owner_storage_or_edit_grants(deletion, headers, code):
    client, state = deletion
    identifier, version = seed(state)
    owned_scope = state['scope']
    response = erase(client, identifier, version, headers=headers)
    assert response.status_code == code, response.text
    with artifact_session(owned_scope) as session:
        assert session.get(Judgment, identifier).status == '草稿'
        assert list(session.scalars(select(WorkbenchArtifact))) == []


@pytest.mark.parametrize('changes', [
    {'confirmed': False}, {'confirmed': 'true'}, {'confirmed': 1},
    {'version': True}, {'version': '1'}, {'revision': -1}, {'revision': True},
])
def test_delete_needs_strict_confirmation_and_explicit_versions(deletion, changes):
    client, state = deletion
    identifier, version = seed(state)
    response = erase(client, identifier, version, **changes)
    assert response.status_code == 422, response.text
    with artifact_session(state['scope']) as session:
        assert session.get(Judgment, identifier).status == '草稿'


def test_wrong_version_or_unexpected_panel_revision_has_no_partial_mutation(deletion):
    client, state = deletion
    identifier, version = seed(state)
    assert erase(client, identifier, version + 1).status_code == 409
    assert erase(client, identifier, version, revision=1).status_code == 409
    with artifact_session(state['scope']) as session:
        assert session.get(Judgment, identifier).status == '草稿'
        assert list(session.scalars(select(WorkbenchArtifact))) == []


def test_two_delete_requests_can_only_publish_one_tombstone_and_audit(deletion):
    client, state = deletion
    identifier, version = seed(state)
    owned_scope = state['scope']
    with ThreadPoolExecutor(max_workers=2) as workers:
        replies = list(workers.map(lambda _: erase(client, identifier, version), range(2)))
    assert sum(reply.status_code == 200 for reply in replies) == 1
    assert all(reply.status_code in {200, 404, 409} for reply in replies)
    with artifact_session(owned_scope) as session:
        assert session.get(Judgment, identifier).status == '已删除'
        markers = list(session.scalars(select(WorkbenchArtifact)))
        assert len(markers) == 1 and markers[0].revision == 1
        audits = list(session.scalars(select(AuditLog).where(AuditLog.action == '研判删除')))
        assert len(audits) == 1 and audits[0].target_id == str(identifier)


def test_legacy_storage_ownership_proof_is_checked_before_deleting_another_workbench(deletion):
    client, state = deletion
    assert client.get(BASE + '/panels', headers={'x-mode': 'legacy'}).status_code == 200
    own_id, own_version = seed(state)
    original_scope = state['scope']
    assert client.get(BASE + '/panels', headers={'x-mode': 'legacy', 'x-workbench': OTHER}).status_code == 200
    foreign_id, foreign_version = seed(state)
    assert erase(client, foreign_id, foreign_version, headers={'x-mode': 'legacy'}).status_code == 404
    assert erase(client, own_id, own_version, headers={'x-mode': 'legacy'}).status_code == 200
    with artifact_session(original_scope) as session:
        assert session.get(Judgment, own_id).status == '已删除'
        assert session.get(Judgment, foreign_id).status == '草稿'


@pytest.mark.parametrize('body', [{'version': 1, 'revision': 0},
                                  {'confirmed': True, 'revision': 0},
                                  {'confirmed': True, 'version': 1}])
def test_delete_does_not_default_omitted_confirmation_or_concurrency_fields(deletion, body):
    client, state = deletion
    identifier, _ = seed(state)
    response = client.request('DELETE', BASE + '/records/' + str(identifier), json=body)
    assert response.status_code == 422
    with artifact_session(state['scope']) as session:
        assert session.get(Judgment, identifier).status == '草稿'


def test_deleting_the_active_record_does_not_promote_archived_history_or_reuse_its_version(deletion):
    client, state = deletion
    old, _ = seed(state)
    with artifact_session(state['scope']) as session:
        session.get(Judgment, old).status = '已归档'
        session.commit()
    active, version = seed(state, active=True)
    assert erase(client, active, version).status_code == 200
    managed = client.get(BASE + '/manage?variety=XY').json()
    assert managed['current'] is None, 'Opening management does not silently restore an archived selection'
    assert [row['id'] for row in managed['versions']] == [old]
    assert managed['latest']['version'] == version
    historical = client.get(BASE + '/manage', params={'variety': 'XY', 'judgment_id': old})
    assert historical.status_code == 200 and historical.json()['current']['id'] == old
    with artifact_session(state['scope']) as session:
        assert current(session, 'XY', scope=state['scope']) is None
        assert session.get(Judgment, old).status == '已归档'
        assert latest(session, 'XY').version == version
        document = JudgmentIn.model_validate(payload())
        session.info.update(knowledge_facts=True,
            knowledge_facts_catalog={sid: {'unit': '吨'} for sid in document.whitelist})
        saved = save_version(session, document, actor='first', expected_base_version=version)
        session.commit()
        assert saved.version == version + 1 and saved.status == '草稿'
        assert current(session, 'XY', scope=state['scope']).id == saved.id


def add_artifact_reference(scope, identifier, version, *, module='strategies', historical=False):
    from tin.web.research_references import remember_record_scope
    from tin.web.report_drafts import ReportDraft, freeze_references, report_id
    from tests.test_research_strategy_cards import entry
    now = datetime.now(timezone.utc)
    reference = {'kind': 'judgment', 'id': str(identifier), 'revision': version}
    with artifact_session(scope) as session:
        if module == 'strategies':
            contents = {**entry(source_ids=[])['card'], 'judgment_reference': reference}
            plain = entry(source_ids=[])['card']
            artifact_id = str(uuid4())
        else:
            report = ReportDraft(period='daily', date='2024-03-01', title='An authored report',
                references=[reference]).model_dump(mode='json')
            snapshots = freeze_references(session, scope, report['references'])
            contents = {**report, 'workbench_id': scope.workbench_id, 'source_revision': 'a' * 64,
                'reference_snapshots': snapshots}
            plain = {**contents, 'references': [], 'reference_snapshots': []}
            artifact_id = report_id(scope, report['date'], report['period'])
        artifact = WorkbenchArtifact(id=artifact_id, actor=scope.identity.user_id, module=module,
            title='An authored reference', body=json.dumps(plain if historical else contents),
            status='draft', source_ids=[], source_snapshots=[], revision=2 if historical else 1,
            created_at=now, updated_at=now)
        session.add(artifact)
        session.flush()
        remember_record_scope(session, 'artifact', artifact.id)
        if historical:
            session.add(WorkbenchArtifactRevision(artifact_id=artifact.id, revision=1,
                title=artifact.title, body=json.dumps(contents), status='draft', source_ids=[],
                source_snapshots=[], edited_by=scope.identity.user_id, recorded_at=now))
        session.commit()
        return artifact.id


@pytest.mark.parametrize('module,historical', [('strategies', False), ('briefs', False),
                                            ('strategies', True), ('briefs', True)])
def test_existing_artifact_references_prevent_deletion_including_saved_history(deletion, module, historical):
    client, state = deletion
    identifier, version = seed(state)
    add_artifact_reference(state['scope'], identifier, version, module=module, historical=historical)
    response = erase(client, identifier, version)
    assert response.status_code == 409, response.text
    assert '引用' in response.json()['detail']
    original = client.get('/api/sn/research-references/judgment/' + str(identifier), params={'revision': version})
    assert original.status_code == 200 and original.json()['revision'] == version
    with artifact_session(state['scope']) as session:
        assert session.get(Judgment, identifier).status == '草稿'


def test_a_saved_strategy_plan_cannot_lose_its_exact_judgment_reference(deletion):
    from tin.web.research_references import remember_record_scope
    client, state = deletion
    identifier, version = seed(state)
    with artifact_session(state['scope']) as session:
        owner = session.get(Judgment, identifier).researcher_id
        plan = Strategy(researcher_id=owner, title='Saved plan', variety='XY', kind='outright',
            mode='simulation', status='draft', author='first', version=1,
            plan={'source_context': {'research': {'id': identifier, 'version': version}}},
            created_at=datetime.now(timezone.utc), entry_fills=[], exit_fills=[])
        session.add(plan)
        session.flush()
        remember_record_scope(session, 'plan', plan.id)
        session.commit()
    response = erase(client, identifier, version)
    assert response.status_code == 409 and '引用' in response.json()['detail']


def test_commit_failure_rolls_back_the_judgment_marker_and_audit_together(deletion, monkeypatch):
    client, state = deletion
    identifier, version = seed(state)
    scope = state['scope']
    with artifact_session(scope) as session:
        audit_before = list(session.scalars(select(AuditLog.id)))
    def fail_deletion(_session):
        raise RuntimeError('synthetic deletion commit failure')

    with monkeypatch.context() as scoped:
        scoped.setattr(Session, 'commit', fail_deletion)
        with pytest.raises(Exception, match='synthetic deletion commit failure'):
            erase(client, identifier, version)
    with artifact_session(scope) as session:
        assert session.get(Judgment, identifier).status == '草稿'
        assert list(session.scalars(select(WorkbenchArtifact))) == []
        assert list(session.scalars(select(AuditLog.id))) == audit_before
