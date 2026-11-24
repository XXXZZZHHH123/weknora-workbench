"""Saved judgments can be edited against real catalog and observation responses."""
import asyncio
from copy import deepcopy
from datetime import datetime, timezone
from types import SimpleNamespace

import pytest
from sqlalchemy import select

from tests.test_judgment_analysis_jobs import scope_for_fixture
from tests.test_judgment_generation import endpoint, rows
from tests.test_judgment_review import all_items, update
from tests.test_knowledge_facts import FIRST, SERIES, canonical
from tin.integration.workbench_scope import isolated_session_factory, workbench_session
from tin.judgments.analysis import ALLOWED_FIELDS
from tin.judgments.service import save_version, to_payload
from tin.models import AuditLog, Judgment, SourceMapping
from tin.models.workbench import WorkbenchArtifact
from tin.schemas.judgment import JudgmentIn
from tin.web import judgment_generation as generation, judgment_recall as recall
from tin.web import judgment_quantity as quantity
from tin.web.judgment_panels import register_panel
from tin.web.judgment_review import digest
from tin.web.research_dimensions import DIMENSIONS_KEY, default_dimensions


OTHER_SERIES = 'kb-' + 'c' * 48
WATCH_QUOTE = 'Synthetic stock limit > 12 吨.'
SIBLING_QUOTE = 'Synthetic reserve limit < 13 吨.'
STOP_QUOTE = 'Synthetic stock falsifier >= 2 吨.'
SOURCE = '\n'.join((WATCH_QUOTE, SIBLING_QUOTE, STOP_QUOTE, 'Synthetic observed stocks.',
    'Synthetic seasonal evidence.', 'Synthetic cost basis.', 'Synthetic unresolved observation.'))


def judgment():
    return JudgmentIn.model_validate({
        'variety': 'SN', 'author': 'researcher', 'written_at': '2026-01-02',
        'source_note': SOURCE, 'review_period_days': 42,
        'contradiction': {
            'bull': {'claim': 'Synthetic supply claim', 'evidence': [
                {'desc': 'Synthetic observed stocks.', 'series_id': SERIES, 'note': 'Preserve this note.'},
                {'desc': 'Synthetic seasonal evidence.', 'dimension': 'supply',
                 'note': 'No matching observation was available.'}]},
            'bear': {'claim': 'Synthetic demand claim'},
            'window': {'from': '2026-01-01', 'to': '2026-06-30', 'note': 'Preserve this window.'}},
        'marginal_focus': [{'desc': 'Synthetic momentum.', 'series_id': SERIES}],
        'cost_line': {'type': 'cash', 'items': [{'desc': 'Synthetic cost basis.', 'series_id': SERIES}]},
        'whitelist': [SERIES], 'tone': '观望', 'survey_notes': 'Preserve this survey note.',
        'thresholds': [
            {'id': 'watch', 'name': 'Synthetic stock limit', 'series_id': SERIES,
             'operator': '>', 'value': 12, 'type': '观察位', 'horizon': '短期', 'note': 'Review this rule unit.'},
            {'id': 'reserve', 'name': 'Synthetic unchanged sibling rule', 'series_id': SERIES,
             'operator': '<', 'value': 13, 'type': '观察位', 'horizon': '短期'}],
        'priority_rule': {'order': ['watch', 'reserve']},
        'falsifiers': [{'id': 'stop', 'desc': 'Synthetic stock falsifier', 'series_id': SERIES,
            'expression': 'value >= 200', 'unit_confirmed': '吨', 'side': 'bull',
            'source_value': 2, 'scale': 100, 'value': 200}],
        'unstructured': [{'section': 'Observation', 'text': 'Synthetic unresolved observation.',
            'reason': 'Preserved as text'}],
    })


def item_id(*path):
    return 'item-' + digest(list(path))[:24]


