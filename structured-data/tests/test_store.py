import copy

import pytest

from structured_data.store import DataStore


@pytest.fixture
def snapshot(tmp_path):
    store = DataStore(tmp_path)
    series = [dict(series_id="price", sheet="test", metric="价格", unit="元", dimensions={}, header_cells=["B1"]), dict(series_id="stock", sheet="test", metric="库存", unit="吨", dimensions={}, header_cells=["C1"])]
    observations = [dict(series_id="price", date="2023-09-21", value=10, source_cell="B2", formula=None, value_status="valid", date_cell="A2", raw_value=10), dict(series_id="price", date="2023-09-22", value=None, source_cell="B3", formula="=NA()", value_status="excel_error", date_cell="A3", raw_value="#N/A"), dict(series_id="price", date="2023-09-23", value=20, source_cell="B4", formula=None, value_status="valid", date_cell="A4", raw_value=20), dict(series_id="stock", date="2023-09-21", value=30, source_cell="C2", formula=None, value_status="valid", date_cell="A2", raw_value=30)]
    parsed = dict(source_file="fixture.xlsx", sha256="a"*64, rule_version="v1", series=series, observations=observations, sheets=[], excluded_regions=[], issues=[])
    doc = store.publish(parsed)
    return store, doc["dataset_id"], parsed


def test_statistics_include_all_and_do_not_zero_errors(snapshot):
    store, id, _ = snapshot
    q = store.query_series(id, "price", "2023-09-21", "2023-09-23", "mean", 1)
    assert q["aggregate"] == 15
    assert q["record_count"] == 3 and q["valid_count"] == 2 and q["missing_count"] == 1
    error = store.query_series(id, "price", "2023-09-22", "2023-09-22")
    assert error["status"] == "no_data"
    assert error["rows"][0]["value"] is None
    assert error["rows"][0]["raw_value"] == "#N/A"
    assert error["rows"][0]["date_cell"] == "A3"


def test_saved_rows_page_keeps_original_evidence_and_counts(snapshot):
    store, dataset, _ = snapshot
    pages = [store.query_series(dataset, 'price', limit=1, offset=i) for i in range(3)]
    assert [page['rows'][0]['source_cell'] for page in pages] == ['B2', 'B3', 'B4']
    assert [page['rows'][0]['value'] for page in pages] == [10, None, 20]
    assert pages[1]['rows'][0]['raw_value'] == '#N/A'
    assert pages[1]['rows'][0]['date_cell'] == 'A3'
    assert all(page['record_count'] == 3 and page['valid_count'] == 2 for page in pages)
    assert store.query_series(dataset, 'price', limit=1, offset=3)['rows'] == []
    for offset in (-1, True, '1', 10000001):
        with pytest.raises(ValueError):
            store.query_series(dataset, 'price', offset=offset)
    with pytest.raises(ValueError):
        store.query_series(dataset, 'price', operation='sum', offset=1)


def test_extrema_bind_value_date_and_source_row(snapshot):
    store,ds,_=snapshot
    maximum=store.query_series(ds,'price',operation='max')
    assert maximum['aggregate']==20
    assert maximum['actual_first_date']=='2023-09-21'
    assert maximum['rows_are']=='extremum_observations'
    assert [(r['date'],r['value'],r['source_cell'],r['date_cell']) for r in maximum['extremum_rows']]==[('2023-09-23',20,'B4','A4')]
    assert maximum['extremum_count']==1 and not maximum['truncated']
    assert store.query_series(ds,'price',operation='min')['rows'][0]['source_cell']=='B2'
    missing=store.query_series(ds,'price','2023-09-22','2023-09-22','max')
    assert missing['status']=='no_data' and missing['aggregate'] is None
    assert missing['rows']==[] and missing['extremum_count']==0


def test_tied_extrema_keep_all_dates_and_respect_range(snapshot):
    store,_,parsed=snapshot
    parsed=copy.deepcopy(parsed);parsed['sha256']='c'*64
    parsed['observations'].append(dict(series_id='price',date='2023-09-24',value=20,source_cell='B5',date_cell='A5',formula='=10*2',value_status='valid',raw_value=20))
    ds=store.publish(parsed)['dataset_id']
    result=store.query_series(ds,'price',operation='max',limit=1)
    assert result['extremum_count']==2 and result['truncated']
    assert len(store.query_series(ds,'price',operation='max')['rows'])==2
    ranged=store.query_series(ds,'price','2023-09-24','2023-09-24','max')
    assert ranged['extremum_count']==1 and ranged['rows'][0]['source_cell']=='B5'


