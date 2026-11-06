"""PPT API against real private result stores; external projections are mocked."""
import hashlib
from io import BytesIO
import json
import stat
from types import SimpleNamespace
from zipfile import ZipFile

from fastapi import HTTPException
import pytest
from sqlalchemy import select

from tin.integration.workbench_scope import isolated_database_path, isolated_session_factory
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision
from tin.ppt import package as package_module
from tin.ppt.package import MAX_UPLOAD
from tin.web import ppt
from tests.chart_fixtures import OTHER, SID, definition
from tests.ppt_fixtures import PNG, archive_bytes, make_pptx, ppt_entries
from tests.test_chart_api import SECOND_WB, chart_client, create
from tests.test_workbench_sources_v2 import WB

BASE = '/api/sn/ppt-templates'


@pytest.fixture
def ppt_client(chart_client):
    client, state = chart_client
    state.app.include_router(ppt.router)
    return client, state


def upload(client, *, data=None, headers=None):
    response = client.post(BASE, files={'file': ('synthetic.pptx', make_pptx() if data is None else data,
                                               'application/vnd.openxmlformats-officedocument.presentationml.presentation')},
                           headers=headers or {})
    assert response.status_code == 201, response.text
    return response.json()


def patch(client, saved, bindings):
    response = client.patch(BASE + '/' + saved['id'] + '/bindings',
        json={'revision': saved['revision'], 'slots': [{'slot_id': key, 'binding': value} for key, value in bindings.items()]})
    assert response.status_code == 200, response.text
    return response.json()


def export(client, identifier, *, images=None, **data):
    data.setdefault('date', '2023-01-05')
    response = client.post(BASE + '/' + identifier + '/export', data=data,
        files={key: ('render.png', value, 'image/png') for key, value in (images or {}).items()})
    assert response.status_code == 200, response.text
    return response.json()


@pytest.mark.parametrize('export_count', [0, 2])
def test_template_summary_keeps_latest_export_metadata_bounded(export_count, monkeypatch):
    reports = [{'export_id': 'export-' + str(index), 'created_at': '2030-01-02T08:00:00Z',
                'download_url': '/api/sn/ppt-templates/template-1/exports/export-' + str(index),
                'updated_slots': [{'slot_id': 'slide-1-pic-1'}], 'kept_slots': [],
                'headlines_to_check': [{'title': '合成标题'}]}
               for index in range(export_count)]
    row = SimpleNamespace(id='template-1', title='合成模板', status='draft', revision=1,
        created_at='2030-01-02T08:00:00Z', updated_at='2030-01-02T08:00:00Z',
        body=json.dumps({'format': 'ppt-template.v1', 'filename': 'synthetic.pptx', 'sha256': 'a' * 64,
                         'presentation': ppt.parse_pptx(make_pptx()), 'bindings': {}, 'exports': reports}))

    def unexpected_read(*args, **kwargs):
        pytest.fail('Serializing recent export metadata must not read sources')

    for name in ('chart_evidence', 'template_candidates', 'catalog_fields', 'read_series'):
        monkeypatch.setattr(ppt, name, unexpected_read)
    item = ppt.serialize(row, detail=False)
    assert item['last_export'] == (None if not reports else {
        'created_at': '2030-01-02T08:00:00Z', 'export_id': 'export-1',
        'download_url': '/api/sn/ppt-templates/template-1/exports/export-1'})
    assert not {'bindings', 'presentation', 'candidates', 'exports'} & item.keys()
    detail = ppt.serialize(row)
    assert detail['last_export'] == item['last_export'] and detail['exports'] == reports


