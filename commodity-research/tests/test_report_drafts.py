"""Editable reports preserve personal intent, explicit references and raw facts."""
from contextlib import contextmanager
from copy import deepcopy
from dataclasses import replace
from datetime import date
import json
from types import SimpleNamespace

from fastapi import FastAPI, HTTPException
from fastapi.testclient import TestClient
import pytest
from sqlalchemy import create_engine, select
from sqlalchemy.orm import sessionmaker

from tin.db import Base
from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, current_workbench
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision

WB = '10101010-1010-4010-8010-101010101010'
OTHER = '20202020-2020-4020-8020-202020202020'
URL = '/api/sn/report-drafts'


def document(**changes):
    result = {'format': 'research-report-draft.v1', 'period': 'weekly', 'date': '2026-01-09',
        'title': 'My dated research', 'sections': {
            key: {'visible': True, 'text': ''} for key in ('summary', 'research', 'dashboard', 'watch')},
        'figures': [], 'references': []}
    result['sections']['summary']['text'] = 'A human explanation, not a verified number.'
    result.update(changes)
    return result


@pytest.fixture
def reports(tmp_path, monkeypatch):
    from tin.web import report_drafts
    factories = {}
    state = {'fact_reads': [], 'resolves': [], 'scope': None}

    @contextmanager
    def storage(scope):
        if scope.workbench_id not in factories:
            engine = create_engine('sqlite:///' + str(tmp_path / (scope.workbench_id + '.db')),
                                   connect_args={'check_same_thread': False})
            Base.metadata.create_all(engine)
            factories[scope.workbench_id] = sessionmaker(engine, expire_on_commit=False)
        with factories[scope.workbench_id]() as session:
            session.info.update(scope.identity.session_info(), workbench_id=scope.workbench_id)
            yield session

    def resolve(session, scope, kind, identifier, revision):
        state['resolves'].append((kind, identifier, revision))
        if identifier == 'missing':
            raise HTTPException(404, 'Reference unavailable')
        return {'kind': kind, 'id': identifier, 'revision': revision, 'title': 'Actual saved record',
            'status': 'draft', 'resource_type': 'research', 'summary': 'A saved summary',
            'body_text': 'Selected revision text', 'body_text_truncated': False,
            'series_ids': [], 'source_ids': ['original-a'], 'payload': {'secret': 'never project'},
            'source_snapshots': [{'secret': 'never freeze'}]}

    def source_version(scope, ids, day):
        state['fact_reads'].append((tuple(ids), day.isoformat()))
        return 'a' * 64

    monkeypatch.setattr(report_drafts, 'artifact_session', storage)
    monkeypatch.setattr(report_drafts, 'resolve_saved_reference', resolve)
    monkeypatch.setattr(report_drafts, 'current_source_version', source_version)
    monkeypatch.setattr(report_drafts, 'bound_series', lambda *_: ['kb-' + 'a' * 48])
    state['storage'] = storage
    app = FastAPI()
    app.include_router(report_drafts.router)

    @app.middleware('http')
    async def identity(request, call_next):
        actor = Identity(request.headers.get('x-actor', 'first'), 'fixture@example.test', 'Fixture', 7,
                         request.headers.get('x-role', 'owner'))
        scope = WorkbenchScope(actor, request.headers.get('x-workbench', WB), 'library', 7,
            {'workspace_mode': 'isolated', 'can_edit': request.headers.get('x-edit', 'true') == 'true',
             'can_read_source': request.headers.get('x-restricted') != 'true'}, 'Bearer fixture',
            scope_version=2, sources_revision=1)
        if request.headers.get('x-no-module') == 'true':
            scope = replace(scope, view_modules=('home',))
        if request.headers.get('x-restricted') == 'true':
            scope = replace(scope, source_binding_id='not-readable')
        state['scope'] = scope
        mark = current_workbench.set(scope)
        user = current_identity.set(actor)
        try:
            return await call_next(request)
        finally:
            current_identity.reset(user)
            current_workbench.reset(mark)

    with TestClient(app) as client:
        yield client, state


def test_personal_report_create_cas_and_real_revision_history(reports):
    client, state = reports
    created = client.post(URL, json={'draft': document()})
    assert created.status_code == 201, created.text
    saved = created.json()
    assert saved['revision'] == 1 and saved['draft']['source_revision'] == 'a' * 64
    draft = document()
    draft['sections']['summary']['text'] = ''
    draft['sections']['watch']['visible'] = False
    changed = client.patch(URL + '/' + saved['id'], json={'draft': draft, 'revision': 1})
    assert changed.status_code == 200, changed.text
    assert changed.json()['revision'] == 2
    assert changed.json()['draft']['sections']['summary']['text'] == ''
    assert client.patch(URL + '/' + saved['id'], json={'draft': draft, 'revision': 1}).status_code == 409
    with state['storage'](state['scope']) as session:
        rows = session.scalars(select(WorkbenchArtifactRevision).order_by(WorkbenchArtifactRevision.revision)).all()
        assert len(rows) == 2
        assert json.loads(rows[0].body)['sections']['summary']['text'] != ''
        assert json.loads(rows[1].body)['sections']['summary']['text'] == ''


def test_report_is_isolated_by_person_workbench_date_and_period(reports):
    client, _ = reports
    created = client.post(URL, json={'draft': document()}).json()
    assert client.get(URL + '?date=2026-01-09&period=weekly').json()['id'] == created['id']
    for query, headers in [('?date=2026-01-08&period=weekly', {}),
        ('?date=2026-01-09&period=daily', {}), ('?date=2026-01-09&period=weekly', {'x-actor': 'second'}),
        ('?date=2026-01-09&period=weekly', {'x-workbench': OTHER})]:
        assert client.get(URL + query, headers=headers).json()['draft'] is None
    assert client.patch(URL + '/' + created['id'], headers={'x-actor': 'second'},
                        json={'draft': document(), 'revision': 1}).status_code == 404
    assert client.patch(URL + '/' + created['id'], json={'draft': document(period='daily'), 'revision': 1}).status_code == 409
    assert client.post(URL, json={'draft': document()}).status_code == 409


