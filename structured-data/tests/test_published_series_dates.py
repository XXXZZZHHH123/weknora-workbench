from copy import deepcopy

import pytest

from structured_data.table_analysis import analyze_table
from test_store import snapshot


@pytest.fixture
def dated_fields(snapshot):
    store, _, parsed = snapshot
    parsed = deepcopy(parsed)
    parsed['sha256'] = 'b' * 64
    parsed['series'].append(dict(series_id='empty', sheet='test', metric='无值指标', header_cells=['D1']))
    parsed['observations'].append(dict(
        series_id='stock', date='2023-09-22', value=0, source_cell='C3', value_status='valid'))
    for field, column in [('price', 'B'), ('stock', 'C'), ('empty', 'D')]:
        parsed['observations'].append(dict(
            series_id=field, date='2023-09-24', value=None, source_cell=column + '5', value_status='blank'))
    return store, store.publish(parsed)['dataset_id']


def published_dates(result):
    assert 'source_semantics' in result['result_contract']['hash_input_keys']
    return {field['series_id']: (field['last_valid_date'], field['last_date'])
            for field in result['source_semantics']['published_series_dates']}


@pytest.mark.parametrize('day,field,valid_day,record_count', [
    ('2023-09-24', 'price', '2023-09-23', 1),
    ('2023-09-25', 'price', '2023-09-23', 0),
    ('2023-09-24', 'empty', None, 1),
])
def test_exact_date_lookup_keeps_full_field_dates(dated_fields, day, field, valid_day, record_count):
    store, dataset = dated_fields
    result = store.query_series(dataset, field, day, day)
    assert result['status'] == 'no_data'
    assert result['record_count'] == record_count
    assert result['valid_count'] == 0
    assert published_dates(result) == {field: (valid_day, '2023-09-24')}


def test_sql_lookup_keeps_each_selected_fields_dates(dated_fields):
    store, dataset = dated_fields
    fields = ['price', 'stock', 'empty']
    expected = {'price': ('2023-09-23', '2023-09-24'),
                'stock': ('2023-09-22', '2023-09-24'),
                'empty': (None, '2023-09-24')}
    blank = analyze_table(store, dataset, fields, 'SELECT value FROM data',
                          start_date='2023-09-24', end_date='2023-09-24')
    assert blank['rows'] == [[None], [None], [None]]
    assert published_dates(blank) == expected
    assert all(item['valid_last_date'] is None for item in blank['inputs'].values())

    missing = analyze_table(store, dataset, fields,
                            "SELECT value FROM data WHERE date = DATE '2023-09-25'")
    assert missing['rows'] == []
    assert published_dates(missing) == expected

    common = analyze_table(store, dataset, ['price', 'stock'], """
        SELECT date FROM data WHERE value IS NOT NULL
        GROUP BY date HAVING COUNT(DISTINCT series_id) = 2 ORDER BY date DESC LIMIT 1
    """)
    assert common['rows'] == [['2023-09-21']]
    assert published_dates(common) == {field: expected[field] for field in ('price', 'stock')}
