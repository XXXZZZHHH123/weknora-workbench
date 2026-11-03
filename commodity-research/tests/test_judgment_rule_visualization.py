"""Synthetic read-only radar geometry and bounded preview observations."""
import asyncio
from contextlib import contextmanager
from copy import deepcopy
from dataclasses import replace
from datetime import date as Date, datetime
import re
from types import SimpleNamespace
from urllib.parse import urlencode

from fastapi import HTTPException, Request
import httpx
import pytest
from sqlalchemy import event, func, select
from sqlalchemy.orm import sessionmaker
from sqlalchemy.pool import StaticPool
from jinja2 import ChoiceLoader, DictLoader

from tin.config import SHANGHAI
from tin.db import Base, make_engine
from tin.export.board import threshold_radar_values
from tin.integration.identity import Identity
from tin.integration.page_facts import _page
from tin.integration.workbench_scope import WorkbenchScope
from tin.judgments.analysis import assess_draft
from tin.models import AuditLog, Indicator, Judgment, Observation, Signal, SourceMapping
from tin.schemas.judgment import JudgmentIn
from tin.web import judgment_generation as generation
from tin.web import judgment_radar as radar
from tin.web import judgment_review as review

SID = 'kb-' + 'a' * 48
OTHER = 'kb-' + 'b' * 48
TOKEN = 'synthetic-preview-opaque-token'
SOURCE = 'Synthetic stock above 10 kg requires review.'


def condition(identifier='boundary', **values):
    return {'id': identifier, 'name': 'Synthetic condition', 'series_id': 'kb-' + 'a' * 48,
        'operator': '>', 'value': 10, 'type': '观察位', 'horizon': '短期', **values}


def test_negative_actual_value_clamps_only_display_width_without_changing_hit():
    rule = condition()
    rows = threshold_radar_values(SimpleNamespace(thresholds=[rule]), {rule['series_id']: {
        'name': 'Synthetic amount', 'unit': 'kg', 'date': '2026-01-02', 'value': -5}})
    assert rows[0]['value'] == -5 and rows[0]['state'] == '未触发'
    assert rows[0]['progress'] == 0


@pytest.mark.parametrize('operator,target,value,hit,progress', [
    ('>', -10, -5, True, 100),
    ('<', -10, -20, True, 100),
    ('>=', -10, -10, True, 100),
    ('<=', -10, -10, True, 100),
    ('>', 0, 1, True, 100),
    ('<', 0, -1, True, 100),
    ('>=', 0, 0, True, 100),
    ('<=', 0, 0, True, 100),
    ('>', -10, -12, False, 80),
    ('<', -10, -5, False, 50),
    ('>=', -10, -11, False, 90),
    ('<=', -10, -9, False, 90),
    ('>', 0, -0.25, False, 75),
    ('<', 0, 0.25, False, 0),
    ('>', 10, 8, False, 80),
    ('<', 10, 12, False, 100 * (1 - 2 / 12)),
    ('>', -10, -10, False, 100),
    ('<', 0, 0, False, 100),
])
def test_scalar_radar_uses_gap_display_for_negative_and_zero_boundaries(
        operator, target, value, hit, progress):
    rule = condition(operator=operator, value=target)
    row = threshold_radar_values(SimpleNamespace(thresholds=[rule]), {rule['series_id']: {
        'name': 'Synthetic signed quantity', 'unit': 'kg', 'date': '2026-01-02', 'value': value}})[0]
    assert row['value'] == value
    assert row['state'] == ('已触发' if hit else '未触发')
    assert row['progress'] == pytest.approx(progress)
    assert 0 <= row['progress'] <= 100


def test_range_and_missing_values_keep_deterministic_geometry_and_explicit_gaps():
    rule = condition(operator='between', value=[10, 20])
    rows = threshold_radar_values(SimpleNamespace(thresholds=[rule]), {rule['series_id']: {
        'name': 'Synthetic amount', 'unit': 'kg', 'date': '2026-01-02', 'value': 15}})
    assert rows[0]['state'] == '在区间内' and rows[0]['progress'] == 100
    missing = threshold_radar_values(SimpleNamespace(thresholds=[rule]), {})[0]
    assert missing['state'] == '数据缺失' and missing['value'] is None and missing['as_of'] is None


