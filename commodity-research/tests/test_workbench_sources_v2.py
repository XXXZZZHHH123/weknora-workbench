"""Multiple source grants never become implicit facts or result ownership."""
from dataclasses import replace

from fastapi import HTTPException
import pytest

from tin.integration.identity import Identity
from tin.integration import workbench_scope as scopes

WB = '10000000-0000-4000-8000-000000000091'
IDENTITY = Identity('reader', 'reader@test', 'Reader', 17, 'owner')


def source(number, delegated=False):
    identifier = '20000000-0000-4000-8000-' + f'{number:012d}'
    return {'id': '30000000-0000-4000-8000-' + f'{number:012d}',
        'source_type': 'knowledge_base', 'source_id': identifier, 'knowledge_base_id': identifier,
        'tenant_id': 17, 'kind': 'primary', 'permission': 'viewer', 'delegated': delegated,
        'view_modules': ['market'] if delegated else list(scopes.VIEW_MODULES), 'knowledge': []}


def grant(sources):
    return {'success': True, 'data': {'scope_version': 2, 'sources_revision': 7, 'id': WB,
        'user_id': IDENTITY.user_id, 'scope_tenant_id': 17, 'knowledge_base_id': '',
        'sources': sources, 'unavailable_sources': [], 'view_modules': list(scopes.VIEW_MODULES)}}


@pytest.mark.parametrize('count', [0, 1, 8])
def test_version_two_allows_an_authorized_empty_or_multi_source_scope(count):
    scope = scopes.WorkbenchScope(IDENTITY, WB, '', 0, {'scope_version': 2}, 'Bearer fake')
    document = grant([source(number) for number in range(1, count + 1)])
    assert scopes.validated_source_scope(scope, document) == document['data']


@pytest.mark.parametrize('problem', ['nine', 'duplicate_binding', 'duplicate_backing', 'revision', 'member', 'type'])
def test_v2_rejects_oversized_ambiguous_or_forged_source_grants(problem):
    scope = scopes.WorkbenchScope(IDENTITY, WB, '', 0, {'scope_version': 2}, 'Bearer fake')
    doc = grant([source(1), source(2)])
    if problem == 'nine': doc['data']['sources'] = [source(number) for number in range(1, 10)]
    elif problem == 'duplicate_binding': doc['data']['sources'][1]['id'] = doc['data']['sources'][0]['id']
    elif problem == 'duplicate_backing': doc['data']['sources'][1]['knowledge_base_id'] = doc['data']['sources'][0]['knowledge_base_id']
    elif problem == 'revision': doc['data']['sources_revision'] = True
    elif problem == 'member': doc['data']['user_id'] = 'someone-else'
    else: doc['data']['sources'][0]['source_type'] = 'arbitrary'
    with pytest.raises(HTTPException): scopes.validated_source_scope(scope, doc)


def test_mixed_source_permissions_are_checked_on_each_target():
    original = scopes.WorkbenchScope(IDENTITY, WB, '', 0,
        {'scope_version': 2, 'can_edit': True, 'can_read_source': False}, 'Bearer fake')
    scope = replace(original, scope_version=2, sources_revision=7, sources=(source(1, True), source(2)))
    assert scope.restricted, 'A directly readable center must not upgrade another private source or the workbench view'
    with pytest.raises(HTTPException): scopes.source_scope_for_binding(scope, source(1)['id'], direct_only=True)
    with pytest.raises(HTTPException): scopes.source_scope_for_binding(scope, source(1)['id'], module='indicators')
    delegated = scopes.source_scope_for_binding(scope, source(1)['id'], module='market')
    assert delegated.restricted and delegated.knowledge_base_id == source(1)['knowledge_base_id']
    assert not scopes.source_scope_for_binding(scope, source(2)['id'], direct_only=True).restricted


def test_zero_source_result_session_is_independent_of_a_removed_legacy_primary(tmp_path, monkeypatch):
    monkeypatch.setattr(scopes.settings, 'data_dir', tmp_path)
    old = scopes.WorkbenchScope(IDENTITY, WB, source(1)['knowledge_base_id'], 17,
        {'workspace_mode': 'isolated', 'can_read_source': True}, 'Bearer fake')
    factory = scopes.isolated_session_factory(17, WB)
    from sqlalchemy import text
    with factory() as session:
        session.execute(text('INSERT INTO isolated_scope VALUES (:wb,:kb,17)'),
                        {'wb': WB, 'kb': old.knowledge_base_id})
        session.commit()
    empty = replace(old, scope_version=2, sources_revision=8, sources=(), knowledge_base_id='',
                    knowledge_base_tenant_id=0, metadata={**old.metadata, 'scope_version': 2})
    with scopes.artifact_session(empty) as session:
        assert session.info['workbench_id'] == WB
        assert session.scalar(text('SELECT count(*) FROM isolated_scope')) == 1
