"""Knowledge-backed templates keep only references; exports read current originals."""
from contextlib import contextmanager
from copy import deepcopy
import hashlib
import json
from types import SimpleNamespace

import httpx
import pytest

from tin.config import settings
from tin.integration import ppt_sources
from tin.integration.workbench_scope import isolated_database_path, isolated_session_factory
from tin.models.workbench import WorkbenchArtifact
from tin.web import ppt
from tests.ppt_fixtures import archive_bytes, make_pptx, ppt_entries
from tests.test_chart_api import chart_client
from tests.test_ppt_api import BASE, export, patch, ppt_client
from tests.test_workbench_sources_v2 import WB, source

KB = source(1)['knowledge_base_id']


@pytest.fixture
def kb_original(ppt_client, monkeypatch):
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://fixture.invalid')
    raw = make_pptx()
    state = SimpleNamespace(raw=raw, rows=[{'id': 'synthetic-ppt', 'knowledge_base_id': KB,
        'file_type': 'pptx', 'file_name': '合成周报.pptx', 'file_hash': hashlib.md5(raw).hexdigest(),
        'file_size': len(raw), 'created_at': '2030-01-02T03:04:05Z', 'metadata': {'page_count': 2}}],
        requests=[], downloads=[], download_status=200, inventory_status=200, on_download=None)

    def inventory(method, url, **kwargs):
        assert method == 'GET' and url == 'http://fixture.invalid/api/v1/knowledge-bases/' + KB + '/knowledge'
        assert kwargs['headers'] == {'Authorization': 'Bearer fixture', 'X-Tenant-ID': '17'}
        state.requests.append(url)
        return httpx.Response(state.inventory_status,
            json={'success': True, 'data': deepcopy(state.rows), 'total': len(state.rows)},
            request=httpx.Request(method, url))

    @contextmanager
    def download(method, url, **kwargs):
        assert method == 'GET' and url == 'http://fixture.invalid/api/v1/knowledge/synthetic-ppt/download'
        assert kwargs['headers'] == {'Authorization': 'Bearer fixture', 'X-Tenant-ID': '17'}
        state.downloads.append(url)
        if state.on_download:
            state.on_download()
        yield httpx.Response(state.download_status, content=state.raw, request=httpx.Request(method, url))

    monkeypatch.setattr(ppt_sources.httpx, 'request', inventory)
    monkeypatch.setattr(ppt_sources.httpx, 'stream', download)
    return (*ppt_client, state)


def selection(original):
    return {key: original.rows[0][field] for key, field in (
        ('knowledge_base_id', 'knowledge_base_id'), ('knowledge_id', 'id'), ('file_hash', 'file_hash'))}


def from_source(client, original):
    response = client.post(BASE + '/from-source', json=selection(original))
    assert response.status_code == 201, response.text
    return response.json()


def test_source_picker_and_creation_persist_reference_without_original(kb_original):
    client, _, original = kb_original
    listing = client.get(BASE + '/sources?search=周报&limit=1').json()
    assert listing['total'] == 1 and listing['next_offset'] is None
    assert listing['items'][0] == {**selection(original), 'filename': '合成周报.pptx',
        'created_at': '2030-01-02T03:04:05Z', 'size': len(original.raw), 'slide_count': 2}
    saved = from_source(client, original)
    assert saved['source'] == {'kind': 'knowledge_base', **selection(original)}
    assert saved['sha256'] == hashlib.sha256(original.raw).hexdigest()
    assert saved['slide_count'] == 2 and saved['source_state']['status'] == 'ready'
    root = isolated_database_path(17, WB).parent / 'ppt'
    assert not root.exists(), 'Creation must not create a private PPT directory or original'
    with isolated_session_factory(17, WB)() as session:
        body = json.loads(session.get(WorkbenchArtifact, saved['id']).body)
        assert body['source'] == saved['source'] and body['bindings'] == {}
    # Two templates sharing an original need only one inventory read per list.
    from_source(client, original)
    original.requests.clear()
    assert client.get(BASE).json()['total'] == 2
    assert len(original.requests) == 1


def test_knowledge_export_reloads_original_each_time_retains_only_five_outputs(kb_original):
    client, _, original = kb_original
    saved = from_source(client, original)
    original.downloads.clear()
    reports = []
    for index in range(6):
        reports.append(export(client, saved['id'], revision=str(saved['revision'] + index)))
    assert len(original.downloads) == 6
    root = isolated_database_path(17, WB).parent / 'ppt'
    assert list(root.glob('*.pptx')) == []
    assert len(list((root / 'exports').glob('*.pptx'))) == 5
    assert client.get(reports[0]['download_url']).status_code == 404
    assert client.get(reports[-1]['download_url']).status_code == 200
    assert client.get(BASE + '/' + saved['id']).json()['source'] == saved['source']


@pytest.mark.parametrize('problem,status', [('hash', 'changed'), ('deleted', 'deleted'),
    ('forbidden', 'forbidden'), ('service', 'unavailable')])
