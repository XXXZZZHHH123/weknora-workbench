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
    calls.clear()
    again = process_draft(store, draft['draft_id'], tmp_path, previous=result, save=lambda s: None)
    assert calls == [['table-2']]
    assert again['accepted_count'] == 1
    assert len(store.list_tables()['tables']) == 1


def test_needs_review_and_model_failure_cannot_publish(tmp_path, monkeypatch):
    store, draft, _ = setup(tmp_path)
    monkeypatch.setattr('structured_data.pdf_automation.group_fragments', lambda *args: [{'fragment_ids': ['table-1'], 'reason': 'real header'}])
    monkeypatch.setattr('structured_data.pdf_api.suggest_plan', lambda *args: {'status': 'needs_review', 'plan': {'ignored': True}, 'message': 'cannot identify columns'})
    result = process_draft(store, draft['draft_id'], tmp_path, save=lambda s: None)
    assert result['attention_count'] == 1 and not store.list_tables()['tables']
    def fail(*args):
        raise RuntimeError('secret-api-key')
    monkeypatch.setattr('structured_data.pdf_api.suggest_plan', fail)
    result = process_draft(store, draft['draft_id'], tmp_path, previous=result, save=lambda s: None)
    assert 'secret-api-key' not in str(result)
    assert not store.list_tables()['tables']


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
