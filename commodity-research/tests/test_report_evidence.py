"""Weekly evidence uses bounded native reads, independent review and scoped receipts."""
import asyncio
from copy import deepcopy
from dataclasses import replace
import hashlib
import json
import re
from types import SimpleNamespace
from uuid import uuid4

from fastapi import HTTPException
import httpx
from lxml.html import fromstring
import pytest

from tin.integration.workbench_scope import WorkbenchScope, current_workbench
from tin.web import analysis_jobs, brief_export, judgment_jobs, report_dimension_drafts, report_evidence, report_ppt, strategy_text
from tin.web.workbench_artifacts import ArtifactModule, serialize_revision
from tests.analysis_job_fixtures import detached_loop, finished_job
from tests.chart_fixtures import OTHER
from tests.test_chart_api import SECOND_WB, chart_client  # noqa: F401
from tests.test_chart_references import reference_client  # noqa: F401
from tests.test_report_chart_flow import report_flow  # noqa: F401
from tests.test_workbench_sources_v2 import IDENTITY, WB, source

DAY = '2023-01-09'
BINDING = source(1)
REPORT = f'/api/sn/reports/{DAY}/weekly'
DRAFT = '/api/sn/report-drafts'
CLAIM = '字段乙40件，资料对后续方向存在分歧。'
POSITIVE = '供应偏紧，资料方预计交付趋缓。'
NEGATIVE = '供应充足，资料方预计交付提速。'


@pytest.fixture
def native(monkeypatch):
    texts = {'positive': POSITIVE, 'duplicate': POSITIVE, 'negative': NEGATIVE,
             'foreign': '其他工作台资料', 'future': '未来的资料', 'background': '往期材料'}
    state = {'paths': [], 'ids': list(texts), 'texts': texts, 'revision': 0, 'revoked': False}

    async def request(scope, path, *, search=None):
        state['paths'].append(path)
        assert scope.authorization.startswith('Bearer ') and scope.knowledge_base_id == BINDING['knowledge_base_id']
        assert scope.knowledge_base_tenant_id == BINDING['tenant_id']
        if search is not None:
            assert path == '/api/v1/knowledge-bases/' + BINDING['knowledge_base_id'] + '/hybrid-search'
            assert search == {'query_text': '检索本期报告的补证', 'match_count': 9, 'skip_context_enrichment': True}
            return {'data': [{'id': key, 'knowledge_id': key, 'chunk_type': 'text',
                              'content': '仅检索命中的摘要，不得供模型引用'} for key in state['ids']]}
        route, identifier = path.rsplit('/', 1)
        if state['revoked'] and identifier == 'positive':
            raise HTTPException(403, 'stub revoked')
        row = {'id': identifier, 'tenant_id': BINDING['tenant_id'],
               'knowledge_base_id': BINDING['knowledge_base_id'], 'updated_at': '2023-01-08T11:00:00+08:00'}
        if route == '/api/v1/knowledge':
            metadata = {'observation_date': '2023-01-05', 'published_at': '2023-01-06'}
            if identifier == 'foreign':
                row['knowledge_base_id'] = source(2)['knowledge_base_id']
            if identifier == 'future':
                metadata['published_at'] = '2023-01-10'
            if identifier == 'background':
                metadata = {}
            if identifier == 'negative':
                metadata.pop('published_at')
            return {'data': {**row, 'title': '同名市场观察', 'parse_status': 'completed', 'enable_status': 'enabled',
                'source': 'https://example.test/original', 'created_at': '2023-01-08T11:00:00+08:00',
                'custom_metadata': metadata}}
        assert route == '/api/v1/chunks/by-id'
        text = texts.get(identifier, '段落 ' + identifier)
        locators = [{'type': 'slide', 'slide': 4}] if identifier == 'positive' else []
        return {'data': {**row, 'knowledge_id': identifier, 'content': text, 'chunk_type': 'text', 'chunk_index': 0,
            'start_at': 12, 'end_at': 12 + len(text), 'content_revision': state['revision'],
            'is_enabled': True, 'status': 2, 'source_locators': locators}}

    async def refresh(*_):
        return current_workbench.get() or standalone_scope()

    monkeypatch.setattr(report_evidence, 'go_request', request)
    monkeypatch.setattr(report_evidence, '_signing_key', lambda: b'test-fake-receipt-key' * 2)
    monkeypatch.setattr(report_evidence, 'resolve_workbench', refresh)
    return state


