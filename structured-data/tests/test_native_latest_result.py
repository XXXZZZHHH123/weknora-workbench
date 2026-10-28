"""Opening an original selects its latest published result, not a scan mode."""
import hashlib
import json

import pytest

from structured_data.imports import ImportManager
from test_fresh_original_review import original
from test_native_knowledge_import import headers, native


def rewrite(path, **changes):
    doc = json.loads(path.read_text())
    doc.update(changes)
    path.write_text(json.dumps(doc, ensure_ascii=False))


def records(manager, path, source):
    result = []
    for mode, stamp in [(True, '2024-01-01T00:00:00+00:00'),
                        (False, '2024-01-02T00:00:00+00:00')]:
        saved = manager.ingest(path, use_reviewed_mappings=mode, native_source=source)
        rewrite(manager.store._path(saved['dataset_id']) / 'manifest.json', imported_at=stamp)
        result.append(saved)
    # A rescan's metadata timestamp does not make its old publication newer.
    rewrite(manager.directory / result[0]['import_id'] / 'review.json',
            updated_at='2024-01-03T00:00:00+00:00')
    return result


@pytest.fixture
def paired(tmp_path):
    path = original(tmp_path / 'original.xlsx')
    manager = ImportManager(tmp_path / 'data')
    source = {'source_knowledge_id': 'source-a', 'file_name': path.name,
              'file_size': path.stat().st_size, 'file_hash': hashlib.md5(path.read_bytes()).hexdigest(),
              'knowledge_base_id': 'library-a', 'tenant_id': 10000}
    old, fresh = records(manager, path, source)
    return manager, source, old, fresh


@pytest.mark.parametrize('mode,position', [(True, 0), (False, 1), (None, 1)])
def test_source_open_uses_latest_publication_while_explicit_import_modes_stay_separate(paired, mode, position):
    manager, source, old, fresh = paired
    before = {str(p): p.read_bytes() for p in manager.root.rglob('*') if p.is_file()}
    result = manager.find_native_source(source, use_reviewed_mappings=mode)
    assert result['dataset_id'] == (old, fresh)[position]['dataset_id']
    assert before == {str(p): p.read_bytes() for p in manager.root.rglob('*') if p.is_file()}


def test_open_ignores_unpublished_review_but_explicit_mode_can_resume_it(paired):
    manager, source, old, fresh = paired
    rewrite(manager.directory / fresh['import_id'] / 'review.json',
            dataset_id=None, publish_status='pending', updated_at='2024-02-01T00:00:00+00:00')
    assert manager.find_native_source(source, use_reviewed_mappings=None)['dataset_id'] == old['dataset_id']
    assert manager.find_native_source(source, use_reviewed_mappings=False)['import_id'] == fresh['import_id']


def test_open_returns_none_when_no_result_has_been_published(paired):
    manager, source, old, fresh = paired
    for item in (old, fresh):
        rewrite(manager.directory / item['import_id'] / 'review.json', dataset_id=None, publish_status='pending')
    assert manager.find_native_source(source, use_reviewed_mappings=None) is None


def test_same_publication_time_with_different_versions_is_a_conflict(paired):
    manager, source, old, fresh = paired
    rewrite(manager.store._path(old['dataset_id']) / 'manifest.json',
            imported_at='2024-01-02T08:00:00+08:00')
    with pytest.raises(ValueError, match='冲突'):
        manager.find_native_source(source, use_reviewed_mappings=None)


@pytest.mark.parametrize('field,value', [('tenant_id', 20000), ('knowledge_base_id', 'other-library'),
                                         ('source_knowledge_id', 'other-source'), ('file_hash', '0' * 32)])
def test_latest_lookup_preserves_exact_authorized_native_binding(paired, field, value):
    manager, source, _, _ = paired
    assert manager.find_native_source({**source, field: value}, use_reviewed_mappings=None) is None


@pytest.mark.parametrize('field,value', [('import_id', 'f' * 24), ('sha256', '0' * 64),
                                         ('source_file', 'other.xlsx')])
def test_published_result_must_belong_to_matching_review_and_original(paired, field, value):
    manager, source, _, fresh = paired
    rewrite(manager.store._path(fresh['dataset_id']) / 'manifest.json', **{field: value})
    with pytest.raises(ValueError, match='不一致'):
        manager.find_native_source(source, use_reviewed_mappings=None)


def test_latest_original_digest_is_still_verified(paired):
    manager, source, _, fresh = paired
    path = manager.source_path(manager.get(fresh['import_id']))
    data = bytearray(path.read_bytes())
    data[-1] ^= 1
    path.write_bytes(data)
    with pytest.raises(ValueError, match='校验失败'):
        manager.find_native_source(source, use_reviewed_mappings=None)


@pytest.mark.parametrize('name', ['data.duckdb', 'series.json', 'source.xlsx'])
def test_incomplete_snapshot_cannot_be_exposed_as_latest_result(paired, name):
    manager, source, _, fresh = paired
    (manager.store._path(fresh['dataset_id']) / name).unlink()
    with pytest.raises(ValueError, match='不完整'):
        manager.find_native_source(source, use_reviewed_mappings=None)


def seed_native(native):
    _, _, state, root = native
    path = root / 'original.xlsx'
    path.write_bytes(state['contents'])
    # Use the authorized native filename exactly as production load_source does.
    path = path.rename(root / state['document']['file_name'])
    source = {'source_knowledge_id': state['document']['id'], **{key: state['document'][key]
              for key in ('file_name', 'file_size', 'file_hash', 'knowledge_base_id', 'tenant_id')}}
    manager = ImportManager(root / 'knowledge-bases' / 'kb-a')
    old, fresh = records(manager, path, source)
    return manager, old, fresh


def test_real_original_result_route_selects_new_mode_without_importing_or_writing(native):
    client, _, state, _ = native
    manager, _, fresh = seed_native(native)
    before = {str(p): p.read_bytes() for p in manager.root.rglob('*') if p.is_file()}
    response = client.get('/api/knowledge-results/source-a', headers=headers())
    assert response.status_code == 200, response.text
    assert response.json()['import_result']['dataset_id'] == fresh['dataset_id']
    assert before == {str(p): p.read_bytes() for p in manager.root.rglob('*') if p.is_file()}
    assert not any(path.endswith('/download') for path, _ in state['calls'])


def test_original_result_route_rejects_ambiguous_publications(native):
    client, _, _, _ = native
    manager, old, _ = seed_native(native)
    rewrite(manager.store._path(old['dataset_id']) / 'manifest.json', imported_at='2024-01-02T00:00:00+00:00')
    response = client.get('/api/knowledge-results/source-a', headers=headers())
    assert response.status_code == 409, response.text
    assert '冲突' in response.json()['detail']


def test_original_result_route_does_not_open_another_library(native):
    client, _, state, _ = native
    seed_native(native)
    response = client.get('/api/knowledge-results/source-a', headers=headers(kb='other-library'))
    assert response.status_code == 403, response.text
    assert not any(path.endswith('/download') for path, _ in state['calls'])
