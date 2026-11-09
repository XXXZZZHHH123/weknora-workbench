"""Metadata-only source status under the same live grants as native charts."""
from copy import deepcopy
import json
import shutil

import pytest

from structured_data import research_source_status
from structured_data.store import DataStore
from test_research_native_charts import AUTHORIZATION, research_charts, signed as chart_signed

PATH = '/api/research/source-status'


def signed(body=None, **kwargs):
    return chart_signed({} if body is None else body, path=PATH, **kwargs)


def read(fixture):
    response = fixture.client.post(PATH, **signed())
    assert response.status_code == 200, response.text
    return response.json()


def save(source):
    source.location.write_text(json.dumps(source.review))


def mapped_sheet(name, status, count):
    return {'name': name, 'status': status, 'mapping': {'sheet': name, 'blocks': [
        {'columns': [{'column': str(index), 'metric': 'PRIVATE FIELD'} for index in range(count)]}]},
        'preview': [{'value': 'PRIVATE CELL'}], 'review_state': {'status': 'confirmed'}}


@pytest.mark.parametrize('mode', ['unsigned', 'bad_signature', 'wrong_path', 'unsigned_authorization'])
def test_status_requires_path_and_bearer_bound_signature(research_charts, mode):
    request = signed()
    if mode == 'unsigned':
        request = {'json': {}, 'headers': {'Authorization': AUTHORIZATION}}
    elif mode == 'bad_signature':
        request['headers']['X-Research-Signature'] = '0' * 64
    elif mode == 'wrong_path':
        request = chart_signed({})
    else:
        request['headers']['Authorization'] = 'Bearer another-user'
    assert research_charts.client.post(PATH, **request).status_code == 401
    assert research_charts.state['checks'] == 0


@pytest.mark.parametrize('mode', ['delegated', 'unavailable', 'metadata_restricted'])
def test_status_rejects_restricted_whole_workbench_without_partial_result(research_charts, mode):
    state = research_charts.state
    if mode == 'delegated':
        state['sources'][1]['delegated'] = True
    elif mode == 'unavailable':
        state['unavailable'] = [{'id': 'hidden-source', 'status': 'active'}]
    else:
        state['can_read_source'] = False
    response = research_charts.client.post(PATH, **signed())
    assert response.status_code == 403
    assert 'sources' not in response.json()


@pytest.mark.parametrize('change,expected', [({'user_id': 'other'}, 403), ({'tenant': 10}, 403),
    ({'id': 'other-workbench'}, 403), ({'revision': 8}, 409), ({'inventory': False}, 503), ({'status': 403}, 403)])
def test_status_requires_current_source_inventory_and_identity(research_charts, change, expected):
    research_charts.state.update(change)
    response = research_charts.client.post(PATH, **signed())
    assert response.status_code == expected and 'sources' not in response.json()


def test_status_rejects_legacy_scope_before_loading_sources(research_charts):
    response = research_charts.client.post(PATH, **signed(legacy=True))
    assert response.status_code == 409 and research_charts.state['checks'] == 0


def test_status_preserves_raw_states_counts_fields_and_never_returns_cell_or_field_contents(research_charts, monkeypatch):
    first, second = research_charts.first, research_charts.second
    first.review['sheets'] = [mapped_sheet('Data', 'confirmed', 2), mapped_sheet('Pending', 'needs_review', 3),
                             mapped_sheet('Figures', 'auto_ready', 1)]
    second.review['sheets'] = [mapped_sheet('Data', 'template_ready', 2), mapped_sheet('Pending', 'trusted_existing', 1),
                              {'name': 'Figures', 'status': 'needs_review', 'layout': 'chart_or_image', 'rows': 1},
                              {'name': 'Empty', 'status': 'empty'}]
    save(first)
    save(second)
    monkeypatch.setattr(DataStore, '_connect', lambda *args, **kwargs: pytest.fail('status must never query observations'))
    document = read(research_charts)
    assert set(document) == {'sources', 'summary'}
    assert document['summary'] == {'confirmed': 1, 'pending': 4, 'pending_fields': 7, 'pending_without_fields': 0}
    a, b = document['sources']
    assert set(a) == {'knowledge_id', 'source_file', 'sheets', 'summary'}
    assert a['knowledge_id'] == 'original-a' and a['source_file'] == 'fixture-a.xlsx'
    assert a['sheets'] == [{'name': 'Data', 'status': 'confirmed', 'field_count': 2},
                           {'name': 'Pending', 'status': 'needs_review', 'field_count': 3},
                           {'name': 'Figures', 'status': 'auto_ready', 'field_count': 1}]
    assert a['summary'] == {'confirmed': 1, 'pending': 2, 'pending_fields': 4, 'pending_without_fields': 0}
    assert b['summary'] == {'confirmed': 0, 'pending': 2, 'pending_fields': 3, 'pending_without_fields': 0}
    assert all(set(sheet) == {'name', 'status', 'field_count'} for source in document['sources'] for sheet in source['sheets'])
    assert 'PRIVATE' not in json.dumps(document) and 'value' not in json.dumps(document)
    assert research_charts.state['checks'] == 2


