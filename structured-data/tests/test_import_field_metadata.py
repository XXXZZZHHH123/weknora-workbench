"""Source text, roles and strict optional metadata survive publication."""
from copy import deepcopy
from datetime import datetime
import json
from pathlib import Path

import pytest
from openpyxl import Workbook
from pydantic import ValidationError

from structured_data.field_metadata import FieldMetadata
from structured_data.imports import ImportManager


def original(tmp_path, *, composite=True):
    path = tmp_path / 'metadata.xlsx'
    book = Workbook(); sheet = book.active; sheet.title = 'Observations'
    sheet.append(['Date', '收入（元） / 周度 / 周' if composite else '收入', '附注'])
    sheet.append([datetime(2024, 1, 1), 4, 101]); sheet.append([datetime(2024, 1, 2), 5, 102])
    book.save(path)
    return path


def suggestion():
    return {'column_updates': [
        {'block_index': 0, 'column': 'B', 'unit': '元', 'unit_cells': ['B1'],
         'frequency': '周', 'frequency_cells': ['B1'], 'field_role': 'numeric'},
        {'block_index': 0, 'column': 'C', 'field_role': 'annotation'}],
        'evidence': [{'block_index': 0, 'column': column, 'field': field, 'cells': [column+'1']}
                     for column, fields in [('B', ['unit', 'unit_cells', 'frequency', 'frequency_cells', 'field_role']),
                                            ('C', ['field_role'])] for field in fields]}


def configured(manager):
    (manager.root / 'model-settings.json').write_text(json.dumps({'chat': {
        'model': 'fixture', 'base_url': 'https://example.invalid/v1', 'api_key': 'fixture-only'}}))


def test_new_ready_import_uses_source_checked_model_metadata(tmp_path, monkeypatch):
    path = original(tmp_path)
    manager = ImportManager(tmp_path / 'data'); configured(manager)
    calls = []
    def request(cfg, payload, budget, cancelled):
        budget.consume(); calls.append(payload)
        return suggestion()
    monkeypatch.setattr('structured_data.ai_review._request', request)
    first = manager.ingest(path, use_reviewed_mappings=False)
    assert len(calls) == 1
    assert any(c['cell'] == 'B1' and c['value'] == '收入（元） / 周度 / 周' for c in calls[0]['source_cells'])
    fields = manager.store.list_series(first['dataset_id'])['series']
    numeric = next(f for f in fields if f['field_metadata']['role'] == 'numeric')
    annotation = next(f for f in fields if f['field_metadata']['role'] == 'annotation')
    assert numeric['unit'] == '元' and numeric['frequency'] == '周'
    assert numeric['field_metadata']['unit_source'] == 'source'
    assert numeric['field_metadata']['unit_evidence'] == [{'sheet': 'Observations', 'cell': 'B1', 'text': '收入（元） / 周度 / 周'}]
    assert numeric['field_metadata']['frequency_evidence'] == numeric['field_metadata']['unit_evidence']
    assert manager.store.query_series(first['dataset_id'], numeric['series_id'], operation='sum')['aggregate'] == 9
    with pytest.raises(ValueError, match='不能作为数值指标聚合'):
        manager.store.query_series(first['dataset_id'], annotation['series_id'], operation='sum')
    rows = manager.store.query_series(first['dataset_id'], annotation['series_id'])['rows']
    assert {str(r['raw_value']) for r in rows} == {'101', '102'}
    assert all(r['value'] is None for r in rows)
    quality = manager.publication_view(manager.get(first['import_id']))['sheets'][0]['published_quality']
    assert quality['annotation_field_count'] == 1 and quality['no_valid_numeric_count'] == 0
    assert quality['unit_review_count'] == 0
    manager.ingest(path, use_reviewed_mappings=False)
    assert len(calls) == 1, 'existing imports are never silently backfilled'


def test_unreliable_model_metadata_leaves_units_unconfirmed(tmp_path, monkeypatch):
    path = original(tmp_path, composite=False)
    manager = ImportManager(tmp_path / 'data'); configured(manager)
    monkeypatch.setattr('structured_data.ai_review._request', lambda *args: suggestion())
    result = manager.ingest(path, use_reviewed_mappings=False)
    fields = manager.store.list_series(result['dataset_id'])['series']
    assert all(f['unit'] is None and not f.get('frequency') for f in fields)
    assert manager.get(result['import_id'])['sheets'][0]['metadata_review'] == 'needs_review'


