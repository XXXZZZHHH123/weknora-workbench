import hashlib
from datetime import date
import re
from zipfile import ZipFile

from openpyxl import Workbook
import pytest

from structured_data.imports import ImportManager
from structured_data.research_catalog import current_index, page
from structured_data.research_facts import research_series_id
from structured_data.table_analysis import analyze_table
from structured_data.workspaces import Identity, current_identity


ORIGINAL_SAVED_AT = '2024-10-01T13:14:15Z'
UPDATED_SAVED_AT = '2025-03-04T16:17:18'
SAVED_AT_FIELD = 'workbook_declared_last_saved_at'


def write_workbook(path, *, append=False, change=False):
    workbook = Workbook()
    workbook.remove(workbook.active)
    names = ['Daily', 'Revised'] + (['Added'] if change else ['Removed'])
    for index, name in enumerate(names):
        sheet = workbook.create_sheet(name)
        sheet.append(['date', 'new measure' if change and name == 'Revised' else 'measure'])
        sheet.append([date(2025, 3, 1), 10 + index])
        sheet.append([date(2025, 3, 2), 20 + index])
        if append:
            sheet.append([date(2025, 3, 3), 30 + index])
    workbook.save(path)
    # openpyxl sets dcterms:modified during save, so set the fixture's declaration in the written package.
    stamped = path.with_suffix('.stamped.xlsx')
    stamp = (UPDATED_SAVED_AT if append else ORIGINAL_SAVED_AT).encode()
    with ZipFile(path) as archive, ZipFile(stamped, 'w') as replacement:
        for part in archive.infolist():
            payload = archive.read(part)
            if part.filename == 'docProps/core.xml':
                payload = re.sub(rb'(<dcterms:modified\b[^>]*>)[^<]*(</dcterms:modified>)',
                                 lambda match: match[1] + stamp + match[2], payload)
            replacement.writestr(part, payload)
    stamped.replace(path)
    return {'source_knowledge_id': 'original', 'knowledge_base_id': 'kb-a', 'tenant_id': 9,
            'file_name': path.name, 'file_size': path.stat().st_size,
            'file_hash': hashlib.md5(path.read_bytes()).hexdigest()}


@pytest.fixture
def versioned(tmp_path, monkeypatch):
    monkeypatch.setenv('STRUCTURED_MODEL_CONFIG', str(tmp_path / 'no-model.json'))
    marker = current_identity.set(Identity('reader', '', '', 9, 'owner', 'kb-a', 9, True))
    try:
        manager = ImportManager(tmp_path / 'knowledge-bases/kb-a')
        path = tmp_path / 'source.xlsx'
        original = write_workbook(path)
        first = manager.ingest(path, native_source=original, use_reviewed_mappings=False)
        for sheet in ('Daily', 'Revised', 'Removed'):
            first = manager.confirm(first['import_id'], manager.sheet(first['import_id'], sheet)['mapping'])
        yield manager, path, original, first
    finally:
        current_identity.reset(marker)


def research_catalog(manager, native, **extra):
    source = {'knowledge_id': native['source_knowledge_id'], 'file_hash': native['file_hash'], **extra}
    with current_index(manager.store, manager, [source]) as (connection, metadata):
        fields, _, _ = page(connection)
    return {field['sheet']: field for field in fields}, metadata


def test_source_update_autopublishes_confirmed_templates_and_preserves_ids(versioned):
    manager, path, original, first = versioned
    fields, baseline = research_catalog(manager, original)
    updated = write_workbook(path, append=True)
    result = manager.ingest(path, native_source=updated)
    assert result['dataset_id'] != first['dataset_id']
    assert result['summary'] == {'template_ready': 3}
    assert result['source_update']['summary'] == '原文件更新：3 张表新增 3 条观测；0 张表待核对'
    assert result['source_update']['previous_latest_date'] == '2025-03-02'
    assert result['source_update']['latest_date'] == '2025-03-03'
    assert manager.ingest(path, native_source=updated)['dataset_id'] == result['dataset_id']
    history = {item['dataset_id']: item for item in manager.store.list_datasets()}
    assert history[result['dataset_id']]['import_event'] == result['source_update']
    assert not history[first['dataset_id']]['is_latest_for_source']
    changed, refreshed = research_catalog(manager, updated)
    assert baseline['version'] != refreshed['version']
    assert set(changed) == set(fields)
    for name in changed:
        old, current = fields[name], changed[name]
        assert current['series_id'] == old['series_id'] == research_series_id('kb-a', 'original', old['source_series_id'])
        assert current['last_valid_date'] == '2025-03-03' and current['valid_count'] == 3
    dataset = result['dataset_id']
    series_id = changed['Daily']['source_series_id']
    query = manager.store.query_series(dataset, series_id, order='asc')
    assert [row['value'] for row in query['rows']] == [10, 20, 30]
    assert query['source'][SAVED_AT_FIELD] == UPDATED_SAVED_AT
    assert query['actual_last_date'] == '2025-03-03'
    sql = analyze_table(manager.store, dataset, [series_id], 'SELECT MAX(date), SUM(value) FROM data')
    assert sql['rows'] == [['2025-03-03', 60]]
    assert sql['source'][SAVED_AT_FIELD] == UPDATED_SAVED_AT
    assert sql['source']['sha256'] == query['source']['sha256']


