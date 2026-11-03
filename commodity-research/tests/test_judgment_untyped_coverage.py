"""Independent model classifications may label a pending item without duplication."""
from copy import deepcopy

from tests.test_judgment_business_conditions import CATALOG, QUOTE, SOURCE, document
from tin.judgments.analysis import assess_draft, unique_condition_kinds
from tin.judgments.coverage import retain_coverage, source_blocks, validate_inventory


def test_source_inventory_type_preserves_one_existing_pending_item_and_raw_audit():
    raw = document()
    raw['unresolved'] = [{'section': 'Research observation', 'text': QUOTE,
        'reason': 'Requires review', 'business_condition': QUOTE, 'rule_kind': None}]
    blocks = source_blocks(SOURCE)
    inventory = validate_inventory({'blocks': [
        {'id': block['id'], 'disposition': 'research', 'reason': 'Source observation'} for block in blocks],
        'items': [{'id': 'observation', 'block_ids': [block['id'] for block in blocks],
                   'quote': QUOTE, 'kind': 'falsifier', 'source_unit': None}]}, SOURCE)
    original = deepcopy(raw)
    assessed = assess_draft(raw, SOURCE, CATALOG, variety='EXAMPLE', actor='owner',
                            condition_kinds=unique_condition_kinds(inventory['items']))
    retained, coverage = retain_coverage(assessed, inventory, {'observation': '/unresolved/0'},
                                       SOURCE, CATALOG, variety='EXAMPLE', actor='owner')
    assert coverage['retained'] == 0
    assert len(retained.document['unresolved']) == 1
    assert retained.document['unresolved'][0]['rule_kind'] == 'falsifier'
    assert retained.document['judgment']['falsifiers'] == []
    assert retained.candidate_document == original and raw == original


def test_conflicting_inventory_kinds_do_not_choose_a_condition_type():
    raw = document()
    raw['unresolved'] = [{'section': 'Research observation', 'text': QUOTE,
        'reason': 'Requires review', 'business_condition': QUOTE, 'rule_kind': None}]
    kinds = unique_condition_kinds([{'quote': QUOTE, 'kind': 'threshold'},
                                    {'quote': QUOTE, 'kind': 'falsifier'}])
    assessed = assess_draft(raw, SOURCE, CATALOG, variety='EXAMPLE', actor='owner', condition_kinds=kinds)
    assert assessed.document['unresolved'][0]['rule_kind'] is None
    assert QUOTE in assessed.document['unresolved'][0]['text']
    assert not assessed.document['judgment']['falsifiers']
