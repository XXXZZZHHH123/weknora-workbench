"""Item review uses synthetic sources, isolated SQLite, and a mock model only."""
import copy
from types import SimpleNamespace
import time

import pytest
from fastapi import HTTPException
from sqlalchemy import select

from tests.test_knowledge_facts import canonical, FIRST, SECOND, SERIES, page
from tests.test_judgment_generation import generator, generate, apply, rows, TEXT, document
from tin.integration.workbench_scope import isolated_session_factory
from tin.models import AuditLog, SourceMapping
from tin.web import judgment_generation as generation
from tin.web import judgment_review as review
from tin.judgments.analysis import assess_draft


def endpoint(suffix=''):
    return page(FIRST, '/api/sn/judgment/review' + suffix)


def all_items(preview):
    return [item for section in preview['review']['sections'] for item in section['items']]


def pick(preview, kind):
    return next(item for item in all_items(preview) if item['kind'] == kind)


def update(client, headers, preview, item, action='confirm', **fields):
    return client.post(endpoint('/update'), headers=headers, json={
        'draft_id': preview['draft_id'], 'revision': preview['review']['revision'],
        'item_id': item['id'], 'action': action, **fields})


def reuse(client, headers, preview, enabled=True):
    return client.post(endpoint('/reuse'), headers=headers, json={
        'draft_id': preview['draft_id'], 'revision': preview['review']['revision'], 'enabled': enabled})


def preferences():
    with isolated_session_factory(17, FIRST)() as session:
        return session.scalars(select(SourceMapping).where(SourceMapping.confirmed_by == 'researcher')).all()


def test_review_read_and_confirm_only_save_when_explicitly_applied(generator):
    client, headers, state, _, calls = generator
    preview = generate(client, headers).json()
    assert preview['review_available'] and preview['review']['pending_count'] == preview['review']['total']
    assert preview['source_text'] == TEXT
    count, pages = len(calls), state['pages']
    fetched = client.get(endpoint(), headers=headers, params={'draft_id': preview['draft_id']})
    assert fetched.status_code == 200 and fetched.json() == preview['review']
    item = pick(preview, 'thresholds')
    assert item['can_confirm'] and item['binding']['series_id'] == SERIES and item['source_quote'] in TEXT
    result = update(client, headers, preview, item)
    assert result.status_code == 200, result.text
    changed = result.json()
    assert pick(changed, 'thresholds')['status'] == 'confirmed'
    assert changed['review']['revision'] == preview['review']['revision'] + 1
    assert len(calls) == count and state['pages'] == pages and rows() == []
    assert apply(client, headers, changed).status_code == 200
    assert len(rows()) == 1 and rows()[0].status == '草稿'
    with isolated_session_factory(17, FIRST)() as session:
        actions = session.scalars(select(AuditLog).where(AuditLog.action == '研判逐项确认')).all()
        saved = session.scalars(select(AuditLog).where(AuditLog.action == '研究逻辑分析保存')).one()
        assert len(actions) == 1 and actions[0].detail['remember'] is False
        assert saved.detail['review_status'] and saved.detail['review'][0]['action'] == 'confirmed'


def test_repeat_confirmation_uses_current_revision_and_stale_change_cannot_overwrite(generator):
    client, headers, _, _, _ = generator
    preview = generate(client, headers).json()
    item = pick(preview, 'binding')
    changed = update(client, headers, preview, item).json()
    assert update(client, headers, preview, item, 'exclude').status_code == 409
    repeated = update(client, headers, changed, item)
    assert repeated.status_code == 200 and pick(repeated.json(), 'binding')['status'] == 'confirmed'
    missing = {**item, 'id': 'item-absent'}
    assert update(client, headers, repeated.json(), missing).status_code == 404


