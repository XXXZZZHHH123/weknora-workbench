"""Typed human research imports keep their product and actual authorized IDs."""
import copy

from sqlalchemy import select

from tin.models import Judgment
from tin.models.workbench import WorkbenchArtifact
from tin.integration.workbench_scope import isolated_session_factory
from tests.test_knowledge_facts import canonical, FIRST, page
from tests.test_judgment_generation import SERIES


def payload():
    return {'variety': 'XY', 'author': 'untrusted name', 'written_at': '2026-09-18',
        'contradiction': {'bull': {'claim': 'Measured supply', 'evidence': [{'desc': 'Authorized source', 'series_id': SERIES}]},
                          'bear': {'claim': 'Demand not yet verified', 'evidence': [{'desc': 'Missing survey'}]}},
        'marginal_focus': [{'desc': 'Verified inventory', 'series_id': SERIES}],
        'pricing_power': {'category': '有境外市场', 'short': ['Measured structure'], 'long': ['Production and demand']},
        'whitelist': [SERIES], 'tone': '观望', 'unstructured': [{'section': 'conditions', 'text': 'Needs review', 'reason': 'Unit not specified'}]}


def path(suffix):
    return page(FIRST, '/api/sn/judgment' + suffix)


def saved():
    with isolated_session_factory(17, FIRST)() as session:
        return list(session.scalars(select(Judgment)))


def test_structured_import_previews_then_saves_explicit_product_without_any_model(canonical):
    client, headers, *_ = canonical
    response = client.post(path('/structured/preview'), headers=headers, json={'payload': payload()})
    assert response.status_code == 200, response.text
    preview = response.json()
    assert saved() == [] and 'XY' in preview['markdown']
    assert preview['base_version'] == 0 and preview['kind'] == 'structured'
    result = client.post(path('/structured/apply'), headers=headers,
                         json={'draft_id': preview['draft_id'], 'base_version': preview['base_version']})
    assert result.status_code == 200, result.text
    row = saved()[0]
    assert row.variety == 'XY' and row.author == 'researcher' and row.status == '草稿'
    managed = client.get(path('/manage'), headers=headers)
    assert managed.status_code == 200
    assert managed.json()['variety'] == 'XY' and managed.json()['current']['version'] == 1
    assert client.post(path('/structured/apply'), headers=headers,
        json={'draft_id': preview['draft_id'], 'base_version': 0}).status_code == 409


def test_structured_import_rejects_unregistered_ids_instead_of_guessing_by_name(canonical):
    client, headers, *_ = canonical
    document = payload(); document['whitelist'].append('kb-' + 'f' * 48)
    response = client.post(path('/structured/preview'), headers=headers, json={'payload': document})
    assert response.status_code in (403, 422), response.text
    assert saved() == []


def test_structured_import_cannot_silently_activate_or_accept_unknown_protocol(canonical):
    client, headers, *_ = canonical
    response = client.post(path('/structured/preview'), headers=headers,
        json={'payload': copy.deepcopy(payload()), 'activate': True})
    assert response.status_code == 422
    assert saved() == []


def test_multiple_imports_keep_independent_markdown_and_open_the_selected_draft(canonical):
    client, headers, *_ = canonical
    identifiers = []
    for title in ['First research case', 'Second research case']:
        document = payload()
        document['contradiction']['bull']['claim'] = title
        preview = client.post(path('/structured/preview'), headers=headers,
            json={'payload': document, 'panel_title': title})
        assert preview.status_code == 200, preview.text
        proposed = preview.json()
        response = client.post(path('/structured/apply'), headers=headers,
            json={'draft_id': proposed['draft_id'], 'base_version': proposed['base_version']})
        assert response.status_code == 200, response.text
        result = response.json()
        identifiers.append(result['id'])
        assert result['panel']['title'] == title and result['panel']['judgment_id'] == result['id']
    assert identifiers[0] != identifiers[1]
    for identifier, title in zip(identifiers, ['First research case', 'Second research case']):
        managed = client.get(path('/manage?judgment_id=' + str(identifier)), headers=headers)
        assert managed.status_code == 200, managed.text
        assert managed.json()['current']['id'] == identifier
        assert title in managed.json()['current']['markdown']
        assert managed.json()['latest'] == {'version': 2}
    with isolated_session_factory(17, FIRST)() as session:
        assert len(list(session.scalars(select(Judgment)))) == 2
        assert len(list(session.scalars(select(WorkbenchArtifact).where(WorkbenchArtifact.module == 'judgments')))) == 2
    assert all(row.status == '草稿' for row in saved())