def test_uploaded_templates_are_personal_private_hashed_and_soft_deleted(ppt_client):
    client, state = ppt_client
    source = make_pptx()
    saved = upload(client, data=source)
    root = isolated_database_path(17, WB).parent / 'ppt'
    digest = hashlib.sha256(source).hexdigest()
    original = root / (digest + '.pptx')
    assert saved['sha256'] == digest and original.read_bytes() == source
    assert stat.S_IMODE(original.stat().st_mode) == 0o600
    assert stat.S_IMODE(root.stat().st_mode) == 0o700
    assert saved['slide_count'] == 2 and saved['slot_count'] == 9 and saved['revision'] == 1
    assert saved['bindings'] == {} and saved['exports'] == []
    again = upload(client, data=source)
    assert again['id'] != saved['id'] and len(list(root.glob('*.pptx'))) == 1
    assert client.get(BASE).json()['total'] == 2
    own_viewer = {'x-role': 'viewer', 'x-can-edit': 'false'}
    assert client.get(BASE + '/' + saved['id'], headers=own_viewer).status_code == 200
    assert client.get(BASE, headers={'x-actor': 'second'}).json()['total'] == 0
    assert client.get(BASE + '/' + saved['id'], headers={'x-actor': 'second'}).status_code == 404
    assert client.get(BASE + '/' + saved['id'], headers={'x-workbench': SECOND_WB}).status_code == 404
    assert client.delete(BASE + '/' + saved['id'] + '?revision=9').status_code == 409
    deleted = client.delete(BASE + '/' + saved['id']).json()
    assert deleted['status'] == 'deleted' and deleted['revision'] == 2
    assert client.get(BASE + '/' + saved['id']).status_code == 404
    assert client.get(BASE).json()['total'] == 1
    assert original.read_bytes() == source
    with isolated_session_factory(17, WB)() as session:
        row = session.get(WorkbenchArtifact, saved['id'])
        assert row.module == 'ppt_templates' and row.actor == 'first' and row.source_ids == []
        revisions = session.scalars(select(WorkbenchArtifactRevision).where(
            WorkbenchArtifactRevision.artifact_id == row.id)).all()
        assert [value.revision for value in revisions] == [1, 2]
    assert state.requests == []


def test_personal_templates_in_shared_legacy_database_are_still_workbench_scoped(ppt_client):
    client, _ = ppt_client
    headers = {'x-workspace-mode': 'legacy'}
    saved = upload(client, headers=headers)
    assert client.get(BASE + '/' + saved['id'], headers=headers).status_code == 200
    assert client.get(BASE, headers={**headers, 'x-workbench': SECOND_WB}).json()['total'] == 0
    assert client.get(BASE + '/' + saved['id'], headers={**headers, 'x-workbench': SECOND_WB}).status_code == 404
    from tin.db import tenant_database_path
    assert (tenant_database_path(17).parent / 'ppt' / (saved['sha256'] + '.pptx')).exists()


def test_list_includes_readonly_slot_counts_without_candidates_or_source_reads(ppt_client, monkeypatch):
    client, state = ppt_client
    saved = upload(client)
    assert saved['slot_counts'] == {'bound': 0, 'kept': 3, 'pending': 6}
    chart = create(client)
    saved = patch(client, saved, {
        'slide-1-pic-1': {'kind': 'chart', 'chart_id': chart['id']},
        'slide-1-pic-2': {'kind': 'series', 'series_id': SID, 'rows': 5},
        'slide-1-pic-3': {'kind': 'keep'},
        'slide-1-pic-4': {'kind': 'keep'},  # Unsupported slots count as kept only once.
        'slide-1-pic-6': None})
    expected = {'bound': 2, 'kept': 4, 'pending': 3}
    assert saved['slot_counts'] == expected
    state.requests.clear()

    def unexpected_read(*args, **kwargs):
        pytest.fail('Template list must not calculate candidates or read chart/source data')

    for name in ('chart_evidence', 'template_candidates', 'catalog_fields', 'read_series'):
        monkeypatch.setattr(ppt, name, unexpected_read)
    for headers in ({}, {'x-role': 'viewer', 'x-can-edit': 'false'}):
        response = client.get(BASE + '?limit=1', headers=headers)
        assert response.status_code == 200, response.text
        result = response.json()
        assert result['total'] == 1 and result['next_offset'] is None
        item = result['items'][0]
        assert item['id'] == saved['id'] and item['revision'] == saved['revision']
        assert item['updated_at'] == saved['updated_at']
        assert item['slot_counts'] == expected and sum(item['slot_counts'].values()) == item['slot_count']
        assert item['last_export'] is None
        assert not {'bindings', 'presentation', 'candidates', 'exports'} & item.keys()
    assert client.get(BASE, headers={'x-actor': 'second'}).json()['items'] == []
    with isolated_session_factory(17, WB)() as session:
        row = session.get(WorkbenchArtifact, saved['id'])
        assert row.revision == saved['revision']
        assert json.loads(row.body)['bindings'] == saved['bindings']
    assert state.requests == []


