"""Named judgment references remain private, immutable and non-activating."""
from dataclasses import replace
from datetime import datetime, timezone
import json

from fastapi import FastAPI, HTTPException
from fastapi.testclient import TestClient
import pytest
from sqlalchemy import select

from tin.config import settings
from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, artifact_session, current_workbench
from tin.judgments.service import current, save_version, to_payload
from tin.models import Judgment
from tin.models.workbench import WorkbenchArtifact
from tin.schemas.judgment import JudgmentIn
from tests.test_structured_judgment_import import payload

WB = '9e761d83-5abe-4adf-859d-6cd57816ddaf'
OTHER = '36a33274-1730-4ce8-8c32-62c2d23dd032'
URL = '/api/sn/judgment/panels'


@pytest.fixture
def panels(tmp_path, monkeypatch):
    from tin.web.judgment_panels import router
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://native.invalid')
    state = {}
    app = FastAPI()
    app.include_router(router)

    @app.middleware('http')
    async def scoped(request, call_next):
        identity = Identity(request.headers.get('x-actor', 'first'), '', 'Fixture', 17,
                            request.headers.get('x-role', 'owner'))
        scope = WorkbenchScope(identity, request.headers.get('x-workbench', WB), 'fixture-kb', 17,
            {'workspace_mode': request.headers.get('x-mode', 'isolated'), 'can_edit': request.headers.get('x-edit', 'true') == 'true',
             'can_read_source': request.headers.get('x-restricted') != 'true'}, 'Bearer fixture')
        if request.headers.get('x-no-module'):
            scope = replace(scope, view_modules=('home',))
        state['scope'] = scope
        token, actor = current_workbench.set(scope), current_identity.set(identity)
        try:
            return await call_next(request)
        finally:
            current_workbench.reset(token)
            current_identity.reset(actor)

    with TestClient(app) as client:
        client.get(URL)
        yield client, state


def seed(state, *, active=False):
    with artifact_session(state['scope']) as session:
        document = JudgmentIn.model_validate(payload())
        session.info.update(knowledge_facts=True,
            knowledge_facts_catalog={identifier: {'unit': '吨'} for identifier in document.whitelist})
        row = save_version(session, document, actor=state['scope'].identity.user_id)
        if active:
            row.status = '生效'
        session.commit()
        return row.id, row.version


def test_old_imports_are_available_without_rewriting_any_row_or_creating_panels(panels):
    client, state = panels
    first, _ = seed(state, active=True)
    second, _ = seed(state)
    response = client.get(URL)
    assert response.status_code == 200
    assert [item['judgment_id'] for item in response.json()['items']] == [second, first]
    with artifact_session(state['scope']) as session:
        assert list(session.scalars(select(WorkbenchArtifact))) == []
        assert current(session, 'XY').id == first
        assert len(list(session.scalars(select(Judgment)))) == 2


def test_named_reference_changes_label_not_judgment_or_formal_selection(panels):
    client, state = panels
    first, _ = seed(state, active=True)
    second, version = seed(state)
    with artifact_session(state['scope']) as session:
        before = to_payload(session.get(Judgment, second)).model_dump(mode='json')
    response = client.post(URL, json={'judgment_id': second, 'version': version, 'title': 'Alternative case'})
    assert response.status_code == 201, response.text
    saved = response.json()
    assert saved['judgment_id'] == second and saved['title'] == 'Alternative case'
    assert saved['revision'] == 1
    listed = client.get(URL).json()['items']
    assert len(listed) == 2 and next(item for item in listed if item['judgment_id'] == second)['title'] == 'Alternative case'
    with artifact_session(state['scope']) as session:
        assert to_payload(session.get(Judgment, second)).model_dump(mode='json') == before
        assert current(session, 'XY').id == first
    changed = client.patch(URL + '/' + saved['id'],
        json={'title': 'Reviewed name', 'revision': 1})
    assert changed.status_code == 200 and changed.json()['revision'] == 2
    assert client.patch(URL + '/' + saved['id'],
        json={'title': 'Stale overwrite', 'revision': 1}).status_code == 409
    assert client.post(URL, json={'judgment_id': second, 'version': version + 1, 'title': 'Wrong pin'}).status_code == 409