def test_current_original_state_blocks_export_without_mutating_template(kb_original, problem, status):
    client, _, original = kb_original
    saved = from_source(client, original)
    if problem == 'hash':
        original.rows[0]['file_hash'] = 'b' * 32
    elif problem == 'deleted':
        original.rows.clear()
    elif problem == 'forbidden':
        original.inventory_status = 403
    else:
        original.inventory_status = 503
    original.downloads.clear()
    detail = client.get(BASE + '/' + saved['id']).json()
    assert detail['source_state']['status'] == status and detail['candidates'] == {}
    assert client.get(BASE).json()['items'][0]['source_state']['status'] == status
    response = client.post(BASE + '/' + saved['id'] + '/export',
        data={'date': '2030-01-02', 'revision': '1'})
    assert response.status_code == (403 if problem == 'forbidden' else 503 if problem == 'service' else 409)
    if problem in {'hash', 'deleted'}:
        assert response.json()['detail']['code'] == 'source_' + status
    assert original.downloads == []
    assert not list(isolated_database_path(17, WB).parent.glob('ppt/exports/*.pptx'))
    assert detail['revision'] == saved['revision'] and detail['exports'] == []


def test_same_registered_hash_but_different_download_bytes_cannot_export(kb_original):
    client, _, original = kb_original
    saved = from_source(client, original)
    original.raw = b'different bytes during download'
    response = client.post(BASE + '/' + saved['id'] + '/export',
        data={'date': '2030-01-02', 'revision': '1'})
    assert response.status_code == 409 and response.json()['detail']['code'] == 'source_changed'
    assert not list(isolated_database_path(17, WB).parent.glob('ppt/exports/*.pptx'))


def test_saved_sha256_also_fences_a_changed_original(kb_original, monkeypatch):
    client, _, original = kb_original
    saved = from_source(client, original)
    # Even a reader returning bytes under an unchanged native hash must satisfy
    # the SHA256 pinned to the parsed layout.
    monkeypatch.setattr(ppt_sources, 'read_ppt_source', lambda *args: b'different source')
    response = client.post(BASE + '/' + saved['id'] + '/export',
        data={'date': '2030-01-02', 'revision': '1'})
    assert response.status_code == 409 and response.json()['detail']['code'] == 'source_changed'


def test_reselection_explicitly_accepts_new_hash_reparses_clears_bindings_and_fences_exports(kb_original):
    client, _, original = kb_original
    saved = from_source(client, original)
    saved = patch(client, saved, {'slide-1-pic-1': {'kind': 'keep'}})
    previous = export(client, saved['id'], revision=str(saved['revision']))
    entries = ppt_entries()
    entries['ppt/slides/slide7.xml'] = entries['ppt/slides/slide7.xml'].replace(b'2030', b'2031')
    entries['custom/new-part.txt'] = b'new edition'
    original.raw = archive_bytes(entries)
    original.rows[0].update(file_hash=hashlib.md5(original.raw).hexdigest(), file_name='新一期合成周报.pptx')
    response = client.patch(BASE + '/' + saved['id'] + '/source',
        json={'revision': previous['revision'], **selection(original)})
    assert response.status_code == 200, response.text
    current = response.json()
    assert current['bindings'] == {} and current['source_state']['status'] == 'ready'
    assert current['sha256'] != saved['sha256'] and current['title'] == '新一期合成周报.pptx'
    assert current['presentation'] == ppt.parse_pptx(original.raw)
    assert current['exports'] == [previous]
    assert client.get(previous['download_url']).status_code == 200
    for revision in (None, str(previous['revision'])):
        data = {'date': '2030-01-02', **({'revision': revision} if revision else {})}
        assert client.post(BASE + '/' + saved['id'] + '/export', data=data).status_code == 409
    assert client.patch(BASE + '/' + saved['id'] + '/source',
        json={'revision': previous['revision'], **selection(original)}).status_code == 409
    assert not list(isolated_database_path(17, WB).parent.glob('ppt/*.pptx'))


def test_foreign_and_readonly_reselection_are_rejected_before_source_reads(kb_original):
    client, _, original = kb_original
    saved = from_source(client, original)
    original.requests.clear()
    for headers, expected in (({'x-actor': 'someone-else'}, 404), ({'x-can-edit': 'false'}, 403)):
        assert client.patch(BASE + '/' + saved['id'] + '/source',
            json={'revision': 1, **selection(original)}, headers=headers).status_code == expected
    assert original.requests == []
    assert client.post(BASE + '/from-source', json=selection(original),
        headers={'x-can-edit': 'false'}).status_code == 403


def test_ungranted_reference_and_native_download_acl_are_never_bypassed(kb_original):
    client, _, original = kb_original
    denied = {**selection(original), 'knowledge_base_id': source(2)['knowledge_base_id']}
    assert client.post(BASE + '/from-source', json=denied).status_code == 403
    assert original.requests == original.downloads == []
    original.download_status = 403
    assert client.get(BASE + '/sources').json()['total'] == 1
    assert client.post(BASE + '/from-source', json=selection(original)).status_code == 403
    assert client.get(BASE).json()['total'] == 0
    assert not list(isolated_database_path(17, WB).parent.glob('ppt/*.pptx'))


def test_reselection_cas_rechecks_after_remote_download(kb_original):
    client, _, original = kb_original
    saved = from_source(client, original)

    def competing_edit():
        with isolated_session_factory(17, WB)() as session:
            row = session.get(WorkbenchArtifact, saved['id'])
            row.revision += 1
            row.title = '并发编辑保留'
            session.commit()

    original.on_download = competing_edit
    response = client.patch(BASE + '/' + saved['id'] + '/source',
        json={'revision': 1, **selection(original)})
    assert response.status_code == 409
    current = client.get(BASE + '/' + saved['id']).json()
    assert current['title'] == '并发编辑保留' and current['revision'] == 2
    assert not list(isolated_database_path(17, WB).parent.glob('ppt/*.pptx'))
