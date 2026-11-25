"""Real metadata labels and mixed-unit hazards survive the SQL result contract."""
from copy import deepcopy
import json

import pytest

from structured_data.result_contract import excel_value_unit
from structured_data.table_analysis import analyze_table
from test_result_contract import assert_binding
from test_store import snapshot


@pytest.mark.parametrize('expression', [
    'SUM(value)', 'ROUND(SUM(value), 2)',
    'SUM(CASE WHEN date IS NOT NULL THEN value END)',
    'SUM(CASE WHEN date IS NOT NULL THEN value ELSE 0 END)',
    'SUM(value) * 100', 'SUM(value) / SUM(value)', 'SUM(value) * SUM(value)',
    'SQRT(SUM(value))', 'CAST(CAST(SUM(value) AS VARCHAR) AS DOUBLE)',
    'COALESCE(SUM(value), 0)',
])
def test_mixed_measurements_cannot_become_an_unknown_single_axis(snapshot, expression):
    store, dataset, _ = snapshot
    result = analyze_table(store, dataset, ['price', 'stock'],
                           'SELECT ' + expression + ' AS total FROM data')
    unit = assert_binding(result)['column_metadata'][0]['unit']
    assert unit['status'] == 'mixed'
    assert {e['declared_unit'] for e in unit['evidence']} == {'元', '吨'}
    assert {e['series_id'] for e in unit['evidence']} == {'price', 'stock'}
    assert all(e['source_sha256'] == 'a' * 64 for e in unit['evidence'])
    saved = json.loads((store.root / 'analysis-results' / (result['result_id'] + '.json')).read_text())
    assert saved['result_contract'] == result['result_contract']


PIVOT_SQL = """
WITH totals AS (
    SELECT metric, strftime('%Y', date) AS yr, SUM(value) AS total
    FROM data GROUP BY metric, yr
)
SELECT metric AS category,
       SUM(CASE WHEN yr = '2023' THEN total END) AS observed
FROM totals GROUP BY metric ORDER BY metric
"""


def test_mixed_cte_pivot_does_not_claim_sql_conditions_bind_units(snapshot):
    store, dataset, _ = snapshot
    result = analyze_table(store, dataset, ['price', 'stock'], PIVOT_SQL)
    columns = assert_binding(result)['column_metadata']
    assert result['rows'] == [['价格', 30], ['库存', 30]]
    assert columns[1]['unit']['status'] == 'mixed'
    assert columns[0]['dependency']['source_columns'] == ['metric']
    assert columns[0]['dependency']['technical_metadata'] is False


@pytest.mark.parametrize('sql', [
    'SELECT * EXCLUDE(series_id, date, source_cell, date_cell, period, value_status, metric, sheet, unit) FROM data',
    'SELECT (SELECT SUM(value) FROM data) AS total FROM data LIMIT 1',
])
def test_unsupported_unit_shape_preserves_proven_mixed_source_dependency(snapshot, sql):
    store, dataset, _ = snapshot
    result = analyze_table(store, dataset, ['price', 'stock'], sql)
    contract = assert_binding(result)
    assert contract['evidence_capabilities']['column_dependencies_verified'] is True
    assert contract['column_metadata'][0]['unit']['status'] == 'mixed'


def test_same_unit_cte_pivot_uses_real_names_without_technical_id_labels(snapshot):
    store, _, parsed = snapshot
    changed = deepcopy(parsed)
    changed['sha256'] = 'b' * 64
    changed['series'][1]['unit'] = '元'
    dataset = store.publish(changed)['dataset_id']
    result = analyze_table(store, dataset, ['price', 'stock'], PIVOT_SQL)
    contract = assert_binding(result)
    assert contract['evidence_capabilities']['column_dependencies_verified'] is True
    assert result['rows'] == [['价格', 30], ['库存', 30]]
    label, value = contract['column_metadata']
    assert label['dependency'] == dict(kind='source_value', source_columns=['metric'],
                                      source_row_dependency=False, technical_metadata=False)
    assert value['dependency']['technical_metadata'] is False
    assert value['unit']['status'] == 'derived' and value['unit']['symbol'] == '元'
    assert {e['series_id'] for e in value['unit']['evidence']} == {'price', 'stock'}


