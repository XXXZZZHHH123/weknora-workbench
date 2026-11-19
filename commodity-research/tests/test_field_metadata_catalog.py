"""The read-only catalogue accepts optional source metadata without loosening its schema."""
from datetime import date
from importlib.util import module_from_spec, spec_from_file_location
from pathlib import Path
from types import SimpleNamespace

import pytest
from fastapi import HTTPException
from lxml.html import fromstring
from pydantic import ValidationError

from tin.integration import page_facts
from tin.integration.field_metadata import FieldMetadata, validate_field_metadata
from tin.web.catalog import indicator_row


HEADER_NOTE = {'cell': 'B2', 'text': '  原表数据范围。\n' + '以首次公布口径为准。' * 22 + '末尾 <原文>  '}
SOURCE_CODES = [{'cell': 'B4', 'text': 'S5329490'}, {'cell': 'B5', 'text': '  <原始代码>  '}]


def test_header_metadata_round_trip_between_strict_service_models():
    path = Path(__file__).resolve().parents[2] / 'structured-data/src/structured_data/field_metadata.py'
    spec = spec_from_file_location('structured_field_metadata_contract', path)
    producer = module_from_spec(spec)
    spec.loader.exec_module(producer)
    for sender, receiver in ((producer.FieldMetadata, FieldMetadata), (FieldMetadata, producer.FieldMetadata)):
        for payload in ({}, {'header_notes': [HEADER_NOTE], 'source_codes': SOURCE_CODES}):
            outbound = sender.model_validate(payload).model_dump_json(exclude_unset=True)
            assert receiver.model_validate_json(outbound).model_dump(exclude_unset=True) == payload
        with pytest.raises(ValidationError):
            receiver.model_validate({'header_notes': [{**HEADER_NOTE, 'unrecognized': True}]})
        with pytest.raises(ValidationError):
            receiver.model_validate({'source_codes': [{'cell': '$sheet_title', 'text': 'S5329490'}]})


def test_published_header_note_is_raw_expandable_text_in_indicator_caliber_and_previews(session):
    from tin.export import diagram, indicators
    from tin.web.app import templates

    identifier = 'SMM.SN.spot.1'
    through = date(2026, 1, 3)
    short = {'cell': 'B3', 'text': '  来源资料 <样本>  '}
    session.info['knowledge_facts_catalog'] = {identifier: {'field_metadata': {
        'header_notes': [HEADER_NOTE, short], 'source_codes': SOURCE_CODES}}}

    detail = diagram.detail(session, identifier, through)
    preview = indicators.previews(session, [identifier], through)[0]
    assert detail['header_notes'] == preview['header_notes'] == [HEADER_NOTE, short]
    assert detail['source_codes'] == preview['source_codes'] == SOURCE_CODES
    assert detail['caliber'] and all(entry['value'] != HEADER_NOTE['text'] for entry in detail['caliber'])
    for template, context in (('_indicator_caliber.html', {'d': detail}),
                              ('_indicator_cards.html', {'cards': [preview], 'd': through})):
        html = fromstring(templates.get_template(template).render(context))
        section, = html.xpath('.//details[@class="header-note"]')
        assert 'open' not in section.attrib
        assert section.xpath('./summary/text()') == ['表头说明 B2']
        assert section.xpath('./summary/span/text()') == [HEADER_NOTE['text'][:120] + '…']
        assert html.xpath('.//p[@class="header-note-text"]/text()') == [HEADER_NOTE['text'], short['text']]
        assert html.xpath('.//div[@class="header-note-text"]/text()') == [
            '代码：S5329490', '代码：  <原始代码>  ']
        assert not section.xpath('./p/*')


def test_research_catalog_displays_unit_frequency_role_and_quality():
    field = {'series_id': 'fixture', 'metric': 'Quantity', 'unit': '实物吨', 'frequency': '月',
             'field_metadata': {'role': 'numeric', 'unit': '实物吨', 'frequency': '月'}}
    row = indicator_row(field)
    assert row['unit'] == '实物吨' and row['frequency'] == '月' and row['note'] == '数值指标'
    old = indicator_row({'series_id': 'legacy'})
    assert old['field_metadata'] is None and old['frequency'] == '未注明'
    assert 'header_notes' not in old
    assert '单位待核对' in old['note'] and '频率未注明' in old['note']
    assert '说明列' in indicator_row({**field, 'field_metadata': {'role': 'annotation'}})['note']


def test_strict_metadata_defaults_and_unknown_fields():
    assert FieldMetadata().unit is None and FieldMetadata().frequency is None
    assert FieldMetadata().unit_source is None
    with pytest.raises(ValidationError):
        FieldMetadata.model_validate({'frequency': '周', 'invented': True})


def test_catalog_explains_human_declared_units_without_inventing_source_evidence():
    field = {'series_id': 'declared', 'unit': '人工核定单位',
             'field_metadata': {'unit': '人工核定单位', 'unit_source': 'declared', 'unit_evidence': []}}
    row = indicator_row(field)
    assert row['unit'] == '人工核定单位'
    assert '单位为人工确认，未在原表找到对应文字' in row['note']
    assert row['field_metadata']['unit_evidence'] == []


