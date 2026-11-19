from copy import deepcopy
from datetime import datetime, timedelta
import hashlib

import duckdb
import pytest
from fastapi import HTTPException
from openpyxl import Workbook

from structured_data import header_cleanup
from structured_data.research_facts import research_series_id
from structured_data.research_indicator_remaps import read_current
from structured_data.workspaces import Identity, WorkspaceRegistry, current_identity
from test_ai_review import mock_model
from test_review_api import FakeAuthClient, Reply, client, poll


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


@pytest.mark.parametrize('changed', ['tiny_delta', 'missing_date', 'extra_date', 'shifted_date', 'shifted_cell'])
def test_any_observation_difference_excludes_the_corresponding_series(publication, changed):
    store, registry, source, _, new = publication
    fields = store.list_series(new)['series']
    east = next(row['series_id'] for row in fields if row['dimensions']['类别'] == 'east')
    mutations = {
        'tiny_delta': "UPDATE observations SET value = value + 0.000001 WHERE series_id = ? AND date = '2024-01-01'",
        'missing_date': "DELETE FROM observations WHERE series_id = ? AND date = '2024-01-01'",
        'extra_date': "INSERT INTO observations (series_id,date,value,source_cell,value_status) VALUES (?, '2030-01-01', 99, 'C5000', 'valid')",
        'shifted_date': "UPDATE observations SET date = '2023-12-31' WHERE series_id = ? AND date = '2024-01-01'",
        'shifted_cell': "UPDATE observations SET source_cell = 'C5000' WHERE series_id = ? AND date = '2024-01-01'",
    }
    with duckdb.connect(str(store._path(new) / 'data.duckdb')) as connection:
        connection.execute(mutations[changed], [east])
    assert [item['category'] for item in read_current(registry, {}, [source])['items']] == ['west']


@pytest.mark.parametrize(('date_mode', 'times', 'latest_date'), [
    ('date', [datetime(2024, 1, 1), datetime(2024, 2, 1)], '2024-02-01'),
    ('period', ['上一季', '本季'], None),
    ('none', [None, None], None),
])
def test_header_cleanup_exposes_each_renamed_column_in_research(
        client, tmp_path, monkeypatch, date_mode, times, latest_date):
    original_get = FakeAuthClient.get

    async def scoped_get(self, url, headers):
        if url.endswith('/api/v1/knowledge-bases/library'):
            return Reply({'data': {'id': 'library', 'tenant_id': 10000, 'my_permission': 'editor'}})
        return await original_get(self, url, headers)

    monkeypatch.setattr(FakeAuthClient, 'get', scoped_get)
    client.headers['X-Knowledge-Base-ID'] = 'library'
    registry = WorkspaceRegistry(tmp_path / 'data', 10000)
    marker = current_identity.set(Identity('test-user', '', '', 10000, 'owner', 'library', 10000, True,
                                           workbench_id='workbench', scope_version=2, sources_revision=1))
    try:
        objects = registry.objects()
        store, manager = objects['store'], objects['manager']
        workbook = Workbook()
        sheet = workbook.active
        sheet.title = 'Supply'
        sheet.append(['时间', '到货量', '出货量', '余量', '批次'])
        sheet.append([None, '  部分批次尚待核对。\n  ', '统计范围为本地。', None, '按登记单分组。'])
        for time, value in zip(times, (6, 9)):
            sheet.append([time, value, value, value * 2, str(value) + '组'])
        path = tmp_path / 'supply.xlsx'
        workbook.save(path)
        digest = hashlib.md5(path.read_bytes()).hexdigest()
        original = {'source_knowledge_id': 'original', 'knowledge_base_id': 'library', 'tenant_id': 10000,
                    'file_name': path.name, 'file_hash': digest, 'file_size': path.stat().st_size}
        imported = manager.ingest(path, native_source=original)
        import_id = imported['import_id']
        renamed = {'B': ('到货量 / 部分批次尚待核对。', '到货量'), 'C': ('出货量 / 统计范围为本地。', '出货量'),
                   'E': ('批次 / 按登记单分组。', '批次')}
        columns = [{'column': col, 'metric': old, 'header_cells': [f'{col}1', f'{col}2']}
                   for col, (old, _) in renamed.items()]
        columns[-1]['field_role'] = 'text_dimension'
        columns.append({'column': 'D', 'metric': '余量', 'header_cells': ['D1']})
        block = {'header_rows': [1, 2], 'date_mode': date_mode, 'data_start_row': 3, 'data_end_row': 4,
                 'columns': columns, **({'date_column': 'A'} if date_mode != 'none' else {})}
        mapping = {'sheet': sheet.title, 'fingerprint': manager.sheet(import_id, sheet.title)['fingerprint'],
                   'blocks': [block]}
        old = manager.confirm(import_id, mapping, save_template=False)['dataset_id']
        monkeypatch.setattr(header_cleanup, 'read_model', lambda _: {
            'model': 'fixture', 'base_url': 'https://example.invalid/v1', 'api_key': 'fixture-only',
        })
        calls = []
        note_cells = [f'{column}2' for column in renamed]
        mock_model(monkeypatch, [{
            'block_updates': [{'block_index': 0, 'header_note_cells': note_cells, 'metric_header_rows': [1]}],
            'evidence': [{'block_index': 0, 'field': 'header_note_cells', 'cells': note_cells},
                         {'block_index': 0, 'field': 'metric', 'cells': ['B1', 'C1', 'E1']}],
        }], calls)

        url = f'/api/imports/{import_id}/header-cleanup'
        started = client.post(url, json={})
        proposal = poll(client, started)
        assert proposal['sheets'][0]['status'] == 'rename', proposal['sheets'][0]
        assert len(calls) == 1
        applied = client.post(url + '/apply', json={'job_id': started.json()['id'], 'sheet_names': [sheet.title]})
        assert applied.status_code == 200, applied.text
        assert applied.json()['renamed_fields'] == len(renamed)
        new = manager.get(import_id)['dataset_id']
        assert new != old and manager.sheet(import_id, sheet.title)['status'] == 'confirmed'
        source = {'id': 'binding', 'knowledge_base_id': 'library', 'tenant_id': 10000, 'delegated': False,
                  'knowledge': [{'id': 'original', 'file_type': 'xlsx', 'file_hash': digest}]}
        candidates = {item['column']: item for item in read_current(registry, {}, [source])['items']}
        assert set(candidates) == set(renamed)
        before = {field['metric']: field['series_id'] for field in store.list_series(old)['series']}
        after = {field['metric']: field['series_id'] for field in store.list_series(new)['series']}
        assert before['余量'] == after['余量']
        for column, (old_name, new_name) in renamed.items():
            item = candidates[column]
            assert item['old']['series_id'] == research_series_id('library', 'original', before[old_name])
            assert item['new_series_id'] == research_series_id('library', 'original', after[new_name])
            assert item['old']['dataset_id'] == old and item['new_dataset_id'] == new
            assert item['matching_periods'] == 2 and item['latest_date'] == latest_date
    finally:
        current_identity.reset(marker)