def fake_model(monkeypatch, *, on_review=None, claim_overrides=None, binding=None, omit_plan=False):
    contexts, selected = {}, {}

    async def complete(_scope, model_id, prompt, *, purpose, audit_content=True):
        assert model_id == 'fake'
        if purpose == 'report_dimension_draft':
            return {'text': '已有事实仍待进一步观察。', 'impact': 'neutral', 'metric_names': []}
        if purpose == 'report_dimension_review':
            return {'grounded': True, 'metric_names': [], 'dates': []}
        if purpose in {'report_summary_draft', 'report_summary_review'}:
            assert CLAIM not in prompt
            if purpose == 'report_summary_draft':
                return {'text': '总体方向仍需观察。', 'metric_names': []}
            return {'grounded': True, 'metric_names': [], 'dates': []}
        assert audit_content is False
        context = json.loads(prompt.split('\n', 1)[1])
        contexts[purpose] = context
        if purpose == 'report_evidence_plan':
            assert context['start'] == DAY and context['end'] == DAY
            return {'searches': [] if omit_plan else [
                {'binding_id': binding or BINDING['id'], 'query': '检索本期报告的补证'}]}
        if purpose == 'report_evidence_draft':
            selected.update(dimension_id=context['dimensions'][0]['id'], text=CLAIM, impact='neutral',
                            evidence_ids=['F1', 'P1', 'P2'], metric_names=['字段乙'])
            selected.update(claim_overrides or {})
            return {'claims': [selected]}
        assert purpose == 'report_evidence_review'
        if on_review:
            on_review()
        return {'claims': [{'dimension_id': selected['dimension_id'], 'grounded': True,
                            'evidence_ids': selected['evidence_ids']}]}

    monkeypatch.setattr(strategy_text, 'completion', complete)
    return contexts


def test_empty_citations_preserve_existing_background_result_fingerprint():
    report = {'manual_report': {'draft': {'title': '已有周报'}}}
    fingerprint = report_dimension_drafts.report_fingerprint(report)
    report['manual_report']['draft']['evidence_citations'] = []
    assert report_dimension_drafts.report_fingerprint(report) == fingerprint
    report['manual_report']['draft']['evidence_citations'] = [{'dimension_id': 'fixture'}]
    assert report_dimension_drafts.report_fingerprint(report) != fingerprint


