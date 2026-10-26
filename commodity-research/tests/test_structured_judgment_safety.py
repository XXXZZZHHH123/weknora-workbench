"""The typed import remains bounded human research, never a facts/permission bypass."""
from copy import deepcopy
from dataclasses import replace
from datetime import datetime, timedelta, timezone

import pytest

from tin.config import SHANGHAI
from tin.web import judgment_structured as structured
from tests.test_knowledge_facts import canonical, FIRST, SECOND, SERIES, page
from tests.test_structured_judgment_import import payload, path, saved


def preview(client, headers, document=None):
    response = client.post(path('/structured/preview'), headers=headers,
                           json={'payload': document or payload()})
    assert response.status_code == 200, response.text
    return response.json()


def apply(client, headers, draft, workbench=FIRST):
    return client.post(page(workbench, '/api/sn/judgment/structured/apply'), headers=headers,
        json={'draft_id': draft['draft_id'], 'base_version': draft['base_version']})


def rule():
    return {'id': 'watch', 'name': 'Explicit manual condition', 'series_id': SERIES,
            'operator': '>', 'value': 12, 'type': '观察位', 'horizon': '短期'}


def falsifier():
    return {'id': 'review', 'desc': 'Explicit manual invalidation', 'series_id': SERIES,
            'expression': 'value > 12', 'unit_confirmed': '吨', 'side': 'bull',
            'consecutive_periods': 2}


def test_apply_rechecks_fresh_workbench_write_permission_even_when_sources_are_unchanged(canonical, monkeypatch):
    client, headers, *_ = canonical
    draft = preview(client, headers)
    original = structured.resolve_workbench

    async def downgraded(*args, **kwargs):
        verified = await original(*args, **kwargs)
        # This downgrade happens after request authorization. Source inventory,
        # IDs, revision, tenant and user remain exactly the same.
        return replace(verified, metadata={**verified.metadata, 'can_edit': False})
    monkeypatch.setattr(structured, 'resolve_workbench', downgraded)
    response = apply(client, headers, draft)
    assert response.status_code == 403, response.text
    assert saved() == []


def test_preview_token_cannot_be_applied_to_another_workbench_with_the_same_source(canonical):
    client, headers, *_ = canonical
    draft = preview(client, headers)
    assert apply(client, headers, draft, SECOND).status_code == 409
    assert saved() == []


def test_changed_source_snapshot_between_preview_and_apply_requires_a_new_preview(canonical):
    client, headers, state, _ = canonical
    draft = preview(client, headers)
    state['value'] += 1
    response = apply(client, headers, draft)
    assert response.status_code == 409, response.text
    assert saved() == []


def test_streamed_oversize_body_is_rejected_before_json_decoding_or_fact_reads(canonical):
    client, headers, state, _ = canonical
    def chunks():
        yield b'{'  # Intentionally not valid JSON: size must win over decoding.
        for _ in range(5):
            yield b'x' * (32 * 1024)
    response = client.post(path('/structured/preview'),
        headers={**headers, 'Content-Type': 'application/json'}, content=chunks())
    assert response.status_code == 413, response.text
    assert not any(endpoint == '/api/research/facts' for _, endpoint in state['calls'])
    assert saved() == []


def test_typed_payload_has_its_own_smaller_budget(canonical):
    client, headers, state, _ = canonical
    document = payload(); document['source_note'] = 'x' * (49 * 1024)
    response = client.post(path('/structured/preview'), headers=headers, json={'payload': document})
    assert response.status_code == 413, response.text
    assert not any(endpoint == '/api/research/facts' for _, endpoint in state['calls'])
    assert saved() == []


@pytest.mark.parametrize('value', ['NaN', 'Infinity', '-Infinity'])
def test_nonfinite_manual_rule_values_are_not_saved_as_silent_false_conditions(canonical, value):
    client, headers, *_ = canonical
    document = payload(); document['thresholds'] = [{**rule(), 'value': value}]
    response = client.post(path('/structured/preview'), headers=headers, json={'payload': document})
    assert response.status_code == 422, response.text
    assert saved() == []


