"""Model repair stays bounded and cannot waive source-ID checks."""
import asyncio

from fastapi import HTTPException
import pytest

from tin.web import diagram_generation as generation


def fields():
    return [{'id':'kb-'+letter*48,'name':'Synthetic '+letter,'unit':'kg'} for letter in 'abc']


def setup(monkeypatch, responses):
    catalog = fields()
    size = len(generation.catalog_json(catalog[:2]).encode())
    monkeypatch.setattr(generation, 'CATALOG_BYTES', size)
    calls = []
    async def complete(scope, model, prompt, image=None):
        calls.append({'prompt': prompt, 'image': image})
        result = responses.pop(0)
        if isinstance(result, Exception): raise result
        return result
    monkeypatch.setattr(generation, 'completion', complete)
    return catalog, calls


def test_invalid_shape_is_repaired_once_with_the_same_source_and_image(monkeypatch):
    catalog, calls = setup(monkeypatch, [
        {'nodes': []}, {'series_ids': ['kb-'+'b'*48]}, {'series_ids': []}])
    selected, subset = asyncio.run(generation.select_catalog(None, 'synthetic', catalog,
        'Complete synthetic source', 'data:image/png;base64,eA=='))
    assert subset is True and selected == [catalog[1]]
    assert len(calls) == 3
    assert all('Complete synthetic source' in call['prompt'] for call in calls)
    assert all(call['image'] == 'data:image/png;base64,eA==' for call in calls)
    assert 'selection_shape' in calls[1]['prompt']
    assert '只修正指标筛选' in calls[1]['prompt']


def test_repair_budget_is_shared_across_catalog_batches(monkeypatch):
    catalog, calls = setup(monkeypatch, [
        {'series_ids': ['invented']}, {'series_ids': [fields()[0]['id']]}, {'series_ids': ['invented']}])
    with pytest.raises(HTTPException) as caught:
        asyncio.run(generation.select_catalog(None, 'synthetic', catalog, 'Source', None))
    assert caught.value.status_code == 422
    assert len(calls) == 3
    assert '目录' in caught.value.detail


def test_two_invalid_plans_stop_without_replacing_them_with_an_empty_selection(monkeypatch):
    catalog, calls = setup(monkeypatch, [{'series_ids': ['invented']}] * 2)
    with pytest.raises(HTTPException) as caught:
        asyncio.run(generation.select_catalog(None, 'synthetic', catalog, 'Source', None))
    assert caught.value.status_code == 422 and len(calls) == 2
    assert '目录' in caught.value.detail


@pytest.mark.parametrize('status', [400, 401, 403, 409, 429, 503])
def test_permission_source_and_transport_failures_never_get_an_extra_model_call(monkeypatch, status):
    catalog, calls = setup(monkeypatch, [HTTPException(status, 'Application-authored failure')])
    with pytest.raises(HTTPException) as caught:
        asyncio.run(generation.select_catalog(None, 'synthetic', catalog, 'Source', None))
    assert caught.value.status_code == status and len(calls) == 1
