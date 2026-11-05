import hashlib
import json

from fastapi.testclient import TestClient

from structured_data.server import create_app
from structured_data.store import DataStore
from test_scoped_service import signed


def test_actual_manifest_is_full_stable_selected_independent_and_pinned_before_execution(tmp_path,monkeypatch):
    key=b'synthetic-manifest-service-key-1234567890'
    path=tmp_path/'manifest-read-key';path.write_bytes(key)
    monkeypatch.setenv('STRUCTURED_SERVICE_TOKEN_FILE',str(path))
    monkeypatch.setenv('STRUCTURED_TENANT_ID','10000')
    app=create_app(tmp_path)
    calls=[]
    monkeypatch.setattr(DataStore,'list_datasets',lambda self: calls.append('executed') or [])
    with TestClient(app) as client:
        def post(operation,arguments,extra=None):
            raw,headers=signed(key,body={'operation':operation,'arguments':arguments})
            return client.post('/api/tools/read',content=raw,headers={**headers,**(extra or {})})
        full=post('describe_tools',{})
        assert full.status_code==200
        first=full.json();sha=first['manifest_sha256']
        assert len(sha)==64 and first['protocol_version']==2
        assert first['build_version'].startswith('source-sha256:')
        assert full.headers['X-Structured-Manifest-SHA256']==sha
        names=[t['name'] for t in first['tools']]
        selected=post('describe_tools',{'operations':names}).json()
        assert selected['manifest_sha256']==sha
        assert all(t['inputSchema'] for t in selected['tools'])
        canonical={k:selected[k] for k in ('protocol_version','build_version','tools')}
        encoded=json.dumps(canonical,sort_keys=True,ensure_ascii=True,separators=(',',':'),allow_nan=False).encode()
        assert hashlib.sha256(encoded).hexdigest()==sha
        refused=post('list_datasets',{}, {'X-Structured-Manifest-SHA256':'0'*64})
        assert refused.status_code==409 and refused.json()['detail']['code']=='manifest_changed'
        assert refused.headers['X-Structured-Manifest-SHA256']==sha and calls==[]
        # Even malformed/stale operation arguments never execute under a bad pin.
        assert post('not-a-tool',{'invented':True},{'X-Structured-Manifest-SHA256':'bad'}).status_code==409
        accepted=post('list_datasets',{}, {'X-Structured-Manifest-SHA256':sha})
        assert accepted.status_code==200 and calls==['executed']
        assert accepted.headers['X-Structured-Manifest-SHA256']==sha
        assert post('list_datasets',{}).status_code==200 # legacy compatibility
        assert client.post('/api/tools/read',json={'operation':'describe_tools','arguments':{}},headers={'Host':'localhost:18082'}).status_code==401