def test_explicit_metadata_has_no_unit_dictionary_and_preserves_raw_evidence(tmp_path):
    path = original(tmp_path)
    manager = ImportManager(tmp_path / 'data')
    result = manager.ingest(path, use_reviewed_mappings=False)
    mapping = deepcopy(manager.sheet(result['import_id'], 'Observations')['mapping'])
    column = mapping['blocks'][0]['columns'][0]
    column.update(unit='元', unit_cells=['B1'], frequency='周度', frequency_cells=['B1'])
    checked = manager.preview(result['import_id'], mapping)['series'][0]
    assert checked['unit'] == '元' and checked['frequency'] == '周度'
    column['unit'] = 'not-in-the-source'
    with pytest.raises(ValueError, match='实际元数据与映射不一致'):
        manager.preview(result['import_id'], mapping)


def test_metadata_contract_defaults_and_rejects_unknown_fields():
    assert FieldMetadata.model_validate({}).role is None
    assert FieldMetadata.model_validate({}).frequency is None
    assert FieldMetadata.model_validate({}).unit_source is None
    assert FieldMetadata.model_validate({'unit_source': 'declared'}).unit_evidence == []
    with pytest.raises(ValidationError):
        FieldMetadata.model_validate({'unit_source': 'guessed'})
    with pytest.raises(ValidationError):
        FieldMetadata.model_validate({'unknown': 'invented'})
    with pytest.raises(ValidationError):
        FieldMetadata.model_validate({'unit': '\n元'})
    consumer = Path(__file__).resolve().parents[2] / 'commodity-research/src/tin/integration/field_metadata.py'
    producer = Path(__file__).resolve().parents[1] / 'src/structured_data/field_metadata.py'
    assert producer.read_text() == consumer.read_text()


def test_existing_mapping_unit_without_header_text_parses_as_declared(tmp_path):
    from structured_data.generic_parser import parse_mapped_workbook
    from structured_data.scanner import scan_workbook
    path = original(tmp_path, composite=False)
    mapping = scan_workbook(path, use_reviewed_mappings=False)['sheets'][0]['mapping']
    mapping['blocks'][0]['columns'][0]['unit'] = '研究员确认单位'
    parsed = parse_mapped_workbook(path, [mapping])
    field = parsed['series'][0]
    assert field['unit'] == '研究员确认单位'
    assert field['field_metadata']['unit_source'] == 'declared'
    assert field['field_metadata']['unit_evidence'] == []
    assert '单位为人工确认，未在原表找到对应文字' in field['description']
    assert [r['value'] for r in parsed['observations'] if r['series_id'] == field['series_id']] == [4, 5]


def test_user_declared_unit_can_preview_confirm_and_save_template(tmp_path):
    manager = ImportManager(tmp_path / 'data')
    first = manager.ingest(original(tmp_path, composite=False), use_reviewed_mappings=False)
    mapping = deepcopy(manager.sheet(first['import_id'], 'Observations')['mapping'])
    mapping['blocks'][0]['columns'][0]['unit'] = '人工核定单位'
    preview = manager.preview(first['import_id'], mapping)
    assert preview['series'][0]['field_metadata']['unit_source'] == 'declared'
    saved = manager.confirm(first['import_id'], mapping, save_template=True)
    field = next(f for f in manager.store.list_series(saved['dataset_id'])['series'] if f['unit'])
    assert field['unit'] == '人工核定单位'
    assert field['field_metadata']['unit_source'] == 'declared'
    assert field['field_metadata']['unit_evidence'] == []
    assert manager.get(first['import_id'])['sheets'][0]['template_status'] == 'saved'
    assert manager.store.query_series(saved['dataset_id'], field['series_id'], operation='sum')['aggregate'] == 9


@pytest.mark.parametrize('metadata_only', [False, True])
def test_model_suggested_unit_without_verbatim_evidence_remains_unadopted(tmp_path, monkeypatch, metadata_only):
    from structured_data.ai_review import suggest_mapping
    manager = ImportManager(tmp_path / 'data')
    first = manager.ingest(original(tmp_path, composite=False), use_reviewed_mappings=False)
    proposal = {'column_updates': [{'block_index': 0, 'column': 'B', 'unit': '模型猜测单位'}],
                'evidence': [{'block_index': 0, 'column': 'B', 'field': 'unit', 'cells': ['B1']}]}
    monkeypatch.setattr('structured_data.ai_review._request', lambda *args: proposal)
    result = suggest_mapping(manager, first['import_id'], 'Observations', strict=True, metadata_only=metadata_only)
    assert any(d.get('field') == 'unit' for d in result['diagnostics'])
    assert all(f['unit'] is None for f in manager.store.list_series(first['dataset_id'])['series'])
    assert manager.sheet(first['import_id'], 'Observations')['mapping']['blocks'][0]['columns'][0]['unit'] is None


