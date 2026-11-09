"""Chart pins remain immutable personal judgment evidence, with exact-ID links."""
from datetime import date, datetime, timezone
from types import SimpleNamespace

import pytest
from pydantic import ValidationError

from tests.test_chart_api import chart_client, create, SECOND_WB
from tests.test_workbench_sources_v2 import WB
from tests.chart_fixtures import SID, OTHER, definition
from tin.integration.workbench_scope import artifact_session, current_workbench, isolated_session_factory
from tin.judgments.service import save_version, to_payload
from tin.models import Researcher
from tin.schemas.judgment import JudgmentIn
from tin.web import judgment_charts
from tin.web.judgment_generation import serialize


def payload(references=None):
    return {'variety': 'XY', 'author': 'first', 'written_at': '2023-01-03',
        'contradiction': {'bull': {'claim': 'Supply', 'evidence': [{'desc': 'Observed source', 'series_id': SID}]},
            'bear': {'claim': 'Demand'}},
        'thresholds': [{'id': 'watch', 'name': 'Same label is no proof', 'series_id': SID, 'operator': 'between',
            'value': [8, 15], 'type': '观察位', 'horizon': '短期'}],
        'chart_references': references or []}


@pytest.fixture
def client(chart_client):
    client, state = chart_client
    state.app.include_router(judgment_charts.router)

    @state.app.post('/seed-judgment')
    def seed(document: dict):
        scope = current_workbench.get()
        with artifact_session(scope) as session:
            session.info.update(knowledge_facts=True, knowledge_facts_catalog=state.fields)
            row = save_version(session, JudgmentIn.model_validate(document), actor=scope.identity.user_id)
            result = serialize(row, session, scope)
            session.commit()
            return result

    return client, state


def test_schema_rejects_nonexistent_or_ambiguous_attachment_targets():
    for target in ({'kind': 'threshold', 'id': 'not-there'}, {'kind': 'evidence', 'side': 'bear', 'index': 0},
            {'kind': 'evidence', 'side': 'bull', 'index': True}, {'kind': 'judgment', 'id': 'extra'}):
        with pytest.raises(ValidationError):
            JudgmentIn.model_validate(payload([{'kind': 'chart', 'id': 'chart-id', 'revision': 1, 'target': target}]))


def test_save_freezes_server_fields_and_roundtrips_public_refs_without_migration(client):
    browser, _ = client
    chart = create(browser)
    reference = {'kind': 'chart', 'id': chart['id'], 'revision': 1, 'title': 'forged',
        'source_version': 'forged', 'target': {'kind': 'evidence', 'side': 'bull', 'index': 0}}
    response = browser.post('/seed-judgment', json=payload([reference]))
    assert response.status_code == 200, response.text
    data = response.json(); pin = data['chart_references'][0]
    assert pin['title'] == chart['title'] and pin['source_version'] != 'forged'
    assert pin['revision'] == 1 and pin['as_of'] == data['written_at'] == '2023-01-03'
    assert pin['definition']['curves'][0]['series_id'] == SID
    assert pin['source_versions'][SID] and pin['definition_summary']
    assert pin['target'] == reference['target']
    saved = browser.get('/api/sn/judgment/library-references', params={'judgment_id': data['id']})
    assert saved.status_code == 200, saved.text
    assert saved.json()['references'] == data['chart_references']
    assert saved.json()['rules'][0]['value'] == [8, 15]


def test_related_charts_use_only_ids_and_exclude_deleted_or_foreign_records(client):
    browser, _ = client
    match = create(browser, title='Unrelated title')
    other_definition = definition(title='Same label is no proof')
    other_definition['curves'][0]['series_id'] = OTHER
    other = browser.post('/api/sn/charts', json=other_definition).json()
    deleted = create(browser, title='Deleted same-ID chart')
    assert browser.request('DELETE', '/api/sn/charts/' + deleted['id'], json={'revision': 1}).status_code == 200
    record = browser.post('/seed-judgment', json=payload()).json()
    result = browser.get('/api/sn/judgment/library-references', params={'judgment_id': record['id']})
    assert result.status_code == 200, result.text
    assert [row['id'] for row in result.json()['related']['watch']] == [match['id']]
    # Chart transport uses one display email for all actors; pre-register the
    # second personal owner with its distinct account email.
    with isolated_session_factory(17, WB)() as session:
        session.add(Researcher(username='wk:second', weknora_user_id='second', email='second@test',
            display_name='Second', varieties=[], status='在职', created_at=datetime.now(timezone.utc)))
        session.commit()
    assert browser.get('/api/sn/judgment/library-references', params={'judgment_id': record['id']}, headers={'x-actor': 'second'}).status_code == 404
    assert browser.get('/api/sn/judgment/library-references', params={'judgment_id': record['id']}, headers={'x-workbench': SECOND_WB}).status_code == 404
    restricted = browser.get('/api/sn/judgment/library-references', params={'judgment_id': record['id']}, headers={'x-restricted': 'true'})
    assert restricted.status_code == 403 and restricted.json()['detail'] == '图表引用暂不对受限视角开放'


def test_editing_references_saves_new_version_and_preserves_original(client):
    browser, _ = client
    chart = create(browser)
    original = browser.post('/seed-judgment', json=payload()).json()
    response = browser.post('/api/sn/judgment/library-references', json={
        'judgment_id': original['id'], 'base_version': 1,
        'references': [{'kind': 'chart', 'id': chart['id'], 'revision': 1, 'target': {'kind': 'threshold', 'id': 'watch'}}]})
    assert response.status_code == 200, response.text
    assert response.json()['version'] == 2 and response.json()['id'] != original['id']
    old = browser.get('/api/sn/judgment/library-references', params={'judgment_id': original['id']}).json()
    new = browser.get('/api/sn/judgment/library-references', params={'judgment_id': response.json()['id']}).json()
    assert old['references'] == [] and len(new['references']) == 1
    assert new['references'][0]['target'] == {'kind': 'threshold', 'id': 'watch'}


def test_restricted_payloads_and_board_blocks_never_expose_internal_pins():
    from tin.export.board import judgment_record_block
    document = JudgmentIn.model_validate(payload([{'kind': 'chart', 'id': 'shared-chart', 'revision': 1,
        'title': 'Private frozen chart title'}])).model_dump(mode='python', by_alias=True)
    references = document.pop('chart_references')
    document['contradiction']['chart_references'] = references
    row = SimpleNamespace(**document, id=1, version=1, status='草稿',
        review_due=date(2023, 2, 3), created_at=datetime.now(timezone.utc))
    assert to_payload(row).chart_references == references
    token = current_workbench.set(SimpleNamespace(restricted=True))
    try:
        assert to_payload(row).chart_references == []
        assert serialize(row, scope=SimpleNamespace(restricted=True))['chart_references'] == []
        block = judgment_record_block(row, date(2023, 1, 3))
        assert 'chart_references' not in block['contradiction']
        assert 'Private frozen chart title' not in str(block)
        assert row.contradiction['chart_references'] == references
    finally:
        current_workbench.reset(token)
