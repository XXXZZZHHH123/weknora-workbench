"""Undecidable wording after a source number goes to a person, never to a regex patch."""
from copy import deepcopy

import pytest
from sqlalchemy import select

from tests.test_judgment_generation import generator, endpoint, rows  # noqa: F401 (fixture)
from tests.test_judgment_quote_unit_context import envelope, catalog
from tests.test_judgment_review import update, pick
from tests.test_knowledge_facts import canonical, SERIES, FIRST  # noqa: F401 (fixtures)
from tin.integration.workbench_scope import isolated_session_factory
from tin.judgments.analysis import AnalysisError, validate_analysis
from tin.models import AuditLog
from tin.web import judgment_generation as generation


def outcome(source, quote, unit, value, *, kind='thresholds', others=(), confirmations=None, actor='synthetic'):
    try:
        validate_analysis(envelope(quote, unit, value, kind=kind), source, catalog(unit, *others),
            variety='TEST', actor=actor, number_confirmations=confirmations)
    except AnalysisError as error:
        return error.issues[0]
    return None


# Ordinary clause wording after a quantity. No single word list can decide
# these, so each must reach review rather than a permanent rejection.
REVIEWABLE = [
    ('AAA>19时降低敞口', 'AAA>19时降低敞口', '点', 19, ''),
    ('AAA升至19后降低敞口', 'AAA升至19后降低敞口', '点', 19, ''),
    ('AAA突破19，降低敞口', 'AAA突破19，降低敞口', '点', 19, ''),
    ('库存超过5000吨时减仓', '库存超过5000吨时减仓', '吨', 5000, ''),
    ('库存超过5000吨时减仓', '库存超过5000吨', '吨', 5000, '时减仓'),
    ('价格跌破25万元/吨离场', '价格跌破25万元/吨离场', '元/吨', 250000, ''),
    ('价格在40万附近放量', '价格在40万附近放量', '元/吨', 400000, ''),
    ('AAA>17.5或BBB>1/2', 'AAA>17.5或BBB>1/2', '点', 17.5, ''),
]


@pytest.mark.parametrize('kind', ['thresholds', 'falsifiers'])
@pytest.mark.parametrize('source,quote,unit,value,following', REVIEWABLE)
def test_unclassified_wording_requires_and_accepts_only_an_exact_personal_attestation(
        source, quote, unit, value, following, kind):
    issue = outcome(source, quote, unit, value, kind=kind)
    assert issue['code'] == 'number_review_required'
    subject = issue['number_review']
    assert subject['quote'] == quote and subject['unit'] == unit
    assert subject['values'] == [str(value)] and subject['following'] == following
    if following:
        assert following in issue['message'], 'The reviewer must see source text hidden by a short quote'
    attested = {'synthetic': {**subject, 'confirmed_by': 'synthetic'}}
    assert outcome(source, quote, unit, value, kind=kind, confirmations=attested) is None
    other_user = {'synthetic': {**subject, 'confirmed_by': 'someone-else'}}
    assert outcome(source, quote, unit, value, kind=kind, confirmations=other_user)['code'] == 'number_review_required'
    for key, changed in [('values', ['20']), ('unit', '吨' if unit != '吨' else '点'), ('series_id', 'other'),
                         ('quote', quote + ' '), ('following', following + 'x')]:
        stale = {'synthetic': {**subject, key: changed, 'confirmed_by': 'synthetic'}}
        assert outcome(source, quote, unit, value, kind=kind, confirmations=stale)['code'] == 'number_review_required', key


@pytest.mark.parametrize('kind', ['thresholds', 'falsifiers'])
@pytest.mark.parametrize('source,quote,unit,value,others,code', [
    ('库存10万吨', '库存10万吨', '吨', 10, (), 'number_not_in_source'),
    ('库存19美元', '库存19美元', '元', 19, ('美元',), 'number_not_in_source'),
    ('AAA>19时降低敞口', 'AAA>19时降低敞口', '点', 12345, (), 'number_not_in_source'),
    ('容量超过12安时需要复核。', '容量超过12安', '安', 12, ('安时',), 'unit_context'),
    ('数量超过120kg。', '数量超过12', 'kg', 12, (), 'unit_context'),
    ('数量超过12kg。', '12', '吨', 12, ('kg',), 'unit_context'),
])
def test_definite_number_scale_and_unit_conflicts_cannot_be_attested(source, quote, unit, value, others, code, kind):
    issue = outcome(source, quote, unit, value, kind=kind, others=others)
    assert issue['code'] == code and 'number_review' not in issue
    forged = {'synthetic': {'kind': kind, 'rule_id': 'synthetic', 'series_id': 'synthetic', 'unit': unit,
        'quote': quote, 'following': '', 'values': [str(value)], 'rule': '{}', 'confirmed_by': 'synthetic'}}
    assert outcome(source, quote, unit, value, kind=kind, others=others, confirmations=forged)['code'] == code