@pytest.mark.parametrize('damage', ['number', 'quote', 'unit'])
def test_invalid_candidate_is_typed_pending_and_cannot_be_confirmed(generator, damage):
    client, headers, _, response, _ = generator
    if damage == 'number':
        response['judgment']['thresholds'][0]['value'] = 0
    elif damage == 'quote':
        response['rule_quotes']['watch'] = 'AI invented quotation'
    else:
        response['binding_units'][SERIES] = 'kg'
    preview = generate(client, headers).json()
    item = pick(preview, 'thresholds')
    if damage in {'number', 'quote'}:
        assert item['can_confirm'] is False and item['warnings']
        assert update(client, headers, preview, item).status_code == 422
    # Explicit human selection can correct a model unit declaration using the
    # real catalog unit; neither an invented number nor quote is corrected.
    if damage == 'unit':
        assert update(client, headers, preview, item, series_id=SERIES).status_code == 200
    else:
        assert '未通过校验的模型候选' not in preview['markdown']
        assert '"series_id"' not in preview['markdown']
    assert rows() == []


def test_source_quote_can_be_repaired_only_by_exact_original_selection(generator):
    client, headers, _, response, _ = generator
    response['rule_quotes']['watch'] = 'AI invented quotation'
    preview = generate(client, headers).json()
    item = pick(preview, 'thresholds')
    assert update(client, headers, preview, item, source_quote='Made up 12 吨').status_code == 422
    result = update(client, headers, preview, item, source_quote='Stocks above 12 吨')
    assert result.status_code == 200, result.text
    assert pick(result.json(), 'thresholds')['status'] == 'confirmed'
    assert apply(client, headers, result.json()).status_code == 200
    assert rows()[0].thresholds[0]['value'] == 12


def test_invalid_rule_keep_text_and_exclude_never_make_executable_rules(generator):
    client, headers, _, response, _ = generator
    response['judgment']['thresholds'][0]['value'] = 0
    preview = generate(client, headers).json()
    result = update(client, headers, preview, pick(preview, 'thresholds'), 'keep_text')
    assert result.status_code == 200, result.text
    changed = result.json()
    assert pick(changed, 'thresholds')['status'] == 'kept_text'
    assert changed['unresolved'] and '"value"' not in changed['markdown']
    excluded = update(client, headers, changed, pick(changed, 'thresholds'), 'exclude')
    assert excluded.status_code == 200 and not excluded.json()['unresolved'][-1]['text'].startswith('{')
    assert apply(client, headers, excluded.json()).status_code == 200 and rows()[0].thresholds == []


def test_confirm_ready_confirms_verified_items_without_confirming_unsafe_rules(generator):
    client, headers, _, response, _ = generator
    response['judgment']['thresholds'][0]['value'] = 0
    preview = generate(client, headers).json()
    result = client.post(endpoint('/confirm-ready'), headers=headers, json={
        'draft_id': preview['draft_id'], 'revision': preview['review']['revision']})
    assert result.status_code == 200, result.text
    changed = result.json()
    assert changed['review']['confirmed_count'] > 0 and pick(changed, 'thresholds')['status'] == 'pending'
    assert apply(client, headers, changed).status_code == 200 and rows()[0].thresholds == []


def test_model_unit_mismatch_requires_explicit_reselection_and_is_not_batch_confirmed(generator):
    client, headers, _, response, _ = generator
    response['binding_units'][SERIES] = 'kg'
    preview = generate(client, headers).json()
    item = pick(preview, 'thresholds')
    assert not item['can_confirm'] and update(client, headers, preview, item).status_code == 422
    batch = client.post(endpoint('/confirm-ready'), headers=headers, json={
        'draft_id': preview['draft_id'], 'revision': preview['review']['revision']})
    assert batch.status_code == 200
    assert pick(batch.json(), 'thresholds')['status'] == 'pending'
    explicit = update(client, headers, batch.json(), pick(batch.json(), 'thresholds'), series_id=SERIES)
    assert explicit.status_code == 200 and pick(explicit.json(), 'thresholds')['status'] == 'confirmed'