@pytest.fixture
def visual(monkeypatch):
    engine = make_engine('sqlite://', poolclass=StaticPool, connect_args={'check_same_thread': False})
    Base.metadata.create_all(engine)
    factory = sessionmaker(engine, expire_on_commit=False)
    with factory() as session:
        for identifier in (SID, OTHER):
            session.add(Indicator(series_id=identifier, name='Synthetic ' + identifier[-1],
                variety='XY', category='fixture', caliber={}, unit='kg', source='Synthetic',
                frequency='日', fetch_mode='readonly'))
        session.flush()
        for identifier in (SID, OTHER):
            for day, value in ((1, 7), (2, 8)):
                stamp = datetime(2026, 1, day, 15, tzinfo=SHANGHAI)
                session.add(Observation(series_id=identifier, value=value, as_of=stamp,
                    fetched_at=stamp, caliber_snapshot={}, source='Synthetic', revision=0, status='正常'))
        session.commit()
    scope = WorkbenchScope(Identity('synthetic-user', 'synthetic@test.invalid', 'Synthetic', 61, 'contributor'),
        '10000000-0000-4000-8000-000000000661', 'synthetic-kb', 61,
        {'workspace_mode': 'isolated', 'can_edit': True, 'can_read_source': True},
        'Bearer synthetic', scope_version=2, sources_revision=1)
    catalog = [{'id': identifier, 'name': 'Synthetic ' + identifier[-1], 'unit': 'kg',
        'knowledge_base_id': 'synthetic-kb'} for identifier in (SID, OTHER)]
    document = {'judgment': {'contradiction': {}, 'thresholds': [condition()],
        'marginal_focus': [{'desc': 'Optional synthetic observation', 'series_id': OTHER}]},
        'binding_units': {SID: 'kg', OTHER: 'kg'}, 'rule_quotes': {'boundary': 'Synthetic stock above 10 kg'},
        'unresolved': []}
    assessment = assess_draft(document, SOURCE, catalog, variety='XY', actor='synthetic-user')
    preview = {'text': SOURCE, 'variety': 'XY', 'written_at': '2026-01-02'}
    review.initialize_review(preview, assessment, catalog)
    case = SimpleNamespace(scope=scope, catalog=catalog, preview=preview, factory=factory,
        reads=[], currents=0, changed=False, denied=False, mutations=[])

    async def current_preview(current, token):
        assert current == case.scope and token == TOKEN
        case.currents += 1
        if case.denied:
            raise HTTPException(403, 'Synthetic permission revoked')
        value = deepcopy(case.preview)
        if case.changed and case.currents > 1:
            value['review']['revision'] += 1
        return value, {'catalog': case.catalog}

    @contextmanager
    def projected(current_engine, current):
        plan = deepcopy(_page.get())
        assert plan['module'] == 'judgments'
        case.reads.append(plan)
        with factory() as session:
            yield session

    def sql(connection, cursor, statement, parameters, context, executemany):
        if statement.lstrip().split(' ', 1)[0].upper() in {'INSERT', 'UPDATE', 'DELETE'}:
            case.mutations.append(statement)

    event.listen(engine, 'before_cursor_execute', sql)
    monkeypatch.setattr(generation, 'scope_for_analysis', lambda write=False: case.scope)
    monkeypatch.setattr(generation, 'result_session', lambda scope: factory())
    monkeypatch.setattr(review, 'current_preview', current_preview)
    monkeypatch.setattr(radar, 'projected_page_session', projected)
    async def forbidden(*args, **kwargs):
        pytest.fail('A read-only chart called the model')
    monkeypatch.setattr(generation, 'completion', forbidden)
    yield case
    engine.dispose()


def request(path, fields):
    return Request({'type': 'http', 'method': 'GET', 'path': path,
        'query_string': urlencode(fields).encode(), 'headers': []})


def get_radar(case, *, date='2026-01-02', **extra):
    fields = {'draft_id': TOKEN, **({'date': date} if date is not None else {}), **extra}
    return asyncio.run(radar.radar(request('/radar', fields), draft_id=TOKEN, date=date))


def get_indicators(case, ids, *, date=None, **extra):
    fields = {'draft_id': TOKEN, 'item_ids': ids, **({'date': date} if date is not None else {}), **extra}
    return asyncio.run(radar.indicator_values(request('/indicator-values', fields),
        draft_id=TOKEN, item_ids=ids, date=date))


def test_preview_radar_reads_only_verified_rule_ids_and_one_actual_point_without_writes(visual):
    result = get_radar(visual)
    assert len(result['rows']) == 1
    row = result['rows'][0]
    assert row['id'] == 'boundary' and row['value'] == 8 and row['progress'] == 80
    item = next(item for item in visual.preview['review']['items'] if item['kind'] == 'thresholds')
    assert row.get('item_id') == item['id']
    assert row['state'] == '未触发' and row['unit'] == 'kg' and row['as_of']
    assert visual.reads[0]['series_ids'] == [SID] and visual.reads[0]['points'] == 1
    assert visual.currents == 2 and visual.mutations == []
    with visual.factory() as session:
        for model in (AuditLog, Judgment, Signal, SourceMapping):
            assert session.scalar(select(func.count()).select_from(model)) == 0


