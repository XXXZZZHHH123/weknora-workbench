"""Report chart evidence and recall protocols, with no live model or source."""
import asyncio
from copy import deepcopy
import json

from fastapi import HTTPException
import pytest

from tin.web import report_chart_suggestions as reports
from tests.test_strategy_citations import case, parsed, file_ref  # noqa: F401


def batch(text='Original title\nLegend A kg\nLegend B kg', page=2):
    return [{'id': 'chunk', 'index': 7, 'page': page, 'content': text}]


def extraction():
    return {'charts': [{'title': 'Original title', 'page': 2, 'chart_type': 'comparison',
        'series': [{'name': 'Legend A', 'unit': 'kg'}, {'name': 'Legend B', 'unit': 'kg'}],
        'evidence': [{'chunk_id': 'chunk', 'quote': batch()[0]['content']}]}]}


def test_source_reader_uses_only_existing_chunks_and_rechecks_every_page(case):
    ref = parsed(case, [f'<!-- Slide number: {i + 1} -->\nTitle {i}\nLegend {i}' for i in range(25)])
    case[1]['files']['pdf']['file_type'] = 'pptx'
    ref = file_ref(case, 'pdf')
    result = asyncio.run(reports.read_report(case[0], ref))
    assert len(result['fragments']) == 25 and len(result['batches']) == 5
    assert [fragment['page'] for fragment in result['fragments']] == list(range(1, 26))
    assert len(case[1]['chunk_reads']) == 4 and not case[1]['downloads']
    assert result['provenance'][-1]['index'] == 24


@pytest.mark.parametrize('change,status', [('foreign', 403), ('wrong', 404), ('version', 409),
    ('processing', 422), ('type', 422), ('empty', 422)])
def test_source_reader_rejects_unusable_or_unauthorized_report(case, change, status):
    ref = parsed(case, ['Parsed evidence'])
    if change == 'foreign': ref['source_binding_id'] = 'unknown'
    if change == 'wrong': ref['source_binding_id'] = 'binding-default'
    if change == 'version': ref['file_version'] = 'a' * 64
    if change == 'processing': case[1]['files']['pdf']['parse_status'] = 'processing'
    if change == 'type':
        case[1]['files']['pdf']['file_type'] = 'docx'
        ref = file_ref(case, 'pdf')
    if change == 'empty': case[1]['chunks']['pdf'] = []
    with pytest.raises(HTTPException) as caught:
        asyncio.run(reports.read_report(case[0], ref))
    assert caught.value.status_code == status and not case[1]['downloads']


@pytest.mark.parametrize('change', ['revision', 'content', 'locator', 'version', 'processing'])
def test_rechecks_revision_content_locators_and_file_metadata_after_model(case, change):
    ref = parsed(case, ['Original title\nLegend A'])
    original = asyncio.run(reports.read_report(case[0], ref))
    row = case[1]['chunks']['pdf'][0]
    if change == 'revision': row['content_revision'] += 1
    if change == 'content': row['content'] += 'Changed'
    if change == 'locator': row['source_locators'] = [{'type': 'pdf', 'page': 4}]
    if change == 'version': case[1]['files']['pdf']['updated_at'] = 'later'
    if change == 'processing': case[1]['files']['pdf']['parse_status'] = 'processing'
    with pytest.raises(HTTPException) as caught:
        asyncio.run(reports.verify_report(case[0], ref, original))
    assert caught.value.status_code == 409


def test_read_time_revision_check_catches_changes_before_inference(case):
    ref = parsed(case, ['Original', 'Second'])
    def mutate(identifier, page, response):
        if len(case[1]['chunk_reads']) == 2:
            response['data'][1]['content_revision'] += 1
    case[1]['chunk_hook'] = mutate
    with pytest.raises(HTTPException) as caught:
        asyncio.run(reports.read_report(case[0], ref))
    assert caught.value.status_code == 409