def test_workflow_renders_clickable_citations_and_markdown_then_hides_revoked_sources(report_flow, native, monkeypatch):
    from tin.web import dimension_board

    client, state = report_flow
    for router in (dimension_board.router, report_dimension_drafts.router, report_evidence.router,
                   report_ppt.router, analysis_jobs.router):
        state.app.include_router(router)

    async def refresh(*_):
        return current_workbench.get()

    monkeypatch.setattr(judgment_jobs, 'resolve_workbench', refresh)

    def draft_in_background(body):
        with detached_loop(client):
            accepted = client.post(REPORT + '/draft-dimensions', json=body,
                                   headers={'Idempotency-Key': uuid4().hex})
            assert accepted.status_code == 202, accepted.text
            path = '/api/sn/analysis-jobs/' + accepted.json()['id']
            job, response = finished_job(client, path)
            assert job['state'] == 'succeeded', job
            assert response.status_code == 200, response.text
            return path + '/result', response.json()

    state.observations[OTHER] = [('2023-01-02', 20), (DAY, 40)]
    configured = client.put('/api/sn/dimensions', json={'revision': 0, 'dimensions': [
        {'label': '维度样例', 'category': '需求', 'sections': [
            {'label': '观测', 'items': [{'kind': 'indicator', 'id': OTHER}]}]}]})
    assert configured.status_code == 200, configured.text
    identifier = configured.json()['dimensions'][0]['id']
    calls = fake_model(monkeypatch)
    body = {'model_id': 'fake', 'dimension_notes': {
        identifier: {'text': '请核查意见是否一致', 'impact': 'undecided'}}}
    _, regular = draft_in_background(body)
    assert 'evidence' not in regular and not native['paths'] and not calls
    assert regular['summary']['text'] == '总体方向仍需观察。'
    assert client.post(REPORT.replace('/weekly', '/daily') + '/draft-dimensions',
                       json={**body, 'include_kb_evidence': True}).status_code == 422
    result_url, result = draft_in_background({**body, 'include_kb_evidence': True})
    assert result['summary']['text'] == '总体方向仍需观察。'
    plan = calls['report_evidence_plan']['dimensions'][0]
    assert plan['researcher_text'] == '请核查意见是否一致' and plan['facts']['F1']['latest'] == 40
    passages = calls['report_evidence_draft']['passages']
    assert {row['text'] for row in passages.values()} == {POSITIVE, NEGATIVE, '往期材料'}
    assert passages['P3']['background'] and passages['P3']['published_at'] is None
    assert passages['P3']['observation_date'] is None and passages['P3']['ingested_at'].startswith('2023-01-08')
    assert '不可读' in ''.join(result['evidence_gaps'])
    proof = result['evidence'][identifier]
    assert result['dimensions'][0]['text'] == CLAIM
    assert proof['sources'][0]['content_sha256'] == hashlib.sha256(POSITIVE.encode()).hexdigest()
    assert proof['sources'][0]['location'] == '第 4 张幻灯片'
    assert proof['sources'][0]['excerpt'] == POSITIVE
    assert proof['sources'][1]['location'] == '' and proof['sources'][1]['excerpt'] == NEGATIVE
    assert client.get(result_url).json()['evidence'] == result['evidence']
    assert client.get(result_url, headers={'x-actor': 'second'}).status_code == 404
    native['revoked'] = True
    revoked = client.get(result_url)
    assert revoked.status_code == 200 and revoked.json()['evidence'] == {}
    assert revoked.json()['dimensions'][0]['status'] == 'sources_unverified'
    assert all(text not in revoked.text for text in (CLAIM, POSITIVE, NEGATIVE))
    assert revoked.json()['summary']['text'] == '总体方向仍需观察。'
    native['revoked'] = False
    # Reconstruct the pre-redesign fingerprint from the original wire fields.
    revision = '2023-01-08T11:00:00+08:00'
    legacy = [BINDING['id'], BINDING['knowledge_base_id'], BINDING['tenant_id'], 'positive', 'positive', {
        'title': '同名市场观察', 'location': f'分块 1 · 字符 12–{12 + len(POSITIVE)}',
        'url': 'https://example.test/original', 'published_at': '2023-01-06', 'observation_date': '2023-01-05',
        'ingested_at': revision, 'background': True, 'content_sha256': hashlib.sha256(POSITIVE.encode()).hexdigest(),
    }, revision, 0, revision]
    expected = hashlib.sha256(json.dumps(legacy, ensure_ascii=False, sort_keys=True, separators=(',', ':')).encode())
    assert proof['receipt']['passages'][0]['fingerprint'] == expected.hexdigest()
    draft = {'period': 'weekly', 'date': DAY, 'title': '带引用周报',
             'layout': {'dimension_order': [identifier]},
             'sections': {'summary': {'text': result['summary']['text']}},
             'dimension_notes': {identifier: {'text': CLAIM, 'impact': 'neutral'}},
             'evidence_citations': [proof['receipt']]}
    assert client.post(DRAFT, headers={'x-workbench': SECOND_WB}, json={'draft': draft}).status_code == 422
    forged = deepcopy(draft)
    forged['evidence_citations'][0]['passages'][0]['chunk_id'] = 'foreign'
    assert client.post(DRAFT, json={'draft': forged}).status_code == 422
    created = client.post(DRAFT, json={'draft': draft})
    assert created.status_code == 201, created.text
    saved = created.json()
    check = saved['evidence_checks'][identifier]
    assert check['facts'][0]['baseline']['value'] == 20 and 'text' not in check['sources'][0]
    page = client.get(f'/sn/reports?date={DAY}&period=weekly')
    assert page.status_code == 200, page.text
    page_html = fromstring(page.text)
    assert '总体方向仍需观察。' in page_html.get_element_by_id('report-period-conclusion').text_content()
    assert 'checked' not in page_html.get_element_by_id('report-include-kb-evidence').attrib
    editor = json.loads(page_html.get_element_by_id('report-editor-data').text)
    assert editor['draft']['layout']['dimension_order'] == [identifier]
    assert editor['draft']['evidence_citations'] == [proof['receipt']]
    assert page_html.xpath(f'//*[@data-report-dimension="{identifier}"]//sup/a/text()') == ['[1]', '[2]', '[3]']
    citation_block = page_html.get_element_by_id('report-citations')
    positive, negative = (citation_block.get_element_by_id(f'report-evidence-{i}') for i in (2, 3))
    assert positive.xpath('.//div[@class="report-citation-links"]/span/text()') == ['发布于 2023-01-06']
    assert negative.xpath('.//div[@class="report-citation-links"]/span/text()') == ['数据日期 2023-01-05']
    assert not negative.xpath('.//div[@class="report-citation-heading"]/span')
    assert positive.xpath('.//details/div/text()')[0] == '入库时间 2023-01-08 11:00:00'
    assert positive.xpath('.//small/@title') == ['发布日期早于本周或未知']
    assert positive.xpath('.//a[.="原网页"]/@href') == ['https://example.test/original']
    citations_text = citation_block.text_content()
    assert '字段乙：2023-01-02 为 20 件 → 2023-01-09 为 40 件' in citations_text
    assert f'“{POSITIVE}”' in citations_text and f'“{NEGATIVE}”' in citations_text
    read_url = positive.xpath('.//a[.="查看原文"]/@href')[0]
    prefix = f'/research/workbenches/{WB}'
    assert read_url.startswith(prefix + '/api/sn/reports/')
    # This fixture installs routes directly. The application middleware strips the scoped prefix.
    read_url = read_url.removeprefix(prefix)
    assert client.get(read_url, headers={'x-workbench': SECOND_WB}).status_code == 404
    assert client.get(read_url.split('?')[0] + '?version=' + '0' * 64).status_code == 409
    clicked = client.get(read_url)
    assert clicked.status_code == 200 and clicked.text == POSITIVE
    assert clicked.headers['content-type'].startswith('text/plain')
    download = f'/sn/reports/{DAY}/weekly.md'
    markdown = client.get(download)
    assert markdown.status_code == 200
    assert CLAIM + '[^e1][^e2][^e3]' in markdown.text
    footnotes = markdown.text.split('## 逐条引用\n', 1)[1]
    assert '[^e1]: 字段乙：2023-01-02 为 20 件 → 2023-01-09 为 40 件 · [查看数据]' in footnotes
    assert f'[^e2]: 同名市场观察 · 第 4 张幻灯片 · “{POSITIVE}” · 发布于 2023-01-06 · [查看原文]' in footnotes
    assert f'[^e3]: 同名市场观察 · “{NEGATIVE}” · 数据日期 2023-01-05 · [查看原文]' in footnotes
    assert '[查看原文](<http://testserver' + prefix + read_url + '>' in footnotes
    assert '[原网页](<https://example.test/original>)' in footnotes
    assert '入库时间' not in footnotes
    for readable in (citations_text, footnotes):
        assert '分块' not in readable and '字符' not in readable
        assert not re.search(r'\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:Z|[+-]\d{2}:\d{2})', readable)
        assert '20.0 件' not in readable and '40.0 件' not in readable
    assert client.get(download).text == markdown.text
    assert client.post(REPORT + '/ppt').status_code == 409
    raw_url = f'/sn/reports/{DAY}/weekly.json'
    with pytest.raises(HTTPException, match='目标知识库读者范围'):
        brief_export._markdown(None, 'weekly', DAY, '', client.get(raw_url).json())

    observations = state.observations[OTHER]
    state.observations[OTHER] = [*observations[:-1], (DAY, 41)]
    assert '当前计算事实或维度已变化' in client.get(download).text
    assert '当前计算事实或维度已变化' in client.get(DRAFT, params={'date': DAY, 'period': 'weekly'}).text
    assert client.get(read_url).status_code == 404
    state.observations[OTHER] = observations
    native['revision'] = 1
    assert '段落已变化' in client.get(download).text
    assert client.get(read_url).status_code == 404
    native['revision'] = 0

    url = DRAFT + '/' + saved['id']
    unlinked = {**draft, 'evidence_citations': []}
    assert client.patch(url, json={'draft': unlinked, 'revision': 1}).status_code == 422
    edited = {**draft, 'dimension_notes': {identifier: {'text': '研究员修改原句', 'impact': 'neutral'}}}
    updated = client.patch(url, json={'draft': edited, 'revision': 1})
    assert updated.status_code == 200, updated.text
    assert '原句已编辑' in updated.json()['evidence_checks'][identifier]['notice']
    after_edit = client.get(download).text
    assert '研究员修改原句' in after_edit and '需重新核对' in after_edit and '[^e2]' not in after_edit
    native['revoked'] = True
    redacted = client.get(raw_url)
    assert redacted.status_code == 200
    assert all(text not in redacted.text for text in (POSITIVE, NEGATIVE, '同名市场观察', '研究员修改原句'))
    redacted_page = client.get(f'/sn/reports?date={DAY}&period=weekly')
    assert redacted_page.status_code == 200 and '部分逐条引用需重新核对' in redacted_page.text
    assert '同名市场观察' not in redacted_page.text
    assert '需重新核对' in client.get(download).text
    assert client.get(read_url).status_code == 404
    revision = SimpleNamespace(body=json.dumps(edited), artifact_id=saved['id'], revision=2, title='fixture',
        status='draft', source_ids=[], source_snapshots=[], edited_by='first', recorded_at='fixture')
    generic = serialize_revision(revision, module=ArtifactModule.briefs)
    assert '研究员修改原句' not in generic['body']
    cleared = {**draft, 'dimension_notes': {}, 'evidence_citations': []}
    response = client.patch(url, json={'draft': cleared, 'revision': 2})
    assert response.status_code == 200 and response.json()['draft']['evidence_citations'] == []


