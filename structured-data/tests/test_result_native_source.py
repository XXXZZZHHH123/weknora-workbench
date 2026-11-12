"""Only an actual saved native binding may turn Excel results into doc refs."""
import hashlib
import json

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
