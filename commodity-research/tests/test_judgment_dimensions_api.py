"""Synthetic model suggestions never write content; manual labels use version CAS."""
from copy import deepcopy
from datetime import datetime, timezone

from fastapi import HTTPException
import pytest
from sqlalchemy import select

from tests.test_judgment_generation import generator, generate, apply, endpoint, rows
from tests.test_judgment_review import update, all_items
from tests.test_judgment_panels import panels, seed
from tests.test_knowledge_facts import canonical, FIRST, SECOND, page
from tin.integration.workbench_scope import isolated_session_factory
from tin.judgments.service import to_payload
from tin.models import AuditLog, SourceMapping
from tin.web import judgment_generation as generation
from tin.web.research_dimensions import DIMENSIONS_KEY


def saved(generator):
    client, headers, _, _, _ = generator
    preview = generate(client, headers).json()
    result = apply(client, headers, preview)
    assert result.status_code == 200, result.text
    return client, headers, rows()[0]


def items(client, headers):
    response = client.get(endpoint('/manage'), headers=headers)
    assert response.status_code == 200, response.text
    return response.json()['current']['dimension_items']


def custom_dimensions():
    with isolated_session_factory(17, FIRST)() as session:
        session.add(SourceMapping(id=f'{DIMENSIONS_KEY}:{FIRST}', config={'dimensions': [
            {'id': 'desk-flow', 'label': '合成流量', 'category': '其他', 'description': 'Synthetic desk classification'}]},
            version=1, confirmed_by='researcher', confirmed_at=datetime.now(timezone.utc)))
        session.commit()


def model_suggestions(monkeypatch, *, dimension='inventory', failure=False):
    calls = []
    async def complete(scope, model, prompt):
        calls.append(prompt)
        if failure:
            raise HTTPException(503, 'Synthetic model failure')
        return {'suggestions': [
            {'section': 'supporting', 'index': 0, 'dimension': dimension, 'reason': 'Synthetic evidence context'},
            {'section': 'thresholds', 'index': 0, 'dimension': dimension, 'reason': 'Synthetic rule context'}]}
    monkeypatch.setattr(generation, 'completion', complete)
    return calls


def suggest(client, headers, row):
    return client.post(endpoint('/dimension-suggestions'), headers=headers,
        json={'judgment_id': row.id, 'version': row.version, 'model_id': 'synthetic-chat'})


@pytest.mark.parametrize('custom', [False, True])
def test_suggestions_are_model_only_audited_and_unsaved_until_explicit_dimension_save(generator, monkeypatch, custom):
    client, headers, original = saved(generator)
    if custom:
        custom_dimensions()
    identifier = 'desk-flow' if custom else 'inventory'
    calls = model_suggestions(monkeypatch, dimension=identifier)
    before = to_payload(original).model_dump(mode='json')
    response = suggest(client, headers, original)
    assert response.status_code == 200, response.text
    suggestions = response.json()['suggestions']
    assert all(item['dimension'] == identifier for item in suggestions)
    assert '【本研究台的研究维度】' in calls[0] and original.source_note in calls[0]
    assert ('合成流量' if custom else '库存') in calls[0]
    assert len(rows()) == 1 and to_payload(rows()[0]).model_dump(mode='json') == before
    with isolated_session_factory(17, FIRST)() as session:
        audit = session.scalar(select(AuditLog).where(AuditLog.action == '研判维度建议'))
        assert audit.detail['status'] == 'succeeded' and audit.detail['model_id'] == 'synthetic-chat'
        assert audit.detail['suggestions'] == suggestions
    edits = [{key: value for key, value in item.items() if key != 'reason'} for item in suggestions]
    result = client.post(endpoint('/dimensions'), headers=headers,
        json={'judgment_id': original.id, 'base_version': original.version, 'items': edits})
    assert result.status_code == 200, result.text
    assert result.json()['version'] == 2 and result.json()['status'] == '草稿'
    after = to_payload(rows()[1]).model_dump(mode='json')
    assert after['contradiction']['bull']['evidence'][0].pop('dimension') == identifier
    assert after['thresholds'][0].pop('dimension') == identifier
    assert after == before
    assert to_payload(rows()[0]).model_dump(mode='json') == before
    assert client.post(endpoint('/dimensions'), headers=headers,
        json={'judgment_id': original.id, 'base_version': 1, 'items': edits}).status_code == 409


def test_invalid_model_dimension_is_unannotated_without_keyword_fallback(generator, monkeypatch):
    client, headers, original = saved(generator)
    model_suggestions(monkeypatch, dimension='not-a-current-dimension')
    response = suggest(client, headers, original)
    assert response.status_code == 200, response.text
    assert all(item['dimension'] is None for item in response.json()['suggestions'])
    assert len(rows()) == 1


def test_model_failure_is_reported_and_audited_without_saving_or_fixed_classification(generator, monkeypatch):
    client, headers, original = saved(generator)
    model_suggestions(monkeypatch, failure=True)
    response = suggest(client, headers, original)
    assert response.status_code == 503 and response.json()['detail'] == 'Synthetic model failure'
    assert len(rows()) == 1
    with isolated_session_factory(17, FIRST)() as session:
        audit = session.scalar(select(AuditLog).where(AuditLog.action == '研判维度建议'))
        assert audit.detail['status'] == 'failed' and audit.detail['error_status'] == 503