@pytest.fixture
def saved_case(canonical, monkeypatch):
    client, headers, state, _ = canonical
    state.update(default_macro={'tenant_id': 17}, macro_value=1200)

    async def forbidden(*args, **kwargs):
        pytest.fail('The saved-judgment item edit called the model')

    monkeypatch.setattr(generation, 'completion', forbidden)
    monkeypatch.setattr(generation, '_analyze_scoped', forbidden)
    monkeypatch.setattr(recall, 'recommend', forbidden)
    monkeypatch.setattr(generation, 'previews', generation.PreviewStore())
    scope = asyncio.run(scope_for_fixture())
    with workbench_session(scope) as session:
        row = save_version(session, judgment(), actor='researcher')
        panel = register_panel(session, scope, row, 'Synthetic reviewed panel')
        watch_id = item_id('thresholds', 0)
        statuses = [
            (item_id('contradiction', 'bull', 'evidence', 0), 'confirmed'),
            (item_id('contradiction', 'bull', 'evidence', 1), 'pending'),
            (watch_id, 'confirmed'), (item_id('falsifiers', 0), 'confirmed'),
            (item_id('unresolved', 0), 'kept_text'),
        ]
        caliber = {'mode': 'catalog', 'unit': '吨', 'scope_note': 'Checked original unit.',
            'confirmed_by': 'researcher', 'confirmed_at': '2026-01-02T00:00:00+00:00',
            'catalog_version': session.info['knowledge_facts_version']}
        before = to_payload(row).model_dump(mode='json', by_alias=True)
        candidate = {'judgment': {key: before[key] for key in ALLOWED_FIELDS},
            'unresolved': before['unstructured'],
            'rule_quotes': {'watch': WATCH_QUOTE, 'reserve': SIBLING_QUOTE, 'stop': STOP_QUOTE},
            'binding_units': {SERIES: '吨'}}
        session.add(AuditLog(at=datetime.now(timezone.utc), actor='researcher',
            action='研究逻辑分析保存', target_type='judgment', target_id=str(row.id), detail={
                'judgment_id': row.id, 'version': row.version, 'workbench_id': FIRST,
                'rule_quotes': candidate['rule_quotes'], 'binding_units': candidate['binding_units'],
                'review_candidates': candidate,
                'review_status': [{'item_id': identifier, 'status': status} for identifier, status in statuses],
                'review': [{'item_id': watch_id, 'action': 'confirmed', 'series_id': SERIES,
                    'source_quote': WATCH_QUOTE, 'caliber': caliber}],
            }))
        session.commit()
        return SimpleNamespace(client=client, headers=headers, state=state, scope=scope, row=row,
            panel=panel, statuses=statuses, before=before)


def open_edit(case, *, headers=None, judgment_id=None, version=None):
    return case.client.post(endpoint('/review/edit'), headers=headers or case.headers,
        json={'judgment_id': judgment_id or case.row.id, 'expected_base_version': version or case.row.version})


def success(response):
    assert response.status_code == 200, response.text
    return response.json()


def save_edit(case, preview):
    return case.client.post(endpoint('/apply'), headers=case.headers, json={
        'draft_id': preview['draft_id'], 'base_version': preview['base_version'],
        'review_revision': preview['review']['revision']})


def items_by_id(preview):
    return {item['id']: item for item in all_items(preview)}


def confirm(case, preview, item, **fields):
    response = update(case.client, case.headers, preview, item, **fields)
    tokens = {'binding_warning': 'binding_warning_token', 'number_review': 'number_review_token'}
    for code, key in tokens.items():
        detail = response.json()['detail'] if response.status_code == 422 else None
        if isinstance(detail, dict) and detail.get('code') == code:
            fields[key] = detail['confirmation_token']
            response = update(case.client, case.headers, preview, item, **fields)
    return success(response)


def counts():
    with isolated_session_factory(17, FIRST)() as session:
        return {model: len(session.scalars(select(model)).all())
            for model in (AuditLog, WorkbenchArtifact)}


def test_open_restores_saved_review_and_unchanged_save_creates_no_version(saved_case):
    case = saved_case
    before_counts = counts()
    preview = success(open_edit(case))
    assert preview['item_edit'] is True and preview['review']['has_changes'] is False
    assert preview['diff']['fields'] == [] and preview['diff']['markdown'] == ''
    items = items_by_id(preview)
    assert {identifier: items[identifier]['status'] for identifier, _ in case.statuses} == dict(case.statuses)
    bound = items[item_id('contradiction', 'bull', 'evidence', 0)]
    assert bound['binding']['series_id'] == SERIES and bound['binding']['unit'] == '吨'
    assert items[item_id('contradiction', 'bull', 'evidence', 1)]['binding'] is None
    assert items[item_id('thresholds', 0)]['caliber']['confirmed'] is True
    assert items[item_id('falsifiers', 0)]['rule']['conversion_text'].endswith('按 ×100 换算）')
    assert items[item_id('cost_line', 'items', 0)]['binding']['series_id'] == SERIES
    assert items[item_id('survey_notes')]['description'] == 'Preserve this survey note.'
    rejected = save_edit(case, preview)
    assert rejected.status_code == 400 and '没有修改' in rejected.json()['detail']
    assert len(rows()) == 1 and counts() == before_counts
    assert to_payload(rows()[0]).model_dump(mode='json', by_alias=True) == case.before