def standalone_scope(**changes):
    scope = WorkbenchScope(IDENTITY, WB, BINDING['knowledge_base_id'], BINDING['tenant_id'],
                           {'can_edit': True}, 'Bearer fixture', scope_version=2, sources=(BINDING,))
    return replace(scope, **changes)


@pytest.mark.parametrize('locators, label', [
    ([{'type': 'pdf', 'page': 7, 'bbox': [0, 0, 1, 1]}], 'PDF 第 7 页'),
    ([{'type': 'slide', 'slide': 2, 'page': 8}], '第 2 张幻灯片'),
    ([{'type': 'sheet', 'sheet': '库存', 'row_start': 7, 'row_end': 8}], '工作表「库存」第 7–8 行'),
    ([{'type': 'sheet', 'sheet': '库存', 'row_start': 7, 'row_end': 8,
       'column_start': 26, 'column_end': 28}], '工作表「库存」Z7:AB8'),
    ([{'type': 'section', 'section': 3, 'title': '交付计划'}], '章节「交付计划」'),
    ([{'type': 'section', 'section': 3}, {'type': 'pdf', 'page': False},
      {'type': 'text', 'start': 7, 'end': 8}, {'type': 'pdf', 'page': 5, 'source_hash': 'old'}], ''),
    ([{'type': 'pdf', 'page': 2, 'source_hash': 'file'}, {'type': 'pdf', 'page': 2},
      {'type': 'pdf', 'page': 4}], 'PDF 第 2 页、PDF 第 4 页'),
])
def test_human_location_uses_only_native_text_locators(locators, label):
    chunk = {'source_locators': locators, 'metadata': {'page': 99}, 'image_info': '[{"page": 99}]'}
    assert report_evidence._source_location(chunk, {'file_hash': 'file'}) == label


