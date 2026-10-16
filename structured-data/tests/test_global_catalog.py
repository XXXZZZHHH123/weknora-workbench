"""Global browsing keeps file identities and existing KB authorization."""
import pytest

from structured_data import global_catalog
from structured_data.store import DataStore
from structured_data.workspaces import Identity, current_identity
from test_knowledge_base_scope import scoped, headers


def publish(store, name, fingerprint, metrics, *, kind='business'):
    parsed = dict(source_file=name, sha256=fingerprint * 64, rule_version='fixture-v1',
                  dataset_kind=kind, sheets=[], excluded_regions=[], issues=[],
                  series=[dict(series_id='shared-' + str(i), sheet='Source', metric=metric,
                               unit=None, dimensions={}, header_cells=['B1'])
                          for i, metric in enumerate(metrics)],
                  observations=[dict(series_id='shared-' + str(i), date='2026-01-01', value=i + 1,
                                     source_cell='B2', value_status='valid', date_cell='A2', raw_value=i + 1)
                                for i in range(len(metrics))])
    return store.publish(parsed)['dataset_id']


def test_all_files_keeps_each_real_dataset_and_global_pagination(tmp_path):
    store = DataStore(tmp_path)
    a = publish(store, 'alpha.xlsx', 'a', ['Common measure', 'Alpha remainder'])
    b = publish(store, 'beta.xlsx', 'b', ['Common measure', 'Beta remainder'])
    pages = [global_catalog.list_series(store, limit=1, offset=i) for i in range(4)]
    entries = [page['series'][0] for page in pages]
    assert {(row['dataset_id'], row['series_id']) for row in entries} == {
        (a, 'shared-0'), (a, 'shared-1'), (b, 'shared-0'), (b, 'shared-1')}
    assert {row['source_file'] for row in entries} == {'alpha.xlsx', 'beta.xlsx'}
    assert all(page['total'] == 4 and page['dataset_count'] == 2 for page in pages)
    assert [page['next_offset'] for page in pages] == [1, 2, 3, None]
    assert global_catalog.list_series(store, limit=1, offset=4)['series'] == []
    found = global_catalog.list_series(store, search='Common measure')
    assert found['total'] == 2
    # Selecting one literal candidate still queries exactly its own dataset.
    assert all(store.query_series(row['dataset_id'], row['series_id'])['source']['file'] == row['source_file']
               for row in found['series'])


def test_all_excludes_historical_versions_and_explicit_samples(tmp_path):
    store = DataStore(tmp_path)
    old = publish(store, 'alpha.xlsx', 'a', ['Old only'])
    latest = publish(store, 'alpha.xlsx', 'b', ['New only'])
    sample = publish(store, 'fixture.xlsx', 'c', ['Sample only'], kind='sample')
    result = global_catalog.list_series(store)
    assert [row['dataset_id'] for row in result['series']] == [latest]
    assert result['dataset_count'] == 1
    assert store._series(old, 'shared-0')['metric'] == 'Old only'
    assert store._series(sample, 'shared-0')['metric'] == 'Sample only'


def test_partial_matches_do_not_displace_real_multiword_matches_in_other_file(tmp_path):
    store = DataStore(tmp_path)
    publish(store, 'partial.xlsx', 'a', ['North'])
    selected = publish(store, 'exact.xlsx', 'b', ['North measure'])
    result = global_catalog.list_series(store, search='North measure')
    assert result['total'] == 1
    assert result['series'][0]['dataset_id'] == selected
    assert result['match_mode'] == 'all_terms'


def test_all_charts_preserves_curve_source_across_same_ids(tmp_path, monkeypatch):
    store = DataStore(tmp_path)
    a = publish(store, 'alpha.xlsx', 'a', ['One'])
    b = publish(store, 'beta.xlsx', 'b', ['Two'])
    monkeypatch.setattr(store, 'chart_index', lambda dataset: {'charts':[
        {'chart_id':'same-chart', 'title':'Measure', 'sheet':'Plot', 'curves':[{'series_id':'chart_same'}]}
    ], 'notice':'Fixture'})
    result = global_catalog.list_charts(store, limit=1)
    next_page = global_catalog.list_charts(store, limit=1, offset=result['next_offset'])
    assert result['total'] == 2
    assert {result['charts'][0]['dataset_id'], next_page['charts'][0]['dataset_id']} == {a, b}
    assert result['sheets'] == ['Plot']


@pytest.mark.parametrize('limit,offset', [(0, 0), (201, 0), (True, 0), (1, -1), (1, True)])
def test_global_catalog_retains_pagination_validation(tmp_path, limit, offset):
    store = DataStore(tmp_path)
    for operation in (global_catalog.list_series, global_catalog.list_charts):
        with pytest.raises(ValueError): operation(store, limit=limit, offset=offset)


def test_global_http_is_scoped_to_verified_kb_and_rechecks_revoked_grant(scoped):
    client, app, grants, _, _ = scoped
    registry = app.state.workspaces
    for kb, tenant, name, fingerprint in [('kb-a', 10000, 'alpha.xlsx', 'a'),
                                         ('kb-b', 10000, 'beta.xlsx', 'b'),
                                         ('', 10000, 'legacy.xlsx', 'c'),
                                         ('kb-other', 10001, 'other.xlsx', 'd')]:
        marker = current_identity.set(Identity('test-user', '', '', tenant, 'owner', kb, tenant, True))
        try: publish(registry.objects()['store'], name, fingerprint, ['Shared label'])
        finally: current_identity.reset(marker)
    assert client.get('/api/series').status_code == 401
    for kb, tenant, expected in [('kb-a',10000,'alpha.xlsx'), ('kb-b',10000,'beta.xlsx'),
                                ('',10000,'legacy.xlsx'), ('kb-other',10001,'other.xlsx')]:
        response = client.get('/api/series?search=Shared', headers=headers(kb, tenant))
        assert response.status_code == 200, response.text
        assert [row['source_file'] for row in response.json()['series']] == [expected]
        assert client.get('/api/charts', headers=headers(kb, tenant)).status_code == 200
    assert client.get('/api/series', headers=headers('kb-other')).status_code == 403
    del grants['kb-a']
    assert client.get('/api/series', headers=headers('kb-a')).status_code == 403
    assert client.get('/api/charts', headers=headers('kb-a')).status_code == 403
