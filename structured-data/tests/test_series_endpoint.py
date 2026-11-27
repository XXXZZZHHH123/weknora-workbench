from contextlib import contextmanager
from copy import deepcopy

import duckdb
import pytest

from structured_data.result_contract import typed_sha256
from structured_data.series_endpoint import MAX_ENDPOINT_ROWS
from structured_data.table_analysis import analyze_table
from test_native_charts import chart_store
from test_native_source_update import research_catalog, versioned, write_workbook
from test_result_contract import assert_binding
from test_store import snapshot


@pytest.fixture(autouse=True)
def enable_last_valid(monkeypatch):
    monkeypatch.setenv('WEKNORA_STRUCTURED_LAST_VALID', 'true')


@pytest.fixture
def observations(snapshot):
    store, _, parsed = snapshot
    parsed = deepcopy(parsed)
    parsed['sha256'] = 'b' * 64
    parsed['series'][1]['metric'] = parsed['series'][0]['metric']
    parsed['series'][0]['dimensions'] = {'category': 'a'}
    parsed['series'][0]['category_provenance'] = {'B5': 'D5'}
    parsed['series'].extend([
        dict(series_id='empty', sheet='test', metric='missing values', unit='kg'),
        dict(series_id='never', sheet='test', metric='missing rows', unit='kg'),
        dict(series_id='labels', sheet='test', metric='label axis', date_mode='period', unit='kg'),
        dict(series_id='undated', sheet='test', metric='no axis', date_mode='none', unit='kg'),
    ])
    parsed['observations'].extend([
        dict(series_id='price', date='2023-09-24', value=0, source_cell='B5',
             date_cell='A5', formula='=0', raw_value='0', value_status='valid'),
        dict(series_id='price', date='2023-09-25', source_cell='B6', date_cell='A6',
             value_status='blank'),
        dict(series_id='price', date='2023-09-26', source_cell='B7', date_cell='A7',
             formula='=3+2', value_status='formula_no_cache'),
        dict(series_id='price', date='2023-09-28', value=50, source_cell='B8',
             date_cell='A8', raw_value='50', value_status='valid'),
        dict(series_id='empty', date='2023-09-25', source_cell='E6', date_cell='A6',
             value_status='blank'),
        dict(series_id='labels', period='FY25 plan', period_cell='A1', source_cell='F1',
             value=44, raw_value='44', value_status='valid'),
        dict(series_id='labels', period='FY25 actual', period_cell='A2', source_cell='F2',
             value=45, raw_value='45', value_status='valid'),
        dict(series_id='undated', source_cell='G2', value=15, raw_value='15', value_status='valid'),
    ])
    return store, store.publish(parsed)['dataset_id']


def test_as_of_bounds_endpoint_and_request_preserve_blank_formula_and_zero(observations):
    store, dataset = observations
    result = store.query_series(dataset, 'price', '2023-09-25', '2023-09-26',
                                include_last_valid=True)
    contract = assert_binding(result)
    assert {'requested_range', 'coverage'} <= set(contract['hash_input_keys'])
    assert result['requested_range'] == {
        'start_date': '2023-09-25', 'end_date': '2023-09-26', 'period': None,
    }
    assert (result['scope_record_count'], result['valid_count']) == (2, 0)
    assert [(r['value_status'], r['formula_cache_status']) for r in result['rows']] == [
        ('blank', 'not_formula'), ('formula_no_cache', 'missing'),
    ]
    coverage = result['coverage']
    assert coverage['complete'] and coverage['scope'] == 'through_requested_end'
    assert coverage['as_of_date'] == '2023-09-26'
    assert (coverage['record_first_date'], coverage['record_last_date']) == ('2023-09-21', '2023-09-26')
    assert (coverage['valid_first_date'], coverage['valid_last_date']) == ('2023-09-21', '2023-09-24')
    assert coverage['requested_range_status_counts'] == {'blank': 1, 'formula_no_cache': 1}
    assert coverage['last_record']['date'] == '2023-09-26'
    assert coverage['last_record']['rows'][0]['source_cell'] == 'B7'
    assert coverage['last_record']['rows'][0]['formula_cache_status'] == 'missing'
    assert result['actual_last_date'] == '2023-09-26'
    assert result['valid_last_date'] is None
    endpoint = coverage['last_valid']
    assert (endpoint['status'], endpoint['direction'], endpoint['date']) == ('observed', 'before_range', '2023-09-24')
    assert endpoint['series_id'] == 'price' and endpoint['sheet'] == 'test'
    assert endpoint['dimensions'] == {'category': 'a'}
    assert endpoint['rows'] == [{
        'date': '2023-09-24', 'value': 0, 'raw_value': '0',
        'source_cell': 'B5', 'date_cell': 'A5', 'category_cell': 'D5',
        'value_status': 'valid', 'formula_cache_status': 'cached_numeric',
        'period': None, 'period_cell': None, 'metric_cell': None,
    }]
    assert endpoint['manifest_sha256'] == contract['source_snapshot']['versions']['manifest_sha256']
    assert endpoint['source_sha256'] == result['source']['sha256'] == coverage['source']['sha256']
    assert endpoint['unit']['symbol'] == '元'
    assert endpoint['unit']['evidence'][0]['source_sha256'] == result['source']['sha256']

    tampered = deepcopy(result)
    tampered['coverage']['last_valid']['rows'][0]['value'] = 17
    with pytest.raises(AssertionError):
        assert_binding(tampered)