def test_list_exposes_only_latest_personal_export_summary_without_reading_sources(ppt_client, monkeypatch):
    client, state = ppt_client
    saved = upload(client)
    export(client, saved['id'])
    latest = export(client, saved['id'])
    state.requests.clear()

    def unexpected_read(*args, **kwargs):
        pytest.fail('Recent export metadata must not load sources or candidates')

    for name in ('chart_evidence', 'template_candidates', 'catalog_fields', 'read_series'):
        monkeypatch.setattr(ppt, name, unexpected_read)
    for headers in ({}, {'x-role': 'viewer', 'x-can-edit': 'false'}):
        item = client.get(BASE, headers=headers).json()['items'][0]
        assert item['last_export'] == {key: latest[key] for key in ('created_at', 'export_id', 'download_url')}
        assert not {'bindings', 'presentation', 'candidates', 'exports'} & item.keys()
        assert client.get(item['last_export']['download_url'], headers=headers).status_code == 200
    assert client.get(BASE, headers={'x-actor': 'second'}).json()['items'] == []
    assert client.get(BASE, headers={'x-workbench': SECOND_WB}).json()['items'] == []
    assert state.requests == []


@pytest.mark.parametrize('headers,status', [({'x-restricted': 'true'}, 403), ({'x-scope-version': '1'}, 409),
                                          ({'x-local': 'true'}, 409)])
@pytest.mark.parametrize('method,path,body', [
    ('GET', '', None), ('POST', '', None), ('GET', '/absent', None),
    ('GET', '/sources', None),
    ('POST', '/from-source', {'knowledge_base_id': 'fixture-kb', 'knowledge_id': 'fixture-ppt', 'file_hash': 'a' * 32}),
    ('PATCH', '/absent/source', {'revision': 1, 'knowledge_base_id': 'fixture-kb',
                               'knowledge_id': 'fixture-ppt', 'file_hash': 'a' * 32}),
    ('PATCH', '/absent/bindings', {'revision': 1, 'slots': [{'slot_id': 'a', 'binding': None}]}),
    ('DELETE', '/absent', None), ('POST', '/absent/export', None), ('GET', '/absent/exports/absent', None),
])
def test_all_routes_reject_restricted_or_old_scope_before_reading(ppt_client, headers, status, method, path, body):
    client, state = ppt_client
    response = client.request(method, BASE + path, json=body, headers=headers)
    assert response.status_code == status, response.text
    assert state.requests == []


@pytest.mark.parametrize('headers', [{'x-can-edit': 'false'}, {'x-role': 'viewer'},
                                     {'x-role': 'contributor', 'x-can-edit': 'false'}])
def test_every_write_including_export_requires_workbench_edit_permission(ppt_client, headers):
    client, state = ppt_client
    saved = upload(client)
    path = BASE + '/' + saved['id']
    assert client.post(BASE, headers=headers).status_code == 403
    assert client.patch(path + '/bindings', json={'revision': 1, 'slots': [{'slot_id': 'slide-1-pic-1',
        'binding': {'kind': 'keep'}}]}, headers=headers).status_code == 403
    assert client.delete(path, headers=headers).status_code == 403
    assert client.post(path + '/export', headers=headers).status_code == 403
    assert state.requests == []


def test_foreign_templates_cannot_be_written_or_exported(ppt_client):
    client, _ = ppt_client
    saved = upload(client)
    path = BASE + '/' + saved['id']
    headers = {'x-actor': 'second'}
    assert client.patch(path + '/bindings', headers=headers,
        json={'revision': 1, 'slots': [{'slot_id': 'slide-1-pic-1', 'binding': {'kind': 'keep'}}]}).status_code == 404
    assert client.delete(path, headers=headers).status_code == 404
    assert client.post(path + '/export', headers=headers).status_code == 404


@pytest.mark.parametrize('mode,status', [('not_zip', 400), ('missing_presentation', 400), ('path', 400), ('entity', 400),
                                       ('expansion', 413), ('count', 413)])
def test_upload_safety_rejects_bad_packages_without_storing_templates(ppt_client, mode, status):
    client, _ = ppt_client
    entries = ppt_entries()
    if mode == 'missing_presentation':
        del entries['ppt/presentation.xml']
    elif mode == 'path':
        entries['../escape'] = b'bad'
    elif mode == 'entity':
        entries['custom/unused.xml'] = b'<!DOCTYPE x [<!ENTITY e SYSTEM "file:///not-read">]><x>&e;</x>'
    elif mode == 'count':
        entries.update({f'empty/{index}': b'' for index in range(5001)})
    data = archive_bytes(entries)
    if mode == 'expansion':
        import struct
        data = bytearray(data)
        struct.pack_into('<I', data, data.index(b'PK\x01\x02') + 24, 501 * 1024**2)
        data = bytes(data)
    if mode == 'not_zip':
        data = b'fake file'
    response = client.post(BASE, files={'file': ('synthetic.pptx', data)})
    assert response.status_code == status, response.text
    assert client.get(BASE).json()['total'] == 0
    assert not list(isolated_database_path(17, WB).parent.glob('ppt/*.pptx'))


