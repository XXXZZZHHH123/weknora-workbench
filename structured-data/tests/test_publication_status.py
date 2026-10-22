"""Query availability must come from a bound saved snapshot, never scan labels."""
import copy
import json

import pytest

from structured_data.imports import ImportManager


def publication_fixture(tmp_path):
    manager = ImportManager(tmp_path / 'data')
    doc = {'import_id': 'a' * 24, 'dataset_id': 'b' * 24,
           'sha256': 'c' * 64, 'source_file': 'measurements.xlsx',
           'sheets': [{'name': state, 'status': state, 'mapping': {'blocks': []}}
                      for state in ['auto_ready', 'template_ready', 'confirmed', 'trusted_existing']]}
    snapshot = {k: doc[k] for k in ['import_id', 'dataset_id', 'sha256', 'source_file']}
    snapshot['sheets'] = [{'name': 'auto_ready', 'mapping': {'blocks': [{'columns': []}]}},
                          {'name': 'trusted_existing'}]
    folder = manager.store.datasets / doc['dataset_id']
    folder.mkdir()
    (folder / 'manifest.json').write_text(json.dumps(snapshot))
    return manager, doc, snapshot, folder


def test_publication_uses_saved_sheets_not_scanner_status_and_does_not_mutate(tmp_path):
    manager, doc, snapshot, _ = publication_fixture(tmp_path)
    original = copy.deepcopy(doc)
    view = manager.publication_view(doc)
    states = {s['name']: s for s in view['sheets']}
    assert states['auto_ready']['published_in_dataset'] is True
    assert states['trusted_existing']['published_in_dataset'] is True
    assert states['template_ready']['published_in_dataset'] is False
    assert states['confirmed']['published_in_dataset'] is False
    assert states['auto_ready']['published_mapping'] == snapshot['sheets'][0]['mapping']
    assert states['trusted_existing']['published_mapping'] is None
    assert doc == original


@pytest.mark.parametrize('key,value', [('dataset_id', 'd' * 24), ('sha256', 'e' * 64),
                                      ('source_file', 'another.xlsx'), ('import_id', 'f' * 24)])
def test_snapshot_binding_mismatch_cannot_claim_queryability(tmp_path, key, value):
    manager, doc, snapshot, folder = publication_fixture(tmp_path)
    snapshot[key] = value
    (folder / 'manifest.json').write_text(json.dumps(snapshot))
    assert all(not s['published_in_dataset'] for s in manager.publication_view(doc)['sheets'])


def test_missing_snapshot_and_unsaved_review_are_not_queryable(tmp_path):
    manager, doc, _, folder = publication_fixture(tmp_path)
    (folder / 'manifest.json').unlink()
    assert all(not s['published_in_dataset'] for s in manager.publication_view(doc)['sheets'])
    doc['dataset_id'] = None
    assert all(not s['published_in_dataset'] for s in manager.publication_view(doc)['sheets'])


def test_only_verified_legacy_detail_can_supply_missing_mapping(tmp_path):
    manager, doc, _, _ = publication_fixture(tmp_path)
    for name in ['trusted_existing', 'template_ready']:
        verified = {'name': name, 'status': name, 'mapping': {'blocks': [{'legacy': True}]}}
        view = manager.publication_view(doc, verified_legacy_sheet=verified)
        sheet = next(s for s in view['sheets'] if s['name'] == name)
        assert sheet['published_mapping'] == (verified['mapping'] if name == 'trusted_existing' else None)