@pytest.mark.parametrize(('observed_date', 'range_start'), [
    ('2026-01-02', '2023-01-02'), ('2023-09-22', '2020-09-22')])
def test_rebind_threshold_and_apply_observation_backed_conversion_in_same_panel(
        saved_case, monkeypatch, observed_date, range_start):
    case = saved_case
    today = datetime(2026, 11, 24)
    monkeypatch.setattr(quantity, 'datetime', SimpleNamespace(now=lambda _zone: today))
    case.state['bounded_row_metadata'] = {'date': observed_date}
    preview = success(open_edit(case))
    item = items_by_id(preview)[item_id('thresholds', 0)]
    diagnostic = success(case.client.get(endpoint('/review/quantity'), headers=case.headers, params={
        'draft_id': preview['draft_id'], 'item_id': item['id'], 'series_id': OTHER_SERIES, 'exponent': 2}))
    assert diagnostic['latest'] == {'date': observed_date, 'value': '1200'}
    assert diagnostic['range_start'] == range_start and diagnostic['range_end'] == observed_date
    assert diagnostic['converted_value'] == '1200' and diagnostic['converted_overlaps']
    assert [candidate['exponent'] for candidate in diagnostic['candidates']] == [2]

    changed = confirm(case, preview, item, series_id=OTHER_SERIES, caliber={'mode': 'catalog'},
        quantity_alignment={'mode': 'convert', 'exponent': 2})
    assert changed['review']['has_changes']
    unchanged = items_by_id(changed)
    unchanged.pop(item['id'])
    assert unchanged == {key: row for key, row in items_by_id(preview).items() if key != item['id']}
    assert [field['field'] for field in changed['diff']['fields']] == ['thresholds']
    saved = success(save_edit(case, changed))
    assert saved['version'] == case.row.version + 1 and saved['status'] == '草稿'
    assert saved['panel']['id'] == case.panel['id'] and saved['panel']['title'] == case.panel['title']
    listed = success(case.client.get(endpoint('/panels'), headers=case.headers))
    assert listed['total'] == 1 and listed['items'][0]['judgment_id'] == saved['id']

    original, current = rows()
    after = to_payload(current).model_dump(mode='json', by_alias=True)
    expected = deepcopy(case.before)
    expected['thresholds'][0].update(series_id=OTHER_SERIES, source_value=12, scale=100, value=1200, note=None)
    assert after == expected
    assert to_payload(original).model_dump(mode='json', by_alias=True) == case.before
    detail = success(case.client.get(endpoint('/versions/2'), headers=case.headers,
        params={'judgment_id': original.id}))
    for diff in (changed['diff'], detail['diff']):
        field, = diff['fields']
        assert field['field'] == 'thresholds'
        assert [entry['id'] for entry in field['before']] == ['watch']
        assert [entry['id'] for entry in field['after']] == ['watch']
        assert field['before'][0]['note'] == 'Review this rule unit.' and field['after'][0]['note'] is None
        assert 'Synthetic unchanged sibling rule' not in diff['markdown']
    with isolated_session_factory(17, FIRST)() as session:
        event = session.scalars(select(AuditLog).where(AuditLog.action == '研判逐条修改保存')).one()
        assert event.detail['model_calls'] == 0
        recorded, = event.detail['changes']
        assert recorded['item_id'] == item['id']
        assert recorded['before']['series_id'] == SERIES and recorded['before']['value'] == 12
        assert recorded['after']['series_id'] == OTHER_SERIES and recorded['after']['value'] == 1200
        assert recorded['before']['note'] == 'Review this rule unit.' and recorded['after']['note'] is None