def test_upload_filename_part_count_and_size_limits(ppt_client, monkeypatch):
    client, _ = ppt_client
    assert client.post(BASE, files={'file': ('fake.zip', make_pptx())}).status_code == 400
    assert client.post(BASE, files={'other': ('fake.pptx', make_pptx())}).status_code == 422
    assert client.post(BASE, files=[('file', ('first.pptx', make_pptx())),
                                   ('file', ('second.pptx', make_pptx()))]).status_code == 400
    assert MAX_UPLOAD == 60 * 1024**2
    monkeypatch.setattr(ppt, 'MAX_UPLOAD', len(make_pptx()) - 1)
    assert client.post(BASE, files={'file': ('large.pptx', make_pptx())}).status_code == 413
    monkeypatch.setattr(package_module, 'MAX_UPLOAD', 10)
    assert client.post(BASE, files={'file': ('large.pptx', b'x' * 11)}).status_code == 413


def test_candidates_use_current_charts_round_real_observations_and_do_not_bind(ppt_client):
    client, state = ppt_client
    chart = create(client, title='隔离图表', curves=[{'series_id': SID, 'label': '合成曲线'}])
    state.observations[SID] = [('2023-01-01', 10.004), ('2023-01-02', 11.04), ('2023-01-03', 11.5),
                               ('2023-01-04', 13.4), ('2023-01-05', 999)]
    state.observations[OTHER] = [('2023-01-0' + str(index), 9 + index) for index in range(1, 6)]
    saved = upload(client)
    state.requests.clear()
    result = client.get(BASE + '/' + saved['id']).json()
    assert result['candidates']['slide-1-pic-1'][0]['chart_id'] == chart['id']
    candidates = result['candidates']['slide-1-pic-2']
    assert len(candidates) == 1 and candidates[0]['series_id'] == SID
    assert candidates[0]['matched'] == 4 and candidates[0]['score'] == 0.8 and candidates[0]['label'] == '数值已核实'
    assert result['bindings'] == {} and result['revision'] == 1
    requests = [body for endpoint, body in state.requests if 'points_per_series' in body]
    assert requests and all(body['series_ids'] == [SID] and body['view_module'] == 'indicators' for body in requests)
    assert all(len(body['series_ids']) <= 256 and len(body['series_ids']) * body['points_per_series'] <= 20000 for body in requests)
    state.observations[SID][3] = ('2023-01-04', 999)
    result = client.get(BASE + '/' + saved['id']).json()
    assert result['candidates']['slide-1-pic-2'] == []
    assert result['bindings'] == {}


def test_deleted_foreign_and_uncalled_chart_series_never_become_table_candidates(ppt_client):
    client, state = ppt_client
    chart = create(client, title='隔离图表')
    create(client, title='entirely unrelated', curves=[{'series_id': OTHER, 'label': 'unrelated'}])
    state.observations[OTHER] = [('2023-01-0' + str(index), 9 + index) for index in range(1, 6)]
    assert client.delete('/api/sn/charts/' + chart['id']).status_code == 422  # Needs a chart revision.
    assert client.delete('/api/sn/charts/' + chart['id'] + '?revision=1').status_code == 200
    saved = upload(client)
    state.requests.clear()
    result = client.get(BASE + '/' + saved['id']).json()
    assert result['candidates']['slide-1-pic-1'] == result['candidates']['slide-1-pic-2'] == []
    assert all('points_per_series' not in body for _, body in state.requests)


