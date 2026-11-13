import duckdb
from structured_data.sql_result_operations import describe_sql_operations
from structured_data.table_analysis import analyze_table
from test_store import snapshot
from test_result_contract import assert_binding


def test_alias_cannot_turn_endpoint_into_peak():
    with duckdb.connect() as con:
        con.execute("CREATE TABLE data(date DATE, value DOUBLE)")
        con.execute("INSERT INTO data VALUES ('2020-01-01',90),('2020-12-31',12)")
        sql = "SELECT arg_max(value,date) AS annual_peak, max(value) AS closing FROM data"
        assert con.execute(sql).fetchone() == (12,90)
        metadata = describe_sql_operations(con,sql,2)
        assert metadata[0]['meaning'] == 'first_argument_at_extreme_of_second_argument'
        assert metadata[0]['arguments'][1]['names'] == ['date']
        assert metadata[1]['meaning'] == 'extreme_of_argument_itself'
        assert metadata == describe_sql_operations(con,sql.replace('annual_peak','x').replace('closing','y'),2)


def test_unknown_shapes_do_not_invent_projection_lineage():
    with duckdb.connect() as con:
        for sql in ['SELECT * FROM data', 'SELECT 1 UNION SELECT 2', 'SELECT row_number() OVER () FROM data']:
            assert describe_sql_operations(con,sql,1)[0]['kind'] == 'unknown'
        result = describe_sql_operations(con,'WITH x AS (SELECT max(value) AS v FROM data) SELECT v FROM x',1)
        assert result[0]['kind'] == 'column_reference'
        assert result[0]['scope'] == 'sql_relation_not_original_cell'


def test_actual_result_persists_hash_bound_operations(snapshot):
    store,ds,_ = snapshot
    result = analyze_table(store,ds,['price'],'SELECT arg_max(value,date) AS misleading, max(value) AS peak FROM data')
    contract = assert_binding(result)
    assert contract['column_metadata'][0]['operation']['name'] == 'arg_max'
    assert contract['column_metadata'][1]['operation']['name'] == 'max'
    assert contract['evidence_capabilities']['aggregate_membership_verified'] is False
