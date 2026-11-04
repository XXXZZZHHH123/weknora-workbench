"""PDF SQL outputs must bind to actual values, not merely mention a source."""
import duckdb
import pytest

from structured_data.pdf_sql_provenance import verify_pdf_sql_dependencies


@pytest.fixture
def scoped():
    with duckdb.connect(config={'enable_external_access': 'false', 'threads': '1'}) as connection:
        connection.execute('CREATE TABLE data (_row_id VARCHAR, label VARCHAR, amount DOUBLE)')
        connection.execute("INSERT INTO data VALUES ('row-a','A',2.5),('row-b','B',-1.5)")
        yield connection


def dependencies(connection, sql, columns=('label', 'amount')):
    result = verify_pdf_sql_dependencies(connection, sql, columns)
    # Check actual DuckDB binding as well as the AST projection. The production
    # query repeats this positional check before returning provenance metadata.
    rows = connection.execute(sql)
    assert len(result) == len(rows.description)
    return result, rows.fetchall()


@pytest.mark.parametrize('sql', [
    'SELECT 123',
    'SELECT UNNEST([1,2]) AS period,UNNEST([20,30]) AS observation',
    'SELECT 123 FROM data',
    'SELECT _row_id,123 FROM data',
    'SELECT 123 FROM data WHERE amount > 0',
    'WITH unused AS(SELECT amount FROM data) SELECT 123',
    'WITH x AS(SELECT 123 AS amount FROM data) SELECT amount FROM x',
    'WITH data AS(SELECT 123 AS amount) SELECT amount FROM data',
    'WITH x AS(SELECT amount FROM data), data AS(SELECT 123 AS amount) SELECT amount FROM data',
    'WITH x AS(SELECT 123 AS amount) SELECT x.amount FROM x CROSS JOIN data',
    'SELECT COUNT(*) FROM (SELECT 123) x',
    'SELECT COUNT(*) FROM (SELECT amount FROM data UNION ALL SELECT 999) x',
    'SELECT UNNEST([amount,999]) FROM data',
    'SELECT amount FROM data UNION ALL SELECT 999',
    'SELECT 123 WHERE EXISTS(SELECT amount FROM data)',
])
def test_constant_observations_cannot_acquire_pdf_source_authority(scoped, sql):
    with pytest.raises(ValueError, match='source dependency cannot be verified'):
        verify_pdf_sql_dependencies(scoped, sql, ['label', 'amount'])


def test_reused_ctes_cannot_expand_dependency_analysis_without_a_total_budget(scoped):
    definitions = ['x0 AS (SELECT amount AS observation FROM data)']
    definitions += [f'x{i} AS (SELECT a.observation FROM x{i-1} a CROSS JOIN x{i-1} b)' for i in range(1, 18)]
    sql = 'WITH ' + ','.join(definitions) + ' SELECT observation FROM x17'
    assert len(sql) < 16000
    # Only parse/bind provenance. Never execute this deliberately expanding join.
    with pytest.raises(ValueError, match='dependency analysis node budget exceeded'):
        verify_pdf_sql_dependencies(scoped, sql, ['label', 'amount'])


@pytest.mark.parametrize('sql', [
    "SELECT * FROM read_csv('/etc/passwd')",
    "SELECT * FROM query('SELECT amount FROM data')",
    'SELECT * FROM duckdb_tables()',
    'SELECT amount FROM data WHERE EXISTS(SELECT * FROM duckdb_tables())',
    'WITH unused AS(SELECT * FROM duckdb_tables()) SELECT amount FROM data',
    'SELECT amount FROM other_scope.data',
    'SELECT amount FROM other',
    'SELECT * FROM range(10)',
])
def test_relations_including_unused_ctes_and_predicates_remain_scoped(scoped, sql):
    with pytest.raises(ValueError, match='source dependency cannot be verified'):
        verify_pdf_sql_dependencies(scoped, sql, ['label', 'amount'])


def test_real_fields_casts_scales_thresholds_and_source_count_are_distinct(scoped):
    sql = """WITH base(k,v) AS (SELECT label,amount*1000 FROM data)
             SELECT k,CAST(v AS DECIMAL(20,2)) AS converted,
                    'series label' AS series, 2000 AS dimension_label,
                    SUM(CASE WHEN v > 0 THEN 1 ELSE 0 END) OVER() AS n_positive,
                    COUNT(*) OVER() AS n
             FROM base ORDER BY k"""
    columns, rows = dependencies(scoped, sql)
    assert rows[0][0] == 'A' and float(rows[0][1]) == 2500
    assert [c['kind'] for c in columns] == [
        'source_value', 'source_value', 'constant_label', 'constant_label', 'source_value', 'source_cardinality']
    assert columns[1]['source_columns'] == ['amount']
    assert columns[4]['source_columns'] == ['amount']
    assert columns[5]['source_row_dependency'] and not columns[5]['technical_metadata']


