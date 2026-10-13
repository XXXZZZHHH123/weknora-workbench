from copy import deepcopy
from pathlib import Path
import hashlib
import json
import pytest
from openpyxl import Workbook
from structured_data.profiles.reviewed import reviewed_mapping, parse_workbook
from structured_data.store import DataStore


def test_unreviewed_file_cannot_inherit_legacy_mapping(tmp_path):
    path = tmp_path / 'new.xlsx'
    workbook = Workbook()
    workbook.active.title = '郑棉期货'
    workbook.active.append(['日期', '收盘价'])
    workbook.save(path)
    assert reviewed_mapping(path) is None
    with pytest.raises(ValueError, match='文件指纹'):
        parse_workbook(path)
    from structured_data.imports import ImportManager
    manager = ImportManager(tmp_path / 'data')
    result = manager.ingest(path)
    assert manager.sheet(result['import_id'], '郑棉期货')['status'] != 'trusted_existing'


def dataset(tmp_path, units=(None, None), issues=None):
    store = DataStore(tmp_path)
    fields = [dict(series_id=sid, sheet='arbitrary records', metric='value '+sid,
                   unit=unit, dimensions={}, header_cells=[col+'1'])
              for sid, col, unit in zip(('a', 'b'), ('B', 'C'), units)]
    rows = [dict(series_id=sid, date='2023-01-01', value=value, source_cell=col+'2',
                 date_cell='A2', formula=None, value_status='valid', raw_value=value)
            for sid, col, value in [('a', 'B', 10), ('b', 'C', 25)]]
    identifier = store.publish(dict(source_file='fixture.xlsx', sha256='f'*64, rule_version='fixture',
        series=fields, observations=rows, sheets=[], excluded_regions=[], issues=issues or []))['dataset_id']
    return store, identifier


def test_raw_difference_requires_explicit_plan_for_every_sheet(tmp_path):
    store, identifier = dataset(tmp_path)
    with pytest.raises(ValueError, match='单位'):
        store.compare_series(identifier, 'a', 'b', '2023-01-01')
    result = store.compare_series(identifier, 'a', 'b', '2023-01-01', 'raw_values')
    assert result['difference'] == -15 and result['unit'] is None and result['warnings']


def test_raw_mode_never_ignores_conflicting_explicit_units(tmp_path):
    store, identifier = dataset(tmp_path, ('吨', '元'))
    with pytest.raises(ValueError, match='单位'):
        store.compare_series(identifier, 'a', 'b', '2023-01-01', 'raw_values')


def test_source_warning_requires_actual_stored_evidence(tmp_path):
    store, identifier = dataset(tmp_path)
    assert not any('ShowBlank' in item for item in store.query_series(identifier, 'a')['warnings'])
    (store._path(identifier) / 'issues.json').write_text(json.dumps([
        {'sheet':'arbitrary records', 'cell':'B2', 'code':'source_zero_fill', 'message':'actual saved source flag'}]))
    assert 'actual saved source flag' in store.query_series(identifier, 'a')['warnings']


def test_reviewed_currency_metadata_round_trips_import(tmp_path):
    from datetime import datetime
    from structured_data.scanner import scan_workbook
    from structured_data.generic_parser import parse_mapped_workbook
    path = tmp_path / 'rate.xlsx'
    workbook = Workbook()
    workbook.active.append(['date', 'quoted currency per base currency'])
    workbook.active.append([datetime(2024, 1, 2), 1.25])
    workbook.save(path)
    sheet = scan_workbook(path)['sheets'][0]
    mapping = {'sheet':sheet['name'],'fingerprint':sheet['fingerprint'],'blocks':[{'date_mode':'date','date_column':'A','header_rows':[1],'data_start_row':2,'data_end_row':2,'columns':[{'column':'B','metric':'source rate','unit':None,'header_cells':['B1']}]}]}
    column = mapping['blocks'][0]['columns'][0]
    column['exchange_rate'] = {'base_currency':'USD', 'quote_currency':'CNY', 'evidence_cells':['B1']}
    result = parse_mapped_workbook(path, [mapping])
    assert result['series'][0]['exchange_rate'] == column['exchange_rate']
    column['exchange_rate']['evidence_cells'] = ['Z99']
    with pytest.raises(ValueError, match='元数据'):
        parse_mapped_workbook(path, [mapping])


def test_sample_visibility_uses_explicit_source_metadata_not_names(tmp_path):
    from structured_data.dataset_kind import dataset_kind
    assert dataset_kind({'source_file':'中哲_范围核对验收_非行情.xlsx', 'sha256':'a'*64}) == 'business'
    assert dataset_kind({'source_file':'any.xlsx', 'dataset_kind':'sample'}) == 'sample'
    (tmp_path / 'source-classifications.json').write_text(json.dumps({'b'*64:{'dataset_kind':'sample'}}))
    assert dataset_kind({'source_file':'renamed.xlsx','sha256':'b'*64},tmp_path) == 'sample'
    assert dataset_kind({'source_file':'renamed.xlsx','sha256':'c'*64},tmp_path) == 'business'
