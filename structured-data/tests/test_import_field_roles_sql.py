"""A numeric-looking annotation never regains numeric authority through SQL."""
from copy import deepcopy
from datetime import datetime

from openpyxl import Workbook
import pytest

from structured_data.imports import ImportManager
from structured_data.table_analysis import AnalysisSQLError, analyze_table


@pytest.mark.parametrize('role', ['annotation', 'text_dimension'])
@pytest.mark.parametrize('layout', ['row', 'matrix'])
def test_non_numeric_field_role_cannot_aggregate_numeric_raw_values(tmp_path, role, layout):
    path = tmp_path / 'field-roles.xlsx'
    book = Workbook(); sheet = book.active; sheet.title = 'Records'
    if layout == 'row':
        sheet.append(['Date', 'Measure', 'Supplement'])
        sheet.append([datetime(2024, 1, 1), 4, 101])
        sheet.append([datetime(2024, 1, 2), 5, 102])
        raw_values = {'101', '102'}
        expected_count, expected_numeric_sum = 2, 9
    else:
        sheet.append(['Measure', '2021', '2022', '2023'])
        sheet.append(['Quantity', 3, 4, 5])
        sheet.append(['Supplement', 101, 102, 103])
        raw_values = {'101', '102', '103'}
        expected_count, expected_numeric_sum = 3, 12
    book.save(path)

    manager = ImportManager(tmp_path / 'data')
    first = manager.ingest(path, use_reviewed_mappings=False)
    mapping = deepcopy(manager.sheet(first['import_id'], 'Records')['mapping'])
    block = mapping['blocks'][0]
    targets = block['row_rules'] if layout == 'matrix' else block['columns']
    targets[0]['field_role'] = 'numeric'
    targets[1]['field_role'] = role
    saved = manager.confirm(first['import_id'], mapping)
    dataset_id = saved['dataset_id']
    fields = manager.store.list_series(dataset_id)['series']
    numeric = next(field for field in fields if field['field_metadata']['role'] == 'numeric')
    non_numeric = next(field for field in fields if field['field_metadata']['role'] == role)

    rows = manager.store.query_series(dataset_id, non_numeric['series_id'])['rows']
    assert {str(row['raw_value']) for row in rows} == raw_values
    assert all(row['value'] is None and row['value_status'] == role for row in rows)
    with pytest.raises(ValueError, match='不能作为数值指标聚合'):
        manager.store.query_series(dataset_id, non_numeric['series_id'], operation='sum')

    result = analyze_table(manager.store, dataset_id, [non_numeric['series_id']],
                           'SELECT sum(value), count(value), count(*) FROM data')
    assert result['rows'] == [[None, 0, expected_count]]
    assert result['inputs'][non_numeric['series_id']]['valid_count'] == 0
    with pytest.raises(AnalysisSQLError, match='raw_value'):
        analyze_table(manager.store, dataset_id, [non_numeric['series_id']],
                      'SELECT sum(CAST(raw_value AS DOUBLE)) FROM data')

    # A mixed SQL input cannot let annotations contribute their raw numbers.
    combined = analyze_table(manager.store, dataset_id,
                             [numeric['series_id'], non_numeric['series_id']],
                             'SELECT sum(value), count(value) FROM data')
    assert combined['rows'] == [[expected_numeric_sum, expected_count]]
