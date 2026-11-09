"""A researcher's own words become reviewable card drafts that cite only real, listed references."""
from datetime import date

from fastapi import HTTPException
import pytest

from tests.test_research_strategy_cards import URL, cards  # noqa: F401
from tests.test_strategy_citations import FIELD_VERSION, SID, case, run  # noqa: F401
from tin.web import research_strategy, strategy_citations as citations, strategy_markdown, strategy_text

TEXT = '库存连续两周累库就撤回；指标高于一万二时分批减仓，月底复核。'


def card(**changes):
    value = {'title': '库存累库撤回', 'direction': 'bear', 'trigger': '指标高于一万二', 'response_action': '分批减仓',
             'invalidation': '库存连续两周累库', 'thesis': '', 'watch_for': '', 'review_date': '2030-01-31',
             'judgment': None, 'conditions': [{'ref': 'c2', 'field': 'invalidation'}],
             'indicator_queries': [{'query': 'Synthetic quantity', 'field': 'trigger'}], 'excerpt': '', 'gaps': []}
    value.update(changes)
    return value


@pytest.fixture
def model(monkeypatch):
    calls, replies = [], []

    async def complete(scope, model_id, prompt):
        calls.append({'model': model_id, 'prompt': prompt})
        reply = replies.pop(0)
        if isinstance(reply, Exception):
            raise reply
        return reply
    monkeypatch.setattr(strategy_text, 'completion', complete)
    monkeypatch.setattr(strategy_text, '_today', lambda: date(2030, 1, 15))
    return calls, replies


def draft(case, text=TEXT):
    return run(case, lambda: strategy_text.draft_from_text(case[0], text, 'synthetic-model'))


def test_single_card_cites_only_listed_conditions_and_chosen_catalog_indicators(case, model):
    calls, replies = model
    replies += [{'cards': [card()]}, {'indicators': [{'card': 0, 'ref': 'i1', 'field': 'trigger'}]}]
    [result] = draft(case)['cards']
    body = result['body']['card']
    assert body['title'] == '库存累库撤回' and body['direction'] == 'bear' and body['review_date'] == '2030-01-31'
    assert body['original_text'] == TEXT and result['body']['acknowledge_observation'] is False
    assert body['judgment_reference'] == {'kind': 'judgment', 'id': '17', 'revision': 4}
    kinds = [(item['kind'], item['field']) for item in body['citations']]
    assert kinds == [('judgment_condition', 'invalidation'), ('indicator', 'trigger')]
    assert body['citations'][0]['rule_id'] == 'rule-f' and body['citations'][1]['series_id'] == SID
    assert any('按所引研判条件' in note for note in result['notes'])
    assert any('请核对是否就是你指的指标' in note for note in result['notes'])
    # The model saw the complete text, today's date and the real listed condition; then the catalog candidates.
    assert TEXT in calls[0]['prompt'] and '2030-01-15' in calls[0]['prompt'] and 'Synthetic accumulation' in calls[0]['prompt']
    assert 'Synthetic quantity' in calls[1]['prompt'] and len(calls) == 2
    # The draft passes the same model and citation resolution the save path uses.
    research_strategy.ResearchStrategyCard.model_validate(body)
    resolved = run(case, lambda: citations.resolve_citations(case[0], body['citations']))
    assert [item['kind'] for item in resolved] == ['judgment_condition', 'indicator']


def test_unlisted_references_bad_dates_and_missing_title_are_notes_never_bindings(case, model):
    calls, replies = model
    replies += [{'cards': [card(title='', review_date='明天', judgment='j9', conditions=[{'ref': 'c99', 'field': 'trigger'}],
                                indicator_queries=[], trigger='', response_action='', gaps=['原话与 j1 的“c2”方向相反，请确认'])]}]
    [result] = draft(case)['cards']
    body = result['body']['card']
    assert 'citations' not in body and 'judgment_reference' not in body and 'review_date' not in body
    assert body['title'] == TEXT[:30] and result['body']['acknowledge_observation'] is True
    notes = ' '.join(result['notes'])
    for text in ('c99', 'j9', '复核日期', '方案名称', '适用 / 触发', '观察方案'):
        assert text in notes
    assert '原话与 “Synthetic judgment · 2030-01-01” 的“Synthetic accumulation”方向相反' in notes, notes
    assert '““' not in notes and '””' not in notes
    assert len(calls) == 1, 'no indicator lookup without queries'