def test_excerpt_prefers_sentence_boundaries_and_keeps_real_decimal_points():
    sentence = '初值为 12.375 件。'
    assert report_evidence._excerpt(sentence + '延长内容' * 40) == sentence
    fallback = report_evidence._excerpt('很长的连续叙述' * 30)
    assert len(fallback) <= 80 and fallback.endswith('…')
    from tin.web.report_view import _citation_value
    assert [_citation_value(v) for v in (20.0, 12.375, 0.000005)] == ['20', '12.375', '0.000005']


def generate(scope=None):
    row = {'id': 'fixture-dimension', 'label': '样例维度', 'facts': [
        {'name': '字段乙', 'latest': 40, 'unit': '件', 'date': DAY}]}
    return asyncio.run(report_evidence.draft(scope or standalone_scope(), 'fake', DAY, [row],
                                            {row['id']: ''}, {'workbench': '样例'}))


@pytest.mark.parametrize('case', ['binding', 'delegated', 'no_plan', 'no_hits', 'bad_id', 'bad_number', 'review', 'budget'])
def test_evidence_gaps_and_bounded_reads(native, monkeypatch, case):
    def fail_review():
        raise HTTPException(503, 'stub')

    scope = standalone_scope(sources=(source(1, delegated=True),)) if case == 'delegated' else None
    if case == 'no_hits':
        native['ids'] = []
    if case == 'budget':
        native['ids'] = ['positive', 'negative'] + [f'extra-{i}' for i in range(12)]
    overrides = {'evidence_ids': ['F1', 'P8']} if case == 'bad_id' else (
        {'text': '字段乙999件。'} if case == 'bad_number' else None)
    observed = fake_model(monkeypatch, on_review=fail_review if case == 'review' else None,
                          claim_overrides=overrides, binding='unbound' if case == 'binding' else None,
                          omit_plan=case == 'no_plan')
    results, citations, gaps = generate(scope)
    if case == 'budget':
        assert sum('/by-id/' in path for path in native['paths']) == 8
        assert len(observed['report_evidence_review']['passages']) == 8
        assert len(results) == len(citations) == 1 and '达到 8 段复核预算' in ''.join(gaps)
    else:
        assert not results and not citations
        expected = {'binding': '未授权绑定', 'delegated': '没有可为报告读取正文',
                    'no_plan': '未提出', 'no_hits': '未取得', 'bad_id': '证据 ID',
                    'bad_number': '数值、指标或日期', 'review': '独立模型复核失败'}
        assert expected[case] in ''.join(gaps)
    if case in {'binding', 'delegated', 'no_plan'}:
        assert native['paths'] == []


