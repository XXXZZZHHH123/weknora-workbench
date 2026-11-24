"""MA windows preserve real dated gaps without revealing invalid numeric values."""
from contextlib import contextmanager

import duckdb
from fastapi import HTTPException
import pytest

from structured_data.research_facts import _bounded_parameters, _bounded_rows, _parameters
from structured_data.research_facts_v2 import parameters

ID = 'kb-' + 'a' * 48
OTHER = 'kb-' + 'b' * 48
PERIOD = 'kb-' + 'c' * 48

@pytest.fixture
def store():
    con = duckdb.connect(':memory:')
    con.execute('CREATE TABLE observations(series_id VARCHAR,date VARCHAR,value DOUBLE,value_status VARCHAR,source_cell VARCHAR)')
    con.executemany('INSERT INTO observations VALUES (?,?,?,?,?)',[
        ('source','2031-01-01',1,'valid','B2'),('source','2031-01-02',987654321,'error','B3'),
        ('source','2031-01-03',3,'valid','B4'),('source','2031-01-04',None,'missing','B5'),
        ('private','2031-01-04',123456789,'valid','C5')])
    class Store:
        @contextmanager
        def _connect(self, dataset):
            assert dataset == 'synthetic'; yield con
    yield Store(), con
    con.close()

def selection(**extra):
    return {'series_ids':[ID], 'as_of':'2031-01-04','points_per_series':3,'include_invalid':True,**extra}

def test_dated_invalid_periods_stay_in_the_bounded_window_and_hide_raw_values(store):
    service, _ = store
    rows, undated = _bounded_rows(service,[({'dataset_id':'synthetic'},{'source':ID})],selection(),strict=True)
    assert rows == [
        {'series_id':ID,'date':'2031-01-02','value':None,'value_status':'missing'},
        {'series_id':ID,'date':'2031-01-03','value':3,'value_status':'valid'},
        {'series_id':ID,'date':'2031-01-04','value':None,'value_status':'missing'}]
    assert not undated
    assert '987654321' not in repr(rows) and '123456789' not in repr(rows) and OTHER not in repr(rows)

def test_old_default_retains_its_original_valid_only_behavior(store):
    service, con = store
    con.execute("INSERT INTO observations VALUES ('source',NULL,50,'valid','B6')")
    rows, undated = _bounded_rows(service,[({'dataset_id':'synthetic'},{'source':ID})],
        {'series_ids':[ID],'as_of':'2031-01-04','points_per_series':2},strict=True)
    assert [(row['date'],row['value']) for row in rows] == [('2031-01-01',1),('2031-01-03',3)]
    assert not undated

def test_undated_and_mixed_series_are_excluded_while_dated_series_keep_their_window(store):
    service, con = store
    con.executemany('INSERT INTO observations VALUES (?,?,?,?,?)', [
        ('mixed','2031-01-01',30,'valid','C2'), ('mixed',None,40,'valid','C3'),
        ('period',None,50,'valid','D2'), ('private',None,60,'valid','E2')])
    rows, undated = _bounded_rows(service,
        [({'dataset_id':'synthetic'},{'source':ID,'mixed':OTHER,'period':PERIOD})],
        selection(series_ids=[ID,OTHER,PERIOD]),strict=True)
    assert undated == {OTHER,PERIOD}
    assert [(row['series_id'],row['date'],row['value']) for row in rows] == [
        (ID,'2031-01-02',None),(ID,'2031-01-03',3),(ID,'2031-01-04',None)]

def test_conflicting_valid_duplicates_are_not_averaged_or_arbitrarily_chosen(store):
    service, con = store
    con.execute("INSERT INTO observations VALUES ('source','2031-01-03',55,'valid','B6')")
    with pytest.raises(HTTPException) as error:
        _bounded_rows(service,[({'dataset_id':'synthetic'},{'source':ID})],selection(),strict=True)
    assert error.value.status_code == 409

def test_explicit_ma_window_is_available_for_both_protocols_but_old_default_stays_two():
    body = {'series_ids':[ID],'view_module':'judgments','points_per_series':121,'include_invalid':True}
    assert _bounded_parameters(body)['include_invalid'] is True
    assert parameters({'source_bindings':['fixture-source'],**body})['selection']['include_invalid'] is True
    with pytest.raises(ValueError): _bounded_parameters({**body,'include_invalid':False})
    with pytest.raises(ValueError): _bounded_parameters({**body,'points_per_series':122})

@pytest.mark.parametrize('value',[None,1,'true',[],{}])
def test_gap_flag_is_a_strict_boolean(value):
    with pytest.raises(ValueError):
        _bounded_parameters({'series_ids':[ID],'view_module':'judgments','include_invalid':value})

@pytest.mark.parametrize('module',['home','market','indicators','strategies','diagram','reports',None])
def test_gap_flag_cannot_expand_unrelated_page_windows(module):
    with pytest.raises(ValueError):
        _bounded_parameters({'series_ids':[ID],'view_module':module,'include_invalid':True},extended=True)

def test_gap_flag_cannot_be_used_for_catalog_or_unbounded_reads():
    with pytest.raises(ValueError): _bounded_parameters({'include_invalid':True})
    with pytest.raises(ValueError): parameters({'source_bindings':[],'catalog_only':True,'include_invalid':True})
    with pytest.raises(ValueError): parameters({'source_bindings':[],'include_invalid':True})