def test_several_cards_become_an_importable_markdown_document(case, model):
    _, replies = model
    text = '方案一：库存累库就撤回。方案二：价格高于一万二就减仓。'
    replies += [{'cards': [card(title='方案一', excerpt='方案一：库存累库就撤回。', indicator_queries=[]),
                           card(title='方案二', excerpt='不在原文里的片段', indicator_queries=[], conditions=[])]}]
    result = draft(case, text)
    assert [row['body']['card']['original_text'] for row in result['cards']] == ['方案一：库存累库就撤回。', text]
    parsed = strategy_markdown.parse(result['markdown'])
    assert [row['title'] for row in parsed] == ['方案一', '方案二'] and all(row['id'] is None for row in parsed)
    assert parsed[0]['citations'][0]['rule_id'] == 'rule-f'
    assert parsed[0]['meta']['judgment_reference'] == {'kind': 'judgment', 'id': '17', 'revision': 4}
    # The second card's notes travel with it as reminders shown when the document is checked.
    assert any(warning.startswith('待核对：未能定位这份方案对应的原话片段') for warning in parsed[1]['warnings'])
    assert not any('未识别' in warning for row in parsed for warning in row['warnings'])


def test_model_variations_in_shape_are_read_but_identifiers_are_still_checked(case, model):
    calls, replies = model
    replies += [{'cards': [{'title': '只给了部分字段', 'direction': '偏空', 'invalidation': '库存连续两周累库', 'confidence': 0.8,
                            'conditions': [{'ref': 'c2', 'field': 'invalidation'}, {'field': 'trigger'}, 'c1'],
                            'indicator_queries': [{'name': 'Synthetic quantity', 'field': 'trigger'}]}]},
                {'indicators': [{'card': 0, 'ref': 'i1', 'field': 'trigger', 'reason': 'same series'}, {'ref': 'i9'}]}]
    [result] = draft(case)['cards']
    body = result['body']['card']
    assert body['direction'] == 'bear' and body['trigger'] == '' and body['thesis'] == ''
    assert [(item['kind'], item['field']) for item in body['citations']] == [('judgment_condition', 'invalidation'), ('indicator', 'trigger')]
    notes = ' '.join(result['notes'])
    assert '2 条引用或检索词格式不完整' in notes and 'i9' in notes and '适用 / 触发' in notes
    assert 'Synthetic quantity' in calls[1]['prompt']


def test_malformed_model_output_keeps_the_text_and_reports_a_retry(case, model):
    _, replies = model
    replies += [{'cards': 'not a list of cards'}]
    with pytest.raises(HTTPException) as caught:
        draft(case)
    assert caught.value.status_code == 502 and '原文保持不变' in caught.value.detail


def test_endpoint_requires_writing_access_and_returns_drafts_without_saving(cards, monkeypatch):
    client, _ = cards
    async def fake(scope, text, model_id):
        return {'cards': [{'body': {'card': {'title': text}}, 'notes': []}]}
    monkeypatch.setattr(strategy_text, 'draft_from_text', fake)
    body = {'text': '一句话方案', 'model_id': 'synthetic-model'}
    assert client.post(URL + '/draft-from-text', json=body, headers={'x-role': 'viewer', 'x-can-edit': 'false'}).status_code == 403
    response = client.post(URL + '/draft-from-text', json=body)
    assert response.status_code == 200 and response.json()['cards'][0]['body']['card']['title'] == '一句话方案'
    assert client.post(URL + '/draft-from-text', json={'text': 'x' * 6001, 'model_id': 'm'}).status_code == 422
    assert client.get(URL).json()['total'] == 0


FOLLOWED = {'ref': 's1', 'title': '已有的低吸方案', 'direction': '区间观察', 'review_date': '2030-01-20',
            'reference': {'kind': 'strategy', 'id': 'research:card-1', 'revision': 2}}