def test_matrix_declared_unit_without_row_text_is_retained(tmp_path):
    from structured_data.scanner import scan_workbook
    from structured_data.generic_parser import parse_mapped_workbook
    path = tmp_path / 'declared-matrix.xlsx'
    book = Workbook(); sheet = book.active; sheet.title = 'Periods'
    sheet.append(['Measure', '2021', '2022', '2023'])
    sheet.append(['Quantity', 3, 4, 5]); book.save(path)
    mapping = scan_workbook(path, use_reviewed_mappings=False)['sheets'][0]['mapping']
    mapping['blocks'][0]['row_rules'][0]['unit'] = '人工核定单位'
    parsed = parse_mapped_workbook(path, [mapping])
    field = parsed['series'][0]
    assert field['unit'] == '人工核定单位'
    assert field['field_metadata']['unit_source'] == 'declared'
    assert field['field_metadata']['unit_evidence'] == []
    assert '单位为人工确认，未在原表找到对应文字' in field['description']


def test_model_unit_source_cannot_disagree_with_reviewed_evidence(tmp_path, monkeypatch):
    from structured_data.ai_review import suggest_mapping
    manager = ImportManager(tmp_path / 'data')
    first = manager.ingest(original(tmp_path), use_reviewed_mappings=False)
    bad = suggestion(); bad['column_updates'][0]['unit_cells'] = ['C1']
    monkeypatch.setattr('structured_data.ai_review._request', lambda *args: bad)
    result = suggest_mapping(manager, first['import_id'], 'Observations', strict=True)
    assert any(d.get('field') == 'unit_cells' for d in result['diagnostics'])
    mapping = deepcopy(manager.sheet(first['import_id'], 'Observations')['mapping'])
    mapping['blocks'][0]['columns'][0].update(unit='附注', unit_cells=['C1'])
    with pytest.raises(ValueError, match='未覆盖该字段原列'):
        manager.preview(first['import_id'], mapping)


def test_matrix_metadata_uses_explicit_row_rule_and_keeps_annotation_non_numeric(tmp_path):
    from structured_data.scanner import scan_workbook
    from structured_data.generic_parser import parse_mapped_workbook
    path = tmp_path / 'matrix.xlsx'
    book = Workbook(); sheet = book.active; sheet.title = 'Periods'
    sheet.append(['Measure', '2021', '2022', '2023'])
    sheet.append(['数量（实物吨） / 月', 3, 4, 5]); sheet.append(['记录说明', 7, 8, 9]); book.save(path)
    mapping = scan_workbook(path, use_reviewed_mappings=False)['sheets'][0]['mapping']
    assert all(r['unit'] is None for r in mapping['blocks'][0]['row_rules'])
    rules = mapping['blocks'][0]['row_rules']
    rules[0].update(unit='实物吨', unit_cells=['A2'], frequency='月', frequency_cells=['A2'], field_role='numeric')
    rules[1].update(field_role='annotation')
    parsed = parse_mapped_workbook(path, [mapping])
    assert parsed['series'][0]['frequency'] == '月'
    assert parsed['series'][0]['field_metadata']['unit_evidence'] == [{'sheet': 'Periods', 'cell': 'A2', 'text': '数量（实物吨） / 月'}]
    assert [r['value'] for r in parsed['observations'] if r['series_id'] == parsed['series'][1]['series_id']] == [None] * 3


@pytest.mark.parametrize('unit_source', ['source', 'declared'])
def test_research_catalog_projects_published_metadata_with_real_source_identity(tmp_path, monkeypatch, unit_source):
    import hashlib
    from structured_data.research_facts import read_facts
    from structured_data.workspaces import Identity, current_identity
    path = original(tmp_path)
    manager = ImportManager(tmp_path / 'knowledge-bases/kb-fixture')
    native = dict(source_knowledge_id='original-fixture', knowledge_base_id='kb-fixture', tenant_id=9,
                  file_name=path.name, file_size=path.stat().st_size,
                  file_hash=hashlib.md5(path.read_bytes()).hexdigest())
    first = manager.ingest(path, use_reviewed_mappings=False, native_source=native)
    mapping = deepcopy(manager.sheet(first['import_id'], 'Observations')['mapping'])
    unit = '元' if unit_source == 'source' else '人工核定单位'
    column = mapping['blocks'][0]['columns'][0]
    column.update(unit=unit, frequency='周', frequency_cells=['B1'])
    if unit_source == 'source':
        column['unit_cells'] = ['B1']
    manager.confirm(first['import_id'], mapping)
    marker = current_identity.set(Identity('reader', '', '', 9, 'owner', 'kb-fixture', 9, True))
    try:
        response = read_facts(manager.store, manager, {'sources': [{'knowledge_id': 'original-fixture', 'file_hash': native['file_hash']}], 'catalog_only': True})
    finally:
        current_identity.reset(marker)
    field = next(f for f in response['indicators'] if f.get('unit') == unit)
    assert field['frequency'] == '周'
    assert field['source_knowledge_id'] == 'original-fixture'
    assert field['field_metadata']['unit_source'] == unit_source
    if unit_source == 'source':
        assert field['field_metadata']['unit_evidence'][0]['cell'] == 'B1'
    else:
        assert field['field_metadata']['unit_evidence'] == []
    assert field['field_metadata']['frequency_evidence'][0]['text'] == '收入（元） / 周度 / 周'

    # Run the actual consumer contract and display adapter on the producer payload.
    monkeypatch.syspath_prepend(str(Path(__file__).resolve().parents[2] / 'commodity-research/src'))
    for key, value in {'TIN_LLM_BASE_URL': '', 'TIN_LLM_API_KEY': '', 'TIN_DATABASE_URL': 'sqlite://', 'TIN_AUTO_MIGRATE': 'false'}.items():
        monkeypatch.setenv(key, value)
    # The structured-data CI job intentionally installs only producer dependencies.
    # Consumer coverage also runs unconditionally in commodity-research's suite.
    pytest.importorskip('tin')
    pytest.importorskip('sqlalchemy')
    from tin.integration.field_metadata import validate_field_metadata
    from tin.web.catalog import indicator_row
    validate_field_metadata(response['indicators'])
    displayed = indicator_row(field)
    assert displayed['unit'] == unit and displayed['frequency'] == '周'
    if unit_source == 'source':
        assert displayed['field_metadata']['unit_evidence'][0]['cell'] == 'B1'
    else:
        assert '单位为人工确认，未在原表找到对应文字' in displayed['note']
        assert displayed['field_metadata']['unit_evidence'] == []


