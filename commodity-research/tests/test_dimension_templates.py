"""Built-in structures, lossless draft merges and the existing save boundary."""
from copy import deepcopy
from dataclasses import replace

from fastapi import FastAPI, HTTPException
from fastapi.testclient import TestClient
from pydantic import ValidationError
import pytest

from tin.integration.workbench_scope import current_workbench
from tin.models import SourceMapping
from tin.web import dimension_board as board, dimension_templates as templates
from tin.web.research_dimensions import (CATEGORIES, DIMENSIONS_KEY, default_dimensions,
                                         read_dimensions, validate_dimension_list)
from tin.web.restricted_views import mapping_key
from tests.chart_fixtures import SID
from tests.test_core_focus import focus, add_chart
from tests.test_dimension_board import api, active, config, save


def apply(api, template_id='textile-chain', *, revision=None, dimensions=None):
    return board.template_draft(template_id, board.DimensionTemplateDraft(
        revision=board.revision(api.session) if revision is None else revision,
        dimensions=dimensions))


def custom(label='研究员维度', *, identifier='d-custom', sections=None):
    return {'id': identifier, 'label': label, 'category': '其他',
            'description': '研究员已有说明。', 'sections': sections or []}


def test_every_template_uses_shared_validation_and_contains_structure_only(monkeypatch):
    called = []
    real_validate = templates.validate_dimension_list
    def validate(value):
        called.append(deepcopy(value))
        return real_validate(value)
    monkeypatch.setattr(templates, 'validate_dimension_list', validate)
    result = templates.template_list()
    assert len(result) == len(called) == 2
    assert [value['name'] for value in result] == ['通用六维度', '纺织产业链']
    assert all(value['applicability'] for value in result)
    assert [{key: value for key, value in dim.items() if key != 'sections'}
            for dim in result[0]['dimensions']] == default_dimensions()
    assert all(not dim['sections'] for dim in result[0]['dimensions'])
    assert [dim['label'] for dim in result[1]['dimensions']] == [
        '价格与价差', '美棉供给', '国内供给与进口', '纺纱需求',
        '织布与终端需求', '库存与仓单', '替代品', '资金与宏观']
    for template in result:
        for dim in template['dimensions']:
            assert set(dim) <= {'id', 'label', 'category', 'description', 'sections'}
            assert dim['category'] in CATEGORIES and dim['description']
            assert all(isinstance(name, str) for name in dim['sections'])
            assert all('kb-' not in value for value in dim.values() if isinstance(value, str))
    result[1]['dimensions'][0]['sections'].clear()
    assert templates.template_list()[1]['dimensions'][0]['sections']


@pytest.mark.parametrize('change', ['items', 'indicator_id', 'category', 'sections', 'default_id'])
def test_loader_rejects_invalid_or_non_structural_template_data(monkeypatch, change):
    data = deepcopy(templates.TEMPLATE_DATA)
    if change in {'items', 'indicator_id'}:
        data[1]['dimensions'][0][change] = SID
    elif change == 'category':
        data[1]['dimensions'][0]['category'] = '未定义类别'
    elif change == 'sections':
        data[1]['dimensions'][0]['sections'] = [{'label': '不能放绑定', 'items': []}]
    else:
        data[0]['dimensions'][0]['id'] = 'd-unreserved'
    monkeypatch.setattr(templates, 'TEMPLATE_DATA', data)
    with pytest.raises((HTTPException, ValueError)):
        templates.template_list()


def test_empty_draft_has_server_generated_ids_and_never_persists(api):
    before = read_dimensions(api.session)
    first, second = apply(api), apply(api)
    assert len(first['dimensions']) == 8 and first['revision'] == 0
    assert first['template'] == {'id': 'textile-chain', 'name': '纺织产业链'}
    assert not first['warnings']
    validate_dimension_list(first['dimensions'])
    assert {dim['id'] for dim in first['dimensions']}.isdisjoint(dim['id'] for dim in second['dimensions'])
    assert all(dim['id'].startswith('d-') and not dim.get('template_extra') for dim in first['dimensions'])
    assert all(not section['items'] and section['id'].startswith('s-')
               for dim in first['dimensions'] for section in dim['sections'])
    assert read_dimensions(api.session) == before and board.revision(api.session) == 0
    assert api.session.get(SourceMapping, mapping_key(api.session, DIMENSIONS_KEY)) is None
    general = apply(api, 'general-six')
    assert [{key: dim[key] for key in ('id', 'label', 'category', 'description')}
            for dim in general['dimensions']] == default_dimensions()
    assert all(not dim['sections'] for dim in general['dimensions'])


