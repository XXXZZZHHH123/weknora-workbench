"""Real PDF/Excel executions retain values and report bounded evidence honestly."""
from copy import deepcopy
import hashlib
import json

import pytest

from structured_data.result_contract import typed_sha256
from structured_data.table_analysis import analyze_table
from test_store import snapshot
from test_pdf_tables import setup, publish, bundle


def assert_binding(result):
    c=deepcopy(result['result_contract'])
    digest=c.pop('result_hash')
    assert c['schema_version']==2
    assert digest==typed_sha256({'payload':{k:result[k] for k in c['hash_input_keys']},'contract':c})
    assert c['evidence_capabilities']['cell_lineage_verified'] is False
    assert c['evidence_capabilities']['aggregate_membership_verified'] is False
    return c


def pdf(tmp_path, units=('kg', 'kg')):
    store,draft,plan=setup(tmp_path,bundle([['Label','Left','Right'],['A','1.25','2.5'],['B','','4']]),types=('text','decimal','decimal'))
    plan['columns'][1]['unit']=units[0]
    plan['columns'][2]['unit']=units[1]
    return store,publish(store,draft,plan)


def test_typed_digest_is_unambiguous_unicode_and_lossless():
    assert typed_sha256({'n':1.0,'中':'<\u2028'})==typed_sha256({'中':'<\u2028','n':1})
    assert typed_sha256(1)!=typed_sha256('1')
    assert typed_sha256([None,False,0])!=typed_sha256(['',0,False])
    assert typed_sha256(9007199254740993)!=typed_sha256(9007199254740992)
    # Cross-language stable fixture used by internal/structuredresult tests.
    assert typed_sha256({'n':9007199254740993,'s':'中<\u2028','values':[None,True,1.25,-0.0]})=='f6c198398de7d26f6531c811699342cf24cbf92904fbd4f7ed9b9649645d17a1'


def test_pdf_raw_cells_contract_does_not_mutate_published_record(tmp_path):
    store,table=pdf(tmp_path)
    before=store._read_table(table['table_id'])
    result=store.read_table(table['table_id'],limit=1)
    c=assert_binding(result)
    assert c['evidence_capabilities']['level']=='source_cells'
    assert c['source_snapshot']['versions']['record_sha256']==table['record_sha256']
    assert result['rows'][0]['source_cells']['col1'][0]['text']=='1.25'
    assert c['column_metadata'][1]['unit']['status']=='declared'
    assert c['column_metadata'][1]['unit']['evidence_level']=='reviewed_plan_declaration'
    assert c['column_metadata'][1]['unit']['evidence'][0]['original_annotation_verified'] is False
    assert before==store._read_table(table['table_id'])
    assert result==store.read_table(table['table_id'],limit=1)


def test_pdf_literal_dimension_and_sourced_measurement_units_are_separate(tmp_path):
    store,table=pdf(tmp_path)
    result=store.query(table['table_id'],"SELECT 'period label' AS dimension,col1 AS observation FROM data")
    c=assert_binding(result)
    assert result['columns']==['dimension','observation'] and result['rows'][0]==['period label','1.250000000000000000']
    assert c['column_metadata'][0]['dependency']['kind']=='constant_label'
    assert c['column_metadata'][0]['unit']['status']=='unknown'
    assert c['column_metadata'][1]['unit']['symbol']=='kg'
    assert c['column_metadata'][1]['unit']['status']=='declared'
    assert c['evidence_capabilities']['level']=='dependency_only'
    assert not c['evidence_capabilities']['source_cells_available']
    assert result['provenance']['sql_result_lineage_verified'] is False


@pytest.mark.parametrize('expression,unit,status',[
    ('SUM(col1)','kg','derived'),('AVG(col1)','kg','derived'),
    ('col1+col2','kg','derived'),('col1/col2','1','derived'),
    ('100*col1/col2','1','derived'),('col1*100','kg','derived'),
    ('CAST(col1 AS DECIMAL(20,2))','kg','derived'),
    ('COUNT(*)','1','derived'),('LN(col1)',None,'unknown'),
    ('CASE WHEN col1>0 THEN col1 ELSE 0 END',None,'unknown')])
def test_conservative_unit_algebra_never_invents_percent_or_conversion(tmp_path,expression,unit,status):
    store,table=pdf(tmp_path)
    result=store.query(table['table_id'],'SELECT '+expression+' AS computed FROM data')
    c=assert_binding(result)
    u=c['column_metadata'][0]['unit']
    assert u['status']==status and u.get('symbol')==unit
    assert u.get('symbol')!='%'