def test_unresolved_can_bind_as_descriptive_evidence_without_becoming_a_rule(generator):
    client, headers, _, _, _ = generator
    preview = generate(client, headers).json()
    item = pick(preview, 'unresolved_binding')
    assert item['can_bind'] and not item['requires_binding'] and item['binding'] is None
    result = update(client, headers, preview, item, series_id=SERIES, remember=True)
    assert result.status_code == 200, result.text
    pending = result.json()['unresolved'][0]
    assert pending['section'] == 'demand' and pending['text'] == 'Orders' and pending['series_id'] == SERIES
    assert len(all_items(result.json())) == len(all_items(preview))
    kept = update(client, headers, result.json(), pick(result.json(), 'unresolved_binding'), 'keep_text')
    assert kept.status_code == 200 and kept.json()['unresolved'][0]['series_id'] is None
    assert len(preferences()) == 1


def test_unknown_series_in_model_unresolved_is_rejected_without_exposing_preview(generator):
    client, headers, _, response, _ = generator
    response['unresolved'][0]['series_id'] = 'foreign-secret'
    result = generate(client, headers)
    assert result.status_code == 422 and rows() == [] and generation.previews.items == {}


def test_user_cannot_submit_arbitrary_rule_or_unknown_series(generator):
    client, headers, _, _, _ = generator
    preview = generate(client, headers).json()
    item = pick(preview, 'thresholds')
    assert update(client, headers, preview, item, series_id='foreign-secret').status_code == 422
    assert update(client, headers, preview, item, rule={'value_literal': '99999'}).status_code == 422
    assert update(client, headers, preview, item, 'keep_text', remember=True).status_code == 422
    assert preferences() == [] and rows() == []


def test_review_expiry_permission_source_and_workbench_changes_are_rejected(generator):
    client, headers, state, _, _ = generator
    preview = generate(client, headers).json()
    token, item = preview['draft_id'], pick(preview, 'binding')
    viewer = {**headers, 'Authorization': 'Bearer source-viewer'}
    assert client.get(endpoint(), headers=viewer, params={'draft_id': token}).status_code == 403
    assert client.get(page(SECOND, '/api/sn/judgment/review'), headers=headers, params={'draft_id': token}).status_code == 409
    state['wb_edit'] = False
    assert update(client, headers, preview, item).status_code == 403
    state['wb_edit'] = True
    state['value'] += 1
    assert update(client, headers, preview, item).status_code == 409
    state['value'] -= 1
    expiry, scope, data = generation.previews.items[token]
    generation.previews.items[token] = (time.monotonic() - 1, scope, data)
    assert client.get(endpoint(), headers=headers, params={'draft_id': token}).status_code == 409


def test_new_saved_version_invalidates_review(generator):
    client, headers, _, _, _ = generator
    first, second = generate(client, headers).json(), generate(client, headers).json()
    assert apply(client, headers, first).status_code == 200
    assert update(client, headers, second, pick(second, 'binding')).status_code == 409


def test_old_tab_apply_revision_is_rejected_and_current_tab_can_save(generator):
    client, headers, _, _, _ = generator
    old = generate(client, headers).json()
    current = update(client, headers, old, pick(old, 'binding')).json()
    body = {'draft_id': old['draft_id'], 'base_version': old['base_version'],
            'review_revision': old['review']['revision']}
    endpoint_apply = page(FIRST, '/api/sn/judgment/apply')
    stale = client.post(endpoint_apply, headers=headers, json=body)
    assert stale.status_code == 409 and rows() == []
    body['review_revision'] = current['review']['revision']
    assert client.post(endpoint_apply, headers=headers, json=body).status_code == 200
    assert len(rows()) == 1 and rows()[0].status == '草稿'


def test_apply_without_review_revision_preserves_existing_draft_contract(generator):
    client, headers, _, _, _ = generator
    preview = generate(client, headers).json()
    assert preview['review']['pending_count'] > 0
    assert apply(client, headers, preview).status_code == 200
    assert len(rows()) == 1 and rows()[0].status == '草稿'