def test_interval_facts_are_kept_with_filters_paging_and_open_end(observations):
    store, dataset = observations
    get = lambda *args, **kwargs: store.query_series(dataset, 'price', *args, include_last_valid=True, **kwargs)
    found = get('2023-09-21', '2023-09-24', limit=1)
    assert found['truncated'] and found['coverage']['complete']
    assert found['valid_count'] == 3 and found['scope_record_count'] == 4
    assert found['rows'][0]['value'] == 10
    assert found['coverage']['last_valid']['rows'][0]['value'] == 0
    assert found['coverage']['last_valid']['direction'] == 'in_range'

    error = get('2023-09-22', '2023-09-22')
    assert error['rows'][0]['value_status'] == 'excel_error'
    assert error['rows'][0]['formula_cache_status'] == 'cached_error'
    assert error['coverage']['last_valid']['date'] == '2023-09-21'

    filtered = get('2023-09-24', '2023-09-26', value_filters=[{'op': 'gt', 'value': 5}])
    assert (filtered['scope_record_count'], filtered['record_count'], filtered['valid_count']) == (3, 0, 0)
    assert filtered['coverage']['last_valid']['date'] == '2023-09-23'
    assert filtered['coverage']['last_valid']['direction'] == 'before_range'
    assert filtered['coverage']['requested_range_status_counts'] == {'valid': 1, 'blank': 1, 'formula_no_cache': 1}

    window = get('2023-09-24', '2023-09-26', last_n=2)
    assert window['valid_count'] == 0
    assert window['coverage']['record_first_date'] == '2023-09-25'
    assert window['coverage']['last_valid']['status'] == 'no_valid'

    latest = get('2023-09-26')
    assert latest['coverage']['scope'] == 'snapshot'
    assert latest['coverage']['as_of_date'] is None
    assert latest['coverage']['last_valid']['date'] == '2023-09-28'


def test_empty_latest_result_keeps_saved_statuses_and_last_record_coordinates(observations):
    store, dataset = observations
    last = store.query_series(dataset, 'price', '2023-09-25', '2023-09-26',
                              operation='latest', include_last_valid=True)
    assert last['rows'] == [] and last['valid_count'] == 0
    assert last['rows_are'] == 'latest_valid_observation'
    assert last['coverage']['requested_range_status_counts'] == {'blank': 1, 'formula_no_cache': 1}
    assert last['coverage']['last_record']['rows'][0]['source_cell'] == 'B7'
    assert last['coverage']['last_valid']['rows'][0]['source_cell'] == 'B5'

    no_requested_rows = store.query_series(dataset, 'price', '2023-09-27', '2023-09-27',
                                           operation='latest', include_last_valid=True)
    assert no_requested_rows['scope_record_count'] == 0
    assert no_requested_rows['coverage']['requested_range_status_counts'] == {}
    assert no_requested_rows['coverage']['last_record']['date'] == '2023-09-26'
    assert no_requested_rows['coverage']['last_valid']['date'] == '2023-09-24'


