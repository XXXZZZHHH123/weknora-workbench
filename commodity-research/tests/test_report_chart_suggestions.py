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
        'legend_semantics': 'indicators',
        'series': [{'name': 'Legend A', 'unit': 'kg'}, {'name': 'Legend B', 'unit': 'kg'}],
        'evidence': [{'chunk_id': 'chunk', 'quote': batch()[0]['content']}]}]}


def test_source_reader_uses_only_existing_chunks_and_rechecks_every_page(case):
    ref = parsed(case, [f'<!-- Slide number: {i + 1} -->\nTitle {i}\nLegend {i}' for i in range(25)])
    case[1]['files']['pdf']['file_type'] = 'pptx'
    ref = file_ref(case, 'pdf')
    result = asyncio.run(reports.read_report(case[0], ref))
    assert len(result['fragments']) == 25 and len(result['batches']) == 3
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
    ('quote', '依据'), ('unit', '单位'), ('duplicate', '重复')])
def test_invalid_model_chart_degrades_with_reason_and_preserves_other_charts(change, reason):
    document = extraction()
    bad = deepcopy(document['charts'][0])
    if change == 'type': bad['chart_type'] = 'invented'
    if change == 'legend': bad['series'][0]['name'] = 'Fabricated legend'
    if change == 'evidence': bad['evidence'] = []
    if change == 'quote': bad['evidence'][0]['quote'] = 'Fabricated evidence'
    if change == 'unit': bad['series'][0]['unit'] = 'tonnes'
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


def period_extraction():
    text = '原文共同指标 kg 2019年度 2020年度 2021年度'
    return batch(text), {'charts': [{'title': '原文共同指标', 'page': 2,
        'chart_type': 'seasonal', 'legend_semantics': 'periods',
        'series': [{'name': '原文共同指标', 'unit': 'kg',
                    'legends': ['2019年度', '2020年度', '2021年度']}],
        'evidence': [{'chunk_id': 'chunk', 'quote': text}]}]}


def test_model_period_groups_recall_one_indicator_and_retain_original_legends(monkeypatch):
    fragments, document = period_extraction()
    seen = []
    async def complete(scope, model_id, prompt, *, purpose):
        seen.append(purpose)
        if purpose == 'report_chart_extract': return document
        if purpose == 'report_chart_queries':
            assert '2019年度' in prompt and 'periods' in prompt
            return {'queries': {'c1/s1': ['原文共同指标']}}
        items = json.loads(prompt.split('\n研究条目：', 1)[1].split('\n候选指标：')[0])
        assert len(items) == 1
        return {'picks': {'c1/s1': {'series_id': 'field', 'reason': '对象、统计口径与单位一致'}}}
    monkeypatch.setattr(reports.strategy_text, 'completion', complete)
    result = asyncio.run(reports.recognize_batch(object(), 'model', fragments,
        {'field': {'name': '原文共同指标', 'unit': 'kg'}}))
    chart = result['charts'][0]
    assert not result['failed'] and chart['legend_semantics'] == 'periods'
    assert len(chart['series']) == 1 and chart['series'][0]['selected_id'] == 'field'
    assert chart['series'][0]['legends'] == ['2019年度', '2020年度', '2021年度']
    assert seen == ['report_chart_extract', 'report_chart_queries', 'report_chart_fields']


def test_multi_indicator_period_groups_remain_distinct_by_model_decision():
    fragments, document = period_extraction()
    fragments[0]['content'] += ' 另一原文指标'
    document['charts'][0]['evidence'][0]['quote'] = fragments[0]['content']
    document['charts'][0]['series'].append({'name': '另一原文指标', 'unit': 'kg',
        'legends': ['2019年度', '2020年度', '2021年度']})
    charts, failed = reports.checked_extraction(document, fragments)
    assert not failed and len(charts[0]['series']) == 2
    assert [item['name'] for item in charts[0]['series']] == ['原文共同指标', '另一原文指标']


def test_numeric_indicator_legends_are_not_reclassified_into_periods():
    fragments = batch('Source 2019指数 2020指数')
    document = {'charts': [{'title': 'Source', 'page': 2, 'chart_type': 'comparison',
        'legend_semantics': 'indicators',
        'series': [{'name': '2019指数', 'legends': ['2019指数']},
                   {'name': '2020指数', 'legends': ['2020指数']}],
        'evidence': [{'chunk_id': 'chunk', 'quote': fragments[0]['content']}]}]}
    charts, failed = reports.checked_extraction(document, fragments)
    assert not failed and charts[0]['legend_semantics'] == 'indicators'
    assert len(charts[0]['series']) == 2 and charts[0]['chart_type'] == 'comparison'


@pytest.mark.parametrize('proposed_page,actual_page', [(9, 2), (9, None), ('2', 2), (None, 2), (True, 2)])
def test_page_mismatch_is_corrected_to_verified_fragment_without_losing_chart(proposed_page, actual_page):
    document = extraction()
    document['charts'][0]['page'] = proposed_page
    charts, failed = reports.checked_extraction(document, batch(page=actual_page))
    assert not failed and len(charts) == 1
    chart = charts[0]
    assert chart['page'] == actual_page and chart['source_index'] == 7
    assert chart['page_label'] == ('第2页' if actual_page == 2 else '片段8')
    assert any('依据位置' in warning for warning in chart['warnings'])


