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