def test_cte_alias_scopes_cannot_rebind_a_literal_to_a_real_column_name(scoped):
    columns, _ = dependencies(scoped, """WITH actual AS (SELECT amount AS x, label AS y FROM data),
        labels AS (SELECT 999 AS amount, x AS observed, y AS label FROM actual)
        SELECT amount,observed,label FROM labels""")
    assert columns[0]['kind'] == 'constant_label'
    assert columns[0]['source_columns'] == []
    assert columns[1]['kind'] == 'source_value'
    assert columns[1]['source_columns'] == ['amount']


def test_technical_row_identity_stays_technical_through_aliases_and_calculations(scoped):
    columns, _ = dependencies(scoped, """WITH x AS(SELECT _row_id AS hidden_id,amount FROM data)
        SELECT LENGTH(hidden_id) AS invented_measure, amount FROM x""")
    assert columns[0]['kind'] == 'source_metadata'
    assert columns[0]['source_columns'] == ['_row_id']
    assert columns[0]['technical_metadata']
    counts, rows = dependencies(scoped, 'SELECT COUNT(_row_id) AS n FROM data')
    assert rows == [(2,)]
    assert counts[0]['kind'] == 'source_cardinality'
    assert not counts[0]['technical_metadata']
    columns, _ = dependencies(scoped, 'SELECT label,CASE WHEN amount IS NOT NULL THEN LENGTH(_row_id) ELSE 0 END FROM data')
    assert columns[1]['technical_metadata']
    assert columns[1]['source_columns'] == ['_row_id', 'amount']


@pytest.mark.parametrize('sql', [
    'SELECT label,amount FROM data UNION ALL SELECT label,999 FROM data',
    'SELECT label,UNNEST([amount,999]) FROM data',
    'SELECT label,UNNEST(list_concat([amount],[999])) FROM data',
    'SELECT label,UNNEST(array_resize([amount],3,999)) FROM data',
    'SELECT label,UNNEST(list_resize([amount],3,999)) FROM data',
    'SELECT label,x.v FROM data CROSS JOIN LATERAL (VALUES(amount),(999)) x(v)',
])
def test_a_real_category_cannot_launder_mixed_constant_measurements(scoped, sql):
    columns, _ = dependencies(scoped, sql)
    assert columns[0]['kind'] == 'source_value'
    assert columns[1]['kind'] == 'mixed_constant_values'


@pytest.mark.parametrize('sql', [
    "SELECT label,UNNEST(string_split(CAST(amount AS VARCHAR)||',999',',')) FROM data",
    'SELECT label,UNNEST(flatten([[amount],[999]])) FROM data',
    'SELECT label,UNNEST(CAST(amount AS DOUBLE[])) FROM data',
])
def test_unverified_collection_generators_fail_closed(scoped, sql):
    with pytest.raises(ValueError, match='UNNEST requires verified source-element arrays'):
        verify_pdf_sql_dependencies(scoped, sql, ['label', 'amount'])


@pytest.mark.parametrize('sql', [
    "WITH x AS(SELECT label,struct_pack(actual := amount,invented := 999) AS point FROM data) "
    "SELECT label,struct_extract(point,'invented') FROM x",
    "WITH x AS(SELECT label,map(['actual','invented'],[amount,999]) AS point FROM data) "
    "SELECT label,map_extract_value(point,'invented') FROM x",
    "SELECT label,json_extract(json_object('actual',amount,'invented',999),'$.invented') FROM data",
])
def test_unverified_composite_fields_cannot_inherit_unrelated_field_dependencies(scoped, sql):
    with pytest.raises(ValueError, match='composite construction/field extraction is unsupported'):
        verify_pdf_sql_dependencies(scoped, sql, ['label', 'amount'])