def test_real_metric_unit_projection_preserves_imported_metadata(snapshot):
    store, dataset, _ = snapshot
    result = analyze_table(store, dataset, ['price'],
                           'SELECT metric AS category, unit AS declared_unit, SUM(value) AS total '
                           'FROM data GROUP BY metric, unit')
    assert result['rows'] == [['价格', '元', 30]]
    label, unit, total = assert_binding(result)['column_metadata']
    assert label['dependency']['source_columns'] == ['metric']
    assert unit['dependency']['source_columns'] == ['unit']
    assert all(not c['dependency']['technical_metadata'] for c in (label, unit, total))
    assert total['unit']['symbol'] == '元'


def test_case_from_series_id_remains_technical_even_with_business_looking_labels(snapshot):
    store, dataset, _ = snapshot
    result = analyze_table(store, dataset, ['price'],
                           "SELECT CASE series_id WHEN 'price' THEN '真实名称的声称' "
                           "ELSE '其他' END AS category, SUM(value) AS total "
                           'FROM data GROUP BY series_id')
    label = assert_binding(result)['column_metadata'][0]
    assert label['dependency']['technical_metadata'] is True
    assert label['dependency']['source_columns'] == ['series_id']
    assert label['unit']['status'] == 'unknown'
    assert 'direct_source_column' not in label['operation']


@pytest.mark.parametrize(('expression', 'direct'), [
    ('series_id', True),
    ("CASE WHEN value IS NOT NULL THEN series_id ELSE series_id END", False),
    ("split_part(series_id, '.', 1)", False),
    ('CAST(series_id AS VARCHAR)', False),
])
def test_series_id_projection_proof_follows_renamed_cte_columns(snapshot, expression, direct):
    store, dataset, _ = snapshot
    result = analyze_table(store, dataset, ['price', 'stock'],
                           'WITH selected AS (SELECT ' + expression + ' AS ref, value FROM data) '
                           'SELECT s.ref AS category, avg(value) FROM selected s GROUP BY s.ref')
    label = assert_binding(result)['column_metadata'][0]
    assert label['dependency']['technical_metadata'] is True
    assert label['operation'].get('direct_source_column') == ('series_id' if direct else None)


@pytest.mark.parametrize('sql', [
    "SELECT COUNT(value) AS n, 'caption' AS label FROM data",
    "SELECT (SELECT COUNT(value) FROM data) AS n, 'caption' AS label FROM data LIMIT 1",
])
def test_count_and_literal_outputs_do_not_inherit_measurement_unit_conflicts(snapshot, sql):
    store, dataset, _ = snapshot
    result = analyze_table(store, dataset, ['price', 'stock'], sql)
    count, label = assert_binding(result)['column_metadata']
    assert result['rows'] == [[3, 'caption']]
    assert count['unit']['status'] == 'derived' and count['unit']['symbol'] == '1'
    assert count['unit']['quantity'] == 'record_count'
    assert label['unit']['status'] == 'unknown'


def test_missing_unit_stays_unknown_without_erasing_a_separate_known_conflict(snapshot):
    store, _, parsed = snapshot
    changed = deepcopy(parsed)
    changed['sha256'] = 'c' * 64
    changed['series'][0]['unit'] = None
    dataset = store.publish(changed)['dataset_id']
    result = analyze_table(store, dataset, ['price'], 'SELECT SUM(value) AS total FROM data')
    assert assert_binding(result)['column_metadata'][0]['unit']['status'] == 'unknown'
    declarations = {s['series_id']: s for s in parsed['series']}
    declarations['missing'] = dict(unit=None)
    unit = excel_value_unit(declarations, parsed['sha256'])
    assert unit['status'] == 'mixed'
    assert {e['declared_unit'] for e in unit['evidence']} == {'元', '吨'}
