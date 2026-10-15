"""Historical reviews recover only for authenticated identical native originals."""
import hashlib
from pathlib import Path

import pytest
from openpyxl import Workbook

from structured_data.imports import ImportManager
from structured_data.legacy_import import candidate, recover


def original(path, value=17):
    book=Workbook(); sheet=book.active; sheet.title='Records'
    sheet.append(['Date','Metric']); sheet.append(['2024-01-01',value]); sheet.append(['2024-01-02',value+1]); book.save(path)
    return path


def confirmed(manager, path):
    result=manager.ingest(path)
    sheet=manager.sheet(result['import_id'],'Records')
    return manager.confirm(result['import_id'],sheet['mapping'],True)


@pytest.fixture
def migration(tmp_path):
    path=original(tmp_path/'original.xlsx')
    legacy=ImportManager(tmp_path/'tenant')
    saved=confirmed(legacy,path)
    target=ImportManager(tmp_path/'tenant'/'knowledge-bases'/'library')
    scanned=target.ingest(path)
    source={'source_knowledge_id':'source-a','file_name':path.name,'file_size':path.stat().st_size,
            'file_hash':hashlib.md5(path.read_bytes()).hexdigest(),'tenant_id':10000,'knowledge_base_id':'library'}
    target.bind_native_source(scanned['import_id'],source)
    return path, legacy, target, source, saved


def test_recovery_preserves_original_mapping_values_and_history_without_reparse(migration,monkeypatch):
    path,legacy,target,source,saved=migration
    before=(legacy.directory/saved['import_id']/'review.json').read_bytes()
    def no_parser(*args,**kwargs): raise AssertionError('Recovery must never reparse the workbook')
    monkeypatch.setattr('structured_data.scanner.scan_workbook',no_parser)
    result=recover(target,legacy,source,path)
    assert result['recovered_legacy'] is True
    doc=target.get(result['import_id']);old=legacy.get(saved['import_id'])
    assert doc['dataset_id']==old['dataset_id']
    assert doc['sheets']==old['sheets']
    assert doc['native_source_bindings']==[source]
    assert before==(legacy.directory/saved['import_id']/'review.json').read_bytes()
    for item in legacy.store.list_datasets():
        for name in ['series.json','data.duckdb','issues.json','source.xlsx']:
            assert (target.store._path(item['dataset_id'])/name).read_bytes()==(legacy.store._path(item['dataset_id'])/name).read_bytes()
    assert target.store.list_datasets()[0]['dataset_id']==saved['dataset_id']
    assert list((target.directory/saved['import_id']/'recovery-history').glob('*.json'))
    assert recover(target,legacy,source,path) is None


def test_existing_human_review_is_never_overwritten(migration):
    path,legacy,target,source,_=migration
    current=target.find_native_source(source)
    sheet=target.sheet(current['import_id'],'Records')
    sheet['mapping']['blocks'][0]['columns'][0]['metric']='New approved meaning'
    target.confirm(current['import_id'],sheet['mapping'],True)
    before=target.get(current['import_id'])
    assert recover(target,legacy,source,path) is None
    assert target.get(current['import_id'])==before


def test_verified_download_must_match_complete_legacy_sha256(migration,tmp_path):
    _,legacy,target,source,_=migration
    altered=original(tmp_path/'changed.xlsx',99)
    before=target.list()
    with pytest.raises(ValueError,match='不一致'):
        recover(target,legacy,source,altered)
    assert target.list()==before


def test_same_name_and_size_is_not_identity(migration):
    _,legacy,target,source,_=migration
    assert candidate(legacy,{**source,'file_hash':'0'*32}) is None


def test_incomplete_legacy_snapshot_never_replaces_current_review(migration):
    path,legacy,target,source,saved=migration
    (legacy.store._path(saved['dataset_id'])/'series.json').unlink()
    before=target.get(saved['import_id'])
    with pytest.raises(ValueError,match='不完整'):
        recover(target,legacy,source,path)
    assert target.get(saved['import_id'])==before


def test_existing_scoped_original_can_be_used_for_explicit_recovery(migration):
    _, legacy, target, source, _ = migration
    current = target.find_native_source(source)
    path = target.source_path(target.get(current['import_id']))
    before = path.read_bytes()
    assert recover(target, legacy, source, path)['recovered_legacy'] is True
    assert path.read_bytes() == before


def test_reuploaded_identical_native_source_never_overwrites_scoped_human_edits(migration):
    path, legacy, target, source, _ = migration
    current = target.find_native_source(source)
    mapping = target.sheet(current['import_id'], 'Records')['mapping']
    mapping['blocks'][0]['columns'][0]['metric'] = 'New review'
    target.confirm(current['import_id'], mapping, True)
    before = target.get(current['import_id'])
    assert recover(target, legacy, {**source, 'source_knowledge_id': 'reuploaded'}, path) is None
    assert target.get(current['import_id']) == before
