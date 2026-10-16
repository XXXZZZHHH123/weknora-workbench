"""Native origin identity, independent of duplicate basenames and display labels."""
import hashlib
import json
import shutil

import pytest

from structured_data import global_catalog
from structured_data.imports import ImportManager
from structured_data.workspaces import Identity, current_identity
from test_imports import workbook


def native_import(manager, path, source_id, kb='kb-a', tenant=17):
    imported = manager.ingest(path)
    raw = path.read_bytes()
    manager.bind_native_source(imported['import_id'], {
        'source_knowledge_id':source_id,'knowledge_base_id':kb,'tenant_id':tenant,
        'file_name':path.name,'file_size':len(raw),'file_hash':hashlib.md5(raw).hexdigest()})
    return imported


@pytest.fixture
def origins(tmp_path):
    root = tmp_path/'data'/'knowledge-bases'/'kb-a'
    manager = ImportManager(root)
    paths = []
    for folder, day in [('folder-a',1),('folder-b',2)]:
        path = tmp_path/folder/'same.xlsx'
        path.parent.mkdir()
        workbook(path,day=day)
        paths.append(path)
    marker=current_identity.set(Identity('reader','','',17,'owner','kb-a',17,True))
    try:
        yield manager, paths
    finally:
        current_identity.reset(marker)


def test_native_same_basename_originals_both_remain_current_and_history_independent(origins):
    manager,(a,b)=origins
    first=native_import(manager,a,'original-a')
    second=native_import(manager,b,'original-b')
    listed=manager.store.list_datasets()
    assert {item['dataset_id'] for item in listed if item['is_latest_for_source']} == {first['dataset_id'],second['dataset_id']}
    assert all(item['import_event']['kind']=='first_parse' for item in listed)
    result=global_catalog.list_series(manager.store)
    assert {item['dataset_id'] for item in result['series']} == {first['dataset_id'],second['dataset_id']}


def test_confirmed_versions_share_actual_origin_but_not_other_same_name_file(origins):
    manager,(a,b)=origins
    first=native_import(manager,a,'original-a')
    second=native_import(manager,b,'original-b')
    mapping=manager.sheet(first['import_id'],'复杂表')['mapping']
    confirmed=manager.confirm(first['import_id'],mapping)
    listed={item['dataset_id']:item for item in manager.store.list_datasets()}
    assert not listed[first['dataset_id']]['is_latest_for_source']
    assert listed[confirmed['dataset_id']]['is_latest_for_source']
    assert listed[second['dataset_id']]['is_latest_for_source']
    assert listed[confirmed['dataset_id']]['import_event']['kind']=='expand_scope'
    assert listed[second['dataset_id']]['import_event']['kind']=='first_parse'


def test_same_native_id_content_update_replaces_its_own_previous_snapshot(origins):
    manager,(a,b)=origins
    first=native_import(manager,a,'original-a')
    second=native_import(manager,b,'original-b')
    workbook(a,day=3)
    update=native_import(manager,a,'original-a')
    listed={item['dataset_id']:item for item in manager.store.list_datasets()}
    assert not listed[first['dataset_id']]['is_latest_for_source']
    assert listed[update['dataset_id']]['is_latest_for_source']
    assert listed[second['dataset_id']]['is_latest_for_source']
    assert listed[update['dataset_id']]['import_event']['kind']=='source_update'


def test_shared_identical_snapshot_remains_current_for_other_original_after_one_updates(origins):
    manager,(a,_)=origins
    shared=native_import(manager,a,'original-a')
    native_import(manager,a,'original-b')
    workbook(a,day=4)
    updated=native_import(manager,a,'original-a')
    current={item['dataset_id'] for item in manager.store.list_datasets() if item['is_latest_for_source']}
    assert current=={shared['dataset_id'],updated['dataset_id']}


def test_native_original_rename_does_not_change_its_version_identity(origins):
    manager,(a,b)=origins
    first=native_import(manager,a,'original-a')
    second=native_import(manager,b,'original-b')
    renamed=a.with_name('renamed.xlsx')
    shutil.copyfile(a,renamed)
    native_import(manager,renamed,'original-a')
    workbook(renamed,day=5)
    update=native_import(manager,renamed,'original-a')
    current={item['dataset_id'] for item in manager.store.list_datasets() if item['is_latest_for_source']}
    assert current=={update['dataset_id'],second['dataset_id']}
    assert first['dataset_id'] not in current


def test_review_symlink_is_not_followed_for_origin_grouping(origins):
    manager,(a,b)=origins
    first=native_import(manager,a,'original-a')
    second=native_import(manager,b,'original-b')
    for imported in (first,second):
        path=manager.path(imported['import_id'])/'review.json'
        outside=manager.root.parent.parent/(imported['import_id']+'.json')
        path.replace(outside)
        path.symlink_to(outside)
    current=[item for item in manager.store.list_datasets() if item['is_latest_for_source']]
    assert len(current)==1


@pytest.mark.parametrize('change', ['foreign-kb','foreign-tenant','review-hash','review-id','import-traversal'])
def test_invalid_review_or_foreign_bindings_cannot_define_native_origin(origins,change):
    manager,(a,b)=origins
    first=native_import(manager,a,'original-a')
    second=native_import(manager,b,'original-b')
    for imported in (first,second):
        path=manager.path(imported['import_id'])/'review.json'
        doc=json.loads(path.read_text())
        if change=='foreign-kb': doc['native_source_bindings'][0]['knowledge_base_id']='kb-other'
        elif change=='foreign-tenant': doc['native_source_bindings'][0]['tenant_id']=99
        elif change=='review-hash': doc['sha256']='0'*64
        elif change=='review-id': doc['import_id']='0'*24
        elif change=='import-traversal':
            path=manager.store._path(imported['dataset_id'])/'manifest.json'
            doc=json.loads(path.read_text());doc['import_id']='../../outside'
        path.write_text(json.dumps(doc))
    current=[item for item in manager.store.list_datasets() if item['is_latest_for_source']]
    assert len(current)==1, 'Invalid binding metadata must retain legacy filename grouping, never create a native origin'


def test_legacy_unscoped_basename_version_behavior_is_preserved(tmp_path):
    manager=ImportManager(tmp_path/'legacy')
    source=tmp_path/'same.xlsx';workbook(source)
    first=native_import(manager,source,'original-a')
    workbook(source,day=2)
    latest=native_import(manager,source,'original-b')
    items={item['dataset_id']:item for item in manager.store.list_datasets()}
    assert not items[first['dataset_id']]['is_latest_for_source']
    assert items[latest['dataset_id']]['is_latest_for_source']
    assert items[latest['dataset_id']]['import_event']['kind']=='source_update'