def test_batched_text_is_complete_bounded_and_uses_explicit_pages_or_fragment_order():
    rows = [{'id': str(i), 'chunk_index': i, 'content': 'x' * 30001,
             'source_locators': [{'type': 'slide', 'slide': i + 1}]} for i in range(10)]
    fragments = reports.report_fragments(rows)
    batches = reports.report_batches(fragments)
    assert sum(len(item['content']) for group in batches for item in group) == 300010
    assert all(sum(len(item['content']) for item in group) <= 24000 for group in batches)
    assert all(len({item['page'] for item in group}) <= reports.MAX_BATCH_PAGES for group in batches)
    assert fragments[0]['page'] == 1
    unknown = reports.report_fragments([{'id': 'a', 'chunk_index': 8, 'content': 'No page locator'}])
    assert unknown[0]['page'] is None
    document = extraction()
    document['charts'][0]['page'] = None
    charts, _ = reports.checked_extraction(document, batch(page=None))
    assert charts[0]['page_label'] == '片段8'


@pytest.mark.parametrize('change,reason', [('type', '图型'), ('legend', '图例'), ('evidence', '依据'),
    ('quote', '依据'), ('title', '标题'), ('unit', '单位'), ('page', '页码'), ('duplicate', '重复')])
def test_invalid_model_chart_degrades_with_reason_and_preserves_other_charts(change, reason):
    document = extraction()
    bad = deepcopy(document['charts'][0])
    if change == 'type': bad['chart_type'] = 'invented'
    if change == 'legend': bad['series'][0]['name'] = 'Fabricated legend'
    if change == 'evidence': bad['evidence'] = []
    if change == 'quote': bad['evidence'][0]['quote'] = 'Fabricated evidence'
    if change == 'title': bad['title'] = 'Fabricated title'
    if change == 'unit': bad['series'][0]['unit'] = 'tonnes'
    if change == 'page': bad['page'] = 5
    if change == 'duplicate': bad['series'][1]['name'] = bad['series'][0]['name']
    document['charts'].insert(0, bad)
    charts, failed = reports.checked_extraction(document, batch())
    assert len(charts) == 1 and len(failed) == 1 and reason in failed[0]['reason']
    assert charts[0]['series'][0]['name'] == 'Legend A'


def test_extraction_retains_model_semantic_type_and_never_classifies_from_words():
    # Same source words, any valid type chosen by model remains exactly that type.
    for chart_type in reports.CHART_TYPES:
        document = extraction()
        document['charts'][0]['chart_type'] = chart_type
        charts, failed = reports.checked_extraction(document, batch())
        assert charts[0]['chart_type'] == chart_type and not failed
    prompt = reports.extraction_prompt(batch())
    assert '不输出结论' in prompt and '不能把一句结论当标题' in prompt
    assert '图片链接和正文结论本身不能证明图例' in prompt


@pytest.mark.parametrize('forged', ['unseen', 'other-series-only', 123])
def test_mapping_only_accepts_this_series_displayed_candidates(forged):
    charts, _ = reports.checked_extraction(extraction(), batch())
    inventory = reports.series_inventory(charts)['items']
    groups = [(inventory[0], ['field-a']), (inventory[1], ['other-series-only'])]
    document = {'picks': {'c1/s1': {'series_id': forged, 'reason': 'choice'},
                          'c1/s2': {'series_id': None, 'reason': 'no fit'}}}
    with pytest.raises(HTTPException) as caught:
        reports.apply_mapping(charts, document, groups, {})
    assert caught.value.status_code == 422


def test_recall_wiring_uses_full_catalog_and_model_phrasings(monkeypatch):
    data = {f'field-{i}': {'name': f'Synthetic irrelevant {i}', 'unit': 'kg'} for i in range(1800)}
    data['field-1799']['name'] = 'Model chosen concept'
    calls = []
    async def complete(scope, model_id, prompt, *, purpose):
        calls.append(purpose)
        if purpose == 'report_chart_extract': return extraction()
        if purpose == 'report_chart_queries':
            return {'queries': {'c1/s1': ['Model chosen concept'], 'c1/s2': ['Model chosen concept']}}
        items = json.loads(prompt.split('\n研究条目：', 1)[1].split('\n候选指标：')[0])
        assert len(items) == 2 and all('field-1799' in item['candidates'] for item in items)
        return {'picks': {item['id']: {'series_id': 'field-1799', 'reason': 'matching concept'} for item in items}}
    monkeypatch.setattr(reports.strategy_text, 'completion', complete)
    result = asyncio.run(reports.recognize_batch(object(), 'model', batch(), data))
    assert calls == ['report_chart_extract', 'report_chart_queries', 'report_chart_fields']
    assert all(series['selected_id'] == 'field-1799' for series in result['charts'][0]['series'])
    assert all('id' in candidate for series in result['charts'][0]['series'] for candidate in series['candidates'])