def test_candidate_reads_batch_many_series_inside_page_limits(ppt_client):
    client, state = ppt_client
    identifiers = ['kb-' + f'{index + 1000:048x}' for index in range(272)]
    for sid in identifiers:
        state.fields[sid] = {'series_id': sid, 'metric': '合成字段', 'unit': '件'}
        state.observations[sid] = [('2023-01-0' + str(index), 9 + index) for index in range(1, 6)]
    # Three top candidates, each with 16 curves, already require several 20,000-point batches.
    for index in range(3):
        create(client, title='隔离图表', curves=[{'series_id': sid, 'label': '合成曲线'}
                                              for sid in identifiers[index * 16:(index + 1) * 16]])
    saved = upload(client)
    state.requests.clear()
    result = client.get(BASE + '/' + saved['id']).json()
    assert len(result['candidates']['slide-1-pic-2']) == 48
    requests = [body for _, body in state.requests if 'points_per_series' in body]
    assert len(requests) >= 5
    assert all(len(body['series_ids']) <= 256 and len(body['series_ids']) * body['points_per_series'] <= 20000 for body in requests)
    assert set(sid for body in requests for sid in body['series_ids']) == set(identifiers[:48])


def test_explicit_binding_validation_clear_keep_revision_and_history(ppt_client):
    client, _ = ppt_client
    chart_response = client.post('/api/sn/charts', json=definition(), headers={'x-actor': 'second'})
    assert chart_response.status_code == 201
    chart = chart_response.json()
    saved = upload(client)
    updated = patch(client, saved, {'slide-1-pic-1': {'kind': 'chart', 'chart_id': chart['id']},
        'slide-1-pic-2': {'kind': 'series', 'series_id': SID, 'rows': 5}, 'slide-1-pic-4': {'kind': 'keep'}})
    assert updated['revision'] == 2
    assert updated['bindings']['slide-1-pic-2']['rows'] == 5
    assert client.patch(BASE + '/' + saved['id'] + '/bindings',
        json={'revision': 1, 'slots': [{'slot_id': 'slide-1-pic-1', 'binding': None}]}).status_code == 409
    cleared = patch(client, updated, {'slide-1-pic-1': None})
    assert cleared['revision'] == 3 and cleared['bindings']['slide-1-pic-1'] is None
    assert cleared['bindings']['slide-1-pic-2'] == updated['bindings']['slide-1-pic-2']
    with isolated_session_factory(17, WB)() as session:
        history = session.scalars(select(WorkbenchArtifactRevision).where(
            WorkbenchArtifactRevision.artifact_id == saved['id']).order_by(WorkbenchArtifactRevision.revision)).all()
        assert [row.revision for row in history] == [1, 2, 3]
        assert json.loads(history[0].body)['bindings'] == {}
        assert json.loads(history[1].body)['bindings']['slide-1-pic-1']['chart_id'] == chart['id']


@pytest.mark.parametrize('slot,binding', [
    ('absent', {'kind': 'keep'}), ('slide-1-pic-1', {'kind': 'chart', 'chart_id': 'absent'}),
    ('slide-1-pic-2', {'kind': 'series', 'series_id': 'kb-' + 'd' * 48, 'rows': 5}),
    ('slide-1-pic-4', {'kind': 'series', 'series_id': SID, 'rows': 5}),
    ('slide-1-pic-5', {'kind': 'chart', 'chart_id': 'absent'}),
    ('slide-1-chart-1', {'kind': 'chart', 'chart_id': 'absent'}),
    *[('slide-1-pic-2', {'kind': 'series', 'series_id': SID, 'rows': rows}) for rows in (0, 61, True, 1.0, '5')],
    ('slide-1-pic-2', {'kind': 'series', 'series_id': 'invented', 'rows': 5}),
    ('slide-1-pic-1', {'kind': 'keep', 'chart_id': 'extra'}),
])
def test_invalid_binding_is_atomic_and_does_not_increment_revision(ppt_client, slot, binding):
    client, _ = ppt_client
    saved = upload(client)
    response = client.patch(BASE + '/' + saved['id'] + '/bindings', json={'revision': 1, 'slots': [
        {'slot_id': 'slide-2-pic-1', 'binding': {'kind': 'keep'}}, {'slot_id': slot, 'binding': binding}]})
    assert response.status_code == 422, response.text
    result = client.get(BASE + '/' + saved['id']).json()
    assert result['revision'] == 1 and result['bindings'] == {}


def test_bindings_reject_duplicate_slots_and_foreign_deleted_chart(ppt_client):
    client, _ = ppt_client
    saved = upload(client)
    path = BASE + '/' + saved['id'] + '/bindings'
    change = {'slot_id': 'slide-1-pic-1', 'binding': None}
    assert client.patch(path, json={'revision': 1, 'slots': [change, change]}).status_code == 422
    foreign = client.post('/api/sn/charts', json=definition(), headers={'x-workbench': SECOND_WB}).json()
    for chart in (foreign, create(client)):
        if chart['id'] != foreign['id']:
            assert client.delete('/api/sn/charts/' + chart['id'] + '?revision=1').status_code == 200
        change['binding'] = {'kind': 'chart', 'chart_id': chart['id']}
        assert client.patch(path, json={'revision': 1, 'slots': [change]}).status_code == 422


