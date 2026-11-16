"""SQL operations retain known unit conflicts without inventing declarations."""
from copy import deepcopy

import duckdb
import pytest

from structured_data.result_contract import excel_value_unit, mixed_unit, unknown_unit
from structured_data.sql_result_units import infer_sql_units


@pytest.fixture
def unit_connection():
    connection = duckdb.connect(':memory:')
    try:
        yield connection
    finally:
        connection.close()


def declared(symbol, column):
    return dict(status='declared', symbol=symbol, evidence_level='source_declaration',
                binding_scope='source_column', evidence=[dict(column=column, unit=symbol)])


@pytest.fixture
def mixed():
    return mixed_unit([declared('kg', 'left'), declared('m', 'right')],
                      'The source column contains incompatible measurement units')


@pytest.mark.parametrize('expression', [
    'SUM(value)', 'AVG(value)', 'MIN(value)', 'MAX(value)', 'ROUND(value, 2)',
    'ABS(value)', 'FLOOR(value)', 'CEIL(value)', 'CAST(value AS DECIMAL(20,2))',
    'CAST(CAST(value AS VARCHAR) AS DOUBLE)',
    'value * 2', '2 * value', 'value / 2', '+value', '-value',
    'ROUND(SUM(CAST(-value * 2 / 3 AS DOUBLE)), 2)',
])
def test_unit_preserving_expressions_keep_mixed_and_source_evidence(unit_connection, mixed, expression):
    before = deepcopy(mixed)
    unit = infer_sql_units(unit_connection, 'SELECT ' + expression + ' AS computed FROM data',
                           {'value': mixed}, 1)[0]
    assert unit['status'] == 'mixed'
    assert mixed['reason'] in unit['source_reasons']
    assert unit['source_units'] == ['kg', 'm']
    assert unit['evidence'] == mixed['evidence']
    assert not unit.get('symbol')
    assert unit['expression']
    assert mixed == before


@pytest.mark.parametrize('sql', [
    'SELECT CASE WHEN flag THEN value ELSE other END AS v FROM data',
    'SELECT CASE WHEN flag THEN other ELSE value END AS v FROM data',
    'SELECT COALESCE(other, value) AS v FROM data',
    'SELECT GREATEST(value, other) AS v FROM data',
    'SELECT value + other AS v FROM data',
    'SELECT value / other AS v FROM data',
    'SELECT value AS v FROM data UNION ALL SELECT other FROM data',
    'SELECT other AS v FROM data UNION ALL SELECT value FROM data',
])
@pytest.mark.parametrize('other_status', ['declared', 'unknown'])
def test_mixed_expression_branch_cannot_be_hidden_by_another_branch(
        unit_connection, mixed, sql, other_status):
    other = declared('kg', 'other') if other_status == 'declared' else unknown_unit('No declaration')
    unit = infer_sql_units(unit_connection, sql,
                           {'value': mixed, 'other': other, 'flag': unknown_unit()}, 1)[0]
    assert unit['status'] == 'mixed'
    assert unit['source_units'] == ['kg', 'm']
    assert all(entry in unit['evidence'] for entry in mixed['evidence'])
    assert mixed['reason'] in unit['source_reasons']
    assert not unit.get('symbol')


@pytest.mark.parametrize('expression', [
    'CASE WHEN flag THEN value ELSE other END',
    'CASE WHEN flag THEN other ELSE value END', 'COALESCE(value, other)', 'value + other',
])
def test_declared_and_unknown_remain_unknown(unit_connection, expression):
    unit = infer_sql_units(unit_connection, 'SELECT ' + expression + ' FROM data',
                           {'value': declared('kg', 'value'), 'other': unknown_unit(),
                            'flag': unknown_unit()}, 1)[0]
    assert unit['status'] == 'unknown'
    assert not unit.get('symbol')


def test_aggregate_over_mixed_union_retains_both_real_declarations(unit_connection):
    sources = {'left': declared('kg', 'left'), 'right': declared('m', 'right')}
    unit = infer_sql_units(unit_connection, '''
        WITH alternatives AS (
                SELECT "left" AS v FROM data UNION ALL SELECT "right" AS v FROM data
        ) SELECT SUM(v) FROM alternatives
    ''', sources, 1)[0]
    assert unit['status'] == 'mixed'
    assert unit['source_units'] == ['kg', 'm']
    assert unit['evidence'] == sources['left']['evidence'] + sources['right']['evidence']
    assert unit['expression'] == 'sql_sum'


