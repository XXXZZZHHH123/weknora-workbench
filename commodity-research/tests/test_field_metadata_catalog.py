"""The read-only catalogue accepts optional source metadata without loosening its schema."""
from types import SimpleNamespace

import pytest
from fastapi import HTTPException
from pydantic import ValidationError

from tin.integration import page_facts
from tin.integration.field_metadata import FieldMetadata
from tin.web.catalog import indicator_row


def test_research_catalog_displays_unit_frequency_role_and_quality():
    field = {'series_id': 'fixture', 'metric': 'Quantity', 'unit': '实物吨', 'frequency': '月',
             'field_metadata': {'role': 'numeric', 'unit': '实物吨', 'frequency': '月'}}
    row = indicator_row(field)
    assert row['unit'] == '实物吨' and row['frequency'] == '月' and row['note'] == '数值指标'
    old = indicator_row({'series_id': 'legacy'})
    assert old['field_metadata'] is None and old['frequency'] == '未注明'
    assert '单位待核对' in old['note'] and '频率未注明' in old['note']
    assert '说明列' in indicator_row({**field, 'field_metadata': {'role': 'annotation'}})['note']


def test_strict_metadata_defaults_and_unknown_fields():
    assert FieldMetadata().unit is None and FieldMetadata().frequency is None
    with pytest.raises(ValidationError):
        FieldMetadata.model_validate({'frequency': '周', 'invented': True})


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
    document['indicators'][0]['field_metadata'] = {'unit': '元'}
    assert page_facts._request(scope, {'series_ids': []})['indicators'][0]['unit'] == '元'