def test_source_list_aggregates_and_null_array_fills_preserve_element_dependencies(scoped):
    columns, rows = dependencies(scoped, 'SELECT UNNEST(list(amount ORDER BY label)) FROM data')
    assert rows == [(2.5,), (-1.5,)]
    assert columns[0]['kind'] == 'source_value'
    columns, rows = dependencies(scoped, 'SELECT label,UNNEST(array_resize([amount],2)) FROM data')
    assert rows == [('A', 2.5), ('A', None), ('B', -1.5), ('B', None)]
    assert columns[1]['kind'] == 'source_value'
    columns, _ = dependencies(scoped, 'SELECT label,UNNEST(list_prepend(amount,[amount])) FROM data')
    assert columns[1]['kind'] == 'source_value'
    # A real aggregate filter cannot upgrade a literal array into observations.
    columns, _ = dependencies(scoped, 'SELECT COUNT(*) AS n,UNNEST(list(999) FILTER(WHERE amount > 0)) FROM data')
    assert columns[1]['kind'] == 'constant_label'


def test_null_alternatives_remain_missing_values(scoped):
    columns, _ = dependencies(scoped, 'SELECT label,amount FROM data UNION ALL SELECT label,NULL FROM data')
    assert columns[1]['kind'] == 'source_value'
    assert columns[1]['source_columns'] == ['amount']


def test_subquery_column_names_bind_to_the_nearest_scope(scoped):
    columns, rows = dependencies(scoped,
        'SELECT d.label,(SELECT MAX(amount) FROM data) AS maximum FROM data d ORDER BY d.label')
    assert rows == [('A', 2.5), ('B', 2.5)]
    assert columns[1]['source_columns'] == ['amount']
    columns, rows = dependencies(scoped,
        'SELECT d.label,(SELECT d.amount*2) AS doubled FROM data d ORDER BY d.label')
    assert rows == [('A', 5.0), ('B', -3.0)]
    assert columns[1]['source_columns'] == ['amount']
    # A source table in the enclosing query does not turn the fixed one-row
    # cardinality of a SELECT without FROM into a source-table statistic.
    columns, _ = dependencies(scoped, 'SELECT label,(SELECT COUNT(*)) AS invented FROM data')
    assert columns[1]['kind'] == 'constant_label'


def test_horizontal_rows_can_be_reshaped_using_actual_source_cells():
    with duckdb.connect(config={'enable_external_access': 'false', 'threads': '1'}) as connection:
        connection.execute('CREATE TABLE data (_row_id VARCHAR, axis VARCHAR, c1 VARCHAR, c2 VARCHAR)')
        connection.execute("INSERT INTO data VALUES ('r1','period','2041','2044'),('r2','value','15.5','18.75')")
        sql = """WITH cells AS (
            SELECT axis, UNNEST([c1,c2]) AS cell, UNNEST([1,2]) AS position FROM data
        ), points AS (
            SELECT position, MAX(CASE WHEN axis='period' THEN cell END) AS period,
                   MAX(CASE WHEN axis='value' THEN cell END) AS observation
            FROM cells GROUP BY position
        ) SELECT CAST(period AS INTEGER) AS period, CAST(observation AS DOUBLE) AS observation
          FROM points ORDER BY period"""
        columns, rows = dependencies(connection, sql, ('axis', 'c1', 'c2'))
        assert rows == [(2041, 15.5), (2044, 18.75)]
        assert all(c['kind'] == 'source_value' for c in columns)
        assert all(c['source_columns'] == ['axis', 'c1', 'c2'] for c in columns)
        # The same source layout also supports native UNPIVOT. Header labels
        # remain labels; the values still depend on actual source fields.
        columns, rows = dependencies(connection,
            'UNPIVOT data ON c1,c2 INTO NAME period_column VALUE observation', ('axis', 'c1', 'c2'))
        assert len(rows) == 4
        assert columns[-2]['kind'] == 'constant_label'
        assert columns[-1]['source_columns'] == ['c1', 'c2']


@pytest.mark.parametrize('sql', [
    'SELECT * EXCLUDE(_row_id) REPLACE(amount*2 AS amount) FROM data',
    'SELECT d.label,d.amount FROM data d JOIN data e ON d._row_id=e._row_id',
    'SELECT label,amount AS v,v*2 AS doubled FROM data',
    'SELECT (SELECT SUM(amount) FROM data) AS total',
    'SELECT * FROM (SELECT SUM(amount) FROM data) x',
])
def test_supported_binding_shapes_retain_actual_output_dependencies(scoped, sql):
    columns, _ = dependencies(scoped, sql)
    assert all(c['kind'] in ('source_value', 'source_cardinality') for c in columns)