def test_no_rows_no_numeric_records_and_no_order_have_separate_evidence(observations):
    store, dataset = observations
    empty = store.query_series(dataset, 'empty', '2023-09-25', '2023-09-25', include_last_valid=True)
    never = store.query_series(dataset, 'never', '2023-09-25', '2023-09-25', include_last_valid=True)
    earlier = store.query_series(dataset, 'price', '2023-09-20', '2023-09-20', include_last_valid=True)
    for result in (empty, never, earlier):
        assert result['coverage']['complete']
        assert result['coverage']['last_valid']['status'] == 'no_valid'
        assert result['coverage']['last_valid']['rows'] == []
        assert result['coverage']['last_valid']['date'] is None
    assert empty['scope_record_count'] == 1 and empty['coverage']['record_last_date'] == '2023-09-25'
    assert never['scope_record_count'] == 0 and never['coverage']['record_last_date'] is None
    assert earlier['scope_record_count'] == 0 and earlier['coverage']['record_last_date'] is None

    labels = store.query_series(dataset, 'labels', period='FY25 plan', include_last_valid=True)
    undated = store.query_series(dataset, 'undated', include_last_valid=True)
    assert labels['coverage']['scope'] == 'requested_period'
    assert labels['requested_range']['period'] == labels['coverage']['period'] == 'FY25 plan'
    assert [(r['period'], r['period_cell'], r['value']) for r in labels['rows']] == [('FY25 plan', 'A1', 44)]
    assert undated['coverage']['scope'] == 'undated'
    for result in (labels, undated):
        assert result['coverage']['complete']
        assert result['coverage']['last_valid']['status'] == 'not_ordered'
        assert result['coverage']['last_valid']['date'] is None
        assert result['coverage']['last_valid']['rows'] == []
    no_approximate_label = store.query_series(dataset, 'labels', period='FY25', include_last_valid=True)
    assert no_approximate_label['rows'] == [] and no_approximate_label['scope_record_count'] == 0
    sql = analyze_table(store, dataset, ['labels', 'undated'], 'SELECT COUNT(*) FROM data',
                        '2023-09-25', '2023-09-26', include_last_valid=True)
    assert sql['rows'] == [[0]]
    for item in sql['inputs'].values():
        assert item['coverage']['as_of_date'] is None
        assert item['coverage']['last_valid']['status'] == 'not_ordered'
        assert item['coverage']['requested_range_status_counts'] == {}


def test_sql_uses_bound_source_snapshot_once_and_separates_endpoint_from_output(observations, monkeypatch):
    store, dataset = observations
    connections = []
    original_connect = store._connect

    @contextmanager
    def connect(snapshot_id):
        connections.append(snapshot_id)
        with original_connect(snapshot_id) as con:
            yield con

    monkeypatch.setattr(store, '_connect', connect)
    result = analyze_table(store, dataset, ['price', 'stock'], 'SELECT COUNT(value), 999 FROM data',
                           '2023-09-25', '2023-09-26', include_last_valid=True)
    assert connections == [dataset]
    assert result['rows'] == [[0, 999]] and result['input_count'] == 2
    assert 'inputs' in assert_binding(result)['hash_input_keys']
    assert set(result['inputs']) == {'price', 'stock'}
    for field, value, day in [('price', 0, '2023-09-24'), ('stock', 30, '2023-09-21')]:
        item = result['inputs'][field]
        assert item['valid_count'] == 0
        assert item['series']['metric'] == result['inputs']['price']['series']['metric']
        assert item['source'] == item['coverage']['source']
        endpoint = item['coverage']['last_valid']
        assert endpoint['series_id'] == field and endpoint['date'] == day
        assert endpoint['direction'] == 'before_range' and endpoint['rows'][0]['value'] == value
    assert result['inputs']['price']['coverage']['requested_range_status_counts'] == {'blank': 1, 'formula_no_cache': 1}
    assert result['inputs']['price']['coverage']['last_record']['rows'][0]['source_cell'] == 'B7'
    assert result['inputs']['stock']['coverage']['requested_range_status_counts'] == {}
    tampered = deepcopy(result)
    tampered['inputs']['price']['coverage']['last_valid']['date'] = '2023-09-27'
    with pytest.raises(AssertionError):
        assert_binding(tampered)

    connections.clear()
    series_result = store.query_series(dataset, 'price', operation='latest', include_last_valid=True)
    assert connections == [dataset]
    assert series_result['coverage']['last_valid']['date'] == '2023-09-28'
    assert series_result['rows_are'] == 'latest_valid_observation'
    assert [row['value'] for row in series_result['rows']] == [50]

    in_range = analyze_table(store, dataset, ['price'], 'SELECT value FROM data ORDER BY date',
                             '2023-09-23', '2023-09-24', limit=1, include_last_valid=True)
    assert in_range['rows'] == [[20]] and in_range['output_truncated']
    coverage = in_range['inputs']['price']['coverage']
    assert coverage['complete'] and coverage['last_valid']['direction'] == 'in_range'
    assert coverage['last_valid']['rows'][0]['value'] == 0