def test_bind_unbound_evidence_without_changing_other_fields(saved_case):
    case = saved_case
    preview = success(open_edit(case))
    item = items_by_id(preview)[item_id('contradiction', 'bull', 'evidence', 1)]
    changed = confirm(case, preview, item, series_id=OTHER_SERIES)
    updated = items_by_id(changed)[item['id']]
    assert updated['status'] == 'confirmed' and updated['description'] == 'Synthetic seasonal evidence.'
    result = success(save_edit(case, changed))
    assert result['version'] == 2
    original, current = rows()
    expected = deepcopy(case.before)
    expected['contradiction']['bull']['evidence'][1].update(series_id=OTHER_SERIES, note=None)
    assert to_payload(current).model_dump(mode='json', by_alias=True) == expected
    assert to_payload(original).model_dump(mode='json', by_alias=True) == case.before
    version = success(case.client.get(endpoint('/versions/2'), headers=case.headers,
        params={'judgment_id': original.id}))
    for diff in (changed['diff'], version['diff']):
        field, = diff['fields']
        evidence, = field['before']['bull']['evidence']
        assert evidence['note'] == 'No matching observation was available.'
        assert 'No matching observation was available.' in diff['markdown']
        assert 'Preserve this note.' not in diff['markdown']
    with isolated_session_factory(17, FIRST)() as session:
        event = session.scalars(select(AuditLog).where(AuditLog.action == '研判逐条修改保存')).one()
        recorded, = event.detail['changes']
        assert recorded['before']['note'] == 'No matching observation was available.'
        assert recorded['after']['note'] is None


@pytest.mark.parametrize('fields', [
    {'related_series_ids': [OTHER_SERIES]},
    {'caliber': {'mode': 'catalog'}},
])
def test_add_related_binding_or_confirm_caliber_clears_evidence_note(saved_case, fields):
    case = saved_case
    preview = success(open_edit(case))
    item = items_by_id(preview)[item_id('contradiction', 'bull', 'evidence', 0)]
    unchanged = confirm(case, preview, item, series_id=SERIES, related_series_ids=[])
    assert not unchanged['review']['has_changes']
    assert 'Preserve this note.' in items_by_id(unchanged)[item['id']]['description']

    changed = confirm(case, unchanged, item, **fields)
    assert changed['review']['has_changes']
    success(save_edit(case, changed))
    expected = deepcopy(case.before)
    evidence = expected['contradiction']['bull']['evidence'][0]
    evidence['note'] = None
    if 'related_series_ids' in fields:
        evidence['related_series_ids'] = fields['related_series_ids']
    assert to_payload(rows()[1]).model_dump(mode='json', by_alias=True) == expected


def test_confirm_conversion_without_rebinding_clears_rule_note(saved_case):
    case = saved_case
    case.state['value'] = 1200
    preview = success(open_edit(case))
    item = items_by_id(preview)[item_id('thresholds', 0)]
    changed = confirm(case, preview, item, quantity_alignment={'mode': 'convert', 'exponent': 2})
    success(save_edit(case, changed))
    rule = to_payload(rows()[1]).thresholds[0]
    assert rule.series_id == SERIES and rule.value == 1200 and rule.note is None


def test_reconfirm_same_falsifier_indicator_after_catalog_unit_correction(saved_case):
    case = saved_case
    with isolated_session_factory(17, FIRST)() as session:
        row = session.get(Judgment, case.row.id)
        row.falsifiers = [{**row.falsifiers[0], 'unit_confirmed': 'Prior registered unit'}]
        session.commit()

    identifier = item_id('falsifiers', 0)
    preview = success(open_edit(case))
    item = items_by_id(preview)[identifier]
    assert item['binding']['unit'] == 'Prior registered unit'
    changed = confirm(case, preview, item, caliber={'mode': 'catalog'})
    assert changed['review']['has_changes']
    saved = success(save_edit(case, changed))

    original, current = rows()
    assert to_payload(original).falsifiers[0].unit_confirmed == 'Prior registered unit'
    assert to_payload(current).falsifiers[0].unit_confirmed == '吨'
    assert to_payload(current).falsifiers[0].scale == 100
    reopened = success(open_edit(case, judgment_id=saved['id'], version=saved['version']))
    assert items_by_id(reopened)[identifier]['binding']['unit'] == '吨'