@pytest.mark.parametrize('status', ['draft', 'deleted'])
def test_binding_compare_and_swap_rechecks_after_catalog_validation(ppt_client, monkeypatch, status):
    client, _ = ppt_client
    saved = upload(client)
    original = ppt.catalog_fields

    def race(scope, identifiers):
        result = original(scope, identifiers)
        with isolated_session_factory(17, WB)() as session:
            row = session.get(WorkbenchArtifact, saved['id'])
            row.revision = 2
            row.status = status
            row.title = '并发修改'
            session.commit()
        return result

    monkeypatch.setattr(ppt, 'catalog_fields', race)
    response = client.patch(BASE + '/' + saved['id'] + '/bindings', json={'revision': 1,
        'slots': [{'slot_id': 'slide-1-pic-2', 'binding': {'kind': 'series', 'series_id': SID, 'rows': 5}}]})
    assert response.status_code == 409, response.text
    with isolated_session_factory(17, WB)() as session:
        assert session.get(WorkbenchArtifact, saved['id']).title == '并发修改'
        assert session.scalar(select(WorkbenchArtifactRevision).where(
            WorkbenchArtifactRevision.artifact_id == saved['id'], WorkbenchArtifactRevision.revision == 2)) is None


def test_export_reports_updated_kept_dates_and_titles_and_can_download(ppt_client):
    client, _ = ppt_client
    chart = create(client)
    original = make_pptx()
    saved = patch(client, upload(client, data=original), {
        'slide-1-pic-1': {'kind': 'chart', 'chart_id': chart['id']},
        'slide-1-pic-2': {'kind': 'series', 'series_id': SID, 'rows': 5},
        'slide-1-pic-3': {'kind': 'keep'}})
    result = export(client, saved['id'], images={'slide-1-pic-1': PNG, 'slide-1-pic-2': PNG, 'slide-1-pic-3': PNG},
                    mark_headlines='true', meta=json.dumps({'slide-1-pic-1': {'data_as_of': '2023-01-04'},
                                                           'slide-1-pic-2': {'as_of': '2023-01-03'}}))
    assert result['revision'] == 3 and result['date'] == '2023-01-05'
    assert result['updated_slots'] == [{'slot_id': 'slide-1-pic-1', 'slide': 1, 'data_as_of': '2023-01-04'},
                                       {'slot_id': 'slide-1-pic-2', 'slide': 1, 'data_as_of': '2023-01-03'}]
    reasons = {value['slot_id']: value['reason'] for value in result['kept_slots']}
    assert reasons == {'slide-1-pic-3': 'keep', 'slide-1-pic-4': 'svg', 'slide-1-pic-5': 'svg',
                       'slide-1-chart-1': 'native_chart', 'slide-1-pic-6': 'unbound',
                       'slide-2-pic-1': 'unbound', 'slide-2-pic-2': 'unbound'}
    assert result['headlines_to_check'] == [{'slide': 1, 'title': '合成周报 2030 后半段 页码'}]
    response = client.get(result['download_url'])
    assert response.status_code == 200 and response.headers['content-type'].startswith('application/vnd.openxmlformats')
    assert response.headers['content-disposition'].endswith('.pptx"')
    with ZipFile(BytesIO(response.content)) as archive, ZipFile(BytesIO(original)) as before:
        assert archive.testzip() is None
        assert archive.read('ppt/media/shared.emf') == before.read('ppt/media/shared.emf')
        assert archive.read('ppt/slides/slide2.xml') == before.read('ppt/slides/slide2.xml')
    root = isolated_database_path(17, WB).parent / 'ppt'
    assert (root / (saved['sha256'] + '.pptx')).read_bytes() == original
    assert all(stat.S_IMODE(path.stat().st_mode) == 0o600 for path in (root / 'exports').glob('*.pptx'))
    assert client.get(result['download_url'], headers={'x-actor': 'second'}).status_code == 404
    assert client.get(result['download_url'], headers={'x-workbench': SECOND_WB}).status_code == 404


