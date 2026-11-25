"""Only an actual saved native binding may turn Excel results into doc refs."""
import hashlib
import json
from zipfile import ZipFile

import pytest

from structured_data.snapshot_metadata import workbook_last_saved_at
from structured_data.store import DataStore
from structured_data.workspaces import Identity,current_identity


def test_result_source_uses_exact_native_binding_and_original_bytes(tmp_path):
    root=tmp_path/'knowledge-bases'/'kb-one';store=DataStore(root)
    data=b'synthetic spreadsheet original bytes'
    sha=hashlib.sha256(data).hexdigest();md5=hashlib.md5(data,usedforsecurity=False).hexdigest()
    import_id='a'*24;dataset='b'*24
    imported=root/'imports'/import_id;imported.mkdir(parents=True)
    (imported/'stored.xlsx').write_bytes(data)
    manifest={'dataset_id':dataset,'import_id':import_id,'source_file':'uploaded.xlsx','sha256':sha}
    binding={'file_name':'native.xlsx','knowledge_base_id':'kb-one','source_knowledge_id':'doc-one',
             'tenant_id':7,'file_size':len(data),'file_hash':md5}
    review={**manifest,'storage_file':'stored.xlsx','native_source_bindings':[binding]}
    (imported/'review.json').write_text(json.dumps(review))
    path=store.datasets/dataset;path.mkdir()
    (path/'manifest.json').write_text(json.dumps(manifest))
    identity=Identity('owner','owner@example.test','owner',7,'owner','kb-one',7,True)
    token=current_identity.set(identity)
    try:
        assert store.raw_manifest(dataset) == manifest
        assert store.manifest(dataset)['source_file'] == 'native.xlsx'
        assert json.loads((path/'manifest.json').read_text()) == manifest
        result=store.result_source(dataset)
        assert result['file']=='native.xlsx' and result['knowledge_id']=='doc-one'
        assert result['knowledge_base_id']=='kb-one' and result['file_hash']==md5
        # Equal length changed bytes may retain a presentation binding, but
        # never receive a source citation identity.
        (imported/'stored.xlsx').write_bytes(b'x'*len(data))
        assert 'knowledge_id' not in store.result_source(dataset)
        (imported/'stored.xlsx').write_bytes(data)
        review['native_source_bindings'][0]['knowledge_base_id']='other-kb'
        (imported/'review.json').write_text(json.dumps(review))
        assert 'knowledge_id' not in store.result_source(dataset)
    finally:current_identity.reset(token)


@pytest.mark.parametrize(('modified', 'prefix'), [
    (None, ''), ('<d:created>2024-05-06T01:02:03Z</d:created>', ''),
    ('<modified>2024-05-06T01:02:03Z</modified>', ''),
    ('<d:modified>2024-02-31T01:02:03Z</d:modified>', ''),
    ('<d:modified>not a date</d:modified>', ''), ('<d:modified>', ''),
    pytest.param('<d:modified>' + ' ' * 65536 + '</d:modified>', '', id='oversized-core'),
    ('<d:modified>2024-05-06T01:02:03Z</d:modified>', '<?xml version="1.0" encoding="unknown-encoding"?>'),
    ('<d:modified>&stamp;</d:modified>', '<!DOCTYPE coreProperties [<!ENTITY stamp "2024-05-06T01:02:03Z">]>'),
])
def test_optional_workbook_saved_at_requires_a_valid_core_property(tmp_path, modified, prefix):
    source = tmp_path / 'source.xlsx'
    with ZipFile(source, 'w') as archive:
        archive.writestr('xl/workbook.xml', '<workbook/>')
        if modified is not None:
            archive.writestr('docProps/core.xml',
                             prefix + '<coreProperties xmlns="http://schemas.openxmlformats.org/package/2006/metadata/core-properties"'
                             ' xmlns:d="http://purl.org/dc/terms/">' + modified + '</coreProperties>')
    assert workbook_last_saved_at(source, hashlib.sha256(source.read_bytes()).hexdigest()) is None


def test_optional_workbook_saved_at_ignores_a_non_zip_original(tmp_path):
    source = tmp_path / 'source.xlsx'
    source.write_bytes(b'invalid workbook bytes')
    assert workbook_last_saved_at(source, hashlib.sha256(source.read_bytes()).hexdigest()) is None
