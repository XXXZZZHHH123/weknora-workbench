import asyncio
from concurrent.futures import ThreadPoolExecutor
from threading import Event
import json
import pytest

from sqlalchemy import select
from tests.test_research_strategy_cards import cards, entry
from tests.test_judgment_panels import seed
from tests.test_judgment_delete import erase
from tin.models import Judgment
from tin.models.workbench import WorkbenchArtifact
from tin.integration.workbench_scope import artifact_session
from tin.web import research_strategy, judgment_lifecycle


@pytest.mark.parametrize("revise", [False, True])
def test_reference_save_validated_before_delete_can_commit_after_delete(cards, monkeypatch, revise):
    client, state = cards
    client.app.include_router(judgment_lifecycle.router)
    assert client.get('/api/sn/research-strategies').status_code == 200
    identifier, version = seed(state)
    scope = state['scope']
    validated, resume = Event(), Event()
    target = 'update_artifact' if revise else 'create_artifact'
    original = getattr(research_strategy, target)

    async def paused_create(*args, **kwargs):
        validated.set()
        assert await asyncio.to_thread(resume.wait, 15)
        return await original(*args, **kwargs)

    existing = client.post('/api/sn/research-strategies', json=entry(source_ids=[])).json() if revise else None
    monkeypatch.setattr(research_strategy, target, paused_create)
    body = entry(source_ids=[])
    body['card']['judgment_reference'] = {'kind': 'judgment', 'id': str(identifier), 'revision': version}
    if revise:
        body['revision'] = existing['revision']
    with ThreadPoolExecutor(max_workers=1) as workers:
        operation = client.patch if revise else client.post
        url = '/api/sn/research-strategies' + ('/' + existing['id'] if revise else '')
        pending = workers.submit(operation, url, json=body)
        try:
            assert validated.wait(10)
            deleted = erase(client, identifier, version)
            assert deleted.status_code == 200, deleted.text
        finally:
            resume.set()
        saved = pending.result(timeout=15)
    assert saved.status_code in {404, 409}, 'A stale reference was saved after its judgment was deleted: ' + saved.text
    with artifact_session(scope) as session:
        assert session.get(Judgment, identifier).status == '已删除'
        assert not any(json.loads(row.body).get('judgment_reference', {}).get('id') == str(identifier)
                       for row in session.scalars(select(WorkbenchArtifact).where(WorkbenchArtifact.module == 'strategies')))


def test_generic_artifact_writer_cannot_publish_a_deleted_explicit_judgment_pin(cards):
    from fastapi import HTTPException
    from tin.integration.identity import current_identity
    from tin.integration.workbench_scope import current_workbench
    from tin.integration.page_facts import page_read_scope
    from tin.web.workbench_artifacts import create_artifact, ArtifactModule
    client, state = cards
    client.app.include_router(judgment_lifecycle.router)
    client.get('/api/sn/research-strategies')
    identifier, version = seed(state)
    scope = state['scope']
    assert erase(client, identifier, version).status_code == 200
    data = entry(source_ids=[])
    data['card']['judgment_reference'] = {'kind': 'judgment', 'id': str(identifier), 'revision': version}
    parsed = research_strategy.CardInput.model_validate(data)
    board = current_workbench.set(scope)
    actor = current_identity.set(scope.identity)
    try:
        with page_read_scope('strategies', series_ids=[]), pytest.raises(HTTPException) as blocked:
            asyncio.run(create_artifact(ArtifactModule.strategies, research_strategy.artifact_input(parsed), scope))
        assert blocked.value.status_code in {404, 409}
    finally:
        current_identity.reset(actor)
        current_workbench.reset(board)
