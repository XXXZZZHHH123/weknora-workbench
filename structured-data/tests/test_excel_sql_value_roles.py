"""Technical selectors do not turn actual observations into technical values."""
from copy import deepcopy

import pytest

from structured_data.store import DataStore
from structured_data.table_analysis import analyze_table
from test_result_contract import assert_binding


@pytest.fixture
def fruit_source(tmp_path):
    store = DataStore(tmp_path)
    parsed = dict(source_file='synthetic-fruit.xlsx', sha256='f' * 64, rule_version='v1',
                  series=[dict(series_id=sid, sheet='synthetic', metric=metric, unit='kg',
                               dimensions={}, header_cells=[column + '1'])
                          for sid, metric, column in [('apple', '苹果净重', 'B'), ('pear', '梨净重', 'C')]],
                  observations=[dict(series_id=sid, date=date, value=value, source_cell=cell,
                                     date_cell='A' + cell[1:], formula=None,
                                     value_status='valid', raw_value=value)
                                for sid, date, value, cell in [
                                    ('apple', '2022-01-01', 10, 'B2'), ('apple', '2022-02-01', 20, 'B3'),
                                    ('pear', '2022-01-01', 45, 'C2'), ('apple', '2023-01-01', 70, 'B4'),
                                    ('pear', '2023-01-01', 80, 'C4')]],
                  sheets=[], excluded_regions=[], issues=[])
    return store, store.publish(parsed)['dataset_id'], parsed


@pytest.mark.parametrize('expression', [
    "SUM(CASE WHEN series_id='apple' THEN value END)",
    "SUM(IF(series_id='apple', CAST(value AS DOUBLE), NULL))",
    "SUM(value) FILTER (WHERE series_id='apple')",
])
def test_technical_predicate_selects_a_real_measurement(fruit_source, expression):
    store, dataset, _ = fruit_source
    out = analyze_table(store, dataset, ['apple', 'pear'], 'SELECT ' + expression + ' AS observed FROM data')
    column = assert_binding(out)['column_metadata'][0]
    assert out['rows'] == [[100]]
    assert column['dependency'] == dict(kind='source_value', source_columns=['series_id', 'value'],
                                        source_row_dependency=True, technical_metadata=False)
    assert column['unit']['status'] == 'derived' and column['unit']['symbol'] == 'kg'


def test_cte_alias_cast_and_reordered_pivot_columns_keep_value_roles(fruit_source):
    store, dataset, _ = fruit_source
    out = analyze_table(store, dataset, ['apple', 'pear'], """
        WITH scoped AS (
            SELECT date_trunc('year', date) AS period, series_id AS selector,
                   CAST(value AS DOUBLE) AS measurement FROM data
        ), pivoted AS (
            SELECT period,
                   SUM(IF(selector='apple', measurement, NULL)) AS first_measure,
                   SUM(CASE WHEN selector='pear' THEN measurement END) AS second_measure
            FROM scoped GROUP BY period
        )
        SELECT second_measure, period, first_measure FROM pivoted ORDER BY period
    """)
    columns = assert_binding(out)['column_metadata']
    assert out['rows'] == [[45, '2022-01-01T00:00:00', 30], [80, '2023-01-01T00:00:00', 70]]
    assert [c['name'] for c in columns] == ['second_measure', 'period', 'first_measure']
    for i in (0, 2):
        assert columns[i]['dependency']['source_columns'] == ['series_id', 'value']
        assert columns[i]['dependency']['source_row_dependency'] is True
        assert columns[i]['dependency']['technical_metadata'] is False
        assert columns[i]['unit']['symbol'] == 'kg'


@pytest.mark.parametrize('expression', [
    'series_id',
    'CAST(series_id AS VARCHAR)',
    "CASE WHEN value>0 THEN series_id END",
    "CASE WHEN series_id='apple' THEN 'invented label' ELSE 'other label' END",
    "IF(series_id='apple', CAST('invented label' AS VARCHAR), NULL)",
    "CASE WHEN value>0 THEN LENGTH(series_id) ELSE value END",
    "CASE WHEN series_id='apple' THEN value ELSE LENGTH(series_id) END",
    "CASE WHEN series_id='apple' THEN value ELSE 999 END",
])
def test_identifiers_and_invented_case_outputs_do_not_gain_measurement_roles(fruit_source, expression):
    store, dataset, _ = fruit_source
    out = analyze_table(store, dataset, ['apple', 'pear'], 'SELECT ' + expression + ' AS output FROM data')
    assert assert_binding(out)['column_metadata'][0]['dependency']['technical_metadata'] is True


def test_aliasing_an_invented_label_then_reusing_it_does_not_launder_role(fruit_source):
    store, dataset, _ = fruit_source
    out = analyze_table(store, dataset, ['apple', 'pear'], """
        WITH labels AS (
            SELECT CASE series_id WHEN 'apple' THEN 'invented label' ELSE 'other label' END AS title
            FROM data
        )
        SELECT CAST(title AS VARCHAR) AS caption FROM labels
    """)
    assert assert_binding(out)['column_metadata'][0]['dependency']['technical_metadata'] is True


def test_window_controls_do_not_supply_output_values(fruit_source):
    store, dataset, _ = fruit_source
    out = analyze_table(store, dataset, ['apple', 'pear'], """
        SELECT SUM(value) OVER (PARTITION BY series_id ORDER BY date) AS observed FROM data
    """)
    dep = assert_binding(out)['column_metadata'][0]['dependency']
    assert dep['source_columns'] == ['date', 'series_id', 'value']
    assert dep['technical_metadata'] is False


def test_value_role_does_not_resolve_mixed_units(fruit_source):
    store, _, parsed = fruit_source
    changed = deepcopy(parsed)
    changed['sha256'] = 'e' * 64
    changed['series'][1]['unit'] = 'm'
    dataset = store.publish(changed)['dataset_id']
    out = analyze_table(store, dataset, ['apple', 'pear'],
                        "SELECT SUM(CASE WHEN series_id='apple' THEN value END) AS observed FROM data")
    column = assert_binding(out)['column_metadata'][0]
    assert column['dependency']['technical_metadata'] is False
    assert column['unit']['status'] == 'mixed'
