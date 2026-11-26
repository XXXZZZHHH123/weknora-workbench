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
    warnings = '\n'.join(result['warnings'])
    assert '最后有值的日期（若有）' in warnings
    assert '不能用其他日期或模型记忆补齐' in warnings
    assert '用户未排除补充且有符合其范围的授权文档可查，按所问日期与指标检索并实读' in warnings
    assert '确认有对应值时，另列日期、出处、原口径和原值' in warnings
    assert '用户明确只用该来源时只交付限定范围的结果' in warnings


def test_exact_date_warning_uses_query_filters_and_stays_out_of_other_results(dated_fields):
    store, dataset = dated_fields
    day = '2023-09-23'
    filtered = store.query_series(dataset, 'price', day, day, value_filters=[{'op': 'gt', 'value': 1000}])
    assert filtered['status'] == 'no_data'
    assert (filtered['scope_record_count'], filtered['record_count']) == (1, 0)
    assert '筛选后零行不能证明原字段当天无记录' in '\n'.join(filtered['warnings'])
    assert published_dates(filtered) == {'price': (day, '2023-09-24')}

    available = store.query_series(dataset, 'price', day, day)
    missing_range = store.query_series(dataset, 'price', '2023-09-24', '2023-09-25')
    count_only = store.query_series(dataset, 'price', '2023-09-24', '2023-09-24', operation='count')
    assert available['status'] == 'ok' and missing_range['status'] == 'no_data'
    assert count_only['status'] == 'ok' and count_only['aggregate'] == 0
    assert published_dates(count_only) == {'price': (day, '2023-09-24')}
    for result in (available, missing_range, count_only):
        assert '按所问日期与指标检索并实读' not in '\n'.join(result['warnings'])


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