@pytest.mark.parametrize('damage', ['content', 'missing', 'duplicate', 'position'])
def test_suggestion_protocol_refuses_content_and_mismatched_target_list(generator, monkeypatch, damage):
    client, headers, original = saved(generator)
    value = {'suggestions': [
        {'section': 'supporting', 'index': 0, 'dimension': 'inventory', 'reason': 'Synthetic'},
        {'section': 'thresholds', 'index': 0, 'dimension': 'inventory', 'reason': 'Synthetic'}]}
    if damage == 'content':
        value['judgment'] = {'tone': '偏多'}
    elif damage == 'missing':
        value['suggestions'].pop()
    elif damage == 'duplicate':
        value['suggestions'].append(deepcopy(value['suggestions'][0]))
    else:
        value['suggestions'][0]['index'] = 123
    async def complete(*args):
        return value
    monkeypatch.setattr(generation, 'completion', complete)
    response = suggest(client, headers, original)
    assert response.status_code == 422, response.text
    assert len(rows()) == 1


def test_suggestion_and_save_enforce_actor_workbench_and_restricted_permissions(generator, monkeypatch):
    client, headers, original = saved(generator)
    calls = model_suggestions(monkeypatch)
    edits = [{**item, 'dimension': 'inventory'} for item in items(client, headers)]
    edits = [{key: item[key] for key in ('section', 'index', 'dimension')} for item in edits]
    assert client.post(page(SECOND, '/api/sn/judgment/dimension-suggestions'), headers=headers,
        json={'judgment_id': original.id, 'version': 1, 'model_id': 'synthetic-chat'}).status_code == 404
    viewer = {**headers, 'Authorization': 'Bearer source-viewer'}
    assert suggest(client, viewer, original).status_code == 403
    assert client.post(endpoint('/dimensions'), headers=viewer,
        json={'judgment_id': original.id, 'base_version': 1, 'items': edits}).status_code == 403
    state = generator[2]
    state['source_delegated'] = True
    assert suggest(client, headers, original).status_code == 403
    assert client.post(endpoint('/dimensions'), headers=headers,
        json={'judgment_id': original.id, 'base_version': 1, 'items': edits}).status_code == 403
    assert calls == [] and len(rows()) == 1


def test_deleted_dimension_is_shown_and_requires_current_reassignment_on_save(generator):
    client, headers, original = saved(generator)
    with isolated_session_factory(17, FIRST)() as session:
        from tin.models import Judgment
        record = session.get(Judgment, original.id)
        record.contradiction = deepcopy(record.contradiction)
        record.contradiction['bull']['evidence'][0]['dimension'] = 'removed-dimension'
        session.commit()
    current = client.get(endpoint('/manage'), headers=headers).json()['current']
    assert '已删除维度' in current['markdown']
    edits = [{key: item[key] for key in ('section', 'index', 'dimension')} for item in current['dimension_items']]
    bad = client.post(endpoint('/dimensions'), headers=headers,
        json={'judgment_id': original.id, 'base_version': 1, 'items': edits})
    assert bad.status_code == 422
    edits[0]['dimension'] = 'supply'
    good = client.post(endpoint('/dimensions'), headers=headers,
        json={'judgment_id': original.id, 'base_version': 1, 'items': edits})
    assert good.status_code == 200, good.text
    assert rows()[1].contradiction['bull']['evidence'][0]['dimension'] == 'supply'


def test_dimension_endpoints_reject_another_writable_researcher_before_model_call(panels, monkeypatch):
    from tin.integration.workbench_scope import artifact_session
    from tin.web.judgment_dimensions import router
    client, state = panels
    client.app.include_router(router)
    monkeypatch.setattr(generation, 'result_session', artifact_session)
    identifier, version = seed(state)
    calls = model_suggestions(monkeypatch)
    assert client.post('/api/sn/judgment/dimension-suggestions', headers={'x-actor': 'second'},
        json={'judgment_id': identifier, 'version': version, 'model_id': 'synthetic-chat'}).status_code == 404
    assert client.post('/api/sn/judgment/dimensions', headers={'x-actor': 'second'},
        json={'judgment_id': identifier, 'base_version': version, 'items': []}).status_code == 404
    assert calls == []


def test_review_dimension_edit_preserves_status_content_and_manual_confirmation_priority(generator):
    client, headers, _, _, _ = generator
    preview = generate(client, headers).json()
    evidence = next(item for item in all_items(preview) if item['can_dimension'] and item['kind'] == 'binding')
    result = update(client, headers, preview, evidence, action='dimension', dimension='supply')
    assert result.status_code == 200, result.text
    after = result.json()
    changed = next(item for item in all_items(after) if item['id'] == evidence['id'])
    assert changed['dimension'] == 'supply' and changed['status'] == evidence['status']
    assert changed['description'] == evidence['description'] and changed['binding'] == evidence['binding']
    assert rows() == []
    assert update(client, headers, after, changed, action='dimension', dimension='missing').status_code == 422
    assert update(client, headers, after, changed, action='dimension', dimension='demand', series_id=None).status_code == 422
    confirmed = update(client, headers, after, changed, dimension='demand')
    assert confirmed.status_code == 200, confirmed.text
    assert apply(client, headers, confirmed.json()).status_code == 200
    assert rows()[0].contradiction['bull']['evidence'][0]['dimension'] == 'demand'
