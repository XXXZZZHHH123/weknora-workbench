"""The compact wire format preserves real field bindings and source counts."""
import json

import httpx
import pytest

from tests.test_knowledge_facts import canonical, api, SERIES
from tin.integration import kb_facts


@pytest.fixture
def compact(canonical, monkeypatch):
    client, headers, state, root = canonical
    original = httpx.request

    def request(method, url, **kwargs):
        response = original(method, url, **kwargs)
        if url.endswith('/api/research/facts') and response.status_code == 200:
            body = json.loads(kwargs['content'])
            if body.get('encoding') == kb_facts.FACT_ENCODING:
                document = response.json()
                for dataset in document['datasets']:
                    dataset['raw_observation_count'] = document['total'] + 3
                document['encoding'] = kb_facts.FACT_ENCODING
                document['row_columns'] = list(kb_facts.FACT_COLUMNS)
                document['rows'] = [[row.get(name) for name in kb_facts.FACT_COLUMNS] for row in document['rows']]
                if state.get('corrupt'):
                    state['corrupt'](document)
                return httpx.Response(200, json=document, request=response.request)
        return response

    monkeypatch.setattr(httpx, 'request', request)
    return canonical


def test_compact_values_and_raw_dataset_counts_are_preserved(compact):
    client, headers, state, _ = compact
    state['value'] = 0
    state['paginate'] = True
    result = client.get(api(suffix='indicators/' + SERIES + '/observations'), headers=headers)
    assert result.status_code == 200, result.text
    assert [row['value'] for row in result.json()['observations']] == [0, 0]
    overview = client.get(api(suffix='overview'), headers=headers)
    assert overview.status_code == 200
    assert overview.json()['counts']['observations'] == 2
    from tin.integration.identity import Identity, current_identity
    from tin.integration.workbench_scope import WorkbenchScope, workbench_session
    from tests.test_knowledge_facts import FIRST, KB
    identity = Identity('researcher', 'test', 'Test', 17, 'owner')
    scope = WorkbenchScope(identity, FIRST, KB, 17, {'workspace_mode': 'isolated'}, 'Bearer source-user')
    marker = current_identity.set(identity)
    try:
        with workbench_session(scope) as session:
            assert session.info['knowledge_facts_datasets'][0]['observation_count'] == 5
    finally:
        current_identity.reset(marker)


@pytest.mark.parametrize('corrupt', [
    lambda doc: doc.update(encoding='unknown'),
    lambda doc: doc['row_columns'].reverse(),
    lambda doc: doc.pop('encoding'),
    lambda doc: doc['rows'][0].append('extra'),
    lambda doc: doc['rows'][0].__setitem__(0, 'kb-' + 'f' * 48),
    lambda doc: doc['rows'][0].__setitem__(0, 0),
    lambda doc: doc['rows'][0].__setitem__(1, None),
    lambda doc: doc['rows'][0].__setitem__(2, None),
    lambda doc: doc['rows'][0].__setitem__(5, 'missing'),
    lambda doc: doc['datasets'][0].pop('raw_observation_count'),
    lambda doc: doc['datasets'][0].update(raw_observation_count=True),
    lambda doc: doc['datasets'][0].update(raw_observation_count=0),
    lambda doc: doc['datasets'].append(dict(doc['datasets'][0])),
    lambda doc: doc['datasets'][0].update(source_knowledge_id='another-original'),
])
def test_malformed_compact_pages_never_publish_a_cache(compact, corrupt):
    client, headers, state, root = compact
    state['corrupt'] = corrupt
    response = client.get(api(), headers=headers)
    assert response.status_code == 503, response.text
    assert list(root.rglob('*.facts.sqlite')) == []


def test_catalog_binding_is_required_on_every_compact_page(compact):
    client, headers, state, root = compact
    state['paginate'] = True
    def corrupt(document):
        if document['offset']:
            document['indicators'][0]['source_knowledge_id'] = 'changed-original'
    state['corrupt'] = corrupt
    response = client.get(api(), headers=headers)
    assert response.status_code == 409
    assert list(root.rglob('*.facts.sqlite')) == []
