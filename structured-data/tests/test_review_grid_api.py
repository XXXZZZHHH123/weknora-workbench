"""Original-sheet review contracts; fixtures never access business workbooks."""
import threading
import time

import pytest
from fastapi.testclient import TestClient

from structured_data.server import create_app
from structured_data.store import DataStore
from test_review_api import client, make_workbook, poll


def test_review_workers_and_jobs_stay_in_the_verified_workspace(tmp_path, monkeypatch):
    import httpx

    class Auth:
        def __init__(self, **kwargs):
            pass

        async def __aenter__(self):
            return self

        async def __aexit__(self, *args):
            pass

        async def get(self, url, headers):
            tenant = int(headers['Authorization'].rsplit('-', 1)[1])
            return httpx.Response(200, json={'data': {
                'user': {'id': f'user-{tenant}', 'tenant_id': tenant},
                'memberships': [{'tenant_id': tenant, 'role': 'owner'}],
            }})

    def auth(tenant):
        return {'Authorization': f'Bearer workspace-{tenant}',
                'X-Tenant-ID': str(tenant), 'Origin': 'http://testserver'}

    monkeypatch.setattr(httpx, 'AsyncClient', Auth)
    monkeypatch.setenv('STRUCTURED_TENANT_ID', '10000')
    with TestClient(create_app(tmp_path / 'data'), headers=auth(10001)) as scoped:
        uploaded = poll(scoped, scoped.post('/api/import', files={
            'file': ('workspace.xlsx', make_workbook()),
        }))
        base = '/api/imports/' + uploaded['import_id']
        assert scoped.get(base).status_code == 200, 'The uploader must see its own import'
        assert scoped.get('/api/imports', headers=auth(10000)).json() == {'imports': []}
        assert scoped.get(base, headers=auth(10000)).status_code == 400
        mapping = scoped.get(base + '/sheet', params={'name': 'parallel'}).json()['mapping']
        assert scoped.get(base + '/sheet-view', params={'name': 'parallel'}).status_code == 200
        assert scoped.post(base + '/sample-preview', json={'mapping': mapping}).json()['sample_only']

        response = scoped.post(base + '/preview?background=true', json={'mapping': mapping})
        preview = poll(scoped, response)
        assert preview['observation_count'] == 4
        assert scoped.get('/api/jobs/' + response.json()['id'], headers=auth(10000)).status_code == 404

        confirmed = poll(scoped, scoped.post(base + '/confirm', json={
            'mapping': mapping, 'save_template': False,
        }))
        assert scoped.get('/api/datasets').json()['datasets'][0]['dataset_id'] == confirmed['dataset_id']
        assert scoped.get('/api/datasets', headers=auth(10000)).json() == {'datasets': []}
        assert scoped.post(base + '/rescan').status_code == 200

        invalid = {**mapping, 'blocks': [{**mapping['blocks'][0], 'date_mode': 'year'}]}
        failed = scoped.post(base + '/preview?background=true', json={'mapping': invalid}).json()
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            response = scoped.get('/api/jobs/' + failed['id'])
            assert response.status_code == 200
            if response.json()['status'] != 'processing':
                break
            time.sleep(.01)
        assert response.json()['status'] == 'failed'
        assert scoped.get('/api/jobs/' + failed['id'], headers=auth(10000)).status_code == 404
        assert scoped.get(base).json()['dataset_id'] == confirmed['dataset_id']


@pytest.fixture
def review(client):
    return poll(client, client.post('/api/import', files={'file': ('grid.xlsx', make_workbook())}))


def test_current_sheet_view_does_not_start_full_parser(client, review, monkeypatch):
    def unexpected(*args, **kwargs):
        raise AssertionError('Viewing original cells must not parse or publish a dataset')
    monkeypatch.setattr('structured_data.server.run_review_task', unexpected)
    base = '/api/imports/' + review['import_id']
    response = client.get(base + '/sheet-view', params={'name': 'parallel'})
    assert response.status_code == 200, response.text
    result = response.json()
    cells = {cell['cell']: cell for cell in result['cells']}
    assert cells['B2']['value'] == 20
    assert cells['E2']['value'] == 30
    assert cells['A2']['date_value'] == '2023-01-01'
    assert 'C2' not in cells  # The empty separator retains its original column.
    assert result['total_cols'] == 5
    response = client.get(base + '/sheet-view', params={'name': 'parallel', 'start_row': 2, 'start_col': 4, 'row_count': 1, 'col_count': 2})
    assert {cell['cell'] for cell in response.json()['cells']} == {'D2', 'E2'}
    assert client.get(base).json()['dataset_id'] == review['dataset_id']


