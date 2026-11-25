"""Structured human previews validate only current configured dimension IDs."""
from datetime import datetime, timezone

import pytest

from tests.test_knowledge_facts import canonical, FIRST
from tests.test_structured_judgment_import import payload, path, saved
from tin.integration.workbench_scope import isolated_session_factory
from tin.models import SourceMapping
from tin.web.research_dimensions import DIMENSIONS_KEY


CUSTOM = [{'id': 'desk_7', 'label': '自定义成本', 'category': '供给', 'description': 'Synthetic desk definition'}]


def configure(dimensions):
    with isolated_session_factory(17, FIRST)() as session:
        session.merge(SourceMapping(id=DIMENSIONS_KEY + ':' + FIRST, config={'dimensions': dimensions},
            confirmed_by='synthetic', confirmed_at=datetime.now(timezone.utc)))
        session.commit()


@pytest.mark.parametrize('custom,identifier,label', [(False, 'price', '价格与价差'), (True, 'desk_7', '自定义成本')])
def test_structured_preview_uses_current_dimensions_and_preserves_the_tag_on_save(canonical, custom, identifier, label):
    browser, headers, *_ = canonical
    if custom:
        configure(CUSTOM)
    document = payload()
    document['contradiction']['bull']['evidence'][0]['dimension'] = identifier
    response = browser.post(path('/structured/preview'), headers=headers, json={'payload': document})
    assert response.status_code == 200, response.text
    proposed = response.json()
    assert label + '（`' + identifier + '`）' in proposed['markdown']
    assert any(row['id'] == identifier and row['label'] == label for row in proposed['research_dimensions'])
    assert saved() == []
    result = browser.post(path('/structured/apply'), headers=headers,
        json={'draft_id': proposed['draft_id'], 'base_version': proposed['base_version']})
    assert result.status_code == 200, result.text
    assert saved()[0].contradiction['bull']['evidence'][0]['dimension'] == identifier


def test_structured_preview_rejects_removed_ids_and_apply_rechecks_current_list(canonical):
    browser, headers, *_ = canonical
    configure(CUSTOM)
    document = payload()
    document['contradiction']['bull']['evidence'][0]['dimension'] = 'supply'
    invalid = browser.post(path('/structured/preview'), headers=headers, json={'payload': document})
    assert invalid.status_code == 422 and '维度「供给」已不在本研究台' in invalid.json()['detail']
    document['contradiction']['bull']['evidence'][0]['dimension'] = 'desk_7'
    proposed = browser.post(path('/structured/preview'), headers=headers, json={'payload': document}).json()
    configure([{**CUSTOM[0], 'id': 'other_7'}])
    stale = browser.post(path('/structured/apply'), headers=headers,
        json={'draft_id': proposed['draft_id'], 'base_version': proposed['base_version']})
    assert stale.status_code == 422 and '维度「desk_7」已不在本研究台' in stale.json()['detail']
    assert saved() == []