def test_unpivot_mixed_branch_keeps_known_conflicts(unit_connection, mixed):
    units = infer_sql_units(unit_connection,
        'UNPIVOT data ON value,other INTO NAME dimension VALUE observation',
        {'value': mixed, 'other': unknown_unit()}, 2)
    assert units[0]['status'] == 'unknown'
    assert units[1]['status'] == 'mixed'
    assert units[1]['evidence'] == mixed['evidence']


def test_count_is_a_record_quantity_even_when_input_measurements_are_mixed(unit_connection, mixed):
    unit = infer_sql_units(unit_connection, 'SELECT COUNT(value) FROM data', {'value': mixed}, 1)[0]
    assert unit['status'] == 'derived'
    assert unit['quantity'] == 'record_count'
    assert unit['symbol'] == '1'


@pytest.mark.parametrize('symbols', [(None, 'kg', 'm'), ('kg', None, 'm'), ('kg', 'm', None)])
def test_unknown_source_cannot_hide_conflicting_known_units(unit_connection, symbols):
    series = {str(i): dict(unit=symbol, sheet='Data', header_cells=[str(i)])
              for i, symbol in enumerate(symbols)}
    unit = excel_value_unit(series, 'a' * 64)
    assert unit['status'] == 'mixed'
    assert unit['source_units'] == ['kg', 'm']
    assert len(unit['evidence']) == 2
    assert {entry['declared_unit'] for entry in unit['evidence']} == {'kg', 'm'}
    assert unknown_unit()['reason'] in unit['source_reasons']
    aggregate = infer_sql_units(unit_connection, 'SELECT SUM(value) FROM data', {'value': unit}, 1)[0]
    assert aggregate['status'] == 'mixed'
    assert aggregate['evidence'] == unit['evidence']
    assert set(unit['source_reasons']).issubset(aggregate['source_reasons'])
    assert unit['reason'] in aggregate['source_reasons']


@pytest.mark.parametrize('symbols', [(None, 'kg'), ('kg', None), ('kg', 'kg', None)])
def test_one_known_unit_with_unknown_sources_stays_unknown(symbols):
    unit = excel_value_unit({str(i): dict(unit=symbol) for i, symbol in enumerate(symbols)}, 'a' * 64)
    assert unit['status'] == 'unknown'
    assert not unit.get('symbol')


def test_unsupported_cast_retains_conflict_without_a_unit_derivation(unit_connection, mixed):
    unit = infer_sql_units(unit_connection, 'SELECT CAST(value AS VARCHAR) FROM data',
                           {'value': mixed}, 1)[0]
    assert unit['status'] == 'mixed'
    assert unit['source_units'] == mixed['source_units']
    assert unit['evidence'] == mixed['evidence']
    assert mixed['reason'] in unit['source_reasons']
    assert not unit.get('symbol')
    assert 'expression' not in unit


def test_coalesce_operator_uses_the_shared_alternative_unit_rule(unit_connection):
    source = {'left': declared('kg', 'left'), 'right': declared('kg', 'right')}
    unit = infer_sql_units(unit_connection, 'SELECT COALESCE("left", "right") FROM data', source, 1)[0]
    assert unit['status'] == 'derived' and unit['symbol'] == 'kg'
    assert unit['expression'] == 'coalesce_alternatives'
    assert unit['evidence'] == source['left']['evidence'] + source['right']['evidence']


@pytest.mark.parametrize('sql', [
    'SELECT CASE WHEN flag THEN "left" WHEN other_flag THEN missing ELSE "right" END FROM data',
    'SELECT COALESCE("left", missing, "right") FROM data',
])
def test_unknown_alternative_cannot_hide_two_known_conflicting_units(unit_connection, sql):
    source = {'left': declared('kg', 'left'), 'right': declared('m', 'right'),
              'missing': unknown_unit(), 'flag': unknown_unit(), 'other_flag': unknown_unit()}
    unit = infer_sql_units(unit_connection, sql, source, 1)[0]
    assert unit['status'] == 'mixed'
    assert unit['source_units'] == ['kg', 'm']
    assert unit['evidence'] == source['left']['evidence'] + source['right']['evidence']
    assert unknown_unit()['reason'] in unit['source_reasons']
    assert not unit.get('symbol')


def test_nested_union_with_missing_unit_stays_unverified(unit_connection):
    # Keep main's conservative result when a nested UNION has already lost
    # a single declaration through an unknown branch; no unit is certified.
    source = {'left': declared('kg', 'left'), 'right': declared('m', 'right'),
              'missing': unknown_unit()}
    unit = infer_sql_units(unit_connection,
        'SELECT "left" AS value FROM data UNION ALL SELECT missing FROM data '
        'UNION ALL SELECT "right" FROM data', source, 1)[0]
    assert unit['status'] == 'unknown'
    assert not unit.get('symbol')