def test_catalog_search_uses_complete_directory_metadata_without_model_or_observations(generator, monkeypatch):
    client, headers, state, _, calls = generator
    original_snapshot, original_page = generation.snapshot, review.catalog_page
    fields = [{'id': 'kb-' + f'{index:048x}', 'name': f'Other field {index}',
        'unit': 'kg', 'source_file': 'Other source.xlsx', 'sheet': 'Other worksheet'} for index in range(240)]
    def directory():
        before = original_snapshot()
        before['catalog'] += fields
        return before
    requests = []
    def paged(scope, **kwargs):
        requests.append(kwargs)
        result = original_page(scope, limit=1, version=kwargs.get('version'))
        complete = result['series'][:1] + [{'series_id': field['id'], 'metric': field['name'],
            **{key: field[key] for key in ('unit', 'source_file', 'sheet')}} for field in fields]
        search = kwargs.get('search', '').casefold()
        filtered = [field for field in complete if not search or search in field.get('metric', '').casefold()]
        start, limit = kwargs.get('offset', 0), kwargs.get('limit', 200)
        return {**result, 'series': filtered[start:start + limit], 'total': len(filtered),
            'next_offset': start + limit if start + limit < len(filtered) else None}
    monkeypatch.setattr(generation, 'snapshot', directory)
    monkeypatch.setattr(review, 'catalog_page', paged)
    preview = generate(client, headers).json()
    count, pages = len(calls), state['pages']
    result = client.get(endpoint('/indicators'), headers=headers,
        params={'draft_id': preview['draft_id'], 'offset': 200, 'limit': 50})
    assert result.status_code == 200 and result.json()['total'] == 241 and len(result.json()['items']) == 41
    assert requests[-1]['offset'] == 200 and requests[-1]['limit'] == 50
    found = client.get(endpoint('/indicators'), headers=headers,
        params={'draft_id': preview['draft_id'], 'search': 'Other field 239'})
    assert found.status_code == 200 and found.json()['items'][0]['name'] == 'Other field 239'
    assert requests[-1]['search'] == 'Other field 239' and requests[-1]['limit'] == 20
    assert client.get(endpoint('/indicators'), headers=headers,
        params={'draft_id': preview['draft_id'], 'limit': 51}).status_code == 422
    assert len(calls) == count and state['pages'] == pages
    selected = update(client, headers, preview, pick(preview, 'binding'), series_id=fields[-1]['id'])
    assert selected.status_code == 200 and pick(selected.json(), 'binding')['binding']['series_id'] == fields[-1]['id']


def test_remember_is_opt_in_reuse_is_pending_and_source_metadata_changes_invalidate(generator, monkeypatch):
    client, headers, _, _, _ = generator
    preview = generate(client, headers).json()
    assert update(client, headers, preview, pick(preview, 'binding')).status_code == 200
    assert preferences() == []
    preview = generate(client, headers).json()
    assert update(client, headers, preview, pick(preview, 'binding'), remember=True).status_code == 200
    assert len(preferences()) == 1
    other = generate(client, headers).json()
    assert not pick(other, 'binding')['remembered']
    reused = reuse(client, headers, other)
    assert reused.status_code == 200, reused.text
    item = pick(reused.json(), 'binding')
    assert item['remembered'] and item['status'] == 'pending'
    disabled = reuse(client, headers, reused.json(), False).json()
    assert not pick(disabled, 'binding')['remembered']
    original = generation.snapshot
    def changed_metadata():
        metadata = original()
        metadata['catalog'] = copy.deepcopy(metadata['catalog'])
        metadata['catalog'][0]['dimensions'] = {'region': 'changed'}
        return metadata
    monkeypatch.setattr(generation, 'snapshot', changed_metadata)
    invalidated = reuse(client, headers, generate(client, headers).json())
    assert invalidated.status_code == 200 and not pick(invalidated.json(), 'binding')['remembered']