def test_no_substitution_and_explicit_truncation(snapshot):
    store, id, _ = snapshot
    assert store.query_series(id, "price", "2024-01-01", "2024-01-01")["rows"] == []
    assert store.query_series(id, "price", operation="rows", limit=1)["truncated"]
    assert store.query_series(id, "price", operation="latest")["rows"][0]["date"] == "2023-09-23"


def test_immutable_snapshots_and_duplicate_rejection(snapshot):
    store, id, parsed = snapshot
    assert store.publish(parsed)["reused"]
    bad = copy.deepcopy(parsed)
    bad["sha256"] = "b" * 64
    bad["observations"].append(bad["observations"][0])
    with pytest.raises(ValueError, match="重复"):
        store.publish(bad)
    assert len(store.list_datasets()) == 1
    assert store.query_series(id, "price", operation="sum")["aggregate"] == 30


def test_dataset_selection_uses_only_current_version_of_single_source(snapshot):
    store, old_id, parsed = snapshot
    assert store.select_dataset() == old_id
    newer = copy.deepcopy(parsed)
    newer['sha256'] = 'd' * 64
    new_id = store.publish(newer)['dataset_id']
    assert store.select_dataset() == new_id
    assert store.select_dataset(old_id) == old_id
    other = copy.deepcopy(parsed)
    other['source_file'] = 'other.xlsx'
    other['sha256'] = 'e' * 64
    store.publish(other)
    with pytest.raises(ValueError, match='多份'):
        store.select_dataset()
    assert store.select_dataset(new_id) == new_id


def test_empty_dataset_selection_reports_missing_import(tmp_path):
    with pytest.raises(ValueError, match='尚无已导入'):
        DataStore(tmp_path).select_dataset()


def test_invalid_queries_do_not_reach_database(snapshot):
    store, id, _ = snapshot
    with pytest.raises(ValueError): store.query_series("../../etc", "price")
    with pytest.raises(ValueError): store.query_series(id, "price' OR 1=1 --")
    with pytest.raises(ValueError): store.query_series(id, "price", operation="DROP TABLE observations")
    with pytest.raises(ValueError): store.query_series(id, "price", "2023-09-24", "2023-09-21")
    with pytest.raises(ValueError): store.query_series(id, "price", limit=0)
    with pytest.raises(ValueError): store.compare_series(id, "price", "stock", "2023-09-21")
    with store._connect(id) as con:
        with pytest.raises(Exception): con.execute("SELECT * FROM read_csv('/etc/passwd')")
        with pytest.raises(Exception): con.execute("DELETE FROM observations")


def test_catalog_recovers_multi_indicator_search_without_changing_query_filters(snapshot):
    store, id, _ = snapshot
    catalog = store.list_series(id, "价格 库存")
    assert catalog["match_mode"] == "partial_candidates"
    assert {s["series_id"] for s in catalog["series"]} == {"price", "stock"}
    with pytest.raises(ValueError):
        store.query_series(id, "价格 库存")


def test_catalog_group_counts_use_whole_matching_scope_before_pagination(snapshot):
    store, _, parsed = snapshot
    parsed = copy.deepcopy(parsed)
    parsed['sha256'] = 'd' * 64
    parsed['series'].append(dict(parsed['series'][0], series_id='price_other', dimensions={'contract': 'Z9'}))
    parsed['observations'].append(dict(parsed['observations'][0], series_id='price_other'))
    dataset = store.publish(parsed)['dataset_id']
    first = store.list_series(dataset, limit=1)
    summary = first['catalog_summary']
    assert first['truncated'] and summary['series_count'] == 3
    assert summary['group_count'] == 2 and not summary['groups_truncated']
    assert summary['group_by'] == ['sheet', 'metric']
    counts = {row['metric']: row['series_count'] for row in summary['groups']}
    assert counts == {'价格': 2, '库存': 1}
    assert store.list_series(dataset, offset=2)['catalog_summary'] == summary
    matched = store.list_series(dataset, search='价格', sheet='test')['catalog_summary']
    assert matched['series_count'] == 2 and matched['group_count'] == 1
    assert matched['scope'] == {'sheet': 'test', 'search': '价格', 'match_mode': 'all_terms'}
    partial = store.list_series(dataset, search='价格 库存')['catalog_summary']
    assert partial['scope']['match_mode'] == 'partial_candidates'


def test_catalog_group_summary_explicitly_bounds_groups():
    rows = [dict(series_id=str(i), sheet='source', metric=f'field_{i}') for i in range(103)]
    summary = DataStore._catalog_summary(rows, None, '', 'all_terms')
    assert summary['group_count'] == 103 and summary['groups_truncated']
    assert len(summary['groups']) == 100
    assert summary['series_count'] == 103