def test_view_bounds_and_sheet_identity_are_server_enforced(client, review):
    base = '/api/imports/' + review['import_id'] + '/sheet-view'
    for params in ({'name': 'parallel', 'row_count': 161}, {'name': 'parallel', 'col_count': 65}, {'name': 'parallel', 'start_row': 0}):
        assert client.get(base, params=params).status_code == 422
    assert client.get(base, params={'name': 'missing'}).status_code == 400


def test_summary_does_not_send_all_sheet_samples(client, review):
    base = '/api/imports/' + review['import_id']
    summary = client.get(base, params={'summary_only': 'true'}).json()
    assert all('preview' not in sheet and 'mapping' not in sheet for sheet in summary['sheets'])
    detail = client.get(base + '/sheet', params={'name': 'parallel'}).json()
    assert len(detail['mapping']['blocks']) == 2
    assert 'preview' in detail


def test_small_sample_never_approves_or_publishes(client, review, monkeypatch):
    base = '/api/imports/' + review['import_id']
    mapping = client.get(base + '/sheet', params={'name': 'parallel'}).json()['mapping']
    monkeypatch.setattr('structured_data.server.run_review_task', lambda *a, **kw: pytest.fail('sample called full parser'))
    response = client.post(base + '/sample-preview', json={'mapping': mapping})
    assert response.status_code == 200, response.text
    result = response.json()
    assert result['sample_only'] is True
    assert result['blocks'][0]['sample'][0]['date'] == '2023-01-01'
    assert result['blocks'][0]['sample'][0]['value'] == 20
    assert result['blocks'][1]['sample'][0]['source_cell'] == 'E2'
    assert client.get(base).json()['dataset_id'] == review['dataset_id']


def test_full_check_background_job_keeps_published_version(client, review):
    base = '/api/imports/' + review['import_id']
    mapping = client.get(base + '/sheet', params={'name': 'parallel'}).json()['mapping']
    result = poll(client, client.post(base + '/preview?background=true', json={'mapping': mapping}))
    assert result['series_count'] == 2
    assert result['observation_count'] == 4
    assert client.get(base).json()['dataset_id'] == review['dataset_id']


def test_full_check_and_import_cannot_overlap(client, review, monkeypatch):
    started, release = threading.Event(), threading.Event()
    def slow_check(*args, **kwargs):
        started.set()
        assert release.wait(5)
        return {'series_count': 2, 'observation_count': 4}
    monkeypatch.setattr('structured_data.server.run_review_task', slow_check)
    base = '/api/imports/' + review['import_id']
    mapping = client.get(base + '/sheet', params={'name': 'parallel'}).json()['mapping']
    response = client.post(base + '/preview?background=true', json={'mapping': mapping})
    try:
        assert started.wait(2)
        assert client.post(base + '/preview?background=true', json={'mapping': mapping}).status_code == 409
        assert client.post(base + '/confirm', json={'mapping': mapping}).status_code == 409
    finally:
        release.set()
    poll(client, response)


def test_static_review_assets_public_but_cells_require_login(tmp_path):
    with TestClient(create_app(tmp_path)) as unauthenticated:
        assert unauthenticated.get('/sheet-review.js').status_code == 200
        assert unauthenticated.get('/sheet-review.css').status_code == 200
        response = unauthenticated.get('/api/imports/' + 'a' * 24 + '/sheet-view', params={'name': 'parallel'})
        assert response.status_code == 401


def test_incomplete_snapshot_not_listed(tmp_path):
    store = DataStore(tmp_path)
    work = store.datasets / '.import-interrupted'
    work.mkdir()
    (work / 'manifest.json').write_text('{}')
    assert store.list_datasets() == []
