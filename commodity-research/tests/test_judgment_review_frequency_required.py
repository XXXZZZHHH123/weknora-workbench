"""Frequency capability must not turn every missing cadence into a blocker."""
import pytest

from tests.test_judgment_review_issue_scope import preview, CATALOG, SCOPE
from tin.web import judgment_review as review
from tin.judgments.analysis import assess_draft
from fastapi import HTTPException


def threshold(preview):
    response = review.review_response(SCOPE, 'synthetic-private-preview', preview, CATALOG)
    return next(item for section in response['sections'] for item in section['items']
                if item['kind'] == 'thresholds')


def test_fixed_level_without_source_frequency_does_not_require_frequency_confirmation(preview):
    item = threshold(preview)
    assert item['caliber']['can_declare_frequency'] is True
    assert item['can_confirm'] is True
    assert item['caliber']['frequency_required'] is False


@pytest.mark.parametrize('code,required', [
    ('period_frequency', True), ('moving_average_frequency', True),
    ('number_not_in_source', False), ('unit_metadata_missing', False),
])
def test_frequency_requirement_uses_only_the_current_finite_validator_diagnostics(preview, monkeypatch, code, required):
    monkeypatch.setattr(review, 'confirmation_issues',
        lambda *_: [{'code': code, 'message': 'Synthetic validator result'}])
    assert threshold(preview)['caliber']['frequency_required'] is required


def test_missing_unit_does_not_mask_the_same_rules_missing_weekly_frequency():
    catalog = [{'id': 'synthetic-stock', 'name': 'Synthetic stock', 'unit': None, 'frequency': None}]
    source = 'Synthetic stock连续2周增加。'
    document = {'judgment': {'contradiction': {}, 'falsifiers': [{
        'id': 'stock-change', 'desc': 'Synthetic stock observation', 'series_id': 'synthetic-stock',
        'expression': 'change > 0', 'unit_confirmed': 'kg', 'consecutive_periods': 2, 'side': 'bull'}]},
        'binding_units': {'synthetic-stock': None}, 'rule_quotes': {'stock-change': source}, 'unresolved': []}
    assessment = assess_draft(document, source, catalog, variety='UNIT', actor=SCOPE.identity.user_id)
    value = {'text': source, 'variety': 'UNIT', 'written_at': '2033-01-02', 'fact_version': 'synthetic-version'}
    review.initialize_review(value, assessment, catalog)
    response = review.review_response(SCOPE, 'synthetic-private-preview', value, catalog)
    item = next(item for section in response['sections'] for item in section['items'] if item['kind'] == 'falsifiers')
    assert item['caliber']['can_declare'] and item['caliber']['can_declare_frequency']
    assert item['caliber']['frequency_required'] is True and item['can_confirm'] is False


@pytest.mark.parametrize('code,required', [('period_frequency', True), ('moving_average_frequency', True),
                                         ('unit_metadata_missing', False)])
def test_failed_update_reports_its_current_frequency_requirement(code, required):
    with pytest.raises(HTTPException) as error:
        review.reject_fields(['binding'], [{'code': code, 'message': 'Synthetic validator result'}])
    assert error.value.status_code == 422
    assert error.value.detail['frequency_required'] is required