def test_preference_key_cannot_cross_user_workbench_variety_or_source_text():
    identity = SimpleNamespace(tenant_id=17, user_id='one')
    scope = SimpleNamespace(identity=identity, workbench_id=FIRST)
    preview = {'variety': 'XY', 'text': 'Synthetic source'}
    item = {'path': ['marginal_focus', 0], 'value': {'desc': 'Quantity', 'series_id': SERIES}}
    key = review.preference_key(scope, preview, item)
    assert review.preference_key(SimpleNamespace(identity=SimpleNamespace(tenant_id=17, user_id='two'), workbench_id=FIRST), preview, item) != key
    assert review.preference_key(SimpleNamespace(identity=identity, workbench_id=SECOND), preview, item) != key
    assert review.preference_key(scope, {**preview, 'variety': 'AB'}, item) != key
    assert review.preference_key(scope, {**preview, 'text': 'Other source'}, item) != key
    assert review.preference_key(scope, preview, {**item, 'path': ['marginal_focus', 1]}) != key


def test_confirmation_does_not_mutate_shared_preview_or_copy_the_catalog(monkeypatch):
    catalog = [{'id': SERIES, 'unit': '吨', 'name': 'Synthetic quantity'}]
    assessment = assess_draft(document(), TEXT, catalog, variety='XY', actor='one')
    preview = {'text': TEXT, 'variety': 'XY', 'written_at': '2026-10-29'}
    review.initialize_review(preview, assessment, catalog)
    before = copy.deepcopy(preview)
    original = review.deepcopy
    def guarded(value):
        assert value is not preview and value is not preview['review'] and value is not preview['review']['catalog']
        return original(value)
    monkeypatch.setattr(review, 'deepcopy', guarded)
    scope = SimpleNamespace(identity=SimpleNamespace(user_id='one'))
    assert not review.confirmation_error(preview, preview['review']['items'][0], catalog, scope)
    assert preview == before


def test_preview_store_rejects_cross_user_token_and_stale_revision():
    identity = SimpleNamespace(tenant_id=17, user_id='one')
    scope = SimpleNamespace(identity=identity, workbench_id=FIRST, knowledge_base_id='kb', knowledge_base_tenant_id=17)
    foreign = SimpleNamespace(**{**vars(scope), 'identity': SimpleNamespace(tenant_id=17, user_id='two')})
    store = generation.PreviewStore()
    token = store.put(scope, {'review': {'revision': 1}})
    with pytest.raises(HTTPException):
        store.get(foreign, token)
    store.replace(scope, token, {'review': {'revision': 2}}, 1)
    with pytest.raises(HTTPException):
        store.replace(scope, token, {'review': {'revision': 3}}, 1)


def test_repair_demotion_preserves_original_typed_rule_for_review(generator, monkeypatch):
    client, headers, _, response, calls = generator
    broken = copy.deepcopy(response)
    broken['judgment']['thresholds'][0]['value'] = 0
    async def complete(scope, model, prompt):
        calls.append(prompt)
        if len(calls) == 1:
            return broken
        repaired = copy.deepcopy(response)
        repaired['judgment']['thresholds'] = []
        repaired['rule_quotes'] = {}
        return repaired
    monkeypatch.setattr(generation, 'completion', complete)
    result = generate(client, headers)
    assert result.status_code == 200, result.text
    item = pick(result.json(), 'thresholds')
    assert item['rule']['value_literal'] == '0' and not item['can_confirm']
    assert update(client, headers, result.json(), item).status_code == 422


def test_missing_real_unit_never_confirms_quantitative_rule(generator, monkeypatch):
    client, headers, _, response, _ = generator
    original = generation.snapshot
    def missing_unit():
        metadata = original()
        metadata['catalog'] = copy.deepcopy(metadata['catalog'])
        metadata['catalog'][0]['unit'] = None
        return metadata
    response['binding_units'][SERIES] = None
    monkeypatch.setattr(generation, 'snapshot', missing_unit)
    preview = generate(client, headers).json()
    result = update(client, headers, preview, pick(preview, 'thresholds'), series_id=SERIES)
    assert result.status_code == 422 and '单位' in result.json()['detail']
    assert rows() == []