def test_text_only_rules_do_not_fetch_observations_or_make_placeholder_progress(visual):
    item = next(item for item in visual.preview['review']['items'] if item['kind'] == 'thresholds')
    item.update(status='kept_text', series_id=None)
    visual.preview['document'] = review.build_document(visual.preview, visual.catalog)
    assert get_radar(visual)['rows'] == [] and visual.reads == []


def test_invalid_numeric_document_is_rejected_before_any_observation_read(visual):
    visual.preview['document']['judgment']['thresholds'][0]['value'] = 0
    with pytest.raises(HTTPException) as rejected:
        get_radar(visual)
    assert rejected.value.status_code == 422 and visual.reads == []


def test_missing_observation_is_explicit_none_with_an_empty_track(visual):
    with visual.factory() as session:
        session.query(Observation).filter(Observation.series_id == SID).delete()
        session.commit()
    visual.mutations.clear()
    row = get_radar(visual)['rows'][0]
    assert row['state'] == '数据缺失' and row['value'] is None and row['as_of'] is None
    assert row['progress'] == 0 and visual.mutations == []


@pytest.mark.parametrize('extra', [{'series_id': OTHER}, {'series_ids': OTHER}])
def test_preview_chart_cannot_accept_arbitrary_client_series_ids(visual, extra):
    with pytest.raises(HTTPException) as rejected:
        get_radar(visual, **extra)
    assert rejected.value.status_code == 422 and visual.reads == []


def test_changed_review_and_revoked_permissions_never_return_stale_geometry(visual):
    visual.changed = True
    with pytest.raises(HTTPException) as stale:
        get_radar(visual)
    assert stale.value.status_code == 409
    visual.currents = 0
    visual.reads.clear()
    visual.denied = True
    with pytest.raises(HTTPException) as revoked:
        get_radar(visual)
    assert revoked.value.status_code == 403 and visual.reads == []


def test_indicator_values_use_visible_opaque_items_and_never_other_bound_fields(visual):
    item = next(item for item in visual.preview['review']['items'] if item['kind'] == 'binding')
    result = get_indicators(visual, item['id'])
    assert [field['series_id'] for field in result['series']] == [OTHER]
    assert result['series'][0]['latest_value'] == 8 and len(result['series'][0]['points']) == 2
    assert visual.reads[0]['series_ids'] == [OTHER] and visual.reads[0]['points'] == 24
    assert visual.currents == 2 and visual.mutations == []


def test_empty_binding_or_text_only_item_does_not_silently_read_its_ai_recommendation(visual):
    item = next(item for item in visual.preview['review']['items'] if item['kind'] == 'binding')
    item.update(status='kept_text', series_id=None)
    visual.preview['document'] = review.build_document(visual.preview, visual.catalog)
    assert item['ai_series_id'] == OTHER
    assert get_indicators(visual, item['id'])['series'] == [] and visual.reads == []


@pytest.mark.parametrize('identifiers', ['not-a-review-item', ','.join('opaque-' + str(i) for i in range(25))])
def test_unknown_or_excessive_review_item_selection_is_rejected(visual, identifiers):
    with pytest.raises(HTTPException) as rejected:
        get_indicators(visual, identifiers)
    assert rejected.value.status_code in {404, 422} and visual.reads == []


def test_changed_observation_unit_cannot_make_a_different_unit_visualization(visual):
    with visual.factory() as session:
        session.get(Indicator, SID).unit = 'lb'
        session.commit()
    with pytest.raises(HTTPException) as rejected:
        get_radar(visual)
    assert rejected.value.status_code == 409


def test_legacy_indicator_values_use_two_point_selected_id_request_without_full_projection(visual, monkeypatch):
    from tin.integration import kb_facts
    from tin.provenance import bridge
    visual.scope = replace(visual.scope, scope_version=1)
    requests = []
    monkeypatch.setattr(kb_facts, 'current_sources', lambda scope: [])
    def bounded(method, path, body, **kwargs):
        requests.append(body)
        assert body['series_ids'] == [OTHER] and body['points_per_series'] == 2
        assert body['view_module'] == 'judgments' and 'offset' not in body and 'limit' not in body
        return httpx.Response(200, request=httpx.Request(method, 'http://synthetic.invalid' + path), json={
            'bounded': True, 'knowledge_base_id': 'synthetic-kb', 'knowledge_base_tenant_id': 61,
            'version': 'a' * 64, 'offset': 0, 'next_offset': None, 'truncated': False,
            'total': 2, 'indicators': [{'series_id': OTHER, 'metric': 'Synthetic b', 'unit': 'kg'}],
            'rows': [{'series_id': OTHER, 'date': '2026-01-01', 'value': 7, 'value_status': 'valid'},
                {'series_id': OTHER, 'date': '2026-01-02', 'value': 8, 'value_status': 'valid'}]})
    monkeypatch.setattr(bridge, 'structured_request', bounded)
    item = next(item for item in visual.preview['review']['items'] if item['kind'] == 'binding')
    result = get_indicators(visual, item['id'])
    assert len(requests) == 1 and visual.reads == []
    assert [point['value'] for point in result['series'][0]['points']] == [7, 8]


