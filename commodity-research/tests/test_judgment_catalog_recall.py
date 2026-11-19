"""Large catalogs: per-item phrasings, ranked recall from the real catalog, bounded model picks."""
import asyncio
from datetime import datetime, timedelta, timezone
import json

from fastapi import HTTPException
import pytest

from tests.test_judgment_catalog_capacity import directory
from tests.analysis_job_fixtures import detached_loop, finished_job
from tests.chart_fixtures import definition
from tests.test_judgment_generation import endpoint, generate, generator, rows
from tests.test_judgment_review import all_items
from tests.test_knowledge_facts import FIRST, SERIES, canonical, page
from tests.test_workbench_setup_jobs import active, workbench
from tin.integration.catalog_recall import CatalogRecall
from tin.integration.workbench_scope import artifact_session
from tin.models import SourceMapping, WorkbenchArtifact
from tin.schemas.chart import ChartDefinition
from tin.web import charts, judgment_generation as generation, judgment_recall as recall
from tin.web.judgment_failures import failure_for
from tin.web.research_dimensions import DIMENSIONS_KEY
from tin.web.restricted_views import CORE_KEY, mapping_key

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


def choose(monkeypatch, answer, rows=None, value=None, shared=()):
    prompts = []

    async def completion(scope, model, prompt):
        prompts.append(prompt)
        return answer(prompt)

    monkeypatch.setattr(generation, 'completion', completion)

    async def run():
        marker = generation._source_inventory.set(value or inventory())
        try:
            return await generation.choose_catalog(None, 'model', rows or catalog(), TEXT, shared=shared)
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


def test_query_prompt_shares_catalog_terms_and_shrinks_every_tables_sample_count():
    long_name = '原表字段' * 20
    fields = [{'name': name, 'sheet': sheet} for sheet, name in [
        ('数据乙', long_name), ('数据乙', long_name), ('数据甲', '英文代码 ABC'),
        ('数据乙', '第二字段'), ('数据甲', '第二字段'), ('数据甲', '第三字段')]]
    expected = [['数据乙', [long_name[:40]]], ['数据甲', ['英文代码 ABC']]]
    budget = len(recall._encode(expected).encode())
    query = recall.query_prompt(TEXT, inventory(), fields, prompt_bytes=generation.PROMPT_BYTES)
    before, overview = query.split('目录用语概览（表名与字段名样例）：', 1)
    full, end = overview.split('\n研究条目：', 1)
    assert json.loads(full) == [['数据乙', [long_name[:40], '第二字段']],
                                ['数据甲', ['英文代码 ABC', '第二字段', '第三字段']]]
    tiny = len((before + '目录用语概览（表名与字段名样例）：\n研究条目：' + end).encode()) + budget
    query = recall.query_prompt(TEXT, inventory(), fields, prompt_bytes=tiny)
    assert len(query.encode()) <= tiny
    assert json.loads(query.split('目录用语概览（表名与字段名样例）：', 1)[1].split('\n研究条目：')[0]) == expected
    assert recall.item_queries({'queries': {'i1': [str(i) for i in range(8)]}}, inventory()) == {
        'i1': ['0', '1', '2', '3', '4']}


def test_shared_workbench_candidate_can_be_picked_without_matching_recall(monkeypatch):
    def answer(prompt):
        if prompt.startswith('research-catalog-recall.v1'):
            assert '目录用语概览' in prompt and TARGET['sheet'] in prompt
            return {'queries': {'i1': ['Only unmatched terms']}}
        shared = json.loads(prompt.split('本研究台已使用的指标（共享候选）：', 1)[1].split('\n研究条目：')[0])
        assert len(shared) == 1
        assert candidates_of(prompt)[shared[0]]['name'] == PORT['name']
        assert shared[0] not in items_of(prompt)[0]['candidates']
        return {'picks': {'i1': shared}}
    (selected, partial), _ = choose(monkeypatch, answer, shared=[PORT['id']])
    assert selected == [PORT] and partial is True