@pytest.mark.parametrize('right_unit,expected',[('kg','derived'),('m','mixed'),(None,'unknown')])
def test_union_all_and_unpivot_propagate_all_branches_not_only_first(tmp_path,right_unit,expected):
    store,table=pdf(tmp_path,('kg',right_unit))
    for sql in ('SELECT col1 AS v FROM data UNION ALL SELECT col2 FROM data',
                'UNPIVOT data ON col1,col2 INTO NAME dimension VALUE v'):
        result=store.query(table['table_id'],sql)
        c=assert_binding(result)
        units={x['name']:x['unit'] for x in c['column_metadata']}
        assert units['v']['status']==expected
        assert c['evidence_capabilities']['cell_lineage_verified'] is False
        if 'dimension' in units:
            assert units['dimension']['status']=='unknown'


def test_page_note_cannot_supply_unknown_column_unit(tmp_path):
    store,draft,plan=setup(tmp_path)
    table=publish(store,draft,plan)
    result=store.query(table['table_id'],'SELECT col1 FROM data')
    assert assert_binding(result)['column_metadata'][0]['unit']['status']=='unknown'


def test_excel_sql_keeps_input_scope_separate_from_aggregate_members(snapshot):
    store,ds,_=snapshot
    result=analyze_table(store,ds,['price'],'SELECT SUM(value) AS total,COUNT(*) AS n FROM data WHERE value>10')
    c=assert_binding(result)
    assert result['rows']==[[20,1]] and result['input_count']==3
    assert c['column_metadata'][0]['unit']['symbol']=='元'
    assert c['source_snapshot']['scope']=='loaded_inputs_before_user_sql'
    assert c['evidence_capabilities']['aggregate_membership_verified'] is False
    assert c['source_snapshot']['versions']['manifest_sha256']
    saved=json.loads((store.root/'analysis-results'/(result['result_id']+'.json')).read_text())
    assert saved['result_contract']==result['result_contract']


def test_excel_mixed_units_unknown_functions_and_constant_results_stay_honest(snapshot):
    store,ds,_=snapshot
    out=analyze_table(store,ds,['price','stock'],'SELECT value FROM data')
    assert assert_binding(out)['column_metadata'][0]['unit']['status']=='mixed'
    out=analyze_table(store,ds,['price'],'SELECT 9 AS invented,value,source_cell FROM data')
    c=assert_binding(out)
    assert [x['dependency']['kind'] for x in c['column_metadata']][:2]==['constant_label','source_value']
    assert c['column_metadata'][2]['dependency']['technical_metadata'] is True
    assert c['column_metadata'][0]['unit']['status']=='unknown'


def test_excel_raw_rows_keep_saved_formula_and_real_source_cells(snapshot):
    store,ds,_=snapshot
    out=store.query_series(ds,'price')
    c=assert_binding(out)
    assert c['evidence_capabilities']['source_cells_available']
    assert out['rows'][1]['formula']=='=NA()' and out['rows'][1]['value'] is None
    value=next(x for x in c['column_metadata'] if x['name']=='value')
    assert value['display_name']=='价格' and value['unit']['symbol']=='元'


def test_result_hash_changes_for_actual_rows_units_versions_and_range(tmp_path):
    store,table=pdf(tmp_path)
    results=[store.query(table['table_id'],s) for s in (
        'SELECT col1 FROM data','SELECT col1 FROM data LIMIT 1','SELECT col1*2 FROM data')]
    assert len({assert_binding(r)['result_id'] for r in results})==3


@pytest.mark.parametrize('sql',[
    'WITH unused AS(SELECT * FROM other) SELECT col1 FROM data',
    'WITH unused AS(WITH nested AS(SELECT * FROM other) SELECT 1) SELECT col1 FROM data',
    'SELECT col1 FROM data WHERE EXISTS(SELECT * FROM other)',
    "WITH unused AS(SELECT * FROM read_csv('/etc/passwd')) SELECT col1 FROM data",
    'SELECT col1 FROM data UNION ALL SELECT 999',
    'WITH x AS(SELECT 999 AS col1 FROM data) SELECT col1 FROM x',
    'SELECT * FROM information_schema.tables',
    'SELECT * FROM range(3)',
    'WITH x AS(SELECT * FROM other), unrelated AS(WITH other AS(SELECT col1 FROM data) SELECT col1 FROM other) SELECT col1 FROM data',
    # A self-named unused CTE must not authorize a real catalog relation when
    # the independent table-name inspector cannot bind UNPIVOT.
    'WITH sqlite_master AS(SELECT * FROM sqlite_master) SELECT * FROM (UNPIVOT data ON col1,col2 INTO NAME dimension VALUE v)',
    'WITH sqlite_master AS(SELECT * FROM sqlite_master) SELECT * FROM (UNPIVOT data ON col1,col2 INTO NAME dimension VALUE v) WHERE EXISTS(SELECT 1 FROM sqlite_master)',
    'WITH nested AS(WITH sqlite_master AS(SELECT * FROM sqlite_master) SELECT 1) SELECT * FROM (UNPIVOT data ON col1,col2 INTO NAME dimension VALUE v)',
])
def test_ast_fallback_keeps_every_relation_scoped_including_unused_lexical_ctes(tmp_path,sql):
    store,table=pdf(tmp_path)
    with pytest.raises(ValueError):store.query(table['table_id'],sql)
