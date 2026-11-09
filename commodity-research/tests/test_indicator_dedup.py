"""Recall never decides identity; scope, evidence and researchers decide."""
from dataclasses import replace
import json

from fastapi import HTTPException
import pytest

from tin.config import settings
from tin.integration import indicator_dedup as service, page_facts
from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope, current_workbench
from tin.web import indicator_dedup as api
from tests.test_workbench_sources_v2 import WB, source

OWN, DEFAULT = 'kb-' + 'a' * 48, 'kb-' + 'b' * 48


def field(identifier, binding, **kw):
    return {'series_id': identifier, 'knowledge_base_id': binding['knowledge_base_id'],
        'knowledge_base_tenant_id': binding['tenant_id'], 'source_binding_id': binding['id'],
        'source_type': binding['source_type'], 'source_id': binding['source_id'],
        'metric': '完整字段名', 'unit': '元/吨', 'frequency': '周', 'source_file': 'Book.xlsx',
        'dimensions': {'sheet': 'SheetA'}, **kw}


@pytest.fixture
def setup(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    personal, default = source(1), {**source(2), 'source_type': 'central_dataset'}
    scope = WorkbenchScope(Identity('researcher', 'fixture@test', 'Fixture', 17, 'admin'), WB, '', 0,
        {'workspace_mode': 'isolated', 'can_read_source': True, 'can_edit': True},
        scope_version=2, sources_revision=7, sources=(personal, default))
    fields = [field(OWN, personal, metric='个人字段', description='指标编码 S1234567'),
              field(DEFAULT, default, metric='不同名称', dimensions={'header': 'S1234567'})]
    state = {'fields': fields, 'rows': [
        {'series_id': OWN, 'date': '2026-01-01', 'value': 1.2, 'raw_value': '1.20'},
        {'series_id': DEFAULT, 'date': '2026-01-01', 'value': 1.204},
        {'series_id': OWN, 'date': '2026-01-08', 'value': 2, 'raw_value': '2.00'},
        {'series_id': DEFAULT, 'date': '2026-01-08', 'value': 2}]}
    def snapshot(actual):
        assert actual.scope_version == 2
        return {'catalog': {item['series_id']: item for item in state['fields']}, 'version': 'catalog', 'datasets': []}
    def facts(actual, ids, module, **kwargs):
        assert module == 'indicators' and kwargs['points'] == 500 and kwargs['include_display_precision'] is True
        assert set(kwargs['origins']) == {OWN, DEFAULT}
        state['value_reads'] = state.get('value_reads', 0) + 1
        return {'indicators': state['fields'], 'rows': state['rows'], 'version': 'facts', 'source_versions': {}}
    monkeypatch.setattr(service, 'raw_metadata', snapshot)
    monkeypatch.setattr(page_facts, 'bounded_facts', facts)
    marker = current_workbench.set(scope)
    try: yield scope, state
    finally: current_workbench.reset(marker)


def only(scope):
    return service.list_mappings(scope)['items'][0]


def decide(scope, item, state='confirmed_default', **kw):
    if state == 'confirmed_default' and item['evidence'] is None:
        item = service.verify_candidate(scope, item['id'])
    return service.decide(scope, [item['id']], {item['id']: item['revision']}, state, **kw)['items'][0]


@pytest.mark.parametrize('code', ['S1234567', 'M1234567', 'ID12345678', 'CM1234567890', 'FU12345678'])
def test_vendor_code_recall_does_not_depend_on_chinese_names(setup, code):
    scope, state = setup
    state['fields'][0].update(metric='Alpha', description=code)
    state['fields'][1].update(metric='Completely unrelated', dimensions={'table_header': code})
    item = only(scope)
    assert item['candidate']['strength'] == 'strong' and item['state'] == 'pending'
    assert item['candidate']['codes'][0]['code'] == code
    assert service.confirmed_replacements(scope) == {} and not state.get('value_reads')


def test_weak_recall_requires_full_normalized_name_unit_frequency_and_never_confirms(setup):
    scope, state = setup
    for item in state['fields']: item.update(description='', dimensions={})
    state['fields'][0]['metric'], state['fields'][1]['metric'] = ' Ａlpha   Beta ', 'alpha beta'
    assert only(scope)['candidate']['strength'] == 'weak' and service.confirmed_replacements(scope) == {}
    state['fields'][1]['frequency'] = '日'
    assert service.candidate_pairs(scope, state['fields']) == []
    state['fields'][1].update(frequency='周', unit='万元/吨')
    assert service.candidate_pairs(scope, state['fields']) == []
    state['fields'][1].update(unit='元/吨', metric='alpha')
    assert service.candidate_pairs(scope, state['fields']) == []


def test_code_boundaries_and_filenames_cannot_recall(setup):
    scope, state = setup
    for item in state['fields']:
        item.update(description='XS1234567Z', metric=item['series_id'], dimensions={}, source_file='S1234567.xlsx')
    assert service.candidate_pairs(scope, state['fields']) == []


def test_rounding_mismatch_dates_asof_and_stale_default(setup):
    scope, state = setup
    state['rows'].extend([
        {'series_id': OWN, 'date': '2026-01-15', 'value': -1.23, 'raw_value': '-1.23'},
        {'series_id': DEFAULT, 'date': '2026-01-15', 'value': -1.225},
        {'series_id': OWN, 'date': '2026-01-22', 'value': 9, 'raw_value': '9.00'},
        {'series_id': DEFAULT, 'date': '2026-01-22', 'value': 9.006},
        {'series_id': OWN, 'date': '2026-01-29', 'value': 10, 'raw_value': '10.00'}])
    item = service.verify_candidate(scope, only(scope)['id'])
    evidence = item['evidence']
    assert evidence['matching_periods'] == 3 and evidence['mismatching_periods'] == 1
    assert evidence['latest_mismatch_date'] == '2026-01-22'
    assert evidence['personal_as_of'] == '2026-01-29' and evidence['default_as_of'] == '2026-01-22'
    assert evidence['stale_default'] and evidence['checked_points'] == 500
    assert evidence['unit_match'] and evidence['frequency_match'] and not item['batch_eligible']
    assert service.confirmed_replacements(scope) == {}


def test_display_precision_overrides_raw_value(setup):
    scope, state = setup
    state['rows'][0].update(value=1.2, raw_value='1.2', display_decimals=2)
    state['rows'][1]['value'] = 1.24
    assert service.verify_candidate(scope, only(scope)['id'])['evidence']['mismatching_periods'] == 1


@pytest.mark.parametrize('problem', ['mismatch', 'unit', 'frequency', 'empty', 'precision', 'weak'])
def test_batch_requires_strong_complete_evidence(setup, problem):
    scope, state = setup
    if problem == 'mismatch': state['rows'][1]['value'] = 1.3
    if problem == 'unit': state['fields'][1]['unit'] = '万吨'
    if problem == 'frequency': state['fields'][1]['frequency'] = '日'
    if problem == 'empty': state['rows'] = []
    if problem == 'precision':
        for row in state['rows']: row.pop('raw_value', None)
    if problem == 'weak':
        for item in state['fields']: item.update(metric='相同完整名', description='', dimensions={})
    with pytest.raises(HTTPException) as error: decide(scope, only(scope), batch=True)
    assert error.value.status_code == 422 and only(scope)['state'] == 'pending'


def test_changed_evidence_requires_review_before_single_or_batch_confirmation(setup):
    scope, state = setup
    item = service.verify_candidate(scope, only(scope)['id'])
    assert item['batch_eligible']
    state['rows'][1]['value'] = 8
    with pytest.raises(HTTPException) as error: decide(scope, item, batch=True)
    assert error.value.status_code == 409
    with pytest.raises(HTTPException) as error: decide(scope, item)
    assert error.value.status_code == 409
    item = service.verify_candidate(scope, item['id'])
    saved = decide(scope, item)
    assert saved['state'] == 'confirmed_default' and saved['evidence']['mismatching_periods'] == 1
    assert service.confirmed_replacements(scope) == {OWN: DEFAULT}
    assert saved['confirmed_by'] == scope.identity.user_id and saved['history'][-1]['evidence']['mismatching_periods'] == 1


@pytest.mark.parametrize('state', ['confirmed_default', 'keep_both', 'rejected'])
def test_single_decision_undo_history_revision_conflict(setup, state):
    scope, _ = setup
    item = only(scope)
    saved = decide(scope, item, state)
    assert saved['revision'] == item['revision'] + (2 if state == 'confirmed_default' else 1) and saved['state'] == state
    with pytest.raises(HTTPException) as error: service.undo(scope, saved['id'], item['revision'])
    assert error.value.status_code == 409
    restored = service.undo(scope, saved['id'], saved['revision'])
    assert restored['state'] == 'pending' and restored['confirmed_by'] is None
    assert [entry['action'] for entry in restored['history']] == ['decide', 'undo']
    assert service.confirmed_replacements(scope) == {}


def test_decisions_shared_by_space_across_workbenches_but_not_removed_sources(setup):
    scope, _ = setup
    saved = decide(scope, only(scope))
    second = replace(scope, workbench_id='10000000-0000-4000-8000-000000000092', identity=replace(scope.identity, user_id='second'))
    assert only(second)['id'] == saved['id'] and service.confirmed_replacements(second) == {OWN: DEFAULT}
    assert service.confirmed_replacements(replace(scope, identity=replace(scope.identity, tenant_id=18))) == {}
    assert service.confirmed_replacements(replace(scope, sources=(scope.sources[1],))) == {}


@pytest.mark.parametrize('role', ['viewer', 'contributor'])
def test_confirmation_permission_matches_central_maintenance(setup, role):
    scope, _ = setup
    reader = replace(scope, identity=replace(scope.identity, role=role))
    assert not service.list_mappings(reader)['can_confirm']
    assert service.verify_candidate(reader, only(reader)['id'])['evidence']
    with pytest.raises(HTTPException) as error: decide(reader, only(reader))
    assert error.value.status_code == 403


def test_restricted_or_foreign_center_cannot_confirm(setup):
    scope, _ = setup
    restricted = replace(scope, sources=({**scope.sources[0], 'delegated': True}, scope.sources[1]))
    with pytest.raises(HTTPException) as error: service.list_mappings(restricted)
    assert error.value.status_code == 403 and service.confirmed_replacements(restricted) == {}
    assert not service.can_confirm(replace(scope, sources=(scope.sources[0], {**scope.sources[1], 'tenant_id': 18})))


def test_catalog_annotation_retains_copies_and_does_not_read_values(setup):
    scope, state = setup
    pending = service.catalog_annotations(scope, state['fields'])
    assert len(pending) == 2 and all(item['dedup_status'] == 'pending' for item in pending)
    assert not state.get('value_reads')
    decide(scope, only(scope))
    reads = state['value_reads']
    assert all(item['dedup_status'] == 'confirmed_default' for item in service.catalog_annotations(scope, state['fields']))
    assert state['value_reads'] == reads


def test_api_rejects_stale_sources_and_missing_revision(setup):
    scope, _ = setup
    item = api.get_indicator_dedup()['items'][0]
    with pytest.raises(HTTPException) as error:
        api.decide_indicator_dedup(api.DecisionInput(ids=[item['id']], revisions={item['id']: 1}, state='keep_both', sources_revision=6))
    assert error.value.status_code == 409
    with pytest.raises(HTTPException) as error:
        api.decide_indicator_dedup(api.DecisionInput(ids=[item['id']], revisions={}, state='keep_both', sources_revision=7))
    assert error.value.status_code == 422


def test_confirm_requires_reviewed_evidence_before_deciding(setup):
    scope, _ = setup
    item = only(scope)
    with pytest.raises(HTTPException) as error:
        service.decide(scope, [item['id']], {item['id']: item['revision']}, 'confirmed_default')
    assert error.value.status_code == 422 and only(scope)['state'] == 'pending'


def test_raw_metadata_uses_bounded_pages_and_rejects_incomplete_catalog(setup, monkeypatch):
    scope, state = setup
    import importlib.util
    spec = importlib.util.spec_from_file_location('dedup_raw_test', service.__file__)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    fields = [{**state['fields'][0], 'series_id': 'kb-' + f'{index:048x}'} for index in range(401)]
    calls = []
    def page(actual, offset=0, limit=200, *, version=None, apply_dedup=True):
        assert not apply_dedup and limit == 200
        calls.append(offset)
        following = offset + limit if offset + limit < len(fields) else None
        return {'series': fields[offset:offset+limit], 'total': len(fields), 'version': 'catalog',
            'datasets': [], 'next_offset': following, 'truncated': following is not None}
    monkeypatch.setattr(page_facts, 'catalog_page', page)
    assert len(module.raw_metadata(scope)['catalog']) == 401 and calls == [0, 200, 400]
    def truncated(*args, **kwargs):
        return {**page(*args, **kwargs), 'next_offset': None, 'truncated': False}
    monkeypatch.setattr(page_facts, 'catalog_page', truncated)
    with pytest.raises(HTTPException) as error: module.raw_metadata(scope)
    assert error.value.status_code == 503


def test_rounding_handles_finite_values_outside_default_decimal_context():
    own, default = {'series_id': OWN}, {'series_id': DEFAULT}
    result = service.compare_series(own, default, [
        {'series_id': OWN, 'date': '2026-01-01', 'value': 1e100, 'display_decimals': 2},
        {'series_id': DEFAULT, 'date': '2026-01-01', 'value': 1e100}])
    assert result['matching_periods'] == 1 and result['precision_complete']


def test_matching_strong_candidate_batch_confirmation_preserves_decision_snapshot(setup):
    scope, state = setup
    item = service.verify_candidate(scope, only(scope)['id'])
    assert item['batch_eligible']
    saved = decide(scope, item, batch=True)
    assert saved['state'] == 'confirmed_default' and service.confirmed_replacements(scope) == {OWN: DEFAULT}
    assert saved['history'][-1]['evidence']['mismatching_periods'] == 0
    state['rows'][1]['value'] = 100
    refreshed = service.verify_candidate(scope, saved['id'])
    assert refreshed['evidence']['mismatching_periods'] == 1
    assert refreshed['history'][-1]['evidence']['mismatching_periods'] == 0
    assert service.confirmed_replacements(scope) == {OWN: DEFAULT}, 'New evidence must not switch the source automatically'


@pytest.mark.parametrize('decision', ['confirmed_default', 'keep_both', 'rejected'])
@pytest.mark.parametrize('withdrawn', [OWN, DEFAULT])
def test_withdrawn_document_fields_never_expose_stored_decision_snapshots(setup, decision, withdrawn):
    scope, state = setup
    secret = 'WITHDRAWN_FIELD_PRIVATE_METADATA'
    state['fields'] = [{**item, 'metric': secret, 'source_file': secret + '.xlsx'}
                       if item['series_id'] == withdrawn else item for item in state['fields']]
    saved = decide(scope, only(scope), decision)
    state['fields'] = [field for field in state['fields'] if field['series_id'] != withdrawn]
    # KB grants are deliberately unchanged: the document/field was withdrawn.
    result = service.list_mappings(scope)
    assert len(result['items']) == 1 and result['pending_count'] == 0
    item = result['items'][0]
    side = 'personal' if withdrawn == OWN else 'default'
    assert item[side] is None and item['unavailable_sides'] == [side]
    assert item['source_unavailable'] and not item['batch_eligible']
    assert item['candidate'] is None and item['evidence'] is None and item['history'] == []
    assert secret not in json.dumps(result, default=str)
    reader = replace(scope, identity=replace(scope.identity, role='viewer'))
    assert service.list_mappings(reader)['items'] == []
    with pytest.raises(HTTPException) as error:
        service.undo(reader, saved['id'], saved['revision'])
    assert error.value.status_code == 403
    annotated = service.catalog_annotations(scope, state['fields'])
    assert all('dedup_pairs' not in field for field in annotated)
    # The unavailable source does not silently change a confirmed selection.
    if decision == 'confirmed_default':
        assert service.confirmed_replacements(scope) == {OWN: DEFAULT}
    with pytest.raises(HTTPException) as error:
        service.undo(scope, saved['id'], saved['revision'] - 1)
    assert error.value.status_code == 409
    undone = service.undo(scope, saved['id'], saved['revision'])
    assert undone['state'] == 'pending' and undone['revision'] == saved['revision'] + 1
    assert undone[side] is None and undone['evidence'] is None and undone['history'] == []
    assert secret not in json.dumps(undone, default=str)
    assert service.confirmed_replacements(scope) == {}
    assert service.list_mappings(scope)['items'] == []


def test_explicit_undo_restores_only_the_still_authorized_personal_selector(setup):
    scope, state = setup
    saved = decide(scope, only(scope))
    state['fields'] = [item for item in state['fields'] if item['series_id'] == OWN]
    assert page_facts.catalog_snapshot(scope)['catalog'] == {}, 'A withdrawn default never switches the source automatically'
    item = service.list_mappings(scope)['items'][0]
    assert item['default'] is None and item['state'] == 'confirmed_default'
    service.undo(scope, saved['id'], item['revision'])
    catalog = page_facts.catalog_snapshot(scope)['catalog']
    assert set(catalog) == {OWN} and DEFAULT not in catalog


def test_unavailable_decision_of_another_center_tenant_cannot_be_exposed_or_undone(setup):
    from tin.models.indicator_dedup import IndicatorCorrespondence
    scope, state = setup
    saved = decide(scope, only(scope))
    with service.correspondence_session(scope) as session:
        session.get(IndicatorCorrespondence, saved['id']).default_tenant_id = 18
        session.commit()
    foreign_center = {**scope.sources[1], 'tenant_id': 18}
    own_center = {**source(3), 'source_type': 'central_dataset'}
    scope = replace(scope, sources=(scope.sources[0], foreign_center, own_center))
    state['fields'] = state['fields'][:1]
    assert service.can_confirm(scope), 'A different local center must not grant ownership of this decision'
    assert service.list_mappings(scope)['items'] == []
    with pytest.raises(HTTPException) as error:
        service.undo(scope, saved['id'], saved['revision'])
    assert error.value.status_code == 403


def test_decided_pair_uses_current_metadata_even_after_names_stop_recalling(setup):
    scope, state = setup
    saved = decide(scope, only(scope))
    state['fields'] = [{**field, 'description': '', 'dimensions': {},
                        'metric': 'Current personal' if field['series_id'] == OWN else 'Current default'}
                       for field in state['fields']]
    assert service.candidate_pairs(scope, state['fields']) == []
    item = service.list_mappings(scope)['items'][0]
    assert item['id'] == saved['id'] and item['personal']['metric'] == 'Current personal'
    assert item['default']['metric'] == 'Current default'
    current = service.undo(scope, item['id'], item['revision'])
    assert current['personal']['metric'] == 'Current personal' and current['state'] == 'pending'


def test_undo_does_not_return_snapshots_after_source_grant_removed(setup):
    scope, _ = setup
    saved = decide(scope, only(scope))
    removed = replace(scope, sources=(scope.sources[1],))
    assert service.list_mappings(removed)['items'] == []
    with pytest.raises(HTTPException) as error:
        service.undo(removed, saved['id'], saved['revision'])
    assert error.value.status_code == 404
