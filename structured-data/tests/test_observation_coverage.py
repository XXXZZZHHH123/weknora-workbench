import duckdb

from structured_data.observation_coverage import summarize_observation_coverage
from structured_data.table_analysis import analyze_table
from test_store import snapshot


def _connection(rows):
    con = duckdb.connect()
    con.execute("CREATE TABLE observations(date DATE, source_cell VARCHAR, value DOUBLE, value_status VARCHAR)")
    con.executemany("INSERT INTO observations VALUES (?, ?, ?, ?)", rows)
    return con


def test_query_rows_coverage_uses_full_filtered_scope_and_not_page(snapshot):
    store, dataset, _ = snapshot
    page1 = store.query_series(dataset, 'price', operation='rows', limit=1, offset=0)
    page2 = store.query_series(dataset, 'price', operation='rows', limit=1, offset=1)
    for result in (page1, page2):
        coverage = result['observation_coverage']
        assert coverage['scope_record_count'] == 3
        assert coverage['scope_valid_count'] == 2
        assert coverage['total_segment_count'] == 3
        assert [segment['has_value'] for segment in coverage['segments']] == [True, False, True]
        assert coverage['segments'][1]['value_status'] == 'excel_error'
        assert coverage['segments'][1]['first_cell'] == 'B3'
        assert '不证明日/月连续' in coverage['interpretation']
    assert page1['observation_coverage']['value_changes'] == page2['observation_coverage']['value_changes']
    assert page1['observation_coverage']['value_changes']['scope']['record_count'] == 3

    filtered = store.query_series(
        dataset, 'price', operation='rows', limit=1,
        value_filters=[{'op': 'gte', 'value': 15}],
    )['observation_coverage']
    assert filtered['scope_record_count'] == 1
    assert filtered['scope_valid_count'] == 1
    assert filtered['segments'][0]['first_cell'] == 'B4'
    assert filtered['value_changes']['scope']['record_count'] == 1
    assert filtered['value_changes']['adjacent_comparison_count'] == 0

    aggregate = store.query_series(dataset, 'price', operation='mean')
    assert 'observation_coverage' not in aggregate


def test_coverage_isolates_null_and_duplicate_dates_and_caps_segments():
    rows = [
        ('2020-01-01', 'A1', None, 'blank'),
        ('2020-01-02', 'A2', 1, 'valid'),
        ('2020-01-02', 'B2', 2, 'valid'),
        ('2020-01-03', 'A3', None, 'blank'),
        (None, 'A4', 4, 'valid'),
        (None, 'A5', None, 'blank'),
    ]
    con = _connection(rows)
    try:
        summary = summarize_observation_coverage(con, table='observations', max_segments=4)
        full_summary = summarize_observation_coverage(con, table='observations')
    finally:
        con.close()

    assert summary['scope_record_count'] == 6
    assert summary['scope_valid_count'] == 3
    assert summary['total_segment_count'] == 6
    assert summary['truncated'] is True
    assert len(summary['segments']) == 4
    segments = summary['segments']
    assert segments[0]['value_status'] == 'blank' and not segments[0]['has_value']
    assert segments[1]['duplicate_date_boundary'] is True
    assert segments[2]['duplicate_date_boundary'] is True
    assert segments[3]['start_date'] == '2020-01-03'
    assert full_summary['segments'][4]['undated_boundary'] is True
    assert full_summary['segments'][4]['start_date'] is None
    assert full_summary['segments'][5]['undated_boundary'] is True


def test_coverage_scope_filter_keeps_status_and_value_presence_distinct():
    con = _connection([
        ('2021-01-01', 'A1', None, 'same'),
        ('2021-01-02', 'A2', 5, 'same'),
        ('2021-01-03', 'A3', None, 'same'),
    ])
    try:
        full = summarize_observation_coverage(con, table='observations')
        middle = summarize_observation_coverage(
            con, table='observations', where='date = ?', params=['2021-01-02'])
    finally:
        con.close()
    assert [s['has_value'] for s in full['segments']] == [False, True, False]
    assert all(s['value_status'] == 'same' for s in full['segments'])
    assert middle['scope_record_count'] == 1
    assert middle['segments'][0]['first_cell'] == 'A2'


def test_table_analysis_coverage_describes_loaded_inputs_not_sql_subset(snapshot):
    store, dataset, _ = snapshot
    full_inputs = analyze_table(
        store, dataset, ['price'],
        "SELECT date, value FROM data WHERE value = 10")
    coverage = full_inputs['inputs']['price']['observation_coverage']
    assert full_inputs['rows'] == [['2023-09-21', 10]]
    assert coverage['scope_record_count'] == 3
    assert coverage['scope_valid_count'] == 2
    assert full_inputs['inputs']['price']['observation_coverage_scope'] == 'loaded_analysis_inputs_before_user_sql'

    narrowed = analyze_table(
        store, dataset, ['price'], 'SELECT count(*) FROM data',
        start_date='2023-09-22', end_date='2023-09-22')
    narrowed_coverage = narrowed['inputs']['price']['observation_coverage']
    assert narrowed_coverage['scope_record_count'] == 1
    assert narrowed_coverage['scope_valid_count'] == 0
    assert narrowed_coverage['scope_start_date'] == '2023-09-22'
    assert len(narrowed_coverage['segments']) == 1