@pytest.mark.parametrize('title', [None, '', 'Fabricated title', 'x' * 121])
def test_missing_or_ungrounded_title_uses_model_original_phrase_then_legend(title):
    document = extraction()
    document['charts'][0]['title'] = title
    document['charts'][0]['title_candidates'] = ['Fabricated short phrase', 'Legend B']
    charts, failed = reports.checked_extraction(document, batch())
    assert not failed and charts[0]['title'] == 'Legend B'
    assert charts[0]['title'] in batch()[0]['content'] and charts[0]['warnings']
    del document['charts'][0]['title_candidates']
    charts, failed = reports.checked_extraction(document, batch())
    assert not failed and charts[0]['title'] == 'Legend A'


def test_original_title_can_be_outside_quote_but_inside_its_verified_fragment():
    document = extraction()
    document['charts'][0]['evidence'][0]['quote'] = 'Legend A kg\nLegend B kg'
    charts, failed = reports.checked_extraction(document, batch())
    assert not failed and charts[0]['title'] == 'Original title'


@pytest.mark.parametrize('change', ['foreign_chunk', 'quote_from_other_chunk', 'invented_period_legend'])
def test_title_and_page_tolerance_does_not_weaken_batch_evidence_boundary(change):
    fragments, document = period_extraction()
    row = document['charts'][0]
    row['title'], row['page'] = None, 100
    if change == 'foreign_chunk': row['evidence'][0]['chunk_id'] = 'foreign'
    if change == 'quote_from_other_chunk':
        fragments.append({'id': 'other', 'index': 8, 'page': 2, 'content': 'different original quote'})
        row['evidence'][0]['quote'] = 'different original quote'
    if change == 'invented_period_legend': row['series'][0]['legends'].append('Fabricated period')
    charts, failed = reports.checked_extraction(document, fragments)
    assert not charts and len(failed) == 1 and '依据' in failed[0]['reason']


def test_no_grounded_phrase_or_legend_still_fails_instead_of_inventing_title():
    document = extraction()
    row = document['charts'][0]
    row['title'] = None
    row['title_candidates'] = ['Invented']
    row['series'] = [{'name': 'Invented'}]
    charts, failed = reports.checked_extraction(document, batch())
    assert not charts and len(failed) == 1


def test_nonempty_candidates_can_be_left_unmapped_without_name_based_override():
    charts, _ = reports.checked_extraction(extraction(), batch())
    inventory = reports.series_inventory(charts)['items']
    groups = [(item, ['field']) for item in inventory]
    catalog = {'field': {'name': 'Legend A similar wording, different scope'}}
    document = {'picks': {item['id']: {'series_id': None, 'reason': '统计口径不一致，指标库中未找到'}
                           for item in inventory}}
    mapped = reports.apply_mapping(charts, document, groups, catalog)
    assert all(series['selected_id'] is None and series['candidates'] for series in mapped[0]['series'])
    prompt = reports.mapping_prompt(groups, catalog)
    assert '宁可不选' in prompt and '候选非空也完全允许不选' in prompt


def test_larger_batches_reduce_unlocated_fragment_rounds_and_keep_byte_capacity():
    fragments = [{'id': str(i), 'index': i, 'page': None, 'content': '原' * 350} for i in range(455)]
    batches = reports.report_batches(fragments)
    assert len(batches) == 38  # Earlier six-position cap required 76 batches.
    assert [item for group in batches for item in group] == fragments
    for group in batches:
        assert len(group) <= reports.MAX_BATCH_FRAGMENTS
        assert sum(len(item['content']) for item in group) <= reports.MAX_BATCH_CHARACTERS
        assert len(reports.extraction_prompt(group).encode()) <= reports.PROMPT_BYTES
    largest = batch('原' * reports.MAX_BATCH_CHARACTERS)
    assert len(reports.extraction_prompt(largest).encode()) <= reports.PROMPT_BYTES


def test_expanded_chart_count_keeps_mapping_prompt_within_capacity_after_recall_retreat():
    document = extraction()
    document['charts'] = [deepcopy(document['charts'][0]) for _ in range(reports.MAX_BATCH_CHARTS)]
    charts, failed = reports.checked_extraction(document, batch())
    assert len(charts) == 48 and not failed
    inventory = reports.series_inventory(charts)['items']
    catalog = {f'field-{i}': {'name': f'Original indicator {i}', 'unit': 'kg',
        'frequency': 'weekly', 'sheet': 'Original sheet', 'source_file': 'Original source.xlsx'}
        for i in range(20)}
    source = batch(batch()[0]['content'] + '原' * (reports.MAX_BATCH_CHARACTERS - len(batch()[0]['content'])))
    capacities = [len(reports.mapping_prompt([(item, list(catalog)[:limit]) for item in inventory],
                                            catalog, source).encode())
                  for limit in reports.recall.RECALL_STEPS]
    assert any(size <= reports.PROMPT_BYTES for size in capacities)


@pytest.mark.parametrize('semantics', [None, '', 'unknown'])
def test_missing_or_invalid_legend_semantics_fails_only_that_chart(semantics):
    document = extraction()
    bad = deepcopy(document['charts'][0])
    if semantics is None:
        del bad['legend_semantics']
    else:
        bad['legend_semantics'] = semantics
    document['charts'].insert(0, bad)
    charts, failed = reports.checked_extraction(document, batch())
    assert len(charts) == 1 and charts[0]['id'] == 'c2'
    assert charts[0]['legend_semantics'] == 'indicators'
    assert len(failed) == 1 and '图例语义' in failed[0]['reason']
