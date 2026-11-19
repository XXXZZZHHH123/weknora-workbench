from copy import deepcopy
from datetime import datetime, timedelta
import hashlib

import duckdb
import pytest
from fastapi import HTTPException
from openpyxl import Workbook

from structured_data.research_facts import research_series_id
from structured_data.research_indicator_remaps import read_current
from structured_data.workspaces import Identity, WorkspaceRegistry, current_identity


@pytest.fixture
def publication(tmp_path):
    registry = WorkspaceRegistry(tmp_path / 'store', 9)
    identity = Identity('owner', '', '', 9, 'owner', 'library', 9, True,
                        workbench_id='workbench', scope_version=2, sources_revision=1)
    marker = current_identity.set(identity)
    try:
        objects = registry.objects()
        store, manager = objects['store'], objects['manager']
        workbook = Workbook()
        sheet = workbook.active
        sheet.title = 'Balances'
        sheet.append(['Date', None, '  Statement\nwith its original spacing.  '])
        sheet.append([None, 'Group', 'Quantity'])
        for index in range(505):
            for category in ('east', 'west'):
                sheet.append([datetime(2024, 1, 1) + timedelta(days=index), category, index + 1])
        path = tmp_path / 'synthetic.xlsx'
        workbook.save(path)
        digest = hashlib.md5(path.read_bytes()).hexdigest()
        origin = {'source_knowledge_id': 'original', 'knowledge_base_id': 'library', 'tenant_id': 9,
                  'file_name': path.name, 'file_hash': digest, 'file_size': path.stat().st_size}
        imported = manager.ingest(path, native_source=origin)
        mapping = {'sheet': sheet.title, 'fingerprint': manager.get(imported['import_id'])['sheets'][0]['fingerprint'],
                   'blocks': [{'header_rows': [1, 2], 'date_mode': 'date', 'date_column': 'A',
                               'category_column': 'B', 'data_start_row': 3, 'columns': [
                                   {'column': 'C', 'metric': 'Statement / Quantity', 'header_cells': ['C1', 'C2']}]}]}
        old = manager.confirm(imported['import_id'], mapping, save_template=False)['dataset_id']
        updated = deepcopy(mapping)
        updated['blocks'][0]['header_note_cells'] = ['C1']
        updated['blocks'][0]['columns'][0].update(metric='Quantity', header_cells=['C2'])
        new = manager.confirm(imported['import_id'], updated, save_template=False)['dataset_id']
        source = {'id': 'binding', 'knowledge_base_id': 'library', 'tenant_id': 9, 'delegated': False,
                  'knowledge': [{'id': 'original', 'file_type': 'xlsx', 'file_hash': digest}]}
        yield store, registry, source, old, new
    finally:
        current_identity.reset(marker)


def test_exact_pairs_use_real_positions_and_current_original(publication):
    store, registry, source, old, new = publication
    result = read_current(registry, {}, [source])['items']
    assert {item['category'] for item in result} == {'east', 'west'}
    expected = {research_series_id('library', 'original', row['series_id'])
                for row in store.list_series(old)['series']}
    assert {item['old']['series_id'] for item in result} == expected
    for item in result:
        assert item['sheet'] == 'Balances' and item['block_index'] == 0 and item['column'] == 'C'
        assert item['old']['dataset_id'] == old and item['new_dataset_id'] == new
        assert item['matching_periods'] == 505
    assert read_current(registry, {}, [{**source, 'knowledge': []}]) == {'items': []}
    with pytest.raises(HTTPException) as error:
        read_current(registry, {}, [{**source, 'delegated': True}])
    assert error.value.status_code == 403


@pytest.mark.parametrize('changed', ['tiny_delta', 'missing_date', 'extra_date', 'shifted_date'])
def test_any_observation_difference_excludes_the_corresponding_series(publication, changed):
    store, registry, source, _, new = publication
    fields = store.list_series(new)['series']
    east = next(row['series_id'] for row in fields if row['dimensions']['类别'] == 'east')
    mutations = {
        'tiny_delta': "UPDATE observations SET value = value + 0.000001 WHERE series_id = ? AND date = '2024-01-01'",
        'missing_date': "DELETE FROM observations WHERE series_id = ? AND date = '2024-01-01'",
        'extra_date': "INSERT INTO observations (series_id,date,value,source_cell,value_status) VALUES (?, '2030-01-01', 99, 'C5000', 'valid')",
        'shifted_date': "UPDATE observations SET date = '2023-12-31' WHERE series_id = ? AND date = '2024-01-01'",
    }
    with duckdb.connect(str(store._path(new) / 'data.duckdb')) as connection:
        connection.execute(mutations[changed], [east])
    assert [item['category'] for item in read_current(registry, {}, [source])['items']] == ['west']
