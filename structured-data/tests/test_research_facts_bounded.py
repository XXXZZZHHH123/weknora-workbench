"""Page reads fetch only configured fields and the latest required points."""
import copy
import json
from contextlib import contextmanager

import pytest

from structured_data.store import DataStore
from test_knowledge_research_facts import PATH, facts, signed


@pytest.fixture
def bounded_facts(facts):
    client, store, parsed, path, review, body = facts
    parsed = copy.deepcopy(parsed)
    parsed['rule_version'] = 'synthetic-bounded-pages'
    parsed['series'].append({**parsed['series'][0], 'series_id': 'private', 'metric': 'Private sentinel'})
    parsed['observations'] = [
        {'series_id': series, 'date': f'2026-01-{day:02}',
         'value': day if series == 'quantity' else 987654321,
         'value_status': 'valid', 'source_cell': f'B{day + 1}', 'raw_value': str(day)}
        for series in ('quantity', 'private') for day in range(1, 6)
    ]
    review['dataset_id'] = store.publish(parsed)['dataset_id']
    path.write_text(json.dumps(review))
    full = client.post(PATH, **signed(body)).json()
    selected = next(field['series_id'] for field in full['indicators'] if field['source_series_id'] == 'quantity')
    private = next(field['series_id'] for field in full['indicators'] if field['source_series_id'] == 'private')
    return client, body, selected, private, path, review


def test_bounded_facts_filter_in_sql_and_return_only_required_fields(bounded_facts, monkeypatch):
    client, body, selected, private, _, _ = bounded_facts
    queries = []
    original_connect = DataStore._connect

    class TrackedConnection:
        def __init__(self, connection):
            self.connection = connection

        def execute(self, sql, parameters=None):
            queries.append((sql, list(parameters or [])))
            return self.connection.execute(sql, parameters or [])

    @contextmanager
    def tracked_connect(self, dataset_id):
        with original_connect(self, dataset_id) as connection:
            yield TrackedConnection(connection)

    monkeypatch.setattr(DataStore, '_connect', tracked_connect)
    response = client.post(PATH, **signed({**body, 'series_ids': [selected], 'points_per_series': 2, 'as_of': '2026-01-04'}))
    assert response.status_code == 200, response.text
    document = response.json()
    assert document['bounded'] is True
    assert [field['series_id'] for field in document['indicators']] == [selected]
    assert [(row['date'], row['value']) for row in document['rows']] == [('2026-01-03', 3), ('2026-01-04', 4)]
    assert document['total'] == 2 and document['next_offset'] is None and not document['truncated']
    assert private not in response.text and 'Private sentinel' not in response.text and '987654321' not in response.text
    row_queries = [(sql, params) for sql, params in queries if 'FROM observations' in sql]
    assert row_queries and all('quantity' in params and 'private' not in params for _, params in row_queries)
    assert all('series_id IN (' in sql for sql, _ in row_queries)


def test_empty_bindings_read_no_observation_rows(bounded_facts, monkeypatch):
    client, body, _, _, _, _ = bounded_facts
    def forbidden(*args, **kwargs):
        raise AssertionError('empty page bindings must not load the full knowledge base')
    monkeypatch.setattr(DataStore, '_connect', forbidden)
    response = client.post(PATH, **signed({**body, 'series_ids': []}))
    assert response.status_code == 200, response.text
    assert response.json()['rows'] == [] and response.json()['indicators'] == []


@pytest.mark.parametrize('changes', [
    {'series_ids': None}, {'series_ids': 'all'}, {'series_ids': ['*']},
    {'series_ids': ['kb-' + 'a' * 48] * 2}, {'series_ids': ['kb-' + 'a' * 48] * 257},
    {'points_per_series': 0}, {'points_per_series': 3}, {'points_per_series': True},
    {'as_of': '2026-02-30'}, {'as_of': '2026-1-1'}, {'as_of': None},
    {'offset': 1}, {'limit': 1}, {'metadata_only': True}, {'encoding': 'research-rows-v1'},
    {'view_module': 'all'}, {'view_module': []}, {'view_module': {}},
])
def test_bounded_facts_reject_ambiguous_or_unbounded_parameters(bounded_facts, changes):
    client, body, selected, _, _, _ = bounded_facts
    response = client.post(PATH, **signed({**body, 'series_ids': [selected], **changes}))
    assert response.status_code == 400, response.text