def test_report_does_not_accept_client_facts_or_frozen_reference_snapshots(reports):
    client, _ = reports
    for key, value in [('source_revision', 'f' * 64), ('workbench_id', OTHER), ('reference_snapshots', []),
                       ('observations', [{'value': 999}]), ('points', [999])]:
        assert client.post(URL, json={'draft': document(**{key: value})}).status_code == 422
    assert client.post(URL, json={'draft': document(figures=[{'series_id': 'kb-' + 'a' * 48, 'value': 999}])}).status_code == 422


@pytest.mark.parametrize('headers,status', [({'x-restricted': 'true'}, 403),
    ({'x-no-module': 'true'}, 403), ({'x-role': 'viewer'}, 403), ({'x-edit': 'false'}, 403)])
def test_report_write_acl_cannot_be_bypassed_with_a_draft(headers, status, reports):
    client, _ = reports
    assert client.post(URL, headers=headers, json={'draft': document()}).status_code == status


def test_selected_references_freeze_only_explicit_real_revision_projection(reports):
    client, state = reports
    ref = {'kind': 'judgment', 'id': '18', 'revision': 3, 'section': 'research'}
    saved = client.post(URL, json={'draft': document(references=[ref])})
    assert saved.status_code == 201, saved.text
    snapshot = saved.json()['draft']['reference_snapshots'][0]
    assert snapshot['revision'] == 3 and snapshot['body_text'] == 'Selected revision text'
    assert 'payload' not in snapshot and 'source_snapshots' not in snapshot
    assert ('judgment', '18', 3) in state['resolves']
    missing = {'kind': 'strategy', 'id': 'missing', 'revision': 1, 'section': 'research'}
    assert client.post(URL, json={'draft': document(date='2026-01-10', references=[missing])}).status_code == 404


def test_foreign_workbench_body_is_not_exposed_even_in_the_same_storage(reports):
    client, state = reports
    saved = client.post(URL, json={'draft': document()}).json()
    with state['storage'](state['scope']) as session:
        row = session.get(WorkbenchArtifact, saved['id'])
        body = json.loads(row.body)
        assert body['workbench_id'] == WB
        body['workbench_id'] = OTHER
        row.body = json.dumps(body)
        session.commit()
    assert client.get(URL + '?date=2026-01-09&period=weekly').json()['draft'] is None
    assert client.patch(URL + '/' + saved['id'], json={'draft': document(), 'revision': 1}).status_code == 404


def test_lost_reference_permissions_do_not_return_frozen_quotations(reports, monkeypatch):
    from tin.web import report_drafts
    from tin.web.report_view import readable_markdown
    client, _state = reports
    ref = {'kind': 'judgment', 'id': '18', 'revision': 3, 'section': 'research'}
    assert client.post(URL, json={'draft': document(references=[ref])}).status_code == 201
    def denied(*_args):
        raise HTTPException(403, 'Permission changed')
    monkeypatch.setattr(report_drafts, 'resolve_saved_reference', denied)
    visible = client.get(URL + '?date=2026-01-09&period=weekly').json()
    assert visible['draft']['reference_snapshots'] == []
    assert 'Selected revision text' not in json.dumps(visible)
    assert len(visible['unavailable_references']) == 1
    with pytest.raises(HTTPException) as blocked:
        readable_markdown({'as_of': '2026-01-09', 'manual_report': visible})
    assert blocked.value.status_code == 409


def test_manual_sections_and_markdown_remain_same_and_empty_text_does_not_fallback():
    from tin.web.report_view import present_snapshot, readable_markdown
    draft = document()
    draft['sections']['summary']['text'] = ''
    draft['sections']['research']['text'] = '<script>human text</script>\nSaved explanation'
    draft['sections']['watch']['visible'] = False
    draft.update(source_revision='a' * 64, reference_snapshots=[])
    payload = {'as_of': '2026-01-09', 'period': {'kind': 'weekly', 'start': '2026-01-05', 'end': '2026-01-09'},
        'observations': [{'series_id': 'fixture', 'name': 'Actual fact', 'value': 7, 'unit': '吨'}],
        'briefs': [{'summary': 'Do not overwrite manual empty text', 'claims': []}],
        'manual_report': {'id': 'artifact', 'revision': 2, 'draft': draft}}
    old = deepcopy(payload)
    view = present_snapshot(payload)
    assert view['panels']['summary']['text'] == ''
    assert view['panels']['watch']['visible'] is False
    assert view['observations'][0]['value'] == 7
    md = readable_markdown(payload)
    assert 'Do not overwrite' not in md
    assert '&lt;script&gt;human text&lt;/script&gt;' in md
    assert 'Saved explanation' in md and '## 待关注' not in md
    assert 'Actual fact' in md and 'artifact' in md
    from tin.web.brief_export import _markdown
    scope = SimpleNamespace(metadata={'name': 'A real workbench'}, workbench_id=WB,
        knowledge_base_id='library', identity=SimpleNamespace(user_id='first'))
    _name, blob, _hash = _markdown(scope, 'weekly', '2026-01-09', 'reports/chosen-folder', payload)
    main = blob.decode().split('<details><summary>原始记录与来源审计</summary>')[0]
    assert 'Saved explanation' in main and 'Do not overwrite' not in main
    assert 'reports/chosen-folder' in main and 'v2' in main
    assert payload == old