def test_coverage_empty_scope_and_gapped_dates_do_not_create_missing_records():
    con = _connection([
        ('2000-01-01', 'A1', 1, 'valid'),
        ('2000-03-01', 'A2', 2, 'valid'),
    ])
    try:
        gap = summarize_observation_coverage(con, table='observations')
        empty = summarize_observation_coverage(
            con, table='observations', where='date > ?', params=['2050-01-01'])
    finally:
        con.close()
    assert gap['scope_record_count'] == 2
    assert gap['total_segment_count'] == 1
    assert gap['segments'][0]['record_count'] == 2
    assert gap['segments'][0]['start_date'] == '2000-01-01'
    assert gap['segments'][0]['end_date'] == '2000-03-01'
    assert '不证明日/月连续' in gap['interpretation']
    assert empty['scope_record_count'] == 0
    assert empty['segments'] == [] and empty['total_segment_count'] == 0
    assert empty['truncated'] is False


def test_value_changes_distinguish_net_direction_from_adjacent_movements():
    con = _connection([
        ('2001-01-01', 'A1', 10, 'valid'),
        ('2001-02-01', 'A2', 6, 'valid'),
        ('2001-03-01', 'A3', 8, 'valid'),
        ('2001-04-01', 'A4', 5, 'valid'),
        ('2001-05-01', 'A5', 5, 'valid'),
    ])
    try:
        changes = summarize_observation_coverage(con, table='observations')['value_changes']
    finally:
        con.close()

    assert changes['scope'] == {
        'record_count': 5, 'valid_value_count': 5, 'eligible_record_count': 5,
        'eligible_start_date': '2001-01-01', 'eligible_end_date': '2001-05-01',
        'complete_filtered_input': True,
    }
    assert changes['first'] == {'date': '2001-01-01', 'value': 10.0}
    assert changes['last'] == {'date': '2001-05-01', 'value': 5.0}
    assert changes['minimum'] == {'date': '2001-04-01', 'value': 5.0}
    assert changes['maximum'] == {'date': '2001-01-01', 'value': 10.0}
    assert changes['adjacent_comparison_count'] == 4
    assert changes['increase_count'] == 1
    assert changes['decrease_count'] == 2
    assert changes['unchanged_count'] == 1
    assert '未假定采样频率' in changes['interpretation']


def test_value_changes_do_not_bridge_missing_or_duplicate_date_boundaries():
    con = _connection([
        ('2001-01-01', 'A1', 10, 'valid'),
        ('2001-02-01', 'A2', 8, 'valid'),
        ('2001-03-01', 'A3', None, 'blank'),
        ('2001-04-01', 'A4', 6, 'valid'),
        ('2001-04-01', 'B4', 7, 'valid'),
        ('2001-05-01', 'A5', 5, 'valid'),
        (None, 'A6', 4, 'valid'),
    ])
    try:
        changes = summarize_observation_coverage(con, table='observations')['value_changes']
    finally:
        con.close()

    assert changes['scope']['record_count'] == 7
    assert changes['scope']['valid_value_count'] == 6
    assert changes['scope']['eligible_record_count'] == 3
    assert changes['scope']['eligible_start_date'] == '2001-01-01'
    assert changes['scope']['eligible_end_date'] == '2001-05-01'
    assert changes['adjacent_comparison_count'] == 1
    assert changes['increase_count'] == 0
    assert changes['decrease_count'] == 1
    assert changes['unchanged_count'] == 0
    assert changes['excluded_boundary_records'] == {
        'null_date_count': 1, 'missing_value_count': 1,
        'duplicate_date_record_count': 2,
    }


def test_value_changes_uses_common_columns_for_data_table_without_formula():
    con = duckdb.connect()
    con.execute('CREATE TABLE data(date DATE, source_cell VARCHAR, value DOUBLE, value_status VARCHAR)')
    con.executemany('INSERT INTO data VALUES (?, ?, ?, ?)', [
        ('2001-01-01', 'A1', 2, 'valid'),
        ('2001-02-01', 'A2', 4, 'valid'),
        ('2001-03-01', 'A3', 3, 'valid'),
    ])
    try:
        changes = summarize_observation_coverage(con, table='data')['value_changes']
    finally:
        con.close()

    assert changes['adjacent_comparison_count'] == 2
    assert changes['increase_count'] == 1
    assert changes['decrease_count'] == 1
    assert changes['first'] == {'date': '2001-01-01', 'value': 2.0}
    assert changes['last'] == {'date': '2001-03-01', 'value': 3.0}
