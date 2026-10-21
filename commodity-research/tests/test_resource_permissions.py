"""Independent source permission and shared-result ACL acceptance."""
from lxml.html import fromstring

from tests.test_knowledge_facts import canonical, api, page, FIRST, KB, SERIES


def test_workbench_view_does_not_grant_shared_diagram_write(canonical):
    client, headers, state, _ = canonical
    state['wb_edit'] = False
    address = page(FIRST, '/api/sn/diagram/templates')
    payload = {'name': 'Shared diagram', 'layout': {'nodes': [], 'edges': []}}
    denied = client.post(address, headers=headers, json=payload)
    assert denied.status_code == 403, denied.text
    assert client.post(page(FIRST, '/api/sn/diagram/markdown'), headers=headers,
                       json={'markdown': '# Shared diagram'}).status_code == 403
    state['wb_edit'] = True
    assert client.post(address, headers=headers, json=payload).status_code == 200


def test_readonly_diagram_get_does_not_create_shared_state(canonical):
    from sqlalchemy import func, select
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import DiagramTemplate
    client, headers, state, _ = canonical
    state['wb_edit'] = False
    response = client.get(page(FIRST, '/sn/diagram'), headers=headers)
    assert response.status_code == 200, response.text
    html = fromstring(response.text)
    assert html.get_element_by_id('dg-edit-btn').get('disabled') is not None
    with isolated_session_factory(17, FIRST)() as session:
        assert session.scalar(select(func.count()).select_from(DiagramTemplate)) == 0


def test_approved_derived_read_works_without_raw_knowledge_base_access(canonical):
    client, headers, state, _ = canonical
    state['source_delegated'] = True
    result = client.get(api(FIRST, 'indicators/' + SERIES + '/observations'), headers=headers)
    assert result.status_code == 200, result.text
    assert result.json()['observations'][0]['value'] == 17.5
    overview = client.get(api(FIRST, 'overview'), headers=headers)
    assert overview.status_code == 200, overview.text
    assert overview.json()['source_access'] == 'derived'
    assert overview.json()['counts']['indicators'] == 1
    assert overview.json()['source_inventory']['sources'] == []
    assert client.get(api(FIRST, 'sources'), headers=headers).status_code == 403
    before = len(state['calls'])
    assert client.get(api(), headers=headers).status_code == 200
    assert ('GET', '/api/v1/knowledge-bases/' + KB + '/knowledge') not in state['calls'][before:]
    state['source_consent_revoked'] = True
    denied = client.get(api(FIRST, 'indicators/' + SERIES + '/observations'), headers=headers)
    assert denied.status_code == 403 and '17.5' not in denied.text
