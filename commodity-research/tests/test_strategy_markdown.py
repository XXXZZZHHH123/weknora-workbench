"""Strategy cards round-trip through one Markdown document and import through the card save path."""
from tests.test_research_strategy_cards import URL, cards, entry  # noqa: F401
from tin.web import strategy_markdown


def save(client, **changes):
    response = client.post(URL, json=entry(**changes))
    assert response.status_code == 201, response.text
    return response.json()


def exported(client):
    response = client.get(URL + '/markdown')
    assert response.status_code == 200, response.text
    return response.json()


def check(client, markdown):
    return client.post(URL + '/markdown/preview', json={'markdown': markdown})


def apply(client, markdown):
    return client.post(URL + '/markdown/apply', json={'markdown': markdown})


def test_export_then_import_unchanged_is_a_no_op_and_lists_every_card(cards):
    client, _ = cards
    first = save(client)
    second = save(client, card={**entry()['card'], 'title': 'Second hypothesis', 'direction': 'bull',
        'response_action': 'Reduce exposure in two steps.', 'original_text': '# Heading in the source\n\\# literal'})
    document = exported(client)
    assert document['count'] == 2 and document['can_import'] is True
    text = document['markdown']
    assert '## Second hypothesis' in text and f'<!-- card: {first["id"]} revision: 1 -->' in text
    assert '- 方向：偏多观察' in text and '- 观察方案：是' in text
    assert '\\# Heading in the source' in text and '\\\\# literal' in text
    plan = check(client, text).json()
    assert plan['counts'] == {'create': 0, 'update': 0, 'unchanged': 2, 'error': 0} and plan['can_apply'] is False
    assert all(not row['errors'] for row in plan['cards'])
    refused = apply(client, text)
    assert refused.status_code == 422 and '没有需要导入的修改' in refused.json()['detail']['message']
    stored = client.get(URL + '/' + second['id']).json()['card']
    assert stored['original_text'] == '# Heading in the source\n\\# literal'


def test_edited_document_updates_cards_creates_new_ones_and_leaves_others_untouched(cards):
    client, _ = cards
    kept = save(client, card={**entry()['card'], 'title': 'Not in the document'})
    edited = save(client)
    text = exported(client)['markdown']
    start = text.index('## A testable research hypothesis')
    end = text.find('\n## ', start + 1)
    block = text[start:end if end > 0 else None]
    block = block.replace('Revisit after the next authorized observation.', 'Revisit after two weekly observations.')
    block = block.replace('- 方向：等待验证', '- 方向：区间')
    new_card = ('\n## Fresh card typed by hand\n- 方向：偏空\n- 复核日期：2026/12/31\n\n### 适用 / 触发\nInventory rises twice.\n'
                '\n### 应对措施\nCut the position by half.\n\n### 暂停 / 撤回\nInventory falls again.\n')
    document = '# 策略方案\n\n' + block + new_card
    plan = check(client, document).json()
    rows = {row['title']: row for row in plan['cards']}
    assert rows['A testable research hypothesis']['action'] == 'update'
    assert set(rows['A testable research hypothesis']['changes']) == {'方向', '适用 / 触发'}
    assert rows['Fresh card typed by hand']['action'] == 'create' and plan['untouched'] == 1 and plan['can_apply']
    result = apply(client, document)
    assert result.status_code == 200, result.text
    saved = {row['title']: row for row in result.json()['saved']}
    assert saved['A testable research hypothesis']['revision'] == 2
    assert saved['Fresh card typed by hand']['card']['direction'] == 'bear'
    assert saved['Fresh card typed by hand']['card']['review_date'] == '2026-12-31'
    current = client.get(URL + '/' + edited['id']).json()
    assert current['card']['trigger'] == 'Revisit after two weekly observations.' and current['card']['direction'] == 'range'
    assert current['source_ids'] == ['source-a'], 'linked sources are kept when the document does not list them'
    assert client.get(URL + '/' + kept['id']).json()['revision'] == 1


def test_removing_the_card_comment_imports_a_separate_copy(cards):
    client, _ = cards
    original = save(client)
    text = exported(client)['markdown']
    copy = text.replace(f'<!-- card: {original["id"]} revision: 1 -->\n', '')
    plan = check(client, copy).json()
    assert [row['action'] for row in plan['cards']] == ['create']
    assert apply(client, copy).status_code == 200
    assert client.get(URL + '/' + original['id']).json()['revision'] == 1
    assert client.get(URL).json()['total'] == 2


