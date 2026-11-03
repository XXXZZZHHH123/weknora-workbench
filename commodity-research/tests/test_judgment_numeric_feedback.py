"""Quantity and period failures point to their actual condition, not the unit form."""
import pytest

from tests.test_judgment_falsifier_quantity_audit import validate
from tin.judgments.analysis import AnalysisError


def test_missing_comparison_value_names_the_value_and_keeps_unit_separate():
    with pytest.raises(AnalysisError) as caught:
        validate('Review the quantity at its next release.')
    issue = caught.value.issues[0]
    assert '10000' in issue['message'] and '吨' in issue['message']
    assert '连续' not in issue['message']
    assert issue['path'].endswith('/expression')


def test_missing_period_evidence_points_to_period_count_instead_of_expression():
    with pytest.raises(AnalysisError) as caught:
        validate('Quantity >= 10,000吨.', periods=2)
    issue = caught.value.issues[0]
    assert '连续2期' in issue['message'] and '比较值' not in issue['message']
    assert issue['path'].endswith('/consecutive_periods')


@pytest.mark.parametrize('original', ['0.1万吨', '3万吨', '3 万吨', '百万吨', '一万三千吨'])
def test_shortened_implicit_quantity_cannot_change_the_original_amount(original):
    from datetime import date
    from tests.test_judgment_falsifier_quantity_audit import document, declaration, SID, ACTOR
    body = document('万吨')
    with pytest.raises(AnalysisError):
        from tin.judgments.analysis import validate_analysis
        validate_analysis(body, original, [{'id': SID, 'name': 'Synthetic quantity', 'unit': None}],
            variety='UNIT', actor=ACTOR, written_at=date(2033, 1, 2),
            caliber_confirmations={'quantity_review': declaration()})


def test_complete_implicit_scaled_quantity_keeps_its_exact_rule_value():
    result = validate('Quantity >= 万吨.')
    assert result.payload.falsifiers[0].expression == 'value >= 10000'
    assert result.payload.falsifiers[0].unit_confirmed == '吨'


def test_an_ordinary_word_opening_does_not_become_a_quantity_prefix():
    from datetime import date
    from tests.test_judgment_falsifier_quantity_audit import document, declaration, SID, ACTOR
    from tin.judgments.analysis import validate_analysis
    quote = '万一数量达到10,000吨.'
    result = validate_analysis(document(quote), '1.' + quote,
        [{'id': SID, 'name': 'Synthetic quantity', 'unit': None}],
        variety='UNIT', actor=ACTOR, written_at=date(2033, 1, 2),
        caliber_confirmations={'quantity_review': declaration()})
    assert result.payload.falsifiers[0].expression == 'value >= 10000'