def test_existing_id_and_same_name_preserve_sections_items_and_chart_revisions(api):
    identifier = add_chart(api)
    sections = [
        {'id': 's-old', 'label': '现货基差', 'items': [{'kind': 'indicator', 'id': SID}]},
        {'id': 's-private', 'label': '研究员自有环节', 'items': [{'kind': 'chart', 'id': identifier, 'revision': 1}]},
    ]
    existing = save(api, [custom('价格与价差', sections=sections), custom('其他观察', identifier='d-other')])
    stored = read_dimensions(api.session)
    result = apply(api)
    price = result['dimensions'][0]
    assert price['id'] == existing['dimensions'][0]['id']
    assert [section['label'] for section in price['sections']][:5] == [
        '郑棉/美棉价格', '内外价差', '月间价差', '现货基差', '进口利润']
    assert price['sections'][3]['id'] == 's-old'
    assert price['sections'][3]['items'][0]['id'] == SID
    assert price['sections'][-1]['id'] == 's-private'
    assert price['sections'][-1]['items'][0]['revision'] == 1
    assert result['dimensions'][-1]['label'] == '其他观察' and result['dimensions'][-1]['template_extra']
    assert result['revision'] == 1 and read_dimensions(api.session) == stored
    persisted = save(api, result['dimensions'], 1)
    assert persisted['revision'] == 2
    assert persisted['dimensions'][0]['id'] == price['id']
    assert not any('template_extra' in dim for dim in read_dimensions(api.session))


def test_general_matches_renamed_ids_and_same_names_without_orphaning_references(api):
    first = save(api, [custom('价格与价差')])
    row = active(api, first['dimensions'])
    result = apply(api, 'general-six')
    assert result['dimensions'][0]['id'] == first['dimensions'][0]['id']
    assert result['dimensions'][0]['references']['total'] == 2
    saved = save(api, result['dimensions'], 1)
    assert saved['dimensions'][0]['id'] == first['dimensions'][0]['id']
    assert api.session.get(type(row), row.id).contradiction['bull']['evidence'][0]['dimension'] == saved['dimensions'][0]['id']
    renamed = deepcopy(saved['dimensions'])
    renamed[1]['label'] = '研究员改名供给'
    renamed[1]['sections'] = [{'id': 's-original', 'label': '已有环节', 'items': []}]
    save(api, renamed, 2)
    next_draft = apply(api, 'general-six')
    assert next_draft['dimensions'][1]['id'] == 'supply'
    assert next_draft['dimensions'][1]['sections'][0]['id'] == 's-original'


def test_id_matches_are_reserved_globally_before_name_matches():
    # The price template must not consume the supply id just because its
    # researcher-edited label happens to match the earlier price dimension.
    previous = [custom('价格与价差', identifier='supply', sections=[
        {'id': 's-original', 'label': '原供给环节', 'items': []}
    ])]
    result = templates.merge_template('general-six', previous)
    assert result[0]['id'] == 'price' and result[1]['id'] == 'supply'
    assert result[1]['sections'][0]['id'] == 's-original'
    assert len(result) == 6
    assert len({dim['id'] for dim in result}) == 6


def test_unmatched_referenced_dimension_stays_until_explicit_delete_confirmation(api):
    existing = save(api, [custom()])
    row = active(api, existing['dimensions'])
    original = deepcopy(row.contradiction)
    result = apply(api, 'general-six')
    extra = result['dimensions'][-1]
    assert extra['id'] == existing['dimensions'][0]['id']
    assert extra['template_extra'] and extra['references']['total'] == 2
    dimensions = result['dimensions'][:-1]
    with pytest.raises(HTTPException) as error:
        save(api, dimensions, 1)
    assert error.value.status_code == 409
    assert error.value.detail['reason'] == 'dimension_referenced'
    assert error.value.detail['references'] == {extra['id']: 2}
    assert board.revision(api.session) == 1
    saved = save(api, dimensions, 1, confirm=[extra['id']])
    assert [dim['id'] for dim in saved['dimensions']] == [dim['id'] for dim in default_dimensions()]
    assert api.session.get(type(row), row.id).contradiction == original


