from copy import deepcopy
import pytest

from structured_data.pdf_automation import group_fragments, process_draft
from structured_data.pdf_automation import grouping_evidence
from structured_data.pdf_errors import PDFProcessingError, processing_problem
from test_pdf_tables import bundle, setup


def test_automatic_acceptance_is_queryable_and_never_claims_human_review(tmp_path):
    store, draft, plan = setup(tmp_path)
    table = store.publish_automatic(draft['draft_id'], plan)
    assert table['status'] == 'auto_accepted'
    assert not table['extraction_verified']
    assert 'reviewed' not in table['plan']
    assert store.query(table['table_id'])['rows']
    assert store.publish_automatic(draft['draft_id'], plan)['table_id'] == table['table_id']
    assert store.list_tables()['tables'][0]['status'] == 'auto_accepted'


@pytest.mark.parametrize('field', ['reviewed', 'quality_reviews', 'cell_corrections', 'row_alignment_review'])
def test_automatic_acceptance_cannot_forge_review(tmp_path, field):
    store, draft, plan = setup(tmp_path)
    plan[field] = True if field == 'reviewed' else []
    with pytest.raises(ValueError, match='human review'):
        store.publish_automatic(draft['draft_id'], plan)


def test_automatic_quality_gate_blocks_uncertain_cells(tmp_path):
    raw = bundle()
    raw['fragments'][0]['quality'] = {'status': 'needs_review', 'issues': [{'code': 'missing_header', 'message': 'header unclear'}]}
    store, draft, plan = setup(tmp_path, raw)
    with pytest.raises(ValueError):
        store.publish_automatic(draft['draft_id'], plan)
    assert store.list_tables()['tables'] == []


@pytest.mark.parametrize('groups', [
    [], [{'fragment_ids': ['invented'], 'reason': 'guess'}],
    [{'fragment_ids': ['table-1', 'table-1'], 'reason': 'guess'}],
    [{'fragment_ids': ['table-1']}],
])
def test_model_grouping_must_cover_real_fragments_exactly_once(tmp_path, monkeypatch, groups):
    _, draft, _ = setup(tmp_path)
    monkeypatch.setattr('structured_data.pdf_api._model_json', lambda *args: {'groups': groups})
    monkeypatch.setattr('structured_data.model_settings.read_model', lambda *args: {})
    with pytest.raises(ValueError):
        group_fragments(draft, tmp_path)


def test_partial_success_persists_and_retry_skips_accepted_tables(tmp_path, monkeypatch):
    raw = bundle([['Item', 'Value'], ['A', '-1.20']], [['Item', 'Value'], ['B', '3']])
    raw['fragments'][1]['quality'] = {'status': 'needs_review', 'issues': [{'code': 'uncertain', 'message': 'uncertain value'}]}
    store, draft, combined = setup(tmp_path, raw)
    calls = []
    monkeypatch.setattr('structured_data.pdf_automation.group_fragments', lambda *args: [
        {'fragment_ids': ['table-1'], 'reason': 'independent title'},
        {'fragment_ids': ['table-2'], 'reason': 'independent title'},
    ])
    def suggest(draft, body, root):
        calls.append(body.fragment_ids)
        plan = deepcopy(combined)
        plan['fragment_ids'] = body.fragment_ids
        plan['header_rows'] = {i: combined['header_rows'][i] for i in body.fragment_ids}
        for col in plan['columns']:
            col['source_columns'] = {i: col['source_columns'][i] for i in body.fragment_ids}
        return {'status': 'ready', 'plan': plan}
    monkeypatch.setattr('structured_data.pdf_api.suggest_plan', suggest)
    snapshots = []
    result = process_draft(store, draft['draft_id'], tmp_path, save=lambda s: snapshots.append(deepcopy(s)))
    assert result['accepted_count'] == 1 and result['attention_count'] == 1
    assert len(store.list_tables()['tables']) == 1
    assert len(snapshots) >= 3
    pending = result['groups'][1]
    assert pending['plan']['fragment_ids'] == ['table-2']
    assert pending['plan']['columns'][1]['source_columns'] == {'table-2': 1}
    assert 'quality_reviews' not in pending['plan']
    calls.clear()
    again = process_draft(store, draft['draft_id'], tmp_path, previous=result, save=lambda s: None)
    assert calls == []  # source review is not repaired by repeating the same model call
    assert again['accepted_count'] == 1
    assert len(store.list_tables()['tables']) == 1


def test_needs_review_and_model_failure_cannot_publish(tmp_path, monkeypatch):
    store, draft, _ = setup(tmp_path)
    monkeypatch.setattr('structured_data.pdf_automation.group_fragments', lambda *args: [{'fragment_ids': ['table-1'], 'reason': 'real header'}])
    monkeypatch.setattr('structured_data.pdf_api.suggest_plan', lambda *args: {'status': 'needs_review', 'plan': {'ignored': True}, 'message': 'cannot identify columns'})
    result = process_draft(store, draft['draft_id'], tmp_path, save=lambda s: None)
    assert result['attention_count'] == 1 and not store.list_tables()['tables']
    failures = []
    def fail(*args):
        failures.append(True)
        raise RuntimeError('secret-api-key')
    monkeypatch.setattr('structured_data.pdf_api.suggest_plan', fail)
    result = process_draft(store, draft['draft_id'], tmp_path, save=lambda s: None)
    assert result['groups'][0]['problem']['action'] == 'retry'
    assert 'secret-api-key' not in str(result)
    assert not store.list_tables()['tables']
    process_draft(store, draft['draft_id'], tmp_path, previous=result, save=lambda s: None)
    assert len(failures) == 2