def test_changed_source_between_review_and_preview_is_not_returned(native, monkeypatch):
    fake_model(monkeypatch, on_review=lambda: native['texts'].update(positive='重解析后的新正文'))
    _, receipts, _ = generate()
    assert receipts
    assert asyncio.run(report_evidence.preview(standalone_scope(), DAY, receipts)) == {}


def test_attributed_document_opinion_can_be_cited_without_computed_numbers(native, monkeypatch):
    sentence = '资料方预计交付趋缓。'
    fake_model(monkeypatch, claim_overrides={'text': sentence, 'evidence_ids': ['P1'], 'metric_names': []})
    row = {'id': 'fixture-dimension', 'label': '样例维度', 'facts': []}
    results, citations, gaps = asyncio.run(report_evidence.draft(
        standalone_scope(), 'fake', DAY, [row], {row['id']: ''}, {}))
    assert results[row['id']]['text'] == sentence and citations[row['id']].fact_indexes == []
    assert not any('没有可提交复核' in gap for gap in gaps)


def test_native_hybrid_search_uses_bearer_and_bound_post_route(monkeypatch):
    from tin.integration import workbench_scope

    bound, _ = report_evidence._bindings(standalone_scope())[BINDING['id']]
    path = f'/api/v1/knowledge-bases/{bound.knowledge_base_id}/hybrid-search'
    calls = []

    def transport(request):
        calls.append(request)
        assert request.method == 'POST' and request.url.path == path
        assert request.headers['authorization'] == 'Bearer fixture'
        assert request.headers['x-tenant-id'] == str(IDENTITY.tenant_id)
        assert json.loads(request.content) == {'query_text': '样例'}
        return httpx.Response(200, json={'success': True, 'data': []})

    real_client = httpx.AsyncClient
    monkeypatch.setattr(workbench_scope.settings, 'weknora_api_url', 'http://fixture.test')
    monkeypatch.setattr(httpx, 'AsyncClient', lambda **kwargs: real_client(transport=httpx.MockTransport(transport), **kwargs))
    assert asyncio.run(workbench_scope.go_request(bound, path, search={'query_text': '样例'}))['data'] == []
    with pytest.raises(ValueError, match='bound knowledge base'):
        asyncio.run(workbench_scope.go_request(bound, path.replace(bound.knowledge_base_id, 'unbound'),
                                               search={'query_text': '样例'}))
    assert len(calls) == 1
