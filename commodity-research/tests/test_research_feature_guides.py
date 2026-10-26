"""Feature tours target the authenticated native page and real empty-state controls."""
import json
import re

from tests.test_isolated_workbenches import isolated, WB, KB


def test_feature_pages_publish_exact_scope_and_real_start_controls(isolated):
    client, headers, state, _ = isolated
    assert client.post('/research/auth/session', headers=headers).status_code == 200
    anchors = {
        '/sn': ['market-core-open', 'market-core-search', 'market-core-selected'],
        '/sn/indicators': ['indicator-search', 'indicator-favorites-title'],
        '/sn/judgment': ['judgment-empty', 'judgment-manager', 'logic-input', 'logic-model'],
        '/sn/diagram': ['dg-md-btn', 'dg-edit-btn', 'dg-wrap', 'dg-md-text'],
    }
    for suffix, expected in anchors.items():
        response = client.get(f'/research/workbenches/{WB}{suffix}')
        assert response.status_code == 200
        match = re.search(r'window.RESEARCH_CONTEXT = (.*?);', response.text)
        assert match
        assert json.loads(match.group(1)) == {
            'tenant': 17, 'user': 'writer', 'workbench': WB, 'knowledge_base': KB,
            'scope_version': 1, 'sources_revision': 0,
        }
        for anchor in expected:
            assert f'id="{anchor}"' in response.text
    assert not any('generate' in path for _, path in state['calls'])
