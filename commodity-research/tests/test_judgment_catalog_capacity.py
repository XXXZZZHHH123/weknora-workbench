"""Large, synthetic research directories keep complete metadata within request budgets."""
import asyncio
from copy import deepcopy
import json

from fastapi import HTTPException
import pytest

from tin.web import judgment_generation as generation


TEXT = 'Review the whole supplied research scope, including conflicting evidence and missing data.'


def directory(count=2372):
    return [{'id': 'kb-' + f'{index:048x}', 'name': f'测量字段{index} / 分组{index % 31}',
             'unit': '件' if index % 3 else None, 'frequency': '日',
             'sheet': '真实工作表' + str(index % 80),
             'dimensions': {'block': index % 12, 'data_region': '实际记录区域' + str(index % 8)},
             'source_file': 'actual-source-' + str(index % 4) + '-' + 'x' * 70 + '.xlsx',
             'source_kind': 'primary' if index % 4 else 'macro',
             'knowledge_base_id': 'library-' + str(index % 2) + '-' + 'a' * 30,
             'source_version': ('a' if index % 2 else 'b') * 64}
            for index in range(count)]


def encoded_directory(prompt):
    return json.loads(prompt.split('真实字段目录：', 1)[1].split('\n完整原文：', 1)[0])


def restore(encoded):
    if isinstance(encoded, list):
        return encoded
    if encoded.get('format') == 'research-selection-flat.v1':
        return list(encoded['indicators'].values())
    if encoded.get('format') in {'research-catalog-columns.v1', 'research-selection-columns.v1'}:
        columns, dictionaries = encoded['columns'], encoded['dictionaries']
        restored = []
        rows = encoded['indicators'].values() if isinstance(encoded['indicators'], dict) else encoded['indicators']
        for row in rows:
            item = dict(zip(columns[:2], row[:2]))
            for column, reference in zip(columns[2:], row[2:]):
                if reference != -1:
                    item[column] = dictionaries[column][reference]
            restored.append(item)
        return restored
    return [{**{key: value for key, value in field.items() if key != 'source_ref'},
             **encoded['sources'][field['source_ref']]} for field in encoded['indicators']]


def selected_refs(encoded, ids):
    """Independent test-side lookup of explicit references to the real fields."""
    return [ref for ref, field in zip(encoded['indicators'], restore(encoded)) if field['id'] in ids]


def test_large_repeated_metadata_directory_reaches_complete_bounded_model_selection(monkeypatch):
    catalog = directory()
    before = deepcopy(catalog)
    assert len(generation.catalog_json(catalog).encode()) > 4 * 96 * 1024
    seen, calls, selected, seen_refs = [], [], [], []

    async def choose(scope, model, prompt):
        batch = restore(encoded_directory(prompt))
        assert TEXT in prompt
        assert len(prompt.encode()) <= generation.PROMPT_BYTES
        assert len(json.dumps(encoded_directory(prompt), ensure_ascii=False,
                              separators=(',', ':')).encode()) <= 96 * 1024
        seen.extend(batch)
        refs = list(encoded_directory(prompt)['indicators'])
        assert set(refs).isdisjoint(seen_refs)
        seen_refs.extend(refs)
        calls.append(prompt)
        selected.append(batch[-1])
        return {'selection_refs': selected_refs(encoded_directory(prompt), [batch[-1]['id']])}

    monkeypatch.setattr(generation, 'completion', choose)
    candidates, partial = asyncio.run(generation.choose_catalog(None, 'model', catalog, TEXT))
    assert partial is True and 1 < len(calls) <= 4
    assert seen == catalog and candidates == selected and catalog == before
    assert all(encoded_directory(prompt)['format'] == 'research-selection-columns.v1' for prompt in calls)
    assert all('字典索引不是指标ID' in prompt for prompt in calls)
    # Reasons are written for researchers, with decoded values rather than directory encoding.
    assert all('不提目录格式、字典或索引' in prompt for prompt in calls)
    assert seen_refs == [f'r{index + 1}' for index in range(len(catalog))]
    assert all(set(item) == set(catalog[0]) for item in candidates)


def test_short_references_resolve_exact_real_ids_without_mutating_or_guessing():
    batch = directory(3)
    before = deepcopy(batch)
    refs = ('r41', 'r42', 'r43')
    assert generation.selected_catalog_ids({'selection_refs': ['r43', 'r41']}, batch, refs) == [
        batch[2]['id'], batch[0]['id']]
    assert generation.selected_catalog_ids({'selection_refs': []}, batch, refs) == []
    assert batch == before
    assert generation.selected_catalog_ids({'selection_refs': [f'r{index + 1}' for index in range(32)]},
        directory(32), tuple(f'r{index + 1}' for index in range(32))) == [row['id'] for row in directory(32)]


@pytest.mark.parametrize('count', [96, 98])
def test_explicit_reference_selection_is_not_capped_by_the_requested_batch_count(count):
    # A model observed returning 98 valid references must not lose the whole
    # analysis; capacity is enforced on the combined candidates instead.
    batch = directory(count)
    before = deepcopy(batch)
    refs = tuple(f'r{index + 1}' for index in range(count))
    response = {'selection_refs': list(refs)}
    assert generation.selected_catalog_ids(response, batch, refs) == [row['id'] for row in batch]
    assert batch == before and response['selection_refs'] == list(refs)