@pytest.mark.parametrize('failure,reason', [('deleted_chart', 'chart_missing'), ('missing_series', 'series_missing'),
                                          ('frontend_error', 'data_read_failed'), ('no_png', 'png_not_supplied'),
                                          ('catalog_unavailable', 'source_unavailable')])
def test_export_preserves_original_on_invalid_binding_or_data_failure(ppt_client, monkeypatch, failure, reason):
    client, state = ppt_client
    chart = create(client)
    original = make_pptx()
    saved = patch(client, upload(client, data=original), {'slide-1-pic-1': {'kind': 'chart', 'chart_id': chart['id']}})
    if failure == 'deleted_chart':
        assert client.delete('/api/sn/charts/' + chart['id'] + '?revision=1').status_code == 200
    elif failure == 'missing_series':
        del state.fields[SID]
    elif failure == 'catalog_unavailable':
        monkeypatch.setattr(ppt, 'catalog_fields', lambda *args: (_ for _ in ()).throw(HTTPException(503, 'unavailable')))
    images = {} if failure == 'no_png' else {'slide-1-pic-1': PNG}
    result = export(client, saved['id'], images=images, meta=json.dumps(
        {'slide-1-pic-1': {'error': '前端数据读取失败'}} if failure == 'frontend_error' else {}))
    assert result['updated_slots'] == []
    assert next(value for value in result['kept_slots'] if value['slot_id'] == 'slide-1-pic-1')['reason'] == reason
    with ZipFile(BytesIO(original)) as before, ZipFile(BytesIO(client.get(result['download_url']).content)) as after:
        assert all(before.read(name) == after.read(name) for name in before.namelist())


def test_export_prunes_to_last_five_and_deletion_hides_downloads(ppt_client):
    client, _ = ppt_client
    saved = upload(client)
    reports = [export(client, saved['id']) for _ in range(7)]
    assert client.get(reports[0]['download_url']).status_code == 404
    assert client.get(reports[1]['download_url']).status_code == 404
    assert all(client.get(report['download_url']).status_code == 200 for report in reports[2:])
    root = isolated_database_path(17, WB).parent / 'ppt'
    assert len(list((root / 'exports').glob('*.pptx'))) == 5
    current = client.get(BASE + '/' + saved['id']).json()
    assert current['revision'] == 8 and current['exports'] == reports[2:]
    assert client.delete(BASE + '/' + saved['id'] + '?revision=8').status_code == 200
    assert client.get(reports[-1]['download_url']).status_code == 404


@pytest.mark.parametrize('data,status', [
    ({'date': 'bad'}, 400), ({'date': '2023-1-1'}, 400), ({'date': '2023-01-05', 'mark_headlines': 'maybe'}, 422),
    ({'date': '2023-01-05', 'meta': '[]'}, 422), ({'date': '2023-01-05', 'meta': '{bad'}, 422),
    ({'date': '2023-01-05', 'meta': '{"slide-1-pic-1":{"data_as_of":"2023-01-06"}}'}, 422),
    ({'date': '2023-01-05', 'meta': '{"slide-1-pic-1":{"data_as_of":123}}'}, 422),
    ({'date': '2023-01-05', 'meta': '{"unknown":{"data_as_of":"2023-01-01"}}'}, 422),
    ({'date': '2023-01-05', 'unknown': 'text'}, 422),
])
def test_export_form_metadata_and_dates_are_validated(ppt_client, data, status):
    client, _ = ppt_client
    saved = upload(client)
    response = client.post(BASE + '/' + saved['id'] + '/export', data=data)
    assert response.status_code == status, response.text
    assert not list(isolated_database_path(17, WB).parent.glob('ppt/exports/*.pptx'))


def test_export_image_signature_size_count_unknown_and_duplicate_validation(ppt_client):
    client, _ = ppt_client
    saved = upload(client)
    path = BASE + '/' + saved['id'] + '/export'
    assert client.post(path, data={'date': '2023-01-05'}, files={'slide-1-pic-1': ('bad.png', b'not png')}).status_code == 400
    assert client.post(path, data={'date': '2023-01-05'}, files={'unknown': ('ok.png', PNG)}).status_code == 422
    assert client.post(path, data={'date': '2023-01-05'},
        files=[('slide-1-pic-1', ('ok.png', PNG)), ('slide-1-pic-1', ('ok.png', PNG))]).status_code == 422
    assert client.post(path, data={'date': '2023-01-05'},
        files={'slide-1-pic-1': ('large.png', PNG + b'x' * (4 * 1024**2))}).status_code == 413
    assert client.post(path, data={'date': '2023-01-05'},
        files=[('image-' + str(index), ('ok.png', PNG)) for index in range(401)]).status_code == 400
    # A boundary-size PNG is accepted; an unbound slot keeps the original.
    boundary = PNG + b'x' * (4 * 1024**2 - len(PNG))
    assert client.post(path, data={'date': '2023-01-05'}, files={'slide-1-pic-1': ('ok.png', boundary)}).status_code == 200