def test_workbench_usage_collects_recent_authorized_dimensions_charts_core_and_enabled_monitors(workbench, monkeypatch):
    scope = workbench.scope
    chart, dimension, core, monitor, disabled, absent, other_actor = ['kb-' + c * 48 for c in 'abcdef0']
    now = datetime.now(timezone.utc)
    with active(scope), artifact_session(scope) as session:
        curve = {'series_id': chart, 'label': '测试曲线', 'kind': 'line', 'axis': 'left'}
        chart_row = charts.create_row(session, scope, ChartDefinition.model_validate(definition(curves=[curve])))
        chart_row.updated_at = now
        session.add(SourceMapping(id=mapping_key(session, DIMENSIONS_KEY), config={'dimensions': [
            {'id': 'desk-1', 'label': '测试维度', 'category': '其他', 'sections': [
                {'items': [{'kind': 'indicator', 'id': dimension}, {'kind': 'indicator', 'id': absent}]}]}]},
            confirmed_by=scope.identity.user_id, confirmed_at=now + timedelta(seconds=1)))
        session.add(SourceMapping(id=mapping_key(session, CORE_KEY), config={'items': [{'kind': 'indicator', 'id': core}]},
            confirmed_by=scope.identity.user_id, confirmed_at=now + timedelta(seconds=2)))
        for identifier, enabled, actor in [(monitor, True, scope.identity.user_id),
                (disabled, False, scope.identity.user_id), (other_actor, True, 'other')]:
            session.add(WorkbenchArtifact(id='monitor-' + identifier, module='monitors', actor=actor, title='测试监测',
                body=json.dumps({'workbench_id': scope.workbench_id, 'enabled': enabled,
                                 'rules': [{'series_id': identifier}]}), status='draft', source_ids=[],
                revision=1, created_at=now, updated_at=now + timedelta(seconds=3)))
        session.commit()
    fields = [{'id': sid} for sid in (chart, dimension, core, monitor, disabled, other_actor)]
    with active(scope):
        assert recall.shared_candidate_ids(scope, fields) == [monitor, core, dimension, chart]
        monkeypatch.setattr(recall, 'SHARED_CANDIDATE_LIMIT', 2)
        assert recall.shared_candidate_ids(scope, fields) == [monitor, core]


@pytest.mark.parametrize('bad_ref', [None, SERIES, 'r9999'])
def test_recommendation_job_accepts_only_shown_refs_and_retains_reasons(generator, monkeypatch, bad_ref):
    client, headers, _, _, _ = generator
    response = generate(client, headers)
    assert response.status_code == 200, response.text
    preview = response.json()
    selected_items = [i for i in all_items(preview) if i['kind'] == 'thresholds' or i['title'] == '多方依据']
    assert len(selected_items) == 2

    async def complete(scope, model, prompt):
        assert 'Stocks above 12 吨' in prompt
        if prompt.startswith('research-catalog-recall.v1'):
            assert '目录用语概览' in prompt and 'Measurements' in prompt and 'sample hint' in prompt
            item = json.loads(prompt.split('\n研究条目：', 1)[1].split('\n研究员检索提示：', 1)[0])[0]
            return {'queries': {item['id']: ['Measured quantity']}}
        shown = candidates_of(prompt)
        selected_ref = next(ref for ref, row in shown.items() if row['name'] == 'Measured quantity')
        return {'recommendations': [{'ref': bad_ref or selected_ref, 'reason': '原文数量匹配实测字段'}]}

    monkeypatch.setattr(generation, 'completion', complete)
    job_paths = []
    with detached_loop(client):
        for index, item in enumerate(selected_items):
            response = client.post(endpoint('/indicator-recommendations'), headers={**headers,
                'Idempotency-Key': 'indicator-recommendation-test-' + str(index)}, json={
                'draft_id': preview['draft_id'], 'item_id': item['id'], 'model_id': 'synthetic-chat', 'search': 'sample hint'})
            assert response.status_code == 202, response.text
            job = response.json()
            assert job['purpose'] == 'judgment_indicator_recommendations'
            path = page(FIRST, '/api/sn/analysis-jobs/' + job['id'])
            status, result = finished_job(client, path, headers)
            if bad_ref:
                assert status['state'] == 'failed' and result.status_code == 409
                assert '模型未给出有效的真实指标推荐' in status['message']
            else:
                assert status['state'] == 'succeeded', status
                assert result.status_code == 200, result.text
                data = result.json()
                assert data['item_id'] == item['id'] and data['draft_id'] == preview['draft_id']
                assert data['recommendations'][0]['series_id'] == SERIES
                assert data['recommendations'][0]['reason'] == '原文数量匹配实测字段'
                job_paths.append(path)
        for path in job_paths:
            assert client.get(path + '/result', headers=headers).status_code == 200
    assert rows() == []
