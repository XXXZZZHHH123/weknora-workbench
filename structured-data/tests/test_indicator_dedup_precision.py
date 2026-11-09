"""Source metadata and exact display precision, independent of indicator names."""
import copy
import hashlib
import json
from pathlib import Path

import pytest
from openpyxl import load_workbook
from fastapi import HTTPException

from structured_data.research_facts_v2 import read_current, parameters
from structured_data.research_precision import display_decimals
from structured_data.store import DataStore
from structured_data.workspaces import Identity, WorkspaceRegistry, current_identity
from test_knowledge_research_facts import facts


@pytest.fixture
def precision_source(facts):
    _, store, parsed, review_path, review, _ = facts
    parsed = copy.deepcopy(parsed)
    source = Path(parsed['source_file'])
    workbook = load_workbook(source)
    sheet = workbook['Records']
    sheet['B2'] = 1.2345
    sheet['B2'].number_format = '0.00'
    sheet['B3'] = 2.5
    sheet['B3'].number_format = '0.000'
    sheet.append(['2026-01-03', 3.75])
    sheet['B4'].number_format = 'General'
    workbook.save(source)
    digest = hashlib.md5(source.read_bytes()).hexdigest()
    parsed['sha256'] = hashlib.sha256(source.read_bytes()).hexdigest()
    parsed['rule_version'] = 'dedup-precision-fixture'
    parsed['series'][0].update(metric='Arbitrary field', description='S0000042',
        dimensions={'vendor_identifier':'EDB-00000042'}, header_text=['S0000042', 'unrelated heading'])
    parsed['observations'] = [{'series_id':'quantity', 'date':f'2026-01-0{index}', 'value':value,
        'raw_value':raw, 'source_cell':f'B{index+1}', 'value_status':'valid'}
        for index, value, raw in [(1, 1.2345, '1.2345'), (2, 2.5, '2.5'), (3, 3.75, '3.750')]]
    review['dataset_id'] = store.publish(parsed)['dataset_id']
    review['native_source_bindings'][0]['file_hash'] = digest
    review_path.write_text(json.dumps(review))
    sources = [{'id':'binding-a', 'source_type':'knowledge_base', 'source_id':'library-a',
        'knowledge_base_id':'library-a', 'tenant_id':9, 'kind':'primary', 'delegated':False,
        'knowledge':[{'id':'original-a', 'file_type':'xlsx', 'file_hash':digest}]}]
    identity = Identity('reader','reader@example.test','Reader',9,'viewer',
        workbench_id='10000000-0000-4000-8000-000000000091',scope_version=2,sources_revision=7)
    registry = WorkspaceRegistry(store.root.parent.parent,9)
    def read(body, selected=None):
        marker = current_identity.set(identity)
        try:
            return read_current(registry, {'source_bindings':['binding-a'], **body}, sources if selected is None else selected)
        finally:
            current_identity.reset(marker)
    return read, sources


def test_catalog_preserves_vendor_evidence_without_opening_observations(precision_source, monkeypatch):
    read, _ = precision_source
    monkeypatch.setattr(DataStore, '_connect', lambda *a, **kw: pytest.fail('catalog opened observations'))
    field = read({'catalog_only':True})['series'][0]
    assert field['metric'] == 'Arbitrary field'
    assert field['description'] == 'S0000042'
    assert field['dimensions']['vendor_identifier'] == 'EDB-00000042'
    assert field['header_text'] == ['S0000042', 'unrelated heading']


def test_precision_is_opt_in_and_uses_real_numeric_formats(precision_source):
    read, _ = precision_source
    identifier = read({'catalog_only':True})['series'][0]['series_id']
    body = {'series_ids':[identifier], 'view_module':'indicators', 'points_per_series':500}
    regular = read(body)
    assert all('raw_value' not in row and 'display_decimals' not in row for row in regular['rows'])
    verified = read({**body, 'include_display_precision':True})
    assert [row['display_decimals'] for row in verified['rows']] == [2, 3, 3]
    assert [row['raw_value'] for row in verified['rows']] == ['1.2345', '2.5', '3.750']
    assert regular['version'] != verified['version']
    assert all('source_cell' not in row for row in verified['rows'])


@pytest.mark.parametrize('code,raw,expected', [
    ('0.00','12.345',2), ('0','1.5',0), ('#,##0.000','1234.5',3),
    ('General','1.2300',4), ('General','1e-3',3), ('0.##','1.20',1),
    ('0.00%','0.12345',4), ('USD0.00','1.234',None), (None,'1.234',None), ('0.00','NaN',None),
])
def test_numeric_format_precision_is_identifier_and_name_independent(code, raw, expected):
    assert display_decimals(raw, code) == expected


@pytest.mark.parametrize('extra', [
    {'catalog_only':True}, {'view_module':'market'}, {'include_display_precision':'true'},
    {'points_per_series':1001}, {'series_ids':['kb-'+'a'*48]*3,'points_per_series':500},
])
def test_precision_read_cannot_expand_page_budget_or_other_modules(extra):
    body = {'source_bindings':['binding-a'], 'series_ids':['kb-'+'b'*48],
        'view_module':'indicators', 'points_per_series':500, 'include_display_precision':True, **extra}
    with pytest.raises(ValueError):
        parameters(body)


def test_delegated_sources_cannot_read_precision(precision_source):
    read, sources = precision_source
    identifier = read({'catalog_only':True})['series'][0]['series_id']
    with pytest.raises(HTTPException) as error:
        read({'series_ids':[identifier], 'view_module':'indicators', 'points_per_series':500,
            'include_display_precision':True}, [{**sources[0], 'delegated':True}])
    assert error.value.status_code == 403
