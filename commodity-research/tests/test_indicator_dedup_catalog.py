"""Confirmed copies are hidden before pagination; historical IDs stay intact."""
from types import SimpleNamespace

import pytest
from fastapi import HTTPException

from tin.integration import indicator_dedup as dedup, page_facts
from tin.web.catalog import indicator_page


@pytest.fixture
def directory(monkeypatch):
    scope = SimpleNamespace(scope_version=2, restricted=False, sources=[{'source_type': 'central_dataset'}])
    fields = [{'series_id': f'field-{index:04d}', 'metric': f'Synthetic {index}', 'unit': 'unit'} for index in range(405)]
    mapping, calls = {}, []
    def raw(scope, offset=0, limit=200, search=None, series_ids=None, *, version=None, source_kind=None):
        assert limit <= 200
        assert version in (None, 'a' * 64)
        rows = [field for field in fields if series_ids is None or field['series_id'] in series_ids]
        if search:
            rows = [field for field in rows if search in field['metric']]
        selected = rows[offset:offset + limit]
        calls.append((offset, limit))
        return {'series': selected, 'version': 'a' * 64, 'datasets': [], 'offset': offset,
                'total': len(rows), 'next_offset': offset + len(selected) if offset + len(selected) < len(rows) else None,
                'truncated': offset + len(selected) < len(rows),
                'unavailable_series_ids': sorted(set(series_ids or []) - {field['series_id'] for field in rows})}
    monkeypatch.setattr(page_facts, '_raw_catalog_page', raw)
    monkeypatch.setattr(dedup, 'confirmed_replacements', lambda scope: mapping)
    monkeypatch.setattr(dedup, 'catalog_annotations', lambda scope, fields, **kwargs: [{**field, 'dedup_status': 'pending'} for field in fields])
    return scope, fields, mapping, calls


def test_filter_precedes_pagination_and_model_snapshot(directory):
    scope, fields, mapping, calls = directory
    mapping.update({field['series_id']: fields[-1]['series_id'] for field in fields[:200]})
    page = page_facts.catalog_page(scope)
    assert page['total'] == 205 and page['next_offset'] == 200
    assert [field['series_id'] for field in page['series']] == [field['series_id'] for field in fields[200:400]]
    following = page_facts.catalog_page(scope, offset=200, version=page['version'])
    assert len(following['series']) == 5 and following['next_offset'] is None
    calls.clear()
    snapshot = page_facts.catalog_snapshot(scope)
    assert len(snapshot['catalog']) == 205 and not set(mapping) & snapshot['catalog'].keys()
    assert len(calls) == 3, 'a complete model catalog traverses metadata pages only once'
    assert all(limit <= 200 for _, limit in calls)


def test_raw_catalog_keeps_copy_and_does_not_annotate(directory):
    scope, fields, mapping, _ = directory
    mapping[fields[0]['series_id']] = fields[1]['series_id']
    raw = page_facts.catalog_snapshot(scope, apply_dedup=False)
    assert len(raw['catalog']) == 405 and 'dedup_status' not in raw['catalog'][fields[0]['series_id']]
    selected = page_facts.catalog_page(scope, series_ids=[fields[0]['series_id']], apply_dedup=False)
    assert selected['series'] == fields[:1]
    hidden = page_facts.catalog_page(scope, series_ids=[fields[0]['series_id']])
    assert hidden['total'] == 0 and hidden['unavailable_series_ids'] == []


def test_decision_change_invalidates_filtered_directory_version(directory):
    scope, fields, mapping, _ = directory
    mapping[fields[0]['series_id']] = fields[-1]['series_id']
    original = page_facts.catalog_page(scope)
    mapping[fields[1]['series_id']] = fields[-1]['series_id']
    with pytest.raises(HTTPException) as error:
        page_facts.catalog_page(scope, version=original['version'])
    assert error.value.status_code == 409


def test_catalog_row_and_selector_metadata_keep_dedup_badges(directory):
    scope, fields, mapping, _ = directory
    mapping[fields[0]['series_id']] = fields[-1]['series_id']
    page = indicator_page(scope, search='Synthetic 1')
    assert page['indicators'] and all(row['dedup_status'] == 'pending' for row in page['indicators'])
    assert fields[0]['series_id'] not in {row['series_id'] for row in page['indicators']}


def test_restricted_scope_never_loads_correspondence(directory, monkeypatch):
    scope, _, _, _ = directory
    scope.restricted = True
    def forbidden(*args, **kwargs):
        raise AssertionError('restricted callers must not discover correspondences')
    monkeypatch.setattr(dedup, 'confirmed_replacements', forbidden)
    page = page_facts.catalog_page(scope)
    assert 'dedup_status' not in page['series'][0]
