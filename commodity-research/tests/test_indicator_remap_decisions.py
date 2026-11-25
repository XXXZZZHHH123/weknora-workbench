from dataclasses import replace

import pytest
from fastapi import HTTPException
from httpx import Response

from tin.integration import indicator_dedup as service
from tin.provenance import bridge
from tests.test_indicator_dedup import DEFAULT, OWN, setup  # noqa: F401


@pytest.fixture
def remap(setup, monkeypatch):
    scope, state = setup
    source = {**scope.sources[0], 'permission': 'editor'}
    scope = replace(scope, sources=(source,))
    old = {**state['fields'][0], 'metric': 'Long advance statement / Quantity', 'sheet': 'Data', 'block_index': 0,
           'source_knowledge_id': 'original', 'source_sha256': 'a' * 64, 'dataset_id': 'a' * 24}
    new = {**old, 'series_id': DEFAULT, 'metric': 'Quantity', 'dataset_id': 'b' * 24,
           'source_version': 'c' * 64}
    state['fields'] = [new]
    item = {'old': old, 'new_series_id': DEFAULT, 'new_dataset_id': new['dataset_id'],
            'source_version': new['source_version'], 'sheet': 'Data', 'block_index': 0,
            'column': 'C', 'category': None, 'matching_periods': 530, 'latest_date': '2026-01-01'}
    upstream = [item]

    def request(method, path, body, **kwargs):
        assert (method, path, body) == ('POST', '/api/research/indicator-remaps', {})
        return Response(200, json={'items': upstream})

    monkeypatch.setattr(bridge, 'structured_request', request)
    return scope, upstream


def test_same_original_remap_confirms_and_restores_using_the_review_revision(remap):
    scope, _ = remap
    assert service.list_mappings(scope)['items'] == []
    listing = service.list_mappings(scope, include_remaps=True)
    item, = listing['items']
    assert listing['can_confirm'] and item['batch_eligible']
    assert item['candidate']['kind'] == 'remap' and item['evidence']['matching_periods'] == 530
    bound = [OWN]

    def apply(actual, items):
        assert actual is scope and len(items) == 1
        confirmed, = items
        assert confirmed['revision'] == item['revision'] + 1 and confirmed['state'] == 'confirmed_default'
        assert bound[0] == confirmed['personal']['series_id']
        bound[0] = confirmed['default']['series_id']
        return {confirmed['id']: [scope.workbench_id]}

    def undo(actual, confirmed):
        assert actual is scope and confirmed['state'] == 'confirmed_default'
        assert confirmed['revision'] == item['revision'] + 1
        assert confirmed['workbench_ids'] == [scope.workbench_id]
        assert bound[0] == confirmed['default']['series_id']
        bound[0] = confirmed['personal']['series_id']

    result = service.decide(scope, [item['id']], {item['id']: item['revision']}, 'confirmed_default',
                            batch=True, apply_remaps=apply)['items'][0]
    assert bound == [DEFAULT] and service.confirmed_replacements(scope) == {}
    assert 'workbench_ids' not in result['history'][-1]
    verified = service.verify_candidate(scope, result['id'])
    restored = service.undo(scope, result['id'], verified['revision'], undo_remap=undo)
    assert restored['state'] == 'pending' and bound == [OWN]


def test_changed_or_withdrawn_exact_evidence_cannot_be_confirmed(remap):
    scope, upstream = remap
    item = service.list_mappings(scope, include_remaps=True)['items'][0]

    def fail(*args):
        pytest.fail('stale remap cannot write references')

    upstream[0]['matching_periods'] += 1
    with pytest.raises(HTTPException) as conflict:
        service.decide(scope, [item['id']], {item['id']: item['revision']}, 'confirmed_default', apply_remaps=fail)
    assert conflict.value.status_code == 409
    refreshed = service.list_mappings(scope, include_remaps=True)['items'][0]
    upstream.clear()
    with pytest.raises(HTTPException) as missing:
        service.decide(scope, [item['id']], {item['id']: refreshed['revision']}, 'confirmed_default', apply_remaps=fail)
    assert missing.value.status_code == 404
    assert service.list_mappings(scope, include_remaps=True)['items'] == []


def test_confirmation_route_updates_and_undoes_a_workbench_reference(remap, monkeypatch):
    from datetime import datetime, timezone
    from tin.integration.workbench_scope import artifact_session
    from tin.models import SourceMapping
    from tin.web import restricted_views, workbench_remaps
    from tests.test_indicator_dedup_ui_api import client_for

    scope, _ = remap

    async def directory(actual, path, **kwargs):
        assert actual.workbench_id == scope.workbench_id and path == '/api/v1/workspaces/research'
        return {'data': [{'id': scope.workbench_id, 'can_edit': True}]}

    monkeypatch.setattr(workbench_remaps, 'go_request', directory)
    with artifact_session(scope) as session:
        session.add(SourceMapping(id=restricted_views.mapping_key(session, restricted_views.CORE_KEY),
            config={'items': [{'kind': 'indicator', 'id': OWN}]}, version=1,
            confirmed_by=scope.identity.user_id, confirmed_at=datetime.now(timezone.utc)))
        session.commit()
    client = client_for(scope, {'fields': []}, monkeypatch)
    item = client.get('/api/sn/indicator-dedup?include_remaps=true').json()['items'][0]
    result = client.post('/api/sn/indicator-dedup/decisions', json={
        'ids': [item['id']], 'revisions': {item['id']: item['revision']},
        'state': 'confirmed_default', 'batch': True, 'sources_revision': scope.sources_revision})
    assert result.status_code == 200, result.text
    with artifact_session(scope) as session:
        assert restricted_views.core_items(session) == [{'kind': 'indicator', 'id': DEFAULT}]
    saved = result.json()['items'][0]
    undone = client.post(f"/api/sn/indicator-dedup/{saved['id']}/undo", json={
        'revision': saved['revision'], 'sources_revision': scope.sources_revision})
    assert undone.status_code == 200, undone.text
    with artifact_session(scope) as session:
        assert restricted_views.core_items(session) == [{'kind': 'indicator', 'id': OWN}]
