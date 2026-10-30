"""Original units follow every exact occurrence of a covered source item."""
import pytest

from tin.web.judgment_review import source_unit_for_item


QUOTE = 'Synthetic quantity in kg per reporting observation.'
PATH = '/judgment/contradiction/bull/evidence/1'
ITEM = {'section': 'contradiction', 'path': ['contradiction', 'bull', 'evidence', 1]}


def preview(*coverage):
    return {'text': QUOTE, 'source_coverage': {'items': list(coverage)}}


@pytest.mark.parametrize('field', ['candidate_paths', 'paths'])
def test_unit_applies_to_each_exact_multi_occurrence_path(field):
    value = {'quote': QUOTE, 'source_unit': 'kg', 'path': '/judgment/thresholds/0',
        'candidate_path': '/judgment/thresholds/0', field: ['/judgment/thresholds/0', PATH]}
    assert source_unit_for_item(preview(value), ITEM) == 'kg'


def test_multi_occurrence_paths_do_not_match_prefixes_or_unrelated_items():
    value = {'quote': QUOTE, 'source_unit': 'kg', 'candidate_paths': [PATH + '/note', PATH + '0']}
    assert source_unit_for_item(preview(value), ITEM) is None


@pytest.mark.parametrize('damage', ['unit', 'quote'])
def test_multi_occurrence_units_still_require_verbatim_original_evidence(damage):
    value = {'quote': QUOTE, 'source_unit': 'kg', 'candidate_paths': [PATH]}
    value['source_unit' if damage == 'unit' else 'quote'] = 'Invented original'
    assert source_unit_for_item(preview(value), ITEM) is None


def test_conflicting_explicit_units_remain_unknown_and_single_path_is_compatible():
    first = {'quote': QUOTE, 'source_unit': 'kg', 'candidate_path': PATH}
    assert source_unit_for_item(preview(first), ITEM) == 'kg'
    second = {'quote': QUOTE, 'source_unit': 'reporting observation', 'paths': [PATH]}
    assert source_unit_for_item(preview(first, second), ITEM) is None
