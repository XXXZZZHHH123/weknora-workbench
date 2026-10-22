"""Historical reviews recover only for authenticated identical native originals."""
import hashlib
from pathlib import Path
import shutil

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


def test_recovery_stages_everything_before_guard_and_denial_publishes_nothing(migration):
    path, legacy, target, source, saved = migration
    before_review = target.get(saved['import_id'])
    before_datasets = {item['dataset_id'] for item in target.store.list_datasets(include_samples=True)}
    observed = []

    def revoked():
        staging = list(target.store.datasets.glob('.legacy-recover-*'))
        assert len(staging) == 1
        # The restored, human-confirmed snapshot is fully staged but has not
        # appeared in the target catalog when authorization is rechecked.
        assert any((entry / 'manifest.json').is_file() for entry in staging[0].iterdir()
                   if entry.is_dir())
        assert {item['dataset_id'] for item in target.store.list_datasets(include_samples=True)} == before_datasets
        observed.append('guard')
        raise ValueError('编辑权限已撤销，未恢复。')

    with pytest.raises(ValueError, match='权限已撤销'):
        recover(target, legacy, source, path, before_commit=revoked)
    assert observed == ['guard']
    assert target.get(saved['import_id']) == before_review
    assert {item['dataset_id'] for item in target.store.list_datasets(include_samples=True)} == before_datasets
    assert not list(target.store.datasets.glob('.legacy-recover-*'))


def test_recovery_rechecks_before_review_and_rolls_back_created_snapshots(migration):
    path, legacy, target, source, saved = migration
    before_review = (target.directory / saved['import_id'] / 'review.json').read_bytes()
    before_datasets = {item['dataset_id'] for item in target.store.list_datasets(include_samples=True)}
    calls = []

    def revoke_after_snapshot():
        calls.append('checked')
        if len(calls) == 2:
            raise ValueError('编辑权限已撤销，未恢复。')

    with pytest.raises(ValueError, match='权限已撤销'):
        recover(target, legacy, source, path, before_commit=revoke_after_snapshot)
    # One check guards the new snapshot rename and a fresh check guards the
    # final review/source publication. The first rename is rolled back.
    assert calls == ['checked', 'checked']
    assert (target.directory / saved['import_id'] / 'review.json').read_bytes() == before_review
    assert {item['dataset_id'] for item in target.store.list_datasets(include_samples=True)} == before_datasets


def test_recovery_with_existing_snapshots_still_guards_final_review_write(migration):
    path, legacy, target, source, saved = migration
    for item in legacy.store.list_datasets(include_samples=True):
        destination = target.store.datasets / item['dataset_id']
        if not destination.exists():
            shutil.copytree(legacy.store._path(item['dataset_id']), destination)
    before_review = (target.directory / saved['import_id'] / 'review.json').read_bytes()
    calls = []

    def revoked():
        calls.append('review-boundary')
        raise ValueError('编辑权限已撤销，未恢复。')

    with pytest.raises(ValueError, match='权限已撤销'):
        recover(target, legacy, source, path, before_commit=revoked)
    assert calls == ['review-boundary']
    assert (target.directory / saved['import_id'] / 'review.json').read_bytes() == before_review


def test_recovery_preserves_target_bindings_and_non_recovery_metadata(migration):
    path, legacy, target, source, saved = migration
    current = target.get(saved['import_id'])
    other = {**source, 'source_knowledge_id': 'source-b', 'file_name': 'copy.xlsx'}
    target.bind_native_source(saved['import_id'], other)
    current = target.get(saved['import_id'])
    current['target_only_metadata'] = {'kept': True}
    target._save(current)

    recover(target, legacy, source, path)
    restored = target.get(saved['import_id'])
    assert restored['native_source_bindings'] == [source, other]
    assert restored['target_only_metadata'] == {'kept': True}
    assert restored['sheets'] == legacy.get(saved['import_id'])['sheets']
