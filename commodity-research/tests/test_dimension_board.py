"""Shared dimension configuration, identity boundaries and optimistic locking."""
from contextlib import contextmanager
from copy import deepcopy
from dataclasses import replace
from datetime import date, datetime, timezone
from fastapi import HTTPException, Request
import pytest
from tin.integration import page_facts
from tin.integration.workbench_scope import current_workbench
from tin.models import Judgment, SourceMapping
from tin.ownership import current_researcher
from tin.web import dimension_board as board, charts
from tin.web.research_dimensions import DIMENSIONS_KEY, default_dimensions, read_dimensions
from tin.web.restricted_views import mapping_key
from tests.test_core_focus import focus, add_chart
from tests.chart_fixtures import SID, OTHER


@pytest.fixture
def api(focus, monkeypatch):
    focus.scope.metadata['name'] = '隔离测试研究台'
    focus.scope = replace(focus.scope, view_modules=('market', 'indicators', 'judgments'))
    marker = current_workbench.set(focus.scope)
    @contextmanager
    def storage(scope):
        yield focus.session
    monkeypatch.setattr(board, 'artifact_session', storage)
    monkeypatch.setattr('tin.web.chart_data.catalog_fields', lambda scope, ids: {
        key: value for key, value in focus.session.info['knowledge_facts_catalog'].items() if key in ids})
    yield focus
    current_workbench.reset(marker)


def config(items=None):
    dims = default_dimensions()
    dims[0]['sections'] = [{'id': 's-a1', 'label': '环节', 'items': items or []}]
    return dims


def save(api, dimensions, revision=0, confirm=()):
    return board.put_config(board.DimensionConfig(dimensions=dimensions, revision=revision, confirm_delete=list(confirm)))


def active(api, dims):
    researcher = current_researcher(api.session)
    row = Judgment(variety='SN', version=1, author='fixture', researcher_id=researcher.id,
        written_at=date(2026, 9, 18), review_due=date(2026, 10, 18),
        contradiction={'bull': {'evidence': [{'desc': '支持', 'dimension': dims[0]['id']}, {'desc': '旧依据'}]},
                       'bear': {'evidence': [{'desc': '反对', 'dimension': dims[0]['id']}, {'desc': '删除', 'dimension': 'd-deleted'}]}},
        marginal_focus=[], pricing_power={}, whitelist=[], thresholds=[], falsifiers=[],
        status='生效', created_at=datetime.now(timezone.utc))
    api.session.add(row)
    api.session.commit()
    return row


def test_defaults_order_names_and_no_automatic_persistence(api):
    value = board.get_config()
    assert [d['id'] for d in value['dimensions']] == ['price', 'supply', 'demand', 'inventory', 'balance', 'macro']
    assert value['dimensions'][0]['label'] == '价格与价差'
    assert value['revision'] == 0 and value['can_write']
    assert api.session.get(SourceMapping, mapping_key(api.session, DIMENSIONS_KEY)) is None


def test_custom_ids_server_generated_rename_stable_order_and_revision(api):
    dims = config([{'kind': 'indicator', 'id': SID}])
    dims.insert(0, {'id': 'client-do-not-trust', 'label': '产业链', 'category': '其他', 'description': '研究员定义', 'sections': []})
    first = save(api, dims)
    identifier = first['dimensions'][0]['id']
    assert identifier.startswith('d-') and len(identifier) == 10
    assert first['revision'] == 1
    renamed = deepcopy(first['dimensions'])
    renamed[0]['label'] = '研究员改名'
    second = save(api, renamed, 1)
    assert second['dimensions'][0]['id'] == identifier
    assert second['revision'] == 2
    assert read_dimensions(api.session)[0]['label'] == '研究员改名'
    assert second['dimensions'][1]['sections'][0]['items'][0]['name']
    with pytest.raises(HTTPException) as error: save(api, renamed, 1)
    assert error.value.status_code == 409 and error.value.detail['reason'] == 'revision_conflict'


def test_referenced_delete_needs_confirmation_and_preserves_evidence(api):
    dims = config()
    row = active(api, dims)
    before = deepcopy(row.contradiction)
    value = board.get_config()
    assert value['dimensions'][0]['references'] == {'support': 1, 'oppose': 1, 'total': 2}
    assert value['evidence']['unassigned']['total'] == 1 and value['evidence']['deleted']['total'] == 1
    with pytest.raises(HTTPException) as error: save(api, dims[1:])
    assert error.value.status_code == 409 and error.value.detail['references'] == {'price': 2}
    assert save(api, dims[1:], confirm=['price'])['revision'] == 1
    assert api.session.get(Judgment, row.id).contradiction == before
    assert board.get_config()['evidence']['deleted']['total'] == 3


