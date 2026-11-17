"""Synthetic model tags: custom desk IDs, no content-based classification."""
from copy import deepcopy
from datetime import datetime, timezone

import pytest

from tests.test_judgment_generation import (
    canonical, generator, generate, apply, rows, TEXT, document,
)
from tests.test_knowledge_facts import FIRST
from tin.integration.workbench_scope import isolated_session_factory
from tin.models import SourceMapping
from tin.web import judgment_generation as generation
from tin.web.research_dimensions import DIMENSIONS_KEY, default_dimensions


def save_custom_dimensions():
    with isolated_session_factory(17, FIRST)() as session:
        session.add(SourceMapping(id=f'{DIMENSIONS_KEY}:{FIRST}', config={'dimensions': [
            {'id': 'desk-flow', 'label': '本台合成流量', 'category': '其他',
             'description': 'Read the full source context for this synthetic desk dimension'}]},
            version=1, confirmed_by='researcher', confirmed_at=datetime.now(timezone.utc)))
        session.commit()


@pytest.mark.parametrize('custom', [False, True])
def test_model_current_dimension_tags_survive_explicit_save(generator, custom):
    client, headers, _, response, calls = generator
    if custom:
        save_custom_dimensions()
    dimension = 'desk-flow' if custom else 'inventory'
    response['judgment']['contradiction']['bull']['evidence'][0]['dimension'] = dimension
    response['judgment']['thresholds'][0]['dimension'] = dimension
    generated = generate(client, headers)
    assert generated.status_code == 200, generated.text
    assert rows() == []
    prompt = calls[1]['prompt']
    assert '【研究框架】' in prompt and '【本研究台的研究维度】' in prompt
    assert (('本台合成流量' in prompt and 'desk-flow（' in prompt) if custom else 'inventory（库存' in prompt)
    assert TEXT in prompt
    result = apply(client, headers, generated.json())
    assert result.status_code == 200, result.text
    row = rows()[0]
    assert row.contradiction['bull']['evidence'][0]['dimension'] == dimension
    assert row.thresholds[0]['dimension'] == dimension
    assert row.source_note == TEXT


@pytest.mark.parametrize('value', ['unknown-dimension', 'Inventory!', '', 7, ['inventory'], None])
def test_invalid_or_missing_model_dimension_remains_unannotated(generator, value):
    client, headers, _, response, _ = generator
    response['judgment']['contradiction']['bull']['evidence'][0]['dimension'] = value
    response['judgment']['thresholds'][0]['dimension'] = value
    generated = generate(client, headers)
    assert generated.status_code == 200, generated.text
    result = apply(client, headers, generated.json())
    assert result.status_code == 200, result.text
    row = rows()[0]
    assert 'dimension' not in row.contradiction['bull']['evidence'][0]
    assert 'dimension' not in row.thresholds[0]


def test_model_dimension_boundary_only_changes_tags_in_all_four_sections():
    source = document()
    source['judgment']['contradiction']['bull']['evidence'][0]['dimension'] = 'inventory'
    source['judgment']['contradiction']['bear']['evidence'] = [
        {'desc': 'Stocks and price in full source context', 'dimension': 'unknown-dimension'}]
    source['judgment']['thresholds'][0]['dimension'] = 'inventory'
    source['judgment']['falsifiers'] = [
        {'id': 'synthetic-rule', 'desc': 'Full context rule', 'dimension': 'Inventory!'}]
    untouched = deepcopy(source)
    result = generation.checked_model_dimensions(source, default_dimensions())
    assert source == untouched
    assert result['judgment']['contradiction']['bull']['evidence'][0]['dimension'] == 'inventory'
    assert result['judgment']['thresholds'][0]['dimension'] == 'inventory'
    assert 'dimension' not in result['judgment']['contradiction']['bear']['evidence'][0]
    assert 'dimension' not in result['judgment']['falsifiers'][0]
    for value in (source, result):
        for viewpoint in value['judgment']['contradiction'].values():
            for evidence in viewpoint.get('evidence', []):
                evidence.pop('dimension', None)
        for kind in ('thresholds', 'falsifiers'):
            for rule in value['judgment'][kind]:
                rule.pop('dimension', None)
    assert result == source


def test_dimension_configuration_changes_require_fresh_model_interpretation(generator):
    client, headers, _, response, calls = generator
    response['judgment']['thresholds'][0]['dimension'] = 'inventory'
    first = generate(client, headers)
    assert first.status_code == 200, first.text
    count = len(calls)
    save_custom_dimensions()
    response['judgment']['thresholds'][0]['dimension'] = 'desk-flow'
    second = generate(client, headers)
    assert second.status_code == 200, second.text
    assert len(calls) > count and 'desk-flow（' in calls[count + 1]['prompt']
    assert second.json()['analysis_reused'] is False