def test_grouping_compaction_keeps_all_text_rows_and_spans():
    raw = bundle([['Identifier', 'Quantity'], ['A', '-1.20'], ['B', '']])
    raw['fragments'][0]['rows'][1][0]['rowspan'] = 2
    raw['page_context'] = [{'page': 1, 'bbox': [0, 0, 1, 1], 'text': 'unit t', 'text_parts': ['unit ', 't']},
                           {'page': 2, 'text': 'different', 'text_parts': ['extra evidence']}]
    compact = grouping_evidence(raw)
    assert compact['fragments'][0]['rows'] == [['Identifier', 'Quantity'], ['A', '-1.20'], ['B', '']]
    assert compact['fragments'][0]['spans'] == [{'row': 1, 'column': 0, 'rowspan': 2}]
    assert compact['page_context'][0]['text'] == 'unit t'
    assert 'text_parts' not in compact['page_context'][0]
    assert compact['page_context'][1]['text_parts'] == ['extra evidence']


@pytest.mark.parametrize('code,action', [('GROUPING_INPUT_LIMIT', 'review'), ('MODEL_NOT_CONFIGURED', 'model_settings'),
    ('MODEL_RATE_LIMIT', 'retry'), ('MODEL_TIMEOUT', 'retry'), ('MODEL_OUTPUT_INVALID', 'retry')])
def test_specific_failure_is_persisted_with_recovery_and_no_secret(tmp_path, monkeypatch, code, action):
    store, draft, _ = setup(tmp_path)
    def fail(*args):
        raise PDFProcessingError(code, 'private-api-key hidden-provider-url', input_chars=220000, limit_chars=200000)
    monkeypatch.setattr('structured_data.pdf_automation.group_fragments', fail)
    saved = []
    result = process_draft(store, draft['draft_id'], tmp_path, save=lambda state: saved.append(deepcopy(state)))
    assert result['problem']['code'] == code and result['problem']['action'] == action
    assert result['problem']['input_chars'] == 220000
    assert saved and 'private-api-key' not in str(saved) and 'hidden-provider-url' not in str(saved)
    assert not store.list_tables()['tables']


def test_unknown_failure_does_not_guess_model_configuration():
    problem = processing_problem(RuntimeError('secret'), stage='grouping')
    assert problem['code'] == 'PROCESSING_FAILED'
    assert problem['action'] == 'retry' and 'secret' not in str(problem)


def test_review_preserves_validated_horizontal_plan_and_units(tmp_path, monkeypatch):
    from test_pdf_tables import horizontal_setup
    store, draft, plan = horizontal_setup(tmp_path, unlabeled=True)
    monkeypatch.setattr('structured_data.pdf_automation.group_fragments', lambda *args: [
        {'fragment_ids': plan['fragment_ids'], 'reason': 'source continuation'}])
    monkeypatch.setattr('structured_data.pdf_api.suggest_plan', lambda *args: {'status': 'ready', 'plan': deepcopy(plan)})
    result = process_draft(store, draft['draft_id'], tmp_path, save=lambda state: None)
    group = result['groups'][0]
    assert group['status'] == 'needs_attention'
    assert group['plan'] == plan
    assert group['plan']['columns'][2]['unit'] == '%'
    assert group['plan']['row_groups'] == plan['row_groups']
    assert any(issue['code'] == 'row_alignment_unverified' for issue in group['issues'])
    assert not store.list_tables()['tables']


def _large_grouping_draft():
    raw = bundle()
    raw['fragments'] = []
    raw['page_context'] = []
    for page in range(1, 5):
        fragment = deepcopy(bundle()['fragments'][0])
        fragment.update(id=f'fragment-{page}', page=page)
        fragment['rows'][1][1]['text'] = '-0001.200'
        fragment['notes'] = ['source note ' * 60]
        raw['fragments'].append(fragment)
        raw['page_context'].append({'page': page, 'text': f'boundary-{page} ' + 'source ' * 100})
    return {'bundle': raw}