def test_same_date_conflicting_rows_and_truncation_are_reported(observations, monkeypatch):
    store, dataset = observations
    database = store._path(dataset) / 'data.duckdb'
    with duckdb.connect(str(database)) as con:
        # A malformed legacy table can bypass the importer's duplicate guard.
        con.execute("""
            INSERT INTO observations(series_id,date,value,raw_value,source_cell,date_cell,value_status)
            VALUES ('price','2023-09-24',5,'5','B50','A50','valid'),
                   ('price','2023-09-24',NULL,NULL,'B51','A51','blank'),
                   ('stock','2023-09-21',30,'30','C50','A50','valid')
        """)
        con.execute('CHECKPOINT')
    query = store.query_series(dataset, 'price', end_date='2023-09-24', include_last_valid=True)
    sql = analyze_table(store, dataset, ['price', 'stock'], 'SELECT COUNT(*) FROM data',
                        end_date='2023-09-24', include_last_valid=True)
    conflicted = query['coverage']['last_valid']
    assert conflicted == sql['inputs']['price']['coverage']['last_valid']
    assert conflicted['status'] == 'conflict' and conflicted['conflict']
    assert [(r['source_cell'], r['value']) for r in conflicted['rows']] == [
        ('B5', 0), ('B50', 5), ('B51', None),
    ]
    tied = sql['inputs']['stock']['coverage']['last_valid']
    assert tied['status'] == 'observed' and not tied['conflict'] and tied['row_count'] == 2

    latest = store.query_series(dataset, 'price', end_date='2023-09-24', operation='latest',
                                include_last_valid=True)
    assert latest['coverage']['last_valid'] == conflicted
    assert latest['status'] == 'ok' and latest['valid_count'] == 4
    assert latest['rows'] == [] and latest['rows_are'] == 'unresolved_latest_valid_observation'
    assert any('coverage.last_valid' in warning for warning in latest['warnings'])
    contract = assert_binding(latest)
    assert {'rows', 'rows_are', 'warnings'} <= set(contract['hash_input_keys'])
    assert not contract['evidence_capabilities']['source_cells_available']
    legacy = store.query_series(dataset, 'price', end_date='2023-09-24', operation='latest')
    assert legacy['rows_are'] == 'latest_valid_observation'
    assert len(legacy['rows']) == 1 and legacy['rows'][0]['value'] in (0, 5)

    with duckdb.connect(str(database)) as con:
        con.executemany("""
            INSERT INTO observations(series_id,date,value,source_cell,date_cell,value_status)
            VALUES (?, ?, NULL, ?, ?, 'blank')
        """, [(field, day, f'{column}{i}', f'A{i}')
              for field, day, column in [('price', '2023-09-24', 'B'), ('stock', '2023-09-21', 'C')]
              for i in range(100, 100 + MAX_ENDPOINT_ROWS)])
        con.execute('CHECKPOINT')
    coverage = store.query_series(dataset, 'price', end_date='2023-09-24', include_last_valid=True)['coverage']
    endpoint = coverage['last_valid']
    assert coverage['complete'] and endpoint['status'] == 'truncated' and endpoint['conflict']
    assert endpoint['row_count'] == MAX_ENDPOINT_ROWS + 3
    assert len(endpoint['rows']) == MAX_ENDPOINT_ROWS
    incomplete = store.query_series(dataset, 'stock', operation='latest', include_last_valid=True)
    assert incomplete['coverage']['last_valid']['status'] == 'truncated'
    assert not incomplete['coverage']['last_valid']['conflict']
    assert incomplete['valid_count'] == 2
    assert incomplete['rows'] == [] and incomplete['rows_are'] == 'unresolved_latest_valid_observation'
    assert_binding(incomplete)
    monkeypatch.setenv('WEKNORA_STRUCTURED_LAST_VALID', 'false')
    legacy = store.query_series(dataset, 'stock', operation='latest', include_last_valid=True)
    assert 'coverage' not in legacy and legacy['rows_are'] == 'latest_valid_observation'
    assert [row['value'] for row in legacy['rows']] == [30]


