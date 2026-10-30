"""Synthetic API coverage failures never freeze a partial interpretation."""
from copy import deepcopy

from fastapi import HTTPException
import pytest
from sqlalchemy import select

from tests.test_judgment_generation import (generator, TEXT, endpoint, generate,
    rows, inventory_fixture, covered_fixture)
from tests.test_knowledge_facts import canonical, FIRST
from tin.integration.workbench_scope import isolated_session_factory
from tin.models import SourceMapping
from tin.web import judgment_generation as generation
from tin.web.judgment_analysis_cache import KIND


def snapshots():
    with isolated_session_factory(17, FIRST)() as session:
        return [deepcopy(row.config) for row in session.scalars(select(SourceMapping))
                if row.config.get('kind') == KIND]


def test_missing_inventory_block_is_not_cached_and_valid_retry_can_run(generator, monkeypatch):
    client, headers, _, response, calls = generator
    broken = {'blocks': [], 'items': []}

    async def completion(scope, model, prompt):
        calls.append(prompt)
        if prompt.startswith('research-source-inventory.v1'):
            return broken if len(calls) == 1 else inventory_fixture()
        return covered_fixture(response)

    monkeypatch.setattr(generation, 'completion', completion)
    first = generate(client, headers)
    assert first.status_code == 422, first.text
    assert snapshots() == [] and rows() == []
    second = generate(client, headers)
    assert second.status_code == 200, second.text
    assert len(calls) == 4
    assert len(snapshots()) == 1 and snapshots()[0]['state'] == 'ready'


@pytest.mark.parametrize('status', [422, 429, 503])
def test_unavailable_independent_review_returns_partial_preview_but_does_not_cache_it(generator, monkeypatch, status):
    client, headers, _, response, calls = generator

    async def completion(scope, model, prompt):
        calls.append(prompt)
        if prompt.startswith('research-source-inventory.v1'):
            return inventory_fixture()
        if len(calls) == 3:
            raise HTTPException(status, 'Synthetic unavailable review')
        return covered_fixture(response)

    monkeypatch.setattr(generation, 'completion', completion)
    first = generate(client, headers)
    assert first.status_code == 200, first.text
    assert any(issue['code'] == 'repair_unavailable' for issue in first.json()['validation_issues'])
    assert snapshots() == [] and rows() == []
    second = generate(client, headers)
    assert second.status_code == 200, second.text
    assert len(calls) == 6
    assert len(snapshots()) == 1


def test_analysis_that_omits_coverage_protocol_is_rejected_without_pinning_it(generator, monkeypatch):
    client, headers, _, response, calls = generator

    async def completion(scope, model, prompt):
        calls.append(prompt)
        return inventory_fixture() if prompt.startswith('research-source-inventory.v1') else deepcopy(response)

    monkeypatch.setattr(generation, 'completion', completion)
    result = generate(client, headers)
    assert result.status_code == 422, result.text
    assert snapshots() == [] and rows() == [] and len(calls) == 3


def test_model_cannot_invent_source_currency_to_suppress_a_binding_warning(generator, monkeypatch):
    client, headers, _, response, calls = generator

    async def completion(scope, model, prompt):
        calls.append(prompt)
        if prompt.startswith('research-source-inventory.v1'):
            value = inventory_fixture()
            value['items'][0]['source_unit'] = 'USD/ton'
            return value
        return covered_fixture(response)

    monkeypatch.setattr(generation, 'completion', completion)
    result = generate(client, headers)
    assert result.status_code == 422, result.text
    assert snapshots() == [] and rows() == [] and len(calls) == 1


def test_review_preparation_failure_is_readable_and_cleans_pending_snapshot(generator, monkeypatch):
    from tin.judgments.analysis import AnalysisError
    from tin.web import judgment_review
    client, headers, _, _, calls = generator
    initialize = judgment_review.initialize_review
    attempts = []

    def fail_once(*args, **kwargs):
        attempts.append(True)
        if len(attempts) == 1:
            raise AnalysisError('Synthetic review preparation failed', code='synthetic_review_failure')
        return initialize(*args, **kwargs)

    monkeypatch.setattr(judgment_review, 'initialize_review', fail_once)
    first = generate(client, headers)
    assert first.status_code == 422, first.text
    assert snapshots() == [] and rows() == []
    second = generate(client, headers)
    assert second.status_code == 200, second.text
    assert len(calls) == 6 and len(snapshots()) == 1


def test_oversized_source_inventory_returns_readable_capacity_error_before_model_calls(generator):
    client, headers, _, _, calls = generator
    result = client.post(endpoint('/generate'), headers=headers, json={
        'model_id': 'synthetic-chat', 'text': '\n'.join('Supplied line.' for _ in range(513)), 'base_version': 0})
    assert result.status_code in {413, 422}, result.text
    assert snapshots() == [] and rows() == [] and calls == []
