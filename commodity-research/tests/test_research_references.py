"""Same-workbench, owner-bound immutable research references without fact reads."""
from dataclasses import replace
from datetime import datetime, timezone
import json
from uuid import uuid4

import pytest
from sqlalchemy import event

from tests.test_research_strategy_cards import cards, entry
from tin.integration.workbench_scope import artifact_session
from tin.models.workbench import WorkbenchArtifact


def add_router(client):
    from tin.web.research_references import router
    client.app.include_router(router)


def test_reference_directory_is_small_and_pinned_card_versions_are_actual(cards):
    client, state = cards
    add_router(client)
    original = client.post('/api/sn/research-strategies', json=entry(source_ids=[]))
    assert original.status_code == 201, original.text
    row = original.json()
    reference_id = 'research:' + row['id']
    before_directory = list(state['fact_reads'])
    directory = client.get('/api/sn/research-references', params={'kind':'strategy'}).json()
    assert state['fact_reads'] == before_directory
    assert directory['total'] == 1
    assert directory['items'][0]['id'] == reference_id
    assert not {'payload','body_text','source_snapshots'} & directory['items'][0].keys()
    changed = entry(source_ids=[])
    changed['revision'] = 1
    changed['card']['thesis'] = 'A revised hypothesis with separate provenance.'
    result = client.patch('/api/sn/research-strategies/' + row['id'], json=changed)
    assert result.status_code == 200, result.text
    before_versions = list(state['fact_reads'])
    first = client.get('/api/sn/research-references/strategy/' + reference_id, params={'revision':1})
    second = client.get('/api/sn/research-references/strategy/' + reference_id, params={'revision':2})
    assert first.status_code == second.status_code == 200
    assert first.json()['payload']['thesis'] == original.json()['card']['thesis']
    assert second.json()['payload']['thesis'] == changed['card']['thesis']
    assert first.json()['revision'] == 1
    assert client.get('/api/sn/research-references/strategy/' + reference_id,params={'revision':3}).status_code == 404
    assert state['fact_reads'] == before_versions, 'Referencing authored records must not hydrate knowledge facts'


def test_references_cannot_read_another_owner_workbench_or_restricted_scope(cards):
    client, state = cards
    add_router(client)
    created = client.post('/api/sn/research-strategies', json=entry(source_ids=[])).json()
    path = '/api/sn/research-references/strategy/research:' + created['id']
    assert client.get(path,headers={'x-actor':'second'}).status_code == 404
    assert client.get(path,headers={'x-workbench':str(uuid4())}).status_code == 404
    assert client.get(path,headers={'x-restricted':'true'}).status_code == 403
    assert client.get(path,headers={'x-no-module':'true'}).status_code == 403
    for identifier in ['plan:0','plan:01','plan:true','research:not-a-uuid','../outside']:
        assert client.get('/api/sn/research-references/strategy/'+identifier).status_code in {404,422}


def test_legacy_scope_ignores_client_body_claim_and_read_does_not_write(cards):
    from tin.web.research_references import record_scope_status, remember_record_scope
    client, state = cards
    # Establish a real authorized current scope without creating an artifact.
    assert client.get('/api/sn/research-strategies').status_code == 200
    scope = state['scope']
    foreign_id = str(uuid4())
    with artifact_session(scope) as session:
        now = datetime.now(timezone.utc)
        row = WorkbenchArtifact(id=str(uuid4()),module='strategies',actor=scope.identity.user_id,
            title='Unassigned historical note',body=json.dumps({'workbench_id':scope.workbench_id}),
            status='draft',source_ids=[],source_snapshots=[],revision=1,created_at=now,updated_at=now)
        session.add(row);session.commit()
        legacy = replace(scope,metadata={**scope.metadata,'workspace_mode':'legacy'})
        session.info['source_storage_legacy'] = True
        assert record_scope_status(session,legacy,'artifact',row.id,row) == 'unassigned'
        session.info['workbench_id'] = foreign_id
        remember_record_scope(session,'artifact',row.id)
        session.commit();session.info['workbench_id'] = scope.workbench_id
        statements=[]
        def capture(_connection,_cursor,statement,_parameters,_context,_many): statements.append(statement)
        event.listen(session.get_bind(),'before_cursor_execute',capture)
        try:
            assert record_scope_status(session,legacy,'artifact',row.id,row) == 'foreign'
        finally:event.remove(session.get_bind(),'before_cursor_execute',capture)
        assert all(s.lstrip().upper().startswith('SELECT') for s in statements)


def test_reference_directory_does_not_hydrate_source_snapshots_or_query_each_stamp(cards):
    client, state = cards
    add_router(client)
    for _ in range(8):
        assert client.post('/api/sn/research-strategies', json=entry(source_ids=[])).status_code == 201
    with artifact_session(state['scope']) as session:
        engine = session.get_bind()
    statements = []
    def capture(_connection, _cursor, statement, *_args):
        statements.append(statement)
    event.listen(engine, 'before_cursor_execute', capture)
    try:
        response = client.get('/api/sn/research-references', params={'kind': 'strategy'})
    finally:
        event.remove(engine, 'before_cursor_execute', capture)
    assert response.status_code == 200
    assert response.json()['total'] == 8
    selects = [sql for sql in statements if sql.lstrip().upper().startswith('SELECT')]
    assert all('workbench_artifacts.source_snapshots' not in sql for sql in selects)
    assert len(selects) <= 6, 'A reference directory must batch registry proofs, not query once per card'


def test_legacy_directory_keeps_proved_cards_without_materializing_large_snapshot(cards):
    from tin.web.research_references import list_references
    client, state = cards
    assert client.get('/api/sn/research-strategies').status_code == 200
    scope = state['scope']
    with artifact_session(scope) as session:
        now = datetime.now(timezone.utc)
        row = WorkbenchArtifact(id=str(uuid4()), module='strategies', actor=scope.identity.user_id,
            title='Old proved card', body='An actual saved note', status='draft', source_ids=[],
            source_snapshots=[{'download_url': '/research/api/workbenches/' + scope.workbench_id + '/sources/x',
                               'unrelated_large_payload': 'x' * 300000}], revision=1, created_at=now, updated_at=now)
        session.add(row); session.commit(); session.expunge_all()
        legacy = replace(scope, metadata={**scope.metadata, 'workspace_mode': 'legacy'})
        statements = []
        def capture(_connection, _cursor, statement, *_args):
            statements.append(statement)
        event.listen(session.get_bind(), 'before_cursor_execute', capture)
        try:
            result = list_references(session, legacy, 'strategy')
        finally:
            event.remove(session.get_bind(), 'before_cursor_execute', capture)
        assert result['total'] == 1 and result['items'][0]['title'] == 'Old proved card'
        for sql in statements:
            # SQL may derive a boolean from JSON; it must not select raw snapshots.
            projection = sql.split('FROM')[0]
            assert 'workbench_artifacts.source_snapshots' not in projection