def test_chart_endpoint_is_confined_to_the_linked_curve(chart_store):
    store, dataset, _ = chart_store
    view = next(v for v in store.chart_index(dataset)['curve_views'] if v['chart_legends'] == ['2023'])
    end = '2025-01-01'
    result = store.query_series(dataset, view['series_id'], end, end, include_last_valid=True)
    endpoint = result['coverage']['last_valid']
    assert result['scope_record_count'] == 0
    assert endpoint['series_id'] == view['series_id'] and endpoint['sheet'] == '源 表'
    assert (endpoint['date'], endpoint['direction']) == ('2023-09-08', 'before_range')
    assert endpoint['rows'][0]['source_cell'] == 'B3' and endpoint['rows'][0]['value'] == 20


def test_retained_sheet_endpoint_uses_prior_workbook_hash_and_unit_evidence(versioned):
    manager, path, _, first = versioned
    updated = write_workbook(path, append=True, change=True)
    dataset = manager.ingest(path, native_source=updated)['dataset_id']
    fields, _ = research_catalog(manager, updated)
    retained = fields['Revised']['source_series_id']
    current = fields['Daily']['source_series_id']
    store = manager.store
    query = store.query_series(dataset, retained, include_last_valid=True)
    sql = analyze_table(store, dataset, [retained, current], 'SELECT SUM(value) FROM data',
                        include_last_valid=True)
    assert query['source']['sha256'] != sql['source']['sha256']
    earlier_manifest = store.manifest(first['dataset_id'])
    first_hash = typed_sha256(earlier_manifest)
    query_hash = assert_binding(query)['source_snapshot']['versions']['manifest_sha256']
    assert assert_binding(sql)['source_snapshot']['versions']['manifest_sha256'] == query_hash
    assert first_hash != query_hash
    endpoint = query['coverage']['last_valid']
    assert endpoint == sql['inputs'][retained]['coverage']['last_valid']
    assert query['source']['source_dataset_id'] == endpoint['source_dataset_id'] == first['dataset_id']
    assert endpoint['date'] == '2025-03-02' and endpoint['rows'][0]['value'] == 21
    assert endpoint['source_sha256'] == earlier_manifest['sha256']
    assert endpoint['source_manifest_sha256'] == first_hash
    assert endpoint['manifest_sha256'] == query_hash
    assert sql['inputs'][retained]['source'] == query['coverage']['source']
    assert sql['inputs'][current]['source']['sha256'] == sql['source']['sha256']


def test_opt_in_flag_and_disabled_capability_keep_old_contract(observations, monkeypatch):
    store, dataset = observations
    monkeypatch.delenv('WEKNORA_STRUCTURED_LAST_VALID', raising=False)
    query = lambda **kw: store.query_series(dataset, 'price', end_date='2023-09-26', **kw)
    sql = lambda **kw: analyze_table(store, dataset, ['price'], 'SELECT COUNT(*) FROM data',
                                     end_date='2023-09-26', **kw)
    import structured_data.table_analysis as analysis
    monkeypatch.setattr(analysis.secrets, 'token_hex', lambda _: '5' * 24)
    old_query, old_analysis = query(), sql()
    assert 'coverage' not in old_query and 'requested_range' not in old_query
    assert 'coverage' not in old_analysis['inputs']['price']
    assert_binding(old_query)
    assert_binding(old_analysis)
    assert query(include_last_valid=True)['coverage']['last_valid']['date'] == '2023-09-24'
    assert sql(include_last_valid=True)['inputs']['price']['coverage']['last_valid']['date'] == '2023-09-24'
    monkeypatch.setenv('WEKNORA_STRUCTURED_LAST_VALID', 'false')
    assert query(include_last_valid=True) == old_query
    assert sql(include_last_valid=True) == old_analysis
    monkeypatch.setenv('WEKNORA_STRUCTURED_LAST_VALID', 'true')
    assert query(include_last_valid=False) == old_query
    assert sql(include_last_valid=False) == old_analysis
    for read in (query, sql):
        with pytest.raises(ValueError, match='include_last_valid'):
            read(include_last_valid='true')
