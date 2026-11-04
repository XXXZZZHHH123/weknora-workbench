"""Synthetic research logic only: an elided quote is accepted only as the contiguous source span it abbreviates."""
import copy

import pytest

from tin.judgments.analysis import (ELIDED_SPAN_CHARS, assess_draft, restore_elided_quote, validate_analysis)
from tin.web.judgment_review import build_document, initialize_review

SID = 'kb-' + '7' * 48
CATALOG = [{'id': SID, 'name': 'Measured quantity', 'unit': 'kg', 'frequency': 'day',
            'knowledge_base_id': 'library', 'source_kind': 'structured'}]
TEXT = ('Production contraction supports prices. Stocks above 12 kg show strong restocking interest, '
        'downstream buyers stay active, so that level is the entry zone. Weak orders constrain them.')
ELIDED = 'Stocks above 12 kg show strong restocking interest...that level is the entry zone'
SPAN = 'Stocks above 12 kg show strong restocking interest, downstream buyers stay active, so that level is the entry zone'


def document(quote=ELIDED, value=12):
    return {'judgment': {'contradiction': {'bull': {'claim': 'Production contraction', 'evidence': [
                {'desc': 'Monitor available quantity', 'series_id': SID}]},
                'bear': {'claim': 'Weak orders', 'evidence': []}},
            'thresholds': [{'id': 'entry', 'name': 'Entry zone', 'series_id': SID,
                'operator': '>', 'value': value, 'type': '观察位', 'horizon': '短期'}]},
            'binding_units': {SID: 'kg'}, 'rule_quotes': {'entry': quote}, 'unresolved': []}


@pytest.mark.parametrize('quote,expected', [
    (ELIDED, SPAN),
    ('Stocks above 12 kg show strong restocking interest…that level is the entry zone', SPAN),
    ('Stocks above 12 kg show strong restocking interest …… that level is the entry zone', SPAN),
    ('Stocks above 12 kg...downstream buyers...the entry zone', 'Stocks above 12 kg show strong restocking interest, '
     'downstream buyers stay active, so that level is the entry zone'),
    (SPAN, None),                                                   # already verbatim: nothing to restore
    ('Stocks above 12 kg...the level is invented', None),            # a fragment absent from the source
    ('that level is the entry zone...Stocks above 12 kg', None),     # fragments out of source order
    ('...that level is the entry zone', None),                       # an empty fragment proves nothing
    ('Stocks above 12 kg', None),                                    # no ellipsis and not elided
])
def test_restore_requires_every_fragment_verbatim_and_in_order(quote, expected):
    assert restore_elided_quote(quote, TEXT) == expected


def test_restore_picks_the_shortest_span_and_refuses_distant_fragments():
    text = 'A 5 kg note. ' + 'filler ' * 10 + 'A 5 kg note, then the close.'
    assert restore_elided_quote('A 5 kg note...the close', text) == 'A 5 kg note, then the close'
    far = 'Begin 5 kg. ' + 'x' * ELIDED_SPAN_CHARS + ' End.'
    assert restore_elided_quote('Begin 5 kg...End', far) is None


def test_elided_quote_rule_is_verified_on_the_restored_span_and_the_audit_keeps_the_model_quote():
    raw = document()
    before = copy.deepcopy(raw)
    assessed = assess_draft(raw, TEXT, CATALOG, variety='TEST', actor='r')
    assert [rule.id for rule in assessed.result.payload.thresholds] == ['entry']
    assert assessed.result.rule_quotes == {'entry': SPAN}
    assert any(item['code'] == 'quote_elision_restored' and item['severity'] == 'normalized' for item in assessed.issues)
    assert assessed.candidate_document['rule_quotes'] == {'entry': ELIDED} and raw == before
    validate_analysis(assessed.document, TEXT, CATALOG, variety='TEST', actor='r')


def test_restoration_never_licenses_a_number_the_passage_does_not_state():
    assessed = assess_draft(document(value=99999), TEXT, CATALOG, variety='TEST', actor='r')
    assert assessed.result.payload.thresholds == []
    assert any(item.get('rule_id') == 'entry' and item['code'] == 'number_not_in_source' for item in assessed.issues)


def test_fabricated_fragment_still_quarantines_the_rule():
    assessed = assess_draft(document('Stocks above 12 kg...an invented entry zone'), TEXT, CATALOG, variety='TEST', actor='r')
    assert assessed.result.payload.thresholds == []
    assert any(item['code'] == 'quote_not_in_source' for item in assessed.issues)


def test_review_card_cites_the_restored_passage_and_confirmation_rebuilds_from_it():
    assessed = assess_draft(document(), TEXT, CATALOG, variety='TEST', actor='r')
    preview = {'text': TEXT, 'variety': 'TEST', 'written_at': '2030-01-02', 'source_coverage': {'items': []}}
    initialize_review(preview, assessed, CATALOG)
    assert preview['review']['candidate']['rule_quotes'] == {'entry': ELIDED}
    [item] = [row for row in preview['review']['items'] if row['kind'] == 'thresholds']
    assert item['source_quote'] == SPAN
    item['status'] = 'confirmed'
    rebuilt = build_document(preview, CATALOG)
    assert rebuilt['rule_quotes'] == {'entry': SPAN}
    validate_analysis(rebuilt, TEXT, CATALOG, variety='TEST', actor='r')
