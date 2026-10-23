"""A verified legacy workbench shares its owner's assigned records, not a tenant dump."""
from sqlalchemy import select

from tests.test_canonical_research_results import save_results
from tests.test_knowledge_facts import canonical, LEGACY, SECOND, KB, SERIES, page
from tests.test_restricted_views import delegated


def legacy_results():
    from tin.db import tenant_database_path
    from tin.integration.workbench_scope import result_session_factory
    return result_session_factory(tenant_database_path(17))()


def test_restricted_legacy_judgment_shows_verified_owner_full_page(canonical):
    client, headers, state, _ = canonical
    save_results(LEGACY)
    save_results(LEGACY, 'another-user')
    delegated(state, ('judgments',))
    before = state['pages']
    response = client.get(page(LEGACY, '/sn/judgment'), headers={**headers, 'Authorization': 'Bearer source-viewer'})
    assert response.status_code == 200, response.text
    assert 'researcher original thesis' in response.text
    assert 'Human record' in response.text
    assert '主逻辑与证伪条件' in response.text and '版本记录' in response.text
    assert 'another-user' not in response.text
    assert '历史记录尚未归属' not in response.text
    assert 'data-open-logic' not in response.text and 'judgment-manager.js' not in response.text
    assert state['pages'] == before and not state.get('bounded_requests')


def test_restricted_legacy_judgment_fetches_only_its_bound_threshold(canonical):
    from tin.models import Judgment, Researcher
    client, headers, state, _ = canonical
    save_results(LEGACY)
    with legacy_results() as session:
        owner = select(Researcher.id).where(Researcher.weknora_user_id == 'researcher')
        row = session.scalar(select(Judgment).where(Judgment.researcher_id.in_(owner)))
        row.thresholds = [{'id': 'limit', 'name': 'OWNER_BOUND_THRESHOLD', 'series_id': SERIES,
                          'operator': 'between', 'value': [10, 20], 'type': '观察位', 'horizon': '短期'}]
        session.commit()
    delegated(state, ('judgments',))
    before = state['pages']
    response = client.get(page(LEGACY, '/sn/judgment'), headers=headers)
    assert response.status_code == 200, response.text
    assert 'OWNER_BOUND_THRESHOLD' in response.text and '已进入区间' in response.text
    assert state['bounded_requests'][-1]['series_ids'] == [SERIES]
    assert state['bounded_requests'][-1]['view_module'] == 'judgments'
    assert state['pages'] == before


def test_legacy_multiple_workbench_bindings_remain_unassigned(canonical):
    from tin.models.workbench import WorkbenchStorageScope
    client, headers, state, _ = canonical
    save_results(LEGACY)
    with legacy_results() as session:
        session.add(WorkbenchStorageScope(workbench_id=SECOND, knowledge_base_id=KB, knowledge_base_tenant_id=17))
        session.commit()
    delegated(state, ('judgments',))
    response = client.get(page(LEGACY, '/sn/judgment'), headers=headers)
    assert response.status_code == 200, response.text
    assert '历史记录尚未归属到当前研究台' in response.text
    assert 'researcher original thesis' not in response.text
    assert not state.get('bounded_requests')


def test_legacy_unassigned_and_other_members_records_are_not_shared(canonical):
    from tin.models import Judgment
    client, headers, state, _ = canonical
    save_results(LEGACY)
    save_results(LEGACY, 'another-user')
    with legacy_results() as session:
        original = session.scalar(select(Judgment))
        values = {column.key: getattr(original, column.key) for column in Judgment.__table__.columns if column.key != 'id'}
        values.update(researcher_id=None, author='UNASSIGNED_RECORD', version=99,
                      contradiction={'bull': {'claim': 'UNASSIGNED_PRIVATE_BODY'}, 'bear': {'claim': ''}})
        session.add(Judgment(**values))
        session.commit()
    delegated(state, ('judgments',))
    response = client.get(page(LEGACY, '/sn/judgment'), headers=headers)
    assert response.status_code == 200, response.text
    assert 'researcher original thesis' in response.text
    assert 'UNASSIGNED_PRIVATE_BODY' not in response.text and 'UNASSIGNED_RECORD' not in response.text
    assert 'another-user' not in response.text and 'v99' not in response.text


def test_legacy_judgment_requires_current_owner_and_page_grant(canonical):
    client, headers, state, _ = canonical
    save_results(LEGACY)
    delegated(state, ('judgments',))
    state['owner_missing'] = True
    missing = client.get(page(LEGACY, '/sn/judgment'), headers=headers)
    assert missing.status_code == 200 and '主人归属信息尚未同步' in missing.text
    assert 'researcher original thesis' not in missing.text
    state['owner_missing'] = False
    delegated(state, ('diagram',))
    ungranted = client.get(page(LEGACY, '/sn/judgment'), headers=headers)
    assert ungranted.status_code == 200 and '来源未授权' in ungranted.text
    assert 'researcher original thesis' not in ungranted.text


def test_new_workbench_never_inherits_legacy_owner_judgments(canonical):
    client, headers, state, _ = canonical
    save_results(LEGACY)
    delegated(state, ('judgments',))
    response = client.get(page(SECOND, '/sn/judgment'), headers=headers)
    assert response.status_code == 200, response.text
    assert '尚未登记判断' in response.text and 'researcher original thesis' not in response.text


def test_bound_legacy_owner_records_remain_read_only(canonical):
    client, headers, state, _ = canonical
    save_results(LEGACY)
    delegated(state, ('judgments',))
    response = client.post(page(LEGACY, '/sn/judgment/import'), headers=headers,
                           data={'text': 'must not save', 'author': 'visitor'})
    assert response.status_code == 403, response.text


def test_single_bound_legacy_home_uses_only_shared_owner_records(canonical):
    client, headers, state, _ = canonical
    save_results(LEGACY)
    save_results(LEGACY, 'another-user')
    delegated(state, ('home', 'judgments'))
    response = client.get(page(LEGACY, '/'), headers={**headers, 'Authorization': 'Bearer source-viewer'})
    assert response.status_code == 200, response.text
    assert 'researcher original brief' in response.text and 'another-user' not in response.text
    assert not state.get('bounded_requests')
