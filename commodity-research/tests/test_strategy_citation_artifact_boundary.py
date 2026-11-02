"""Generic artifact writes must use the same authoritative citation resolver."""
import asyncio
import json
from types import SimpleNamespace

import pytest
from fastapi import HTTPException

from tin.web import workbench_artifacts as artifacts


SCOPE = SimpleNamespace(identity=SimpleNamespace(can_write=True), can_edit=True)


def body(value):
    return artifacts.ArtifactInput(title='Research strategy', body=json.dumps(value), source_ids=[])


def test_generic_artifact_rebuilds_authored_citation_snapshots(monkeypatch):
    from tin.web import strategy_citations
    calls = []
    untrusted = {'format': 'research-strategy-card.v1', 'citations': [
        {'field': 'trigger', 'kind': 'indicator', 'series_id': 'synthetic-field',
         'source_version': 'current', 'snapshot': {'title': 'Forged authority'}}]}
    expected = {'format': 'research-strategy-card.v1', 'citations': [
        {**untrusted['citations'][0], 'snapshot': {'title': 'Current authorized metadata'}}]}

    async def normalize(scope, card):
        assert scope is SCOPE
        calls.append(card)
        return expected

    monkeypatch.setattr(strategy_citations, 'normalize_card_citations', normalize)
    values, sources = asyncio.run(artifacts.validated_input(SCOPE, body(untrusted)))
    assert calls == [untrusted]
    assert json.loads(values['body']) == expected
    assert sources == []


def test_stale_or_foreign_reference_aborts_generic_publication(monkeypatch):
    from tin.web import strategy_citations

    async def reject(scope, card):
        raise HTTPException(409, '引用来源已变化，请重新选择')

    monkeypatch.setattr(strategy_citations, 'normalize_card_citations', reject)
    with pytest.raises(HTTPException) as error:
        asyncio.run(artifacts.validated_input(SCOPE, body({'format': 'research-strategy-card.v1',
                                                        'citations': []})))
    assert error.value.status_code == 409


def test_legacy_prose_and_old_cards_without_citations_do_not_trigger_source_reads(monkeypatch):
    from tin.web import strategy_citations

    async def unexpected(*args):
        raise AssertionError('No new citation was requested')

    monkeypatch.setattr(strategy_citations, 'normalize_card_citations', unexpected)
    legacy = {'format': 'research-strategy-card.v1', 'thesis': 'Original authored text'}
    values, sources = asyncio.run(artifacts.validated_input(SCOPE, body(legacy)))
    assert json.loads(values['body']) == legacy
    assert sources == []


def test_citation_metadata_has_a_total_byte_budget(monkeypatch):
    from tin.web import strategy_citations

    async def oversized(*args):
        return {'format': 'research-strategy-card.v1', 'citations': [], 'thesis': '字' * 50000}

    monkeypatch.setattr(strategy_citations, 'normalize_card_citations', oversized)
    with pytest.raises(HTTPException) as error:
        asyncio.run(artifacts.validated_input(SCOPE, body({'format': 'research-strategy-card.v1',
                                                        'citations': []})))
    assert error.value.status_code == 413


def test_generic_card_route_cannot_bypass_readonly_workbench(monkeypatch):
    from tin.web import strategy_citations

    async def unexpected(*args):
        raise AssertionError('A readonly workbench cannot resolve a publication')

    monkeypatch.setattr(strategy_citations, 'normalize_card_citations', unexpected)
    readonly = SimpleNamespace(identity=SimpleNamespace(can_write=True), can_edit=False)
    with pytest.raises(HTTPException) as error:
        asyncio.run(artifacts.validated_input(readonly, body({'format': 'research-strategy-card.v1',
                                                           'citations': []})))
    assert error.value.status_code == 403