def test_other_actor_and_workbench_cannot_name_or_open_a_private_judgment(panels):
    from tin.web.judgment_panels import selected_judgment
    client, state = panels
    identifier, version = seed(state)
    body = {'judgment_id': identifier, 'version': version, 'title': 'Private'}
    assert client.post(URL, json=body, headers={'x-actor': 'second'}).status_code == 404
    assert client.get(URL, headers={'x-actor': 'second'}).json()['items'] == []
    with artifact_session(state['scope']) as session:
        with pytest.raises(HTTPException) as error:
            selected_judgment(session, identifier, scope=state['scope'])
        assert error.value.status_code == 404
    assert client.post(URL, json=body, headers={'x-workbench': OTHER}).status_code == 404
    assert client.get(URL, headers={'x-workbench': OTHER}).json()['items'] == []


@pytest.mark.parametrize('headers', [{'x-edit': 'false'}, {'x-role': 'viewer'}, {'x-restricted': 'true'}, {'x-no-module': 'true'}])
def test_panel_writes_do_not_expand_workbench_or_module_permissions(panels, headers):
    client, state = panels
    identifier, version = seed(state)
    assert client.post(URL, json={'judgment_id': identifier, 'version': version, 'title': 'Denied'},
                       headers=headers).status_code == 403


@pytest.mark.parametrize('identifier', [True, -1, 0, '1', 2**80])
def test_panel_identity_is_an_explicit_bounded_integer(panels, identifier):
    client, _ = panels
    response = client.post(URL, json={'judgment_id': identifier, 'version': 1, 'title': 'Invalid identity'})
    assert response.status_code == 422


def test_legacy_directory_filters_foreign_workbench_before_count_and_paging(panels):
    client, state = panels
    client.get(URL, headers={'x-mode': 'legacy'})
    first, _ = seed(state, active=True)
    client.get(URL, headers={'x-mode': 'legacy', 'x-workbench': OTHER})
    foreign, _ = seed(state)
    response = client.get(URL + '?limit=1', headers={'x-mode': 'legacy'})
    assert response.status_code == 200, response.text
    assert response.json()['total'] == 1 and response.json()['has_more'] is False
    assert [item['judgment_id'] for item in response.json()['items']] == [first]
    from tin.web.judgment_panels import selected_judgment
    with artifact_session(state['scope']) as session:
        with pytest.raises(HTTPException) as error:
            selected_judgment(session, foreign, scope=state['scope'])
        assert error.value.status_code == 404


def test_forged_generic_artifact_cannot_be_renamed_as_a_panel(panels):
    client, state = panels
    identifier, version = seed(state)
    forged_id = '2e23864f-279d-49c1-a0d6-335af7af8af5'
    now = datetime.now(timezone.utc)
    with artifact_session(state['scope']) as session:
        session.add(WorkbenchArtifact(id=forged_id, actor='first', module='judgments', title='Forged label',
            body=json.dumps({'format': 'judgment-panel.v1', 'workbench_id': WB,
                'judgment_id': identifier, 'version': version}), status='draft', source_ids=[],
            source_snapshots=[], revision=1, created_at=now, updated_at=now))
        session.commit()
    response = client.patch(URL + '/' + forged_id, json={'title': 'Unexpected rename', 'revision': 1})
    assert response.status_code == 404
    assert client.get(URL).json()['items'][0]['named'] is False


def test_selected_view_requires_scope_and_hydrates_only_its_actual_references(panels):
    from tin.web.judgment_panels import judgment_series_ids, selected_judgment
    client, state = panels
    identifier, _ = seed(state)
    with artifact_session(state['scope']) as session:
        with pytest.raises(HTTPException) as error:
            selected_judgment(session, identifier)
        assert error.value.status_code == 403
        row = selected_judgment(session, identifier, scope=state['scope'])
        assert judgment_series_ids(row) == payload()['whitelist']
