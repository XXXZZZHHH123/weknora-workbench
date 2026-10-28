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
    if encoded.get('format') == 'research-catalog-columns.v1':
        columns, dictionaries = encoded['columns'], encoded['dictionaries']
        restored = []
        for row in encoded['indicators']:
            item = dict(zip(columns[:2], row[:2]))
            for column, reference in zip(columns[2:], row[2:]):
                if reference != -1:
                    item[column] = dictionaries[column][reference]
            restored.append(item)
        return restored
    return [{**{key: value for key, value in field.items() if key != 'source_ref'},
             **encoded['sources'][field['source_ref']]} for field in encoded['indicators']]


def test_large_repeated_metadata_directory_reaches_complete_bounded_model_selection(monkeypatch):
    catalog = directory()
    before = deepcopy(catalog)
    assert len(generation.catalog_json(catalog).encode()) > 4 * 96 * 1024
    seen, calls, selected = [], [], []

    async def choose(scope, model, prompt):
        batch = restore(encoded_directory(prompt))
        assert TEXT in prompt
        assert len(prompt.encode()) <= generation.PROMPT_BYTES
        assert len(json.dumps(encoded_directory(prompt), ensure_ascii=False,
                              separators=(',', ':')).encode()) <= 96 * 1024
        seen.extend(batch)
        calls.append(prompt)
        selected.append(batch[-1])
        return {'series_ids': [batch[-1]['id']]}

    monkeypatch.setattr(generation, 'completion', choose)
    candidates, partial = asyncio.run(generation.choose_catalog(None, 'model', catalog, TEXT))
    assert partial is True and 1 < len(calls) <= 4
    assert seen == catalog and candidates == selected and catalog == before
    assert all(encoded_directory(prompt)['format'] == 'research-catalog-columns.v1' for prompt in calls)
    assert all('字典索引不是指标ID' in prompt for prompt in calls)


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


@pytest.mark.parametrize('kind', ['invented', 'another-batch', 'too-many'])
def test_large_selection_still_rejects_unknown_cross_batch_and_excess_ids(monkeypatch, kind):
    catalog, calls = directory(), []

    async def invalid(scope, model, prompt):
        fields = restore(encoded_directory(prompt))
        calls.append(prompt)
        if kind == 'invented':
            return {'series_ids': ['not-a-real-field']}
        if kind == 'another-batch':
            assert catalog[-1]['id'] not in {item['id'] for item in fields}
            return {'series_ids': [catalog[-1]['id']]}
        return {'series_ids': [item['id'] for item in fields[:33]]}

    monkeypatch.setattr(generation, 'completion', invalid)
    with pytest.raises(HTTPException) as caught:
        asyncio.run(generation.choose_catalog(None, 'model', catalog, TEXT))
    assert caught.value.status_code == 422 and len(calls) == 1