@pytest.mark.parametrize('mutation', ['sections', 'items', 'total', 'duplicate', 'revision', 'label', 'category', 'section_id'])
def test_limits_duplicates_and_shared_contract(api, mutation):
    dims = config([{'kind': 'indicator', 'id': SID}])
    section = dims[0]['sections'][0]
    if mutation == 'sections': dims[0]['sections'] = [{'id': f's-{i}', 'label': str(i), 'items': []} for i in range(9)]
    elif mutation == 'items': section['items'] = [{'kind': 'indicator', 'id': 'kb-' + f'{i:048x}'} for i in range(13)]
    elif mutation == 'total':
        dims[0]['sections'] = [{'id': f's-{i}', 'label': str(i), 'items': [
            {'kind': 'indicator', 'id': 'kb-' + f'{i * 12 + j:048x}'} for j in range(12)]} for i in range(8)]
        dims[1]['sections'] = [{'id': 's-more', 'label': 'extra', 'items': [{'kind': 'indicator', 'id': 'kb-' + f'{100:048x}'}]}]
    elif mutation == 'duplicate': dims[1]['sections'] = [{'id': 's-two', 'label': 'other', 'items': deepcopy(section['items'])}]
    elif mutation == 'revision': section['items'] = [{'kind': 'chart', 'id': 'some-chart'}]
    elif mutation == 'label': dims[1]['label'] = dims[0]['label']
    elif mutation == 'category': dims[0]['category'] = '不存在'
    else: dims[1]['sections'] = [deepcopy(section)]
    with pytest.raises(HTTPException): save(api, dims)
    assert board.revision(api.session) == 0


def test_chart_revision_and_missing_items_remain_visible(api):
    identifier = add_chart(api)
    dims = config([{'kind': 'chart', 'id': identifier, 'revision': 1}, {'kind': 'indicator', 'id': SID}])
    value = save(api, dims)
    assert value['dimensions'][0]['sections'][0]['items'][0]['name'].startswith('任意图表')
    charts.load_artifact(api.session, identifier).status = 'deleted'
    api.session.commit()
    del api.session.info['knowledge_facts_catalog'][SID]
    result = board.get_config()
    items = result['dimensions'][0]['sections'][0]['items']
    assert len(items) == 2 and all(item['status'] == 'missing' and item['reason'] for item in items)
    result['dimensions'][0]['label'] = '保留失效引用'
    assert save(api, result['dimensions'], 1)['revision'] == 2
    assert len(read_dimensions(api.session)[0]['sections'][0]['items']) == 2


def test_new_unavailable_binding_is_rejected(api):
    with pytest.raises(HTTPException) as error: save(api, config([{'kind': 'indicator', 'id': 'kb-' + 'f' * 48}]))
    assert error.value.status_code == 422


def test_config_card_prefers_readable_name_and_retains_sheet_without_catalog_changes(api):
    field = api.session.info['knowledge_facts_catalog'][SID]
    field.update(name='目录可读指标 / ID123456 [SOURCE.EXAMPLE.value]', metric='原始表头', sheet='来源工作表')
    result = save(api, config([{'kind': 'indicator', 'id': SID}]))
    card = result['dimensions'][0]['sections'][0]['items'][0]
    assert card['name'] == '目录可读指标' and card['sheet'] == '来源工作表'
    assert field['name'] == '目录可读指标 / ID123456 [SOURCE.EXAMPLE.value]'
    assert field['metric'] == '原始表头'


@pytest.mark.parametrize('permission', ['viewer', 'restricted', 'module'])
def test_read_write_authorization(api, permission):
    scope = api.scope
    if permission == 'viewer': scope = replace(scope, metadata={**scope.metadata, 'can_edit': False})
    elif permission == 'restricted': scope = replace(scope, metadata={**scope.metadata, 'can_read_source': False})
    else: scope = replace(scope, view_modules=('market',))
    token = current_workbench.set(scope)
    try:
        with pytest.raises(HTTPException) as error: save(api, config())
        assert error.value.status_code == 403
        if permission == 'viewer': assert board.get_config()['can_write'] is False
        else:
            with pytest.raises(HTTPException): board.get_config()
    finally: current_workbench.reset(token)


def test_data_route_preserves_date_period_revision_and_evidence(api, monkeypatch):
    import tin.web.dimension_data as data
    captured = []
    def calculate(scope, dims, day, period):
        captured.append((day, period))
        return {'as_of': '2026-09-18', 'period': period, 'dimensions': dims, 'failed': []}
    monkeypatch.setattr(data, 'dimension_data', calculate)
    assert board.get_data('2026-09-17', 'weekly')['revision'] == 0
    assert captured == [(date(2026, 9, 17), 'weekly')]
    assert board.get_data()['evidence']['unassigned']['total'] == 0
    assert captured[-1] == (None, 'weekly')
    for value in ['2026-9-1', 'not-date']:
        with pytest.raises(HTTPException): board.get_data(value)
    with pytest.raises(HTTPException): board.get_data(period='monthly')


