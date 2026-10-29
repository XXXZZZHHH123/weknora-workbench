"""Explicit empty associations retain text without inventing executable inputs."""
import pytest

from tests.test_judgment_generation import generator, generate, apply, rows, endpoint
from tests.test_knowledge_facts import canonical


@pytest.mark.parametrize('section', ['contradiction', 'marginal_focus', 'whitelist', 'thresholds', 'unresolved'])
def test_explicit_empty_binding_can_be_saved_without_restoring_model_suggestion(generator, section):
    client, headers, _, _, calls = generator
    preview = generate(client, headers).json()
    items = next(group['items'] for group in preview['review']['sections'] if group['id'] == section)
    item = next(item for item in items if item['can_bind'])
    model_calls = len(calls)
    response = client.post(endpoint('/review/update'), headers=headers, json={
        'draft_id': preview['draft_id'], 'revision': preview['review']['revision'],
        'item_id': item['id'], 'action': 'keep_text', 'series_id': None, 'remember': False})
    assert response.status_code == 200, response.text
    changed = response.json()
    current = next(
        entry for group in changed['review']['sections'] for entry in group['items'] if entry['id'] == item['id'])
    assert current['binding'] is None and current['status'] == 'kept_text'
    assert not current['requires_attention'] and not current['attention_fields']
    assert len(calls) == model_calls
    assert apply(client, headers, changed).status_code == 200
    saved = rows()[0]
    assert saved.status == '草稿'
    if section == 'contradiction': assert saved.contradiction['bull']['evidence'][0]['series_id'] is None
    elif section == 'marginal_focus': assert saved.marginal_focus[0]['series_id'] is None
    elif section == 'whitelist': assert saved.whitelist == []
    elif section == 'thresholds': assert saved.thresholds == [] and saved.unstructured
    else: assert saved.unstructured[0].get('series_id') is None


def test_empty_binding_cannot_be_stored_as_a_reusable_indicator_choice(generator):
    client, headers, _, _, _ = generator
    preview = generate(client, headers).json()
    item = next(item for group in preview['review']['sections'] for item in group['items'] if item['can_bind'])
    response = client.post(endpoint('/review/update'), headers=headers, json={
        'draft_id': preview['draft_id'], 'revision': preview['review']['revision'],
        'item_id': item['id'], 'action': 'keep_text', 'series_id': None, 'remember': True})
    assert response.status_code == 422 and rows() == []
