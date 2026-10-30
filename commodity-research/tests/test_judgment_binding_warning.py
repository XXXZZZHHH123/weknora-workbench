"""Binding warnings require an explicit acknowledgement of the exact selection."""
import copy
import pytest

from sqlalchemy import select

from tests.test_judgment_generation import generator, generate, rows
from tests.test_judgment_review import update, pick, endpoint, all_items
from tests.test_knowledge_facts import canonical, SERIES, FIRST
from tin.integration.workbench_scope import isolated_session_factory
from tin.models import AuditLog
from tin.web import judgment_generation as generation
from tin.web import judgment_review as review


def test_new_manual_binding_warns_without_mutating_preview_or_audit(generator):
    client, headers, _, _, _ = generator
    preview = generate(client, headers).json()
    item = pick(preview, 'unresolved_binding')
    response = update(client, headers, preview, item, series_id=SERIES)
    assert response.status_code == 422
    warning = response.json()['detail']
    assert warning['code'] == 'binding_warning' and warning['confirmation_token']
    assert '原文口径尚未核对' in warning['message'] and '吨' in warning['message']
    assert '不自动换算' in warning['message']
    current = client.get(endpoint(), headers=headers, params={'draft_id': preview['draft_id']}).json()
    assert current['revision'] == preview['review']['revision']
    assert next(row for section in current['sections'] for row in section['items'] if row['id'] == item['id'])['binding'] is None
    with isolated_session_factory(17, FIRST)() as session:
        assert not session.scalars(select(AuditLog).where(AuditLog.action == '研判逐项确认')).all()
    assert rows() == []


def test_acknowledged_binding_keeps_raw_selection_and_does_not_warn_again(generator):
    client, headers, _, _, _ = generator
    preview = generate(client, headers).json()
    item = pick(preview, 'unresolved_binding')
    warning = update(client, headers, preview, item, series_id=SERIES).json()['detail']
    response = update(client, headers, preview, item, series_id=SERIES,
        binding_warning_token=warning['confirmation_token'])
    assert response.status_code == 200, response.text
    changed = response.json()
    confirmed = pick(changed, 'unresolved_binding')
    assert confirmed['binding']['series_id'] == SERIES and confirmed['binding']['unit'] == '吨'
    assert update(client, headers, changed, confirmed, series_id=SERIES).status_code == 200
    with isolated_session_factory(17, FIRST)() as session:
        audit = session.scalars(select(AuditLog).where(AuditLog.action == '研判逐项确认')).first()
        assert audit.detail['binding_warning']['series_id'] == SERIES
        assert audit.detail['binding_warning']['unit'] == '吨'
        assert audit.detail['binding_warning']['confirmed_by'] == 'researcher'


def test_warning_acknowledgement_cannot_be_forged_or_reused_for_another_revision(generator):
    client, headers, _, _, _ = generator
    preview = generate(client, headers).json(); item = pick(preview, 'unresolved_binding')
    first = update(client, headers, preview, item, series_id=SERIES).json()['detail']
    for value in ['forged', '伪造确认']:
        forged = update(client, headers, preview, item, series_id=SERIES, binding_warning_token=value)
        assert forged.status_code == 422 and forged.json()['detail']['code'] == 'binding_warning'
    updated = update(client, headers, preview, pick(preview, 'binding')).json()
    replay = update(client, headers, updated, item, series_id=SERIES,
        binding_warning_token=first['confirmation_token'])
    assert replay.status_code == 422 and replay.json()['detail']['code'] == 'binding_warning'


def test_rebinding_reports_known_unit_difference_and_warning_does_not_fix_invalid_numbers(generator, monkeypatch):
    client, headers, _, response, _ = generator
    other = 'synthetic-other-unit'
    original = generation.snapshot
    def snapshot(*args, **kwargs):
        state = original(*args, **kwargs)
        state['catalog'] = copy.deepcopy(state['catalog']) + [{'id': other, 'name': 'Other quantity', 'unit': 'kg'}]
        return state
    monkeypatch.setattr(generation, 'snapshot', snapshot)
    response['judgment']['thresholds'][0]['value'] = 99999
    preview = generate(client, headers).json()
    item = pick(preview, 'thresholds')
    warning_response = update(client, headers, preview, item, series_id=other)
    assert warning_response.status_code == 422
    warning = warning_response.json()['detail']
    assert warning['code'] == 'binding_warning'
    assert '吨' in warning['message'] and 'kg' in warning['message']
    attempted = update(client, headers, preview, item, series_id=other,
        binding_warning_token=warning['confirmation_token'])
    assert attempted.status_code == 422 and attempted.json()['detail'].get('code') != 'binding_warning'
    assert rows() == []