def test_reopening_restores_confirmed_descriptive_caliber(saved_case):
    case = saved_case
    identifier = item_id('cost_line', 'items', 0)
    preview = success(open_edit(case))
    changed = confirm(case, preview, items_by_id(preview)[identifier],
        caliber={'mode': 'catalog', 'scope_note': 'Researcher checked this cost scope.'})
    saved = success(save_edit(case, changed))
    reopened = success(open_edit(case, judgment_id=saved['id'], version=saved['version']))
    assert items_by_id(reopened)[identifier]['caliber']['scope_note'] == 'Researcher checked this cost scope.'
    assert not reopened['review']['has_changes']


@pytest.mark.parametrize('fields', [
    {'series_id': 'kb-' + 'f' * 48},
    {'series_id': OTHER_SERIES, 'scale': 20},
    {'series_id': OTHER_SERIES, 'quantity_alignment': {'mode': 'convert', 'exponent': 1}},
])
def test_reject_unknown_ids_and_unoffered_scale(saved_case, fields):
    case = saved_case
    preview = success(open_edit(case))
    item = items_by_id(preview)[item_id('thresholds', 0)]
    rejected = update(case.client, case.headers, preview, item, **fields)
    if rejected.status_code == 422 and isinstance(rejected.json()['detail'], dict):
        warning = rejected.json()['detail']
        if warning.get('code') == 'binding_warning':
            rejected = update(case.client, case.headers, preview, item,
                binding_warning_token=warning['confirmation_token'], **fields)
    assert rejected.status_code == 422
    assert 'confirmation_token' not in rejected.json()['detail']
    assert success(case.client.get(endpoint('/review'), headers=case.headers,
        params={'draft_id': preview['draft_id']})) == preview['review']
    assert len(rows()) == 1


@pytest.mark.parametrize('conflict', ['version', 'source', 'write_permission'])
def test_changes_during_review_conflict_without_saving(saved_case, conflict):
    case = saved_case
    preview = success(open_edit(case))
    item = items_by_id(preview)[item_id('contradiction', 'bull', 'evidence', 1)]
    changed = confirm(case, preview, item, series_id=OTHER_SERIES)
    if conflict == 'version':
        with workbench_session(case.scope) as session:
            save_version(session, judgment(), actor='researcher', expected_base_version=1)
            session.commit()
    elif conflict == 'source':
        case.state['macro_value'] += 1
    else:
        case.state['wb_edit'] = False
    refused = save_edit(case, changed)
    assert refused.status_code == 409, refused.text
    assert {'version': '版本', 'source': '来源', 'write_permission': '权限'}[conflict] in refused.json()['detail']
    assert len(rows()) == (2 if conflict == 'version' else 1)


def test_viewer_cannot_open_item_edit(saved_case):
    case = saved_case
    viewer = {**case.headers, 'Authorization': 'Bearer source-viewer'}
    denied = open_edit(case, headers=viewer)
    assert denied.status_code == 403, denied.text
    preview = success(open_edit(case))
    denied = case.client.post(endpoint('/apply'), headers=viewer, json={
        'draft_id': preview['draft_id'], 'base_version': preview['base_version'],
        'review_revision': preview['review']['revision']})
    assert denied.status_code == 403, denied.text
    case.state['wb_edit'] = False
    assert success(case.client.get(endpoint('/manage'), headers=case.headers))['can_write'] is False
    assert open_edit(case).status_code == 403
    assert len(rows()) == 1


def test_recommendations_read_catalog_without_running_model_jobs(saved_case):
    case = saved_case
    preview = success(open_edit(case))
    item = items_by_id(preview)[item_id('contradiction', 'bull', 'evidence', 1)]
    query = {'draft_id': preview['draft_id'], 'item_id': item['id'], 'search': 'Macro quantity'}
    result = success(case.client.get(endpoint('/review/indicators'), headers=case.headers, params=query))
    recommendations = [field['series_id'] for field in result['recommendations']]
    assert recommendations[0] == OTHER_SERIES
    assert set(recommendations) <= {SERIES, OTHER_SERIES}
    request = {**query, 'model_id': 'synthetic-chat'}
    assert case.client.post(endpoint('/indicator-recommendations'), headers=case.headers,
        json=request).status_code == 422