def test_four_hundred_png_files_are_accepted(ppt_client):
    client, _ = ppt_client
    saved = upload(client, data=make_pptx(many=400))
    slots = saved['presentation']['slides'][0]['slots']
    keys = [slot['slot_id'] for slot in slots if slot['replaceable']][:400]
    assert len(keys) == 400
    result = export(client, saved['id'], images={key: PNG for key in keys})
    assert result['updated_slots'] == [] and len(result['kept_slots']) == 409


def test_original_hash_change_blocks_export_and_never_substitutes_another_file(ppt_client):
    client, _ = ppt_client
    saved = upload(client)
    root = isolated_database_path(17, WB).parent / 'ppt'
    (root / (saved['sha256'] + '.pptx')).write_bytes(b'changed')
    response = client.post(BASE + '/' + saved['id'] + '/export', data={'date': '2023-01-05'})
    assert response.status_code == 409
    assert not list((root / 'exports').glob('*.pptx'))


def test_export_fences_binding_changes_during_assembly_and_removes_uncommitted_file(ppt_client, monkeypatch):
    client, _ = ppt_client
    saved = upload(client)
    original = ppt.assemble

    def racing_assembly(*args, **kwargs):
        titles = original(*args, **kwargs)
        with isolated_session_factory(17, WB)() as session:
            row = session.get(WorkbenchArtifact, saved['id'])
            row.revision = 2
            row.title = '并发编辑'
            session.commit()
        return titles

    monkeypatch.setattr(ppt, 'assemble', racing_assembly)
    response = client.post(BASE + '/' + saved['id'] + '/export', data={'date': '2023-01-05'})
    assert response.status_code == 409, response.text
    assert not list(isolated_database_path(17, WB).parent.glob('ppt/exports/*.pptx'))
    current = client.get(BASE + '/' + saved['id']).json()
    assert current['revision'] == 2 and current['title'] == '并发编辑' and current['exports'] == []


def test_app_registration_and_scoped_auth_middleware_accept_ppt_routes(ppt_client, monkeypatch):
    from tin.config import settings
    from tin.integration import auth, workbench_scope
    from tin.integration.identity import Identity
    from tin.integration.workbench_scope import WorkbenchScope
    from tin.web.app import app
    from tests.test_workbench_sources_v2 import source

    client, state = ppt_client
    assert {route.path for route in ppt.router.routes} <= set(app.openapi()['paths'])
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://fixture.invalid')
    monkeypatch.setattr(settings, 'public_prefix', '')
    identity = Identity('first', 'fixture@test', 'Fixture', 17, 'owner')

    async def verify(*args, **kwargs):
        return identity

    async def resolve(identity, identifier, authorization):
        return WorkbenchScope(identity, identifier, '', 0, {'workspace_mode': 'isolated', 'can_edit': True},
                              authorization, scope_version=2, sources_revision=7, sources=(source(1),))

    monkeypatch.setattr(auth, 'verify_bearer', verify)
    monkeypatch.setattr(workbench_scope, 'resolve_workbench', resolve)
    state.app.add_middleware(auth.ResearchAuthMiddleware)
    headers = {'authorization': 'Bearer fixture', 'origin': 'http://testserver'}
    root = '/workbenches/' + WB + BASE
    uploaded = client.post(root, headers=headers, files={'file': ('synthetic.pptx', make_pptx())})
    assert uploaded.status_code == 201, uploaded.text
    path = root + '/' + uploaded.json()['id']
    assert client.get(path, headers=headers).status_code == 200
    exported = client.post(path + '/export', headers=headers, data={'date': '2023-01-05'})
    assert exported.status_code == 200, exported.text
    assert client.get(path + '/exports/' + exported.json()['export_id'], headers=headers).status_code == 200
    assert client.patch(path + '/bindings', headers={**headers, 'x-can-edit': 'false'},
                        json={'revision': 2, 'slots': [{'slot_id': 'slide-1-pic-1', 'binding': None}]}).status_code == 403