def test_changed_deleted_and_new_sheets_are_pending_with_last_published_fields(versioned):
    manager, path, original, first = versioned
    fields, _ = research_catalog(manager, original)
    updated = write_workbook(path, append=True, change=True)
    result = manager.ingest(path, native_source=updated)
    assert result['source_update']['summary'] == '原文件更新：1 张表新增 1 条观测；3 张表待核对'
    reviewed = manager.publication_view(manager.get(result['import_id']))
    sheets = {item['name']: item for item in reviewed['sheets']}
    assert sheets['Revised']['reasons'] == ['表头已变化，沿用上一版数据，待核对']
    assert sheets['Removed']['source_update_state'] == 'retained_deleted'
    assert not sheets['Added']['published_in_dataset']
    changed, _ = research_catalog(manager, updated)
    assert set(changed) == set(fields)
    for name in ['Revised', 'Removed']:
        assert sheets[name]['status'] == 'needs_review'
        assert sheets[name]['published_source_dataset_id'] == first['dataset_id']
        assert changed[name]['series_id'] == fields[name]['series_id']
        assert changed[name]['last_valid_date'] == '2025-03-02'
        assert changed[name]['source_sha256'] == fields[name]['source_sha256']
        query = manager.store.query_series(result['dataset_id'], changed[name]['source_series_id'], operation='latest')
        assert query['source']['source_dataset_id'] == first['dataset_id']
        assert query['source']['file_hash'] == original['file_hash']
        assert query['source'][SAVED_AT_FIELD] == ORIGINAL_SAVED_AT
        assert query['rows'][0]['date'] == '2025-03-02'
    dataset = result['dataset_id']
    for names, expected in [(('Revised',), 32), (('Daily', 'Revised'), 92)]:
        sql = analyze_table(manager.store, dataset, [changed[name]['source_series_id'] for name in names],
                            'SELECT SUM(value) FROM data')
        assert sql['rows'] == [[expected]]
        assert SAVED_AT_FIELD not in sql['source']
        assert sql['source']['file_hash'] == updated['file_hash']
        assert sql['source']['sha256'] == sql['result_contract']['source_snapshot']['source_sha256']


@pytest.mark.parametrize('remove', [True, False])
def test_queries_keep_values_when_the_snapshot_original_cannot_be_verified(versioned, remove):
    manager, path, original, first = versioned
    dataset = first['dataset_id']
    fields, _ = research_catalog(manager, original)
    series_id = fields['Daily']['source_series_id']
    source = manager.store.datasets / dataset / 'source.xlsx'
    assert manager.store.query_series(dataset, series_id)['source'][SAVED_AT_FIELD] == ORIGINAL_SAVED_AT
    if remove:
        source.unlink()
    else:
        write_workbook(path, append=True)
        source.write_bytes(path.read_bytes())
    query = manager.store.query_series(dataset, series_id, operation='sum')
    assert query['aggregate'] == 30
    assert SAVED_AT_FIELD not in query['source']
    sql = analyze_table(manager.store, dataset, [series_id], 'SELECT SUM(value) FROM data')
    assert sql['rows'] == [[30]]
    assert SAVED_AT_FIELD not in sql['source']


def test_publication_failure_keeps_verified_previous_research_snapshot(versioned, monkeypatch):
    manager, path, original, first = versioned
    fields, _ = research_catalog(manager, original)
    updated = write_workbook(path, append=True)

    def reject_publication():
        raise ValueError('publication rejected')

    with pytest.raises(ValueError, match='publication rejected'):
        manager.ingest(path, native_source=updated, before_publish=reject_publication)
    assert not manager.find_native_source(updated, use_reviewed_mappings=None)
    assert research_catalog(manager, updated)[0] == {}
    restored, metadata = research_catalog(manager, updated, previous_file_hashes=[original['file_hash']])
    assert restored == fields
    assert metadata['datasets'][0]['source_version_pending']
    assert metadata['datasets'][0]['dataset_id'] == first['dataset_id']
    assert research_catalog(manager, {**updated, 'source_knowledge_id': 'unrelated'},
                            previous_file_hashes=[original['file_hash']])[0] == {}
    result = manager.ingest(path, native_source=updated)
    assert result['dataset_id'] != first['dataset_id']


def test_review_write_failure_does_not_promote_an_unbound_snapshot(versioned, monkeypatch):
    manager, path, _, first = versioned
    updated = write_workbook(path, append=True)

    def reject_review_write(doc):
        raise OSError('review write failed')

    with monkeypatch.context() as patch:
        patch.setattr(manager, '_save', reject_review_write)
        with pytest.raises(OSError, match='review write failed'):
            manager.ingest(path, native_source=updated)

    assert [item['dataset_id'] for item in manager.store.list_datasets() if item['is_latest_for_source']] == [first['dataset_id']]
    assert manager.ingest(path, native_source=updated)['dataset_id'] != first['dataset_id']