def test_status_includes_unpublished_reviews_and_counts_matrix_metrics_instead_of_period_columns(research_charts):
    first = research_charts.first
    shutil.copyfile(first.store._path(first.review['dataset_id']) / 'source.xlsx',
                    first.location.parent / first.review['source_file'])
    first.review.update(dataset_id=None, sha256=first.parsed['sha256'])
    matrix = mapped_sheet('Pending', 'needs_review', 5)
    matrix['mapping']['blocks'][0].update(layout='matrix', row_rules=[{'row': 2}, {'row': 3}])
    first.review['sheets'] = [mapped_sheet('Data', 'auto_ready', 2), matrix]
    save(first)
    source = read(research_charts)['sources'][0]
    assert source['sheets'][1]['field_count'] == 2
    assert source['summary'] == {'confirmed': 0, 'pending': 2, 'pending_fields': 4, 'pending_without_fields': 0}


def test_status_matches_review_page_for_skipped_and_unrecognized_sheets(research_charts):
    # The review page hides verified no-data sheets; a sheet without any
    # recognized field is still pending but has no field awaiting confirmation.
    first = research_charts.first
    first.review['sheets'] = [
        mapped_sheet('Data', 'needs_review', 2),
        {'name': 'Notes', 'status': 'needs_review', 'layout': 'unrecognized', 'rows': 5, 'mapping': {}},
        {'name': 'Blank values', 'status': 'needs_review', 'layout': 'unrecognized', 'rows': 5, 'mapping': {},
         'automatic_skip': {'code': 'no_indicator_data', 'message': 'fixture'}}]
    save(first)
    source = read(research_charts)['sources'][0]
    assert [sheet['field_count'] for sheet in source['sheets']] == [2, 0, 0]
    assert source['summary'] == {'confirmed': 0, 'pending': 2, 'pending_fields': 2, 'pending_without_fields': 1}


@pytest.mark.parametrize('change', ['hash', 'knowledge_id', 'deleted', 'removed_binding', 'binding_owner', 'binding_kb'])
def test_status_never_returns_other_or_stale_originals(research_charts, change):
    first = research_charts.first
    if change == 'hash':
        first.grant['knowledge'][0]['file_hash'] = 'e' * 32
    elif change == 'knowledge_id':
        first.grant['knowledge'][0]['id'] = 'not-approved'
    elif change == 'deleted':
        first.grant['knowledge'][0]['parse_status'] = 'deleting'
    elif change == 'removed_binding':
        research_charts.state['sources'] = [research_charts.second.grant]
    else:
        first.review['native_source_bindings'][0]['tenant_id' if change == 'binding_owner' else 'knowledge_base_id'] = (
            10 if change == 'binding_owner' else 'another-library')
        save(first)
    assert [source['knowledge_id'] for source in read(research_charts)['sources']] == ['original-b']


def test_status_uses_each_source_owners_namespace_and_deduplicates_bindings(research_charts):
    duplicate = deepcopy(research_charts.second.grant)
    duplicate['id'] = 'duplicate-binding'
    research_charts.state['sources'] = [duplicate, research_charts.first.grant, research_charts.second.grant]
    assert {source['knowledge_id'] for source in read(research_charts)['sources']} == {'original-a', 'original-b'}
    research_charts.state['sources'] = [dict(research_charts.second.grant, tenant_id=9)]
    assert read(research_charts)['sources'] == []


@pytest.mark.parametrize('change', ['hash', 'removed_binding', 'permission', 'revision', 'delegated', 'revoked'])
def test_status_rechecks_scope_before_returning_metadata(research_charts, change):
    state = research_charts.state

    def update():
        if change == 'hash':
            research_charts.first.grant['knowledge'][0]['file_hash'] = 'e' * 32
        elif change == 'removed_binding':
            state['sources'] = []
        elif change == 'permission':
            research_charts.first.grant['permission'] = 'editor'
        elif change == 'revision':
            state['revision'] = 8
        elif change == 'delegated':
            research_charts.first.grant['delegated'] = True
        else:
            state['status'] = 403
    state['on_recheck'] = update
    response = research_charts.client.post(PATH, **signed())
    assert response.status_code == 409 and 'sources' not in response.json()


def test_status_rechecks_review_identity_after_reading(research_charts, monkeypatch):
    original = research_source_status.sheet_field_count

    def change_review(sheet):
        result = original(sheet)
        research_charts.first.review['sheets'][0]['status'] = 'needs_review'
        save(research_charts.first)
        return result
    research_charts.first.review['sheets'][0]['mapping'] = {'blocks': [{'columns': [{}]}]}
    save(research_charts.first)
    monkeypatch.setattr(research_source_status, 'sheet_field_count', change_review)
    response = research_charts.client.post(PATH, **signed())
    assert response.status_code == 409 and 'sources' not in response.json()


def test_status_rejects_a_binding_that_does_not_match_current_original_bytes(research_charts):
    first = research_charts.first
    first.grant['knowledge'][0]['file_hash'] = 'e' * 32
    first.review['native_source_bindings'][0]['file_hash'] = 'e' * 32
    save(first)
    assert research_charts.client.post(PATH, **signed()).status_code == 409


@pytest.mark.parametrize('body', [[], None, {'source_bindings': ['binding-a']}, {'sources': []}, {'offset': 0}])
def test_status_does_not_accept_caller_selected_sources(research_charts, body):
    response = research_charts.client.post(PATH, **chart_signed(body, path=PATH))
    assert response.status_code == 400


def test_empty_status_grant_does_not_fall_back_to_old_sources(research_charts, monkeypatch):
    research_charts.state['sources'] = []
    monkeypatch.setattr(research_source_status, 'current_source_records', lambda *args, **kwargs: pytest.fail('empty grant opened a review'))
    assert read(research_charts) == {'sources': [], 'summary': {'confirmed': 0, 'pending': 0, 'pending_fields': 0, 'pending_without_fields': 0}}