def test_ready_matrix_also_requests_source_checked_metadata(tmp_path, monkeypatch):
    from structured_data.scanner import scan_workbook
    path = tmp_path / 'matrix-ready.xlsx'
    book = Workbook(); sheet = book.active; sheet.title = 'Periods'
    sheet.append(['Measure', '2021', '2022', '2023'])
    sheet.append(['数量（实物吨） / 月', 3, 4, 5]); book.save(path)
    manager = ImportManager(tmp_path / 'data'); configured(manager)
    scan = scan_workbook(path, use_reviewed_mappings=False)
    # Exercise the new-ready/template path before it publishes, without altering scanner approval.
    doc = {**scan, 'import_id': 'a'*24, 'source_file': path.name}
    folder = manager.directory / doc['import_id']; folder.mkdir(); (folder/path.name).write_bytes(path.read_bytes())
    doc['sheets'][0]['status'] = 'template_ready'
    def request(cfg, payload, budget, cancelled):
        budget.consume()
        rule = {**payload['candidate_mapping']['blocks'][0]['row_rules'][0],
                'unit': '实物吨', 'unit_cells': ['A2'], 'frequency': '月', 'frequency_cells': ['A2'], 'field_role': 'numeric'}
        return {'block_updates': [{'block_index': 0, 'row_rules': [rule]}],
                'evidence': [{'block_index': 0, 'field': 'row_rules', 'cells': ['A2']}]}
    monkeypatch.setattr('structured_data.ai_review._request', request)
    manager._enrich_new_metadata(doc)
    assert doc['sheets'][0]['metadata_review'] == 'source_checked'
    rule = doc['sheets'][0]['mapping']['blocks'][0]['row_rules'][0]
    assert rule['frequency'] == '月' and rule['unit'] == '实物吨'


def test_workbook_title_is_a_real_metadata_location_not_an_inferred_unit(tmp_path, monkeypatch):
    from openpyxl import load_workbook
    path = original(tmp_path, composite=False)
    book = load_workbook(path); book.active.title = 'Records · 元 · 周度'; book.save(path)
    manager = ImportManager(tmp_path / 'data'); configured(manager)
    def request(cfg, payload, budget, cancelled):
        budget.consume()
        assert {'cell': '$sheet_title', 'value': 'Records · 元 · 周度'} in payload['source_cells']
        return {'column_updates': [{'block_index': 0, 'column': 'B', 'unit': '元', 'unit_cells': ['$sheet_title'],
                                   'frequency': '周度', 'frequency_cells': ['$sheet_title']}],
                'evidence': [{'block_index': 0, 'column': 'B', 'field': field, 'cells': ['$sheet_title']}
                             for field in ('unit', 'unit_cells', 'frequency', 'frequency_cells')]}
    monkeypatch.setattr('structured_data.ai_review._request', request)
    result = manager.ingest(path, use_reviewed_mappings=False)
    spec = next(f for f in manager.store.list_series(result['dataset_id'])['series'] if f.get('unit'))
    assert spec['unit'] == '元' and spec['frequency'] == '周度'
    assert spec['field_metadata']['unit_evidence'] == [{'sheet': 'Records · 元 · 周度', 'cell': '$sheet_title', 'text': 'Records · 元 · 周度'}]