def test_long_grouping_overlaps_whole_pages_and_verifies_cross_batch_source(tmp_path, monkeypatch):
    import structured_data.pdf_automation as automation
    draft = _large_grouping_draft()
    monkeypatch.setattr(automation, 'GROUPING_REQUEST_BYTES', 6500)
    monkeypatch.setattr('structured_data.model_settings.read_model', lambda *args: {})
    batches = automation._overlapping_batches(grouping_evidence(draft['bundle']))
    assert len(batches) > 1
    assert set.union(*batches) == {1, 2, 3, 4}
    assert all(left & right for left, right in zip(batches, batches[1:]))
    calls = []
    def model(cfg, messages):
        evidence = __import__('json').loads(messages[1]['content'])
        calls.append((messages[0]['content'], evidence))
        if 'batch_groups' in evidence:
            # Deliberately propose an incorrect cross-batch merge. The summary
            # alone must not authorize it; complete source verification splits it.
            return {'groups': [{'fragment_ids': ['fragment-1', 'fragment-4'], 'reason': 'candidate'},
                               {'fragment_ids': ['fragment-2'], 'reason': 'independent'},
                               {'fragment_ids': ['fragment-3'], 'reason': 'independent'}]}
        ids = [f['id'] for f in evidence['fragments']]
        for f in evidence['fragments']:
            assert f['rows'][1][1] == '-0001.200'
        return {'groups': [{'fragment_ids': [i], 'reason': 'source title', 'identity': 'source identity'} for i in ids]}
    monkeypatch.setattr('structured_data.pdf_api._model_json', model)
    result = group_fragments(draft, tmp_path)
    assert sorted(i for g in result for i in g['fragment_ids']) == [f'fragment-{p}' for p in range(1, 5)]
    assert len(result) == 4
    full_checks = [(prompt, e) for prompt, e in calls if '跨批次候选的完整原件复核' in prompt]
    assert len(full_checks) == 1
    assert {c['page'] for c in full_checks[0][1]['page_context']} == {1, 2, 3, 4}
    assert len([e for _, e in calls if 'batch_groups' not in e and any(f['page'] == 2 for f in e['fragments'])]) >= 2


def test_long_grouping_rejects_duplicate_final_ids(tmp_path, monkeypatch):
    import structured_data.pdf_automation as automation
    monkeypatch.setattr(automation, 'GROUPING_REQUEST_BYTES', 6500)
    monkeypatch.setattr('structured_data.model_settings.read_model', lambda *args: {})
    def model(cfg, messages):
        evidence = __import__('json').loads(messages[1]['content'])
        ids = evidence.get('source_fragment_ids') or [f['id'] for f in evidence['fragments']]
        if 'batch_groups' in evidence:
            ids.append(ids[0])
        return {'groups': [{'fragment_ids': [i], 'reason': 'source title', 'identity': 'source identity'} for i in ids]}
    monkeypatch.setattr('structured_data.pdf_api._model_json', model)
    with pytest.raises(PDFProcessingError, match='遗漏、重复'):
        group_fragments(_large_grouping_draft(), tmp_path)


def test_oversized_page_is_not_truncated_or_sent(tmp_path, monkeypatch):
    draft = _large_grouping_draft()
    draft['bundle']['page_context'][0]['text'] = '原文' * 20000
    monkeypatch.setattr('structured_data.model_settings.read_model', lambda *args: {})
    monkeypatch.setattr('structured_data.pdf_api._model_json', lambda *args: pytest.fail('oversized request'))
    with pytest.raises(PDFProcessingError, match='单页完整来源'):
        group_fragments(draft, tmp_path)
    assert draft['bundle']['page_context'][0]['text'] == '原文' * 20000


def test_grouping_does_not_let_quality_diagnostics_consume_source_budget():
    raw = bundle()
    raw['fragments'][0]['quality'] = {'issues': [{'cells': [{'row': 0, 'column': 0}]}]}
    compact = grouping_evidence(raw)
    assert 'quality' not in compact['fragments'][0]
    assert raw['fragments'][0]['quality']['issues']  # immutable review evidence kept


def test_long_grouping_keeps_text_only_batches_and_reports_progress(tmp_path, monkeypatch):
    import json
    import structured_data.pdf_automation as automation
    draft = _large_grouping_draft()
    for page in range(5, 11):
        draft['bundle']['page_context'].append({'page': page, 'text': f'context-{page} ' + 'narrative ' * 150})
    monkeypatch.setattr(automation, 'GROUPING_REQUEST_BYTES', 6500)
    monkeypatch.setattr('structured_data.model_settings.read_model', lambda *args: {})
    seen_pages, text_only = set(), []
    def model(cfg, messages):
        evidence = json.loads(messages[1]['content'])
        if 'batch_groups' in evidence:
            assert any(not g['fragment_ids'] and g.get('identity') for g in evidence['batch_groups'])
            return {'groups': [{'fragment_ids': [i], 'reason': 'independent'} for i in evidence['source_fragment_ids']]}
        seen_pages.update(c['page'] for c in evidence['page_context'])
        ids = [f['id'] for f in evidence['fragments']]
        if not ids:
            text_only.append(True)
        return {'groups': [{'fragment_ids': [i], 'reason': 'source', 'identity': 'source'} for i in ids],
                'context_identity': 'narrative continuation context'}
    monkeypatch.setattr('structured_data.pdf_api._model_json', model)
    stages = []
    result = group_fragments(draft, tmp_path, stages.append)
    assert text_only and seen_pages == set(range(1, 11))
    assert stages[-1] == '正在核对跨批次续表关系'
    assert len(result) == 4
