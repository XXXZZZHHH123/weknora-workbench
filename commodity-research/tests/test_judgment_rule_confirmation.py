"""The researcher who wrote the logic may confirm a rule as written when it cannot be matched with the source."""
import copy

import pytest

from tests.test_judgment_analysis import CATALOG, SID, TEXT as SOURCE, document as analysis_document
from tests.test_judgment_generation import generator, generate, rows  # noqa: F401
from tests.test_judgment_review import pick, update
from tests.test_knowledge_facts import FIRST, canonical, page  # noqa: F401
from tin.judgments.analysis import AnalysisError, assess_draft, validate_analysis
from tin.web import judgment_generation as generation


def confirm(client, headers, preview, item):
    first = update(client, headers, preview, item)
    assert first.status_code == 422, first.text
    detail = first.json()['detail']
    assert detail['code'] == 'number_review' and '人工确认' in detail['message']
    return detail, update(client, headers, preview, item, number_review_token=detail['confirmation_token'])


@pytest.mark.parametrize(('change', 'reason'), [
    (lambda model: model['rule_quotes'].update(watch='Stocks rise beyond the review line'), '逐字'),
    (lambda model: model['judgment']['thresholds'][0].update(value=15), '数字'),
])
def test_researcher_confirms_a_rule_that_does_not_match_the_source_and_can_save_it(generator, change, reason):
    client, headers, _, response, _ = generator
    change(response)
    preview = generate(client, headers).json()
    item = pick(preview, 'thresholds')
    assert item['requires_attention'] and '核对并确认整条' in item['warnings'][0]
    detail, confirmed = confirm(client, headers, preview, item)
    assert reason in detail['message'] and detail['attention_fields']
    assert confirmed.status_code == 200, confirmed.text
    after = pick(confirmed.json(), 'thresholds')
    assert after['status'] == 'confirmed' and after['manually_confirmed'] is True
    saved = client.post(page(FIRST, '/api/sn/judgment/apply'), headers=headers, json={
        'draft_id': preview['draft_id'], 'base_version': preview['base_version'],
        'review_revision': confirmed.json()['review']['revision']})
    assert saved.status_code == 200, saved.text
    [row] = rows()
    assert row.status == '草稿' and [rule['id'] for rule in row.thresholds] == ['watch']


def test_wrong_confirmation_token_registers_nothing(generator):
    client, headers, _, response, _ = generator
    response['rule_quotes'].update(watch='Stocks rise beyond the review line')
    preview = generate(client, headers).json()
    item = pick(preview, 'thresholds')
    forged = update(client, headers, preview, item, number_review_token='0' * 64)
    assert forged.status_code == 422 and forged.json()['detail']['code'] == 'number_review'
    stored = generation.previews.items[preview['draft_id']][2]
    assert stored['review']['revision'] == preview['review']['revision']
    assert stored['document']['judgment']['thresholds'] == []


def unverified():
    body = analysis_document()
    body['rule_quotes']['watch'] = 'stocks stay elevated'
    with pytest.raises(AnalysisError) as caught:
        validate_analysis(body, SOURCE, CATALOG, variety='TEST', actor='researcher')
    [issue] = caught.value.issues
    assert issue['code'] == 'quote_not_in_source'
    return body, issue['manual_confirmation']


def test_confirmation_is_bound_to_the_researcher_and_the_exact_rule():
    body, subject = unverified()
    confirmations = {'watch': {**subject, 'confirmed_by': 'researcher'}}
    result = validate_analysis(body, SOURCE, CATALOG, variety='TEST', actor='researcher', number_confirmations=confirmations)
    assert [rule.id for rule in result.payload.thresholds] == ['watch']
    with pytest.raises(AnalysisError):
        validate_analysis(body, SOURCE, CATALOG, variety='TEST', actor='someone-else', number_confirmations=confirmations)
    edited = copy.deepcopy(body)
    edited['judgment']['thresholds'][0]['value'] = 13
    with pytest.raises(AnalysisError):
        validate_analysis(edited, SOURCE, CATALOG, variety='TEST', actor='researcher', number_confirmations=confirmations)


def test_confirmation_never_covers_integrity_failures_or_automatic_analysis():
    body, subject = unverified()
    confirmations = {'watch': {**subject, 'confirmed_by': 'researcher'}}
    foreign = copy.deepcopy(body)
    foreign['judgment']['thresholds'][0]['series_id'] = 'kb-' + '9' * 48
    with pytest.raises(AnalysisError) as caught:
        validate_analysis(foreign, SOURCE, CATALOG, variety='TEST', actor='researcher', number_confirmations=confirmations)
    assert caught.value.issues[0]['code'] == 'unknown_series_id'
    with pytest.raises(AnalysisError) as caught:
        validate_analysis(body, SOURCE, [{**CATALOG[0], 'unit': None}], variety='TEST', actor='researcher',
                          number_confirmations=confirmations)
    assert caught.value.issues[0]['code'] in {'unit_metadata_missing', 'unit_mismatch'}
    # Without a researcher's confirmation, automatic analysis keeps the rule pending.
    assessed = assess_draft(body, SOURCE, CATALOG, variety='TEST', actor='researcher')
    assert assessed.result.payload.thresholds == [] and SID in {row['id'] for row in CATALOG}