def test_atomic_revision_conflict_even_if_precheck_raced(api, monkeypatch):
    value = save(api, config())
    real_execute = api.session.execute
    def race(statement, *args, **kwargs):
        from sqlalchemy.sql.dml import Update
        if isinstance(statement, Update) and statement.table.name == 'source_mappings': return type('Conflict', (), {'rowcount': 0})()
        return real_execute(statement, *args, **kwargs)
    monkeypatch.setattr(api.session, 'execute', race)
    with pytest.raises(HTTPException) as error: save(api, value['dimensions'], 1)
    assert error.value.status_code == 409


def test_restricted_page_reads_only_persisted_ids_and_hides_charts(api, monkeypatch):
    from tin.web import app as web
    identifier = add_chart(api)
    save(api, config([{'kind': 'indicator', 'id': SID}, {'kind': 'chart', 'id': identifier, 'revision': 1}]))
    calls = []
    def bounded(scope, ids, module, **kwargs):
        calls.append((ids, module, kwargs))
        return {'indicators': [{'series_id': SID, 'metric': '授权观测', 'unit': '吨'}],
                'rows': [{'series_id': SID, 'date': '2026-09-17', 'value': 1}, {'series_id': SID, 'date': '2026-09-18', 'value': 2}]}
    monkeypatch.setattr(page_facts, 'bounded_facts', bounded)
    request = Request({'type': 'http', 'method': 'GET', 'path': '/sn/dimensions', 'headers': [], 'query_string': b''})
    scope = replace(api.scope, metadata={**api.scope.metadata, 'can_read_source': False})
    response = board.restricted_page(request, scope, web.templates)
    assert calls[0][0] == [SID] and calls[0][1] == 'indicators'
    page = response.context['dimension_page']
    assert page['restricted'] and not page['can_write']
    assert page['data']['dimensions'][0]['sections'][0]['items'][0]['latest'] == 2
    assert page['data']['dimensions'][0]['sections'][0]['items'][1]['status'] == 'unavailable'


def test_restricted_page_update_window_uses_its_report_date(api, monkeypatch):
    from tin.web import app as web
    save(api, config([{'kind': 'indicator', 'id': SID}, {'kind': 'indicator', 'id': OTHER}]))
    def bounded(*args, **kwargs):
        return {'indicators': [{'series_id': SID, 'metric': '旧表头', 'name': '可读指标', 'sheet': '授权工作表'},
                               {'series_id': OTHER, 'metric': '第二指标'}],
                'rows': [{'series_id': SID, 'date': '2024-03-20', 'value': 1},
                         {'series_id': SID, 'date': '2024-03-28', 'value': 2},
                         {'series_id': OTHER, 'date': '2024-03-28', 'value': 3},
                         {'series_id': OTHER, 'date': '2024-03-29', 'value': 3}]}
    monkeypatch.setattr(page_facts, 'bounded_facts', bounded)
    request = Request({'type': 'http', 'method': 'GET', 'path': '/sn/dimensions', 'headers': [], 'query_string': b''})
    scope = replace(api.scope, metadata={**api.scope.metadata, 'can_read_source': False})
    page = board.restricted_page(request, scope, web.templates).context['dimension_page']['data']
    assert page['as_of'] == '2024-03-29'
    dimension = page['dimensions'][0]
    old, new = dimension['sections'][0]['items']
    assert old['direction'] == 'no_update' and old['change'] == 1
    assert old['comparison_start_date'] == '2024-03-28'
    assert old['name'] == '可读指标' and old['sheet'] == '授权工作表'
    assert new['direction'] == 'flat'
    assert dimension['summary']['no_update'] == dimension['summary']['flat'] == 1


def test_counts_do_not_read_unauthorized_judgments(api):
    active(api, config())
    limited = replace(api.scope, view_modules=('indicators',))
    counts, extra = board.evidence_counts(api.session, default_dimensions(), limited)
    assert all(value['total'] == 0 for value in counts.values())
    assert extra['unassigned']['total'] == 0


def test_counts_reject_foreign_workbench_registry(api):
    from tin.web.research_references import remember_record_scope
    row = active(api, config())
    original = api.session.info['workbench_id']
    api.session.info['workbench_id'] = '10000000-0000-4000-8000-000000000092'
    remember_record_scope(api.session, 'judgment', row.id)
    api.session.info['workbench_id'] = original
    api.session.commit()
    assert board.get_config()['dimensions'][0]['references']['total'] == 0