def test_unknown_or_withdrawn_binding_does_not_return_partial_results(bounded_facts):
    client, body, selected, _, path, review = bounded_facts
    response = client.post(PATH, **signed({**body, 'series_ids': [selected, 'kb-' + 'a' * 48]}))
    assert response.status_code == 403
    assert '987654321' not in response.text
    review['sheets'][0]['status'] = 'needs_review'
    path.write_text(json.dumps(review))
    assert client.post(PATH, **signed({**body, 'series_ids': [selected]})).status_code == 403


def test_selection_and_boundary_are_part_of_snapshot_identity(bounded_facts):
    client, body, selected, private, _, _ = bounded_facts
    first = client.post(PATH, **signed({**body, 'series_ids': [selected], 'as_of': '2026-01-04'})).json()
    for changes in ({'series_ids': [private]}, {'as_of': '2026-01-03'}, {'points_per_series': 1}):
        response = client.post(PATH, **signed({**body, 'series_ids': [selected], 'as_of': '2026-01-04', 'version': first['version'], **changes}))
        assert response.status_code == 409


def test_market_history_remains_selected_and_honors_the_cutoff(bounded_facts):
    client, body, selected, private, _, _ = bounded_facts
    response = client.post(PATH, **signed({**body, 'series_ids': [selected],
        'view_module': 'market', 'points_per_series': 180, 'as_of': '2026-01-04'}))
    assert response.status_code == 200, response.text
    document = response.json()
    assert [row['date'] for row in document['rows']] == [f'2026-01-{day:02}' for day in range(1, 5)]
    assert document['total'] == 4 and document['bounded'] is True
    assert private not in response.text and '987654321' not in response.text


@pytest.mark.parametrize('changes', [
    {'view_module': 'market', 'points_per_series': 181},
    {'view_module': 'diagram', 'points_per_series': 180},
    {'view_module': 'indicators', 'points_per_series': 180},
    {'points_per_series': 180},
    {'view_module': 'market', 'points_per_series': 3,
     'series_ids': ['kb-' + f'{number:048x}' for number in range(13)]},
])
def test_long_history_is_not_an_unbounded_page_projection(bounded_facts, changes):
    client, body, selected, _, _, _ = bounded_facts
    response = client.post(PATH, **signed({**body, 'series_ids': [selected], **changes}))
    assert response.status_code == 400, response.text


def test_same_day_snapshot_picks_day_in_service_and_returns_one_value_per_published_field(bounded_facts):
    client, body, selected, private, _, _ = bounded_facts
    request = {**body, 'series_ids': [selected, private], 'same_day_series_ids': [selected, private],
               'view_module': 'market', 'points_per_series': 1, 'as_of': '2026-01-04'}
    response = client.post(PATH, **signed(request))
    assert response.status_code == 200, response.text
    doc = response.json()
    assert doc['selected_date'] == '2026-01-04' and len(doc['rows']) == 2
    assert {row['date'] for row in doc['rows']} == {'2026-01-04'}
    assert {row['series_id'] for row in doc['rows']} == {selected, private}
    changed = {**request, 'same_day_series_ids': [selected], 'version': doc['version']}
    assert client.post(PATH, **signed(changed)).status_code == 409