def test_vague_pointers_resolve_to_listed_strategy_files_and_verbatim_excerpts(case, model, monkeypatch):
    calls, replies = model
    monkeypatch.setattr(strategy_text, '_strategies', lambda scope: [FOLLOWED])
    text = '参考之前那份低吸方案，按那份合成资料里讲 source 的那段做依据，再挂上默认库那份原始数据备查。'
    replies += [{'cards': [card(strategy='s1', judgment='j1', indicator_queries=[], conditions=[],
                                files=[{'ref': 'f1', 'field': 'thesis', 'excerpt': '讲 source 的那段'},
                                       {'ref': 'f2', 'field': 'watch_for', 'excerpt': ''}])]},
                {'passage': 'source text'}]
    [result] = draft(case, text)['cards']
    body = result['body']['card']
    assert body['reference'] == FOLLOWED['reference']
    assert body['judgment_reference'] == {'kind': 'judgment', 'id': '17', 'revision': 4}
    files = [(item['knowledge_id'], item['field']) for item in body['citations'] if item['kind'] == 'knowledge_file']
    assert files == [('file-a', 'thesis'), ('default-file', 'watch_for'), ('file-a', 'original_text')]
    assert body['original_text'] == text + '\n\n【摘自《file-a.md》】\nsource text'
    notes = ' '.join(result['notes'])
    assert '参考策略设为“已有的低吸方案”' in notes and '已从《file-a.md》摘录原文' in notes and '请核对是否就是你指的文件' in notes
    # The model saw the listed strategy and files, then the verified file text for the excerpt only.
    assert '已有的低吸方案' in calls[0]['prompt'] and 'file-a.md' in calls[0]['prompt'] and 'default-file.json' in calls[0]['prompt']
    assert len(calls) == 2 and 'Synthetic source text' in calls[1]['prompt'] and '讲 source 的那段' in calls[1]['prompt']
    research_strategy.ResearchStrategyCard.model_validate(body)
    resolved = run(case, lambda: citations.resolve_citations(case[0], body['citations']))
    assert [item['kind'] for item in resolved] == ['knowledge_file'] * 3


def test_an_excerpt_is_used_only_when_verbatim_and_only_from_readable_text(case, model):
    calls, replies = model
    case[1]['add_file']('sheet', raw=b'PK\x03\x04binary', kind='xlsx')
    replies += [{'cards': [card(indicator_queries=[], conditions=[],
                                files=[{'ref': 'f1', 'field': 'thesis', 'excerpt': '那段'}, {'ref': 'f2', 'field': 'thesis', 'excerpt': '表格里那部分'}])]},
                {'passage': 'paraphrased source wording'}]
    [result] = draft(case)['cards']
    body = result['body']['card']
    assert body['original_text'] == TEXT
    assert [item['field'] for item in body['citations'] if item['kind'] == 'knowledge_file'] == ['thesis', 'thesis']
    notes = ' '.join(result['notes'])
    assert '与文件原文不一致，未采用' in notes and '《sheet.xlsx》不是能读取正文的文件' in notes
    assert len(calls) == 2, 'an unreadable file never reaches the model'


def test_when_files_are_too_many_to_list_the_model_names_searches_and_chooses(case, model, monkeypatch):
    calls, replies = model
    monkeypatch.setattr(strategy_text, 'MAX_LISTED_FILES', 1)
    replies += [{'cards': [card(indicator_queries=[], conditions=[], files=[{'query': 'file-a', 'field': 'thesis', 'excerpt': ''},
                                                                            {'ref': 'f9', 'field': 'thesis'}])]},
                {'indicators': [], 'files': [{'card': 0, 'ref': 'g1', 'field': 'thesis'}]}]
    [result] = draft(case)['cards']
    assert [item['knowledge_id'] for item in result['body']['card']['citations'] if item['kind'] == 'knowledge_file'] == ['file-a']
    assert '文件较多，未在此列出' in calls[0]['prompt'] and 'file-a.md' not in calls[0]['prompt']
    assert 'file-a.md' in calls[1]['prompt'] and '候选文件' in calls[1]['prompt']


def test_wording_that_only_partly_matches_the_catalog_still_reaches_the_model(case, model):
    calls, replies = model
    other = 'kb-' + 'b' * 48
    case[1]['fields'].append({'series_id': other, 'metric': 'Cotton yarn spot processing margin', 'unit': 'yuan/t',
        'frequency': 'daily', 'sheet': 'Yarn margin', 'source_file': 'source.xlsx', 'knowledge_base_id': 'library-default',
        'source_binding_id': 'binding-default', 'source_version': FIELD_VERSION})
    # "spinning margin" is not a substring of the field name, so a contains-every-word search found nothing.
    replies += [{'cards': [card(indicator_queries=[{'query': 'spinning margin', 'field': 'trigger'}])]},
                {'indicators': [{'card': 0, 'ref': 'i1', 'field': 'trigger'}]}]
    [result] = draft(case)['cards']
    assert 'Cotton yarn spot processing margin' in calls[1]['prompt'] and 'Yarn margin' in calls[1]['prompt']
    indicator = [item for item in result['body']['card']['citations'] if item['kind'] == 'indicator']
    assert [item['series_id'] for item in indicator] == [other]