def test_stale_dimension_blocks_save_and_can_be_fixed_in_review(saved_case):
    case = saved_case
    preview = success(open_edit(case))
    item = items_by_id(preview)[item_id('contradiction', 'bull', 'evidence', 1)]
    changed = confirm(case, preview, item, series_id=OTHER_SERIES)
    with isolated_session_factory(17, FIRST)() as session:
        session.add(SourceMapping(id=f'{DIMENSIONS_KEY}:{FIRST}',
            config={'dimensions': [entry for entry in default_dimensions() if entry['id'] != 'supply']},
            version=1, confirmed_by='researcher', confirmed_at=datetime.now(timezone.utc)))
        session.commit()
    rejected = save_edit(case, changed)
    assert rejected.status_code == 422 and '维度' in rejected.json()['detail']
    fixed = confirm(case, changed, item, action='dimension', dimension=None)
    saved = success(save_edit(case, fixed))
    assert saved['version'] == 2
    evidence = rows()[1].contradiction['bull']['evidence'][1]
    assert 'dimension' not in evidence and evidence['series_id'] == OTHER_SERIES


def test_edit_of_active_version_uses_existing_apply_draft_status(saved_case):
    case = saved_case
    with isolated_session_factory(17, FIRST)() as session:
        session.get(Judgment, case.row.id).status = '生效'
        session.commit()
    preview = success(open_edit(case))
    item = items_by_id(preview)[item_id('contradiction', 'bull', 'evidence', 1)]
    changed = confirm(case, preview, item, series_id=OTHER_SERIES)
    saved = success(save_edit(case, changed))
    assert saved['status'] == '草稿' and saved['panel']['id'] == case.panel['id']
    assert [row.status for row in rows()] == ['生效', '草稿']


def test_edit_without_analysis_audit_preserves_saved_chart_snapshot(saved_case):
    case = saved_case
    reference = {'kind': 'chart', 'id': 'synthetic-snapshot', 'revision': 2,
        'as_of': '2026-01-02', 'title': 'Saved snapshot', 'target': {'kind': 'threshold', 'id': 'watch'}}
    with isolated_session_factory(17, FIRST)() as session:
        row = session.get(Judgment, case.row.id)
        row.contradiction = {**row.contradiction, 'chart_references': [reference]}
        event = session.scalars(select(AuditLog).where(AuditLog.action == '研究逻辑分析保存')).one()
        session.delete(event)
        session.commit()
    preview = success(open_edit(case))
    item = items_by_id(preview)[item_id('thresholds', 0)]
    assert item['source_quote'] == '' and preview['diff']['fields'] == []
    changed = confirm(case, preview, item, series_id=OTHER_SERIES, source_quote=WATCH_QUOTE,
        caliber={'mode': 'catalog'}, quantity_alignment={'mode': 'convert', 'exponent': 2})
    success(save_edit(case, changed))
    assert [to_payload(row).chart_references for row in rows()] == [[reference], [reference]]


def test_reopening_keeps_text_decision_and_status_after_an_excluded_item(saved_case):
    case = saved_case
    preview = success(open_edit(case))
    bound = items_by_id(preview)[item_id('contradiction', 'bull', 'evidence', 0)]
    excluded = confirm(case, preview, bound, action='exclude')
    rule = items_by_id(excluded)[item_id('thresholds', 0)]
    kept = confirm(case, excluded, rule, action='keep_text')
    saved = success(save_edit(case, kept))
    reopened = success(open_edit(case, judgment_id=saved['id'], version=saved['version']))
    assert reopened['diff']['fields'] == []
    assert reopened['review']['has_changes'] is False
    evidence = items_by_id(reopened)[item_id('contradiction', 'bull', 'evidence', 0)]
    assert evidence['description'] == 'Synthetic seasonal evidence.\nNo matching observation was available.'
    assert evidence['status'] == 'pending'
    note = items_by_id(reopened)[item_id('unresolved', 1)]
    assert WATCH_QUOTE in note['description'] and note['status'] == 'kept_text'

    panel = saved['panel']
    renamed = success(case.client.patch(endpoint('/panels/' + panel['id']), headers=case.headers,
        json={'title': 'Renamed same panel', 'revision': panel['revision']}))
    changed_again = confirm(case, reopened, evidence, series_id=OTHER_SERIES)
    latest = success(save_edit(case, changed_again))
    assert latest['version'] == 3
    assert latest['panel']['id'] == renamed['id'] and latest['panel']['title'] == 'Renamed same panel'
    listed = success(case.client.get(endpoint('/panels'), headers=case.headers))
    assert listed['total'] == 1 and listed['items'][0]['judgment_id'] == latest['id']