def test_problems_are_reported_per_card_and_nothing_is_saved(cards):
    client, _ = cards
    original = save(client)
    text = exported(client)['markdown']
    client.patch(URL + '/' + original['id'], json={**entry(), 'revision': 1})
    broken = (text.replace('Revisit after', 'Later revisit after')
              + '\n## Missing pieces\n- 方向：大涨\n- 复核日期：明天\n\n### 应对措施\nSomething.\n'
              + '\n## Unknown id\n<!-- card: does-not-exist revision: 1 -->\n- 复核日期：2026-01-01\n'
              + '\n### 适用 / 触发\nA\n\n### 暂停 / 撤回\nB\n')
    plan = check(client, broken).json()
    rows = {row['title']: row for row in plan['cards']}
    assert any('已在别处更新' in message for message in rows['A testable research hypothesis']['errors'])
    missing = ' '.join(rows['Missing pieces']['errors'])
    assert '方向无法识别' in missing and '复核日期无效' in missing and '适用 / 触发' in missing and '暂停 / 撤回' in missing
    assert any('找不到这份方案' in message for message in rows['Unknown id']['errors'])
    assert plan['can_apply'] is False and plan['counts']['error'] == 3
    refused = apply(client, broken)
    assert refused.status_code == 422 and refused.json()['detail']['preview']['counts']['error'] == 3
    assert client.get(URL).json()['total'] == 1 and client.get(URL + '/' + original['id']).json()['revision'] == 2


def test_hand_written_citations_without_identity_are_reported_not_guessed(cards):
    client, _ = cards
    document = ('## Card with a typed reference\n- 复核日期：2026-01-31\n- 依据研判：研判 v3\n\n### 适用 / 触发\nA\n\n'
                '### 暂停 / 撤回\nB\n\n### 引用\n- 适用 / 触发 · 指标：Some indicator I typed\n')
    plan = check(client, document).json()
    [row] = plan['cards']
    assert row['action'] == 'create' and not row['errors']
    assert any('缺少导出编号' in message for message in row['warnings'])
    assert any('依据研判缺少导出编号' in message for message in row['warnings'])
    assert any('观察方案' in message for message in row['warnings'])
    saved = apply(client, document).json()['saved'][0]['card']
    assert 'citations' not in saved and 'judgment_reference' not in saved


def test_readers_can_export_but_not_import(cards):
    client, _ = cards
    save(client)
    text = exported(client)['markdown']
    reader = {'x-role': 'viewer', 'x-can-edit': 'false'}
    assert client.get(URL + '/markdown', headers=reader).status_code == 200
    assert client.post(URL + '/markdown/preview', json={'markdown': text}, headers=reader).status_code == 403
    assert client.post(URL + '/markdown/apply', json={'markdown': text}, headers=reader).status_code == 403


def test_escaping_round_trips_heading_like_lines_exactly():
    for text in ['# a', '\\# b', '\\\\# c', 'plain', '## d\n### e\nend', '  # indented stays']:
        lines = strategy_markdown._escape(text).split('\n')
        assert not any(line.startswith('#') for line in lines)
        assert '\n'.join(strategy_markdown._unescape(line) for line in lines) == text


def test_citations_and_judgment_reference_round_trip_with_their_identity_and_field():
    citations = [
        {'kind': 'judgment_condition', 'field': 'invalidation', 'id': '17', 'revision': 4, 'rule_kind': 'falsifier',
         'rule_id': 'rule-f', 'snapshot': {'title': 'Synthetic accumulation <!-- tricky'}},
        {'kind': 'indicator', 'field': 'trigger', 'series_id': 'kb-' + 'a' * 48, 'source_version': 'b' * 64,
         'source_binding_id': 'binding-default', 'snapshot': {'title': 'Synthetic quantity'}}]
    card = {'title': 'Round trip', 'direction': 'range', 'review_date': '2030-01-31', 'trigger': 'T', 'invalidation': 'I',
            'response_action': 'R', 'judgment_reference': {'kind': 'judgment', 'id': '17', 'revision': 4}, 'citations': citations}
    text = strategy_markdown.dump([{'id': 'card-1', 'revision': 3, 'card': card}], {('17', 4): '研判 v4 · 偏多'})
    assert '- 依据研判：研判 v4 · 偏多 <!-- judgment: 17@4 -->' in text
    assert '- 暂停 / 撤回 · 研判条件：Synthetic accumulation ‹!-- tricky <!-- cite:' in text
    [parsed] = strategy_markdown.parse(text)
    assert parsed['id'] == 'card-1' and parsed['revision'] == 3 and not parsed['errors'] and not parsed['warnings']
    assert parsed['meta'] == {'direction': 'range', 'review_date': '2030-01-31',
                              'judgment_reference': {'kind': 'judgment', 'id': '17', 'revision': 4}}
    assert parsed['citations'] == [{key: value for key, value in item.items() if key != 'snapshot'} for item in citations]
    assert parsed['sections'] == {'trigger': 'T', 'response_action': 'R', 'invalidation': 'I'}