def test_strictly_verified_numbers_still_pass_without_any_review():
    assert outcome('AAA>19。', 'AAA>19', '点', 19) is None
    assert outcome('库存超过5000吨，减仓', '库存超过5000吨', '吨', 5000) is None


SOURCE = 'AAA>17.5吨时复核。'


def generate_reviewable(generator, monkeypatch):
    client, headers, _, _, calls = generator
    candidate = {'judgment': {'contradiction': {}, 'thresholds': [{
        'id': 'boundary', 'name': 'Synthetic numeric condition', 'series_id': SERIES,
        'operator': '>', 'value': 17.5, 'type': '观察位', 'horizon': '短期'}]},
        'binding_units': {SERIES: '吨'}, 'rule_quotes': {'boundary': SOURCE}, 'unresolved': []}

    async def complete(scope, model, prompt):
        calls.append({'model': model, 'prompt': prompt})
        if prompt.startswith('research-source-inventory.v1'):
            return {'blocks': [{'id': 's0001', 'disposition': 'research', 'reason': 'Synthetic source'}],
                'items': [{'id': 'i_condition', 'block_ids': ['s0001'], 'quote': SOURCE,
                    'kind': 'threshold', 'source_unit': '吨'}]}
        return {**deepcopy(candidate), 'source_coverage': {'i_condition': '/judgment/thresholds/0'}}

    monkeypatch.setattr(generation, 'completion', complete)
    generated = client.post(endpoint('/generate'), headers=headers, json={
        'model_id': 'synthetic-chat', 'text': SOURCE, 'base_version': 0})
    assert generated.status_code == 200, generated.text
    return client, headers, generated.json()


def test_whole_item_confirmation_attests_the_number_once_and_saves_it(generator, monkeypatch):
    client, headers, preview = generate_reviewable(generator, monkeypatch)
    item = pick(preview, 'thresholds')
    assert item['status'] == 'pending' and item['requires_attention']
    first = update(client, headers, preview, item, series_id=SERIES, related_series_ids=[])
    assert first.status_code == 422, first.text
    detail = first.json()['detail']
    assert detail['code'] == 'number_review' and detail['attention_fields'] == ['condition']
    assert SOURCE in detail['message'] and '17.5（吨）' in detail['message']
    assert rows() == []

    forged = update(client, headers, preview, item, series_id=SERIES, related_series_ids=[],
        number_review_token='0' * 64)
    assert forged.status_code == 422 and forged.json()['detail']['code'] == 'number_review'

    confirmed = update(client, headers, preview, item, series_id=SERIES, related_series_ids=[],
        number_review_token=detail['confirmation_token'])
    assert confirmed.status_code == 200, confirmed.text
    current = confirmed.json()
    assert pick(current, 'thresholds')['status'] == 'confirmed'

    saved = client.post(endpoint('/apply'), headers=headers, json={
        'draft_id': current['draft_id'], 'base_version': 0, 'review_revision': current['review']['revision']})
    assert saved.status_code == 200, saved.text
    assert rows()[0].thresholds[0]['value'] == 17.5
    with isolated_session_factory(17, FIRST)() as session:
        logs = session.scalars(select(AuditLog).where(AuditLog.action == '研判逐项确认')).all()
    attestations = [log.detail['number_confirmation'] for log in logs if log.detail.get('number_confirmation')]
    assert attestations and attestations[-1]['values'] == ['17.5'] and attestations[-1]['quote'] == SOURCE


def test_number_token_is_bound_to_the_current_revision(generator, monkeypatch):
    client, headers, preview = generate_reviewable(generator, monkeypatch)
    item = pick(preview, 'thresholds')
    first = update(client, headers, preview, item, series_id=SERIES, related_series_ids=[])
    token = first.json()['detail']['confirmation_token']
    kept = update(client, headers, preview, item, action='keep_text', series_id=None, related_series_ids=[])
    assert kept.status_code == 200, kept.text
    refreshed = kept.json()
    reused = update(client, headers, refreshed, pick(refreshed, 'thresholds'), series_id=SERIES,
        related_series_ids=[], number_review_token=token)
    assert reused.status_code == 422 and reused.json()['detail']['code'] == 'number_review'


def test_bulk_confirmation_never_attests_numbers(generator, monkeypatch):
    client, headers, preview = generate_reviewable(generator, monkeypatch)
    result = client.post(endpoint('/review/confirm-ready'), headers=headers, json={
        'draft_id': preview['draft_id'], 'revision': preview['review']['revision']})
    assert result.status_code == 200, result.text
    assert pick(result.json(), 'thresholds')['status'] == 'pending'
