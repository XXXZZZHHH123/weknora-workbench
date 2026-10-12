import json
import pytest
from structured_data.model_settings import read_model, save_model
from structured_data.workspaces import Identity, current_identity, WorkspaceRegistry


def test_new_space_never_inherits_pilot_provider_secret(tmp_path,monkeypatch):
    shared=tmp_path/'shared.json'
    shared.write_text(json.dumps({'chat':{'base_url':'https://trusted.example/v1','model':'model-a','api_key':'pilot-private-key'}}))
    monkeypatch.setenv('STRUCTURED_MODEL_CONFIG',str(shared))
    tenant=tmp_path/'workspaces'/'10001';tenant.mkdir(parents=True)
    assert read_model(tenant)=={}
    with pytest.raises(ValueError):save_model(tenant,{'base_url':'https://elsewhere.example/v1','model':'model-a'})
    assert not (tenant/'model-settings.json').exists()
    assert read_model(tmp_path)['api_key']=='pilot-private-key'


def test_changed_endpoint_requires_its_own_key(tmp_path,monkeypatch):
    monkeypatch.setenv('STRUCTURED_MODEL_CONFIG',str(tmp_path/'absent'))
    save_model(tmp_path,{'base_url':'https://trusted.example/v1','model':'model-a','api_key':'private-key-a'})
    with pytest.raises(ValueError,match='新密钥'):
        save_model(tmp_path,{'base_url':'https://untrusted.example/v1','model':'model-b','api_key':''})
    assert read_model(tmp_path)['base_url']=='https://trusted.example/v1'
    save_model(tmp_path,{'base_url':'https://trusted.example/v1/','model':'model-b'})
    assert read_model(tmp_path)['api_key']=='private-key-a'
    save_model(tmp_path,{'base_url':'https://other.example/v1','model':'model-c','api_key':'new-private-key'})
    assert read_model(tmp_path)['api_key']=='new-private-key'


def test_workspace_upload_locks_do_not_block_other_spaces(tmp_path):
    registry=WorkspaceRegistry(tmp_path,10000)
    marker=current_identity.set(Identity('a','','',10000,'owner'))
    try:
        a=registry.objects()['import_lock'];assert a.acquire(blocking=False)
        marker_b=current_identity.set(Identity('b','','',10001,'owner'))
        try:
            b=registry.objects()['import_lock'];assert b is not a
            assert b.acquire(blocking=False);b.release()
        finally:current_identity.reset(marker_b)
        assert not registry.objects()['import_lock'].acquire(blocking=False)
        a.release()
    finally:current_identity.reset(marker)