def test_saved_six_dimension_config_can_be_merged_without_losing_extra_dimensions(api):
    save(api, config([{'kind': 'indicator', 'id': SID}]))
    result = apply(api)
    assert len(result['dimensions']) == 13
    assert len([dim for dim in result['dimensions'] if dim.get('template_extra')]) == 5
    assert result['warnings'] and '12' in result['warnings'][0]
    assert result['dimensions'][0]['sections'][-1]['items'][0]['id'] == SID
    assert board.revision(api.session) == 1
    with pytest.raises(ValidationError):
        board.DimensionConfig(revision=1, dimensions=result['dimensions'])


def test_merge_keeps_all_old_sections_even_when_review_exceeds_saved_limit(api):
    sections = [{'id': f's-old-{index}', 'label': f'原环节{index}', 'items': []} for index in range(8)]
    existing = save(api, [custom('价格与价差', sections=sections)])
    result = apply(api)
    assert len(result['dimensions'][0]['sections']) == 13
    assert {section['id'] for section in existing['dimensions'][0]['sections']} <= {
        section['id'] for section in result['dimensions'][0]['sections']}
    assert any('8个环节' in warning for warning in result['warnings'])
    with pytest.raises(HTTPException) as error:
        save(api, result['dimensions'], 1)
    assert error.value.status_code == 422
    assert board.revision(api.session) == 1


def test_current_unsaved_edits_are_used_and_client_ids_are_not_trusted(api):
    draft = [custom('自定义草稿', identifier='client-chosen', sections=[
        {'id': 's-draft', 'label': '编辑中的环节', 'items': [{'kind': 'indicator', 'id': SID}]}
    ])]
    result = apply(api, dimensions=draft)
    extra = result['dimensions'][-1]
    assert extra['label'] == '自定义草稿' and extra['id'] != 'client-chosen'
    assert extra['template_extra'] and extra['sections'][0]['items'][0]['id'] == SID
    assert board.revision(api.session) == 0


@pytest.mark.parametrize('permission', ['viewer', 'restricted', 'module', 'scope_version'])
def test_template_authorization_and_read_only_listing(api, permission):
    scope = api.scope
    if permission == 'viewer':
        scope = replace(scope, metadata={**scope.metadata, 'can_edit': False})
    elif permission == 'restricted':
        scope = replace(scope, metadata={**scope.metadata, 'can_read_source': False})
    elif permission == 'module':
        scope = replace(scope, view_modules=('market',))
    else:
        scope = replace(scope, scope_version=1)
    marker = current_workbench.set(scope)
    try:
        with pytest.raises(HTTPException) as error:
            apply(api)
        assert error.value.status_code == (409 if permission == 'scope_version' else 403)
        if permission == 'viewer':
            assert len(board.get_templates()['templates']) == 2
        else:
            with pytest.raises(HTTPException):
                board.get_templates()
    finally:
        current_workbench.reset(marker)


def test_template_revision_conflict_and_unknown_id_do_not_mutate_config(api):
    save(api, config())
    before = read_dimensions(api.session)
    with pytest.raises(HTTPException) as error:
        apply(api, revision=0)
    assert error.value.status_code == 409 and error.value.detail['reason'] == 'revision_conflict'
    with pytest.raises(HTTPException) as error:
        apply(api, 'unknown-template')
    assert error.value.status_code == 404
    assert read_dimensions(api.session) == before and board.revision(api.session) == 1


def test_http_template_routes_validate_request_and_return_unsaved_draft(api):
    app = FastAPI()
    app.include_router(board.router)
    with TestClient(app) as client:
        assert client.get('/api/sn/dimensions/templates').status_code == 200
        response = client.post('/api/sn/dimensions/templates/textile-chain/draft', json={'revision': 0})
        assert response.status_code == 200
        assert len(response.json()['dimensions']) == 8
        assert response.json()['revision'] == 0
        assert client.post('/api/sn/dimensions/templates/general-six/draft', json={'revision': -1}).status_code == 422
        assert client.post('/api/sn/dimensions/templates/general-six/draft', json={'revision': 0, 'unexpected': True}).status_code == 422
        assert client.post('/api/sn/dimensions/templates/general-six/draft', json={'revision': '0'}).status_code == 422
    assert board.revision(api.session) == 0