def test_warning_token_is_bound_to_item_and_selected_field(generator, monkeypatch):
    client, headers, _, response, _ = generator
    response['unresolved'].append({'section': 'another source', 'text': 'Other observation', 'reason': 'Missing field'})
    original = generation.snapshot
    other = 'synthetic-second-selection'
    def snapshot(*args, **kwargs):
        state = original(*args, **kwargs)
        state['catalog'] = copy.deepcopy(state['catalog']) + [{'id': other, 'name': 'Different quantity', 'unit': '吨'}]
        return state
    monkeypatch.setattr(generation, 'snapshot', snapshot)
    preview = generate(client, headers).json()
    first, second = [item for item in all_items(preview) if item['kind'] == 'unresolved_binding']
    warning = update(client, headers, preview, first, series_id=SERIES).json()['detail']
    for item, series in [(second, SERIES), (first, other)]:
        attempt = update(client, headers, preview, item, series_id=series,
            binding_warning_token=warning['confirmation_token'])
        assert attempt.status_code == 422 and attempt.json()['detail']['code'] == 'binding_warning'
        assert attempt.json()['detail']['confirmation_token'] != warning['confirmation_token']
    assert rows() == []


def test_preview_private_signing_key_survives_worker_change_and_is_not_returned(generator, monkeypatch):
    client, headers, _, _, _ = generator
    preview = generate(client, headers).json(); item = pick(preview, 'unresolved_binding')
    assert 'binding_warning_secret' not in str(preview)
    warning = update(client, headers, preview, item, series_id=SERIES).json()['detail']
    monkeypatch.setattr(review, '_binding_warning_secret', b'another-process-secret')
    result = update(client, headers, preview, item, series_id=SERIES,
        binding_warning_token=warning['confirmation_token'])
    assert result.status_code == 200, result.text


def test_verified_original_unit_avoids_unnecessary_warning_and_describes_real_mismatch(generator, monkeypatch):
    client, headers, _, _, _ = generator
    original = generation.snapshot
    other = 'synthetic-other-original-unit'
    def snapshot(*args, **kwargs):
        state = original(*args, **kwargs)
        state['catalog'] = copy.deepcopy(state['catalog']) + [{'id': other, 'name': 'Other quantity', 'unit': 'kg'}]
        return state
    monkeypatch.setattr(generation, 'snapshot', snapshot)
    for series_id, expected in [(SERIES, 200), (other, 422)]:
        preview = generate(client, headers).json(); item = pick(preview, 'unresolved_binding')
        stored = generation.previews.items[preview['draft_id']][2]
        stored['source_coverage'] = {'items': [{'candidate_path': '/unresolved/0', 'path': '/unresolved/99',
            'quote': 'Stocks above 12 吨', 'source_unit': '吨'}]}
        result = update(client, headers, preview, item, series_id=series_id)
        assert result.status_code == expected, result.text
        if expected == 422:
            assert '原文单位为“吨”' in result.json()['detail']['message']
            assert 'kg' in result.json()['detail']['message']


@pytest.mark.parametrize('snapshot', ['legacy', None, 'stable-interpretation'])
def test_preview_reports_partial_analysis_without_claiming_completion(generator, snapshot):
    client, headers, _, _, _ = generator
    preview = generate(client, headers).json()
    stored = generation.previews.items[preview['draft_id']][2]
    if snapshot == 'legacy':
        stored.pop('analysis_snapshot', None)
    else:
        stored['analysis_snapshot'] = snapshot
    stored['analysis_reused'] = snapshot != 'legacy'
    response = update(client, headers, preview, pick(preview, 'binding'))
    assert response.status_code == 200, response.text
    value = response.json()
    assert value['analysis_complete'] is (snapshot is not None)
    if snapshot is None:
        assert value['notice'] == '部分原文尚未完成核对，已保留可用结果。'
    elif snapshot == 'stable-interpretation':
        assert value['notice'] == '已复用相同原文的解析结果。'