def test_bare_reference_array_states_the_same_selection_and_is_checked_the_same_way():
    batch = directory(3)
    refs = ('r41', 'r42', 'r43')
    assert generation.selected_catalog_ids(['r43', 'r41'], batch, refs) == [batch[2]['id'], batch[0]['id']]
    assert generation.selected_catalog_ids([], batch, refs) == []
    for response in (['r44'], ['r41', 'r41'], [41], ['kb-' + '0' * 48], [{'selection_refs': ['r41']}]):
        with pytest.raises(HTTPException) as caught:
            generation.selected_catalog_ids(response, batch, refs)
        assert caught.value.status_code == 422


def test_combined_candidates_remain_bounded_after_generous_batch_selections(monkeypatch):
    catalog = directory()

    async def everything(scope, model, prompt):
        return list(encoded_directory(prompt)['indicators'])

    monkeypatch.setattr(generation, 'completion', everything)
    with pytest.raises(HTTPException) as caught:
        asyncio.run(generation.choose_catalog(None, 'model', catalog, TEXT))
    # Observed: a small model kept 1931 of 2372 fields. Say so instead of
    # asking the researcher to shorten an unchanged source.
    assert caught.value.status_code == 422 and caught.value.detail == generation.SELECTION_TOO_BROAD
    from tin.web.judgment_failures import failure_for
    assert '换用理解能力更强的模型' in failure_for(caught.value, 'selecting')['message']


@pytest.mark.parametrize('response', [
    {'series_ids': ['kb-' + '0' * 48]},
    {'selection_refs': ['kb-' + '0' * 48]},
    {'selection_refs': ['kb-' + '0' * 45]},
    {'selection_refs': ['r41', 'r41']},
    {'selection_refs': ['r40']}, {'selection_refs': ['r44']},
    {'selection_refs': ['R41']}, {'selection_refs': ['r041']},
    {'selection_refs': [41]}, {'selection_refs': [True]},
    {'selection_refs': [None]}, {'selection_refs': 'r41'},
    {'selection_refs': ['r41'], 'extra': []},
])
def test_short_reference_resolution_never_accepts_ids_typo_cross_batch_or_wrong_shape(response):
    batch = directory(3)
    before = deepcopy(batch)
    with pytest.raises(HTTPException) as caught:
        generation.selected_catalog_ids(response, batch, ('r41', 'r42', 'r43'))
    assert caught.value.status_code == 422 and batch == before


def test_dictionary_encoding_keeps_absence_null_types_and_untrusted_literals_distinct():
    catalog = directory(320)
    for index, field in enumerate(catalog):
        field['dimensions'] = {'quote': '\"\\\n<untrusted>', 'values': [None, False, 0, '', {'a': '字' * 50}]}
        if index % 2:
            del field['frequency']
        if index % 3:
            field['unit'] = None
        elif index % 4:
            field['unit'] = ''
    prompt = generation.make_prompt('Protocol', catalog, TEXT)
    encoded = encoded_directory(prompt)
    assert isinstance(encoded, dict) and encoded.get('format') == 'research-catalog-columns.v1'
    assert restore(encoded) == catalog
    assert all(row[0] == field['id'] and row[1] == field['name']
               for row, field in zip(encoded['indicators'], catalog))
    assert '不提目录格式、字典、索引或列序号' in prompt


@pytest.mark.parametrize('kind', ['single-field', 'five-batches', 'full-prompt'])
def test_all_capacity_failures_are_preflighted_before_the_first_model_call(monkeypatch, kind):
    catalog, text = directory(2), TEXT
    if kind == 'single-field':
        catalog[0]['name'] = '字' * 60000
    elif kind == 'five-batches':
        catalog = [{'id': 'real-' + str(index), 'name': '字' * 25000 + str(index), 'unit': '件'}
                   for index in range(5)]
    else:
        monkeypatch.setattr(generation, 'PROMPT_BYTES', 1024)
    calls = []

    async def forbidden(*args):
        calls.append(args)
        raise AssertionError('All complete selection requests must fit before the first model call')

    monkeypatch.setattr(generation, 'completion', forbidden)
    with pytest.raises(HTTPException) as caught:
        asyncio.run(generation.choose_catalog(None, 'model', catalog, text))
    assert caught.value.status_code == 413 and calls == []


@pytest.mark.parametrize('kind', ['invented', 'another-batch'])
def test_large_selection_still_rejects_unknown_and_cross_batch_ids(monkeypatch, kind):
    catalog, calls = directory(), []

    async def invalid(scope, model, prompt):
        fields = restore(encoded_directory(prompt))
        calls.append(prompt)
        if kind == 'invented':
            return {'selection_refs': ['not-a-real-reference']}
        assert catalog[-1]['id'] not in {item['id'] for item in fields}
        return {'selection_refs': [f'r{len(catalog)}']}

    monkeypatch.setattr(generation, 'completion', invalid)
    with pytest.raises(HTTPException) as caught:
        asyncio.run(generation.choose_catalog(None, 'model', catalog, TEXT))
    assert caught.value.status_code == 422 and len(calls) == 1