def test_empty_recall_is_left_unmapped_with_model_reason(monkeypatch):
    async def complete(scope, model_id, prompt, *, purpose):
        if purpose == 'report_chart_extract': return extraction()
        if purpose == 'report_chart_queries':
            return {'queries': {'c1/s1': ['unfindable'], 'c1/s2': ['unfindable']}}
        return {'picks': {key: {'series_id': None, 'reason': 'No evidence'} for key in ('c1/s1', 'c1/s2')}}
    monkeypatch.setattr(reports.strategy_text, 'completion', complete)
    result = asyncio.run(reports.recognize_batch(object(), 'model', batch(), {}))
    assert result['charts'][0]['series'][0]['selected_id'] is None
    assert result['charts'][0]['series'][0]['candidates'] == []


def test_every_legend_requires_model_query():
    charts, _ = reports.checked_extraction(extraction(), batch())
    with pytest.raises(HTTPException):
        reports._checked_queries({'queries': {'c1/s1': ['valid']}}, reports.series_inventory(charts))


def test_page_marker_from_prior_chunk_does_not_guess_a_page_for_unlocated_text():
    rows = [{'id': 'first', 'chunk_index': 0, 'content': '<!-- Slide number: 4 -->\nKnown page'},
            {'id': 'second', 'chunk_index': 1, 'content': 'Unlocated historical fragment'}]
    fragments = reports.report_fragments(rows)
    assert [fragment['page'] for fragment in fragments] == [4, None]


@pytest.mark.parametrize('malformation', ['unseen_id', 'missing_pick'])
def test_bad_chart_mapping_preserves_other_charts_in_same_batch(monkeypatch, malformation):
    document = extraction()
    document['charts'].append(deepcopy(document['charts'][0]))
    catalog = {'field': {'name': 'Model concept', 'unit': 'kg'}}
    async def complete(scope, model_id, prompt, *, purpose):
        if purpose == 'report_chart_extract': return document
        if purpose == 'report_chart_queries':
            return {'queries': {f'c{chart}/s{series}': ['Model concept']
                    for chart in (1, 2) for series in (1, 2)}}
        picks = {f'c{chart}/s{series}': {'series_id': 'field', 'reason': 'Matching evidence'}
                 for chart in (1, 2) for series in (1, 2)}
        if malformation == 'unseen_id': picks['c1/s1']['series_id'] = 'forged'
        else: del picks['c1/s1']
        return {'picks': picks}
    monkeypatch.setattr(reports.strategy_text, 'completion', complete)
    result = asyncio.run(reports.recognize_batch(object(), 'model', batch(), catalog))
    assert len(result['charts']) == 1 and result['charts'][0]['id'] == 'c2'
    assert len(result['failed']) == 1 and result['failed'][0]['reason']
    assert all(series['selected_id'] == 'field' for series in result['charts'][0]['series'])


def test_mapping_unknown_extra_item_rejected_instead_of_becoming_an_accepted_reference():
    charts, _ = reports.checked_extraction(extraction(), batch())
    inventory = reports.series_inventory(charts)['items']
    groups = [(item, ['field']) for item in inventory]
    with pytest.raises(HTTPException) as caught:
        reports.checked_mapping(charts, {'picks': {'foreign-chart/s1': {'series_id': 'field', 'reason': 'forged'}}},
                                groups, {'field': {'name': 'Model concept'}})
    assert caught.value.status_code == 422
