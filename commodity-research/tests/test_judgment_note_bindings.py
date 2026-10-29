"""Manual descriptive references are real scoped IDs, never executable rules."""
from datetime import date

import pytest
from sqlalchemy import select

from tin.judgments.analysis import AnalysisError, assess_draft, validate_analysis
from tin.judgments.service import JudgmentError, _refs, save_version, to_payload
from tin.judgments.markdown import dump_markdown
from tin.models import JudgmentSeriesRef
from tin.schemas.judgment import UnstructuredItem

SID = 'kb-' + '9' * 48
SOURCE = 'Check the observed quantity before the next review.'
CATALOG = [{'id': SID, 'name': 'Observed quantity', 'unit': None}]


def document(sid=SID):
    return {'judgment': {'contradiction': {}}, 'binding_units': {sid: None},
            'rule_quotes': {}, 'unresolved': [{'section': 'Inputs', 'text': SOURCE,
                'reason': 'Manual descriptive reference', 'series_id': sid}]}


def checked(body):
    return validate_analysis(body, SOURCE, CATALOG, variety='TEST', actor='researcher',
                             written_at=date(2030, 1, 1))


def test_old_pending_items_keep_exact_serialized_shape():
    value = {'section': 'Inputs', 'text': SOURCE, 'reason': 'Missing data'}
    assert UnstructuredItem.model_validate(value).model_dump() == value


def test_descriptive_pending_binding_is_in_refs_without_becoming_a_rule():
    result = checked(document())
    assert result.payload.unstructured[0].series_id == SID
    assert ('unstructured', 'note0', SID) in _refs(result.payload)
    assert not result.payload.thresholds and not result.payload.falsifiers
    assert f' · 指标 `{SID}`' in dump_markdown(result.payload)


@pytest.mark.parametrize('sid', ['not-in-catalog', ''])
def test_unknown_or_empty_pending_reference_is_rejected(sid):
    with pytest.raises(AnalysisError) as caught:
        checked(document(sid))
    if sid:
        assert caught.value.issues[0]['code'] == 'unknown_series_id'
    with pytest.raises(AnalysisError):
        assess_draft(document(sid), SOURCE, CATALOG, variety='TEST', actor='researcher')


def test_pending_binding_must_be_in_unit_coverage():
    value = document()
    value['binding_units'] = {}
    with pytest.raises(AnalysisError, match='单位必须覆盖'):
        checked(value)


def test_manually_bound_pending_note_is_persisted_and_checked_on_save(session):
    value = checked(document()).payload
    with pytest.raises(JudgmentError, match='未登记'):
        save_version(session, value, actor='researcher')
    session.info['knowledge_facts'] = True
    session.info['knowledge_facts_catalog'] = {SID: CATALOG[0]}
    row = save_version(session, value, actor='researcher')
    assert row.status == '草稿'
    assert to_payload(row).unstructured[0].series_id == SID
    refs = session.scalars(select(JudgmentSeriesRef).where(
        JudgmentSeriesRef.judgment_id == row.id)).all()
    assert [(ref.ref_type, ref.ref_id, ref.series_id) for ref in refs] == [
        ('unstructured', 'note0', SID)]