@pytest.mark.parametrize('unit', [None, '', ' ', '—'])
def test_quantitative_rule_requires_a_real_nonblank_catalog_unit(canonical, monkeypatch, unit):
    client, headers, *_ = canonical
    document = payload(); document['thresholds'] = [rule()]
    original = structured._state
    def catalog_unit(*args, **kwargs):
        state = original(*args, **kwargs)
        state = {**state, 'catalog': deepcopy(state['catalog'])}
        state['catalog'][SERIES]['unit'] = unit
        return state
    monkeypatch.setattr(structured, '_state', catalog_unit)
    response = client.post(path('/structured/preview'), headers=headers, json={'payload': document})
    assert response.status_code == 422, response.text
    assert saved() == []


def test_falsifier_unit_must_match_the_bound_real_indicator(canonical):
    client, headers, *_ = canonical
    document = payload(); document['falsifiers'] = [{**falsifier(), 'unit_confirmed': '件'}]
    response = client.post(path('/structured/preview'), headers=headers, json={'payload': document})
    assert response.status_code == 422, response.text
    assert saved() == []


def test_manual_falsifier_window_must_fit_the_existing_bounded_reader(canonical):
    client, headers, *_ = canonical
    document = payload(); document['falsifiers'] = [{**falsifier(), 'consecutive_periods': 121}]
    response = client.post(path('/structured/preview'), headers=headers, json={'payload': document})
    assert response.status_code == 422, response.text
    assert saved() == []


def test_falsifier_expression_target_cannot_overflow_into_infinity(canonical):
    client, headers, *_ = canonical
    document = payload(); document['falsifiers'] = [{**falsifier(), 'expression': 'value > ' + '9' * 400}]
    response = client.post(path('/structured/preview'), headers=headers, json={'payload': document})
    assert response.status_code == 422, response.text
    assert saved() == []


@pytest.mark.parametrize('identifier', ['', 'x' * 81, 'watch\n'])
def test_manual_rule_local_ids_fit_the_existing_storage_protocol(canonical, identifier):
    client, headers, *_ = canonical
    document = payload(); document['thresholds'] = [{**rule(), 'id': identifier}]
    response = client.post(path('/structured/preview'), headers=headers, json={'payload': document})
    assert response.status_code == 422, response.text
    assert saved() == []


@pytest.mark.parametrize('change', ['future_date', 'overflow_review', 'blank_variety', 'long_variety', 'control_variety'])
def test_manual_metadata_cannot_break_later_rendering_or_overflow_storage_dates(canonical, change):
    client, headers, *_ = canonical
    document = payload()
    if change == 'future_date':
        document['written_at'] = (datetime.now(SHANGHAI).date() + timedelta(days=1)).isoformat()
    elif change == 'overflow_review':
        document['review_period_days'] = 10 ** 12
    elif change == 'blank_variety':
        document['variety'] = ' '
    elif change == 'long_variety':
        document['variety'] = 'A' * 17
    else:
        document['variety'] = 'XY\n'
    response = client.post(path('/structured/preview'), headers=headers, json={'payload': document})
    assert response.status_code == 422, response.text
    assert saved() == []


@pytest.mark.parametrize('place', ['evidence', 'cost', 'cost_item'])
def test_a_real_series_id_does_not_authorize_injected_fact_values(canonical, place):
    client, headers, *_ = canonical
    document = payload()
    if place == 'evidence':
        document['contradiction']['bull']['evidence'][0].update(value=17.5, as_of='2026-01-02', source='source label')
    elif place == 'cost':
        document['cost_line'] = {'value': 17.5, 'source': 'source label'}
    else:
        document['cost_line'] = {'items': [{'desc': 'Unverified cost', 'series_id': SERIES, 'value': 17.5}]}
    response = client.post(path('/structured/preview'), headers=headers, json={'payload': document})
    assert response.status_code == 422, response.text
    assert saved() == []


def test_created_timestamp_and_actor_are_server_values_not_historical_document_metadata(canonical):
    client, headers, *_ = canonical
    before = datetime.now(timezone.utc)
    draft = preview(client, headers)
    response = apply(client, headers, draft)
    assert response.status_code == 200, response.text
    row = saved()[0]
    assert before <= row.created_at <= datetime.now(timezone.utc)
    assert row.author == 'researcher' and row.written_at.isoformat() == payload()['written_at']
    assert row.status == '草稿'