def test_indicator_history_uses_the_same_explicit_date_boundary_as_radar(visual):
    item = next(item for item in visual.preview['review']['items'] if item['kind'] == 'binding')
    result = get_indicators(visual, item['id'], date='2026-01-01')
    assert result['effective_date'] == '2026-01-01'
    assert result['series'][0]['latest_value'] == 7
    assert result['series'][0]['as_of'] == '2026-01-01'
    assert visual.reads[0]['day'] == '2026-01-01'


@pytest.mark.parametrize('date', ['2026-1-1', '2026-02-30', '9999-01-01', '2026-01-01T00:00:00'])
def test_indicator_date_boundary_rejects_noncanonical_invalid_and_future_dates(visual, date):
    item = next(item for item in visual.preview['review']['items'] if item['kind'] == 'binding')
    with pytest.raises(HTTPException) as rejected:
        get_indicators(visual, item['id'], date=date)
    assert rejected.value.status_code == 422 and visual.reads == []


def saved_document():
    payload = JudgmentIn(variety='XY', author='Synthetic', written_at=Date(2026, 1, 2),
        contradiction={}, thresholds=[condition()]).model_dump(mode='json')
    return SimpleNamespace(**payload, id=1, status='草稿', review_due=Date(2026, 2, 1))


def test_saved_page_keeps_registered_threshold_rows_without_an_observation_day(visual, monkeypatch):
    from tin.web import app as web
    @contextmanager
    def page(*args, **kwargs):
        with visual.factory() as session:
            yield session
    monkeypatch.setattr(web, '_page_session', page)
    monkeypatch.setattr(web, '_home_date', lambda *args: None)
    monkeypatch.setattr(web, 'current', lambda *args: saved_document())
    monkeypatch.setattr(web, 'research_variety', lambda *args: 'XY')
    monkeypatch.setattr(web, 'judgment_record_block', lambda *args: {'expired_days': 0, 'blockers': []})
    monkeypatch.setattr(web, '_shell', lambda session, nav, day, **extra: extra)
    monkeypatch.setattr(web.templates, 'TemplateResponse', lambda request, name, context: context)
    def no_invented_date(*args, **kwargs):
        pytest.fail('A page without an observation day invented a date for value lookup')
    monkeypatch.setattr(web, 'threshold_radar', no_invented_date)
    result = web._judgment_response(request('/sn/judgment', {}))
    assert len(result['radar']) == 1
    assert result['radar'][0]['state'] == '数据缺失'
    assert result['radar'][0]['value'] is None and result['radar'][0]['as_of'] is None


@pytest.mark.parametrize('value,progress,width', [(None, 95, None), (8, -25, '0.0'), (8, 125, '100.0')])
def test_saved_page_draws_only_observed_clamped_fill_and_missing_data_is_neutral(value, progress, width):
    from tin.web import app as web
    env = web.templates.env.overlay(loader=ChoiceLoader([
        DictLoader({'base.html': '{% block main %}{% endblock %}', '_judgment_manager.html': ''}),
        web.templates.env.loader]))
    env.globals = dict(web.templates.env.globals)
    env.globals.update(research_workbench=lambda: None, research_identity=lambda: None)
    row = threshold_radar_values(SimpleNamespace(thresholds=[condition()]), {})[0]
    row.update(value=value, progress=progress, state='数据缺失' if value is None else '未触发')
    html = env.get_template('judgment.html').render(j=saved_document(), names={},
        block={'expired_days': 0, 'blockers': []}, versions=[], radar=[row])
    if width is None:
        assert 'data-threshold-state="missing"' in html
        assert 'data-threshold-fill' not in html
    else:
        assert 'data-threshold-state="observed"' in html
        actual = re.search(r'data-threshold-fill[^>]*style="width: ([0-9.]+)%', html)
        assert actual is not None and float(actual.group(1)) == float(width)