@pytest.mark.parametrize('changes', [
    {'view_module': 'diagram'}, {'points_per_series': 2}, {'as_of': None},
    {'same_day_series_ids': []}, {'same_day_series_ids': ['kb-' + 'f' * 48]},
    {'same_day_series_ids': ['*']}, {'same_day_series_ids': None},
])
def test_same_day_mode_cannot_expand_scope_or_browse_history(bounded_facts, changes):
    client, body, selected, _, _, _ = bounded_facts
    response = client.post(PATH, **signed({**body, 'series_ids': [selected], 'same_day_series_ids': [selected],
        'view_module': 'market', 'points_per_series': 1, 'as_of': '2026-01-04', **changes}))
    assert response.status_code == 400, response.text


@pytest.fixture
def same_day_store(tmp_path):
    import duckdb
    paths = {}
    for label, observations in {
        'one': [('a', f'2026-01-{day:02}', float(day), 'valid') for day in (1, 2, 3, 10, 11)]
               + [('close-a', '2026-01-03', 3.5, 'valid')],
        'two': [('b', f'2026-01-{day:02}', float(day * 2), 'valid') for day in (1, 2, 3, 12, 13)],
    }.items():
        path = tmp_path / (label + '.duckdb')
        with duckdb.connect(str(path), config={'threads': 1, 'memory_limit': '64MB'}) as connection:
            connection.execute('CREATE TABLE observations(series_id VARCHAR,date VARCHAR,value DOUBLE,value_status VARCHAR)')
            connection.executemany('INSERT INTO observations VALUES (?,?,?,?)', observations)
        paths[label] = path
    class Store:
        @contextmanager
        def _connect(self, identifier):
            with duckdb.connect(str(paths[identifier]), config={'threads': 1, 'memory_limit': '64MB'}) as connection:
                yield connection
    a, b, close = ['kb-' + letter * 48 for letter in 'abc']
    bindings = [({'dataset_id': 'one'}, {'a': a, 'close-a': close}), ({'dataset_id': 'two'}, {'b': b})]
    selection = {'series_ids': [a, b, close], 'same_day_series_ids': [a, b],
                 'points_per_series': 1, 'as_of': '2026-01-20'}
    return Store(), bindings, selection


def test_cross_snapshot_common_day_can_be_older_than_both_recent_two_windows(same_day_store):
    from structured_data.research_facts import _same_day_date, _same_day_rows
    store, bindings, selection = same_day_store
    chosen = _same_day_date(store, bindings, selection)
    assert chosen == '2026-01-03'
    rows = _same_day_rows(store, bindings, {**selection, 'selected_date': chosen})
    assert len(rows) == 3 and {row['date'] for row in rows} == {chosen}
    assert sorted(row['value'] for row in rows) == [3, 3.5, 6]


def test_cross_snapshot_without_common_day_uses_true_latest_day_and_leaves_gaps(same_day_store):
    from structured_data.research_facts import _same_day_date, _same_day_rows
    store, bindings, selection = same_day_store
    with store._connect('two') as connection:
        connection.execute("DELETE FROM observations WHERE date < '2026-01-12'")
    chosen = _same_day_date(store, bindings, selection)
    assert chosen == '2026-01-13'
    rows = _same_day_rows(store, bindings, {**selection, 'selected_date': chosen})
    assert len(rows) == 1 and rows[0]['value'] == 26 and rows[0]['date'] == chosen


@pytest.mark.parametrize('duplicate,expected', [(3, 200), (4, 409)])
def test_same_day_duplicates_must_agree_instead_of_taking_an_arbitrary_cell(same_day_store, duplicate, expected):
    from fastapi import HTTPException
    from structured_data.research_facts import _same_day_rows
    store, bindings, selection = same_day_store
    with store._connect('one') as connection:
        connection.execute("INSERT INTO observations VALUES ('a','2026-01-03',?,'valid')", [duplicate])
    if expected == 409:
        with pytest.raises(HTTPException) as denied:
            _same_day_rows(store, bindings, {**selection, 'selected_date': '2026-01-03'})
        assert denied.value.status_code == 409
    else:
        assert len(_same_day_rows(store, bindings, {**selection, 'selected_date': '2026-01-03'})) == 3
