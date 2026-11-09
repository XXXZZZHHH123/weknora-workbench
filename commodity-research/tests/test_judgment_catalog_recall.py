"""Large catalogs: per-item phrasings, ranked recall from the real catalog, bounded model picks."""
import asyncio
import json

from fastapi import HTTPException
import pytest

from tests.test_judgment_catalog_capacity import directory
from tin.integration.catalog_recall import CatalogRecall
from tin.web import judgment_generation as generation
from tin.web.judgment_failures import failure_for

TEXT = '下游纺纱利润持续亏损，纺企对高价原料的接受度下降。另一条：港口库存回升。'
TARGET = {'id': 'kb-' + 'f' * 48, 'name': '棉纱即期加工利润', 'unit': '元/吨', 'frequency': '日',
          'sheet': '纱布利润', 'dimensions': {}, 'source_file': 'synthetic.xlsx', 'source_kind': 'primary',
          'knowledge_base_id': 'library-0-' + 'a' * 30, 'source_version': 'a' * 64}


def inventory():
    return {'blocks': [], 'items': [
        {'id': 'i1', 'block_ids': ['s0001'], 'quote': '下游纺纱利润持续亏损', 'kind': 'bear_evidence', 'source_unit': None},
        {'id': 'i2', 'block_ids': ['s0001'], 'quote': '港口库存回升', 'kind': 'marginal_focus', 'source_unit': None}]}


PORT = {**TARGET, 'id': 'kb-' + 'e' * 48, 'name': '港口棉花库存', 'unit': '万吨', 'sheet': '进口棉港口库存'}


def catalog():
    return [*directory(), TARGET, PORT]


def candidates_of(prompt):
    return json.loads(prompt.split('候选指标：', 1)[1].split('\n完整原文：', 1)[0])


def items_of(prompt):
    return json.loads(prompt.split('研究条目：', 1)[1].split('\n候选指标：', 1)[0])


def choose(monkeypatch, answer, rows=None, value=None):
    prompts = []

    async def completion(scope, model, prompt):
        prompts.append(prompt)
        return answer(prompt)

    monkeypatch.setattr(generation, 'completion', completion)

    async def run():
        marker = generation._source_inventory.set(value or inventory())
        try:
            return await generation.choose_catalog(None, 'model', rows or catalog(), TEXT)
        finally:
            generation._source_inventory.reset(marker)
    return asyncio.run(run()), prompts


def picking(names, queries=None):
    def answer(prompt):
        if prompt.startswith('research-catalog-recall.v1'):
            return {'queries': queries if queries is not None else {'i1': ['纱线加工利润', '纺纱利润']}}
        shown = candidates_of(prompt)
        return {'picks': {'i1': [ref for ref, field in shown.items() if field['name'] in names]}}
    return answer


def test_large_catalog_is_recalled_per_item_and_only_picked_real_rows_reach_analysis(monkeypatch):
    (selected, partial), prompts = choose(monkeypatch, picking({TARGET['name']}))
    assert partial is True and selected == [TARGET]
    assert len(prompts) == 2 and prompts[0].startswith('research-catalog-recall.v1')
    pick = prompts[1]
    # The model sees a bounded per-item shortlist with temporary references, never real IDs.
    assert 'kb-' not in pick and len(candidates_of(pick)) <= 2 * 20
    assert all(len(item['candidates']) <= 20 for item in items_of(pick))


def test_reference_that_was_not_shown_is_rejected_without_guessing(monkeypatch):
    def answer(prompt):
        if prompt.startswith('research-catalog-recall.v1'):
            return {'queries': {'i1': ['纺纱利润']}}
        return {'picks': {'i1': ['r9999']}}
    with pytest.raises(HTTPException) as caught:
        choose(monkeypatch, answer)
    assert caught.value.status_code == 422
    assert failure_for(caught.value, 'selecting')['diagnostic']['code'] == 'selection_invalid'


@pytest.mark.parametrize('queries', [None, [], 'text', {'unknown': ['纺纱利润']}, {'i1': [5, '']}])
def test_unusable_phrasings_still_recall_every_item_by_its_own_quote(monkeypatch, queries):
    def answer(prompt):
        if prompt.startswith('research-catalog-recall.v1'):
            return queries if not isinstance(queries, dict) else {'queries': queries}
        shown = candidates_of(prompt)
        assert {item['id'] for item in items_of(prompt)} == {'i1', 'i2'}
        return {'picks': {'i1': [ref for ref, field in shown.items() if field['name'] == TARGET['name']]}}
    (selected, _), _ = choose(monkeypatch, answer)
    assert selected == [TARGET]


def test_model_may_pick_nothing_and_analysis_gets_an_empty_subset(monkeypatch):
    (selected, partial), _ = choose(monkeypatch, picking(set()))
    assert selected == [] and partial is True


def test_phrasing_stage_output_limit_names_the_selection_stage(monkeypatch):
    def answer(prompt):
        raise HTTPException(422, generation.OUTPUT_LIMIT)
    with pytest.raises(HTTPException) as caught:
        choose(monkeypatch, answer)
    assert caught.value.detail == generation.SELECTION_OUTPUT_LIMIT


def test_shortlists_shrink_to_fit_the_request_and_never_truncate_silently(monkeypatch):
    many = {'blocks': [], 'items': [{'id': f'i{index}', 'block_ids': ['s0001'], 'quote': f'测量字段{index} 分组{index % 31}',
                                     'kind': 'other', 'source_unit': None} for index in range(1, 41)]}
    phrased = {item['id']: [item['quote']] for item in many['items']}
    full = []

    def answer(prompt):
        if prompt.startswith('research-catalog-recall.v1'):
            return {'queries': phrased}
        full.append(prompt)
        return {'picks': {}}
    monkeypatch.setattr(generation, 'PROMPT_BYTES', 120 * 1024)
    choose(monkeypatch, answer, value=many)
    assert len(full[0].encode('utf-8')) <= 120 * 1024
    assert max(len(item['candidates']) for item in items_of(full[0])) < 20
    monkeypatch.setattr(generation, 'PROMPT_BYTES', 24 * 1024)
    with pytest.raises(HTTPException) as caught:
        choose(monkeypatch, answer, value=many)
    assert caught.value.status_code == 413


def test_recall_ranks_partial_wording_overlap_and_keeps_each_phrasing_represented():
    rows = [('a', '棉纱即期加工利润', '纱布利润 元/吨'), ('b', '纺纱厂开机率', '下游情况 %'),
            ('c', '港口库存', '库存 万吨'), ('d', '美棉累计净销售未装运', '美棉出口 吨')]
    index = CatalogRecall(rows)
    assert index.ranked('纱线 利润', 2)[0] == 'a'
    assert index.ranked('2023年9月22日 港口库存', 1) == ['c']
    # Each phrasing contributes its best field before any phrasing contributes a second one.
    assert set(index.recall(['港口库存', '未装运'], 2)) == {'c', 'd'}
    assert index.recall(['', '   '], 3) == [] and index.recall(['毫不相关'], 3) == []