@pytest.mark.parametrize('unit_source', ['source', 'declared'])
def test_published_metadata_projects_with_real_source_identity(monkeypatch, unit_source):
    """Keep the consumer half of the producer's cross-service contract in its CI job."""
    evidence = {'sheet': 'Observations', 'cell': 'B1', 'text': '收入（元） / 周度 / 周'}
    unit = '元' if unit_source == 'source' else '人工核定单位'
    field = {'series_id': 'kb-' + 'a' * 48, 'metric': '收入（元） / 周度 / 周',
             'source_binding_id': 'binding', 'source_knowledge_id': 'original-fixture',
             'source_series_id': 'original-field', 'dataset_id': 'published-fixture',
             'knowledge_base_id': 'kb-fixture', 'knowledge_base_tenant_id': 9,
             'source_file': 'metadata.xlsx', 'sheet': 'Observations',
             'unit': unit, 'frequency': '周', 'field_metadata': {
                 'unit': unit, 'unit_source': unit_source, 'frequency': '周',
                 'unit_evidence': [evidence] if unit_source == 'source' else [],
                 'frequency_evidence': [evidence], 'header_notes': [HEADER_NOTE],
                 'source_codes': SOURCE_CODES}}
    response = {'catalog_only': True, 'series': [field], 'total': 1, 'offset': 0,
                'datasets': [], 'next_offset': None, 'truncated': False}
    scope = SimpleNamespace(scope_version=2, sources=[{'id': 'binding', 'delegated': False,
        'knowledge_base_id': 'kb-fixture', 'tenant_id': 9}])
    monkeypatch.setattr(page_facts, '_request', lambda *args: response)
    projected = page_facts._raw_catalog_page(scope)['series']
    validate_field_metadata(projected)
    published = projected[0]
    assert published['source_knowledge_id'] == 'original-fixture'
    assert published['source_series_id'] == 'original-field'
    assert published['dataset_id'] == 'published-fixture'
    row = indicator_row(published)
    assert row['series_id'] == field['series_id']
    assert row['unit'] == unit and row['frequency'] == '周'
    assert row['knowledge_base_id'] == 'kb-fixture'
    assert row['source'] == 'metadata.xlsx' and row['sheet'] == 'Observations'
    assert row['header_notes'] == [HEADER_NOTE]
    assert row['source_codes'] == SOURCE_CODES
    assert row['field_metadata']['unit_source'] == unit_source
    assert row['field_metadata']['frequency_evidence'] == [evidence]
    if unit_source == 'source':
        assert row['field_metadata']['unit_evidence'] == [evidence]
    else:
        assert row['field_metadata']['unit_evidence'] == []
        assert '单位为人工确认，未在原表找到对应文字' in row['note']


def test_catalog_consumer_rejects_unknown_metadata_without_dropping_acl(monkeypatch):
    scope = SimpleNamespace(scope_version=2, sources=[{'id': 'binding', 'delegated': False,
        'knowledge_base_id': 'kb-fixture', 'tenant_id': 9}])
    field = {'series_id': 'kb-'+'a'*48, 'source_binding_id': 'binding',
             'knowledge_base_id': 'kb-fixture', 'knowledge_base_tenant_id': 9,
             'field_metadata': {'frequency': '周', 'invented': True}}
    response = {'catalog_only': True, 'series': [field], 'total': 1, 'offset': 0, 'datasets': []}
    monkeypatch.setattr(page_facts, '_request', lambda *args: response)
    with pytest.raises(HTTPException) as error:
        page_facts._raw_catalog_page(scope)
    assert error.value.status_code == 503
    field['field_metadata'] = {}
    assert page_facts._raw_catalog_page(scope)['series'][0]['field_metadata'] == {}
    field['knowledge_base_id'] = 'other-kb'
    with pytest.raises(HTTPException, match='目录项超出当前直接来源授权'):
        page_facts._raw_catalog_page(scope)


def test_bounded_response_uses_same_strict_metadata_contract(monkeypatch):
    import httpx
    from tin.provenance import bridge
    scope = SimpleNamespace(workbench_id='fixture', sources_revision=1)
    document = {'scope_version': 2, 'workbench_id': 'fixture', 'sources_revision': 1,
                'version': 'a'*64, 'indicators': [{'series_id': 'field', 'unit': '元',
                  'field_metadata': {'unit': '元', 'unexpected': True}}]}
    monkeypatch.setattr(bridge, 'structured_request', lambda *args, **kwargs:
                        httpx.Response(200, request=httpx.Request('POST', 'https://example.invalid'), json=document))
    with pytest.raises(HTTPException) as error:
        page_facts._request(scope, {'series_ids': []})
    assert error.value.status_code == 503
    document['indicators'][0]['field_metadata'] = {'unit': '元', 'header_notes': [HEADER_NOTE]}
    field = page_facts._request(scope, {'series_ids': []})['indicators'][0]
    assert field['unit'] == '元' and field['field_metadata']['header_notes'] == [HEADER_NOTE]
